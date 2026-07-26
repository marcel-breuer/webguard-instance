package runner

import (
	"context"
	"slices"
	"testing"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func TestExecutorRegistryReturnsUniqueTypesInRegistrationOrder(t *testing.T) {
	t.Parallel()

	registry := newExecutorRegistry(
		functionExecutor{
			name:            "http",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypeHTTP, monitor.TypeKeyword},
		},
		functionExecutor{
			name:            "keyword-extension",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypeKeyword, monitor.TypePing},
		},
		functionExecutor{
			name:            "tls",
			phase:           PhaseSSL,
			monitoringTypes: []monitor.Type{monitor.TypeHTTP},
		},
	)

	got := registry.Types(PhaseResponse)
	want := []monitor.Type{monitor.TypeHTTP, monitor.TypeKeyword, monitor.TypePing}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected response types: got %#v want %#v", got, want)
	}

	if registry.Supports(PhaseResponse, monitor.TypePort) {
		t.Fatalf("expected port monitoring to be unsupported")
	}
	if !registry.Supports(PhaseSSL, monitor.TypeHTTP) {
		t.Fatalf("expected HTTP SSL monitoring to be supported")
	}
}

func TestExecutorRegistryExecutesOnlyMatchingPhaseAndType(t *testing.T) {
	t.Parallel()

	executed := false
	registry := newExecutorRegistry(functionExecutor{
		name:            "ping",
		phase:           PhaseResponse,
		monitoringTypes: []monitor.Type{monitor.TypePing},
		execute: func(_ context.Context, monitoring monitor.Monitoring) Execution {
			executed = true
			return responseExecution(monitoring.ID, monitor.StatusUp, nil, nil)
		},
	})

	execution, ok := registry.Execute(context.Background(), PhaseResponse, monitor.Monitoring{
		ID:   "ping-1",
		Type: monitor.TypePing,
	})
	if !ok || !executed || execution.Response == nil || execution.Response.Status != monitor.StatusUp {
		t.Fatalf("expected matching executor response, got %#v", execution)
	}

	if _, ok := registry.Execute(context.Background(), PhaseSSL, monitor.Monitoring{Type: monitor.TypePing}); ok {
		t.Fatalf("expected phase mismatch to remain unhandled")
	}
}
