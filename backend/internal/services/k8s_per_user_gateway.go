package services

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/sparklabx/sparklabx/backend/internal/database"
)

// Pod lifecycle phases written to user_kernel_pods.status.
// FE consumes via /spawn-status; reaper / capacity counting also keyed off these.
const (
	PhaseSpawning    = "spawning"    // K8s Create issued, pod not yet scheduled
	PhasePulling     = "pulling"     // node pulling image (the slow case)
	PhaseStarting    = "starting"    // container running, kernel booting
	PhaseReady       = "ready"       // probe green, URL usable
	PhaseTerminating = "terminating" // Destroy in progress; concurrent spawns must wait
	PhaseFailed      = "failed"      // unrecoverable; FE shows error, must retry
)

// Spawn cap: hard wall on total pod startup time. Most spawns complete in 5-15s
// once image is cached on node. Cold pulls of the 2GB kernel image can take 60-90s
// over slow networks. 5min gives generous buffer without hanging FE forever.
const spawnTimeout = 5 * time.Minute

// UserCredsResolver returns per-user MinIO IAM credentials. Called once per
// pod spawn so the kernel inherits the user's scoped policy — true isolation,
// not app-layer-only. Return ("", "", nil) to fall back to root creds (e.g.
// when IAM is not configured).
type UserCredsResolver func(userID string) (accessKey, secretKey string, err error)

// pullSecretRefs returns a single-element ImagePullSecrets slice when name is
// set, or nil to leave the field empty (skips imagePullSecrets entirely).
func pullSecretRefs(name string) []corev1.LocalObjectReference {
	if name == "" {
		return nil
	}
	return []corev1.LocalObjectReference{{Name: name}}
}

// K8sPerUserConfig configures the dynamic per-user gateway.
type K8sPerUserConfig struct {
	Namespace     string            // K8s namespace where kernel pods live
	PodImage      string            // image to run for each user pod
	IdleTimeout   time.Duration     // delete pod after this long without activity
	MaxPods       int               // hard cap on concurrent pods cluster-wide
	PullSecret    string            // optional imagePullSecret name; empty → none
	CredsResolver UserCredsResolver // nil → use root creds from sparklabx-secrets (legacy)

	// Per-pod resource quantities ("500m", "1Gi"). Empty → fall back to
	// defaults (500m/1Gi requests, 2000m/4Gi limits). MustParse'd at spawn,
	// so invalid values panic loudly instead of silently spawning misconfigured pods.
	CPURequest    string
	MemoryRequest string
	CPULimit      string
	MemoryLimit   string
}

// PodStatus is the spawn progress snapshot returned to FE for live UI updates.
type PodStatus struct {
	Phase   string `json:"phase"`             // one of the Phase* constants, or "" if no row
	Message string `json:"message"`           // human-readable; safe to display verbatim
	URL     string `json:"url,omitempty"`     // populated only when phase=ready
	PodName string `json:"pod_name,omitempty"`
}

// K8sPerUserGateway spawns one Jupyter pod per user on demand and reaps idle ones.
//
// Lifecycle (Silver design):
//   1. GetGatewayURL → handles terminating handshake, then watches pod events for
//      phase transitions, updating DB so FE polling sees live progress.
//   2. Touch → buffered last_used_at update (batched 10s) to avoid DB hammer.
//   3. Destroy → marks terminating, deletes pod gracefully, background goroutine
//      removes DB row when pod is fully gone.
//   4. Reaper → deletes idle pods + reconciles stuck rows (failed spawns, etc).
//
// Pod naming: sha1(user_id)[:6] → "kernel-<12 hex>" (DNS-safe, deterministic
// so backend restarts still find existing pods).
type K8sPerUserGateway struct {
	cfg      K8sPerUserConfig
	client   *kubernetes.Clientset
	touchMu  sync.Mutex
	touchBuf map[string]time.Time
	stopCh   chan struct{}

	// usageCache memoizes Usage() per user so multiple notebook tabs polling
	// /kernel/usage don't each hit kube-apiserver + metrics-server. metrics-server
	// itself only refreshes every ~15s, so caching costs no freshness. Shares the
	// cachedUsage/usageTTL types defined in docker_per_user_gateway.go.
	usageMu    sync.Mutex
	usageCache map[string]cachedUsage
}

func NewK8sPerUserGateway(cfg K8sPerUserConfig) (*K8sPerUserGateway, error) {
	if cfg.PodImage == "" {
		cfg.PodImage = DefaultKernelImage
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("K8sPerUserConfig.Namespace is required (set KERNEL_POD_NAMESPACE)")
	}
	if cfg.CPURequest == "" {
		cfg.CPURequest = "500m"
	}
	if cfg.MemoryRequest == "" {
		cfg.MemoryRequest = "1Gi"
	}
	if cfg.CPULimit == "" {
		cfg.CPULimit = "2000m"
	}
	if cfg.MemoryLimit == "" {
		cfg.MemoryLimit = "4Gi"
	}
	// Validate each quantity at construction time — better to fail at boot
	// than have every Spawn panic with a less-readable trace.
	for name, q := range map[string]string{
		"CPURequest":    cfg.CPURequest,
		"MemoryRequest": cfg.MemoryRequest,
		"CPULimit":      cfg.CPULimit,
		"MemoryLimit":   cfg.MemoryLimit,
	} {
		if _, err := resource.ParseQuantity(q); err != nil {
			return nil, fmt.Errorf("K8sPerUserConfig.%s = %q: %w", name, q, err)
		}
	}
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s in-cluster config: %w (backend must run inside the cluster with a ServiceAccount)", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}

	gw := &K8sPerUserGateway{
		cfg:        cfg,
		client:     client,
		touchBuf:   make(map[string]time.Time),
		stopCh:     make(chan struct{}),
		usageCache: make(map[string]cachedUsage),
	}

	go gw.reaperLoop()
	go gw.flushTouchLoop()
	return gw, nil
}

func (g *K8sPerUserGateway) IdleTimeout() time.Duration { return g.cfg.IdleTimeout }
func (g *K8sPerUserGateway) Mode() string               { return "k8s_per_user" }

// Usage reports the user's pod CPU/memory. Limits come from the pod spec
// (always available); live usage comes from metrics-server via the
// metrics.k8s.io API. If metrics-server isn't installed/ready the metrics
// call fails and we return ErrUsageUnsupported so the FE hides the widget
// rather than show a limit with no usage.
func (g *K8sPerUserGateway) Usage(ctx context.Context, userID string) (ResourceUsage, error) {
	podName := podNameForUser(userID)

	g.usageMu.Lock()
	if c, ok := g.usageCache[podName]; ok && time.Since(c.at) < usageTTL {
		g.usageMu.Unlock()
		return c.usage, nil
	}
	g.usageMu.Unlock()

	// Limits from the pod spec.
	pod, err := g.client.CoreV1().Pods(g.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return ResourceUsage{}, ErrUsageUnsupported // no pod → nothing to measure
	}
	var cpuLimitCores float64
	var memLimitBytes int64
	if len(pod.Spec.Containers) > 0 {
		lim := pod.Spec.Containers[0].Resources.Limits
		if q, ok := lim[corev1.ResourceCPU]; ok {
			cpuLimitCores = q.AsApproximateFloat64()
		}
		if q, ok := lim[corev1.ResourceMemory]; ok {
			memLimitBytes = int64(q.AsApproximateFloat64())
		}
	}

	// Live usage from metrics-server. AbsPath bypasses the CoreV1 /api/v1 base
	// so we can hit the aggregated metrics.k8s.io API with the existing client.
	raw, err := g.client.CoreV1().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/namespaces/" + g.cfg.Namespace + "/pods/" + podName).
		DoRaw(ctx)
	if err != nil {
		// metrics-server absent or not ready yet.
		return ResourceUsage{}, ErrUsageUnsupported
	}
	var pm struct {
		Containers []struct {
			Usage struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"usage"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(raw, &pm); err != nil {
		return ResourceUsage{}, fmt.Errorf("decode pod metrics: %w", err)
	}
	var usedCores float64
	var usedBytes int64
	for _, cm := range pm.Containers {
		if q, err := resource.ParseQuantity(cm.Usage.CPU); err == nil {
			usedCores += q.AsApproximateFloat64()
		}
		if q, err := resource.ParseQuantity(cm.Usage.Memory); err == nil {
			usedBytes += int64(q.AsApproximateFloat64())
		}
	}

	cpuPct := 0.0
	if cpuLimitCores > 0 {
		cpuPct = usedCores / cpuLimitCores * 100.0
	}
	usage := ResourceUsage{
		CPUPercent:    cpuPct,
		CPUUsedCores:  usedCores,
		CPULimitCores: cpuLimitCores,
		MemUsedBytes:  usedBytes,
		MemLimitBytes: memLimitBytes,
	}

	g.usageMu.Lock()
	g.usageCache[podName] = cachedUsage{usage: usage, at: time.Now()}
	g.usageMu.Unlock()

	return usage, nil
}

// podNameForUser returns a DNS-safe, deterministic pod name for a user ID.
func podNameForUser(userID string) string {
	h := sha1.Sum([]byte(userID))
	return "kernel-" + hex.EncodeToString(h[:6])
}

// Status returns the current spawn phase for FE polling. Returns empty phase
// (not an error) when no row exists — caller treats that as "no spawn in flight".
func (g *K8sPerUserGateway) Status(userID string) (PodStatus, error) {
	if userID == "" {
		return PodStatus{}, nil
	}
	var s PodStatus
	err := database.GetDB().QueryRow(
		`SELECT status, phase_message, pod_url, pod_name FROM user_kernel_pods WHERE user_id = $1`,
		userID,
	).Scan(&s.Phase, &s.Message, &s.URL, &s.PodName)
	if err != nil {
		return PodStatus{}, nil // no row → empty status
	}
	// Self-heal a stale row: a settled phase (terminating / ready) whose pod no
	// longer exists (node reboot, evicted, OOM, terminate that never updated the
	// row) would otherwise pin the user forever and block reconnect. Clear it so
	// the next connect spawns fresh. Skip spawning/pulling/starting: those run
	// before the pod exists, so absence is expected there.
	if (s.Phase == PhaseTerminating || s.Phase == PhaseReady) && s.PodName != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, gerr := g.client.CoreV1().Pods(g.cfg.Namespace).Get(ctx, s.PodName, metav1.GetOptions{}); errors.IsNotFound(gerr) {
			log.Info().Str("user", userID).Str("pod", s.PodName).Str("phase", s.Phase).
				Msg("stale kernel row (pod gone); clearing so reconnect can spawn")
			database.GetDB().Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, userID)
			return PodStatus{}, nil
		}
	}
	return s, nil
}

// updatePhase writes the current phase + message so FE polling sees progress.
// Best-effort: a DB blip here just means one fewer progress tick to FE.
func (g *K8sPerUserGateway) updatePhase(userID, phase, msg string) {
	_, err := database.GetDB().Exec(
		`UPDATE user_kernel_pods SET status = $1, phase_message = $2 WHERE user_id = $3`,
		phase, msg, userID,
	)
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID).Str("phase", phase).Msg("updatePhase failed")
	}
}

// GetGatewayURL — fast path returns cached URL if pod ready. If not ready,
// kicks off async spawn and returns ErrPodNotReady so caller can poll Status
// instead of blocking. Old blocking behavior is preserved for non-Connect
// callers (WS proxy, ProxyHTTP) by waiting briefly after EnsureSpawning.
func (g *K8sPerUserGateway) GetGatewayURL(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("userID is required")
	}
	db := database.GetDB()
	podName := podNameForUser(userID)

	// Fast path: ready row + pod actually healthy
	var dbURL, dbStatus string
	row := db.QueryRow(
		`SELECT pod_url, status FROM user_kernel_pods WHERE user_id = $1`,
		userID,
	)
	if err := row.Scan(&dbURL, &dbStatus); err == nil {
		if dbStatus == PhaseReady && dbURL != "" && g.podHealthy(ctx, podName) {
			g.bufferTouch(userID)
			return dbURL, nil
		}
		if dbStatus == PhaseReady {
			// DB says ready but the pod isn't healthy. Could be: pod gone,
			// pod Failed/CrashLoop, or pod stuck in Pending. EnsureSpawning
			// would otherwise try to create a pod with the same name and
			// hit AlreadyExists, so tear down both pod + row first.
			log.Info().Str("user_id", userID).Str("pod", podName).Msg("stale ready row with unhealthy pod; destroying for respawn")
			_ = g.Destroy(userID)
		}
	}

	// Not ready — for legacy callers (WS/ProxyHTTP), kick off spawn and block
	// briefly. The Connect handler doesn't go through here anymore (it uses
	// EnsureSpawning + Status to stay non-blocking) so this path is only hit
	// when something tries to proxy through a dead pod.
	// nil spec → fall back to whatever resources are already on the row (or
	// gateway defaults). This proxy path never originates a user's size choice.
	if err := g.EnsureSpawning(userID, nil); err != nil {
		return "", err
	}
	url, spawnErr := g.spawnAndWait(ctx, userID, podName, g.rowSizes(userID))
	if spawnErr != nil {
		g.updatePhase(userID, PhaseFailed, spawnErr.Error())
		return "", spawnErr
	}
	_, err := db.Exec(
		`UPDATE user_kernel_pods SET pod_url = $1, status = $2, phase_message = $3, last_used_at = $4 WHERE user_id = $5`,
		url, PhaseReady, "Kernel ready", time.Now(), userID,
	)
	if err != nil {
		log.Warn().Err(err).Str("user_id", userID).Msg("failed to mark pod ready in db")
	}
	return url, nil
}

// EnsureSpawning kicks off pod spawn in a goroutine if no spawn is in flight
// and pod isn't already ready. Returns immediately. Idempotent: safe to call
// many times — only the first call from a fresh state triggers actual work.
func (g *K8sPerUserGateway) EnsureSpawning(userID string, spec *ResourceSpec) error {
	if userID == "" {
		return fmt.Errorf("userID is required")
	}
	db := database.GetDB()
	podName := podNameForUser(userID)
	sizes := g.resolveSpec(spec)

	var dbStatus, dbURL string
	err := db.QueryRow(
		`SELECT status, pod_url FROM user_kernel_pods WHERE user_id = $1`,
		userID,
	).Scan(&dbStatus, &dbURL)
	if err == nil {
		switch dbStatus {
		case PhaseReady:
			// Verify pod still alive. If yes, nothing to do.
			if dbURL != "" && g.podHealthy(context.Background(), podName) {
				return nil
			}
			// Stale ready row — pod is gone. Clean up and re-spawn.
			db.Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, userID)
		case PhaseSpawning, PhasePulling, PhaseStarting:
			return nil // spawn already in flight
		case PhaseTerminating:
			// Predecessor is mid-shutdown; spawnAndWait's step 1 will drain it.
			// Fall through to start fresh attempt.
		case PhaseFailed:
			// Previous attempt failed — retry.
		}
	}

	// Capacity check
	var podCount int
	db.QueryRow(
		`SELECT COUNT(*) FROM user_kernel_pods WHERE status IN ($1, $2, $3, $4, $5)`,
		PhaseSpawning, PhasePulling, PhaseStarting, PhaseReady, PhaseTerminating,
	).Scan(&podCount)
	if podCount >= g.cfg.MaxPods {
		return fmt.Errorf("cluster at capacity (%d active pods); try again later", podCount)
	}

	// Insert spawning row — ON CONFLICT serializes concurrent EnsureSpawning calls.
	// The resolved resources are persisted on the row so buildPodSpec (which runs
	// in the detached goroutine) and any later respawn use the size the user
	// picked for THIS spawn, not the cluster default.
	_, err = db.Exec(
		`INSERT INTO user_kernel_pods (user_id, pod_name, pod_namespace, status, phase_message, created_at, last_used_at, cpu_request, mem_request, cpu_limit, mem_limit)
		 VALUES ($1, $2, $3, $4, $5, $6, $6, $7, $8, $9, $10)
		 ON CONFLICT (user_id) DO UPDATE SET status = $4, phase_message = $5, created_at = $6, last_used_at = $6, pod_url = '', cpu_request = $7, mem_request = $8, cpu_limit = $9, mem_limit = $10`,
		userID, podName, g.cfg.Namespace, PhaseSpawning, "Preparing kernel pod...", time.Now(),
		sizes.cpuReq, sizes.memReq, sizes.cpuLim, sizes.memLim,
	)
	if err != nil {
		return fmt.Errorf("db insert: %w", err)
	}

	// Detached spawn goroutine. Use background context with explicit deadline so
	// the spawn outlives the HTTP request that triggered it. The resolved
	// resources are passed DIRECTLY (not re-read from the row inside the
	// goroutine): a concurrent Destroy — e.g. the resize path destroying the
	// predecessor — deletes the row asynchronously and can race the read,
	// silently reverting the pod to default sizes.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), spawnTimeout+30*time.Second)
		defer cancel()
		url, spawnErr := g.spawnAndWait(ctx, userID, podName, sizes)
		if spawnErr != nil {
			g.updatePhase(userID, PhaseFailed, spawnErr.Error())
			log.Warn().Err(spawnErr).Str("user_id", userID).Msg("background spawn failed")
			return
		}
		_, err := database.GetDB().Exec(
			`UPDATE user_kernel_pods SET pod_url = $1, status = $2, phase_message = $3, last_used_at = $4 WHERE user_id = $5`,
			url, PhaseReady, "Kernel ready", time.Now(), userID,
		)
		if err != nil {
			log.Warn().Err(err).Str("user_id", userID).Msg("failed to mark pod ready in db")
		}
	}()
	return nil
}

// spawnAndWait: 3 stages —
//   1. drain any previous pod still terminating (grace ≤60s)
//   2. K8s Create (idempotent vs AlreadyExists)
//   3. Watch event stream until Ready or timeout (5min)
//
// Phase is written to DB on every meaningful transition so FE polling shows
// "Pulling image…" / "Container starting…" / etc. without guessing.
func (g *K8sPerUserGateway) spawnAndWait(ctx context.Context, userID, podName string, res podSizes) (string, error) {
	pods := g.client.CoreV1().Pods(g.cfg.Namespace)

	// Step 1 — handle terminating predecessor
	if existing, err := pods.Get(ctx, podName, metav1.GetOptions{}); err == nil {
		if existing.DeletionTimestamp != nil {
			g.updatePhase(userID, PhaseTerminating, "Cleaning up previous kernel...")
			if err := g.waitForGone(ctx, podName, 60*time.Second); err != nil {
				return "", fmt.Errorf("previous pod still terminating after 60s: %w", err)
			}
		}
	}

	// Step 2 — Create (idempotent)
	pod := g.buildPodSpec(userID, podName, res)
	if _, err := pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		if !errors.IsAlreadyExists(err) {
			return "", fmt.Errorf("pod create: %w", err)
		}
		// Already exists (and not terminating per step 1) → reuse, fall through to Watch.
		log.Info().Str("pod", podName).Msg("pod already exists, reusing for watch")
	}

	// Step 3 — Watch loop with phase updates
	return g.watchUntilReady(ctx, userID, podName)
}

func (g *K8sPerUserGateway) watchUntilReady(ctx context.Context, userID, podName string) (string, error) {
	pods := g.client.CoreV1().Pods(g.cfg.Namespace)

	deadline := time.Now().Add(spawnTimeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Initial snapshot — pod may already be Ready before Watch opens.
	if p, err := pods.Get(ctx, podName, metav1.GetOptions{}); err == nil {
		if url := readyURL(p); url != "" {
			return url, nil
		}
		phase, msg := derivePhase(p)
		g.updatePhase(userID, phase, msg)
		if phase == PhaseFailed {
			return "", fmt.Errorf("%s", msg)
		}
	}

	// Watch loop — auto-reconnect on channel close (Watch can drop after ~30min).
	for {
		w, err := pods.Watch(ctx, metav1.ListOptions{
			FieldSelector: "metadata.name=" + podName,
		})
		if err != nil {
			return "", fmt.Errorf("watch pod: %w", err)
		}

		url, done, err := g.consumeWatch(ctx, w, userID, podName)
		w.Stop()
		if err != nil {
			return "", err
		}
		if done {
			return url, nil
		}
		// Channel closed without verdict — retry if still time left.
		if time.Now().After(deadline) {
			g.updatePhase(userID, PhaseFailed, "Pod did not become ready within 5 minutes")
			return "", fmt.Errorf("pod %s did not become ready within %v", podName, spawnTimeout)
		}
	}
}

// consumeWatch drains one Watch session. Returns (url, done, err):
//   - done=true, url set: pod is Ready
//   - done=false, err=nil: channel closed; caller should reopen Watch
//   - err set: hard failure (FE shouldn't retry without user action)
func (g *K8sPerUserGateway) consumeWatch(ctx context.Context, w watch.Interface, userID, podName string) (string, bool, error) {
	var lastPhase string
	for {
		select {
		case <-ctx.Done():
			g.updatePhase(userID, PhaseFailed, "Pod did not become ready within 5 minutes")
			return "", false, fmt.Errorf("pod %s did not become ready within %v: %w", podName, spawnTimeout, ctx.Err())
		case event, ok := <-w.ResultChan():
			if !ok {
				return "", false, nil // channel closed, caller reopens
			}
			p, ok := event.Object.(*corev1.Pod)
			if !ok {
				continue
			}
			if url := readyURL(p); url != "" {
				return url, true, nil
			}
			phase, msg := derivePhase(p)
			if phase != lastPhase {
				g.updatePhase(userID, phase, msg)
				lastPhase = phase
				log.Info().Str("user_id", userID).Str("pod", podName).Str("phase", phase).Msg(msg)
			}
			if phase == PhaseFailed {
				return "", false, fmt.Errorf("%s", msg)
			}
		}
	}
}

// readyURL returns http://podIP:8888 if the container is Ready, else "".
func readyURL(p *corev1.Pod) string {
	if p == nil || p.DeletionTimestamp != nil || p.Status.PodIP == "" {
		return ""
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.Ready {
			return fmt.Sprintf("http://%s:8888", p.Status.PodIP)
		}
	}
	return ""
}

// derivePhase reads the pod's K8s state and maps it to one of our user-facing phases.
// Order matters: terminating > failed > pulling > starting > spawning.
func derivePhase(p *corev1.Pod) (phase, message string) {
	if p == nil {
		return PhaseSpawning, "Waiting for pod..."
	}
	if p.DeletionTimestamp != nil {
		return PhaseTerminating, "Pod is terminating"
	}

	// Failed container states bubble up first — no point reporting "pulling" if
	// the pull already failed.
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			switch cs.State.Waiting.Reason {
			case "ImagePullBackOff", "ErrImagePull":
				return PhaseFailed, "Failed to pull kernel image: " + cs.State.Waiting.Message
			case "CrashLoopBackOff":
				return PhaseFailed, "Kernel container crashed: " + cs.State.Waiting.Message
			case "CreateContainerConfigError", "CreateContainerError":
				return PhaseFailed, "Container config error: " + cs.State.Waiting.Message
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return PhaseFailed, fmt.Sprintf("Kernel exited (code %d): %s",
				cs.State.Terminated.ExitCode, cs.State.Terminated.Message)
		}
	}

	if p.Status.Phase == corev1.PodFailed {
		return PhaseFailed, "Pod failed: " + p.Status.Message
	}

	// Pending — distinguish image pull from "waiting for node"
	if p.Status.Phase == corev1.PodPending {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" {
				return PhasePulling, "Pulling kernel image (first time may take a minute)..."
			}
		}
		return PhaseSpawning, "Pod scheduled, waiting for node..."
	}

	if p.Status.Phase == corev1.PodRunning {
		return PhaseStarting, "Container started, waiting for kernel..."
	}
	return PhaseSpawning, ""
}

// podSizes carries the resolved request/limit quantities for one pod spawn.
// Always fully populated (never empty strings — resource.MustParse would
// panic). Threaded explicitly from EnsureSpawning into the spawn goroutine
// instead of being re-read from the DB row, which a concurrent Destroy (the
// resize path destroying the predecessor) can delete mid-spawn, silently
// reverting the pod to default sizes.
type podSizes struct {
	cpuReq, memReq, cpuLim, memLim string
}

// resolveSpec turns a user-picked *ResourceSpec into the pod's request/limit
// quantities. A valid spec applies its CPU/memory as BOTH request and limit
// (Guaranteed QoS — the user gets exactly what they picked); nil or incomplete
// falls back to the gateway's configured defaults (which are burstable: a
// small request with a larger limit).
func (g *K8sPerUserGateway) resolveSpec(spec *ResourceSpec) podSizes {
	if spec != nil && spec.CPU != "" && spec.Memory != "" {
		return podSizes{spec.CPU, spec.Memory, spec.CPU, spec.Memory}
	}
	return podSizes{g.cfg.CPURequest, g.cfg.MemoryRequest, g.cfg.CPULimit, g.cfg.MemoryLimit}
}

// rowSizes reads the sizes recorded on the user_kernel_pods row — used by the
// legacy GetGatewayURL respawn path, which has no user-picked spec, so a
// respawn keeps the size the user last chose. Falls back to gateway defaults
// when the row is missing or predates the resource columns.
func (g *K8sPerUserGateway) rowSizes(userID string) podSizes {
	var cr, mr, cl, ml string
	err := database.GetDB().QueryRow(
		`SELECT cpu_request, mem_request, cpu_limit, mem_limit FROM user_kernel_pods WHERE user_id = $1`,
		userID,
	).Scan(&cr, &mr, &cl, &ml)
	if err == nil && cr != "" && mr != "" && cl != "" && ml != "" {
		return podSizes{cr, mr, cl, ml}
	}
	return podSizes{g.cfg.CPURequest, g.cfg.MemoryRequest, g.cfg.CPULimit, g.cfg.MemoryLimit}
}

func (g *K8sPerUserGateway) buildPodSpec(userID, podName string, res podSizes) *corev1.Pod {
	// Resolve per-user MinIO creds. Empty creds → fall back to root creds via
	// SecretKeyRef (legacy / IAM-not-configured). Per-user creds are injected
	// as plain env values; the pod is per-user and ephemeral so leakage risk
	// is bounded to the pod's lifetime.
	var (
		userAccessKey, userSecretKey string
		usePerUserCreds              bool
	)
	if g.cfg.CredsResolver != nil {
		ak, sk, err := g.cfg.CredsResolver(userID)
		if err != nil {
			log.Warn().Err(err).Str("user", userID).Msg("CredsResolver failed; falling back to root creds")
		} else if ak != "" && sk != "" {
			userAccessKey, userSecretKey = ak, sk
			usePerUserCreds = true
		}
	}

	var awsEnv []corev1.EnvVar
	if usePerUserCreds {
		awsEnv = []corev1.EnvVar{
			{Name: "AWS_ACCESS_KEY_ID", Value: userAccessKey},
			{Name: "AWS_SECRET_ACCESS_KEY", Value: userSecretKey},
		}
	} else {
		awsEnv = []corev1.EnvVar{
			{
				Name: "AWS_ACCESS_KEY_ID",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "sparklabx-secrets"},
						Key:                  "MINIO_ROOT_USER",
					},
				},
			},
			{
				Name: "AWS_SECRET_ACCESS_KEY",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "sparklabx-secrets"},
						Key:                  "MINIO_ROOT_PASSWORD",
					},
				},
			},
		}
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: g.cfg.Namespace,
			Labels: map[string]string{
				"app":            "kernel-pod",
				"managed-by":     "sparklabx",
				"sparklabx-user": labelSafe(userID),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyOnFailure,
			// Kernels hold no durable state — killing one loses the user's
			// variables either way, so a long drain buys nothing. The default
			// 30s grace made every resize/restart sit in "Cleaning up previous
			// kernel..." for half a minute; 5s keeps that snappy.
			TerminationGracePeriodSeconds: ptrInt64(5),
			// imagePullSecrets only attached for private registries (cfg.PullSecret
			// set via KERNEL_PULL_SECRET env). Public ghcr.io images don't need it.
			ImagePullSecrets: pullSecretRefs(g.cfg.PullSecret),
			Containers: []corev1.Container{
				{
					Name:            "jupyter-kernel",
					Image:           g.cfg.PodImage,
					ImagePullPolicy: corev1.PullIfNotPresent,
					Ports: []corev1.ContainerPort{
						{ContainerPort: 8888, Protocol: corev1.ProtocolTCP},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(res.cpuReq),
							corev1.ResourceMemory: resource.MustParse(res.memReq),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(res.cpuLim),
							corev1.ResourceMemory: resource.MustParse(res.memLim),
						},
					},
					// Spark s3a reads these from env to build core-site.xml / spark-defaults
					// at boot. Without them the kernel can't talk to MinIO/S3.
					// awsEnv carries either per-user IAM creds (true isolation) or root
					// creds (legacy fallback). S3_ENDPOINT always comes from ConfigMap.
					Env: append(awsEnv, corev1.EnvVar{
						Name: "S3_ENDPOINT",
						ValueFrom: &corev1.EnvVarSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "sparklabx-config"},
								Key:                  "MINIO_ENDPOINT",
							},
						},
					}),
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(8888)},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       3,
					},
				},
			},
		},
	}
}

// podHealthy returns true only when the pod is actually serving — not just
// when Phase==Running. Phase flips to Running as soon as a container starts;
// Jupyter inside needs another 5-15s to bind port 8888, during which the
// caller would get EOF/reset if we trusted Phase alone. Reading the
// PodReady condition uses k8s's own readinessProbe result (TCP probe on
// 8888 defined in the pod spec) so we don't double-probe from here.
func (g *K8sPerUserGateway) podHealthy(ctx context.Context, podName string) bool {
	p, err := g.client.CoreV1().Pods(g.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return false
	}
	if p.DeletionTimestamp != nil {
		return false
	}
	for _, cond := range p.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (g *K8sPerUserGateway) Touch(userID string) {
	g.bufferTouch(userID)
}

func (g *K8sPerUserGateway) bufferTouch(userID string) {
	g.touchMu.Lock()
	g.touchBuf[userID] = time.Now()
	g.touchMu.Unlock()
}

// Destroy marks the row terminating, deletes the pod gracefully, and clears the
// row in background once the pod is fully gone. A concurrent GetGatewayURL
// sees PhaseTerminating and spawnAndWait's step 1 drains the predecessor.
func (g *K8sPerUserGateway) Destroy(userID string) error {
	db := database.GetDB()
	podName := podNameForUser(userID)

	g.updatePhase(userID, PhaseTerminating, "Shutting down kernel...")

	err := g.client.CoreV1().Pods(g.cfg.Namespace).Delete(
		context.Background(), podName, metav1.DeleteOptions{},
	)
	if err != nil && !errors.IsNotFound(err) {
		// DB row still says terminating — reaper will eventually clean up.
		return err
	}
	if errors.IsNotFound(err) {
		// Pod already gone (e.g. reaper beat us) — clear the row now.
		db.Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, userID)
		return nil
	}

	// Background: wait for pod fully gone, then drop the row so the next spawn
	// from this user starts cleanly without bumping into the terminating-handshake
	// path. Guarded on status=terminating: the resize flow calls Destroy and then
	// immediately EnsureSpawning, which inserts a fresh spawning row (with the
	// newly picked sizes) — an unconditional delete here would race that insert
	// and wipe the new row mid-spawn.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_ = g.waitForGone(ctx, podName, 90*time.Second)
		db.Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1 AND status = $2`, userID, PhaseTerminating)
		log.Info().Str("user_id", userID).Str("pod", podName).Msg("terminating handshake complete")
	}()
	return nil
}

// waitForGone polls until the pod returns NotFound or the timeout fires.
func (g *K8sPerUserGateway) waitForGone(ctx context.Context, podName string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return wait.PollUntilContextTimeout(waitCtx, 1*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		_, err := g.client.CoreV1().Pods(g.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		return errors.IsNotFound(err), nil
	})
}

func (g *K8sPerUserGateway) flushTouchLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.touchMu.Lock()
			if len(g.touchBuf) == 0 {
				g.touchMu.Unlock()
				continue
			}
			snapshot := g.touchBuf
			g.touchBuf = make(map[string]time.Time)
			g.touchMu.Unlock()

			db := database.GetDB()
			for userID, ts := range snapshot {
				_, _ = db.Exec(`UPDATE user_kernel_pods SET last_used_at = $1 WHERE user_id = $2`, ts, userID)
			}
		}
	}
}

// reaperLoop deletes idle pods AND reconciles stuck rows:
//   - status=ready  + last_used_at < cutoff → kill (normal idle reap)
//   - status=failed + older than 60s        → drop row so FE doesn't see stale error
//   - status=spawning/pulling/starting + older than spawn timeout → assume crashed, drop
//   - status=ready but pod is gone / Failed / CrashLoopBackOff (reapDeadPods)
//   - any pod with managed-by=sparklabx label not tracked in DB (sweepOrphanPods)
func (g *K8sPerUserGateway) reaperLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.reapOnce()
			g.reapDeadPods()
			g.sweepOrphanPods()
		}
	}
}

// kernelBusy queries the Jupyter Kernel Gateway on the pod and reports
// whether any kernel is currently executing a cell. Returns false on any
// error (unreachable pod, HTTP timeout, malformed response) so the reaper
// can still kill genuinely dead pods.
//
// This is the fix for the long-running-cell-killed-by-reaper bug
// (issue #44): a user starts a 45-min Spark job, closes the tab,
// `last_used_at` freezes, and 30 min later the reaper used to delete
// the pod mid-execution. With this check, the reaper only kills pods
// whose kernel reports `execution_state == "idle"`.
func kernelBusy(podURL string) bool {
	if podURL == "" {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(podURL + "/api/kernels")
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}
	defer resp.Body.Close()
	var kernels []struct {
		ExecutionState string `json:"execution_state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&kernels); err != nil {
		return false
	}
	for _, k := range kernels {
		if k.ExecutionState == "busy" {
			return true
		}
	}
	return false
}

func (g *K8sPerUserGateway) reapOnce() {
	db := database.GetDB()
	cutoff := time.Now().Add(-g.cfg.IdleTimeout)

	rows, err := db.Query(
		`SELECT user_id, pod_name, pod_url FROM user_kernel_pods
		 WHERE status = $1 AND last_used_at < $2`,
		PhaseReady, cutoff,
	)
	if err != nil {
		log.Error().Err(err).Msg("reaper: query idle pods")
		return
	}
	defer rows.Close()

	type victim struct{ userID, podName, podURL string }
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.userID, &v.podName, &v.podURL); err == nil {
			victims = append(victims, v)
		}
	}
	rows.Close()

	for _, v := range victims {
		// Don't kill a pod whose kernel is mid-execution — the user is
		// probably running a long Spark job with the tab closed.
		if kernelBusy(v.podURL) {
			log.Info().Str("user_id", v.userID).Str("pod", v.podName).
				Msg("reaper: skipping idle reap, kernel is busy")
			// Bump last_used_at so we don't re-check every minute while the
			// job runs — recheck in IdleTimeout/2 instead.
			db.Exec(`UPDATE user_kernel_pods SET last_used_at = $1 WHERE user_id = $2`,
				time.Now().Add(-g.cfg.IdleTimeout/2), v.userID)
			continue
		}
		err := g.client.CoreV1().Pods(g.cfg.Namespace).Delete(context.Background(), v.podName, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			log.Warn().Err(err).Str("pod", v.podName).Msg("reaper: failed to delete pod")
			continue
		}
		db.Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, v.userID)
		log.Info().Str("user_id", v.userID).Str("pod", v.podName).Msg("reaper: deleted idle pod")
	}

	// Drop stale failed/stuck rows so they don't poison capacity counting or FE polling.
	staleCutoff := time.Now().Add(-spawnTimeout)
	res, _ := db.Exec(
		`DELETE FROM user_kernel_pods
		 WHERE (status = $1 AND created_at < $2)
		    OR (status IN ($3, $4, $5) AND created_at < $2)`,
		PhaseFailed, staleCutoff,
		PhaseSpawning, PhasePulling, PhaseStarting,
	)
	if res != nil {
		if n, _ := res.RowsAffected(); n > 0 {
			log.Info().Int64("rows", n).Msg("reaper: cleaned up stale spawn rows")
		}
	}
}

// reapDeadPods cleans up DB rows whose pod is in a dead state. Without this,
// status=ready rows can sit around pointing at a Failed / CrashLoopBackOff
// pod, and users get errors until they manually reconnect (which triggers
// the Layer C self-heal). The sweep covers users who quit and come back
// later, plus admin pods that hit OOM overnight.
func (g *K8sPerUserGateway) reapDeadPods() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db := database.GetDB()
	rows, err := db.Query(`SELECT user_id, pod_name FROM user_kernel_pods WHERE status = $1`, PhaseReady)
	if err != nil {
		return
	}
	type victim struct{ userID, podName string }
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.userID, &v.podName); err == nil {
			victims = append(victims, v)
		}
	}
	rows.Close()

	for _, v := range victims {
		p, err := g.client.CoreV1().Pods(g.cfg.Namespace).Get(ctx, v.podName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				db.Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, v.userID)
				log.Info().Str("user_id", v.userID).Str("pod", v.podName).Msg("reapDead: pod gone, cleared row")
			}
			continue
		}
		dead, reason := podIsDead(p)
		if !dead {
			continue
		}
		log.Info().Str("user_id", v.userID).Str("pod", v.podName).Str("reason", reason).Msg("reapDead: destroying unhealthy pod")
		_ = g.client.CoreV1().Pods(g.cfg.Namespace).Delete(ctx, v.podName, metav1.DeleteOptions{})
		db.Exec(`DELETE FROM user_kernel_pods WHERE user_id = $1`, v.userID)
	}
}

// podIsDead classifies a pod we consider unrecoverable for serving requests.
// Phase=Failed/Succeeded are terminal. Phase=Running with any container in
// CrashLoopBackOff or ImagePullBackOff isn't going to recover on its own.
func podIsDead(p *corev1.Pod) (bool, string) {
	switch p.Status.Phase {
	case corev1.PodFailed:
		return true, "phase=failed"
	case corev1.PodSucceeded:
		return true, "phase=succeeded"
	}
	for _, cs := range p.Status.ContainerStatuses {
		if cs.State.Waiting == nil {
			continue
		}
		switch cs.State.Waiting.Reason {
		case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "CreateContainerConfigError":
			return true, "waiting=" + cs.State.Waiting.Reason
		}
	}
	return false, ""
}

// sweepOrphanPods deletes pods labeled managed-by=sparklabx that have no
// DB row tracking them. These are leftovers from a backend crash mid-spawn
// or mid-destroy. The 1-minute age filter avoids racing with a spawn
// that just created the pod but hasn't inserted the DB row yet.
func (g *K8sPerUserGateway) sweepOrphanPods() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pods, err := g.client.CoreV1().Pods(g.cfg.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "managed-by=sparklabx",
	})
	if err != nil {
		return
	}
	if len(pods.Items) == 0 {
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

	ageCutoff := time.Now().Add(-1 * time.Minute)
	for _, p := range pods.Items {
		if tracked[p.Name] {
			continue
		}
		if p.CreationTimestamp.Time.After(ageCutoff) {
			continue
		}
		log.Info().Str("pod", p.Name).Msg("sweepOrphans: deleting untracked kernel pod")
		_ = g.client.CoreV1().Pods(g.cfg.Namespace).Delete(ctx, p.Name, metav1.DeleteOptions{})
	}
}

// labelSafe sanitizes a string for use as a K8s label value.
func ptrInt64(v int64) *int64 { return &v }

func labelSafe(s string) string {
	const max = 63
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
		if len(out) >= max {
			break
		}
	}
	return string(out)
}
