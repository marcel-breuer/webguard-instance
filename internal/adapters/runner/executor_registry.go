package runner

import (
	"context"
	"fmt"
	"slices"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

type ExecutionPhase string

const (
	PhaseResponse         ExecutionPhase = "response"
	PhaseSSL              ExecutionPhase = "ssl"
	PhaseDomainExpiration ExecutionPhase = "domain_expiration"
)

// Execution is the normalized output of one monitoring executor. The result
// publisher translates populated payloads to the existing Core API endpoints.
type Execution struct {
	Response *monitor.MonitoringResponsePayload
	SSL      *monitor.SSLResultPayload
	Domain   *monitor.DomainResultPayload
}

// Executor owns one check concern. New monitor types are introduced by adding
// and registering an executor instead of extending phase-specific switches.
type Executor interface {
	Name() string
	Phase() ExecutionPhase
	Types() []monitor.Type
	Supports(monitor.Type) bool
	Execute(context.Context, monitor.Monitoring) Execution
}

type executorRegistry struct {
	executors []Executor
}

func newExecutorRegistry(executors ...Executor) *executorRegistry {
	return &executorRegistry{executors: executors}
}

func (r *executorRegistry) Types(phase ExecutionPhase) []monitor.Type {
	types := make([]monitor.Type, 0)
	seen := make(map[monitor.Type]struct{})
	for _, executor := range r.executors {
		if executor.Phase() != phase {
			continue
		}
		for _, monitoringType := range executor.Types() {
			if _, ok := seen[monitoringType]; ok {
				continue
			}
			seen[monitoringType] = struct{}{}
			types = append(types, monitoringType)
		}
	}
	return types
}

func (r *executorRegistry) Supports(phase ExecutionPhase, monitoringType monitor.Type) bool {
	_, ok := r.find(phase, monitoringType)
	return ok
}

func (r *executorRegistry) Execute(ctx context.Context, phase ExecutionPhase, monitoring monitor.Monitoring) (Execution, bool) {
	executor, ok := r.find(phase, monitoring.Type)
	if !ok {
		return Execution{}, false
	}
	return executor.Execute(ctx, monitoring), true
}

func (r *executorRegistry) find(phase ExecutionPhase, monitoringType monitor.Type) (Executor, bool) {
	for _, executor := range r.executors {
		if executor.Phase() == phase && executor.Supports(monitoringType) {
			return executor, true
		}
	}
	return nil, false
}

type functionExecutor struct {
	name            string
	phase           ExecutionPhase
	monitoringTypes []monitor.Type
	execute         func(context.Context, monitor.Monitoring) Execution
}

func (e functionExecutor) Name() string {
	return e.name
}

func (e functionExecutor) Phase() ExecutionPhase {
	return e.phase
}

func (e functionExecutor) Types() []monitor.Type {
	return append([]monitor.Type(nil), e.monitoringTypes...)
}

func (e functionExecutor) Supports(monitoringType monitor.Type) bool {
	return slices.Contains(e.monitoringTypes, monitoringType)
}

func (e functionExecutor) Execute(ctx context.Context, monitoring monitor.Monitoring) Execution {
	return e.execute(ctx, monitoring)
}

func (r *MonitoringService) newExecutorRegistry() *executorRegistry {
	return newExecutorRegistry(
		functionExecutor{
			name:            "http",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypeHTTP},
			execute:         r.executeHTTP,
		},
		functionExecutor{
			name:            "ping",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypePing},
			execute:         r.executePing,
		},
		functionExecutor{
			name:            "keyword",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypeKeyword},
			execute:         r.executeKeyword,
		},
		functionExecutor{
			name:            "tcp-port",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypePort},
			execute:         r.executePort,
		},
		functionExecutor{
			name:            "dns-record",
			phase:           PhaseResponse,
			monitoringTypes: []monitor.Type{monitor.TypeDNSRecord},
			execute:         r.executeDNSRecord,
		},
		functionExecutor{
			name:            "tls-certificate",
			phase:           PhaseSSL,
			monitoringTypes: []monitor.Type{monitor.TypeHTTP, monitor.TypeKeyword, monitor.TypePort},
			execute:         r.executeSSL,
		},
		functionExecutor{
			name:            "domain-expiration",
			phase:           PhaseDomainExpiration,
			monitoringTypes: []monitor.Type{monitor.TypeDomainExpiration},
			execute:         r.executeDomainExpiration,
		},
	)
}

func (r *MonitoringService) executeHTTP(ctx context.Context, monitoring monitor.Monitoring) Execution {
	status, responseTime, httpStatusCode := r.handleHTTPMonitoring(ctx, monitoring)
	return responseExecution(monitoring.ID, status, responseTime, httpStatusCode)
}

func (r *MonitoringService) executeKeyword(ctx context.Context, monitoring monitor.Monitoring) Execution {
	status, responseTime, httpStatusCode := r.handleKeywordMonitoring(ctx, monitoring)
	return responseExecution(monitoring.ID, status, responseTime, httpStatusCode)
}

func (r *MonitoringService) executePing(ctx context.Context, monitoring monitor.Monitoring) Execution {
	status, responseTime := handlePingMonitoring(ctx, monitoring, r.egressPolicy(), r.pingExecutor)
	return responseExecution(monitoring.ID, status, responseTime, nil)
}

func (r *MonitoringService) executePort(ctx context.Context, monitoring monitor.Monitoring) Execution {
	status, responseTime := handlePortMonitoring(ctx, monitoring, r.egressPolicy())
	return responseExecution(monitoring.ID, status, responseTime, nil)
}

func (r *MonitoringService) executeDNSRecord(ctx context.Context, monitoring monitor.Monitoring) Execution {
	checker := r.dnsChecker
	if checker == nil {
		checker = NewDNSRecordChecker(nil, r.logger)
	}
	status, responseTime := checker.Check(ctx, monitoring)
	return responseExecution(monitoring.ID, status, responseTime, nil)
}

func (r *MonitoringService) executeSSL(ctx context.Context, monitoring monitor.Monitoring) Execution {
	payload := r.crawlMonitoringSSL(ctx, monitoring)
	return Execution{SSL: &payload}
}

func (r *MonitoringService) executeDomainExpiration(ctx context.Context, monitoring monitor.Monitoring) Execution {
	status, payload, hasPayload := r.crawlDomainExpiration(ctx, monitoring)
	execution := responseExecution(monitoring.ID, status, nil, nil)
	if hasPayload {
		execution.Domain = &payload
	}
	return execution
}

func responseExecution(monitoringID string, status monitor.Status, responseTime *float64, httpStatusCode *int) Execution {
	payload := monitor.MonitoringResponsePayload{
		MonitoringID:   monitoringID,
		Status:         status,
		ResponseTime:   responseTime,
		HTTPStatusCode: httpStatusCode,
	}
	return Execution{Response: &payload}
}

func maintenanceExecution(phase ExecutionPhase, monitoringID string) Execution {
	if phase == PhaseSSL {
		return Execution{}
	}
	return responseExecution(monitoringID, monitor.StatusUnknown, nil, nil)
}

func (r *MonitoringService) publishExecution(ctx context.Context, execution Execution) {
	if execution.Response != nil {
		if err := r.client.PostMonitoringResponse(ctx, *execution.Response); err != nil {
			r.logger.Printf("Failed to post response result (monitoring_id=%s): %v", execution.Response.MonitoringID, err)
		}
	}
	if execution.SSL != nil {
		if err := r.client.PostSSLResult(ctx, *execution.SSL); err != nil {
			r.logger.Printf("Failed to post SSL result (monitoring_id=%s): %v", execution.SSL.MonitoringID, err)
		}
	}
	if execution.Domain != nil {
		if err := r.client.PostDomainResult(ctx, *execution.Domain); err != nil {
			r.logger.Printf("Failed to post domain expiration result (monitoring_id=%s): %v", execution.Domain.MonitoringID, err)
		}
	}
}

func (e Execution) String() string {
	if e.Response != nil {
		return fmt.Sprintf("status=%s response_time=%v http_status_code=%v", e.Response.Status, pointerFloat64Value(e.Response.ResponseTime), pointerIntValue(e.Response.HTTPStatusCode))
	}
	if e.SSL != nil {
		return fmt.Sprintf("ssl_valid=%t", e.SSL.IsValid)
	}
	return "no result"
}
