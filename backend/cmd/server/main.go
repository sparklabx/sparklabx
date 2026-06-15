package main

import (
	"os"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/handlers"
	"github.com/sparklabx/sparklabx/backend/internal/middleware"
	"github.com/sparklabx/sparklabx/backend/internal/services"
)

func main() {
	cfg := config.Load()
	setupLogging(cfg)

	if cfg.JWTSecretKey == "" {
		log.Fatal().Msg("JWT_SECRET_KEY must be set")
	}
	if len(cfg.JWTSecretKey) < 32 {
		log.Warn().Msg("JWT_SECRET_KEY is shorter than 32 characters — use a stronger key in production")
	}

	log.Info().
		Str("service", cfg.ServiceName).
		Str("environment", cfg.Environment).
		Str("port", cfg.ServicePort).
		Msg("starting server")

	if err := database.Init(cfg); err != nil {
		log.Fatal().Err(err).Msg("failed to initialize database")
	}
	defer database.Close()

	if err := database.MigrateAndSeed(cfg); err != nil {
		log.Fatal().Err(err).Msg("failed to run migrations")
	}

	storageHandler := handlers.NewStorageHandler(cfg)
	storageHandler.EnsureWorkspaceBucket()

	// MinIO IAM: per-user accounts + scoped policies for true kernel isolation.
	// Nil if MinIO not configured — auth + kernel pods then fall back to no creds
	// (storage features simply unavailable).
	minioIAM, err := services.NewMinIOIAM(
		cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey,
		cfg.MinIOWorkspaceBucket, cfg.JWTSecretKey,
	)
	if err != nil {
		log.Warn().Err(err).Msg("MinIO IAM init failed — per-user provisioning disabled")
	}
	authHandler := handlers.NewAuthHandler(cfg, minioIAM)

	// Per-user MinIO creds resolver — kernel pod gateway calls this at spawn time.
	// Returns ("", "", nil) when IAM is not configured, signaling fall-back to
	// root creds (no isolation, dev/docker-compose).
	credsResolver := func(adminID string) (string, string, error) {
		if minioIAM == nil {
			return "", "", nil
		}
		db := database.GetDB()
		var username string
		if err := db.QueryRow("SELECT username FROM admins WHERE id = $1", adminID).Scan(&username); err != nil {
			return "", "", err
		}
		secret, err := handlers.EnsureUserMinIOSecret(minioIAM, adminID, username)
		if err != nil {
			return "", "", err
		}
		return username, secret, nil
	}

	// KernelGateway: shared single container OR per-user pod via KERNEL_MODE.
	kernelGateway, err := services.NewKernelGateway(services.KernelGatewaySettings{
		Mode:              cfg.KernelMode,
		Environment:       cfg.Environment,
		JupyterGatewayURL: cfg.JupyterGatewayURL,
		PodImage:          cfg.KernelPodImage,
		PodNamespace:      cfg.KernelPodNamespace,
		DockerNetwork:     cfg.KernelDockerNetwork,
		MinIOEndpoint:     cfg.MinIOEndpoint,
		IdleTimeout:       time.Duration(cfg.KernelPodIdleMinutes) * time.Minute,
		MaxKernels:        cfg.KernelPodMaxTotal,
		PullSecret:        cfg.KernelPullSecret,
		CredsResolver:     credsResolver,
		PodCPURequest:     cfg.KernelPodCPURequest,
		PodMemoryRequest:  cfg.KernelPodMemoryRequest,
		PodCPULimit:       cfg.KernelPodCPULimit,
		PodMemoryLimit:    cfg.KernelPodMemoryLimit,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize kernel gateway")
	}
	localKernelHandler := handlers.NewLocalKernelHandler(cfg, kernelGateway)
	handlers.LoadKernelMapFromDB()
	notebookHandler := handlers.NewNotebookHandler()
	userHandler := handlers.NewUserManagementHandler()
	allowedDomainHandler := handlers.NewAllowedDomainHandler()

	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.MaxMultipartMemory = 2 << 30 // 2GB max upload

	router.Use(gin.Recovery())
	router.Use(requestLogger())
	corsConfig := cors.Config{
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		ExposeHeaders:    []string{"Content-Length"},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
	}

	allowAll := false
	for _, o := range cfg.CORSOrigins {
		if o == "*" {
			allowAll = true
			break
		}
	}

	if allowAll {
		if cfg.Environment == "production" {
			log.Warn().Msg("SECURITY WARNING: CORS_ORIGINS is set to '*' in production environment. This makes the application vulnerable to CSRF and CSWSH attacks. Please configure a specific domain name.")
		}
		corsConfig.AllowAllOrigins = true
	} else {
		corsConfig.AllowOrigins = cfg.CORSOrigins
	}

	router.Use(cors.New(corsConfig))

	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok", "service": cfg.ServiceName, "time": time.Now().UTC()})
	})

	v1 := router.Group("/api/v1")
	{
		// Auth — admin only (no student flow in notebook-lite)
		authLimiter := middleware.RateLimit(10, time.Minute)
		v1.POST("/admin/login", authLimiter, authHandler.Login)
		v1.POST("/auth/google", authLimiter, authHandler.GoogleLogin)
		v1.POST("/auth/microsoft", authLimiter, authHandler.MicrosoftLogin)
		// Generic OIDC SSO (enterprise IdP via env). /auth/config tells the login
		// page whether to show the SSO button; /auth/oidc/* is the code flow.
		v1.GET("/auth/config", authHandler.AuthConfig)
		v1.GET("/auth/oidc/start", authLimiter, authHandler.OIDCStart)
		v1.GET("/auth/oidc/callback", authLimiter, authHandler.OIDCCallback)

		admin := v1.Group("")
		admin.Use(middleware.RequireAdmin(cfg))
		{
			admin.GET("/admin/me", authHandler.Me)

			// User management
			admin.GET("/admin/users", userHandler.ListAdmins)
			admin.POST("/admin/users", userHandler.CreateAdmin)
			admin.DELETE("/admin/users/:id", userHandler.DeleteAdmin)
			admin.PUT("/admin/users/:id/password", userHandler.ResetPassword)
			admin.PUT("/admin/users/:id/role", userHandler.UpdateRole)

			// MinIO browser
			admin.GET("/minio/buckets", storageHandler.MinIOListBuckets)
			admin.PUT("/minio/buckets/:bucket", storageHandler.MinIOCreateBucket)
			admin.DELETE("/minio/buckets/:bucket", storageHandler.MinIODeleteBucket)
			admin.GET("/minio/buckets/:bucket/objects", storageHandler.MinIOListObjects)
			admin.POST("/minio/buckets/:bucket/upload", storageHandler.MinIOUploadObject)
			admin.POST("/minio/buckets/:bucket/folder", storageHandler.MinIOCreateFolder)
			admin.GET("/minio/buckets/:bucket/download", storageHandler.MinIODownloadObject)
			admin.DELETE("/minio/buckets/:bucket/objects", storageHandler.MinIODeleteObject)

			// OAuth allowlist — superadmin writes only
			admin.GET("/allowed-domains", allowedDomainHandler.List)
			admin.POST("/allowed-domains", middleware.RequireSuperAdmin(), allowedDomainHandler.Create)
			admin.PATCH("/allowed-domains/:id", middleware.RequireSuperAdmin(), allowedDomainHandler.Update)
			admin.DELETE("/allowed-domains/:id", middleware.RequireSuperAdmin(), allowedDomainHandler.Delete)
		}

		// Notebooks — admin only in this lite build
		nb := v1.Group("/notebooks")
		nb.Use(middleware.RequireAdmin(cfg))
		{
			nb.GET("/kernel/specs", notebookHandler.KernelSpecs)

			// Per-user storage (each admin has their own users/<adminID>/ prefix)
			nb.GET("/storage/path", storageHandler.GetUserDataPath)
			nb.GET("/storage/files", storageHandler.ListUserFiles)
			nb.POST("/storage/upload", storageHandler.UploadUserFile)
			nb.POST("/storage/create-folder", storageHandler.CreateUserFolder)
			nb.DELETE("/storage/files/:filename", storageHandler.DeleteUserFile)
			nb.GET("/storage/files/:filename/download", storageHandler.DownloadUserFile)

			nb.GET("", notebookHandler.ListNotebooks)
			nb.POST("", notebookHandler.CreateNotebook)
			nb.POST("/import", notebookHandler.ImportNotebook)
			nb.GET("/:id", notebookHandler.GetNotebook)
			nb.PUT("/:id", notebookHandler.UpdateNotebook)
			nb.DELETE("/:id", notebookHandler.DeleteNotebook)
			nb.GET("/:id/export/html", notebookHandler.ExportNotebookHTML)
			nb.POST("/:id/cells", notebookHandler.CreateCell)
			nb.PUT("/:id/cells/:cellId", notebookHandler.UpdateCell)
			nb.DELETE("/:id/cells/:cellId", notebookHandler.DeleteCell)
			nb.POST("/:id/cells/reorder", notebookHandler.ReorderCells)

			// Local kernel proxy
			nb.POST("/:id/kernel/connect", localKernelHandler.Connect)
			nb.GET("/:id/kernel/status", localKernelHandler.Status)
			nb.POST("/:id/kernel/interrupt", localKernelHandler.Interrupt)
			nb.GET("/:id/kernel/active-executions", localKernelHandler.ActiveExecutions)
			nb.DELETE("/:id/kernel/disconnect", localKernelHandler.Disconnect)
			nb.DELETE("/:id/kernel/shutdown", localKernelHandler.Shutdown)
			nb.Any("/:id/kernel/ws/:kernelId/*path", localKernelHandler.WebSocket)
			nb.Any("/:id/kernel/api/*path", localKernelHandler.ProxyHTTP)
		}

		// Per-user pod spawn progress (polled by FE)
		kernelMeta := v1.Group("/kernel")
		kernelMeta.Use(middleware.RequireAdmin(cfg))
		{
			kernelMeta.GET("/spawn-status", localKernelHandler.SpawnStatus)
			kernelMeta.GET("/usage", localKernelHandler.Usage)
			kernelMeta.GET("/resource-presets", localKernelHandler.ResourcePresets)
			kernelMeta.GET("/library-errors", localKernelHandler.LibraryErrors)
		}
	}

	addr := ":" + cfg.ServicePort
	log.Info().Str("addr", addr).Msg("server listening")
	if err := router.Run(addr); err != nil {
		log.Fatal().Err(err).Msg("server failed")
	}
}

func setupLogging(cfg *config.Config) {
	if cfg.Environment != "production" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})
	}
	switch cfg.LogLevel {
	case "trace":
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "info":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		c.Next()
		latency := time.Since(start)
		status := c.Writer.Status()

		logger := log.With().
			Int("status", status).
			Str("method", c.Request.Method).
			Str("path", path).
			Dur("latency", latency).
			Str("client_ip", c.ClientIP()).
			Logger()

		if status >= 500 {
			logger.Error().Msg("request completed")
		} else if status >= 400 {
			logger.Warn().Msg("request completed")
		} else {
			logger.Debug().Msg("request completed")
		}
	}
}
