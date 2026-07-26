package health

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/marcel-breuer/webguard-instance/internal/application"
)

type ReadinessFunc func() bool

func (f ReadinessFunc) Ready() bool { return f == nil || f() }

type Readiness interface{ Ready() bool }

func NewHandler(readiness Readiness, telemetry *application.Telemetry) http.Handler {
	mux := http.NewServeMux()
	live := func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}
	ready := func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if readiness != nil && !readiness.Ready() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("not ready"))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}
	mux.HandleFunc("/", live)
	mux.HandleFunc("/health", live)
	mux.HandleFunc("/livez", live)
	mux.HandleFunc("/readyz", ready)
	mux.HandleFunc("/metrics", MetricsHandler(telemetry))
	return mux
}

func MetricsHandler(telemetry *application.Telemetry) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = writer.Write([]byte(RenderMetrics(telemetry.Snapshot())))
	}
}

func RenderMetrics(snapshot application.TelemetrySnapshot) string {
	var metrics strings.Builder
	metrics.WriteString("# HELP webguard_instance_runs_total Monitoring runs by terminal outcome.\n# TYPE webguard_instance_runs_total counter\n")
	writeLabeledCounter(&metrics, "webguard_instance_runs_total", "outcome", snapshot.Runs)
	metrics.WriteString("# HELP webguard_instance_run_duration_seconds Total duration of completed monitoring runs.\n# TYPE webguard_instance_run_duration_seconds summary\n")
	fmt.Fprintf(&metrics, "webguard_instance_run_duration_seconds_count %d\nwebguard_instance_run_duration_seconds_sum %.6f\n", snapshot.RunDuration.Count, snapshot.RunDuration.Sum.Seconds())
	fmt.Fprintf(&metrics, "webguard_instance_jobs_active %d\nwebguard_instance_jobs_queued %d\n", snapshot.JobsActive, snapshot.JobsQueued)
	metrics.WriteString("# HELP webguard_instance_executor_outcomes_total Executor outcomes by bounded phase and outcome.\n# TYPE webguard_instance_executor_outcomes_total counter\n")
	writeSplitCounter(&metrics, "webguard_instance_executor_outcomes_total", snapshot.Executors, "phase", "outcome")
	metrics.WriteString("# HELP webguard_instance_core_requests_total Core API requests by bounded operation.\n# TYPE webguard_instance_core_requests_total counter\n")
	writeRequestMetrics(&metrics, snapshot.Core)
	metrics.WriteString("# HELP webguard_instance_lease_events_total Lease lifecycle events.\n# TYPE webguard_instance_lease_events_total counter\n")
	writeLabeledCounter(&metrics, "webguard_instance_lease_events_total", "event", snapshot.Leases)
	return metrics.String()
}

func writeLabeledCounter(builder *strings.Builder, name, label string, values map[string]uint64) {
	for _, key := range sortedKeys(values) {
		fmt.Fprintf(builder, "%s{%s=%q} %d\n", name, label, key, values[key])
	}
}

func writeSplitCounter(builder *strings.Builder, name string, values map[string]uint64, first, second string) {
	for _, key := range sortedKeys(values) {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) == 2 {
			fmt.Fprintf(builder, "%s{%s=%q,%s=%q} %d\n", name, first, parts[0], second, parts[1], values[key])
		}
	}
}

func writeRequestMetrics(builder *strings.Builder, values map[string]application.RequestMetric) {
	for _, key := range sortedRequestKeys(values) {
		metric := values[key]
		fmt.Fprintf(builder, "webguard_instance_core_requests_total{operation=%q} %d\n", key, metric.Count)
		fmt.Fprintf(builder, "webguard_instance_core_request_errors_total{operation=%q} %d\n", key, metric.Errors)
		fmt.Fprintf(builder, "webguard_instance_core_request_duration_seconds_sum{operation=%q} %.6f\n", key, metric.Duration.Seconds())
	}
}

func sortedKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedRequestKeys(values map[string]application.RequestMetric) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
