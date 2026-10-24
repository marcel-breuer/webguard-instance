package runner

import (
	"testing"
	"time"
)

func TestAuxiliaryPhasesKeepFiveMinuteCadence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	service := &MonitoringService{
		now:           func() time.Time { return now },
		phaseLastRuns: make(map[ExecutionPhase]time.Time),
	}

	if !service.allowAuxiliaryPhase(PhaseSSL) {
		t.Fatal("expected the initial SSL phase to run")
	}
	now = now.Add(time.Minute)
	if service.allowAuxiliaryPhase(PhaseSSL) {
		t.Fatal("expected the SSL phase to wait for its five-minute interval")
	}
	if !service.allowAuxiliaryPhase(PhaseDomainExpiration) {
		t.Fatal("expected the domain phase to keep an independent schedule")
	}
	now = now.Add(4 * time.Minute)
	if !service.allowAuxiliaryPhase(PhaseSSL) {
		t.Fatal("expected the SSL phase to run once five minutes have passed")
	}
}
