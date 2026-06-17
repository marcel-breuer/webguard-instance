# Repository Instructions

## Environment

Work inside the provided Docker environment whenever possible.

Before changing code, inspect the available container setup:

- `Dockerfile`
- `compose.yml`
- `docker-compose.override.yml`
- project scripts such as `start-dev.sh`

Prefer project-defined Docker and Docker Compose commands over host tools or global installations. Run tests, linters, formatters, builds, and debugging commands inside Docker whenever technically possible.

For this project, the effective local development service is:

```bash
docker compose config
docker compose build webguard-instance
docker compose run --rm webguard-instance go test ./...
```

The production image has `webguard-instance` as entrypoint. If `docker compose run ... go test` is interpreted as an application command, rebuild the development target first with `docker compose build webguard-instance`.

## Test Concept

Every test change or test review must follow this process:

1. Run the full suite in Docker:

   ```bash
   docker compose run --rm webguard-instance go test ./...
   ```

2. Run coverage in Docker and inspect package-level weak spots:

   ```bash
   docker compose run --rm webguard-instance go test -cover ./...
   ```

3. Repeat timing-sensitive or concurrency-heavy packages when they were touched:

   ```bash
   docker compose run --rm webguard-instance go test -count=10 ./internal/runner ./internal/server ./internal/scheduler
   ```

4. Try the race detector when the Docker image supports it:

   ```bash
   docker compose run --rm -e CGO_ENABLED=1 webguard-instance go test -race ./...
   ```

   The current Alpine development image may need a C compiler package before this works. If the race detector cannot run, state that clearly in the handoff.

## Test Design Rules

- Prefer `httptest`, fake clients, fake resolvers, and injected dependencies over real external network calls.
- Keep tests deterministic. Avoid fixed sleeps, real retry delays, and assumptions about local ports when a controlled fake or listener is possible.
- Protect tests that mutate package globals, such as test hooks, from parallel execution. Prefer dependency injection over global mutation.
- Assert behavior, not only that an error exists. For validation paths, check the relevant error message or error type.
- For concurrent code, use mutex-protected fakes and snapshot helpers before assertions.
- Use exact type filters in fakes. Do not route fake behavior only by slice length when the values matter.
- Keep table tests focused and name cases by the behavior they protect.
- Prefer `slices.Equal` or purpose-built assertions for typed slices instead of broad reflection when possible.
- Add integration-style tests with `httptest` for Core API contract changes, including paths, methods, headers, query parameters, status handling, and JSON payload shape.
- Add negative tests for parsing and validation helpers, not only happy paths.

## Current Coverage Priorities

When adding or optimizing tests, prioritize these areas first:

- `internal/domainlookup`: RDAP status handling, malformed RDAP JSON, registrar parsing, WHOIS fallback behavior, temporary errors, unavailable domains, invalid targets.
- `internal/monitor`: invalid flexible JSON values, boolean parsing variants, optional integer parsing, malformed heartbeat timestamps.
- `internal/target`: host extraction edge cases, IPv6 host/port forms, private hostname rejection, DNS resolution failures.
- `cmd/webguard-instance`: behavior when `RunMonitoring` returns an error.
- `internal/runner`: retry behavior without real sleeps, ping execution without global mutation, response/SSL/domain phase dispatch with exact type assertions.

## Handoff Expectations

When finishing test-related work, report:

- which Docker commands were run;
- whether the full suite passed;
- coverage command results when relevant;
- whether repeated/flakiness checks were run;
- whether `-race` ran or why it could not run.

Do not mention any AI assistant, automation tool, or code generation tool in code comments, commit messages, pull request titles, pull request descriptions, or repository documentation unless explicitly required.
