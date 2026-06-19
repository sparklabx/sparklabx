package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/sparklabx/sparklabx/backend/internal/config"
	"github.com/sparklabx/sparklabx/backend/internal/connectorauth"
	"github.com/sparklabx/sparklabx/backend/internal/database"
	"github.com/sparklabx/sparklabx/backend/internal/handlers"
	"github.com/sparklabx/sparklabx/backend/internal/routes"
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

	// Connector token signing key (app mints RS256 JWTs that connectors validate
	// via /api/v1/.well-known/jwks.json). Precedence: inline PEM (CONNECTOR_JWT_
	// PRIVATE_KEY, used by the Helm Secret), else a key file (CONNECTOR_JWT_
	// PRIVATE_KEY_FILE, optional), else generated once and persisted in the DB.
	// The DB default keeps the JWKS kid stable across restarts with no extra
	// volume/mount.
	connectorKeyPEM := cfg.ConnectorJWTPrivateKey
	keySource := "inline env"
	switch {
	case connectorKeyPEM != "":
		// inline
	case cfg.ConnectorJWTKeyFile != "":
		connectorKeyPEM, err = connectorauth.LoadOrCreatePEM(cfg.ConnectorJWTKeyFile)
		if err != nil {
			log.Fatal().Err(err).Str("file", cfg.ConnectorJWTKeyFile).Msg("failed to load/create connector signing key file")
		}
		keySource = "key file"
	default:
		connectorKeyPEM, err = connectorSigningKeyFromDB(minioIAM)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to load/create connector signing key in DB")
		}
		keySource = "database"
	}
	connectorKeys, err := connectorauth.New(connectorKeyPEM, cfg.ConnectorIssuer)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to init connector signing key")
	}
	log.Info().Str("source", keySource).Msg("connector signing key ready (stable JWKS)")
	authHandler.SetConnectorKeys(connectorKeys)

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

	// Kernel callback-token resolver — kernel spawn calls this to inject a
	// short-lived, narrowly-scoped CALLBACK token (typ=kernel; only reaches
	// /kernel/oidc-token + /connectors/:id/credentials). The kernel uses it to:
	//   - fetch a fresh OIDC access token for SSO passthrough (SSO users), and
	//   - fetch connector credentials (app-jwt minted as the user, or a
	//     broker-mapped username/password) for ANY login method.
	// Minted for every user — connectors must work regardless of how the user
	// logged in (the whole point of app-as-issuer). For non-SSO users
	// /kernel/oidc-token simply returns empty; connector credentials still work.
	oidcTokenResolver := func(adminID string) (string, error) {
		return authHandler.MintKernelToken(adminID)
	}

	// KernelGateway: shared single container OR per-user pod via KERNEL_MODE.
	kernelGateway, err := services.NewKernelGateway(services.KernelGatewaySettings{
		Mode:                       cfg.KernelMode,
		Environment:                cfg.Environment,
		JupyterGatewayURL:          cfg.JupyterGatewayURL,
		PodImage:                   cfg.KernelPodImage,
		PodNamespace:               cfg.KernelPodNamespace,
		DockerNetwork:              cfg.KernelDockerNetwork,
		MinIOEndpoint:              cfg.MinIOEndpoint,
		IdleTimeout:                time.Duration(cfg.KernelPodIdleMinutes) * time.Minute,
		MaxKernels:                 cfg.KernelPodMaxTotal,
		PullSecret:                 cfg.KernelPullSecret,
		CredsResolver:              credsResolver,
		OIDCTokenResolver:          oidcTokenResolver,
		KernelAPIURL:               cfg.KernelCallbackURL,
		ConnectorsManifestProvider: authHandler.ConnectorsKernelManifest,
		PodCPURequest:              cfg.KernelPodCPURequest,
		PodMemoryRequest:           cfg.KernelPodMemoryRequest,
		PodCPULimit:                cfg.KernelPodCPULimit,
		PodMemoryLimit:             cfg.KernelPodMemoryLimit,
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

	routes.Register(router, cfg, routes.Handlers{
		Auth:          authHandler,
		Storage:       storageHandler,
		Notebook:      notebookHandler,
		LocalKernel:   localKernelHandler,
		User:          userHandler,
		AllowedDomain: allowedDomainHandler,
	})

	addr := ":" + cfg.ServicePort
	log.Info().Str("addr", addr).Msg("server listening")
	if err := router.Run(addr); err != nil {
		log.Fatal().Err(err).Msg("server failed")
	}
}

// connectorSigningKeyFromDB loads the connector signing key from app_secrets,
// generating and persisting one on first boot. Encrypted at rest when IAM
// encryption is available ("enc:" prefix), else stored as plain PEM. Stable
// across restarts with no dedicated volume.
func connectorSigningKeyFromDB(iam *services.MinIOIAM) (string, error) {
	const key = "connector_jwt_private_key"
	db := database.GetDB()
	decode := func(stored string) (string, error) {
		if strings.HasPrefix(stored, "enc:") {
			if iam == nil {
				return "", fmt.Errorf("connector signing key is encrypted but encryption is unavailable")
			}
			return iam.DecryptSecret(strings.TrimPrefix(stored, "enc:"))
		}
		return stored, nil
	}
	var stored string
	if err := db.QueryRow(`SELECT value FROM app_secrets WHERE key = $1`, key).Scan(&stored); err == nil && stored != "" {
		return decode(stored)
	}
	pem, err := connectorauth.GeneratePEM()
	if err != nil {
		return "", err
	}
	toStore := pem
	if iam != nil {
		if enc, e := iam.EncryptSecret(pem); e == nil {
			toStore = "enc:" + enc
		}
	}
	// ON CONFLICT keeps the first writer's key if two instances race at boot.
	if _, err := db.Exec(`INSERT INTO app_secrets (key, value) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`, key, toStore); err != nil {
		return "", err
	}
	if err := db.QueryRow(`SELECT value FROM app_secrets WHERE key = $1`, key).Scan(&stored); err == nil && stored != "" {
		return decode(stored)
	}
	return pem, nil
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
