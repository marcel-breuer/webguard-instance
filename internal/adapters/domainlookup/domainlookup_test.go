package domainlookup

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeTarget(t *testing.T) {
	t.Parallel()

	got := NormalizeTarget(" Example.COM. ")
	if got != "example.com" {
		t.Fatalf("expected normalized domain example.com, got %q", got)
	}
}

func TestLookupCandidatesIncludeParentDomains(t *testing.T) {
	t.Parallel()

	got := lookupCandidates("subdomain.example.com")
	expected := []string{"subdomain.example.com", "example.com"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("unexpected candidates: got %#v want %#v", got, expected)
	}
}

func TestValidDomainNameRejectsExcessiveLabels(t *testing.T) {
	t.Parallel()

	labels := make([]string, maxDomainLabels+1)
	for i := range labels {
		labels[i] = "a"
	}
	if validDomainName(strings.Join(labels, ".")) {
		t.Fatalf("expected excessive label count to be rejected")
	}
}

func TestValidDomainNameRejectsMalformedLabels(t *testing.T) {
	t.Parallel()

	testCases := []string{
		"-example.com",
		"example-.com",
		"exa_mple.com",
		strings.Repeat("a", maxDomainLabelLength+1) + ".com",
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase, func(t *testing.T) {
			t.Parallel()

			if validDomainName(testCase) {
				t.Fatalf("expected %q to be rejected", testCase)
			}
		})
	}
}

func TestParseWHOISExpirationFormats(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		whois    string
		expected time.Time
	}{
		{
			name:     "registry expiry rfc3339",
			whois:    "Registry Expiry Date: 2026-07-23T12:00:00Z\nRegistrar: Example Registrar",
			expected: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		},
		{
			name:     "registrar registration expiration with timezone",
			whois:    "Registrar Registration Expiration Date: 2026-07-23 12:00:00 UTC",
			expected: time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		},
		{
			name:     "expiration date day month",
			whois:    "Expiration Date: 23-Jul-2026",
			expected: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "paid till dotted",
			whois:    "paid-till: 2026.07.23",
			expected: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "expiry date iso day",
			whois:    "Expiry Date: 2026-07-23",
			expected: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC),
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			expiresAt, registrar := ParseWHOIS(testCase.whois)
			if expiresAt == nil {
				t.Fatalf("expected expiration date")
			}
			if !expiresAt.Equal(testCase.expected) {
				t.Fatalf("expected expiration %s, got %s", testCase.expected, *expiresAt)
			}
			if testCase.name == "registry expiry rfc3339" {
				if registrar == nil || *registrar != "Example Registrar" {
					t.Fatalf("expected registrar Example Registrar, got %#v", registrar)
				}
			}
		})
	}
}

func TestParseWHOISUsesEarliestExpirationDate(t *testing.T) {
	t.Parallel()

	expiresAt, _ := ParseWHOIS(`
Registry Expiry Date: 2026-07-23T12:00:00Z
Expiry Date: 2026-06-23
`)
	if expiresAt == nil {
		t.Fatalf("expected expiration date")
	}
	expected := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	if !expiresAt.Equal(expected) {
		t.Fatalf("expected earliest expiration %s, got %s", expected, *expiresAt)
	}
}
