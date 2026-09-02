package config

import (
	"testing"
	"time"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("BIND_ADDRESS", "")
	t.Setenv("WEBGUARD_CORE_API_KEY", "")
	t.Setenv("WEBGUARD_CORE_API_URL", "")
	t.Setenv("WEBGUARD_INSTANCE_API_BASE_PATH", "")
	t.Setenv("WEBGUARD_LOCATION", "")
	t.Setenv("WEBGUARD_INSTANCE_ID", "")
	t.Setenv("QUEUE_DEFAULT_WORKERS", "")
	t.Setenv("RUN_MAX_CONCURRENCY", "")
	t.Setenv("WEBGUARD_JOB_LEASES_ENABLED", "")
	t.Setenv("WEBGUARD_JOB_LEASES_DUAL_WRITE", "")
	t.Setenv("WEBGUARD_JOB_LEASE_MAX_BATCH", "")
	t.Setenv("WEBGUARD_ALLOW_PRIVATE_TARGETS", "")
	t.Setenv("SHUTDOWN_DRAIN_TIMEOUT_SECONDS", "")

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
	if cfg.WebGuardInstanceAPIBasePath != "/api/instances" {
		t.Fatalf("expected default instance API base path, got %q", cfg.WebGuardInstanceAPIBasePath)
	}
	if cfg.AllowPrivateTargets {
		t.Fatalf("expected private targets to be disabled by default")
	}
	if cfg.JobLeasesEnabled || cfg.JobLeasesDualWrite {
		t.Fatalf("expected job leases to be disabled by default")
	}
	if cfg.JobLeaseMaxBatch != 3 {
		t.Fatalf("expected default lease batch size 3, got %d", cfg.JobLeaseMaxBatch)
	}
	if cfg.ShutdownDrainTimeout != 10*time.Second {
		t.Fatalf("expected 10 second shutdown drain timeout, got %s", cfg.ShutdownDrainTimeout)
	}
}

func TestFromEnvCustomValues(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("BIND_ADDRESS", "127.0.0.1:9191")
	t.Setenv("WEBGUARD_CORE_API_KEY", "key")
	t.Setenv("WEBGUARD_CORE_API_URL", "https://core.example.com")
	t.Setenv("WEBGUARD_INSTANCE_API_BASE_PATH", "/api/instances")
	t.Setenv("WEBGUARD_LOCATION", "de-1")
	t.Setenv("WEBGUARD_INSTANCE_ID", "worker-de-1-a")
	t.Setenv("QUEUE_DEFAULT_WORKERS", "7")
	t.Setenv("RUN_MAX_CONCURRENCY", "4")
	t.Setenv("WEBGUARD_JOB_LEASES_ENABLED", "true")
	t.Setenv("WEBGUARD_JOB_LEASES_DUAL_WRITE", "true")
	t.Setenv("WEBGUARD_JOB_LEASE_MAX_BATCH", "9")
	t.Setenv("WEBGUARD_ALLOW_PRIVATE_TARGETS", "true")
	t.Setenv("SHUTDOWN_DRAIN_TIMEOUT_SECONDS", "12")

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
	if cfg.WebGuardInstanceAPIBasePath != "/api/instances" {
		t.Fatalf("unexpected instance API base path: %q", cfg.WebGuardInstanceAPIBasePath)
	}
	if cfg.WebGuardLocation != "de-1" {
		t.Fatalf("unexpected location: %q", cfg.WebGuardLocation)
	}
	if cfg.WebGuardInstanceID != "worker-de-1-a" {
		t.Fatalf("unexpected instance id: %q", cfg.WebGuardInstanceID)
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
	if !cfg.JobLeasesEnabled || !cfg.JobLeasesDualWrite {
		t.Fatalf("expected job leases and dual write to be enabled")
	}
	if cfg.JobLeaseMaxBatch != 9 {
		t.Fatalf("expected lease batch size 9, got %d", cfg.JobLeaseMaxBatch)
	}
	if cfg.ShutdownDrainTimeout != 12*time.Second {
		t.Fatalf("expected 12 second shutdown drain timeout, got %s", cfg.ShutdownDrainTimeout)
	}
}

func TestConfigReadinessRequiresCoreConfigurationAndLeaseIdentity(t *testing.T) {
	t.Parallel()

	ready := Config{WebGuardCoreAPIKey: "key", WebGuardCoreAPIURL: "https://core.example.test", WebGuardInstanceAPIBasePath: "/api/instances", WebGuardLocation: "de-1"}
	if !ready.IsReady() {
		t.Fatal("expected instance configuration to be ready")
	}
	if (Config{WebGuardCoreAPIKey: "key", WebGuardLocation: "de-1"}).IsReady() {
		t.Fatal("expected missing Core URL to be unready")
	}
	ready.JobLeasesEnabled = true
	if ready.IsReady() {
		t.Fatal("expected lease configuration without an instance ID to be unready")
	}
	ready.WebGuardInstanceID = "worker-de-1-a"
	if !ready.IsReady() {
		t.Fatal("expected complete lease configuration to be ready")
	}
	ready.WebGuardInstanceAPIBasePath = "/api/v1/internal/instances"
	if ready.IsReady() {
		t.Fatal("expected legacy path to be rejected for an instance")
	}
}

func TestFromEnvClampsShutdownDrainTimeout(t *testing.T) {
	t.Setenv("SHUTDOWN_DRAIN_TIMEOUT_SECONDS", "0")
	if cfg := FromEnv(); cfg.ShutdownDrainTimeout != time.Second {
		t.Fatalf("expected one second minimum drain timeout, got %s", cfg.ShutdownDrainTimeout)
	}
}
