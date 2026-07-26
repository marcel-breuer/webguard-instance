package config

import "testing"

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("BIND_ADDRESS", "")
	t.Setenv("WEBGUARD_CORE_API_KEY", "")
	t.Setenv("WEBGUARD_CORE_API_URL", "")
	t.Setenv("WEBGUARD_LOCATION", "")
	t.Setenv("QUEUE_DEFAULT_WORKERS", "")
	t.Setenv("RUN_MAX_CONCURRENCY", "")
	t.Setenv("WEBGUARD_ALLOW_PRIVATE_TARGETS", "")

	cfg := FromEnv()

	if cfg.Address != ":8080" {
		t.Fatalf("expected default address :8080, got %q", cfg.Address)
	}
	if cfg.QueueDefaultWorkers != 3 {
		t.Fatalf("expected default workers 3, got %d", cfg.QueueDefaultWorkers)
	}
	if cfg.RunMaxConcurrency != 3 {
		t.Fatalf("expected default max concurrency 3, got %d", cfg.RunMaxConcurrency)
	}
	if cfg.AllowPrivateTargets {
		t.Fatalf("expected private targets to be disabled by default")
	}
}

func TestFromEnvCustomValues(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("BIND_ADDRESS", "127.0.0.1:9191")
	t.Setenv("WEBGUARD_CORE_API_KEY", "key")
	t.Setenv("WEBGUARD_CORE_API_URL", "https://core.example.com")
	t.Setenv("WEBGUARD_LOCATION", "de-1")
	t.Setenv("QUEUE_DEFAULT_WORKERS", "7")
	t.Setenv("RUN_MAX_CONCURRENCY", "4")
	t.Setenv("WEBGUARD_ALLOW_PRIVATE_TARGETS", "true")

	cfg := FromEnv()

	if cfg.Address != "127.0.0.1:9191" {
		t.Fatalf("expected bind address override, got %q", cfg.Address)
	}
	if cfg.WebGuardCoreAPIKey != "key" {
		t.Fatalf("unexpected api key: %q", cfg.WebGuardCoreAPIKey)
	}
	if cfg.WebGuardCoreAPIURL != "https://core.example.com" {
		t.Fatalf("unexpected core url: %q", cfg.WebGuardCoreAPIURL)
	}
	if cfg.WebGuardLocation != "de-1" {
		t.Fatalf("unexpected location: %q", cfg.WebGuardLocation)
	}
	if cfg.QueueDefaultWorkers != 7 {
		t.Fatalf("expected workers 7, got %d", cfg.QueueDefaultWorkers)
	}
	if cfg.RunMaxConcurrency != 4 {
		t.Fatalf("expected max concurrency 4, got %d", cfg.RunMaxConcurrency)
	}
	if !cfg.AllowPrivateTargets {
		t.Fatalf("expected private targets to be enabled")
	}
}
