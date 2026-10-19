package runner

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const (
	leaseRenewalRequestTimeout = 5 * time.Second
	leaseReleaseTimeout        = 5 * time.Second
)

func (r *MonitoringService) runClaimedJobs(ctx context.Context) error {
	workerCount := r.workerLimit()
	batchSize := max(1, min(max(1, r.cfg.JobLeaseMaxBatch), workerCount))
	jobs, err := r.client.ClaimMonitoringJobs(ctx, monitor.ClaimMonitoringJobsRequest{
		Location:     r.cfg.WebGuardLocation,
		InstanceID:   r.cfg.WebGuardInstanceID,
		Capabilities: r.leaseCapabilities(),
		Capacity:     workerCount,
		MaxBatchSize: batchSize,
	})
	if err != nil {
		r.logFetchError(err)
		return err
	}
	if len(jobs) == 0 {
		r.logger.Println("No monitoring jobs claimed.")
		return nil
	}

	claimedAt := time.Now()
	r.logger.Printf("Claimed %d monitoring jobs.", len(jobs))
	uniqueJobs := make([]monitor.ClaimedJob, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		r.telemetry.RecordLease("claimed")
		r.telemetry.RecordJobTransition("received", "claimed")
		jobID := strings.TrimSpace(job.ID)
		if jobID != "" {
			if _, exists := seen[jobID]; exists {
				r.telemetry.RecordLease("duplicate")
				r.telemetry.RecordJobTransition("claimed", "duplicate")
				continue
			}
			seen[jobID] = struct{}{}
		}
		uniqueJobs = append(uniqueJobs, job)
		from := "claimed"
		if job.Attempt > 1 {
			from = "retry"
			r.telemetry.RecordLease("retry")
			r.telemetry.RecordJobTransition("claimed", "retry")
		}
		r.telemetry.RecordJobTransition(from, "queued")
		r.telemetry.QueueJob()
	}
	if len(uniqueJobs) == 0 {
		return nil
	}

	queue := make(chan monitor.ClaimedJob)
	var workers sync.WaitGroup
	workerCount = min(workerCount, len(uniqueJobs))
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range queue {
				r.runClaimedJob(ctx, job, claimedAt)
			}
		}()
	}
	for _, job := range uniqueJobs {
		queue <- job
	}
	close(queue)
	workers.Wait()
	return nil
}

func (r *MonitoringService) leaseCapabilities() []string {
	return []string{string(PhaseResponse), string(PhaseSSL), string(PhaseDomainExpiration)}
}

func (r *MonitoringService) runClaimedJob(ctx context.Context, job monitor.ClaimedJob, claimedAt time.Time) {
	phase := ExecutionPhase(strings.TrimSpace(job.Phase))
	if !isLeasePhase(phase) || !r.executors.Supports(phase, job.Monitoring.Type) {
		r.telemetry.DropQueuedJob(leaseMetricPhase(job.Phase), "released")
		r.telemetry.RecordJobTransition("queued", "released")
		r.releaseClaimedJob(ctx, job, "unsupported monitoring job")
		return
	}
	if reason := validateClaimedJob(job); reason != "" {
		r.telemetry.DropQueuedJob(string(phase), "invalid")
		r.telemetry.RecordJobTransition("queued", "invalid")
		r.releaseClaimedJob(ctx, job, reason)
		return
	}
	if !job.LeaseExpiresAt.After(time.Now()) {
		r.telemetry.DropQueuedJob(string(phase), "released")
		r.telemetry.RecordJobTransition("queued", "released")
		r.releaseClaimedJob(ctx, job, "lease expired before execution")
		return
	}

	r.telemetry.RecordLeaseWait(time.Since(claimedAt))
	r.telemetry.StartJob()
	r.telemetry.RecordJobTransition("queued", "running")
	startedAt := time.Now()
	if job.Monitoring.MaintenanceActive {
		completed := r.completeClaimedJob(ctx, job, maintenanceExecution(phase, job.Monitoring.ID))
		if completed {
			r.telemetry.RecordJobTransition("running", "completed")
			r.telemetry.FinishJobWithDuration(string(phase), "maintenance", time.Since(startedAt))
		} else {
			r.telemetry.RecordJobTransition("running", "completion_failed")
			r.telemetry.FinishJobWithDuration(string(phase), "completion_failed", time.Since(startedAt))
		}
		return
	}

	jobContext, stopRenewal := r.startLeaseRenewal(ctx, job)
	releaseSlot, err := application.AcquireExecutionSlot(jobContext)
	if err != nil {
		leaseLost := stopRenewal()
		r.releaseClaimedJob(ctx, job, "execution canceled before start")
		r.telemetry.RecordJobTransition("running", "released")
		r.telemetry.FinishJobWithDuration(string(phase), "canceled", time.Since(startedAt))
		if leaseLost {
			r.telemetry.RecordLease("expired")
		}
		return
	}

	execution, _ := r.executors.Execute(jobContext, phase, job.Monitoring)
	releaseSlot()
	parentCanceled := ctx.Err() != nil
	leaseLost := stopRenewal()
	if parentCanceled || leaseLost {
		reason := "execution canceled"
		if leaseLost {
			reason = "lease renewal failed"
		}
		r.releaseClaimedJob(ctx, job, reason)
		r.telemetry.RecordJobTransition("running", "released")
		r.telemetry.FinishJobWithDuration(string(phase), "canceled", time.Since(startedAt))
		return
	}

	if !r.completeClaimedJob(ctx, job, execution) {
		r.telemetry.RecordJobTransition("running", "completion_failed")
		r.telemetry.FinishJobWithDuration(string(phase), "completion_failed", time.Since(startedAt))
		return
	}
	result := executionOutcome(execution)
	r.telemetry.RecordJobTransition("running", "completed")
	r.telemetry.FinishJobWithDuration(string(phase), result, time.Since(startedAt))
	r.logger.Printf("run_id=%s job_id=%s monitoring_id=%s phase=%s outcome=%s", application.RunID(ctx), job.ID, job.Monitoring.ID, phase, result)
}

func validateClaimedJob(job monitor.ClaimedJob) string {
	switch {
	case strings.TrimSpace(job.ID) == "":
		return "job ID is empty"
	case strings.TrimSpace(job.Monitoring.ID) == "":
		return "monitoring ID is empty"
	case job.LeaseExpiresAt.IsZero():
		return "lease expiration is missing"
	case job.Attempt < 1:
		return "job attempt must be positive"
	case !isUUIDv4(strings.TrimSpace(job.IdempotencyKey)):
		return "job idempotency key is invalid"
	case strings.TrimSpace(job.Monitoring.Target) == "":
		return "monitoring target is empty"
	case job.Monitoring.Timeout < 0:
		return "monitoring timeout must not be negative"
	case job.Monitoring.Type == monitor.TypePort && (job.Monitoring.Port < 1 || job.Monitoring.Port > 65535):
		return "monitoring port is invalid"
	case job.Monitoring.Type == monitor.TypeDNSRecord && (strings.TrimSpace(job.Monitoring.DNSRecordType) == "" || len(job.Monitoring.DNSExpectedValues) == 0):
		return "DNS monitoring configuration is incomplete"
	default:
		return ""
	}
}

func (r *MonitoringService) startLeaseRenewal(ctx context.Context, job monitor.ClaimedJob) (context.Context, func() bool) {
	jobContext, cancelJob := context.WithCancel(ctx)
	done := make(chan struct{})
	expiresAt := job.LeaseExpiresAt
	leaseLost := false

	go func() {
		defer close(done)
		for {
			remaining := time.Until(expiresAt)
			if remaining <= 0 {
				leaseLost = true
				r.telemetry.RecordLease("expired")
				cancelJob()
				return
			}

			timer := time.NewTimer(remaining / 2)
			select {
			case <-jobContext.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}

			requestTimeout := min(remaining/2, leaseRenewalRequestTimeout)
			if requestTimeout <= 0 {
				leaseLost = true
				r.telemetry.RecordLease("renewal_failed")
				cancelJob()
				return
			}
			renewalContext, cancelRenewal := context.WithTimeout(jobContext, requestTimeout)
			extended, err := r.client.ExtendMonitoringJob(renewalContext, job.ID, monitor.ExtendMonitoringJobRequest{Attempt: job.Attempt})
			cancelRenewal()
			if jobContext.Err() != nil {
				return
			}
			if err != nil || !extended.LeaseExpiresAt.After(time.Now()) {
				leaseLost = true
				r.telemetry.RecordLease("renewal_failed")
				cancelJob()
				return
			}
			expiresAt = extended.LeaseExpiresAt
			r.telemetry.RecordLease("extended")
		}
	}()

	stop := func() bool {
		cancelJob()
		<-done
		return leaseLost || !expiresAt.After(time.Now())
	}
	return jobContext, stop
}

func isLeasePhase(phase ExecutionPhase) bool {
	return phase == PhaseResponse || phase == PhaseSSL || phase == PhaseDomainExpiration
}

func leaseMetricPhase(phase string) string {
	switch ExecutionPhase(strings.TrimSpace(phase)) {
	case PhaseResponse:
		return string(PhaseResponse)
	case PhaseSSL:
		return string(PhaseSSL)
	case PhaseDomainExpiration:
		return string(PhaseDomainExpiration)
	default:
		return "unknown"
	}
}

func (r *MonitoringService) completeClaimedJob(ctx context.Context, job monitor.ClaimedJob, execution Execution) bool {
	idempotencyKey := strings.TrimSpace(job.IdempotencyKey)
	if !isUUIDv4(idempotencyKey) {
		r.logger.Printf("Failed to complete monitoring job (job_id=%s): idempotency key is invalid", job.ID)
		return false
	}
	setExecutionIdempotencyKey(&execution, idempotencyKey)
	if r.cfg.JobLeasesDualWrite {
		r.publishExecution(ctx, execution, idempotencyKey)
	}

	err := r.client.CompleteMonitoringJob(ctx, job.ID, idempotencyKey, monitor.CompleteMonitoringJobRequest{
		Attempt: job.Attempt,
		Result:  execution.jobResult(),
	})
	if err != nil {
		r.logger.Printf("Failed to complete monitoring job (job_id=%s monitoring_id=%s): %v", job.ID, job.Monitoring.ID, err)
		return false
	}
	r.telemetry.RecordLease("completed")
	return true
}

func setExecutionIdempotencyKey(execution *Execution, key string) {
	if execution.Response != nil {
		execution.Response.IdempotencyKey = key
	}
	if execution.SSL != nil {
		execution.SSL.IdempotencyKey = key
	}
	if execution.Domain != nil {
		execution.Domain.IdempotencyKey = key
	}
}

func (r *MonitoringService) releaseClaimedJob(ctx context.Context, job monitor.ClaimedJob, reason string) bool {
	if strings.TrimSpace(job.ID) == "" {
		return false
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
	defer cancel()
	if err := r.client.ReleaseMonitoringJob(cleanupContext, job.ID, monitor.ReleaseMonitoringJobRequest{
		Attempt: job.Attempt,
		Reason:  reason,
	}); err != nil {
		r.logger.Printf("Failed to release monitoring job (job_id=%s): %v", job.ID, err)
		return false
	}
	r.telemetry.RecordLease("released")
	return true
}

func (e Execution) jobResult() monitor.JobResult {
	return monitor.JobResult{Response: e.Response, SSL: e.SSL, Domain: e.Domain}
}

func executionOutcome(execution Execution) string {
	if execution.Response != nil {
		return string(execution.Response.Status)
	}
	if execution.SSL != nil {
		if execution.SSL.IsValid {
			return "up"
		}
		return "down"
	}
	if execution.Domain != nil {
		if execution.Domain.IsValid {
			return "up"
		}
		return "down"
	}
	return "unknown"
}
