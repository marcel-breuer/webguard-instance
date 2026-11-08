package target

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"testing"
)

func TestHTTPURLValidatesWithoutResolving(t *testing.T) {
	t.Parallel()

	// .invalid never resolves, so success proves HTTPURL performs no lookup.
	if _, err := HTTPURL("https://unresolvable.invalid/", EgressPolicy{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, rawTarget := range []string{"http://localhost:8080/", "http://app.localhost/", "http://127.0.0.1/", "http://[::1]/", "http://10.0.0.1/"} {
		if _, err := HTTPURL(rawTarget, EgressPolicy{}); err == nil {
			t.Fatalf("expected %s to be rejected", rawTarget)
		}
		if _, err := HTTPURL(rawTarget, EgressPolicy{AllowPrivate: true}); err != nil {
			t.Fatalf("expected %s to be allowed with private targets enabled: %v", rawTarget, err)
		}
	}
}

func staticResolver(addresses ...string) (Resolver, *int) {
	calls := 0
	return Resolver{LookupNetIP: func(context.Context, string, string) ([]netip.Addr, error) {
		calls++
		result := make([]netip.Addr, 0, len(addresses))
		for _, address := range addresses {
			result = append(result, netip.MustParseAddr(address))
		}
		return result, nil
	}}, &calls
}

func TestResolverRejectsHostResolvingToPrivateAddress(t *testing.T) {
	t.Parallel()

	resolver, _ := staticResolver("93.184.216.34", "192.168.1.10")
	if _, err := resolver.ResolveHost(context.Background(), "rebind.example.com", EgressPolicy{}); err == nil {
		t.Fatal("expected private resolution to be rejected")
	}
}

func TestResolverStillResolvesWhenPrivateTargetsAllowed(t *testing.T) {
	t.Parallel()

	resolver, calls := staticResolver("192.168.1.10")
	addresses, err := resolver.ResolveHost(context.Background(), "internal.example.com", EgressPolicy{AllowPrivate: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *calls != 1 || len(addresses) != 1 || addresses[0] != netip.MustParseAddr("192.168.1.10") {
		t.Fatalf("expected resolved private address, got %v (calls=%d)", addresses, *calls)
	}
}

func TestResolverReturnsLiteralWithoutLookup(t *testing.T) {
	t.Parallel()

	resolver, calls := staticResolver()
	addresses, err := resolver.ResolveHost(context.Background(), "[2001:4860:4860::8888]", EgressPolicy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *calls != 0 || len(addresses) != 1 || addresses[0] != netip.MustParseAddr("2001:4860:4860::8888") {
		t.Fatalf("expected literal address without lookup, got %v (calls=%d)", addresses, *calls)
	}
}

func TestDialFirstFallsBackToNextAddress(t *testing.T) {
	t.Parallel()

	var dialed []string
	server, client := net.Pipe()
	defer server.Close()
	dial := func(_ context.Context, _ string, address string) (net.Conn, error) {
		dialed = append(dialed, address)
		if len(dialed) == 1 {
			return nil, errors.New("connection refused")
		}
		return client, nil
	}

	ips := []netip.Addr{netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("2001:db8::1")}
	conn, err := dialFirst(context.Background(), dial, "tcp", ips, "443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer conn.Close()
	if conn != client {
		t.Fatal("expected connection from second address")
	}
	if !slices.Equal(dialed, []string{"203.0.113.1:443", "[2001:db8::1]:443"}) {
		t.Fatalf("unexpected dial order: %v", dialed)
	}
}

func TestDialFirstReturnsLastErrorAndRespectsContext(t *testing.T) {
	t.Parallel()

	lastErr := errors.New("second failed")
	attempts := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("first failed")
		}
		return nil, lastErr
	}
	ips := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("192.0.2.2")}
	if _, err := dialFirst(context.Background(), dial, "tcp", ips, "80"); !errors.Is(err, lastErr) {
		t.Fatalf("expected last dial error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts = 0
	if _, err := dialFirst(ctx, dial, "tcp", ips, "80"); !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("expected canceled context to stop dialing, got err=%v attempts=%d", err, attempts)
	}
}

func TestSafeDialContextRejectsPrivateAddress(t *testing.T) {
	t.Parallel()

	dial := SafeDialContext(EgressPolicy{})
	for _, address := range []string{"localhost:80", "10.0.0.1:80", "[::1]:80"} {
		if _, err := dial(context.Background(), "tcp", address); err == nil {
			t.Fatalf("expected dial to %s to be rejected", address)
		}
	}
}
