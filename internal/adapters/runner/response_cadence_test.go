package runner

import (
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func TestResponseDueImmediatelyWhenNoLocationResultExists(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	for _, interval := range []int{60, 300, 600, 900, 1800, 3600} {
		monitoring := monitor.Monitoring{ID: "website", CheckIntervalSeconds: interval}
		if !responseDue(monitoring, now) {
			t.Errorf("expected first response to be due immediately for interval %d seconds", interval)
		}
	}
}

func TestResponseDueUsesLastResponseAndSelectedInterval(t *testing.T) {
	t.Parallel()

	lastResponseAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	for _, interval := range []int{60, 300, 600, 900, 1800, 3600} {
		monitoring := monitor.Monitoring{
			ID:                   "website",
			CheckIntervalSeconds: interval,
			LastResponseAt:       &lastResponseAt,
		}
		intervalDuration := time.Duration(interval) * time.Second

		if responseDue(monitoring, lastResponseAt.Add(intervalDuration-time.Nanosecond)) {
			t.Errorf("interval %d seconds became due before the full selected interval", interval)
		}
		if !responseDue(monitoring, lastResponseAt.Add(intervalDuration)) {
			t.Errorf("interval %d seconds was not due at the selected interval", interval)
		}
	}
}

func TestResponseCadencePreventsDuplicateStartsPerLocation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	monitoring := monitor.Monitoring{ID: "website-1", CheckIntervalSeconds: 900}
	cadence := newResponseCadence()

	if !cadence.tryStart(monitoring, "de-1", now) {
		t.Fatal("expected first response check to start immediately")
	}
	if cadence.tryStart(monitoring, "de-1", now) {
		t.Fatal("expected duplicate response check for the same location to be rejected")
	}
	if !cadence.tryStart(monitoring, "us-1", now) {
		t.Fatal("expected a location with no prior response to start independently")
	}
	if cadence.tryStart(monitoring, "de-1", now.Add(15*time.Minute-time.Nanosecond)) {
		t.Fatal("expected response check before the selected interval to be rejected")
	}
	if !cadence.tryStart(monitoring, "de-1", now.Add(15*time.Minute)) {
		t.Fatal("expected response check at the selected interval")
	}
}

func TestResponseCheckIntervalKeepsLegacyFiveMinuteDefault(t *testing.T) {
	t.Parallel()

	for _, intervalSeconds := range []int{0, 30, 59} {
		if got := responseCheckInterval(monitor.Monitoring{CheckIntervalSeconds: intervalSeconds}); got != 5*time.Minute {
			t.Errorf("expected legacy 5-minute interval for %d seconds, got %s", intervalSeconds, got)
		}
	}
}
