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
	"k8s.io/apimachinery/pkg/api/resource"

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

	// usageCache memoizes the (slow, ~1s) Docker stats call per user so
	// several notebook tabs polling /kernel/usage don't each hammer the
	// daemon. Entries are served for usageTTL then refreshed on next read.
	usageMu    sync.Mutex
	usageCache map[string]cachedUsage
}

type cachedUsage struct {
	usage ResourceUsage
	at    time.Time
}

const usageTTL = 2 * time.Second

type DockerPerUserConfig struct {
	Image              string                // kernel image to run
	Network            string                // docker network name backend is on (default: "sparklabx_default")
	IdleTimeout        time.Duration         // reap container after this long idle
	MaxContainers      int                   // hard cap; rejects spawn beyond
	MinIOEndpoint      string                // injected as S3_ENDPOINT env so kernel reaches MinIO
	CredsResolver      UserCredsResolver     // nil → fall back to root creds via env passthrough
	OIDCTokenResolver  UserOIDCTokenResolver // returns the kernel callback token (SPARKLABX_KERNEL_TOKEN); nil → no SSO passthrough
	TrinoURL           string                // injected as TRINO_URL for the trino() helper; empty → not set
	KernelAPIURL       string                // injected as SPARKLABX_API_URL so the kernel can fetch a fresh OIDC token
	ConnectorsManifest string                // injected as SPARKLABX_CONNECTORS (JSON [{id,driver,url}]) for the generic data helpers

	// Per-container limits in k8s quantity format ("500m", "1Gi"). Docker
	// doesn't have a separate "request" concept, so only the limit values
	// apply. Empty → no limit (container can use all host resources).
	CPULimit    string
	MemoryLimit string

	// Resolved at construction: CPULimit converted to nano-CPUs, MemoryLimit
	// to bytes. Kept here so Spawn doesn't reparse on every container create.
	nanoCPUs    int64
	memoryBytes int64
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
	if cfg.CPULimit != "" {
		q, err := resource.ParseQuantity(cfg.CPULimit)
		if err != nil {
			return nil, fmt.Errorf("DockerPerUserConfig.CPULimit %q: %w", cfg.CPULimit, err)
		}
		// k8s quantity "2000m" → 2000 milli → 2_000_000_000 nano-CPUs
		cfg.nanoCPUs = q.MilliValue() * 1_000_000
	}
	if cfg.MemoryLimit != "" {
		q, err := resource.ParseQuantity(cfg.MemoryLimit)
		if err != nil {
			return nil, fmt.Errorf("DockerPerUserConfig.MemoryLimit %q: %w", cfg.MemoryLimit, err)
		}
		cfg.memoryBytes = q.Value()
	}

	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		},
	}
	g := &DockerPerUserGateway{
		cfg:        cfg,
		httpc:      &http.Client{Transport: transport, Timeout: 30 * time.Second},
		touchBuf:   make(map[string]time.Time),
		stopCh:     make(chan struct{}),
		usageCache: make(map[string]cachedUsage),
	}
	log.Info().Str("image", cfg.Image).Str("network", cfg.Network).
		Dur("idle_timeout", cfg.IdleTimeout).Msg("docker_per_user kernel gateway initialized")

	go g.reaperLoop()
	go g.flushTouchLoop()
	return g, nil
}

func (g *DockerPerUserGateway) Mode() string               { return "docker_per_user" }
func (g *DockerPerUserGateway) IdleTimeout() time.Duration { return g.cfg.IdleTimeout }

// dockerContainerName returns a deterministic, DNS-safe container name for a
// user. Truncated SHA1 keeps it short; backend restart still finds it.
func dockerContainerName(userID string) string {
	h := sha1.Sum([]byte(userID))
	return "sparklabx-kernel-" + hex.EncodeToString(h[:6])
}

// hostConfig builds the Docker HostConfig fragment. Resource limits are
// only attached when set (per-spawn spec or operator CPULimit/MemoryLimit) —
// a zero value means "no limit" so existing deployments keep working.
func hostConfig(cfg DockerPerUserConfig, nanoCPUs, memoryBytes int64) map[string]any {
	hc := map[string]any{
		"RestartPolicy": map[string]any{"Name": "no"},
		"NetworkMode":   cfg.Network,
		"AutoRemove":    false,
	}
	if nanoCPUs > 0 {
		hc["NanoCpus"] = nanoCPUs
	}
	if memoryBytes > 0 {
		hc["Memory"] = memoryBytes
	}
	return hc
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
	// Self-heal a stale row: if the row claims a settled phase (terminating /
	// ready) but the container no longer exists — it died out of band (daemon
	// restart, OOM, host reboot) — the row is garbage. Left as-is it pins the
	// user at "terminating"/"connected" forever and blocks reconnect. Clear it
	// so the next connect spawns fresh. We skip the spawning/starting/pulling
	// phases: those run before the container exists, so absence there is normal.
	if (s.Phase == PhaseTerminating || s.Phase == PhaseReady) && s.PodName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if g.containerGone(ctx, s.PodName) {
			log.Info().Str("user", userID).Str("container", s.PodName).Str("phase", s.Phase).
				Msg("stale kernel row (container gone); clearing so reconnect can spawn")
			database.GetDB().Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, userID)
			return PodStatus{}, nil
		}
	}
	return s, nil
}

// containerGone reports whether Docker is CERTAIN the container is absent — a
// 404 from the daemon. A transport error or any non-404 status returns false:
// we must not treat "couldn't reach the daemon" as "container gone", or a
// transient daemon blip would delete the row of a still-running kernel.
func (g *DockerPerUserGateway) containerGone(ctx context.Context, name string) bool {
	resp, err := g.dockerReq(ctx, "GET", "/containers/"+name+"/json", nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 404
}

// containerNanoCPUs reads the container's actual CPU quota from inspect.
// Containers can differ from the configured default since per-spawn presets
// (issue #41), so the static cfg value can't be trusted for usage math.
// Returns 0 (= no limit) when the container is missing or has no quota.
func (g *DockerPerUserGateway) containerNanoCPUs(ctx context.Context, name string) int64 {
	resp, err := g.dockerReq(ctx, "GET", "/containers/"+name+"/json", nil)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0
	}
	var info struct {
		HostConfig struct {
			NanoCpus int64 `json:"NanoCpus"`
		} `json:"HostConfig"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0
	}
	return info.HostConfig.NanoCpus
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
	if err := g.EnsureSpawning(userID, nil); err != nil {
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
//
// spec sets THIS container's CPU/memory limits (issue #41 presets); nil falls
// back to the configured CPULimit/MemoryLimit. Like k8s, the size only takes
// effect on a fresh spawn — an already-running container keeps its limits
// until the kernel is restarted. Docker has no "request" concept, so the spec
// maps to limits only.
func (g *DockerPerUserGateway) EnsureSpawning(userID string, spec *ResourceSpec) error {
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

	// SSO token passthrough: inject a short-lived callback token + the backend
	// URL so the notebook data helpers can fetch a FRESH OIDC token per query
	// (the refresh token stays server-side). Absent for non-SSO logins.
	if g.cfg.OIDCTokenResolver != nil {
		if tok, err := g.cfg.OIDCTokenResolver(userID); err != nil {
			log.Warn().Err(err).Str("user", userID).Msg("OIDCTokenResolver failed; no SSO passthrough")
		} else if tok != "" {
			env = append(env, "SPARKLABX_KERNEL_TOKEN="+tok)
			if g.cfg.KernelAPIURL != "" {
				env = append(env, "SPARKLABX_API_URL="+g.cfg.KernelAPIURL)
			}
		}
	}
	// Default Trino endpoint for the trino() notebook helper (operator-configured).
	if g.cfg.TrinoURL != "" {
		env = append(env, "TRINO_URL="+g.cfg.TrinoURL)
	}
	// Connector manifest ([{id,driver,url}]) for the generic data helpers; each
	// connector gets an alias that fetches a fresh credential per query.
	if g.cfg.ConnectorsManifest != "" {
		env = append(env, "SPARKLABX_CONNECTORS="+g.cfg.ConnectorsManifest)
	}

	// Per-spawn limits: user-picked spec wins over the configured defaults.
	// The spec was already validated by the handler, so a parse failure here
	// just falls back to the defaults instead of failing the spawn.
	nanoCPUs, memoryBytes := g.cfg.nanoCPUs, g.cfg.memoryBytes
	cpuStr, memStr := g.cfg.CPULimit, g.cfg.MemoryLimit
	if spec != nil && spec.CPU != "" && spec.Memory != "" {
		if q, err := resource.ParseQuantity(spec.CPU); err == nil {
			nanoCPUs = q.MilliValue() * 1_000_000
			cpuStr = spec.CPU
		}
		if q, err := resource.ParseQuantity(spec.Memory); err == nil {
			memoryBytes = q.Value()
			memStr = spec.Memory
		}
	}

	g.upsertRow(userID, name, "", PhaseSpawning, "Creating kernel container")
	// Record the limits this container was created with (docker has no
	// requests, so the request columns mirror the limits).
	database.GetDB().Exec(
		`UPDATE user_kernel_pods SET cpu_request = $1, mem_request = $2, cpu_limit = $1, mem_limit = $2 WHERE user_id = $3`,
		cpuStr, memStr, userID,
	)

	cfg := map[string]any{
		"Image": g.cfg.Image,
		"Env":   env,
		"Labels": map[string]string{
			"sparklabx.kernel": "1",
			"sparklabx.user":   userID,
		},
		"ExposedPorts": map[string]any{"8888/tcp": map[string]any{}},
		"HostConfig":   hostConfig(g.cfg, nanoCPUs, memoryBytes),
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

// RecentLogs returns the last tailLines of the container's stdout+stderr so the
// handler can scrape Spark/Coursier dependency-resolution failures (#33). The
// non-TTY Docker log stream is multiplexed (8-byte frame headers); we demux to
// recover clean text.
func (g *DockerPerUserGateway) RecentLogs(ctx context.Context, userID string, tailLines int) (string, error) {
	name := dockerContainerName(userID)
	resp, err := g.dockerReq(ctx, "GET",
		fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d", name, tailLines), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("docker logs %s: status %d", name, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return demuxDockerStream(raw), nil
}

// demuxDockerStream strips the 8-byte frame headers Docker prepends to each
// chunk of a multiplexed (non-TTY) log stream. If the data doesn't look
// framed (TTY containers), it's returned as-is.
func demuxDockerStream(b []byte) string {
	var out []byte
	for len(b) >= 8 {
		// header: [stream(1) 0 0 0 size(4 BE)]
		if b[0] > 2 || b[1] != 0 || b[2] != 0 || b[3] != 0 {
			// Not a frame header — treat the rest as plain text.
			out = append(out, b...)
			break
		}
		size := int(b[4])<<24 | int(b[5])<<16 | int(b[6])<<8 | int(b[7])
		b = b[8:]
		if size > len(b) {
			size = len(b)
		}
		out = append(out, b[:size]...)
		b = b[size:]
	}
	out = append(out, b...)
	return string(out)
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

// dockerStatsJSON is the subset of Docker's /containers/{id}/stats payload we
// need. Field names match the Engine API exactly.
type dockerStatsJSON struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  int64   `json:"total_usage"`
			PercpuUsage []int64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage int64 `json:"system_cpu_usage"`
		OnlineCPUs     int   `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage int64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemCPUUsage int64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage int64 `json:"usage"`
		Limit int64 `json:"limit"`
		Stats struct {
			Cache        int64 `json:"cache"`
			InactiveFile int64 `json:"inactive_file"`
		} `json:"stats"`
	} `json:"memory_stats"`
}

// Usage reports live CPU%/memory for the user's kernel container via Docker's
// stats endpoint. With stream=false the daemon returns a single snapshot that
// already includes precpu_stats, so one call yields a valid CPU delta. Results
// are cached for usageTTL to bound load (the stats call is ~1s).
func (g *DockerPerUserGateway) Usage(ctx context.Context, userID string) (ResourceUsage, error) {
	name := dockerContainerName(userID)

	g.usageMu.Lock()
	if c, ok := g.usageCache[name]; ok && time.Since(c.at) < usageTTL {
		g.usageMu.Unlock()
		return c.usage, nil
	}
	g.usageMu.Unlock()

	resp, err := g.dockerReq(ctx, "GET", "/containers/"+name+"/stats?stream=false", nil)
	if err != nil {
		return ResourceUsage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		// No container (not connected / reaped) — nothing to measure.
		return ResourceUsage{}, ErrUsageUnsupported
	}
	if resp.StatusCode != 200 {
		return ResourceUsage{}, fmt.Errorf("docker stats %s: status %d", name, resp.StatusCode)
	}

	var s dockerStatsJSON
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return ResourceUsage{}, fmt.Errorf("decode docker stats: %w", err)
	}

	// Cores actually consumed in the sample window: (container delta / system
	// delta) * online CPUs — the numerator `docker stats` builds before
	// turning it into a percentage.
	var usedCores float64
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemCPUUsage - s.PreCPUStats.SystemCPUUsage)
	cpus := s.CPUStats.OnlineCPUs
	if cpus == 0 {
		cpus = len(s.CPUStats.CPUUsage.PercpuUsage)
	}
	if cpuDelta > 0 && sysDelta > 0 && cpus > 0 {
		usedCores = (cpuDelta / sysDelta) * float64(cpus)
	}
	// Express as a percentage of the container's OWN quota so 100% = "using
	// the whole limit" (consistent with how RAM is shown vs its limit). With
	// a 2-core limit this caps near 100% instead of the confusing 200% a raw
	// docker-stats percentage would show. The quota is read from the container
	// itself (inspect), not the static config — per-spawn presets (issue #41)
	// mean each container can have its own limit. Falls back to %-of-host when
	// the container has no CPU limit.
	cpuPct := 0.0
	limitCores := float64(g.containerNanoCPUs(ctx, name)) / 1e9
	if limitCores <= 0 {
		limitCores = float64(cpus)
	}
	if limitCores > 0 {
		cpuPct = usedCores / limitCores * 100.0
	}

	// Memory: subtract page cache so the figure reflects the working set (what
	// `docker stats` shows), not cache that the kernel will evict under pressure.
	memUsed := s.MemoryStats.Usage - s.MemoryStats.Stats.InactiveFile
	if memUsed < 0 {
		memUsed = s.MemoryStats.Usage
	}

	usage := ResourceUsage{
		CPUPercent:    cpuPct,
		CPUUsedCores:  usedCores,
		CPULimitCores: limitCores,
		MemUsedBytes:  memUsed,
		MemLimitBytes: s.MemoryStats.Limit,
		MetricsLive:   true, // docker stats is immediate; no scrape lag
	}

	g.usageMu.Lock()
	g.usageCache[name] = cachedUsage{usage: usage, at: time.Now()}
	g.usageMu.Unlock()

	return usage, nil
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
//  1. reapIdle      — Destroy containers that have been idle past IdleTimeout.
//  2. reapDead      — Destroy DB rows whose container is in a dead state
//     (exited / dead / removing / vanished) so the next
//     connect-kernel call spawns fresh instead of looping.
//  3. sweepOrphans  — Remove containers labeled sparklabx.kernel=1 that
//     have no DB row (left behind by crashes or manual rm).
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
		`SELECT user_id, pod_url FROM user_kernel_pods WHERE last_used_at < $1 AND status = $2`,
		cutoff, PhaseReady,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	type victim struct{ userID, podURL string }
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.userID, &v.podURL); err == nil {
			victims = append(victims, v)
		}
	}
	rows.Close()

	db := database.GetDB()
	for _, v := range victims {
		// Same protection as the k8s reaper (issue #44): a closed browser
		// tab freezes last_used_at, but the kernel may still be running a
		// long Spark job. Skip reap if any kernel reports execution_state
		// == "busy" and bump last_used_at so we recheck at half-interval.
		if kernelBusy(v.podURL) {
			log.Info().Str("user", v.userID).Msg("reapIdle: skipping, kernel is busy")
			db.Exec(`UPDATE user_kernel_pods SET last_used_at = $1 WHERE user_id = $2`,
				time.Now().Add(-g.cfg.IdleTimeout/2), v.userID)
			continue
		}
		log.Info().Str("user", v.userID).Msg("reaping idle kernel container")
		_ = g.Destroy(v.userID)
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
