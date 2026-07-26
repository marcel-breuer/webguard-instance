# WebGuard Instance

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

> 💡 **System Architecture Note:** This repository contains the **Worker Node**. It requires a running WebGuard Core instance to receive monitoring jobs and report results.

WebGuard Instance is a worker service for executing monitoring jobs from WebGuard Core and reporting the results back to the core API.

The current package boundaries and dependency rule are documented in
[Architecture](ARCHITECTURE.md).

## Features

- **Core-Compatible API Contract**
  - `GET /api/v1/internal/monitorings`
  - `POST /api/v1/internal/monitoring-responses`
  - `POST /api/v1/internal/ssl-results`
  - `POST /api/v1/internal/domain-results`
  - Feature-flagged lease protocol: claim, complete, and release monitoring jobs
  - `X-INSTANCE-CODE` + `X-API-KEY` header authentication
- **Parallel Monitoring Execution**
  - Response, SSL, and domain expiration phases run in parallel
  - Worker-based parallel processing for monitoring jobs
- **Simple Operations**
  - Docker-first local and production setup
  - Liveness: `GET /livez`; readiness: `GET /readyz`; Prometheus metrics: `GET /metrics`
- **Predictable Scheduling**
  - Combined monitoring run every 5 minutes

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
   - `WEBGUARD_CORE_API_KEY`
   - `WEBGUARD_CORE_API_URL`

3. **Start services**
   Local development:
   ```bash
   ./start-dev.sh
   ```

   Production-style:
   ```bash
   ./start-prod.sh
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
- `WEBGUARD_CORE_API_KEY`
- `WEBGUARD_CORE_API_URL`

Runtime settings:

- `QUEUE_DEFAULT_WORKERS` (default: `3`)
- `RUN_MAX_CONCURRENCY` (default: `QUEUE_DEFAULT_WORKERS`; shared upper bound across all check phases)
- `WEBGUARD_JOB_LEASES_ENABLED` (default: `false`; use Core-issued leases instead of legacy polling)
- `WEBGUARD_JOB_LEASES_DUAL_WRITE` (default: `false`; also post legacy result endpoints during a staged Core rollout)
- `WEBGUARD_INSTANCE_ID` (required when leases are enabled; stable worker identity, distinct from its location)
- `WEBGUARD_JOB_LEASE_MAX_BATCH` (default: `QUEUE_DEFAULT_WORKERS`; maximum jobs requested in one lease claim)
- `WEBGUARD_ALLOW_PRIVATE_TARGETS` (default: `false`; set to `true` only when this worker should monitor private, loopback, or link-local targets)
- `SHUTDOWN_DRAIN_TIMEOUT_SECONDS` (default: `10`; drain deadline after `SIGTERM`)
- `PORT` (default: `8080`)

See `.env.example` for full defaults.

The contract and rollout sequence for horizontally scaled workers are in
[the monitoring job lease protocol](JOB_LEASE_PROTOCOL.md).

## Operations

- `GET /livez` only confirms that the process can serve HTTP. Docker uses this
  route for its health check.
- `GET /readyz` returns `200` only when Core configuration is complete and the
  instance is not draining. It returns `503` during configuration errors and
  graceful shutdown.
- `GET /metrics` exposes Prometheus text metrics for run duration and outcome,
  active/queued leased jobs, bounded executor outcomes, Core request latency
  and errors, and lease lifecycle events. It never labels metrics with targets,
  credentials, monitoring IDs, or job IDs.

On `SIGTERM` the instance stops scheduling new work, waits up to
`SHUTDOWN_DRAIN_TIMEOUT_SECONDS` for active work to complete or relinquish its
lease, then shuts down the HTTP server. Telemetry is process-local and can be
scraped without an external backend; OpenTelemetry remains optional and is not
enabled by default.

## CI/CD

- `.github/workflows/ci.yml`
  - formatting, linting, tests, binary build, container build check
- `.github/workflows/docker-image.yml`
  - multi-arch image build and publish to GHCR on `main` and `v*` tags
  - `CHANGELOG.md` and GitHub release notes generation for `v*` tags
