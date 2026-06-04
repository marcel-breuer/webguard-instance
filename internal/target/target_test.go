package target

import (
	"context"
	"testing"
)

func TestTCPAddress(t *testing.T) {
	t.Parallel()

	address, err := TCPAddress("https://example.com:8443/path", 443)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if address != "example.com:443" {
		t.Fatalf("expected example.com:443, got %q", address)
	}
}

func TestTCPAddressInvalidPort(t *testing.T) {
	t.Parallel()

	_, err := TCPAddress("example.com", 0)
	if err == nil {
		t.Fatalf("expected error for invalid port")
	}
}

func TestHostParsesRawIPv4(t *testing.T) {
	t.Parallel()

	host, err := Host("8.8.8.8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "8.8.8.8" {
		t.Fatalf("expected 8.8.8.8, got %q", host)
	}
}

func TestHostParsesRawIPv6(t *testing.T) {
	t.Parallel()

	host, err := Host("2001:4860:4860::8888")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if host != "2001:4860:4860::8888" {
		t.Fatalf("expected 2001:4860:4860::8888, got %q", host)
	}
}

func TestSSLAddressAndServerNameDefaultsTo443(t *testing.T) {
	t.Parallel()

	address, serverName, err := SSLAddressAndServerName("https://example.com/health")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if address != "example.com:443" {
		t.Fatalf("expected example.com:443, got %q", address)
	}
	if serverName != "example.com" {
		t.Fatalf("expected server name example.com, got %q", serverName)
	}
}

func TestSSLAddressAndServerNameKeepsExplicitPort(t *testing.T) {
	t.Parallel()

	address, serverName, err := SSLAddressAndServerName("example.com:9443")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if address != "example.com:9443" {
		t.Fatalf("expected example.com:9443, got %q", address)
	}
	if serverName != "example.com" {
		t.Fatalf("expected server name example.com, got %q", serverName)
	}
}

func TestSSLAddressAndServerNameEmptyTarget(t *testing.T) {
	t.Parallel()

	_, _, err := SSLAddressAndServerName("   ")
	if err == nil {
		t.Fatalf("expected error for empty target")
	}
}

func TestValidateHostRejectsPrivateAddressesByDefault(t *testing.T) {
	t.Parallel()

	testCases := []string{
		"127.0.0.1",
		"::1",
		"10.0.0.1",
		"172.16.0.1",
		"192.168.0.1",
		"169.254.169.254",
		"localhost",
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase, func(t *testing.T) {
			t.Parallel()

			if err := ValidateHost(context.Background(), testCase, EgressPolicy{}); err == nil {
				t.Fatalf("expected %s to be rejected", testCase)
			}
		})
	}
}

func TestValidateHostAllowsPrivateAddressesWhenConfigured(t *testing.T) {
	t.Parallel()

	if err := ValidateHost(context.Background(), "127.0.0.1", EgressPolicy{AllowPrivate: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPURLRejectsUnsupportedSchemes(t *testing.T) {
	t.Parallel()

	_, err := HTTPURL(context.Background(), "file:///etc/passwd", EgressPolicy{AllowPrivate: true})
	if err == nil {
		t.Fatalf("expected unsupported scheme error")
	}
}
