package domainlookup

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const ianaWHOISAddress = "whois.iana.org:43"

type rdapReply struct {
	status int
	body   string
}

type fakeRDAP struct {
	t       *testing.T
	mu      sync.Mutex
	replies map[string]rdapReply
	paths   []string
}

func (f *fakeRDAP) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	domain := strings.TrimPrefix(request.URL.Path, "/domain/")
	f.mu.Lock()
	f.paths = append(f.paths, domain)
	reply, ok := f.replies[domain]
	f.mu.Unlock()
	if accept := request.Header.Get("Accept"); !strings.Contains(accept, "application/rdap+json") {
		f.t.Errorf("unexpected Accept header %q", accept)
	}
	if !ok {
		reply = rdapReply{status: http.StatusNotFound}
	}
	writer.WriteHeader(reply.status)
	_, _ = writer.Write([]byte(reply.body))
}

func (f *fakeRDAP) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths)
}

type fakeWHOIS struct {
	mu        sync.Mutex
	responses map[string]string
	queries   []string
}

func (f *fakeWHOIS) dial(_ context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("unexpected network " + network)
	}
	f.mu.Lock()
	response, ok := f.responses[address]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("connection refused: " + address)
	}

	client, server := net.Pipe()
	go func() {
		defer server.Close()
		line, err := bufio.NewReader(server).ReadString('\n')
		if err != nil {
			return
		}
		f.mu.Lock()
		f.queries = append(f.queries, address+" "+strings.TrimSpace(line))
		f.mu.Unlock()
		_, _ = server.Write([]byte(response))
	}()
	return client, nil
}

func (f *fakeWHOIS) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.queries)
}

func newTestLookup(t *testing.T, rdap map[string]rdapReply, whois map[string]string) (*Lookup, *fakeRDAP, *fakeWHOIS) {
	t.Helper()

	rdapServer := &fakeRDAP{t: t, replies: rdap}
	server := httptest.NewServer(rdapServer)
	t.Cleanup(server.Close)

	whoisServer := &fakeWHOIS{responses: whois}
	lookup := New(time.Second)
	lookup.httpClient = server.Client()
	lookup.rdapBaseURL = server.URL + "/domain/"
	lookup.dialContext = whoisServer.dial
	return lookup, rdapServer, whoisServer
}

const registeredRDAP = `{
  "events": [
    {"eventAction": "registration", "eventDate": "2020-01-01T00:00:00Z"},
    {"eventAction": "expiration", "eventDate": "2027-03-04T05:06:07Z"}
  ],
  "entities": [
    {"roles": ["technical"], "vcardArray": ["vcard", [["fn", {}, "text", "Tech Contact"]]]},
    {"roles": ["Registrar"], "vcardArray": ["vcard", [["version", {}, "text", "4.0"], ["fn", {}, "text", " Example Registrar "]]]}
  ]
}`

func assertNotRegistered(t *testing.T, result Result, domain string) {
	t.Helper()
	if result.Registered || result.ExpiresAt != nil || result.Registrar != nil {
		t.Fatalf("expected unregistered result, got %#v", result)
	}
	if result.Domain != domain {
		t.Fatalf("expected domain %q, got %q", domain, result.Domain)
	}
	if result.CheckedAt.IsZero() {
		t.Fatalf("expected CheckedAt to be set")
	}
}

func TestLookupUsesRDAPExpirationAndRegistrar(t *testing.T) {
	t.Parallel()

	lookup, _, whois := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: registeredRDAP},
	}, nil)

	result, err := lookup.Lookup(context.Background(), " Example.COM. ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Registered || result.Domain != "example.com" {
		t.Fatalf("expected registered example.com, got %#v", result)
	}
	expected := time.Date(2027, 3, 4, 5, 6, 7, 0, time.UTC)
	if result.ExpiresAt == nil || !result.ExpiresAt.Equal(expected) {
		t.Fatalf("expected expiration %s, got %v", expected, result.ExpiresAt)
	}
	if result.Registrar == nil || *result.Registrar != "Example Registrar" {
		t.Fatalf("expected registrar Example Registrar, got %v", result.Registrar)
	}
	if queries := whois.recorded(); len(queries) != 0 {
		t.Fatalf("expected no WHOIS queries, got %v", queries)
	}
}

func TestLookupTreatsRDAPNotFoundAsUnavailable(t *testing.T) {
	t.Parallel()

	lookup, rdap, whois := newTestLookup(t, nil, nil)

	result, err := lookup.Lookup(context.Background(), "available.com")
	if err != nil {
		t.Fatalf("expected no error for unavailable domain, got %v", err)
	}
	assertNotRegistered(t, result, "available.com")
	if paths := rdap.requested(); !slices.Equal(paths, []string{"available.com"}) {
		t.Fatalf("unexpected RDAP requests: %v", paths)
	}
	if queries := whois.recorded(); len(queries) != 0 {
		t.Fatalf("expected no WHOIS fallback after RDAP 404, got %v", queries)
	}
}

func TestLookupReturnsTemporaryErrorWhenRDAPAndWHOISAreUnavailable(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()

			lookup, _, _ := newTestLookup(t, map[string]rdapReply{
				"example.com": {status: status},
			}, nil)

			result, err := lookup.Lookup(context.Background(), "example.com")
			if !IsTemporary(err) {
				t.Fatalf("expected temporary error, got %v", err)
			}
			if !strings.Contains(err.Error(), "rdap returned status") {
				t.Fatalf("expected RDAP status in error, got %q", err.Error())
			}
			assertNotRegistered(t, result, "example.com")
		})
	}
}

func TestLookupFallsBackToWHOISWhenRDAPIsTemporarilyUnavailable(t *testing.T) {
	t.Parallel()

	lookup, _, whois := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusServiceUnavailable},
	}, map[string]string{
		ianaWHOISAddress:            "refer: whois.verisign-grs.com\n",
		"whois.verisign-grs.com:43": "Registry Expiry Date: 2027-01-02T03:04:05Z\nRegistrar: WHOIS Registrar\n",
	})

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Registered {
		t.Fatalf("expected registered result, got %#v", result)
	}
	if result.Registrar == nil || *result.Registrar != "WHOIS Registrar" {
		t.Fatalf("expected WHOIS registrar, got %v", result.Registrar)
	}
	expected := []string{ianaWHOISAddress + " com", "whois.verisign-grs.com:43 example.com"}
	if queries := whois.recorded(); !slices.Equal(queries, expected) {
		t.Fatalf("expected WHOIS queries %v, got %v", expected, queries)
	}
}

func TestLookupFallsBackToWHOISOnMalformedRDAPJSON(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: `{"events": [`},
	}, map[string]string{
		ianaWHOISAddress:   "refer: whois.example\n",
		"whois.example:43": "Expiry Date: 2027-05-06\n",
	})

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := time.Date(2027, 5, 6, 0, 0, 0, 0, time.UTC)
	if !result.Registered || result.ExpiresAt == nil || !result.ExpiresAt.Equal(expected) {
		t.Fatalf("expected WHOIS expiration %s, got %#v", expected, result)
	}
}

func TestLookupMalformedRDAPJSONWithoutWHOISIsNotTemporary(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: `not-json`},
	}, map[string]string{
		ianaWHOISAddress: "% no referral for this TLD\n",
	})

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("expected malformed RDAP without WHOIS server to be non-fatal, got %v", err)
	}
	assertNotRegistered(t, result, "example.com")
}

func TestLookupKeepsRDAPRegistrarWhenWHOISOmitsIt(t *testing.T) {
	t.Parallel()

	rdapWithoutExpiration := `{"entities": [{"roles": ["registrar"], "vcardArray": ["vcard", [["fn", {}, "text", "RDAP Registrar"]]]}]}`
	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: rdapWithoutExpiration},
	}, map[string]string{
		ianaWHOISAddress:   "refer: whois.example\n",
		"whois.example:43": "paid-till: 2027.08.09\n",
	})

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExpiresAt == nil || !result.ExpiresAt.Equal(time.Date(2027, 8, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected WHOIS expiration, got %v", result.ExpiresAt)
	}
	if result.Registrar == nil || *result.Registrar != "RDAP Registrar" {
		t.Fatalf("expected RDAP registrar, got %v", result.Registrar)
	}
}

func TestLookupKeepsRegisteredRDAPResultWhenWHOISReportsNoMatch(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: `{}`},
	}, map[string]string{
		ianaWHOISAddress:   "refer: whois.example\n",
		"whois.example:43": "No match for \"EXAMPLE.COM\".\n",
	})

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Registered || result.ExpiresAt != nil {
		t.Fatalf("expected registered RDAP result without expiration, got %#v", result)
	}
}

func TestLookupKeepsRegisteredRDAPResultWhenWHOISFails(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: `{}`},
	}, nil)

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("expected registered RDAP result despite WHOIS failure, got %v", err)
	}
	if !result.Registered {
		t.Fatalf("expected registered result, got %#v", result)
	}
}

func TestLookupReportsWHOISUnavailableDomain(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusBadRequest},
	}, map[string]string{
		ianaWHOISAddress:   "refer: whois.example\n",
		"whois.example:43": "Status: free\n",
	})

	result, err := lookup.Lookup(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertNotRegistered(t, result, "example.com")
}

func TestLookupReturnsTemporaryErrorWhenRDAPIsTemporaryAndWHOISIsUnavailable(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusBadGateway},
	}, map[string]string{
		ianaWHOISAddress:   "refer: whois.example\n",
		"whois.example:43": "Domain not found.\n",
	})

	_, err := lookup.Lookup(context.Background(), "example.com")
	if !IsTemporary(err) {
		t.Fatalf("expected temporary error when RDAP failed and WHOIS reports no match, got %v", err)
	}
}

func TestLookupReturnsTemporaryErrorWhenWHOISServerFails(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusBadRequest},
	}, map[string]string{
		ianaWHOISAddress: "refer: whois.unreachable\n",
	})

	_, err := lookup.Lookup(context.Background(), "example.com")
	if !IsTemporary(err) {
		t.Fatalf("expected temporary error from unreachable WHOIS server, got %v", err)
	}
}

func TestLookupFallsBackToParentDomainCandidate(t *testing.T) {
	t.Parallel()

	lookup, rdap, _ := newTestLookup(t, map[string]rdapReply{
		"example.com": {status: http.StatusOK, body: registeredRDAP},
	}, nil)

	result, err := lookup.Lookup(context.Background(), "www.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Registered || result.Domain != "example.com" {
		t.Fatalf("expected registered parent domain, got %#v", result)
	}
	if paths := rdap.requested(); !slices.Equal(paths, []string{"www.example.com", "example.com"}) {
		t.Fatalf("unexpected RDAP requests: %v", paths)
	}
}

func TestLookupRejectsInvalidTargetsWithoutNetworkCalls(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"", "   ", "https://example.com/path", "exa mple.com", "-bad.com"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			lookup, rdap, whois := newTestLookup(t, nil, nil)
			result, err := lookup.Lookup(context.Background(), target)
			if err != nil {
				t.Fatalf("expected no error for invalid target, got %v", err)
			}
			if result.Registered {
				t.Fatalf("expected unregistered result, got %#v", result)
			}
			if paths := rdap.requested(); len(paths) != 0 {
				t.Fatalf("expected no RDAP requests, got %v", paths)
			}
			if queries := whois.recorded(); len(queries) != 0 {
				t.Fatalf("expected no WHOIS queries, got %v", queries)
			}
		})
	}
}

func TestLookupTreatsRDAPTransportFailureAsTemporary(t *testing.T) {
	t.Parallel()

	lookup, _, _ := newTestLookup(t, nil, nil)
	lookup.rdapBaseURL = "http://127.0.0.1:0/domain/"

	_, err := lookup.Lookup(context.Background(), "example.com")
	if !IsTemporary(err) {
		t.Fatalf("expected temporary error for RDAP transport failure, got %v", err)
	}
}

func TestLookupSingleLabelDomainSkipsWHOIS(t *testing.T) {
	t.Parallel()

	lookup, _, whois := newTestLookup(t, map[string]rdapReply{
		"localhost": {status: http.StatusOK, body: `{}`},
	}, nil)

	result, err := lookup.Lookup(context.Background(), "localhost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Registered {
		t.Fatalf("expected registered RDAP result, got %#v", result)
	}
	if queries := whois.recorded(); len(queries) != 0 {
		t.Fatalf("expected no WHOIS queries without TLD, got %v", queries)
	}
}

func TestNewAppliesDefaultTimeout(t *testing.T) {
	t.Parallel()

	lookup := New(0)
	if lookup.httpClient.Timeout != 10*time.Second || lookup.dialer.Timeout != 10*time.Second {
		t.Fatalf("expected default 10s timeouts, got http=%s dial=%s", lookup.httpClient.Timeout, lookup.dialer.Timeout)
	}
	if lookup.rdapBaseURL != defaultRDAPBaseURL {
		t.Fatalf("expected default RDAP base URL, got %q", lookup.rdapBaseURL)
	}
}

func TestTemporaryError(t *testing.T) {
	t.Parallel()

	cause := errors.New("upstream down")
	err := error(&TemporaryError{Err: cause})
	if !IsTemporary(err) || !errors.Is(err, cause) || err.Error() != "upstream down" {
		t.Fatalf("unexpected temporary error behavior: %v", err)
	}
	if got := (&TemporaryError{}).Error(); got != "temporary domain lookup failure" {
		t.Fatalf("unexpected default message %q", got)
	}
	if IsTemporary(cause) || IsTemporary(nil) {
		t.Fatalf("plain errors must not be temporary")
	}
}

func TestParseRDAPRegistrarIgnoresMalformedVCards(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		entities []rdapEntity
	}{
		{name: "no registrar role", entities: []rdapEntity{{Roles: []string{"abuse"}, VCardArray: []any{"vcard", []any{[]any{"fn", map[string]any{}, "text", "Abuse"}}}}}},
		{name: "short vcard", entities: []rdapEntity{{Roles: []string{"registrar"}, VCardArray: []any{"vcard"}}}},
		{name: "properties not a list", entities: []rdapEntity{{Roles: []string{"registrar"}, VCardArray: []any{"vcard", "fn"}}}},
		{name: "short property", entities: []rdapEntity{{Roles: []string{"registrar"}, VCardArray: []any{"vcard", []any{[]any{"fn", "text"}}}}}},
		{name: "non-string name", entities: []rdapEntity{{Roles: []string{"registrar"}, VCardArray: []any{"vcard", []any{[]any{1, map[string]any{}, "text", "X"}}}}}},
		{name: "non-string value", entities: []rdapEntity{{Roles: []string{"registrar"}, VCardArray: []any{"vcard", []any{[]any{"fn", map[string]any{}, "text", 7}}}}}},
		{name: "blank value", entities: []rdapEntity{{Roles: []string{"registrar"}, VCardArray: []any{"vcard", []any{[]any{"fn", map[string]any{}, "text", "  "}}}}}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if registrar := parseRDAPRegistrar(testCase.entities); registrar != nil {
				t.Fatalf("expected no registrar, got %q", *registrar)
			}
		})
	}
}

func TestParseRDAPExpirationSkipsUnparseableDates(t *testing.T) {
	t.Parallel()

	expiresAt := parseRDAPExpiration([]rdapEvent{
		{EventAction: "expiration", EventDate: "soon"},
		{EventAction: " Registration Expiration ", EventDate: "2027-09-10"},
	})
	if expiresAt == nil || !expiresAt.Equal(time.Date(2027, 9, 10, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected fallback expiration event, got %v", expiresAt)
	}
	if parseRDAPExpiration([]rdapEvent{{EventAction: "last changed", EventDate: "2027-09-10"}}) != nil {
		t.Fatalf("expected non-expiration events to be ignored")
	}
}

func TestParseWHOISRegistrarAndCleanedDates(t *testing.T) {
	t.Parallel()

	expiresAt, registrar := ParseWHOIS(`
Sponsoring Registrar: First Registrar
Registrar: Second Registrar
Expiration Date: 2027-10-11 (YYYY-MM-DD).
Expiry Date: not a date
`)
	if registrar == nil || *registrar != "First Registrar" {
		t.Fatalf("expected first registrar, got %v", registrar)
	}
	if expiresAt == nil || !expiresAt.Equal(time.Date(2027, 10, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected cleaned expiration date, got %v", expiresAt)
	}

	expiresAt, registrar = ParseWHOIS("Domain Name: EXAMPLE.COM\nRegistrar:   \n")
	if expiresAt != nil || registrar != nil {
		t.Fatalf("expected no fields, got %v %v", expiresAt, registrar)
	}
}

func TestParseDateRejectsEmptyAndUnknownFormats(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "  ", "2027/01/02"} {
		if _, err := parseDate(value); err == nil {
			t.Fatalf("expected parse error for %q", value)
		}
	}
}

func TestLooksUnavailable(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"No match for EXAMPLE.COM", "NOT FOUND", "No Data Found", "No entries found", "Status: free", "Domain not found"} {
		if !looksUnavailable(raw) {
			t.Fatalf("expected %q to look unavailable", raw)
		}
	}
	if looksUnavailable("Domain Name: EXAMPLE.COM\nRegistrar: Example") {
		t.Fatalf("expected registered WHOIS response to look available")
	}
}
