package application

import (
	"errors"
	"testing"
	"time"
)

func TestTelemetrySnapshotTracksBoundedOperations(t *testing.T) {
	t.Parallel()

	telemetry := NewTelemetry()
	telemetry.RecordRun(125*time.Millisecond, "success")
	telemetry.QueueJob()
	telemetry.StartJob()
	telemetry.FinishJob("response", "up")
	telemetry.RecordCoreRequest("claim_monitoring_jobs", time.Second, errors.New("unavailable"))
	telemetry.RecordLease("claimed")

	snapshot := telemetry.Snapshot()
	if snapshot.Runs["success"] != 1 || snapshot.RunDuration.Count != 1 || snapshot.RunDuration.Sum != 125*time.Millisecond {
		t.Fatalf("unexpected run metrics: %#v", snapshot)
	}
	if snapshot.JobsActive != 0 || snapshot.JobsQueued != 0 || snapshot.Executors["response/up"] != 1 {
		t.Fatalf("unexpected job metrics: %#v", snapshot)
	}
	if metric := snapshot.Core["claim_monitoring_jobs"]; metric.Count != 1 || metric.Errors != 1 || metric.Duration != time.Second {
		t.Fatalf("unexpected Core metrics: %#v", metric)
	}
	if snapshot.Leases["claimed"] != 1 {
		t.Fatalf("unexpected lease metrics: %#v", snapshot.Leases)
	}
}
