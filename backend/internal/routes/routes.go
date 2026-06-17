// Package routes wires every HTTP route onto the gin engine, grouped by concern
// (auth, admin, notebooks, kernel, connectors). main.go constructs the handlers
// and calls Register — keeping the route map in one discoverable place and the
// entrypoint minimal.
package routes

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sparklabx/sparklabx/backend/internal/config"
	"github.com/sparklabx/sparklabx/backend/internal/handlers"
	"github.com/sparklabx/sparklabx/backend/internal/middleware"
)

// Handlers bundles the constructed handlers the routes depend on.
type Handlers struct {
	Auth          *handlers.AuthHandler
	Storage       *handlers.StorageHandler
	Notebook      *handlers.NotebookHandler
	LocalKernel   *handlers.LocalKernelHandler
	User          *handlers.UserManagementHandler
	AllowedDomain *handlers.AllowedDomainHandler
}

// Register mounts /health and the whole /api/v1 surface onto router.
func Register(router *gin.Engine, cfg *config.Config, h Handlers) {
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": cfg.ServiceName, "time": time.Now().UTC()})
	})

	v1 := router.Group("/api/v1")
	registerAuth(v1, cfg, h)
	registerAdmin(v1, cfg, h)
	registerNotebooks(v1, cfg, h)
	registerKernel(v1, cfg, h)
	registerConnectors(v1, cfg, h)
}

// registerAuth — public login + SSO endpoints (admin only; no student flow).
func registerAuth(v1 *gin.RouterGroup, cfg *config.Config, h Handlers) {
	authLimiter := middleware.RateLimit(10, time.Minute)
	v1.POST("/admin/login", authLimiter, h.Auth.Login)
	v1.POST("/auth/google", authLimiter, h.Auth.GoogleLogin)
	v1.POST("/auth/microsoft", authLimiter, h.Auth.MicrosoftLogin)
	// Generic OIDC SSO (enterprise IdP via env). /auth/config tells the login
	// page whether to show the SSO button; /auth/oidc/* is the code flow.
	v1.GET("/auth/config", h.Auth.AuthConfig)
	v1.GET("/auth/oidc/start", authLimiter, h.Auth.OIDCStart)
	v1.GET("/auth/oidc/callback", authLimiter, h.Auth.OIDCCallback)
}

// registerAdmin — admin session: profile, user management, MinIO browser,
// OAuth allowlist (superadmin writes only).
func registerAdmin(v1 *gin.RouterGroup, cfg *config.Config, h Handlers) {
	admin := v1.Group("")
	admin.Use(middleware.RequireAdmin(cfg))

	admin.GET("/admin/me", h.Auth.Me)

	// User management
	admin.GET("/admin/users", h.User.ListAdmins)
	admin.POST("/admin/users", h.User.CreateAdmin)
	admin.DELETE("/admin/users/:id", h.User.DeleteAdmin)
	admin.PUT("/admin/users/:id/password", h.User.ResetPassword)
	admin.PUT("/admin/users/:id/role", h.User.UpdateRole)

	// MinIO browser
	admin.GET("/minio/buckets", h.Storage.MinIOListBuckets)
	admin.PUT("/minio/buckets/:bucket", h.Storage.MinIOCreateBucket)
	admin.DELETE("/minio/buckets/:bucket", h.Storage.MinIODeleteBucket)
	admin.GET("/minio/buckets/:bucket/objects", h.Storage.MinIOListObjects)
	admin.POST("/minio/buckets/:bucket/upload", h.Storage.MinIOUploadObject)
	admin.POST("/minio/buckets/:bucket/folder", h.Storage.MinIOCreateFolder)
	admin.GET("/minio/buckets/:bucket/download", h.Storage.MinIODownloadObject)
	admin.DELETE("/minio/buckets/:bucket/objects", h.Storage.MinIODeleteObject)

	// OAuth allowlist — superadmin writes only
	admin.GET("/allowed-domains", h.AllowedDomain.List)
	admin.POST("/allowed-domains", middleware.RequireSuperAdmin(), h.AllowedDomain.Create)
	admin.PATCH("/allowed-domains/:id", middleware.RequireSuperAdmin(), h.AllowedDomain.Update)
	admin.DELETE("/allowed-domains/:id", middleware.RequireSuperAdmin(), h.AllowedDomain.Delete)
}

// registerNotebooks — notebooks, per-user storage, cells, local kernel proxy
// (admin only in this lite build).
func registerNotebooks(v1 *gin.RouterGroup, cfg *config.Config, h Handlers) {
	nb := v1.Group("/notebooks")
	nb.Use(middleware.RequireAdmin(cfg))

	nb.GET("/kernel/specs", h.Notebook.KernelSpecs)

	// Per-user storage (each admin has their own users/<adminID>/ prefix)
	nb.GET("/storage/path", h.Storage.GetUserDataPath)
	nb.GET("/storage/files", h.Storage.ListUserFiles)
	nb.POST("/storage/upload", h.Storage.UploadUserFile)
	nb.POST("/storage/create-folder", h.Storage.CreateUserFolder)
	nb.DELETE("/storage/files/:filename", h.Storage.DeleteUserFile)
	nb.GET("/storage/files/:filename/download", h.Storage.DownloadUserFile)

	nb.GET("", h.Notebook.ListNotebooks)
	nb.POST("", h.Notebook.CreateNotebook)
	nb.POST("/import", h.Notebook.ImportNotebook)
	nb.GET("/:id", h.Notebook.GetNotebook)
	nb.PUT("/:id", h.Notebook.UpdateNotebook)
	nb.DELETE("/:id", h.Notebook.DeleteNotebook)
	nb.GET("/:id/export/html", h.Notebook.ExportNotebookHTML)
	nb.POST("/:id/cells", h.Notebook.CreateCell)
	nb.PUT("/:id/cells/:cellId", h.Notebook.UpdateCell)
	nb.DELETE("/:id/cells/:cellId", h.Notebook.DeleteCell)
	nb.POST("/:id/cells/reorder", h.Notebook.ReorderCells)

	// Local kernel proxy
	nb.POST("/:id/kernel/connect", h.LocalKernel.Connect)
	nb.GET("/:id/kernel/status", h.LocalKernel.Status)
	nb.POST("/:id/kernel/interrupt", h.LocalKernel.Interrupt)
	nb.GET("/:id/kernel/active-executions", h.LocalKernel.ActiveExecutions)
	nb.DELETE("/:id/kernel/disconnect", h.LocalKernel.Disconnect)
	nb.DELETE("/:id/kernel/shutdown", h.LocalKernel.Shutdown)
	nb.Any("/:id/kernel/ws/:kernelId/*path", h.LocalKernel.WebSocket)
	nb.Any("/:id/kernel/api/*path", h.LocalKernel.ProxyHTTP)
}

// registerKernel — Spark UI proxy + per-user pod spawn meta + the kernel-token
// OIDC refresh endpoint.
func registerKernel(v1 *gin.RouterGroup, cfg *config.Config, h Handlers) {
	// Spark UI proxy — loaded in an iframe, so it authenticates via ?token=
	// (then a path-scoped cookie for the UI's own asset/XHR requests), NOT the
	// header-only RequireAdmin guard. The notebook owner check runs inside the
	// handler. Registered on v1 (not the nb group) for that reason.
	v1.GET("/notebooks/:id/kernel/spark-ui/*path", h.LocalKernel.ProxySparkUI)

	// Per-user pod spawn progress (polled by FE)
	kernelMeta := v1.Group("/kernel")
	kernelMeta.Use(middleware.RequireAdmin(cfg))
	{
		kernelMeta.GET("/spawn-status", h.LocalKernel.SpawnStatus)
		kernelMeta.GET("/usage", h.LocalKernel.Usage)
		kernelMeta.GET("/resource-presets", h.LocalKernel.ResourcePresets)
		kernelMeta.GET("/library-errors", h.LocalKernel.LibraryErrors)
	}

	// Kernel fetches a fresh OIDC token here (in-session refresh for SSO token
	// passthrough to external services like Trino). Authenticated by the
	// short-lived kernel token (typ="kernel"), NOT a full admin session — so the
	// token living in the kernel pod's env can only reach this endpoint.
	v1.GET("/kernel/oidc-token", middleware.RequireKernelToken(cfg), h.Auth.KernelOIDCToken)
}

// registerConnectors — the generic data-connector layer (docs/connectors-design.md)
// plus the back-compat Trino metadata alias.
func registerConnectors(v1 *gin.RouterGroup, cfg *config.Config, h Handlers) {
	// Back-compat Trino catalog browser (superseded by /connectors/:id/metadata).
	v1.GET("/trino/metadata", middleware.RequireAdmin(cfg), h.Auth.TrinoMetadata)

	// JWKS so connectors can validate app-minted (app-jwt) tokens (public).
	v1.GET("/.well-known/jwks.json", h.Auth.ConnectorJWKS)

	v1.GET("/connectors", middleware.RequireAdmin(cfg), h.Auth.ListConnectors)
	v1.GET("/connector-types", middleware.RequireAdmin(cfg), h.Auth.ConnectorTypes)
	v1.GET("/connectors/:id/metadata", middleware.RequireAdmin(cfg), h.Auth.ConnectorMetadata)
	// Per-query credential, called by the kernel (kernel token).
	v1.GET("/connectors/:id/credentials", middleware.RequireKernelToken(cfg), h.Auth.ConnectorCredentials)
	// Add/test/edit/remove connectors. Any admin manages their own personal
	// sources — enforced in the handlers.
	v1.POST("/connectors", middleware.RequireAdmin(cfg), h.Auth.CreateConnector)
	v1.POST("/connectors/test", middleware.RequireAdmin(cfg), h.Auth.TestConnector)
	v1.GET("/connectors/:id", middleware.RequireAdmin(cfg), h.Auth.GetConnector)
	v1.PUT("/connectors/:id", middleware.RequireAdmin(cfg), h.Auth.UpdateConnector)
	v1.DELETE("/connectors/:id", middleware.RequireAdmin(cfg), h.Auth.DeleteConnector)
}
