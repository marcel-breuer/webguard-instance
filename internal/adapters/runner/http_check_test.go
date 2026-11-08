package runner

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/adapters/target"
	"github.com/marcel-breuer/webguard-instance/internal/config"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

func TestPerformHTTPRequestBoundsTotalDurationAcrossVariantsAndRetries(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	r := newTestService(nil, config.Config{AllowPrivateTargets: true}, log.New(io.Discard, "", 0))
	start := time.Now()
	_, _, _, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target:     server.URL,
		Timeout:    1,
		HTTPMethod: monitor.HTTPMethodGet,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error for an unresponsive target")
	}
	// Two URL variants with one retry each would take more than 4s with a
	// per-request timeout; the check deadline must cap the whole check at ~1s.
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("expected the check to stop at its 1s deadline, took %s", elapsed)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected no further attempts after the deadline, got %d requests", got)
	}
}

func TestPerformHTTPRequestReportsDurationOfReturnedRequestOnly(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack failed: %v", err)
				return
			}
			_ = connection.Close()
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := newTestService(nil, config.Config{AllowPrivateTargets: true}, log.New(io.Discard, "", 0))
	statusCode, _, duration, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target:     server.URL,
		Timeout:    5,
		HTTPMethod: monitor.HTTPMethodGet,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if statusCode != http.StatusOK || requests.Load() != 2 {
		t.Fatalf("expected retry to succeed, got status=%d requests=%d", statusCode, requests.Load())
	}
	if duration <= 0 || duration >= fixedHTTPRetryDelay {
		t.Fatalf("expected duration of the successful request only (excluding retry delay), got %s", duration)
	}
}

func TestPerformHTTPRequestDisablesKeepAlive(t *testing.T) {
	t.Parallel()

	var closeRequested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		closeRequested.Store(request.Close)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := newTestService(nil, config.Config{AllowPrivateTargets: true}, log.New(io.Discard, "", 0))
	if _, _, _, err := r.performHTTPRequest(context.Background(), monitor.Monitoring{
		Target: server.URL, Timeout: 2, HTTPMethod: monitor.HTTPMethodGet,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !closeRequested.Load() {
		t.Fatal("expected the check to request connection close")
	}
}

func TestObserveHTTPMonitoringUsesCheckerDuration(t *testing.T) {
	t.Parallel()

	r := newTestService(nil, config.Config{AllowPrivateTargets: true}, log.New(io.Discard, "", 0))
	r.httpChecker = HTTPCheckFunc(func(context.Context, monitor.Monitoring) (int, string, time.Duration, error) {
		return http.StatusOK, "needle", 1500 * time.Millisecond, nil
	})

	_, responseTime, _, observation := r.observeHTTPMonitoring(context.Background(), monitor.Monitoring{Type: monitor.TypeHTTP})
	if responseTime == nil || *responseTime != 1500 || observation.ResponseTime == nil || *observation.ResponseTime != 1500 {
		t.Fatalf("expected response time 1500ms, got %v", responseTime)
	}
	_, responseTime, _, _ = r.observeKeywordMonitoring(context.Background(), monitor.Monitoring{Type: monitor.TypeKeyword, Keyword: "needle"})
	if responseTime == nil || *responseTime != 1500 {
		t.Fatalf("expected keyword response time 1500ms, got %v", responseTime)
	}
}

func TestHTTPCheckClientRejectsRedirectToPrivateHostViaDialer(t *testing.T) {
	t.Parallel()

	var privateHits atomic.Int32
	privateServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		privateHits.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer privateServer.Close()

	parsed, err := url.Parse(privateServer.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	for _, location := range []string{privateServer.URL, "http://localhost:" + parsed.Port()} {
		t.Run(location, func(t *testing.T) {
			redirectServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, location, http.StatusFound)
			}))
			defer redirectServer.Close()

			client, transport := newHTTPCheckClient(target.EgressPolicy{}, 2*time.Second)
			defer transport.CloseIdleConnections()
			safeDial := transport.DialContext
			// Route the public-looking entry host to the local redirect server;
			// every other dial, including the redirect hop, uses the safe dialer.
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == "public.example.com:80" {
					return (&net.Dialer{}).DialContext(ctx, network, redirectServer.Listener.Addr().String())
				}
				return safeDial(ctx, network, address)
			}

			response, err := client.Get("http://public.example.com/")
			if err == nil {
				_ = response.Body.Close()
				t.Fatalf("expected redirect to private host to fail, got status %d", response.StatusCode)
			}
		})
	}
	if got := privateHits.Load(); got != 0 {
		t.Fatalf("private server must not be reached, got %d hits", got)
	}
}
