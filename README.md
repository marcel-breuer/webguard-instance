# WebGuard Instance

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

> 💡 **System Architecture Note:** This repository contains the **Worker Node**. It requires a running WebGuard Core instance to receive monitoring jobs and report results.

WebGuard Instance is a worker service for executing monitoring jobs from WebGuard Core and reporting the results back to the core API.

The current package boundaries and dependency rule are documented in
[Architecture](ARCHITECTURE.md).

## Features

- **Core-Compatible API Contract**
  - Core instance contract at `GET /api/instances/monitorings`
  - `POST /api/instances/monitoring-responses`
  - `POST /api/instances/ssl-results`
  - `POST /api/instances/domain-results`
  - additive raw observations for derived health; see
    [the monitoring observation contract](MONITORING_OBSERVATION_CONTRACT.md)
  - Feature-flagged lease protocol: claim, complete, and release monitoring jobs
  - `X-INSTANCE-CODE` + `X-API-KEY` header authentication
- **Parallel Monitoring Execution**
  - Response, SSL, and domain expiration phases run in parallel
  - Worker-based parallel processing for monitoring jobs
- **Simple Operations**
  - Docker-first local and production setup
  - Liveness: `GET /livez`; readiness: `GET /readyz`; Prometheus metrics: `GET /metrics`
- **Predictable Scheduling**
  - The dispatcher polls Core every minute
  - HTTP and keyword checks run when the selected 1, 5, 10, 15, 30, or 60-minute interval is due, independently per monitoring and location
  - New monitors with no response at a location are due on the next poll; existing monitors default to 15 minutes
  - SSL and domain-expiration phases retain their five-minute cadence

## Getting Started

### Prerequisites

- Docker
- Docker Compose
- A running WebGuard Core instance

### Installation

1. **Clone the repository**
   ```bash
   git clone git@github.com:marcel-breuer/webguard-instance.git
   cd webguard-instance
   ```

2. **Configure environment**
   ```bash
   cp .env.example .env
   ```
   Required values:
   - `WEBGUARD_LOCATION`
   - `WEBGUARD_CORE_API_KEY` or `WEBGUARD_CORE_API_KEY_FILE`
   - `WEBGUARD_CORE_API_URL`

3. **Start services**
   Local development:
   ```bash
   ./start-dev.sh
   ```

   Production-style:
   ```bash
   docker compose -f compose.yml up -d --build
   ```

4. **Verify health**
   ```bash
   curl http://localhost:8080/
   ```

## Useful Commands

- Run one-off monitoring:
  ```bash
  docker compose -f compose.yml run --rm webguard-instance monitoring
  ```
- Stop production compose:
  ```bash
  docker compose -f compose.yml down
  ```
- Stop local development compose:
  ```bash
  docker compose -f compose.yml -f docker-compose.override.yml down
  ```

## Configuration

Main integration settings:

- `WEBGUARD_LOCATION` (instance code used for `location` query and `X-INSTANCE-CODE` header)
- `WEBGUARD_CORE_API_KEY` or `WEBGUARD_CORE_API_KEY_FILE` (the mounted file takes precedence and is reread for each Core request)
- `WEBGUARD_CORE_API_URL`
- `WEBGUARD_INSTANCE_API_BASE_PATH` (default: `/api/instances`; only this Core instance route family is accepted)

Runtime settings:

- `QUEUE_DEFAULT_WORKERS` (default: `3`; bounded to `1..64` workers)
- `RUN_MAX_CONCURRENCY` (default: `QUEUE_DEFAULT_WORKERS`; shared upper bound across all check phases, bounded to `1..64`)
- `WEBGUARD_JOB_LEASES_ENABLED` (default: `false`; use Core-issued leases instead of legacy polling)
- `WEBGUARD_JOB_LEASES_DUAL_WRITE` (default: `false`; also post legacy result endpoints during a staged Core rollout)
- `WEBGUARD_INSTANCE_ID` (required when leases are enabled; stable worker identity, distinct from its location)
- `WEBGUARD_JOB_LEASE_MAX_BATCH` (default: `QUEUE_DEFAULT_WORKERS`; maximum jobs requested in one lease claim, bounded to `256` and further limited to available workers)
- `WEBGUARD_ALLOW_PRIVATE_TARGETS` (default: `false`; set to `true` only when this worker should monitor private, loopback, or link-local targets)
- `SHUTDOWN_DRAIN_TIMEOUT_SECONDS` (default: `10`; drain deadline after `SIGTERM`)
- `PORT` (default: `8080`)

See `.env.example` for full defaults.

The worker sends the API key only in the Core `X-API-KEY` header. It does not
log the key or Core error response bodies, and it refuses credential-bearing
Core URLs and cross-origin redirects. Use HTTPS for Core outside trusted local
Docker networks. For rotation without restarting the worker, mount
`WEBGUARD_CORE_API_KEY_FILE` and replace its contents atomically; when both key
settings are present, the file takes precedence.

Monitoring timeouts from Core are capped at 60 seconds. HTTP probes also cap
request bodies at 1 MiB, headers at 64 entries and 32 KiB total, and response
bodies at 2 MiB. The default egress policy rejects loopback, private, link-local,
shared, documentation, benchmarking, reserved, and NAT64 addresses. Set
`WEBGUARD_ALLOW_PRIVATE_TARGETS=true` only for a deployment that intentionally
checks private targets.

Production deployment and rollback guidance is documented in
[the production instance runbook](PRODUCTION_RUNBOOK.md).

For the legacy polling path, Core supplies a location-specific `last_response_at`
and `check_interval_seconds`. The worker polls every minute and starts a
response check when that location has no prior result or its interval has
elapsed since the last response. HTTP and keyword monitorings use the selected
1, 5, 10, 15, 30, or 60-minute interval; each result reports the interval used
for execution. With job leases enabled, Core owns due-time selection and must
apply the same per-location rule before returning a response job. SSL and
domain-expiration polling remains on a five-minute cadence.

The contract and rollout sequence for horizontally scaled workers are in
[the monitoring job lease protocol](JOB_LEASE_PROTOCOL.md).

The supported Core scanner contract uses the `/api/instances` route family,
published by WebGuard Core as the
[WebGuard Instance API contract](https://github.com/marcel-breuer/webguard/blob/main/docs/integrations/webguard-instance-api.md).
The adapter accepts only this instance path; it never calls browser routes.

### Callback retries and idempotency

Each executed monitoring job carries one UUID v4 callback identifier. The
worker sends it as the `Idempotency-Key` header on monitoring, SSL, and domain
result callbacks and reuses it for bounded transport/5xx retries. The key is
never included in the JSON payload and contains no target or credential data.

For leased jobs, a valid Core-issued execution key is reused; older or missing
lease keys are rejected and released before execution so retries cannot create
a new key. The same key is used for the result callback and lease completion.
Duplicate job IDs in a claim batch are executed once. The worker keeps no local
lease database; restart recovery depends on Core retaining the lease attempt and
idempotency key, then making unfinished jobs claimable after expiry. Keep
`WEBGUARD_JOB_LEASES_ENABLED=false` unless the configured Core implements that
lease contract. Independent legacy executions receive different UUID v4 keys.

The observable job path is `received → claimed → queued → running → completed`.
Invalid jobs, cancellation, expiry, or failed lease renewal are counted as
release transitions. The worker renews active leases before expiry; a renewal
failure cancels the check so another worker can safely retry it. Core API
request metrics include claim, extension, completion, and release latency.

## Operations

- `GET /livez` only confirms that the process can serve HTTP. Docker uses this
  route for its health check.
- `GET /readyz` returns `200` only when Core configuration is complete and the
  instance is not draining. It returns `503` during configuration errors and
  graceful shutdown.
- `GET /metrics` exposes Prometheus text metrics for run duration and outcome,
  per-check duration, scheduler delay and run duration, lease wait, active/queued
  jobs, bounded executor outcomes, Core request latency and errors, state
  transitions, lease lifecycle events, goroutine count, and Go heap usage. It
  never labels metrics with targets, credentials, monitoring IDs, or job IDs.

On `SIGTERM` the instance stops scheduling new work, waits up to
`SHUTDOWN_DRAIN_TIMEOUT_SECONDS` for active work to complete or relinquish its
lease, then shuts down the HTTP server. Telemetry is process-local and can be
scraped without an external backend; OpenTelemetry remains optional and is not
enabled by default.

## CI/CD

- `.github/workflows/ci.yml`
  - formatting, vet, tests, binary build, and a pull-request container build check
  - runs only when Go or container configuration changes; superseded pull-request runs are cancelled
- `.github/workflows/docker-image.yml`
  - multi-arch image build and publish to GHCR for image-relevant `main` changes and `v*` tags
  - owns production image builds for `main` so CI does not rebuild the same image
  - release notes generation and `CHANGELOG.md` updates for `v*` tags
