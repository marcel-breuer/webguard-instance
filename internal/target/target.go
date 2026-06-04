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

func HTTPURL(ctx context.Context, rawTarget string, policy EgressPolicy) (*url.URL, error) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return nil, fmt.Errorf("target is empty")
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
	if err := ValidateHost(ctx, parsed.Hostname(), policy); err != nil {
		return nil, err
	}
	return parsed, nil
}

func ValidateHost(ctx context.Context, host string, policy EgressPolicy) error {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return fmt.Errorf("target host is empty")
	}
	if policy.AllowPrivate {
		return nil
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("private target host is not allowed: %s", host)
	}

	_, err := resolveHostIPs(ctx, host, policy)
	return err
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
		ips, err := resolveHostIPs(ctx, host, policy)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
}

func resolveHostIPs(ctx context.Context, host string, policy EgressPolicy) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		if !policy.AllowPrivate && isUnsafeAddress(addr) {
			return nil, fmt.Errorf("private target address is not allowed: %s", host)
		}
		return []netip.Addr{addr}, nil
	}

	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
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
	return addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() ||
		addr.IsUnspecified()
}

func extractHostPort(rawTarget string) (string, string, error) {
	target := strings.TrimSpace(rawTarget)
	if target == "" {
		return "", "", fmt.Errorf("target is empty")
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
