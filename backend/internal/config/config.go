package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Database
	DatabaseURL string

	// JWT
	JWTSecretKey    string
	JWTExpireMinutes int

	// Seed admin
	SeedAdminUsername string
	SeedAdminEmail    string
	SeedAdminPassword string

	// Google OAuth2
	GoogleClientID     string
	GoogleClientSecret string

	// Microsoft OAuth2
	MicrosoftClientID     string
	MicrosoftClientSecret string

	// AWS
	AWSProfile     string
	TFStateBucket  string
	TFStateRegion  string

	// Service
	ServiceName string
	ServicePort string
	Environment string
	LogLevel    string

	// MinIO (local S3-compatible storage for grading)
	MinIOEndpoint        string
	MinIOAccessKey       string
	MinIOSecretKey       string
	MinIOWorkspaceBucket string // single shared bucket; users isolated via prefix

	// Jupyter
	JupyterGatewayURL string

	// Kernel deployment (see KERNEL_MODE in .env.example)
	KernelMode            string
	KernelPodImage        string
	KernelPodNamespace    string
	KernelPodIdleMinutes  int
	KernelPodMaxTotal     int
	KernelDockerNetwork   string
	KernelPullSecret      string // optional K8s imagePullSecret for private forks

	// CORS
	CORSOrigins []string
}

func Load() *Config {
	return &Config{
		DatabaseURL:      getEnv("DATABASE_URL", ""),
		JWTSecretKey:     getEnv("JWT_SECRET_KEY", ""),
		JWTExpireMinutes: getEnvInt("JWT_EXPIRE_MINUTES", 60),

		SeedAdminUsername: getEnv("SEED_ADMIN_USERNAME", ""),
		SeedAdminEmail:    getEnv("SEED_ADMIN_EMAIL", ""),
		SeedAdminPassword: getEnv("SEED_ADMIN_PASSWORD", ""),

		GoogleClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
		GoogleClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),

		MicrosoftClientID:     getEnv("MICROSOFT_CLIENT_ID", ""),
		MicrosoftClientSecret: getEnv("MICROSOFT_CLIENT_SECRET", ""),

		AWSProfile:    getEnv("AWS_PROFILE", ""),
		TFStateBucket: getEnv("TF_STATE_BUCKET", ""),
		TFStateRegion: getEnv("TF_STATE_REGION", ""),

		ServiceName: getEnv("SERVICE_NAME", "sparklabx"),
		ServicePort: getEnv("SERVICE_PORT", "10000"),
		Environment: getEnv("ENVIRONMENT", "development"),
		LogLevel:    getEnv("LOG_LEVEL", "debug"),

		MinIOEndpoint:        getEnv("MINIO_ENDPOINT", ""),
		MinIOAccessKey:       getEnv("MINIO_ACCESS_KEY", ""),
		MinIOSecretKey:       getEnv("MINIO_SECRET_KEY", ""),
		MinIOWorkspaceBucket: getEnv("MINIO_WORKSPACE_BUCKET", "workspace"),

		JupyterGatewayURL: getEnv("JUPYTER_GATEWAY_URL", "http://jupyter:8888"),

		KernelMode:           getEnv("KERNEL_MODE", "shared"),
		KernelPodImage:       getEnv("KERNEL_POD_IMAGE", "ghcr.io/sparklabx/kernel:latest"),
		KernelPodNamespace:   getEnv("KERNEL_POD_NAMESPACE", "sparklabx"),
		KernelPodIdleMinutes: getEnvInt("KERNEL_POD_IDLE_MINUTES", 30),
		KernelPodMaxTotal:    getEnvInt("KERNEL_POD_MAX_TOTAL", 50),
		KernelDockerNetwork:  getEnv("KERNEL_DOCKER_NETWORK", "sparklabx_default"),
		KernelPullSecret:     getEnv("KERNEL_PULL_SECRET", ""), // empty → no imagePullSecret

		CORSOrigins: strings.Split(getEnv("CORS_ORIGINS", "http://localhost:3000"), ","),
	}
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return fallback
}
