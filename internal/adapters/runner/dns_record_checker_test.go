package runner

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const testDNSTimeout = 5 * time.Second

func staticDNSServers(servers ...string) func() ([]string, error) {
	return func() ([]string, error) {
		return servers, nil
	}
}

func aRecordHandler(t *testing.T, ip string, queries *atomic.Int32) dns.HandlerFunc {
	t.Helper()
	return func(writer dns.ResponseWriter, request *dns.Msg) {
		if queries != nil {
			queries.Add(1)
		}
		response := new(dns.Msg)
		response.SetReply(request)
		record, err := dns.NewRR(request.Question[0].Name + " 60 IN A " + ip)
		if err != nil {
			t.Errorf("build A record: %v", err)
			return
		}
		response.Answer = append(response.Answer, record)
		_ = writer.WriteMsg(response)
	}
}

func startDNSServer(t *testing.T, server *dns.Server) {
	t.Helper()

	started := make(chan struct{})
	server.NotifyStartedFunc = func() { close(started) }
	served := make(chan error, 1)
	go func() {
		served <- server.ActivateAndServe()
	}()
	select {
	case <-started:
	case err := <-served:
		t.Fatalf("DNS server failed to start: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Shutdown()
	})
}

func startUDPDNSServer(t *testing.T, handler dns.Handler) string {
	t.Helper()

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	startDNSServer(t, &dns.Server{PacketConn: packetConn, Handler: handler})
	return packetConn.LocalAddr().String()
}

// startUDPAndTCPDNSServers binds both transports to one address, as a real
// resolver does, so a truncated UDP answer can be retried over TCP.
func startUDPAndTCPDNSServers(t *testing.T, udpHandler, tcpHandler dns.Handler) string {
	t.Helper()

	for attempt := 0; attempt < 10; attempt++ {
		packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen udp: %v", err)
		}
		listener, err := net.Listen("tcp", packetConn.LocalAddr().String())
		if err != nil {
			_ = packetConn.Close()
			continue
		}
		startDNSServer(t, &dns.Server{PacketConn: packetConn, Handler: udpHandler})
		startDNSServer(t, &dns.Server{Listener: listener, Handler: tcpHandler})
		return packetConn.LocalAddr().String()
	}
	t.Fatalf("could not bind UDP and TCP on the same loopback port")
	return ""
}

func closedUDPAddress(t *testing.T) string {
	t.Helper()

	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	address := packetConn.LocalAddr().String()
	if err := packetConn.Close(); err != nil {
		t.Fatalf("close udp: %v", err)
	}
	return address
}

func TestSystemDNSRecordResolverFallsBackToNextServerOnNetworkError(t *testing.T) {
	t.Parallel()

	var queries atomic.Int32
	healthy := startUDPDNSServer(t, aRecordHandler(t, "192.0.2.10", &queries))
	resolver := newSystemDNSRecordResolver(staticDNSServers(closedUDPAddress(t), healthy))

	values, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout)
	if err != nil {
		t.Fatalf("expected fallback server to answer, got %v", err)
	}
	if !slices.Equal(values, []string{"192.0.2.10"}) {
		t.Fatalf("unexpected values: %#v", values)
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("expected one query on fallback server, got %d", got)
	}
}

func TestSystemDNSRecordResolverDoesNotFallBackOnNXDOMAIN(t *testing.T) {
	t.Parallel()

	nxdomain := startUDPDNSServer(t, dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetRcode(request, dns.RcodeNameError)
		_ = writer.WriteMsg(response)
	}))
	var fallbackQueries atomic.Int32
	fallback := startUDPDNSServer(t, aRecordHandler(t, "192.0.2.10", &fallbackQueries))
	resolver := newSystemDNSRecordResolver(staticDNSServers(nxdomain, fallback))

	_, err := resolver.Resolve(context.Background(), "missing.example.com", "A", testDNSTimeout)
	if err == nil || !strings.Contains(err.Error(), "NXDOMAIN") {
		t.Fatalf("expected NXDOMAIN error, got %v", err)
	}
	if got := fallbackQueries.Load(); got != 0 {
		t.Fatalf("expected no fallback query after a valid answer, got %d", got)
	}
}

func TestSystemDNSRecordResolverRetriesTruncatedAnswerOverTCP(t *testing.T) {
	t.Parallel()

	var tcpQueries atomic.Int32
	truncated := dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(request)
		response.Truncated = true
		_ = writer.WriteMsg(response)
	})
	address := startUDPAndTCPDNSServers(t, truncated, aRecordHandler(t, "192.0.2.20", &tcpQueries))
	resolver := newSystemDNSRecordResolver(staticDNSServers(address))

	values, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout)
	if err != nil {
		t.Fatalf("expected TCP retry to succeed, got %v", err)
	}
	if !slices.Equal(values, []string{"192.0.2.20"}) {
		t.Fatalf("unexpected values: %#v", values)
	}
	if got := tcpQueries.Load(); got != 1 {
		t.Fatalf("expected one TCP query, got %d", got)
	}
}

func TestSystemDNSRecordResolverReturnsLastNetworkErrorWhenAllServersFail(t *testing.T) {
	t.Parallel()

	resolver := newSystemDNSRecordResolver(staticDNSServers(closedUDPAddress(t), closedUDPAddress(t)))

	if _, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout); err == nil {
		t.Fatalf("expected error when every server is unreachable")
	}
}

func TestSystemDNSRecordResolverLoadsConfigurationOnce(t *testing.T) {
	t.Parallel()

	address := startUDPDNSServer(t, aRecordHandler(t, "192.0.2.30", nil))
	var mu sync.Mutex
	loads := 0
	resolver := newSystemDNSRecordResolver(func() ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		loads++
		return []string{address}, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if loads != 1 {
		t.Fatalf("expected configuration to load once, got %d", loads)
	}
}

func TestSystemDNSRecordResolverRetriesConfigurationAfterLoadFailure(t *testing.T) {
	t.Parallel()

	address := startUDPDNSServer(t, aRecordHandler(t, "192.0.2.40", nil))
	loadErr := errors.New("resolv.conf unreadable")
	var mu sync.Mutex
	loads := 0
	resolver := newSystemDNSRecordResolver(func() ([]string, error) {
		mu.Lock()
		defer mu.Unlock()
		loads++
		if loads == 1 {
			return nil, loadErr
		}
		return []string{address}, nil
	})

	if _, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout); !errors.Is(err, loadErr) {
		t.Fatalf("expected configuration error, got %v", err)
	}
	values, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout)
	if err != nil {
		t.Fatalf("expected configuration reload to succeed, got %v", err)
	}
	if !slices.Equal(values, []string{"192.0.2.40"}) {
		t.Fatalf("unexpected values: %#v", values)
	}
}

func TestSystemDNSRecordResolverRejectsEmptyConfiguration(t *testing.T) {
	t.Parallel()

	resolver := newSystemDNSRecordResolver(staticDNSServers())
	_, err := resolver.Resolve(context.Background(), "example.com", "A", testDNSTimeout)
	if err == nil || !strings.Contains(err.Error(), "no nameservers") {
		t.Fatalf("expected empty configuration error, got %v", err)
	}
}

func TestSystemDNSRecordResolverRejectsUnsupportedRecordType(t *testing.T) {
	t.Parallel()

	resolver := newSystemDNSRecordResolver(func() ([]string, error) {
		t.Fatalf("configuration must not load for unsupported record types")
		return nil, nil
	})
	_, err := resolver.Resolve(context.Background(), "example.com", "NOPE", testDNSTimeout)
	if err == nil || !strings.Contains(err.Error(), "unsupported DNS record type") {
		t.Fatalf("expected unsupported type error, got %v", err)
	}
}

func TestLoadDNSServersReadsAllNameservers(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "resolv.conf")
	content := "nameserver 192.0.2.1\nnameserver 2001:db8::1\noptions timeout:1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write resolv.conf: %v", err)
	}

	servers, err := loadDNSServers(path)
	if err != nil {
		t.Fatalf("load servers: %v", err)
	}
	expected := []string{"192.0.2.1:53", "[2001:db8::1]:53"}
	if !slices.Equal(servers, expected) {
		t.Fatalf("expected %#v, got %#v", expected, servers)
	}
}

func TestLoadDNSServersReportsMissingFile(t *testing.T) {
	t.Parallel()

	if _, err := loadDNSServers(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected error for missing resolv.conf")
	}
}
