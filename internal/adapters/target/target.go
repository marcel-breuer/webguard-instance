package target

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var blockedAddressRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
}

type EgressPolicy struct {
	AllowPrivate bool
}

func TCPAddress(rawTarget string, port int) (string, error) {
	host, _, err := extractHostPort(rawTarget)
	if err != nil {
		return "", err
	}
	if port <= 0 {
		return "", fmt.Errorf("invalid port %d", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func Host(rawTarget string) (string, error) {
	host, _, err := extractHostPort(rawTarget)
	if err != nil {
		return "", err
	}
	return host, nil
}

func SSLAddressAndServerName(rawTarget string) (string, string, error) {
	host, parsedPort, err := extractHostPort(rawTarget)
	if err != nil {
		return "", "", err
	}
	if parsedPort == "" {
		parsedPort = "443"
	}
	return net.JoinHostPort(host, parsedPort), host, nil
}

// Resolver resolves and validates target hosts. A zero Resolver uses
// net.DefaultResolver.
type Resolver struct {
	LookupNetIP func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// HTTPURL parses and syntactically validates an HTTP(S) monitoring target.
// It does not resolve the host: every connection goes through SafeDialContext,
// which resolves and validates the addresses it actually dials.
func HTTPURL(rawTarget string, policy EgressPolicy) (*url.URL, error) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return nil, fmt.Errorf("target is empty")
	}
	if len(target) > 4096 {
		return nil, fmt.Errorf("target exceeds maximum length")
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported target scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("target host is empty")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("target URL must not contain credentials")
	}
	if _, err := checkHostLiteral(parsed.Hostname(), policy); err != nil {
		return nil, err
	}
	return parsed, nil
}

func ValidateHost(ctx context.Context, host string, policy EgressPolicy) error {
	if policy.AllowPrivate {
		_, err := checkHostLiteral(host, policy)
		return err
	}
	_, err := ResolveHost(ctx, host, policy)
	return err
}

// ResolveHost resolves and validates host with net.DefaultResolver. Callers
// should connect to the returned addresses instead of resolving host again.
func ResolveHost(ctx context.Context, host string, policy EgressPolicy) ([]netip.Addr, error) {
	return Resolver{}.ResolveHost(ctx, host, policy)
}

func SafeDialContext(policy EgressPolicy) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if policy.AllowPrivate {
			return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
		}
		ips, err := ResolveHost(ctx, host, policy)
		if err != nil {
			return nil, err
		}
		return dialFirst(ctx, dialer.DialContext, network, ips, port)
	}
}

func dialFirst(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), network string, ips []netip.Addr, port string) (net.Conn, error) {
	lastErr := fmt.Errorf("no addresses to dial")
	for _, ip := range ips {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func checkHostLiteral(host string, policy EgressPolicy) (string, error) {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", fmt.Errorf("target host is empty")
	}
	if policy.AllowPrivate {
		return host, nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return "", fmt.Errorf("private target host is not allowed: %s", host)
	}
	if addr, err := netip.ParseAddr(host); err == nil && isUnsafeAddress(addr) {
		return "", fmt.Errorf("private target address is not allowed: %s", host)
	}
	return host, nil
}

// ResolveHost resolves host once and validates every resulting address against
// the egress policy. With AllowPrivate the host is still resolved, but no
// address is rejected.
func (r Resolver) ResolveHost(ctx context.Context, host string, policy EgressPolicy) ([]netip.Addr, error) {
	host, err := checkHostLiteral(host, policy)
	if err != nil {
		return nil, err
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}

	lookup := r.LookupNetIP
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	ips, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("target host has no IP addresses: %s", host)
	}
	for _, ip := range ips {
		if !policy.AllowPrivate && isUnsafeAddress(ip) {
			return nil, fmt.Errorf("private target address is not allowed: %s", ip.String())
		}
	}
	return ips, nil
}

func isUnsafeAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified() {
		return true
	}
	for _, network := range blockedAddressRanges {
		if network.Contains(addr) {
			return true
		}
	}
	return false
}

func extractHostPort(rawTarget string) (string, string, error) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return "", "", fmt.Errorf("target is empty")
	}
	if len(target) > 4096 {
		return "", "", fmt.Errorf("target exceeds maximum length")
	}

	hostPort := target
	if strings.Contains(target, "://") {
		parsedURL, err := url.Parse(target)
		if err != nil {
			return "", "", err
		}
		hostPort = parsedURL.Host
	} else {
		parsedURL, err := url.Parse("//" + target)
		if err == nil && parsedURL.Host != "" {
			hostPort = parsedURL.Host
		}
	}

	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return "", "", fmt.Errorf("target host is empty")
	}

	host, port, err := net.SplitHostPort(hostPort)
	if err == nil {
		return host, port, nil
	}

	return hostPort, "", nil
}
