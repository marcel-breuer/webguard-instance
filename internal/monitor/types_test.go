package monitor

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMonitoringUnmarshalWithNumericID(t *testing.T) {
	t.Parallel()

	var monitoring Monitoring
	err := json.Unmarshal([]byte(`{
		"id": 42,
		"type": "http",
		"target": "https://example.com",
		"timeout": 10,
		"maintenance_active": false
	}`), &monitoring)
	if err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if monitoring.ID != "42" {
		t.Fatalf("expected id 42, got %s", monitoring.ID)
	}
}

func TestMonitoringUnmarshalWithStringID(t *testing.T) {
	t.Parallel()

	var monitoring Monitoring
	err := json.Unmarshal([]byte(`{
		"id": "42",
		"type": "http",
		"target": "https://example.com",
		"timeout": "10",
		"port": "443",
		"maintenance_active": "true"
	}`), &monitoring)
	if err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if monitoring.ID != "42" {
		t.Fatalf("expected id 42, got %s", monitoring.ID)
	}
	if monitoring.Timeout != 10 {
		t.Fatalf("expected timeout 10, got %d", monitoring.Timeout)
	}
	if monitoring.Port != 443 {
		t.Fatalf("expected port 443, got %d", monitoring.Port)
	}
	if !monitoring.MaintenanceActive {
		t.Fatalf("expected maintenance_active=true")
	}
}

func TestMonitoringUnmarshalWithInvalidID(t *testing.T) {
	t.Parallel()

	var monitoring Monitoring
	err := json.Unmarshal([]byte(`{
		"id": {"nested": true},
		"type": "http",
		"target": "https://example.com"
	}`), &monitoring)
	if err == nil {
		t.Fatalf("expected error for invalid id")
	}
}

func TestMonitoringUnmarshalHeartbeatMonitoring(t *testing.T) {
	t.Parallel()

	var monitoring Monitoring
	err := json.Unmarshal([]byte(`{
		"id": "hb-42",
		"type": "heartbeat",
		"target": "scheduler:nightly-job",
		"heartbeat_interval_minutes": "15",
		"heartbeat_grace_minutes": 5,
		"heartbeat_last_ping_at": "2026-04-18T10:15:00Z",
		"maintenance_active": false
	}`), &monitoring)
	if err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if monitoring.Type != TypeHeartbeat {
		t.Fatalf("expected type heartbeat, got %s", monitoring.Type)
	}
	if monitoring.HeartbeatIntervalMinutes == nil || *monitoring.HeartbeatIntervalMinutes != 15 {
		t.Fatalf("expected heartbeat_interval_minutes=15, got %#v", monitoring.HeartbeatIntervalMinutes)
	}
	if monitoring.HeartbeatGraceMinutes == nil || *monitoring.HeartbeatGraceMinutes != 5 {
		t.Fatalf("expected heartbeat_grace_minutes=5, got %#v", monitoring.HeartbeatGraceMinutes)
	}
	expectedLastPingAt := time.Date(2026, 4, 18, 10, 15, 0, 0, time.UTC)
	if monitoring.HeartbeatLastPingAt == nil || !monitoring.HeartbeatLastPingAt.Equal(expectedLastPingAt) {
		t.Fatalf("expected heartbeat_last_ping_at=%s, got %#v", expectedLastPingAt, monitoring.HeartbeatLastPingAt)
	}
}

func TestMonitoringUnmarshalDNSRecordMonitoring(t *testing.T) {
	t.Parallel()

	var monitoring Monitoring
	err := json.Unmarshal([]byte(`{
		"id": "dns-42",
		"type": "dns_record",
		"target": "example.com",
		"dns_record_type": "A",
		"dns_expected_values": ["192.0.2.10", "192.0.2.11"],
		"timeout": 5,
		"maintenance_active": false
	}`), &monitoring)
	if err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}

	if monitoring.Type != TypeDNSRecord {
		t.Fatalf("expected type dns_record, got %s", monitoring.Type)
	}
	if monitoring.DNSRecordType != "A" {
		t.Fatalf("expected dns_record_type A, got %q", monitoring.DNSRecordType)
	}
	if len(monitoring.DNSExpectedValues) != 2 || monitoring.DNSExpectedValues[0] != "192.0.2.10" || monitoring.DNSExpectedValues[1] != "192.0.2.11" {
		t.Fatalf("unexpected dns_expected_values: %#v", monitoring.DNSExpectedValues)
	}
}
