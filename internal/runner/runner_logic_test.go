package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/core"
	"github.com/marcel-breuer/webguard-instance/internal/domainlookup"
	"github.com/marcel-breuer/webguard-instance/internal/monitor"
)

type staticDomainLookup struct {
	result domainlookup.Result
	err    error
}

func (s staticDomainLookup) Lookup(_ context.Context, target string) (domainlookup.Result, error) {
	result := s.result
	if result.Domain == "" {
		result.Domain = target
	}
	return result, s.err
}

type staticDNSRecordResolver struct {
	values     []string
	err        error
	target     string
	recordType string
	timeout    time.Duration
}

func (s *staticDNSRecordResolver) Resolve(_ context.Context, target string, recordType string, timeout time.Duration) ([]string, error) {
	s.target = target
	s.recordType = recordType
	s.timeout = timeout
	return append([]string(nil), s.values...), s.err
}

func TestNormalizeHeaders(t *testing.T) {
	t.Parallel()

	headers := normalizeHeaders(`{"X-Test":"value","X-Int":1}`)
	if headers["X-Test"] != "value" {
		t.Fatalf("expected X-Test header")
	}
	if headers["X-Int"] != "1" {
		t.Fatalf("expected X-Int header to be stringified")
	}

	headers = normalizeHeaders("not-json")
	if len(headers) != 0 {
		t.Fatalf("expected empty headers for invalid json, got %#v", headers)
	}
}

func TestNormalizeBody(t *testing.T) {
	t.Parallel()

	body := normalizeBody(`{"key":"value"}`)
	var parsed map[string]string
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("expected valid JSON body, got error: %v", err)
	}
	if parsed["key"] != "value" {
		t.Fatalf("unexpected parsed value: %#v", parsed)
	}

	body = normalizeBody("invalid-json")
	if string(body) != "[]" {
		t.Fatalf("expected fallback body [] for invalid JSON string, got %s", string(body))
	}
}

func TestPerformHTTPRequestGETWithHeadersAndBasicAuth(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", request.Method)
		}
		if request.Header.Get("X-Test") != "value" {
			t.Fatalf("expected X-Test header")
		}

		username, password, ok := request.BasicAuth()
		if !ok {
			t.Fatalf("expected basic auth")
		}
		if username != "user" || password != "pass" {
			t.Fatalf("unexpected basic auth credentials")
		}

		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	statusCode, body, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target:       server.URL,
		Timeout:      2,
		HTTPMethod:   monitor.HTTPMethodGet,
		HTTPHeaders:  `{"X-Test":"value"}`,
		AuthUsername: "user",
		AuthPassword: "pass",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", statusCode)
	}
	if body != "ok" {
		t.Fatalf("expected body ok, got %q", body)
	}
}

func TestPerformHTTPRequestPOSTBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", request.Method)
		}

		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("failed reading body: %v", err)
		}

		var parsed map[string]string
		if err := json.Unmarshal(payload, &parsed); err != nil {
			t.Fatalf("invalid JSON body: %v", err)
		}
		if parsed["key"] != "value" {
			t.Fatalf("unexpected body payload: %#v", parsed)
		}

		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	statusCode, _, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target:     server.URL,
		Timeout:    2,
		HTTPMethod: monitor.HTTPMethodPost,
		HTTPBody:   `{"key":"value"}`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", statusCode)
	}
}

func TestPerformHTTPRequestFollowsRedirectAcrossHosts(t *testing.T) {
	t.Parallel()

	targetServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("redirect-ok"))
	}))
	defer targetServer.Close()

	redirectServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, targetServer.URL, http.StatusMovedPermanently)
	}))
	defer redirectServer.Close()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	statusCode, body, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target:     redirectServer.URL,
		Timeout:    2,
		HTTPMethod: monitor.HTTPMethodGet,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected final status 200 after redirect, got %d", statusCode)
	}
	if body != "redirect-ok" {
		t.Fatalf("expected redirected response body, got %q", body)
	}
}

func TestHandleHTTPMonitoringTreatsRedirectStatusAsUp(t *testing.T) {
	t.Parallel()

	redirectOnlyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusMovedPermanently)
	}))
	defer redirectOnlyServer.Close()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	status, responseTime, httpStatusCode := r.handleHTTPMonitoring(context.Background(), monitor.Monitoring{
		Target:     redirectOnlyServer.URL,
		Timeout:    2,
		HTTPMethod: monitor.HTTPMethodGet,
	})

	if status != monitor.StatusUp {
		t.Fatalf("expected up for redirect response, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected response time for redirect response")
	}
	if httpStatusCode == nil {
		t.Fatalf("expected http status code")
	}
	if *httpStatusCode != http.StatusMovedPermanently {
		t.Fatalf("expected http status code 301, got %d", *httpStatusCode)
	}
}

func TestHandleKeywordMonitoringReturnsHTTPStatusCodeWhenKeywordMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
		_, _ = writer.Write([]byte("different-content"))
	}))
	defer server.Close()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	status, responseTime, httpStatusCode := r.handleKeywordMonitoring(context.Background(), monitor.Monitoring{
		Target:     server.URL,
		Timeout:    2,
		HTTPMethod: monitor.HTTPMethodGet,
		Keyword:    "needle",
	})

	if status != monitor.StatusDown {
		t.Fatalf("expected down when keyword is missing, got %s", status)
	}
	if responseTime != nil {
		t.Fatalf("expected nil response time when keyword is missing, got %v", *responseTime)
	}
	if httpStatusCode == nil {
		t.Fatalf("expected http status code")
	}
	if *httpStatusCode != http.StatusTeapot {
		t.Fatalf("expected http status code %d, got %d", http.StatusTeapot, *httpStatusCode)
	}
}

func TestPerformHTTPRequestRetriesOnTransportError(t *testing.T) {
	t.Parallel()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	start := time.Now()
	_, _, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target:     "http://127.0.0.1:1",
		Timeout:    1,
		HTTPMethod: monitor.HTTPMethodGet,
	})
	if err == nil {
		t.Fatalf("expected transport error")
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("expected retry delay to be applied")
	}
}

func TestHandlePingMonitoringSupportsHostnameAndIPTargets(t *testing.T) {
	originalExecutor := pingExecutor
	t.Cleanup(func() {
		pingExecutor = originalExecutor
	})

	testCases := []struct {
		name   string
		target string
	}{
		{name: "hostname", target: "example.com"},
		{name: "ipv4", target: "8.8.8.8"},
		{name: "ipv6", target: "2001:4860:4860::8888"},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			var receivedHost string
			var receivedTimeout int
			pingExecutor = func(_ context.Context, host string, timeoutSeconds int) ([]byte, error) {
				receivedHost = host
				receivedTimeout = timeoutSeconds
				return []byte("64 bytes from " + host + ": icmp_seq=1 ttl=57 time=12.34 ms"), nil
			}

			status, responseTime := handlePingMonitoring(monitor.Monitoring{
				Target:  testCase.target,
				Timeout: 2,
			})

			if status != monitor.StatusUp {
				t.Fatalf("expected up, got %s", status)
			}
			if responseTime == nil {
				t.Fatalf("expected response time")
			}
			if *responseTime != 12.34 {
				t.Fatalf("expected response time 12.34, got %v", *responseTime)
			}
			if receivedHost != testCase.target {
				t.Fatalf("expected ping target %q, got %q", testCase.target, receivedHost)
			}
			if receivedTimeout != 2 {
				t.Fatalf("expected timeout 2, got %d", receivedTimeout)
			}
		})
	}
}

func TestHandlePingMonitoringDown(t *testing.T) {
	originalExecutor := pingExecutor
	t.Cleanup(func() {
		pingExecutor = originalExecutor
	})

	pingExecutor = func(_ context.Context, _ string, _ int) ([]byte, error) {
		return []byte("100% packet loss"), errors.New("exit status 1")
	}

	status, responseTime := handlePingMonitoring(monitor.Monitoring{
		Target: "8.8.8.8",
	})
	if status != monitor.StatusDown {
		t.Fatalf("expected down, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected fallback response time")
	}
}

func TestBuildPingCommand(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		host     string
		timeout  int
		expected []string
	}{
		{
			name:     "hostname",
			host:     "example.com",
			timeout:  5,
			expected: []string{"-c", "1", "-W", "5", "example.com"},
		},
		{
			name:     "ipv4",
			host:     "8.8.8.8",
			timeout:  3,
			expected: []string{"-c", "1", "-W", "3", "-4", "8.8.8.8"},
		},
		{
			name:     "ipv6",
			host:     "2001:4860:4860::8888",
			timeout:  4,
			expected: []string{"-c", "1", "-W", "4", "-6", "2001:4860:4860::8888"},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			command, args := buildPingCommand(testCase.host, testCase.timeout)
			if command != "ping" {
				t.Fatalf("expected ping command, got %q", command)
			}
			if !reflect.DeepEqual(args, testCase.expected) {
				t.Fatalf("unexpected ping args: got %#v want %#v", args, testCase.expected)
			}
		})
	}
}

func TestHandlePortMonitoringDown(t *testing.T) {
	t.Parallel()

	status, responseTime := handlePortMonitoring(monitor.Monitoring{
		Target: "127.0.0.1",
		Port:   1,
	})
	if status != monitor.StatusDown {
		t.Fatalf("expected down, got %s", status)
	}
	if responseTime != nil {
		t.Fatalf("expected nil response time for failed port monitoring")
	}
}

func TestCrawlResponseMonitoringUnknownType(t *testing.T) {
	t.Parallel()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	status, responseTime, httpStatusCode := r.crawlResponseMonitoring(context.Background(), monitor.Monitoring{
		Type: monitor.Type("custom"),
	})
	if status != monitor.StatusUnknown {
		t.Fatalf("expected unknown status, got %s", status)
	}
	if responseTime != nil {
		t.Fatalf("expected nil response time for unknown type")
	}
	if httpStatusCode != nil {
		t.Fatalf("expected nil http status code for unknown type")
	}
}

func TestCrawlResponseMonitoringPortReturnsNilHTTPStatusCode(t *testing.T) {
	t.Parallel()

	server, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to open listener: %v", err)
	}
	defer server.Close()

	_, portRaw, err := net.SplitHostPort(server.Addr().String())
	if err != nil {
		t.Fatalf("failed to split listener address: %v", err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("failed to parse listener port: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := server.Accept()
		if acceptErr == nil && conn != nil {
			_ = conn.Close()
		}
	}()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	status, _, httpStatusCode := r.crawlResponseMonitoring(context.Background(), monitor.Monitoring{
		Type:   monitor.TypePort,
		Target: "127.0.0.1",
		Port:   port,
	})

	if status != monitor.StatusUp {
		t.Fatalf("expected up status for open port, got %s", status)
	}
	if httpStatusCode != nil {
		t.Fatalf("expected nil http status code for port monitoring")
	}

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("port monitor did not connect to test listener")
	}
}

func TestDNSRecordCheckerARecordMatchReportsUp(t *testing.T) {
	t.Parallel()

	resolver := &staticDNSRecordResolver{
		values: []string{"192.0.2.10", "192.0.2.11"},
	}
	checker := NewDNSRecordChecker(resolver, log.New(io.Discard, "", 0))

	status, responseTime := checker.Check(context.Background(), monitor.Monitoring{
		Type:              monitor.TypeDNSRecord,
		Target:            "example.com",
		Timeout:           3,
		DNSRecordType:     "A",
		DNSExpectedValues: []string{"192.0.2.11", "192.0.2.10"},
	})

	if status != monitor.StatusUp {
		t.Fatalf("expected up, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected response time")
	}
	if resolver.target != "example.com" {
		t.Fatalf("expected resolver target example.com, got %q", resolver.target)
	}
	if resolver.recordType != "A" {
		t.Fatalf("expected resolver record type A, got %q", resolver.recordType)
	}
	if resolver.timeout != 3*time.Second {
		t.Fatalf("expected resolver timeout 3s, got %s", resolver.timeout)
	}
}

func TestDNSRecordCheckerARecordMismatchReportsDown(t *testing.T) {
	t.Parallel()

	checker := NewDNSRecordChecker(&staticDNSRecordResolver{
		values: []string{"192.0.2.20"},
	}, log.New(io.Discard, "", 0))

	status, responseTime := checker.Check(context.Background(), monitor.Monitoring{
		Target:            "example.com",
		DNSRecordType:     "A",
		DNSExpectedValues: []string{"192.0.2.10"},
	})

	if status != monitor.StatusDown {
		t.Fatalf("expected down, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected response time")
	}
}

func TestDNSRecordCheckerMissingRecordReportsDown(t *testing.T) {
	t.Parallel()

	checker := NewDNSRecordChecker(&staticDNSRecordResolver{
		err: errors.New("record not found"),
	}, log.New(io.Discard, "", 0))

	status, responseTime := checker.Check(context.Background(), monitor.Monitoring{
		Target:            "example.com",
		DNSRecordType:     "A",
		DNSExpectedValues: []string{"192.0.2.10"},
	})

	if status != monitor.StatusDown {
		t.Fatalf("expected down, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected response time")
	}
}

func TestDNSRecordCheckerExtraValueReportsDown(t *testing.T) {
	t.Parallel()

	checker := NewDNSRecordChecker(&staticDNSRecordResolver{
		values: []string{"192.0.2.10", "192.0.2.11"},
	}, log.New(io.Discard, "", 0))

	status, _ := checker.Check(context.Background(), monitor.Monitoring{
		Target:            "example.com",
		DNSRecordType:     "A",
		DNSExpectedValues: []string{"192.0.2.10"},
	})

	if status != monitor.StatusDown {
		t.Fatalf("expected down for unexpected extra value, got %s", status)
	}
}

func TestDNSRecordCheckerUnsupportedRecordTypeReportsDown(t *testing.T) {
	t.Parallel()

	checker := NewDNSRecordChecker(&staticDNSRecordResolver{
		values: []string{"value"},
	}, log.New(io.Discard, "", 0))

	status, responseTime := checker.Check(context.Background(), monitor.Monitoring{
		Target:            "example.com",
		DNSRecordType:     "HTTPS",
		DNSExpectedValues: []string{"value"},
	})

	if status != monitor.StatusDown {
		t.Fatalf("expected down for unsupported record type, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected response time")
	}
}

func TestNormalizeDNSRecordValues(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		recordType string
		values     []string
		expected   []string
	}{
		{
			name:       "CNAME trailing dot",
			recordType: "CNAME",
			values:     []string{"Target.Example.COM."},
			expected:   []string{"target.example.com"},
		},
		{
			name:       "NS trailing dot",
			recordType: "NS",
			values:     []string{"NS1.Example.COM."},
			expected:   []string{"ns1.example.com"},
		},
		{
			name:       "MX priority and host",
			recordType: "MX",
			values:     []string{"10 Mail.EXAMPLE.com."},
			expected:   []string{"10 mail.example.com"},
		},
		{
			name:       "TXT quotes",
			recordType: "TXT",
			values:     []string{`"v=spf1 include:example.com ~all"`},
			expected:   []string{"v=spf1 include:example.com ~all"},
		},
		{
			name:       "SOA deterministic fields",
			recordType: "SOA",
			values:     []string{"NS1.Example.COM. Hostmaster.Example.COM. 2026051401 7200 3600 1209600 3600"},
			expected:   []string{"ns1.example.com hostmaster.example.com 2026051401 7200 3600 1209600 3600"},
		},
		{
			name:       "CAA fields",
			recordType: "CAA",
			values:     []string{`0 Issue "letsencrypt.org"`},
			expected:   []string{"0 issue letsencrypt.org"},
		},
		{
			name:       "order and duplicates",
			recordType: "A",
			values:     []string{"192.0.2.11", "192.0.2.10", "192.0.2.10"},
			expected:   []string{"192.0.2.10", "192.0.2.11"},
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			actual, err := normalizeDNSRecordValues(testCase.values, testCase.recordType)
			if err != nil {
				t.Fatalf("unexpected normalize error: %v", err)
			}
			if !reflect.DeepEqual(actual, testCase.expected) {
				t.Fatalf("unexpected normalized values: got %#v want %#v", actual, testCase.expected)
			}
		})
	}
}

func TestCrawlResponseMonitoringDNSRecordReturnsNilHTTPStatusCode(t *testing.T) {
	t.Parallel()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	r.dnsChecker = NewDNSRecordChecker(&staticDNSRecordResolver{
		values: []string{"target.example.com."},
	}, log.New(io.Discard, "", 0))

	status, responseTime, httpStatusCode := r.crawlResponseMonitoring(context.Background(), monitor.Monitoring{
		Type:              monitor.TypeDNSRecord,
		Target:            "example.com",
		DNSRecordType:     "CNAME",
		DNSExpectedValues: []string{"target.example.com"},
	})

	if status != monitor.StatusUp {
		t.Fatalf("expected up, got %s", status)
	}
	if responseTime == nil {
		t.Fatalf("expected response time")
	}
	if httpStatusCode != nil {
		t.Fatalf("expected nil http status code for dns_record monitoring")
	}
}

func TestCrawlMonitoringSSLValid(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := New(nil, config.Config{}, log.New(io.Discard, "", 0))
	payload := r.crawlMonitoringSSL(monitor.Monitoring{
		ID:     "12",
		Target: server.URL,
	})

	if payload.MonitoringID != "12" {
		t.Fatalf("unexpected monitoring id: %s", payload.MonitoringID)
	}
	if !payload.IsValid {
		t.Fatalf("expected certificate to be valid for httptest TLS server")
	}
	if payload.ExpiresAt == nil || payload.IssuedAt == nil {
		t.Fatalf("expected issued/expires timestamps")
	}
}

func TestRunSSLPostsResults(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		sslMonitorings: []monitor.Monitoring{
			{
				ID:     "3",
				Type:   monitor.TypeHTTP,
				Target: "https://127.0.0.1:" + strconv.Itoa(1),
			},
		},
	}

	cfg := config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}
	r := New(client, cfg, log.New(io.Discard, "", 0))
	if err := r.runSSL(context.Background()); err != nil {
		t.Fatalf("runSSL failed: %v", err)
	}

	client.mu.Lock()
	postedSSL := append([]monitor.SSLResultPayload(nil), client.postedSSL...)
	client.mu.Unlock()

	if len(postedSSL) != 1 {
		t.Fatalf("expected one ssl result post, got %d", len(postedSSL))
	}
	if postedSSL[0].MonitoringID != "3" {
		t.Fatalf("unexpected monitoring id: %s", postedSSL[0].MonitoringID)
	}
}

func TestRunDomainExpirationPostsUpResponseAndMetadata(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	expiresAt := checkedAt.Add(60 * 24 * time.Hour)
	registrar := "Example Registrar"
	client := &fakeCoreClient{
		domainMonitorings: []monitor.Monitoring{
			{
				ID:     "domain-1",
				Type:   monitor.TypeDomainExpiration,
				Target: "Example.COM.",
			},
		},
	}

	r := New(client, config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}, log.New(io.Discard, "", 0))
	r.domainLookup = staticDomainLookup{
		result: domainlookup.Result{
			Domain:     "example.com",
			Registered: true,
			ExpiresAt:  &expiresAt,
			Registrar:  &registrar,
			CheckedAt:  checkedAt,
		},
	}

	if err := r.runDomainExpiration(context.Background()); err != nil {
		t.Fatalf("runDomainExpiration failed: %v", err)
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 {
		t.Fatalf("expected one response result, got %d", len(postedResponses))
	}
	if postedResponses[0].Status != monitor.StatusUp {
		t.Fatalf("expected up response, got %s", postedResponses[0].Status)
	}
	if postedResponses[0].HTTPStatusCode != nil {
		t.Fatalf("expected nil http status code")
	}

	postedDomains := client.snapshotPostedDomains()
	if len(postedDomains) != 1 {
		t.Fatalf("expected one domain result, got %d", len(postedDomains))
	}
	if !postedDomains[0].IsValid {
		t.Fatalf("expected valid domain result")
	}
	if postedDomains[0].ExpiresAt == nil || !postedDomains[0].ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected expiration date: %#v", postedDomains[0].ExpiresAt)
	}
	if postedDomains[0].Registrar == nil || *postedDomains[0].Registrar != registrar {
		t.Fatalf("unexpected registrar: %#v", postedDomains[0].Registrar)
	}
	if !postedDomains[0].CheckedAt.Equal(checkedAt) {
		t.Fatalf("unexpected checked_at: %s", postedDomains[0].CheckedAt)
	}
}

func TestRunDomainExpirationExpiringWithinThirtyDaysIsDown(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	expiresAt := checkedAt.Add(30 * 24 * time.Hour)
	client := &fakeCoreClient{
		domainMonitorings: []monitor.Monitoring{
			{
				ID:     "domain-2",
				Type:   monitor.TypeDomainExpiration,
				Target: "example.com",
			},
		},
	}

	r := New(client, config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}, log.New(io.Discard, "", 0))
	r.domainLookup = staticDomainLookup{
		result: domainlookup.Result{
			Registered: true,
			ExpiresAt:  &expiresAt,
			CheckedAt:  checkedAt,
		},
	}

	if err := r.runDomainExpiration(context.Background()); err != nil {
		t.Fatalf("runDomainExpiration failed: %v", err)
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 {
		t.Fatalf("expected one response result, got %d", len(postedResponses))
	}
	if postedResponses[0].Status != monitor.StatusDown {
		t.Fatalf("expected down response, got %s", postedResponses[0].Status)
	}

	postedDomains := client.snapshotPostedDomains()
	if len(postedDomains) != 1 {
		t.Fatalf("expected one domain result, got %d", len(postedDomains))
	}
	if postedDomains[0].IsValid {
		t.Fatalf("expected invalid domain result")
	}
}

func TestRunDomainExpirationTemporaryLookupFailurePostsUnknownOnly(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		domainMonitorings: []monitor.Monitoring{
			{
				ID:     "domain-3",
				Type:   monitor.TypeDomainExpiration,
				Target: "example.com",
			},
		},
	}

	r := New(client, config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}, log.New(io.Discard, "", 0))
	r.domainLookup = staticDomainLookup{
		err: &domainlookup.TemporaryError{Err: errors.New("timeout")},
	}

	if err := r.runDomainExpiration(context.Background()); err != nil {
		t.Fatalf("runDomainExpiration failed: %v", err)
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 {
		t.Fatalf("expected one response result, got %d", len(postedResponses))
	}
	if postedResponses[0].Status != monitor.StatusUnknown {
		t.Fatalf("expected unknown response, got %s", postedResponses[0].Status)
	}
	if postedDomains := client.snapshotPostedDomains(); len(postedDomains) != 0 {
		t.Fatalf("expected no domain result for temporary failure, got %d", len(postedDomains))
	}
}

func TestRunDomainExpirationMaintenancePostsUnknownWithoutLookup(t *testing.T) {
	t.Parallel()

	client := &fakeCoreClient{
		domainMonitorings: []monitor.Monitoring{
			{
				ID:                "domain-maintenance",
				Type:              monitor.TypeDomainExpiration,
				Target:            "example.com",
				MaintenanceActive: true,
			},
		},
	}

	r := New(client, config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}, log.New(io.Discard, "", 0))
	r.domainLookup = staticDomainLookup{
		err: errors.New("lookup should not run"),
	}

	if err := r.runDomainExpiration(context.Background()); err != nil {
		t.Fatalf("runDomainExpiration failed: %v", err)
	}

	postedResponses := client.snapshotPostedResponses()
	if len(postedResponses) != 1 {
		t.Fatalf("expected one response result, got %d", len(postedResponses))
	}
	if postedResponses[0].Status != monitor.StatusUnknown {
		t.Fatalf("expected unknown maintenance response, got %s", postedResponses[0].Status)
	}
	if postedDomains := client.snapshotPostedDomains(); len(postedDomains) != 0 {
		t.Fatalf("expected no domain metadata during maintenance, got %d", len(postedDomains))
	}
}

func TestRunDomainExpirationUsesCoreAPIEndpoints(t *testing.T) {
	t.Parallel()

	responseCh := make(chan monitor.MonitoringResponsePayload, 1)
	domainCh := make(chan monitor.DomainResultPayload, 1)
	checkedAt := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	expiresAt := checkedAt.Add(90 * 24 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-INSTANCE-CODE") != "de-1" {
			http.Error(writer, "missing instance code", http.StatusBadRequest)
			return
		}
		if request.Header.Get("X-API-KEY") != "secret-key" {
			http.Error(writer, "missing api key", http.StatusBadRequest)
			return
		}

		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/internal/monitorings":
			if request.URL.Query().Get("location") != "de-1" || request.URL.Query().Get("type") != "domain_expiration" {
				http.Error(writer, "unexpected query", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`[{"id":"domain-api","type":"domain_expiration","target":"example.com","maintenance_active":false}]`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/internal/monitoring-responses":
			var payload monitor.MonitoringResponsePayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			responseCh <- payload
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/internal/domain-results":
			var payload monitor.DomainResultPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			domainCh <- payload
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	r := New(core.NewClient(server.URL, "secret-key", "de-1"), config.Config{
		WebGuardLocation:    "de-1",
		QueueDefaultWorkers: 1,
	}, log.New(io.Discard, "", 0))
	r.domainLookup = staticDomainLookup{
		result: domainlookup.Result{
			Registered: true,
			ExpiresAt:  &expiresAt,
			CheckedAt:  checkedAt,
		},
	}

	if err := r.runDomainExpiration(context.Background()); err != nil {
		t.Fatalf("runDomainExpiration failed: %v", err)
	}

	select {
	case payload := <-responseCh:
		if payload.MonitoringID != "domain-api" || payload.Status != monitor.StatusUp {
			t.Fatalf("unexpected response payload: %#v", payload)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected monitoring response post")
	}

	select {
	case payload := <-domainCh:
		if payload.MonitoringID != "domain-api" || !payload.IsValid {
			t.Fatalf("unexpected domain payload: %#v", payload)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("expected domain result post")
	}
}

func TestLogFetchErrorIncludesStatusBody(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	r := New(nil, config.Config{}, log.New(&logs, "", 0))

	r.logFetchError(&core.HTTPStatusError{
		StatusCode: http.StatusForbidden,
		Body:       "forbidden",
	})

	if !bytes.Contains(logs.Bytes(), []byte("Failed to fetch monitorings from the Core API.")) {
		t.Fatalf("expected generic fetch error log, got %q", logs.String())
	}
	if !bytes.Contains(logs.Bytes(), []byte("forbidden")) {
		t.Fatalf("expected response body to be logged, got %q", logs.String())
	}
}
