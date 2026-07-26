package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	WebGuardCoreAPIKey string
	WebGuardCoreAPIURL string
	WebGuardLocation   string

	QueueDefaultWorkers int
	RunMaxConcurrency   int
	AllowPrivateTargets bool

	Address string
}

func FromEnv() Config {
	port := env("PORT", "8080")
	return Config{
		WebGuardCoreAPIKey: env("WEBGUARD_CORE_API_KEY", ""),
		WebGuardCoreAPIURL: env("WEBGUARD_CORE_API_URL", ""),
		WebGuardLocation:   env("WEBGUARD_LOCATION", ""),

		QueueDefaultWorkers: envInt("QUEUE_DEFAULT_WORKERS", 3),
		RunMaxConcurrency:   envInt("RUN_MAX_CONCURRENCY", envInt("QUEUE_DEFAULT_WORKERS", 3)),
		AllowPrivateTargets: envBool("WEBGUARD_ALLOW_PRIVATE_TARGETS", false),

		Address: env("BIND_ADDRESS", ":"+port),
	}
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
