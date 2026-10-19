package config

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxQueueWorkers    = 64
	maxRunConcurrency  = 64
	maxJobLeaseBatch   = 256
	maxAPIKeyFileBytes = 8 << 10
)

type Config struct {
	WebGuardCoreAPIKey          string
	WebGuardCoreAPIKeyFile      string
	WebGuardCoreAPIURL          string
	WebGuardInstanceAPIBasePath string
	WebGuardLocation            string
	WebGuardInstanceID          string

	QueueDefaultWorkers  int
	RunMaxConcurrency    int
	JobLeasesEnabled     bool
	JobLeasesDualWrite   bool
	JobLeaseMaxBatch     int
	AllowPrivateTargets  bool
	ShutdownDrainTimeout time.Duration

	Address string
}

func FromEnv() Config {
	port := env("PORT", "8080")
	drainTimeoutSeconds := max(1, envInt("SHUTDOWN_DRAIN_TIMEOUT_SECONDS", 10))
	queueWorkers := clamp(envInt("QUEUE_DEFAULT_WORKERS", 3), 1, maxQueueWorkers)
	runConcurrency := clamp(envInt("RUN_MAX_CONCURRENCY", queueWorkers), 1, maxRunConcurrency)
	return Config{
		WebGuardCoreAPIKey:          env("WEBGUARD_CORE_API_KEY", ""),
		WebGuardCoreAPIKeyFile:      env("WEBGUARD_CORE_API_KEY_FILE", ""),
		WebGuardCoreAPIURL:          env("WEBGUARD_CORE_API_URL", ""),
		WebGuardInstanceAPIBasePath: env("WEBGUARD_INSTANCE_API_BASE_PATH", "/api/instances"),
		WebGuardLocation:            env("WEBGUARD_LOCATION", ""),
		WebGuardInstanceID:          env("WEBGUARD_INSTANCE_ID", ""),

		QueueDefaultWorkers:  queueWorkers,
		RunMaxConcurrency:    runConcurrency,
		JobLeasesEnabled:     envBool("WEBGUARD_JOB_LEASES_ENABLED", false),
		JobLeasesDualWrite:   envBool("WEBGUARD_JOB_LEASES_DUAL_WRITE", false),
		JobLeaseMaxBatch:     clamp(envInt("WEBGUARD_JOB_LEASE_MAX_BATCH", queueWorkers), 1, maxJobLeaseBatch),
		AllowPrivateTargets:  envBool("WEBGUARD_ALLOW_PRIVATE_TARGETS", false),
		ShutdownDrainTimeout: time.Duration(drainTimeoutSeconds) * time.Second,

		Address: env("BIND_ADDRESS", ":"+port),
	}
}

func (c Config) IsReady() bool {
	if strings.TrimSpace(c.WebGuardCoreAPIKey) == "" && strings.TrimSpace(c.WebGuardCoreAPIKeyFile) == "" {
		return false
	}
	if keyFile := strings.TrimSpace(c.WebGuardCoreAPIKeyFile); keyFile != "" {
		file, err := os.Open(keyFile)
		if err != nil {
			return false
		}
		content, readErr := io.ReadAll(io.LimitReader(file, maxAPIKeyFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(content) > maxAPIKeyFileBytes || strings.TrimSpace(string(content)) == "" {
			return false
		}
	}
	if strings.TrimSpace(c.WebGuardCoreAPIURL) == "" || strings.TrimSpace(c.WebGuardLocation) == "" {
		return false
	}
	if !isSupportedInstanceAPIBasePath(c.WebGuardInstanceAPIBasePath) {
		return false
	}
	return !c.JobLeasesEnabled || strings.TrimSpace(c.WebGuardInstanceID) != ""
}

func clamp(value, minimum, maximum int) int {
	return min(maximum, max(minimum, value))
}

func isSupportedInstanceAPIBasePath(path string) bool {
	normalized := "/" + strings.Trim(strings.TrimSpace(path), "/")
	return normalized == "/api/instances"
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
