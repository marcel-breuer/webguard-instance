package runner

import (
	"context"
	"net"
	"net/netip"
	"testing"

	"github.com/marcel-breuer/webguard-instance/internal/adapters/target"
	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
)

// stubPingResolver replaces the package-level ping resolver. Tests using it
// must not run in parallel.
func stubPingResolver(t *testing.T, hosts map[string][]netip.Addr) {
	t.Helper()
	previous := resolvePingHost
	resolver := target.Resolver{LookupNetIP: func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
		if addresses, ok := hosts[host]; ok {
			return addresses, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}}
	resolvePingHost = resolver.ResolveHost
	t.Cleanup(func() { resolvePingHost = previous })
}

func TestObservePingMonitoringPingsResolvedAddress(t *testing.T) {
	stubPingResolver(t, map[string][]netip.Addr{
		"v6.example.com": {netip.MustParseAddr("2001:4860:4860::8888"), netip.MustParseAddr("8.8.8.8")},
	})

	var receivedHost string
	executor := PingCommandExecutor(func(_ context.Context, host string, _ int) ([]byte, error) {
		receivedHost = host
		return []byte("time=3 ms"), nil
	})

	status, _, _ := observePingMonitoring(context.Background(), monitor.Monitoring{Target: "v6.example.com"}, target.EgressPolicy{}, executor)
	if status != monitor.StatusUp {
		t.Fatalf("expected up, got %s", status)
	}
	if receivedHost != "2001:4860:4860::8888" {
		t.Fatalf("expected ping to receive the validated IP literal, got %q", receivedHost)
	}
	_, args := buildPingCommand(receivedHost, 1)
	if args[len(args)-2] != "-6" {
		t.Fatalf("expected IPv6 ping flag, got %#v", args)
	}
}

func TestObservePingMonitoringRejectsHostnameResolvingToPrivateAddress(t *testing.T) {
	stubPingResolver(t, map[string][]netip.Addr{
		"rebind.example.com": {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.7")},
	})

	executor := PingCommandExecutor(func(context.Context, string, int) ([]byte, error) {
		t.Fatal("ping must not run for a host resolving to a private address")
		return nil, nil
	})

	status, responseTime, observation := observePingMonitoring(context.Background(), monitor.Monitoring{Target: "rebind.example.com"}, target.EgressPolicy{}, executor)
	if status != monitor.StatusDown || responseTime != nil {
		t.Fatalf("expected down without response time, got %s %v", status, responseTime)
	}
	if observation.TransportError == nil || *observation.TransportError != "target_rejected" {
		t.Fatalf("expected target_rejected, got %#v", observation.TransportError)
	}
}
