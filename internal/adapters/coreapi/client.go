package coreapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const maxResponseBodyBytes = 4 << 20

type Client struct {
	baseURL      string
	apiKey       string
	instanceCode string
	httpClient   *http.Client
	telemetry    *application.Telemetry
}

type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("core API returned status %d", e.StatusCode)
}

func NewClient(baseURL, apiKey, instanceCode string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       strings.TrimSpace(apiKey),
		instanceCode: strings.TrimSpace(instanceCode),
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) SetHTTPClient(httpClient *http.Client) {
	if httpClient == nil {
		return
	}
	c.httpClient = httpClient
}

func (c *Client) SetTelemetry(telemetry *application.Telemetry) {
	c.telemetry = telemetry
}

func (c *Client) GetMonitorings(ctx context.Context, location string, types []monitor.Type) ([]monitor.Monitoring, error) {
	location = strings.TrimSpace(location)
	if location == "" {
		return nil, fmt.Errorf("WEBGUARD_LOCATION is empty")
	}
	if c.instanceCode == "" {
		return nil, fmt.Errorf("WEBGUARD_LOCATION is empty")
	}
	if location != c.instanceCode {
		return nil, fmt.Errorf("location must match instance code")
	}

	if len(types) == 0 {
		return c.getMonitorings(ctx, location, "")
	}

	seenTypes := make(map[monitor.Type]struct{}, len(types))
	seenMonitorings := make(map[string]struct{})
	monitorings := make([]monitor.Monitoring, 0)

	for _, monitoringType := range types {
		if _, ok := seenTypes[monitoringType]; ok {
			continue
		}
		seenTypes[monitoringType] = struct{}{}

		items, err := c.getMonitorings(ctx, location, monitoringType)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if _, ok := seenMonitorings[item.ID]; ok {
				continue
			}
			seenMonitorings[item.ID] = struct{}{}
			monitorings = append(monitorings, item)
		}
	}

	return monitorings, nil
}

func (c *Client) getMonitorings(ctx context.Context, location string, monitoringType monitor.Type) ([]monitor.Monitoring, error) {
	query := make(url.Values)
	query.Set("location", location)
	if monitoringType != "" {
		query.Set("type", string(monitoringType))
	}

	request, err := c.newRequest(ctx, http.MethodGet, "/api/v1/internal/monitorings", query, nil)
	if err != nil {
		return nil, err
	}

	var monitorings []monitor.Monitoring
	if err := c.doJSON(request, &monitorings, "get_monitorings"); err != nil {
		return nil, err
	}
	return monitorings, nil
}

func (c *Client) PostMonitoringResponse(ctx context.Context, payload monitor.MonitoringResponsePayload) error {
	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/monitoring-responses", nil, payload)
	if err != nil {
		return err
	}

	return c.doJSON(request, nil, "post_monitoring_response")
}

func (c *Client) PostSSLResult(ctx context.Context, payload monitor.SSLResultPayload) error {
	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/ssl-results", nil, payload)
	if err != nil {
		return err
	}

	return c.doJSON(request, nil, "post_ssl_result")
}

func (c *Client) PostDomainResult(ctx context.Context, payload monitor.DomainResultPayload) error {
	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/domain-results", nil, payload)
	if err != nil {
		return err
	}

	return c.doJSON(request, nil, "post_domain_result")
}

func (c *Client) ClaimMonitoringJobs(ctx context.Context, payload monitor.ClaimMonitoringJobsRequest) ([]monitor.ClaimedJob, error) {
	if strings.TrimSpace(payload.Location) == "" {
		return nil, fmt.Errorf("claim location is empty")
	}
	if strings.TrimSpace(payload.InstanceID) == "" {
		return nil, fmt.Errorf("WEBGUARD_INSTANCE_ID is empty")
	}
	if payload.Capacity < 1 {
		return nil, fmt.Errorf("claim capacity must be positive")
	}
	if payload.MaxBatchSize < 1 {
		return nil, fmt.Errorf("claim max batch size must be positive")
	}

	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/monitoring-jobs/claim", nil, payload)
	if err != nil {
		return nil, err
	}

	var response monitor.ClaimMonitoringJobsResponse
	if err := c.doJSON(request, &response, "claim_monitoring_jobs"); err != nil {
		return nil, err
	}
	return response.Jobs, nil
}

func (c *Client) CompleteMonitoringJob(ctx context.Context, jobID, idempotencyKey string, payload monitor.CompleteMonitoringJobRequest) error {
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("job ID is empty")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("job idempotency key is empty")
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/monitoring-jobs/"+url.PathEscape(jobID)+"/complete", nil, payload)
	if err != nil {
		return err
	}
	request.Header.Set("Idempotency-Key", idempotencyKey)
	return c.doJSON(request, nil, "complete_monitoring_job")
}

func (c *Client) ReleaseMonitoringJob(ctx context.Context, jobID string, payload monitor.ReleaseMonitoringJobRequest) error {
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("job ID is empty")
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/monitoring-jobs/"+url.PathEscape(jobID)+"/release", nil, payload)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil, "release_monitoring_job")
}

func (c *Client) ExtendMonitoringJob(ctx context.Context, jobID string, payload monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error) {
	if strings.TrimSpace(jobID) == "" {
		return monitor.ExtendMonitoringJobResponse{}, fmt.Errorf("job ID is empty")
	}
	request, err := c.newRequest(ctx, http.MethodPost, "/api/v1/internal/monitoring-jobs/"+url.PathEscape(jobID)+"/extend", nil, payload)
	if err != nil {
		return monitor.ExtendMonitoringJobResponse{}, err
	}
	var response monitor.ExtendMonitoringJobResponse
	if err := c.doJSON(request, &response, "extend_monitoring_job"); err != nil {
		return monitor.ExtendMonitoringJobResponse{}, err
	}
	return response, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body any) (*http.Request, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("WEBGUARD_CORE_API_URL is empty")
	}

	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, err
	}
	if query != nil {
		endpoint.RawQuery = query.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		request.Header.Set("X-API-KEY", c.apiKey)
	}
	if c.instanceCode != "" {
		request.Header.Set("X-INSTANCE-CODE", c.instanceCode)
	}

	return request, nil
}

func (c *Client) doJSON(request *http.Request, out any, operation string) (resultErr error) {
	startedAt := time.Now()
	defer func() {
		c.telemetry.RecordCoreRequest(operation, time.Since(startedAt), resultErr)
	}()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	raw, err := readLimitedBody(response.Body, maxResponseBodyBytes)
	if err != nil {
		return err
	}

	if response.StatusCode >= http.StatusBadRequest {
		return &HTTPStatusError{
			StatusCode: response.StatusCode,
			Body:       string(raw),
		}
	}

	if out == nil || len(raw) == 0 {
		return nil
	}

	return json.Unmarshal(raw, out)
}

func readLimitedBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds %d bytes", maxBytes)
	}
	return payload, nil
}
