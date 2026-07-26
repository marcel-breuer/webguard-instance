package monitor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Type string

const (
	TypeHTTP             Type = "http"
	TypePing             Type = "ping"
	TypeKeyword          Type = "keyword"
	TypePort             Type = "port"
	TypeDNSRecord        Type = "dns_record"
	TypeHeartbeat        Type = "heartbeat"
	TypeDomainExpiration Type = "domain_expiration"
)

type Status string

const (
	StatusUp      Status = "up"
	StatusDown    Status = "down"
	StatusUnknown Status = "unknown"
)

type HTTPMethod string

const (
	HTTPMethodGet    HTTPMethod = "get"
	HTTPMethodPost   HTTPMethod = "post"
	HTTPMethodPut    HTTPMethod = "put"
	HTTPMethodPatch  HTTPMethod = "patch"
	HTTPMethodDelete HTTPMethod = "delete"
)

type Monitoring struct {
	ID   string `json:"id"`
	Type Type   `json:"type"`

	PreferredLocation  string   `json:"preferred_location"`
	PreferredLocations []string `json:"preferred_locations,omitempty"`

	Target string `json:"target"`

	Timeout int `json:"timeout"`

	HTTPMethod  HTTPMethod `json:"http_method"`
	HTTPBody    any        `json:"http_body"`
	HTTPHeaders any        `json:"http_headers"`

	AuthUsername string `json:"auth_username"`
	AuthPassword string `json:"auth_password"`

	Keyword string `json:"keyword"`
	Port    int    `json:"port"`

	DNSRecordType     string   `json:"dns_record_type"`
	DNSExpectedValues []string `json:"dns_expected_values"`

	HeartbeatIntervalMinutes *int       `json:"heartbeat_interval_minutes"`
	HeartbeatGraceMinutes    *int       `json:"heartbeat_grace_minutes"`
	HeartbeatLastPingAt      *time.Time `json:"heartbeat_last_ping_at"`

	MaintenanceActive bool `json:"maintenance_active"`
}

func (m *Monitoring) UnmarshalJSON(data []byte) error {
	type rawMonitoring struct {
		ID any `json:"id"`

		Type Type `json:"type"`

		PreferredLocation  string   `json:"preferred_location"`
		PreferredLocations []string `json:"preferred_locations"`

		Target string `json:"target"`

		Timeout any `json:"timeout"`

		HTTPMethod  HTTPMethod `json:"http_method"`
		HTTPBody    any        `json:"http_body"`
		HTTPHeaders any        `json:"http_headers"`

		AuthUsername string `json:"auth_username"`
		AuthPassword string `json:"auth_password"`

		Keyword string `json:"keyword"`
		Port    any    `json:"port"`

		DNSRecordType     string   `json:"dns_record_type"`
		DNSExpectedValues []string `json:"dns_expected_values"`

		HeartbeatIntervalMinutes any `json:"heartbeat_interval_minutes"`
		HeartbeatGraceMinutes    any `json:"heartbeat_grace_minutes"`
		HeartbeatLastPingAt      any `json:"heartbeat_last_ping_at"`

		MaintenanceActive any `json:"maintenance_active"`
	}

	var raw rawMonitoring
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	id, err := parseStringFlexible(raw.ID, "id")
	if err != nil {
		return err
	}

	timeout, err := parseIntFlexible(raw.Timeout, "timeout")
	if err != nil {
		return err
	}
	port, err := parseIntFlexible(raw.Port, "port")
	if err != nil {
		return err
	}
	heartbeatIntervalMinutes, err := parseOptionalIntFlexible(raw.HeartbeatIntervalMinutes, "heartbeat_interval_minutes")
	if err != nil {
		return err
	}
	heartbeatGraceMinutes, err := parseOptionalIntFlexible(raw.HeartbeatGraceMinutes, "heartbeat_grace_minutes")
	if err != nil {
		return err
	}
	heartbeatLastPingAt, err := parseTimeFlexible(raw.HeartbeatLastPingAt, "heartbeat_last_ping_at")
	if err != nil {
		return err
	}
	maintenanceActive, err := parseBoolFlexible(raw.MaintenanceActive, "maintenance_active")
	if err != nil {
		return err
	}

	*m = Monitoring{
		ID:   id,
		Type: raw.Type,

		PreferredLocation:  strings.TrimSpace(raw.PreferredLocation),
		PreferredLocations: raw.PreferredLocations,

		Target: raw.Target,

		Timeout: timeout,

		HTTPMethod:  raw.HTTPMethod,
		HTTPBody:    raw.HTTPBody,
		HTTPHeaders: raw.HTTPHeaders,

		AuthUsername: raw.AuthUsername,
		AuthPassword: raw.AuthPassword,

		Keyword: raw.Keyword,
		Port:    port,

		DNSRecordType:     raw.DNSRecordType,
		DNSExpectedValues: raw.DNSExpectedValues,

		HeartbeatIntervalMinutes: heartbeatIntervalMinutes,
		HeartbeatGraceMinutes:    heartbeatGraceMinutes,
		HeartbeatLastPingAt:      heartbeatLastPingAt,

		MaintenanceActive: maintenanceActive,
	}

	return nil
}

type MonitoringResponsePayload struct {
	MonitoringID   string   `json:"monitoring_id"`
	Status         Status   `json:"status"`
	ResponseTime   *float64 `json:"response_time"`
	HTTPStatusCode *int     `json:"http_status_code"`
}

type SSLResultPayload struct {
	MonitoringID string     `json:"monitoring_id"`
	IsValid      bool       `json:"is_valid"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Issuer       *string    `json:"issuer"`
	IssuedAt     *time.Time `json:"issued_at"`
}

type DomainResultPayload struct {
	MonitoringID string     `json:"monitoring_id"`
	IsValid      bool       `json:"is_valid"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Registrar    *string    `json:"registrar"`
	CheckedAt    time.Time  `json:"checked_at"`
}

func parseStringFlexible(value any, field string) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(typed), nil
	case float64:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case int:
		return strconv.Itoa(typed), nil
	case json.Number:
		return typed.String(), nil
	default:
		return "", fmt.Errorf("invalid %s type: %T", field, value)
	}
}

func parseInt64Flexible(value any, field string) (int64, error) {
	switch typed := value.(type) {
	case nil:
		return 0, nil
	case float64:
		return int64(typed), nil
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", field, err)
		}
		return parsed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid %s: %w", field, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid %s type: %T", field, value)
	}
}

func parseIntFlexible(value any, field string) (int, error) {
	parsed, err := parseInt64Flexible(value, field)
	if err != nil {
		return 0, err
	}
	return int(parsed), nil
}

func parseOptionalIntFlexible(value any, field string) (*int, error) {
	if value == nil {
		return nil, nil
	}

	parsed, err := parseIntFlexible(value, field)
	if err != nil {
		return nil, err
	}

	return &parsed, nil
}

func parseTimeFlexible(value any, field string) (*time.Time, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil, nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, trimmed)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", field, err)
		}
		return &parsed, nil
	case time.Time:
		parsed := typed.UTC()
		return &parsed, nil
	default:
		return nil, fmt.Errorf("invalid %s type: %T", field, value)
	}
}

func parseBoolFlexible(value any, field string) (bool, error) {
	switch typed := value.(type) {
	case nil:
		return false, nil
	case bool:
		return typed, nil
	case float64:
		return typed != 0, nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return false, fmt.Errorf("invalid %s: %w", field, err)
		}
		return parsed != 0, nil
	case string:
		trimmed := strings.TrimSpace(strings.ToLower(typed))
		if trimmed == "" {
			return false, nil
		}
		switch trimmed {
		case "1", "true", "yes", "on":
			return true, nil
		case "0", "false", "no", "off":
			return false, nil
		default:
			return false, fmt.Errorf("invalid %s: %q", field, typed)
		}
	default:
		return false, fmt.Errorf("invalid %s type: %T", field, value)
	}
}
