# Monitoring observation contract

This document defines the additive scanner-to-Core payload used by issue #42.
It applies to `POST /api/instances/monitoring-responses`.

## Compatibility

Every response keeps the existing `monitoring_id`, `status`, `response_time`,
and `http_status_code` fields. Core versions that only read those fields can
continue to process the callback. New workers additionally send `observation`.

The authenticated instance route family, `X-INSTANCE-CODE`, `X-API-KEY`, and
the existing bounded retry and idempotency behavior are unchanged.

## Payload

```json
{
  "monitoring_id": "monitoring-1",
  "status": "up",
  "response_time": 42.5,
  "http_status_code": 200,
  "observation": {
    "type": "http",
    "observed_at": "2026-08-02T08:00:00Z",
    "http_status_code": 200,
    "response_time": 42.5
  }
}
```

`observation` is omitted for maintenance/legacy-only payload construction. The
worker never includes an underlying transport error message; it sends a stable
failure category instead.

The observation fields are:

| Field | Meaning |
| --- | --- |
| `type` | Monitoring type that produced the observation. |
| `observed_at` | UTC timestamp at which the check started. |
| `http_status_code` | HTTP status received by an HTTP or keyword check. |
| `response_time` | Elapsed milliseconds only when a response or connection was obtained. It is omitted for failed requests. |
| `transport_error` | Stable category such as `http_transport_error`, `ping_failed`, `connection_failed`, or `dns_lookup_failed`. |
| `connected` | Whether ping or TCP-port connection succeeded. |
| `keyword_matched` | Whether a keyword check found its configured keyword. The keyword itself is never relayed. |
| `dns_record_type` | DNS record type queried. |
| `dns_expected_values` | Normalized configured DNS values. |
| `dns_observed_values` | Normalized values returned by the resolver. |
| `dns_matched` | Whether observed DNS values matched the configured values. |
| `metadata` | Small type-specific values that are safe to expose, currently the TCP port. |

Core can derive `up`, `down`, and `unknown` from these observations while
evaluating latency independently for reachable HTTP and keyword checks.
