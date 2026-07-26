package coreapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func TestGetMonitoringsIncludesHeadersAndQuery(t *testing.T) {
	t.Parallel()

	var gotAPIKey string
	var gotInstanceCode string
	var gotLocation string
	var gotType string

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAPIKey = request.Header.Get("X-API-KEY")
		gotInstanceCode = request.Header.Get("X-INSTANCE-CODE")
		gotLocation = request.URL.Query().Get("location")
		gotType = request.URL.Query().Get("type")

		if request.URL.Path != "/api/v1/internal/monitorings" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"1","type":"http","target":"https://example.com","timeout":10}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	monitorings, err := client.GetMonitorings(context.Background(), "de-1", []monitor.Type{
		monitor.TypeHTTP,
	})
	if err != nil {
		t.Fatalf("GetMonitorings failed: %v", err)
	}

	if gotAPIKey != "secret-key" {
		t.Fatalf("expected api key secret-key, got %q", gotAPIKey)
	}
	if gotInstanceCode != "de-1" {
		t.Fatalf("expected instance code de-1, got %q", gotInstanceCode)
	}
	if gotLocation != "de-1" {
		t.Fatalf("expected location=de-1, got %q", gotLocation)
	}
	if gotType != "http" {
		t.Fatalf("expected type=http, got %q", gotType)
	}
	if len(monitorings) != 1 {
		t.Fatalf("expected 1 monitoring, got %d", len(monitorings))
	}
}

func TestGetMonitoringsWithMultipleTypesFetchesAndMerges(t *testing.T) {
	t.Parallel()

	requestedTypes := make([]string, 0)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-INSTANCE-CODE") != "de-1" {
			t.Fatalf("missing/invalid X-INSTANCE-CODE header: %q", request.Header.Get("X-INSTANCE-CODE"))
		}
		if request.URL.Query().Get("location") != "de-1" {
			t.Fatalf("expected location=de-1, got %q", request.URL.Query().Get("location"))
		}

		monitoringType := request.URL.Query().Get("type")
		requestedTypes = append(requestedTypes, monitoringType)

		writer.Header().Set("Content-Type", "application/json")
		switch monitoringType {
		case "http":
			_, _ = writer.Write([]byte(`[{"id":"shared","type":"http","target":"https://example.com","timeout":5},{"id":"http-only","type":"http","target":"https://example.com","timeout":5}]`))
		case "keyword":
			_, _ = writer.Write([]byte(`[{"id":"shared","type":"keyword","target":"https://example.com","timeout":5}]`))
		case "port":
			_, _ = writer.Write([]byte(`[{"id":"port-only","type":"port","target":"example.com","port":443}]`))
		default:
			t.Fatalf("unexpected type query: %q", monitoringType)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	monitorings, err := client.GetMonitorings(context.Background(), "de-1", []monitor.Type{
		monitor.TypeHTTP,
		monitor.TypeKeyword,
		monitor.TypeHTTP,
		monitor.TypePort,
	})
	if err != nil {
		t.Fatalf("GetMonitorings failed: %v", err)
	}

	if len(requestedTypes) != 3 {
		t.Fatalf("expected 3 unique type requests, got %d (%v)", len(requestedTypes), requestedTypes)
	}

	ids := make(map[string]struct{}, len(monitorings))
	for _, item := range monitorings {
		ids[item.ID] = struct{}{}
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 unique monitorings, got %d (%#v)", len(ids), ids)
	}
	if _, ok := ids["shared"]; !ok {
		t.Fatalf("expected merged result to contain shared id")
	}
	if _, ok := ids["http-only"]; !ok {
		t.Fatalf("expected merged result to contain http-only id")
	}
	if _, ok := ids["port-only"]; !ok {
		t.Fatalf("expected merged result to contain port-only id")
	}
}

func TestGetMonitoringsSupportsStringIDs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`[{"id":"123","type":"http","target":"https://example.com","timeout":"10"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	monitorings, err := client.GetMonitorings(context.Background(), "de-1", nil)
	if err != nil {
		t.Fatalf("GetMonitorings failed: %v", err)
	}
	if len(monitorings) != 1 {
		t.Fatalf("expected 1 monitoring, got %d", len(monitorings))
	}
	if monitorings[0].ID != "123" {
		t.Fatalf("expected id 123, got %s", monitorings[0].ID)
	}
	if monitorings[0].Timeout != 10 {
		t.Fatalf("expected timeout 10, got %d", monitorings[0].Timeout)
	}
}

func TestPostMonitoringResponsePayloadShape(t *testing.T) {
	t.Parallel()

	var gotInstanceCode string
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/internal/monitoring-responses" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}

		gotInstanceCode = request.Header.Get("X-INSTANCE-CODE")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	err := client.PostMonitoringResponse(context.Background(), monitor.MonitoringResponsePayload{
		MonitoringID:   "42",
		Status:         monitor.StatusUnknown,
		ResponseTime:   nil,
		HTTPStatusCode: nil,
	})
	if err != nil {
		t.Fatalf("PostMonitoringResponse failed: %v", err)
	}

	if gotInstanceCode != "de-1" {
		t.Fatalf("expected instance code de-1, got %q", gotInstanceCode)
	}
	if body["monitoring_id"] != "42" {
		t.Fatalf("expected monitoring_id=42, got %#v", body["monitoring_id"])
	}
	if body["status"] != "unknown" {
		t.Fatalf("expected status=unknown, got %#v", body["status"])
	}
	if value, ok := body["response_time"]; !ok || value != nil {
		t.Fatalf("expected response_time=null, got %#v", body["response_time"])
	}
	if value, ok := body["http_status_code"]; !ok || value != nil {
		t.Fatalf("expected http_status_code=null, got %#v", body["http_status_code"])
	}
}

func TestPostMonitoringResponsePayloadIncludesHTTPStatusCode(t *testing.T) {
	t.Parallel()

	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/internal/monitoring-responses" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	err := client.PostMonitoringResponse(context.Background(), monitor.MonitoringResponsePayload{
		MonitoringID:   "42",
		Status:         monitor.StatusDown,
		ResponseTime:   nil,
		HTTPStatusCode: intPtr(503),
	})
	if err != nil {
		t.Fatalf("PostMonitoringResponse failed: %v", err)
	}

	if body["http_status_code"] != float64(503) {
		t.Fatalf("expected http_status_code=503, got %#v", body["http_status_code"])
	}
}

func TestPostSSLResultPayloadShape(t *testing.T) {
	t.Parallel()

	var gotInstanceCode string
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/internal/ssl-results" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}

		gotInstanceCode = request.Header.Get("X-INSTANCE-CODE")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	now := time.Now().UTC()
	err := client.PostSSLResult(context.Background(), monitor.SSLResultPayload{
		MonitoringID: "10",
		IsValid:      true,
		ExpiresAt:    &now,
		Issuer:       ptr("issuer"),
		IssuedAt:     &now,
	})
	if err != nil {
		t.Fatalf("PostSSLResult failed: %v", err)
	}

	if gotInstanceCode != "de-1" {
		t.Fatalf("expected instance code de-1, got %q", gotInstanceCode)
	}
	if body["monitoring_id"] != "10" {
		t.Fatalf("expected monitoring_id=10, got %#v", body["monitoring_id"])
	}
	if body["is_valid"] != true {
		t.Fatalf("expected is_valid=true, got %#v", body["is_valid"])
	}
	if body["issuer"] != "issuer" {
		t.Fatalf("expected issuer=issuer, got %#v", body["issuer"])
	}
}

func TestPostDomainResultPayloadShape(t *testing.T) {
	t.Parallel()

	var gotInstanceCode string
	var body map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/internal/domain-results" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}

		gotInstanceCode = request.Header.Get("X-INSTANCE-CODE")
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode payload: %v", err)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	expiresAt := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	err := client.PostDomainResult(context.Background(), monitor.DomainResultPayload{
		MonitoringID: "domain-1",
		IsValid:      true,
		ExpiresAt:    &expiresAt,
		Registrar:    ptr("Example Registrar"),
		CheckedAt:    now,
	})
	if err != nil {
		t.Fatalf("PostDomainResult failed: %v", err)
	}

	if gotInstanceCode != "de-1" {
		t.Fatalf("expected instance code de-1, got %q", gotInstanceCode)
	}
	if body["monitoring_id"] != "domain-1" {
		t.Fatalf("expected monitoring_id=domain-1, got %#v", body["monitoring_id"])
	}
	if body["is_valid"] != true {
		t.Fatalf("expected is_valid=true, got %#v", body["is_valid"])
	}
	if body["registrar"] != "Example Registrar" {
		t.Fatalf("expected registrar=Example Registrar, got %#v", body["registrar"])
	}
	if body["expires_at"] != "2026-07-23T12:00:00Z" {
		t.Fatalf("expected expires_at RFC3339, got %#v", body["expires_at"])
	}
	if body["checked_at"] != "2026-04-24T12:00:00Z" {
		t.Fatalf("expected checked_at RFC3339, got %#v", body["checked_at"])
	}
}

func TestGetMonitoringsReturnsStatusError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	_, err := client.GetMonitorings(context.Background(), "de-1", nil)
	if err == nil {
		t.Fatalf("expected error")
	}

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", statusErr.StatusCode)
	}
	if statusErr.Body != "unauthorized" {
		t.Fatalf("expected body unauthorized, got %q", statusErr.Body)
	}
}

func TestGetMonitoringsRejectsOversizedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("a", maxResponseBodyBytes+1)))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key", "de-1")
	_, err := client.GetMonitorings(context.Background(), "de-1", nil)
	if err == nil {
		t.Fatalf("expected oversized response error")
	}
}

func TestGetMonitoringsWithoutBaseURLFails(t *testing.T) {
	t.Parallel()

	client := NewClient("", "secret", "de-1")
	_, err := client.GetMonitorings(context.Background(), "de-1", nil)
	if err == nil {
		t.Fatalf("expected error for empty base URL")
	}
}

func TestGetMonitoringsWithoutLocationFails(t *testing.T) {
	t.Parallel()

	client := NewClient("https://example.com", "secret", "de-1")
	_, err := client.GetMonitorings(context.Background(), "", nil)
	if err == nil {
		t.Fatalf("expected error for empty location")
	}
}

func TestGetMonitoringsWithoutInstanceCodeFails(t *testing.T) {
	t.Parallel()

	client := NewClient("https://example.com", "secret", "")
	_, err := client.GetMonitorings(context.Background(), "de-1", nil)
	if err == nil {
		t.Fatalf("expected error for empty instance code")
	}
}

func TestGetMonitoringsLocationMustMatchInstanceCode(t *testing.T) {
	t.Parallel()

	client := NewClient("https://example.com", "secret", "de-1")
	_, err := client.GetMonitorings(context.Background(), "us-1", nil)
	if err == nil {
		t.Fatalf("expected error for location mismatch")
	}
}

func ptr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}
