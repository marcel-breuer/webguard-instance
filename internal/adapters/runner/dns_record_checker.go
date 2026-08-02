package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/marcel-breuer/webguard-instance/internal/domain/monitor"
	"github.com/miekg/dns"
)

const fixedDNSTimeoutSeconds = 5

type DNSRecordResolver interface {
	Resolve(ctx context.Context, target string, recordType string, timeout time.Duration) ([]string, error)
}

type DNSRecordChecker struct {
	resolver DNSRecordResolver
	logger   *log.Logger
}

func NewDNSRecordChecker(resolver DNSRecordResolver, logger *log.Logger) *DNSRecordChecker {
	if resolver == nil {
		resolver = systemDNSRecordResolver{}
	}
	return &DNSRecordChecker{
		resolver: resolver,
		logger:   logger,
	}
}

func (c *DNSRecordChecker) Supports(monitoringType monitor.Type) bool {
	return monitoringType == monitor.TypeDNSRecord
}

func (c *DNSRecordChecker) Check(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64) {
	status, responseTime, _ := c.Observe(ctx, monitoring)
	return status, responseTime
}

func (c *DNSRecordChecker) Observe(ctx context.Context, monitoring monitor.Monitoring) (monitor.Status, *float64, monitor.RawObservation) {
	start := time.Now()
	observation := monitor.RawObservation{
		Type:              monitoring.Type,
		ObservedAt:        start.UTC(),
		DNSRecordType:     strings.TrimSpace(monitoring.DNSRecordType),
		DNSExpectedValues: append([]string(nil), monitoring.DNSExpectedValues...),
	}
	responseTime := func() *float64 {
		elapsed := roundMilliseconds(time.Since(start))
		observation.ResponseTime = &elapsed
		return &elapsed
	}
	failed := func(reason string) (monitor.Status, *float64, monitor.RawObservation) {
		observation.TransportError = stringPointer(reason)
		return monitor.StatusDown, nil, observation
	}

	target := strings.TrimSpace(monitoring.Target)
	recordType := strings.TrimSpace(monitoring.DNSRecordType)
	if target == "" || recordType == "" || len(monitoring.DNSExpectedValues) == 0 {
		c.logf("Invalid DNS monitoring configuration for %s %s: target/type/expected values are required", target, recordType)
		return failed("invalid_configuration")
	}

	expected, err := normalizeDNSRecordValues(monitoring.DNSExpectedValues, recordType)
	if err != nil {
		c.logf("Invalid expected DNS values for %s %s: %v", target, recordType, err)
		return failed("invalid_expected_values")
	}
	observation.DNSExpectedValues = expected

	timeout := fixedDNSTimeoutSeconds * time.Second
	if monitoring.Timeout > 0 {
		timeout = time.Duration(monitoring.Timeout) * time.Second
	}

	actualRaw, err := c.resolver.Resolve(ctx, target, recordType, timeout)
	if err != nil {
		c.logf("DNS lookup failed for %s %s: %v", target, recordType, err)
		return failed("dns_lookup_failed")
	}

	actual, err := normalizeDNSRecordValues(actualRaw, recordType)
	if err != nil {
		c.logf("Invalid DNS response values for %s %s: %v", target, recordType, err)
		return failed("invalid_observed_values")
	}
	observation.DNSObservedValues = actual
	matched := slices.Equal(actual, expected)
	observation.DNSMatched = boolPointer(matched)

	if matched {
		return monitor.StatusUp, responseTime(), observation
	}

	c.logf("DNS mismatch for %s %s\nexpected: %q\nactual: %q", target, recordType, expected, actual)
	return monitor.StatusDown, responseTime(), observation
}

func (c *DNSRecordChecker) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

type systemDNSRecordResolver struct{}

func (systemDNSRecordResolver) Resolve(ctx context.Context, target string, recordType string, timeout time.Duration) ([]string, error) {
	qtype, ok := dns.StringToType[strings.ToUpper(strings.TrimSpace(recordType))]
	if !ok {
		return nil, fmt.Errorf("unsupported DNS record type: %s", recordType)
	}

	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || len(config.Servers) == 0 {
		return nil, fmt.Errorf("DNS resolver configuration unavailable: %w", err)
	}

	server := net.JoinHostPort(config.Servers[0], config.Port)
	message := new(dns.Msg)
	message.SetQuestion(dns.Fqdn(strings.TrimSpace(target)), qtype)
	message.RecursionDesired = true

	client := &dns.Client{
		Net:     "udp",
		Timeout: timeout,
	}

	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	response, _, err := client.ExchangeContext(queryCtx, message, server)
	if err != nil {
		return nil, err
	}
	if response != nil && response.Truncated {
		tcpClient := &dns.Client{Net: "tcp", Timeout: timeout}
		response, _, err = tcpClient.ExchangeContext(queryCtx, message, server)
		if err != nil {
			return nil, err
		}
	}
	if response == nil {
		return nil, errors.New("empty DNS response")
	}
	if response.Rcode != dns.RcodeSuccess {
		return nil, fmt.Errorf("DNS response code %s", dns.RcodeToString[response.Rcode])
	}

	values := make([]string, 0, len(response.Answer))
	for _, answer := range response.Answer {
		if answer.Header().Rrtype != qtype {
			continue
		}
		switch record := answer.(type) {
		case *dns.A:
			values = append(values, record.A.String())
		case *dns.AAAA:
			values = append(values, record.AAAA.String())
		case *dns.CNAME:
			values = append(values, record.Target)
		case *dns.NS:
			values = append(values, record.Ns)
		case *dns.MX:
			values = append(values, fmt.Sprintf("%d %s", record.Preference, record.Mx))
		case *dns.TXT:
			values = append(values, strings.Join(record.Txt, ""))
		case *dns.SOA:
			values = append(values, fmt.Sprintf(
				"%s %s %d %d %d %d %d",
				record.Ns,
				record.Mbox,
				record.Serial,
				record.Refresh,
				record.Retry,
				record.Expire,
				record.Minttl,
			))
		case *dns.CAA:
			values = append(values, fmt.Sprintf("%d %s %s", record.Flag, record.Tag, record.Value))
		}
	}

	if len(values) == 0 {
		return nil, fmt.Errorf("DNS record %s not found", strings.ToUpper(recordType))
	}

	return values, nil
}

func normalizeDNSRecordValues(values []string, recordType string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))

	for _, value := range values {
		item, err := normalizeDNSRecordValue(value, recordType)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		normalized = append(normalized, item)
	}

	slices.Sort(normalized)
	return normalized, nil
}

func normalizeDNSRecordValue(value string, recordType string) (string, error) {
	recordType = strings.ToUpper(strings.TrimSpace(recordType))

	switch recordType {
	case "A":
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("invalid A record value: %q", value)
		}
		return ip.To4().String(), nil
	case "AAAA":
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil || ip.To4() != nil || ip.To16() == nil {
			return "", fmt.Errorf("invalid AAAA record value: %q", value)
		}
		return strings.ToLower(ip.String()), nil
	case "CNAME", "NS":
		host := normalizeDNSHostname(value)
		if host == "" {
			return "", fmt.Errorf("invalid %s record value: %q", recordType, value)
		}
		return host, nil
	case "MX":
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return "", fmt.Errorf("invalid MX record value: %q", value)
		}
		priority, err := strconv.ParseUint(fields[0], 10, 16)
		if err != nil {
			return "", fmt.Errorf("invalid MX priority: %q", fields[0])
		}
		host := normalizeDNSHostname(fields[1])
		if host == "" {
			return "", fmt.Errorf("invalid MX host: %q", fields[1])
		}
		return fmt.Sprintf("%d %s", priority, host), nil
	case "TXT":
		return normalizeDNSTXT(value), nil
	case "SOA":
		fields := strings.Fields(value)
		if len(fields) != 7 {
			return "", fmt.Errorf("invalid SOA record value: %q", value)
		}
		ns := normalizeDNSHostname(fields[0])
		mbox := normalizeDNSHostname(fields[1])
		if ns == "" || mbox == "" {
			return "", fmt.Errorf("invalid SOA host fields: %q", value)
		}
		numbers := make([]uint64, 5)
		for i := 0; i < 5; i++ {
			parsed, err := strconv.ParseUint(fields[i+2], 10, 32)
			if err != nil {
				return "", fmt.Errorf("invalid SOA numeric field: %q", fields[i+2])
			}
			numbers[i] = parsed
		}
		return fmt.Sprintf("%s %s %d %d %d %d %d", ns, mbox, numbers[0], numbers[1], numbers[2], numbers[3], numbers[4]), nil
	case "CAA":
		fields := strings.Fields(value)
		if len(fields) < 3 {
			return "", fmt.Errorf("invalid CAA record value: %q", value)
		}
		flags, err := strconv.ParseUint(fields[0], 10, 8)
		if err != nil {
			return "", fmt.Errorf("invalid CAA flags: %q", fields[0])
		}
		tag := strings.ToLower(strings.TrimSpace(fields[1]))
		caaValue := normalizeDNSTXT(strings.Join(fields[2:], " "))
		if tag == "" || caaValue == "" {
			return "", fmt.Errorf("invalid CAA record value: %q", value)
		}
		return fmt.Sprintf("%d %s %s", flags, tag, caaValue), nil
	default:
		return "", fmt.Errorf("unsupported DNS record type: %s", recordType)
	}
}

func normalizeDNSHostname(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func normalizeDNSTXT(value string) string {
	trimmed := strings.TrimSpace(value)
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		return unquoted
	}
	return strings.Trim(trimmed, `"`)
}
