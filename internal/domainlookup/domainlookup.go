package domainlookup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const defaultRDAPBaseURL = "https://rdap.org/domain/"
const maxDomainLength = 253
const maxDomainLabels = 127
const maxDomainLabelLength = 63

var (
	expirationFieldPattern = regexp.MustCompile(`(?i)^\s*(registry expiry date|registrar registration expiration date|expiration date|paid-till|expiry date)\s*:\s*(.+?)\s*$`)
	registrarFieldPattern  = regexp.MustCompile(`(?i)^\s*(registrar|sponsoring registrar)\s*:\s*(.+?)\s*$`)
	whoisReferralPattern   = regexp.MustCompile(`(?i)^\s*refer:\s*(\S+)\s*$`)
)

type TemporaryError struct {
	Err error
}

func (e *TemporaryError) Error() string {
	if e.Err == nil {
		return "temporary domain lookup failure"
	}
	return e.Err.Error()
}

func (e *TemporaryError) Unwrap() error {
	return e.Err
}

func IsTemporary(err error) bool {
	var temporary *TemporaryError
	return errors.As(err, &temporary)
}

type Result struct {
	Domain     string
	Registered bool
	ExpiresAt  *time.Time
	Registrar  *string
	CheckedAt  time.Time
}

type Lookup struct {
	httpClient  *http.Client
	dialer      *net.Dialer
	rdapBaseURL string
}

func New(timeout time.Duration) *Lookup {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Lookup{
		httpClient:  &http.Client{Timeout: timeout},
		dialer:      &net.Dialer{Timeout: timeout},
		rdapBaseURL: defaultRDAPBaseURL,
	}
}

func (l *Lookup) Lookup(ctx context.Context, target string) (Result, error) {
	domain := NormalizeTarget(target)
	checkedAt := time.Now().UTC()
	result := Result{
		Domain:    domain,
		CheckedAt: checkedAt,
	}
	if !validDomainName(domain) {
		return result, nil
	}

	var temporaryErr error
	for _, candidate := range lookupCandidates(domain) {
		candidateResult, err := l.lookupCandidate(ctx, candidate, checkedAt)
		if err != nil {
			if IsTemporary(err) && temporaryErr == nil {
				temporaryErr = err
			}
			continue
		}
		if candidateResult.Registered {
			return candidateResult, nil
		}
	}

	if temporaryErr != nil {
		return result, &TemporaryError{Err: temporaryErr}
	}

	return result, nil
}

func (l *Lookup) lookupCandidate(ctx context.Context, domain string, checkedAt time.Time) (Result, error) {
	result := Result{
		Domain:    domain,
		CheckedAt: checkedAt,
	}

	rdapResult, rdapErr := l.lookupRDAP(ctx, domain, checkedAt)
	if rdapErr == nil && (!rdapResult.Registered || rdapResult.ExpiresAt != nil) {
		return rdapResult, nil
	}

	whoisResult, whoisErr := l.lookupWHOIS(ctx, domain, checkedAt)
	if whoisErr == nil {
		if whoisResult.Registered {
			if whoisResult.Registrar == nil {
				whoisResult.Registrar = rdapResult.Registrar
			}
			return whoisResult, nil
		}
		if rdapErr == nil && rdapResult.Registered {
			return rdapResult, nil
		}
		if IsTemporary(rdapErr) {
			return result, &TemporaryError{Err: rdapErr}
		}
		if whoisResult.Registrar == nil {
			whoisResult.Registrar = rdapResult.Registrar
		}
		return whoisResult, nil
	}

	if rdapErr == nil && rdapResult.Registered {
		return rdapResult, nil
	}
	if IsTemporary(rdapErr) || IsTemporary(whoisErr) {
		return result, &TemporaryError{Err: firstError(rdapErr, whoisErr)}
	}

	return result, nil
}

func NormalizeTarget(target string) string {
	return strings.Trim(strings.TrimSpace(strings.ToLower(target)), ".")
}

func validDomainName(domain string) bool {
	if domain == "" || len(domain) > maxDomainLength || strings.Contains(domain, "/") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) == 0 || len(labels) > maxDomainLabels {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > maxDomainLabelLength || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, char := range label {
			if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func (l *Lookup) lookupRDAP(ctx context.Context, domain string, checkedAt time.Time) (Result, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, l.rdapBaseURL+domain, nil)
	if err != nil {
		return Result{Domain: domain, CheckedAt: checkedAt}, err
	}
	request.Header.Set("Accept", "application/rdap+json, application/json")

	response, err := l.httpClient.Do(request)
	if err != nil {
		return Result{Domain: domain, CheckedAt: checkedAt}, &TemporaryError{Err: err}
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		return Result{Domain: domain, CheckedAt: checkedAt}, nil
	}
	if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
		return Result{Domain: domain, CheckedAt: checkedAt}, &TemporaryError{Err: fmt.Errorf("rdap returned status %d", response.StatusCode)}
	}
	if response.StatusCode >= http.StatusBadRequest {
		return Result{Domain: domain, CheckedAt: checkedAt}, fmt.Errorf("rdap returned status %d", response.StatusCode)
	}

	var payload rdapDomainResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return Result{Domain: domain, CheckedAt: checkedAt}, err
	}

	expiresAt := parseRDAPExpiration(payload.Events)
	registrar := parseRDAPRegistrar(payload.Entities)
	return Result{
		Domain:     domain,
		Registered: true,
		ExpiresAt:  expiresAt,
		Registrar:  registrar,
		CheckedAt:  checkedAt,
	}, nil
}

func (l *Lookup) lookupWHOIS(ctx context.Context, domain string, checkedAt time.Time) (Result, error) {
	server, err := l.lookupWHOISServer(ctx, tld(domain))
	if err != nil {
		return Result{Domain: domain, CheckedAt: checkedAt}, err
	}
	if server == "" {
		return Result{Domain: domain, CheckedAt: checkedAt}, nil
	}

	raw, err := l.queryWHOIS(ctx, server, domain)
	if err != nil {
		return Result{Domain: domain, CheckedAt: checkedAt}, &TemporaryError{Err: err}
	}
	if looksUnavailable(raw) {
		return Result{Domain: domain, CheckedAt: checkedAt}, nil
	}

	expiresAt, registrar := ParseWHOIS(raw)
	return Result{
		Domain:     domain,
		Registered: true,
		ExpiresAt:  expiresAt,
		Registrar:  registrar,
		CheckedAt:  checkedAt,
	}, nil
}

func (l *Lookup) lookupWHOISServer(ctx context.Context, tld string) (string, error) {
	if tld == "" {
		return "", nil
	}
	raw, err := l.queryWHOIS(ctx, "whois.iana.org", tld)
	if err != nil {
		return "", &TemporaryError{Err: err}
	}
	matches := whoisReferralPattern.FindStringSubmatch(raw)
	if len(matches) < 2 {
		return "", nil
	}
	return strings.TrimSpace(matches[1]), nil
}

func (l *Lookup) queryWHOIS(ctx context.Context, server, query string) (string, error) {
	var dialer net.Dialer
	if l.dialer != nil {
		dialer = *l.dialer
	}

	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(server, "43"))
	if err != nil {
		return "", err
	}
	defer connection.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else if dialer.Timeout > 0 {
		_ = connection.SetDeadline(time.Now().Add(dialer.Timeout))
	}

	if _, err := fmt.Fprintf(connection, "%s\r\n", query); err != nil {
		return "", err
	}
	response, err := io.ReadAll(io.LimitReader(connection, 2<<20))
	if err != nil {
		return "", err
	}
	return string(response), nil
}

type rdapDomainResponse struct {
	Events   []rdapEvent  `json:"events"`
	Entities []rdapEntity `json:"entities"`
}

type rdapEvent struct {
	EventAction string `json:"eventAction"`
	EventDate   string `json:"eventDate"`
}

type rdapEntity struct {
	Roles      []string `json:"roles"`
	VCardArray []any    `json:"vcardArray"`
}

func parseRDAPExpiration(events []rdapEvent) *time.Time {
	for _, event := range events {
		action := strings.ToLower(strings.TrimSpace(event.EventAction))
		if action != "expiration" && action != "expiry" && action != "registration expiration" {
			continue
		}
		parsed, err := parseDate(event.EventDate)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func parseRDAPRegistrar(entities []rdapEntity) *string {
	for _, entity := range entities {
		if !hasRole(entity.Roles, "registrar") {
			continue
		}
		if registrar := parseVCardFN(entity.VCardArray); registrar != nil {
			return registrar
		}
	}
	return nil
}

func hasRole(roles []string, role string) bool {
	for _, item := range roles {
		if strings.EqualFold(strings.TrimSpace(item), role) {
			return true
		}
	}
	return false
}

func parseVCardFN(vcard []any) *string {
	if len(vcard) < 2 {
		return nil
	}
	properties, ok := vcard[1].([]any)
	if !ok {
		return nil
	}
	for _, rawProperty := range properties {
		property, ok := rawProperty.([]any)
		if !ok || len(property) < 4 {
			continue
		}
		name, ok := property[0].(string)
		if !ok || !strings.EqualFold(name, "fn") {
			continue
		}
		value, ok := property[3].(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return &value
		}
	}
	return nil
}

func ParseWHOIS(raw string) (*time.Time, *string) {
	var expiresAt *time.Time
	var registrar *string

	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if matches := expirationFieldPattern.FindStringSubmatch(line); len(matches) == 3 {
			if parsed, err := parseDate(cleanDateValue(matches[2])); err == nil {
				if expiresAt == nil || parsed.Before(*expiresAt) {
					value := parsed
					expiresAt = &value
				}
			}
			continue
		}
		if registrar == nil {
			if matches := registrarFieldPattern.FindStringSubmatch(line); len(matches) == 3 {
				value := strings.TrimSpace(matches[2])
				if value != "" {
					registrar = &value
				}
			}
		}
	}

	return expiresAt, registrar
}

func cleanDateValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, ".")
	if index := strings.Index(value, " ("); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value
}

func parseDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty date")
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05",
		"2006-01-02",
		"02-Jan-2006",
		"02-Jan-2006 15:04:05 MST",
		"2006.01.02",
		"02.01.2006",
		"January 2 2006",
		"2 January 2006",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), nil
		}
	}

	return time.Time{}, fmt.Errorf("unsupported date format: %s", value)
}

func looksUnavailable(raw string) bool {
	normalized := strings.ToLower(raw)
	unavailableMarkers := []string{
		"no match for",
		"not found",
		"no data found",
		"no entries found",
		"status: free",
		"domain not found",
	}
	for _, marker := range unavailableMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func tld(domain string) string {
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

func lookupCandidates(domain string) []string {
	parts := strings.Split(domain, ".")
	if len(parts) <= 2 {
		return []string{domain}
	}

	candidates := make([]string, 0, len(parts)-1)
	for i := 0; i <= len(parts)-2; i++ {
		candidates = append(candidates, strings.Join(parts[i:], "."))
	}
	return candidates
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
