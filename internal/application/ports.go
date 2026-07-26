package application

import (
	"context"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

// MonitoringGateway is the application boundary to WebGuard Core. Concrete
// HTTP clients live in adapters and are injected at the composition root.
type MonitoringGateway interface {
	GetMonitorings(ctx context.Context, location string, types []monitor.Type) ([]monitor.Monitoring, error)
	PostMonitoringResponse(ctx context.Context, payload monitor.MonitoringResponsePayload) error
	PostSSLResult(ctx context.Context, payload monitor.SSLResultPayload) error
	PostDomainResult(ctx context.Context, payload monitor.DomainResultPayload) error
	ClaimMonitoringJobs(ctx context.Context, request monitor.ClaimMonitoringJobsRequest) ([]monitor.ClaimedJob, error)
	CompleteMonitoringJob(ctx context.Context, jobID, idempotencyKey string, request monitor.CompleteMonitoringJobRequest) error
	ReleaseMonitoringJob(ctx context.Context, jobID string, request monitor.ReleaseMonitoringJobRequest) error
	ExtendMonitoringJob(ctx context.Context, jobID string, request monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error)
}
