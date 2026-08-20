package runner

import (
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func TestResponseCadenceStartsWebsiteChecksAtMostOncePerConfiguredInterval(t *testing.T) {
	t.Parallel()

	monitoring := monitor.Monitoring{ID: "website-1", CheckIntervalSeconds: 900}
	dueAt := nextDueResponseWindow(monitoring, "de-1", time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))
	cadence := newResponseCadence()

	if !cadence.tryStart(monitoring, "de-1", dueAt) {
		t.Fatal("expected first website check to start in its cadence window")
	}
	if cadence.tryStart(monitoring, "de-1", dueAt) {
		t.Fatal("expected duplicate website check to be rejected")
	}
	if cadence.tryStart(monitoring, "de-1", dueAt.Add(10*time.Minute)) {
		t.Fatal("expected website check before 15 minutes to be rejected")
	}
	if !cadence.tryStart(monitoring, "de-1", dueAt.Add(15*time.Minute)) {
		t.Fatal("expected website check to start after 15 minutes")
	}
}

func TestResponseCadenceKeepsLegacyChecksOnTheFiveMinuteSchedule(t *testing.T) {
	t.Parallel()

	monitoring := monitor.Monitoring{ID: "ping-1"}
	startedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	cadence := newResponseCadence()

	if !cadence.tryStart(monitoring, "de-1", startedAt) {
		t.Fatal("expected legacy response check to start")
	}
	if !cadence.tryStart(monitoring, "de-1", startedAt.Add(5*time.Minute)) {
		t.Fatal("expected legacy response check to remain on five-minute schedule")
	}
}

func nextDueResponseWindow(monitoring monitor.Monitoring, location string, start time.Time) time.Time {
	for offset := 0; offset < 3; offset++ {
		candidate := start.Add(time.Duration(offset) * 5 * time.Minute)
		if isScheduledResponseWindow(monitoring.ID, location, responseCheckInterval(monitoring), candidate) {
			return candidate
		}
	}

	panic("no response cadence window found")
}
