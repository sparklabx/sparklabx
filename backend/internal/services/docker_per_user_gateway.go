package services

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// DockerPerUserGateway runs one kernel container per user on the local Docker
// daemon — used for local dev (docker-compose) when you want true per-user
// MinIO IAM isolation without spinning up K8s. Containers join the same
// user-defined network as the backend so DNS resolution by container name works.
//
// Production should use K8sPerUserGateway instead; this gateway exists for
// developer parity with the prod isolation model.
type DockerPerUserGateway struct {
	cfg      DockerPerUserConfig
	httpc    *http.Client // unix-socket transport
	touchMu  sync.Mutex
	touchBuf map[string]time.Time
	stopCh   chan struct{}
}

type DockerPerUserConfig struct {
	Image         string            // kernel image to run
	Network       string            // docker network name backend is on (default: "sparklabx_default")
	IdleTimeout   time.Duration     // reap container after this long idle
	MaxContainers int               // hard cap; rejects spawn beyond
	MinIOEndpoint string            // injected as S3_ENDPOINT env so kernel reaches MinIO
	CredsResolver UserCredsResolver // nil → fall back to root creds via env passthrough
}

func NewDockerPerUserGateway(cfg DockerPerUserConfig) (*DockerPerUserGateway, error) {
	sock := os.Getenv("DOCKER_SOCKET")
	if sock == "" {
		sock = "/var/run/docker.sock"
	}
	if _, err := os.Stat(sock); err != nil {
		return nil, fmt.Errorf("docker socket %s not accessible: %w (mount it into the backend container)", sock, err)
	}
	if cfg.Image == "" {
		cfg.Image = DefaultKernelImage
	}
	if cfg.Network == "" {
		cfg.Network = "sparklabx_default"
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}

	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		},
	}
	g := &DockerPerUserGateway{
		cfg:      cfg,
		httpc:    &http.Client{Transport: transport, Timeout: 30 * time.Second},
		touchBuf: make(map[string]time.Time),
		stopCh:   make(chan struct{}),
	}
	log.Info().Str("image", cfg.Image).Str("network", cfg.Network).
		Dur("idle_timeout", cfg.IdleTimeout).Msg("docker_per_user kernel gateway initialized")

	go g.reaperLoop()
	go g.flushTouchLoop()
	return g, nil
}

func (g *DockerPerUserGateway) Mode() string                  { return "docker_per_user" }
func (g *DockerPerUserGateway) IdleTimeout() time.Duration    { return g.cfg.IdleTimeout }

// dockerContainerName returns a deterministic, DNS-safe container name for a
// user. Truncated SHA1 keeps it short; backend restart still finds it.
func dockerContainerName(userID string) string {
	h := sha1.Sum([]byte(userID))
	return "sparklabx-kernel-" + hex.EncodeToString(h[:6])
}

// Status returns current spawn phase from the DB row.
func (g *DockerPerUserGateway) Status(userID string) (PodStatus, error) {
	if userID == "" {
		return PodStatus{}, nil
	}
	var s PodStatus
	err := database.GetDB().QueryRow(
		`SELECT status, phase_message, pod_url, pod_name FROM user_kernel_pods WHERE user_id = $1`,
		userID,
	).Scan(&s.Phase, &s.Message, &s.URL, &s.PodName)
	if err != nil {
		// No row → no spawn in flight. Return empty (not error).
		return PodStatus{}, nil
	}
	return s, nil
}

// GetGatewayURL returns the container URL. Spawns if not running. Blocks until
// the container's Jupyter port responds (max ~60s).
func (g *DockerPerUserGateway) GetGatewayURL(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("userID empty")
	}
	name := dockerContainerName(userID)
	url := fmt.Sprintf("http://%s:8888", name)

	// Fast path: check DB for ready URL + verify container is up via inspect.
	var dbStatus, dbURL string
	_ = database.GetDB().QueryRow(
		`SELECT status, pod_url FROM user_kernel_pods WHERE user_id = $1`,
		userID,
	).Scan(&dbStatus, &dbURL)
	if dbStatus == PhaseReady && dbURL != "" && g.containerHealthy(ctx, name) {
		return dbURL, nil
	}

	// DB says ready but the container isn't actually serving (Jupyter dead,
	// container exited, etc). EnsureSpawning would normally short-circuit on
	// "container exists + status=ready" and never respawn, leaving the poll
	// loop below to spin out the 60-second timeout. Tear down the stale
	// pair first so EnsureSpawning sees a clean slate and rebuilds.
	if dbStatus == PhaseReady {
		log.Info().Str("user", userID).Str("container", name).Msg("stale ready row with unhealthy container; destroying for respawn")
		_ = g.Destroy(userID)
	}

	// Spawn fresh
	if err := g.EnsureSpawning(userID); err != nil {
		return "", err
	}

	// Poll until ready (or context cancels)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
		if g.containerHealthy(ctx, name) {
			g.updatePhase(userID, PhaseReady, "Kernel ready")
			g.setReadyURL(userID, url, name)
			return url, nil
		}
	}
	return "", fmt.Errorf("kernel container %s did not become ready in 60s", name)
}

// EnsureSpawning creates and starts the user's container if it isn't already
// running. Idempotent — returns nil if the container exists in any phase.
func (g *DockerPerUserGateway) EnsureSpawning(userID string) error {
	if userID == "" {
		return fmt.Errorf("userID empty")
	}
	name := dockerContainerName(userID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// MaxContainers cap
	if g.cfg.MaxContainers > 0 {
		running, err := g.countKernelContainers(ctx)
		if err == nil && running >= g.cfg.MaxContainers {
			return fmt.Errorf("max kernel containers reached (%d)", g.cfg.MaxContainers)
		}
	}

	// If already running, refresh DB row + kick off watcher ONLY if not already
	// ready. Repeated /connect calls would otherwise spawn a watcher goroutine
	// each time, all polling the same container and all logging "ready" — log spam.
	if g.containerExists(ctx, name) {
		var curStatus string
		_ = database.GetDB().QueryRow(
			`SELECT status FROM user_kernel_pods WHERE user_id = $1`, userID,
		).Scan(&curStatus)
		if curStatus == PhaseReady {
			// Already known-ready; no need to re-watch or re-upsert.
			return nil
		}
		g.upsertRow(userID, name, "", PhaseStarting, "Reusing existing container")
		go g.watchUntilReady(userID, name, fmt.Sprintf("http://%s:8888", name))
		return nil
	}

	// Resolve per-user MinIO creds
	var awsKey, awsSecret string
	if g.cfg.CredsResolver != nil {
		ak, sk, err := g.cfg.CredsResolver(userID)
		if err != nil {
			log.Warn().Err(err).Str("user", userID).Msg("CredsResolver failed; using root creds")
		} else {
			awsKey, awsSecret = ak, sk
		}
	}
	// Fall back to root creds via the host backend's env. Only used if IAM is off.
	if awsKey == "" {
		awsKey = os.Getenv("MINIO_ACCESS_KEY")
	}
	if awsSecret == "" {
		awsSecret = os.Getenv("MINIO_SECRET_KEY")
	}

	env := []string{
		"AWS_ACCESS_KEY_ID=" + awsKey,
		"AWS_SECRET_ACCESS_KEY=" + awsSecret,
		"S3_ENDPOINT=" + g.cfg.MinIOEndpoint,
	}

	g.upsertRow(userID, name, "", PhaseSpawning, "Creating kernel container")

	cfg := map[string]any{
		"Image": g.cfg.Image,
		"Env":   env,
		"Labels": map[string]string{
			"sparklabx.kernel": "1",
			"sparklabx.user":   userID,
		},
		"ExposedPorts": map[string]any{"8888/tcp": map[string]any{}},
		"HostConfig": map[string]any{
			"RestartPolicy": map[string]any{"Name": "no"},
			"NetworkMode":   g.cfg.Network,
			"AutoRemove":    false,
		},
	}
	if err := g.containerCreate(ctx, name, cfg); err != nil {
		g.updatePhase(userID, PhaseFailed, err.Error())
		return fmt.Errorf("container create: %w", err)
	}
	if err := g.containerStart(ctx, name); err != nil {
		g.updatePhase(userID, PhaseFailed, err.Error())
		return fmt.Errorf("container start: %w", err)
	}
	g.updatePhase(userID, PhaseStarting, "Container started; waiting for kernel port")

	// Background watcher — polls the kernel /api endpoint until it responds,
	// then flips DB row to ready. FE's SpawnStatus polling sees the transition
	// and stops the spinner without needing a separate GetGatewayURL call.
	url := fmt.Sprintf("http://%s:8888", name)
	go g.watchUntilReady(userID, name, url)
	return nil
}

// watchUntilReady polls the kernel container's /api endpoint until it returns
// 200 (Jupyter ready) or the 90s deadline passes. Independent of any HTTP
// request that triggered the spawn — runs even if the FE disconnects.
func (g *DockerPerUserGateway) watchUntilReady(userID, name, url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}
		if !g.containerHealthy(ctx, name) {
			continue
		}
		// Container running — probe the Jupyter API. Use /api/kernels which is
		// the gateway's REST endpoint; returns 200 + [] when Jupyter is up.
		req, _ := http.NewRequest("GET", url+"/api/kernels", nil)
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			g.setReadyURL(userID, url, name)
			log.Info().Str("user", userID).Str("container", name).Msg("kernel container ready")
			return
		}
	}
	g.updatePhase(userID, PhaseFailed, "Kernel did not become ready in 90s")
}

// Touch refreshes the user's last-used timestamp (buffered 10s flush).
func (g *DockerPerUserGateway) Touch(userID string) {
	if userID == "" {
		return
	}
	g.touchMu.Lock()
	g.touchBuf[userID] = time.Now()
	g.touchMu.Unlock()
}

// Destroy stops + removes the container and clears the DB row.
func (g *DockerPerUserGateway) Destroy(userID string) error {
	if userID == "" {
		return nil
	}
	name := dockerContainerName(userID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	g.updatePhase(userID, PhaseTerminating, "Stopping kernel container")
	_ = g.containerStop(ctx, name)
	_ = g.containerRemove(ctx, name)
	_, _ = database.GetDB().Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, userID)
	return nil
}

// ============================================================
// Docker REST helpers
// ============================================================

func (g *DockerPerUserGateway) dockerReq(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return g.httpc.Do(req)
}

func (g *DockerPerUserGateway) containerExists(ctx context.Context, name string) bool {
	resp, err := g.dockerReq(ctx, "GET", "/containers/"+name+"/json", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// containerHealthy returns true only if Jupyter is actually answering on
// port 8888 — not just that Docker reports the container as Running.
// Docker flips Running=true when the entrypoint process starts; Jupyter
// then needs 5-15s to import deps and bind the port. Treating Running
// alone as healthy let backend hand out a URL that immediately failed
// with EOF/reset on the first /api/kernels call.
func (g *DockerPerUserGateway) containerHealthy(ctx context.Context, name string) bool {
	resp, err := g.dockerReq(ctx, "GET", "/containers/"+name+"/json", nil)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}
	var info struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	dec := json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if dec != nil || !info.State.Running {
		return false
	}

	// Probe Jupyter directly. Short timeout — this runs on the hot path
	// (every connect-kernel call), and a hung Jupyter shouldn't stall the
	// caller. 2s is enough on a healthy container; anything slower is
	// "not ready" and the outer poll loop will try again.
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, "GET",
		fmt.Sprintf("http://%s:8888/api", name), nil)
	if err != nil {
		return false
	}
	pr, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	pr.Body.Close()
	return pr.StatusCode == 200
}

func (g *DockerPerUserGateway) containerCreate(ctx context.Context, name string, body map[string]any) error {
	resp, err := g.dockerReq(ctx, "POST", "/containers/create?name="+name, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 201 {
		return nil
	}
	if resp.StatusCode == 409 {
		// Already exists; treat as success.
		return nil
	}
	msg, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("docker create %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}

func (g *DockerPerUserGateway) containerStart(ctx context.Context, name string) error {
	resp, err := g.dockerReq(ctx, "POST", "/containers/"+name+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 || resp.StatusCode == 304 {
		return nil
	}
	msg, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("docker start %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
}

func (g *DockerPerUserGateway) containerStop(ctx context.Context, name string) error {
	resp, err := g.dockerReq(ctx, "POST", "/containers/"+name+"/stop?t=5", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (g *DockerPerUserGateway) containerRemove(ctx context.Context, name string) error {
	resp, err := g.dockerReq(ctx, "DELETE", "/containers/"+name+"?force=true", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (g *DockerPerUserGateway) countKernelContainers(ctx context.Context) (int, error) {
	resp, err := g.dockerReq(ctx, "GET", `/containers/json?filters={"label":["sparklabx.kernel=1"]}`, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("list containers %d", resp.StatusCode)
	}
	var arr []struct{}
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return 0, err
	}
	return len(arr), nil
}

// ============================================================
// DB row helpers
// ============================================================

func (g *DockerPerUserGateway) upsertRow(userID, name, url, status, msg string) {
	_, err := database.GetDB().Exec(
		`INSERT INTO user_kernel_pods (user_id, pod_name, pod_namespace, pod_url, status, phase_message, created_at, last_used_at)
		 VALUES ($1, $2, 'docker', $3, $4, $5, NOW(), NOW())
		 ON CONFLICT (user_id) DO UPDATE
		 SET pod_name = EXCLUDED.pod_name, pod_url = EXCLUDED.pod_url,
		     status = EXCLUDED.status, phase_message = EXCLUDED.phase_message,
		     last_used_at = NOW()`,
		userID, name, url, status, msg,
	)
	if err != nil {
		log.Error().Err(err).Str("user", userID).Msg("upsert user_kernel_pods")
	}
}

func (g *DockerPerUserGateway) updatePhase(userID, phase, msg string) {
	_, _ = database.GetDB().Exec(
		`UPDATE user_kernel_pods SET status = $1, phase_message = $2 WHERE user_id = $3`,
		phase, msg, userID,
	)
}

func (g *DockerPerUserGateway) setReadyURL(userID, url, name string) {
	_, _ = database.GetDB().Exec(
		`UPDATE user_kernel_pods SET status = $1, pod_url = $2, phase_message = $3, last_used_at = NOW() WHERE user_id = $4`,
		PhaseReady, url, "Kernel ready", userID,
	)
}

// ============================================================
// Background loops
// ============================================================

// reaperLoop does three things every 5 minutes:
//   1. reapIdle      — Destroy containers that have been idle past IdleTimeout.
//   2. reapDead      — Destroy DB rows whose container is in a dead state
//                      (exited / dead / removing / vanished) so the next
//                      connect-kernel call spawns fresh instead of looping.
//   3. sweepOrphans  — Remove containers labeled sparklabx.kernel=1 that
//                      have no DB row (left behind by crashes or manual rm).
func (g *DockerPerUserGateway) reaperLoop() {
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-tick.C:
			g.reapIdle()
			g.reapDead()
			g.sweepOrphans()
		}
	}
}

func (g *DockerPerUserGateway) reapIdle() {
	cutoff := time.Now().Add(-g.cfg.IdleTimeout)
	rows, err := database.GetDB().Query(
		`SELECT user_id FROM user_kernel_pods WHERE last_used_at < $1 AND status = $2`,
		cutoff, PhaseReady,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			users = append(users, id)
		}
	}
	for _, u := range users {
		log.Info().Str("user", u).Msg("reaping idle kernel container")
		_ = g.Destroy(u)
	}
}

// reapDead cleans up DB rows whose container has died but the row still
// claims it's ready. Without this, the user is stuck behind a stale
// "ready" record until they hit /kernel/connect (where Layer C cleans up
// reactively) — fine for active users, broken for users who quit and come
// back hours later.
func (g *DockerPerUserGateway) reapDead() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := database.GetDB().Query(`SELECT user_id, pod_name FROM user_kernel_pods`)
	if err != nil {
		return
	}
	type podRow struct{ userID, podName string }
	var all []podRow
	for rows.Next() {
		var r podRow
		if err := rows.Scan(&r.userID, &r.podName); err == nil {
			all = append(all, r)
		}
	}
	rows.Close()

	for _, r := range all {
		resp, err := g.dockerReq(ctx, "GET", "/containers/"+r.podName+"/json", nil)
		if err != nil {
			continue
		}
		if resp.StatusCode == 404 {
			resp.Body.Close()
			// Container vanished — clear the row so a fresh spawn can happen.
			_, _ = database.GetDB().Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, r.userID)
			log.Info().Str("user", r.userID).Str("pod", r.podName).Msg("reapDead: container gone, cleared row")
			continue
		}
		var info struct {
			State struct {
				Status string `json:"Status"`
			} `json:"State"`
		}
		dec := json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		if dec != nil {
			continue
		}
		switch info.State.Status {
		case "exited", "dead", "removing":
			log.Info().Str("user", r.userID).Str("pod", r.podName).Str("state", info.State.Status).Msg("reapDead: destroying dead container")
			_ = g.Destroy(r.userID)
		}
	}
}

// sweepOrphans removes kernel containers labeled sparklabx.kernel=1 that
// have no matching DB row. These are leftovers from backend crashes that
// happened mid-spawn or mid-destroy. The age filter (1 minute) avoids
// racing with a spawn that just created the container but hasn't inserted
// the DB row yet.
func (g *DockerPerUserGateway) sweepOrphans() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := g.dockerReq(ctx, "GET",
		`/containers/json?all=true&filters={"label":["sparklabx.kernel=1"]}`, nil)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	var list []struct {
		Names   []string `json:"Names"`
		Created int64    `json:"Created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return
	}
	if len(list) == 0 {
		return
	}

	rows, err := database.GetDB().Query(`SELECT pod_name FROM user_kernel_pods`)
	if err != nil {
		return
	}
	tracked := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			tracked[n] = true
		}
	}
	rows.Close()

	ageCutoff := time.Now().Add(-1 * time.Minute).Unix()
	for _, c := range list {
		if len(c.Names) == 0 || c.Created > ageCutoff {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		if tracked[name] {
			continue
		}
		log.Info().Str("container", name).Msg("sweepOrphans: removing untracked kernel container")
		_ = g.containerStop(ctx, name)
		_ = g.containerRemove(ctx, name)
	}
}

// flushTouchLoop batches Touch() calls into a single UPDATE every 10s.
func (g *DockerPerUserGateway) flushTouchLoop() {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-tick.C:
			g.flushTouches()
		}
	}
}

func (g *DockerPerUserGateway) flushTouches() {
	g.touchMu.Lock()
	if len(g.touchBuf) == 0 {
		g.touchMu.Unlock()
		return
	}
	buf := g.touchBuf
	g.touchBuf = make(map[string]time.Time)
	g.touchMu.Unlock()

	db := database.GetDB()
	for u, t := range buf {
		_, _ = db.Exec(`UPDATE user_kernel_pods SET last_used_at = $1 WHERE user_id = $2`, t, u)
	}
}
