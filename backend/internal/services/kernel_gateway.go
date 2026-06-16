package services

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
)

// KernelGateway abstracts where a user's Jupyter Kernel Gateway lives.
// Two implementations are wired at startup based on KERNEL_MODE env var:
//
//   - SharedGateway:    one Jupyter container serves all users (docker-compose dev,
//     small K8s deployments). Cheap, no isolation.
//   - K8sPerUserGateway: each user gets a dedicated pod, spawned on demand and
//     reaped after idle. Isolated, autoscaling.
//
// Either way, handlers call GetGatewayURL(userID) at request time and proxy
// kernel requests to whatever URL comes back.
type KernelGateway interface {
	// GetGatewayURL returns the http://host:port of the user's Jupyter gateway.
	// For dynamic gateways, this may spawn a pod and wait for it to become ready
	// (so callers should treat this as potentially long-running and pass a context
	// with a reasonable timeout, e.g. 90s).
	GetGatewayURL(ctx context.Context, userID string) (string, error)

	// Touch updates the user's "last active" timestamp. Called on every WS read /
	// REST proxy hit so idle-reaper knows the pod is still in use. No-op for
	// SharedGateway (no per-user lifecycle).
	Touch(userID string)

	// Destroy releases the user's gateway (kill pod, clear DB row). Optional —
	// reaper handles idle cleanup automatically; this is for explicit logout /
	// manual reset. No-op for SharedGateway.
	Destroy(userID string) error

	// Mode returns a string id for logging / diagnostics ("shared" or "k8s_per_user").
	Mode() string

	// Status returns the current spawn phase for FE polling. Empty phase ("")
	// means no spawn is in flight (either ready already or never started).
	// SharedGateway always returns {Phase: "ready"} since there's no spawn step.
	Status(userID string) (PodStatus, error)

	// EnsureSpawning kicks off (or no-ops if already in flight / done) a pod
	// spawn for the user — returns IMMEDIATELY so the HTTP handler doesn't
	// block for minutes waiting on image pull. Caller then polls Status to
	// learn when phase reaches "ready". SharedGateway is a pure no-op since
	// there's nothing to spawn.
	//
	// spec sets the pod's CPU/memory for THIS spawn; nil → gateway defaults.
	// It only takes effect on a fresh spawn — if a pod is already ready or
	// spawning, the size is fixed until that pod is destroyed (the kernel pod
	// is per-user, not per-notebook, so resizing means restarting the kernel).
	EnsureSpawning(userID string, spec *ResourceSpec) error

	// Usage returns live CPU/memory usage of the user's kernel container/pod.
	// Returns ErrUsageUnsupported when the mode has no per-user container to
	// measure (shared) or the metric source is unavailable (k8s without
	// metrics-server) — callers should treat that as "hide the widget", not
	// a hard error.
	Usage(ctx context.Context, userID string) (ResourceUsage, error)

	// RecentLogs returns the last `tailLines` of the user's kernel
	// container/pod stdout+stderr. Used to surface dependency-resolution
	// failures (issue #33): Spark/Coursier print "unresolved dependency …"
	// to the JVM's stderr, which lands in the container log — NOT in the
	// notebook cell — so the UI can't see it without reading the log here.
	// Returns ErrUsageUnsupported for SharedGateway (no per-user container).
	RecentLogs(ctx context.Context, userID string, tailLines int) (string, error)
}

// ResourceUsage is a point-in-time snapshot of a kernel container/pod's
// resource consumption. MemLimitBytes is the cgroup/pod limit (what the user
// is capped at); 0 means "no limit set" so the UI should fall back to the
// host total or hide the denominator.
type ResourceUsage struct {
	CPUPercent    float64 `json:"cpu_percent"`     // 0–100, relative to the container's CPU quota
	CPUUsedCores  float64 `json:"cpu_used_cores"`  // cores currently consumed (e.g. 0.24)
	CPULimitCores float64 `json:"cpu_limit_cores"` // quota in cores (e.g. 2); 0 = unlimited
	MemUsedBytes  int64   `json:"mem_used_bytes"`
	MemLimitBytes int64   `json:"mem_limit_bytes"`
	// MetricsLive is false when the limits are known (pod exists) but live
	// usage isn't available yet — e.g. k8s metrics-server hasn't scraped the
	// fresh pod (~15s lag after spawn). The limit fields are still valid; only
	// the used/percent figures are zero-placeholder. Consumers that just need
	// the kernel's size (the Resources picker warning) can rely on the limits
	// regardless; the live badge can render a neutral state until it flips true.
	MetricsLive bool `json:"metrics_live"`
}

// ResourceSpec is the resolved CPU/memory for a single kernel pod. Both the
// request and the limit are set to these values (Guaranteed QoS) so a user
// gets exactly what they picked. Quantities are in k8s format ("1", "500m",
// "2Gi"). A nil *ResourceSpec means "use the gateway's configured defaults".
type ResourceSpec struct {
	CPU    string
	Memory string
}

// ErrUsageUnsupported signals that resource usage can't be measured for this
// gateway mode / user. Handlers map it to an "unavailable" response rather
// than a 500.
var ErrUsageUnsupported = fmt.Errorf("resource usage not supported")

// DefaultKernelImage is the canonical public kernel image. Used as the fallback
// in both Docker and K8s per-user gateways when KERNEL_POD_IMAGE isn't set in
// the environment. Set KERNEL_POD_IMAGE to override (e.g. to your own fork).
const DefaultKernelImage = "ghcr.io/sparklabx/kernel:latest"

// SharedGateway returns the same fixed URL for every caller. No spawn, no reap.
type SharedGateway struct {
	url string
}

// NewSharedGateway creates a SharedGateway that always returns gatewayURL.
func NewSharedGateway(gatewayURL string) *SharedGateway {
	return &SharedGateway{url: gatewayURL}
}

func (s *SharedGateway) GetGatewayURL(_ context.Context, _ string) (string, error) {
	if s.url == "" {
		return "", fmt.Errorf("JUPYTER_GATEWAY_URL is not configured")
	}
	return s.url, nil
}

func (s *SharedGateway) Touch(_ string)         {}
func (s *SharedGateway) Destroy(_ string) error { return nil }
func (s *SharedGateway) Mode() string           { return "shared" }
func (s *SharedGateway) Status(_ string) (PodStatus, error) {
	// Shared gateway has no spawn step — always ready.
	return PodStatus{Phase: PhaseReady, Message: "Kernel ready", URL: s.url}, nil
}
func (s *SharedGateway) EnsureSpawning(_ string, _ *ResourceSpec) error { return nil }
func (s *SharedGateway) Usage(_ context.Context, _ string) (ResourceUsage, error) {
	// One shared container for everyone — no per-user figure to report.
	return ResourceUsage{}, ErrUsageUnsupported
}
func (s *SharedGateway) RecentLogs(_ context.Context, _ string, _ int) (string, error) {
	return "", ErrUsageUnsupported
}

// KernelGatewaySettings is the fully-resolved config needed to build a gateway.
// Populated from *config.Config in main.go so this package doesn't depend on
// the config package.
type KernelGatewaySettings struct {
	Mode               string        // "shared" | "docker_per_user" | "k8s_per_user"
	Environment        string        // "production" | other — gates the shared-mode safety check
	JupyterGatewayURL  string        // only used in shared mode
	PodImage           string        // kernel container/pod image
	PodNamespace       string        // K8s namespace (k8s_per_user)
	DockerNetwork      string        // host docker network (docker_per_user)
	MinIOEndpoint      string        // injected as S3_ENDPOINT env in kernel
	IdleTimeout        time.Duration // reap kernel after this long idle
	MaxKernels         int           // hard cap on concurrent kernels
	PullSecret         string        // optional K8s imagePullSecret name (empty → none)
	CredsResolver      UserCredsResolver
	OIDCTokenResolver  UserOIDCTokenResolver // returns the kernel's callback token (SPARKLABX_KERNEL_TOKEN); nil → no SSO passthrough
	TrinoURL           string                // optional default Trino JDBC URL injected as TRINO_URL for the trino() helper
	KernelAPIURL       string                // backend URL injected as SPARKLABX_API_URL so kernels can fetch a fresh OIDC token
	ConnectorsManifest string                // JSON [{id,driver,url}] injected as SPARKLABX_CONNECTORS for the generic data helpers (static fallback)
	// ConnectorsManifestProvider, when set, is called at each kernel spawn with the
	// spawning user's id to get the manifest of connectors VISIBLE to that user
	// (shared + their personal) — so runtime adds/removes reach new kernels without
	// a restart. Falls back to the static ConnectorsManifest.
	ConnectorsManifestProvider func(userID string) string

	// Resource requests/limits in k8s quantity format ("500m", "1Gi"). For
	// docker_per_user mode only the *Limit values apply (Docker has no
	// "request" concept). Empty → falls back to gateway-internal defaults.
	PodCPURequest    string
	PodMemoryRequest string
	PodCPULimit      string
	PodMemoryLimit   string
}

// resolveConnectorsManifest returns the SPARKLABX_CONNECTORS JSON to inject at
// spawn — the live provider when set (reflects runtime adds/deletes), else the
// static boot-time value.
func resolveConnectorsManifest(static string, provider func(string) string, userID string) string {
	if provider != nil {
		return provider(userID)
	}
	return static
}

// NewKernelGateway picks an implementation based on settings.Mode.
//   - "shared":          SharedGateway pointed at JupyterGatewayURL. ONE container
//     for everyone, no isolation.
//   - "docker_per_user": DockerPerUserGateway. One container per user on the local
//     Docker daemon. Real IAM isolation. Requires docker.sock mount.
//   - "k8s_per_user":    K8sPerUserGateway. Production isolation via Kubernetes.
//     Backend must run in-cluster with the RBAC in kubernetes/.
func NewKernelGateway(s KernelGatewaySettings) (KernelGateway, error) {
	mode := s.Mode
	explicit := mode != ""
	if mode == "" {
		mode = "shared"
	}

	switch mode {
	case "shared":
		// Shared mode = one Jupyter Gateway for everyone, zero per-user
		// isolation: any user's kernel can reach any other user's in-memory
		// state and (without per-user MinIO IAM) their object-store data.
		// Refuse to start this way in production — losing isolation must be a
		// loud, deliberate choice, never a silent default.
		if s.Environment == "production" {
			return nil, fmt.Errorf("KERNEL_MODE=shared is unsafe in production (no per-user kernel isolation); set KERNEL_MODE=docker_per_user or k8s_per_user")
		}
		if explicit {
			log.Warn().Str("gateway", s.JupyterGatewayURL).Msg("kernel gateway: SHARED mode — NO per-user isolation, all users share one kernel container (dev only)")
		} else {
			log.Warn().Str("gateway", s.JupyterGatewayURL).Msg("kernel gateway: KERNEL_MODE not set, defaulting to SHARED — NO per-user isolation (set docker_per_user/k8s_per_user for multi-user)")
		}
		return NewSharedGateway(s.JupyterGatewayURL), nil
	case "docker_per_user":
		gw, err := NewDockerPerUserGateway(DockerPerUserConfig{
			Image:                      s.PodImage,
			Network:                    s.DockerNetwork,
			IdleTimeout:                s.IdleTimeout,
			MaxContainers:              s.MaxKernels,
			MinIOEndpoint:              s.MinIOEndpoint,
			CredsResolver:              s.CredsResolver,
			OIDCTokenResolver:          s.OIDCTokenResolver,
			TrinoURL:                   s.TrinoURL,
			KernelAPIURL:               s.KernelAPIURL,
			ConnectorsManifest:         s.ConnectorsManifest,
			ConnectorsManifestProvider: s.ConnectorsManifestProvider,
			CPULimit:                   s.PodCPULimit,
			MemoryLimit:                s.PodMemoryLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("init DockerPerUserGateway: %w", err)
		}
		return gw, nil
	case "k8s_per_user":
		gw, err := NewK8sPerUserGateway(K8sPerUserConfig{
			Namespace:                  s.PodNamespace,
			PodImage:                   s.PodImage,
			IdleTimeout:                s.IdleTimeout,
			MaxPods:                    s.MaxKernels,
			PullSecret:                 s.PullSecret,
			CredsResolver:              s.CredsResolver,
			OIDCTokenResolver:          s.OIDCTokenResolver,
			TrinoURL:                   s.TrinoURL,
			KernelAPIURL:               s.KernelAPIURL,
			ConnectorsManifest:         s.ConnectorsManifest,
			ConnectorsManifestProvider: s.ConnectorsManifestProvider,
			CPURequest:                 s.PodCPURequest,
			MemoryRequest:              s.PodMemoryRequest,
			CPULimit:                   s.PodCPULimit,
			MemoryLimit:                s.PodMemoryLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("init K8sPerUserGateway: %w", err)
		}
		log.Info().Str("namespace", s.PodNamespace).Int("max_pods", s.MaxKernels).
			Dur("idle_timeout", gw.IdleTimeout()).Msg("kernel gateway: k8s_per_user mode")
		return gw, nil
	default:
		return nil, fmt.Errorf("unsupported KERNEL_MODE: %q (use 'shared', 'docker_per_user', or 'k8s_per_user')", mode)
	}
}
