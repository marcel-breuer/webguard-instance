package coreapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/application"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

const (
	maxResponseBodyBytes = 4 << 20
	maxClaimBatchSize    = 256
	maxAPIKeyFileBytes   = 8 << 10

	DefaultInstanceAPIBasePath = "/api/instances"
)

type Client struct {
	baseURL         string
	apiKey          string
	apiKeyFile      string
	instanceCode    string
	instanceAPIPath string
	httpClient      *http.Client
	telemetry       *application.Telemetry
}

type HTTPStatusError struct {
	StatusCode int
	Body       string
}

type transportError struct {
	err error
}

func (e *transportError) Error() string {
	return e.err.Error()
}

func (e *transportError) Unwrap() error {
	return e.err
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("core API returned status %d", e.StatusCode)
}

func NewClient(baseURL, apiKey, instanceCode string) *Client {
	return &Client{
		baseURL:         strings.TrimRight(baseURL, "/"),
		apiKey:          strings.TrimSpace(apiKey),
		instanceCode:    strings.TrimSpace(instanceCode),
		instanceAPIPath: DefaultInstanceAPIBasePath,
		httpClient: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: rejectCrossOriginRedirect,
		},
	}
}

func rejectCrossOriginRedirect(request *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) >= 5 {
		return fmt.Errorf("stopped after 5 Core API redirects")
	}
	origin := via[0].URL
	if request.URL.Scheme != origin.Scheme || !strings.EqualFold(request.URL.Host, origin.Host) {
		return fmt.Errorf("Core API redirect changed origin")
	}
	return nil
}

func NewClientWithInstanceAPIPath(baseURL, apiKey, instanceCode, instanceAPIPath string) *Client {
	client := NewClient(baseURL, apiKey, instanceCode)
	if err := client.SetInstanceAPIPath(instanceAPIPath); err != nil {
		client.instanceAPIPath = ""
	}
	return client
}

func (c *Client) SetInstanceAPIPath(instanceAPIPath string) error {
	normalized, err := normalizeInstanceAPIPath(instanceAPIPath)
	if err != nil {
		return err
	}
	c.instanceAPIPath = normalized
	return nil
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

// SetAPIKeyFile configures a secret file that is reread for each request so
// mounted credentials can be rotated without restarting the worker.
func (c *Client) SetAPIKeyFile(path string) {
	c.apiKeyFile = strings.TrimSpace(path)
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

	request, err := c.newRequest(ctx, http.MethodGet, c.instanceAPIEndpoint("/monitorings"), query, nil)
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
	return c.postCallbackJSON(ctx, "/monitoring-responses", "post_monitoring_response", payload.IdempotencyKey, payload)
}

func (c *Client) PostSSLResult(ctx context.Context, payload monitor.SSLResultPayload) error {
	return c.postCallbackJSON(ctx, "/ssl-results", "post_ssl_result", payload.IdempotencyKey, payload)
}

func (c *Client) PostDomainResult(ctx context.Context, payload monitor.DomainResultPayload) error {
	return c.postCallbackJSON(ctx, "/domain-results", "post_domain_result", payload.IdempotencyKey, payload)
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
	if payload.MaxBatchSize > maxClaimBatchSize {
		return nil, fmt.Errorf("claim max batch size exceeds %d", maxClaimBatchSize)
	}

	request, err := c.newRequest(ctx, http.MethodPost, c.instanceAPIEndpoint("/monitoring-jobs/claim"), nil, payload)
	if err != nil {
		return nil, err
	}

	var response monitor.ClaimMonitoringJobsResponse
	if err := c.doJSON(request, &response, "claim_monitoring_jobs"); err != nil {
		return nil, err
	}
	if len(response.Jobs) > payload.MaxBatchSize {
		return nil, fmt.Errorf("Core API returned more jobs than the requested batch limit")
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
	request, err := c.newRequest(ctx, http.MethodPost, c.instanceAPIEndpoint("/monitoring-jobs/"+url.PathEscape(jobID)+"/complete"), nil, payload)
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
	request, err := c.newRequest(ctx, http.MethodPost, c.instanceAPIEndpoint("/monitoring-jobs/"+url.PathEscape(jobID)+"/release"), nil, payload)
	if err != nil {
		return err
	}
	return c.doJSON(request, nil, "release_monitoring_job")
}

func (c *Client) ExtendMonitoringJob(ctx context.Context, jobID string, payload monitor.ExtendMonitoringJobRequest) (monitor.ExtendMonitoringJobResponse, error) {
	if strings.TrimSpace(jobID) == "" {
		return monitor.ExtendMonitoringJobResponse{}, fmt.Errorf("job ID is empty")
	}
	request, err := c.newRequest(ctx, http.MethodPost, c.instanceAPIEndpoint("/monitoring-jobs/"+url.PathEscape(jobID)+"/extend"), nil, payload)
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
	if c.instanceAPIPath == "" {
		return nil, fmt.Errorf("WEBGUARD_INSTANCE_API_BASE_PATH is invalid")
	}

	endpoint, err := url.Parse(c.baseURL + path)
	if err != nil {
		return nil, err
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("WEBGUARD_CORE_API_URL must use HTTP or HTTPS")
	}
	if endpoint.Host == "" {
		return nil, fmt.Errorf("WEBGUARD_CORE_API_URL must include a host")
	}
	if endpoint.User != nil {
		return nil, fmt.Errorf("WEBGUARD_CORE_API_URL must not contain credentials")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("WEBGUARD_CORE_API_URL must not contain a query or fragment")
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
	apiKey, err := c.configuredAPIKey()
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		request.Header.Set("X-API-KEY", apiKey)
	}
	if c.instanceCode != "" {
		request.Header.Set("X-INSTANCE-CODE", c.instanceCode)
	}

	return request, nil
}

func (c *Client) configuredAPIKey() (string, error) {
	if c.apiKeyFile == "" {
		return c.apiKey, nil
	}
	file, err := os.Open(c.apiKeyFile)
	if err != nil {
		return "", fmt.Errorf("read WEBGUARD_CORE_API_KEY_FILE: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxAPIKeyFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read WEBGUARD_CORE_API_KEY_FILE: %w", err)
	}
	if len(content) > maxAPIKeyFileBytes {
		return "", fmt.Errorf("WEBGUARD_CORE_API_KEY_FILE exceeds %d bytes", maxAPIKeyFileBytes)
	}
	apiKey := strings.TrimSpace(string(content))
	if apiKey == "" {
		return "", fmt.Errorf("WEBGUARD_CORE_API_KEY_FILE is empty")
	}
	return apiKey, nil
}

func (c *Client) instanceAPIEndpoint(suffix string) string {
	return c.instanceAPIPath + suffix
}

func (c *Client) postCallbackJSON(ctx context.Context, path, operation, idempotencyKey string, payload any) error {
	maxAttempts := 1
	if strings.TrimSpace(idempotencyKey) != "" {
		maxAttempts = 2
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		request, err := c.newRequest(ctx, http.MethodPost, c.instanceAPIEndpoint(path), nil, payload)
		if err != nil {
			return err
		}
		if idempotencyKey != "" {
			request.Header.Set("Idempotency-Key", idempotencyKey)
		}

		err = c.doJSON(request, nil, operation)
		if err == nil || attempt == maxAttempts-1 || ctx.Err() != nil || !isRetryableCallbackError(err) {
			return err
		}

		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return nil
}

func isRetryableCallbackError(err error) bool {
	var statusError *HTTPStatusError
	if errors.As(err, &statusError) {
		return statusError.StatusCode >= http.StatusInternalServerError
	}

	var requestError *transportError
	return errors.As(err, &requestError)
}

func normalizeInstanceAPIPath(instanceAPIPath string) (string, error) {
	normalized := "/" + strings.Trim(strings.TrimSpace(instanceAPIPath), "/")
	switch normalized {
	case DefaultInstanceAPIBasePath:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported WEBGUARD_INSTANCE_API_BASE_PATH %q", instanceAPIPath)
	}
}

func (c *Client) doJSON(request *http.Request, out any, operation string) (resultErr error) {
	startedAt := time.Now()
	defer func() {
		c.telemetry.RecordCoreRequest(operation, time.Since(startedAt), resultErr)
	}()
	response, err := c.httpClient.Do(request)
	if err != nil {
		return &transportError{err: err}
	}
	defer response.Body.Close()

	raw, err := readLimitedBody(response.Body, maxResponseBodyBytes)
	if err != nil {
		return &transportError{err: err}
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
