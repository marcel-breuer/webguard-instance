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
	telemetry.FinishJobWithDuration("response", "up", 250*time.Millisecond)
	telemetry.RecordJobTransition("running", "completed")
	telemetry.RecordSchedulerRun(50*time.Millisecond, 2*time.Second, "success")
	telemetry.RecordLeaseWait(75 * time.Millisecond)
	telemetry.RecordCoreRequest("claim_monitoring_jobs", time.Second, errors.New("unavailable"))
	telemetry.RecordLease("claimed")

	snapshot := telemetry.Snapshot()
	if snapshot.Runs["success"] != 1 || snapshot.RunDuration.Count != 1 || snapshot.RunDuration.Sum != 125*time.Millisecond {
		t.Fatalf("unexpected run metrics: %#v", snapshot)
	}
	if snapshot.JobsActive != 0 || snapshot.JobsQueued != 0 || snapshot.Executors["response/up"] != 1 {
		t.Fatalf("unexpected job metrics: %#v", snapshot)
	}
	if metric := snapshot.JobDuration["response"]; metric.Count != 1 || metric.Sum != 250*time.Millisecond {
		t.Fatalf("unexpected job duration metrics: %#v", snapshot.JobDuration)
	}
	if snapshot.Transitions["running->completed"] != 1 || snapshot.SchedulerRuns["success"] != 1 {
		t.Fatalf("unexpected scheduler or transition metrics: %#v", snapshot)
	}
	if snapshot.SchedulerDelay.Sum != 50*time.Millisecond || snapshot.SchedulerDuration.Sum != 2*time.Second || snapshot.LeaseWait.Sum != 75*time.Millisecond {
		t.Fatalf("unexpected latency metrics: %#v", snapshot)
	}
	if metric := snapshot.Core["claim_monitoring_jobs"]; metric.Count != 1 || metric.Errors != 1 || metric.Duration != time.Second {
		t.Fatalf("unexpected Core metrics: %#v", metric)
	}
	if snapshot.Leases["claimed"] != 1 {
		t.Fatalf("unexpected lease metrics: %#v", snapshot.Leases)
	}
	if snapshot.Goroutines < 1 || snapshot.HeapAllocBytes == 0 || snapshot.HeapInUseBytes == 0 || snapshot.SysBytes == 0 {
		t.Fatalf("expected live runtime resource metrics, got goroutines=%d heap_alloc=%d heap_inuse=%d sys=%d", snapshot.Goroutines, snapshot.HeapAllocBytes, snapshot.HeapInUseBytes, snapshot.SysBytes)
	}
}
