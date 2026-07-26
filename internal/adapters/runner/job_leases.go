package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func (r *MonitoringService) runClaimedJobs(ctx context.Context) error {
	jobs, err := r.client.ClaimMonitoringJobs(ctx, monitor.ClaimMonitoringJobsRequest{
		Location:     r.cfg.WebGuardLocation,
		InstanceID:   r.cfg.WebGuardInstanceID,
		Capabilities: r.leaseCapabilities(),
		Capacity:     max(1, r.cfg.QueueDefaultWorkers),
		MaxBatchSize: max(1, r.cfg.JobLeaseMaxBatch),
	})
	if err != nil {
		r.logFetchError(err)
		return err
	}
	if len(jobs) == 0 {
		r.logger.Println("No monitoring jobs claimed.")
		return nil
	}

	r.logger.Printf("Claimed %d monitoring jobs.", len(jobs))
	queue := make(chan monitor.ClaimedJob)
	var workers sync.WaitGroup
	for i := 0; i < min(max(1, r.cfg.QueueDefaultWorkers), len(jobs)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range queue {
				r.runClaimedJob(ctx, job)
			}
		}()
	}
	for _, job := range jobs {
		queue <- job
	}
	close(queue)
	workers.Wait()
	return nil
}

func (r *MonitoringService) leaseCapabilities() []string {
	return []string{string(PhaseResponse), string(PhaseSSL), string(PhaseDomainExpiration)}
}

func (r *MonitoringService) runClaimedJob(ctx context.Context, job monitor.ClaimedJob) {
	if !job.LeaseExpiresAt.IsZero() && !job.LeaseExpiresAt.After(time.Now()) {
		r.releaseClaimedJob(ctx, job, "lease expired before execution")
		return
	}
	phase := ExecutionPhase(strings.TrimSpace(job.Phase))
	if !isLeasePhase(phase) || !r.executors.Supports(phase, job.Monitoring.Type) {
		r.releaseClaimedJob(ctx, job, "unsupported monitoring job")
		return
	}
	if job.Monitoring.MaintenanceActive {
		r.completeClaimedJob(ctx, job, maintenanceExecution(phase, job.Monitoring.ID))
		return
	}

	release, err := application.AcquireExecutionSlot(ctx)
	if err != nil {
		r.releaseClaimedJob(ctx, job, fmt.Sprintf("execution canceled: %v", err))
		return
	}
	execution, _ := r.executors.Execute(ctx, phase, job.Monitoring)
	release()
	r.completeClaimedJob(ctx, job, execution)
}

func isLeasePhase(phase ExecutionPhase) bool {
	return phase == PhaseResponse || phase == PhaseSSL || phase == PhaseDomainExpiration
}

func (r *MonitoringService) completeClaimedJob(ctx context.Context, job monitor.ClaimedJob, execution Execution) {
	if r.cfg.JobLeasesDualWrite {
		r.publishExecution(ctx, execution)
	}

	err := r.client.CompleteMonitoringJob(ctx, job.ID, job.IdempotencyKey, monitor.CompleteMonitoringJobRequest{
		Attempt: job.Attempt,
		Result:  execution.jobResult(),
	})
	if err != nil {
		r.logger.Printf("Failed to complete monitoring job (job_id=%s monitoring_id=%s): %v", job.ID, job.Monitoring.ID, err)
	}
}

func (r *MonitoringService) releaseClaimedJob(ctx context.Context, job monitor.ClaimedJob, reason string) {
	if err := r.client.ReleaseMonitoringJob(ctx, job.ID, monitor.ReleaseMonitoringJobRequest{
		Attempt: job.Attempt,
		Reason:  reason,
	}); err != nil {
		r.logger.Printf("Failed to release monitoring job (job_id=%s): %v", job.ID, err)
	}
}

func (e Execution) jobResult() monitor.JobResult {
	return monitor.JobResult{Response: e.Response, SSL: e.SSL, Domain: e.Domain}
}
