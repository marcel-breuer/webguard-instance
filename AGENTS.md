# Repository Agent Instructions

These instructions apply to every human or AI coding agent working in this repository, regardless of the tool, model, IDE, extension, CLI, automation platform, or execution environment.

## Scope and Applicability

- This root `AGENTS.md` is the canonical source of project-wide instructions.
- Local `AGENTS.md` files MAY add directory-specific rules. The nearest applicable `AGENTS.md` takes precedence for local implementation details.
- Local rules MUST NOT weaken security, privacy, compliance, validation, or review requirements from this file.
- Agent-specific adapter files MUST be thin references to this file, local `AGENTS.md` files, and `.agent/skills/`. They MUST NOT duplicate or contradict the canonical rules.
- Shared skills in `.agent/skills/` supplement this file for recurring task types and MUST remain tool-independent.

## Adapter Coverage

- Committed adapters exist for Claude Code, Cursor, and GitHub Copilot.
- OpenAI Codex, OpenCode, Windsurf, Cline, Roo Code, Aider, GitHub Copilot Coding Agent, and comparable IDE-, CLI-, or automation-based agents are governed directly by this file and `.agent/skills/` unless a supported adapter is added later.
- Do not add adapter files for tools whose repository format is not clearly supported or already present.

## Instruction Priority

Apply instructions in this order:

1. Explicit task requirements and acceptance criteria.
2. Security, privacy, legal, and compliance requirements.
3. Nearest applicable local `AGENTS.md`.
4. Root `AGENTS.md`.
5. Existing architecture and established repository patterns.
6. Repository configuration.
7. Tests and technical documentation.
8. Official Go, Docker, and GitHub Actions documentation.
9. Established community standards.
10. Explicitly documented assumptions.

Agents MUST NOT invent business requirements.

## Token Efficiency

- Read only files relevant to the current task.
- Prefer precise searches over broad file reads.
- Do not scan the full repository when targeted inspection is sufficient.
- Avoid repeatedly reading unchanged files.
- Do not restate the complete task before starting.
- Do not repeat rules already defined in this file.
- Do not include long implementation plans unless task complexity requires them.
- Keep plans concise and focused on execution-critical steps.
- Do not narrate routine tool usage.
- Report only findings that affect implementation, validation, risk, or review.
- Prefer diffs and targeted edits over rewriting complete files.
- Avoid creating abstractions, documentation, comments, tests, or files that are not required.
- Do not produce large code excerpts in final output when file references are sufficient.
- Do not duplicate the same information across summaries, findings, and completion reports.
- Use concise tables or short lists where they reduce repetition.
- Preserve correctness, security, and completeness; token efficiency MUST NOT justify skipping required analysis or validation.

Final output SHOULD normally contain only what changed, which files changed, which validations ran, and unresolved issues, assumptions, or risks.

## Project Overview

- `webguard-instance` is a Go worker node for WebGuard Core.
- The service fetches monitoring jobs from WebGuard Core, executes response, SSL, DNS record, ping, port, keyword, and domain-expiration checks, and posts results back to the Core internal API.
- Main entry point: `cmd/webguard-instance/main.go`.
- Primary packages:
  - `internal/config`: environment-derived runtime configuration.
  - `internal/core`: WebGuard Core HTTP API client.
  - `internal/monitor`: monitoring DTOs, payloads, enum-like types, and flexible JSON parsing.
  - `internal/runner`: monitoring orchestration and check execution.
  - `internal/domainlookup`: domain lookup integration.
  - `internal/scheduler`: five-minute scheduling.
  - `internal/server`: `/` and `/health` health endpoints.
  - `internal/target`: target normalization helpers.
- Runtime model: Docker-first Go service built from `Dockerfile`, composed by `compose.yml` and `docker-compose.override.yml`.
- CI runs Go formatting, vet, tests, binary build, and a production Docker image build in `.github/workflows/ci.yml`.
- Release image publishing and tag changelog generation are defined in `.github/workflows/docker-image.yml`.

## Source of Truth

Use this technical priority:

1. Existing implementation and established patterns.
2. Project configuration.
3. Tests.
4. Repository documentation.
5. Official Go, Docker, and GitHub Actions documentation.
6. Established standards.

Agents MUST NOT replace existing conventions with personal preferences without a concrete technical reason.

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

## Language and Framework Standards

- Go code MUST be idiomatic, `gofmt`-formatted, and compatible with the Go version declared in `go.mod`.
- Keep package APIs small. Export names only when required by another package or tests.
- Public types, constructors, and behavior that are part of package contracts SHOULD be explicit and stable.
- Use `context.Context` for cancellation across HTTP calls, scheduled work, and runner operations.
- Return errors instead of panicking in application code.
- Wrap or preserve errors where useful for callers and tests.
- Keep HTTP clients configured with explicit timeouts.
- Use standard-library facilities before adding dependencies.
- Keep JSON field names compatible with the WebGuard Core API contract.
- Preserve the existing CLI commands: `serve` and `monitoring`.

## Architecture Rules

- `cmd/webguard-instance` MUST remain the composition boundary for configuration, logging, service construction, and command dispatch.
- Business logic for monitoring execution belongs in `internal/runner` or a focused package under `internal/`.
- WebGuard Core API request construction and response handling belong in `internal/core`.
- API DTOs and monitoring payload types belong in `internal/monitor`.
- Health endpoint behavior belongs in `internal/server`.
- Scheduling behavior belongs in `internal/scheduler`.
- Environment parsing belongs in `internal/config`; do not read environment variables throughout unrelated packages.
- External integrations MUST be behind interfaces when tests or alternate implementations need isolation.
- Concurrent code MUST honor context cancellation and avoid goroutine leaks.
- New abstractions require a concrete benefit such as testability, isolation of an external dependency, or reduced meaningful duplication.

## Code Quality

- Run configured formatting, vetting, tests, and builds relevant to the change.
- Keep functions focused and names domain-specific.
- Avoid dead code, commented-out code, unused imports, unused variables, and speculative abstractions.
- Use constants for repeated or meaningful values.
- Comments SHOULD explain why, not restate what the code does.
- Do not introduce broad ignore rules, blanket lint suppressions, unsafe casts, or type bypasses.
- Do not weaken existing quality checks, tests, timeouts, authentication headers, or error handling.
- Identify breaking changes explicitly.

## Naming Conventions

- Go packages use short lowercase names without underscores.
- Go files use lowercase names; tests use `*_test.go`.
- Tests SHOULD name the behavior under test, following existing `Test...` patterns.
- Interfaces SHOULD describe required behavior and stay close to their consumers.
- Environment variables are uppercase snake case and must be documented in `.env.example` and relevant docs when added.
- WebGuard Core JSON fields and endpoint paths MUST remain compatible with the existing API contract unless the task explicitly changes that contract.

## Testing Rules

- The repository uses Go tests colocated with packages in `cmd/` and `internal/`.
- Add or update tests for new or changed business logic.
- Add regression tests for bug fixes where feasible.
- Test observable behavior, package contracts, and edge cases rather than implementation details.
- Use standard Go test isolation; avoid real network calls when an HTTP test server, fake client, or injected interface can cover behavior.
- Do not remove or weaken tests merely to make a change pass.
- Do not ignore failing tests. If a relevant check cannot run, state why.

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

## Validation Commands

Prefer running commands inside the project Docker environment when technically possible. Use project-defined Docker Compose files before host-local tools.

| Purpose | Command |
| --- | --- |
| Compose validation | `docker compose -f compose.yml -f docker-compose.override.yml config` |
| Development image build | `docker compose build webguard-instance` |
| Dependency download | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance go mod download` |
| Formatting check | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance sh -lc 'test -z "$(gofmt -l .)"'` |
| Vet | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance go vet ./...` |
| Tests | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance go test ./...` |
| Coverage | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance go test -cover ./...` |
| Timing-sensitive repeats | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance go test -count=10 ./internal/runner ./internal/server ./internal/scheduler` |
| Race detector | `docker compose -f compose.yml -f docker-compose.override.yml run --rm -e CGO_ENABLED=1 webguard-instance go test -race ./...` |
| Binary build | `docker compose -f compose.yml -f docker-compose.override.yml run --rm webguard-instance go build ./cmd/webguard-instance` |
| Production image build | `docker build --target production -t webguard-instance:test .` |
| Local development | `./start-dev.sh` |
| One-off monitoring | `docker compose -f compose.yml run --rm webguard-instance monitoring` |

Use this matrix to choose checks:

| Change type | Required validation |
| --- | --- |
| Documentation or governance | `docker compose ... config`, relevant diff review |
| Go logic | formatting check, `go vet ./...`, `go test ./...`, relevant build |
| WebGuard Core API contract | tests covering request/response behavior and relevant runner/client tests |
| Monitoring runner behavior | runner tests and `go test ./...`; repeat timing-sensitive packages when touched |
| Docker | compose validation and production image build |
| CI/CD | workflow review plus local commands matching changed workflow steps where possible |
| Dependencies | dependency download, lockfile review, tests, build |

If a command cannot be executed, disclose the reason.

When finishing test-related work, report which Docker commands were run, whether the full suite passed, coverage results when relevant, whether repeated/flakiness checks were run, and whether `-race` ran or why it could not run.

## Dependency Management

- Go modules are managed by `go.mod` and `go.sum`.
- Do not change `go.sum` without a corresponding dependency change.
- Prefer the standard library before adding dependencies.
- New dependencies require a concrete need and review for maintenance, security, license, size, transitive dependencies, and runtime impact.
- Do not add duplicate libraries for functionality already present.
- Do not perform unrelated upgrades or unrequested major-version updates.

## Security, Privacy, and Compliance

- Do not commit secrets, credentials, tokens, or production personal data.
- `.env` is local configuration; `.env.example` documents expected variables.
- Use `WEBGUARD_CORE_API_KEY`, `WEBGUARD_CORE_API_URL`, and `WEBGUARD_LOCATION` through existing configuration mechanisms.
- Preserve `X-API-KEY` and `X-INSTANCE-CODE` authentication headers for WebGuard Core calls.
- Validate and normalize input at trust boundaries, including environment, HTTP responses, CLI arguments, and Core API payloads.
- Use context-aware, timeout-bounded HTTP and command execution.
- Avoid logging secrets, auth headers, credentials, request bodies containing sensitive data, or internal error details not needed for operations.
- Use injection-safe shell and command patterns; do not concatenate untrusted input into shell commands.
- Do not disable security controls or add telemetry, external services, or outbound integrations without explicit approval.
- Do not upload repository content to external systems without authorization.

## API and Integration Rules

- WebGuard Core instance endpoints currently used by this worker are:
  - `GET /api/instances/monitorings`
  - `POST /api/instances/monitoring-responses`
  - `POST /api/instances/ssl-results`
  - `POST /api/instances/domain-results`
- Health endpoints are `GET /` and `GET /health`; other methods return `405`.
- Preserve request headers, JSON field names, status handling, and timeout behavior unless the task explicitly changes the contract.
- Use test servers or fakes for Core API behavior in tests.
- External DNS, ping, TLS, and domain lookup behavior MUST be isolated in tests where feasible.

## Docker and Operations Rules

- Docker is the preferred execution environment.
- `Dockerfile` has `development`, `builder`, and `production` targets; preserve that separation.
- `compose.yml` describes production-style service settings, healthcheck, ports, env file, restart policy, and image name.
- `docker-compose.override.yml` switches the service to the development target and mounts the repository.
- Do not commit local `.env` values.
- Keep healthcheck behavior aligned with the service health endpoints.

## Documentation Rules

- Update documentation when commands, environment variables, operational behavior, public API contracts, or setup steps change.
- Do not document commands that do not exist or have not been verified in the repository.
- Do not make unverified claims about external systems.
- Do not manually edit generated release notes or generated changelog content unless the workflow explicitly requires it.
- Do not mention any AI assistant, automation tool, or code generation tool in code comments, commit messages, pull request titles, pull request descriptions, or repository documentation unless explicitly required.

## Git and Change Scope

- Limit changes to the task.
- Do not perform unrelated refactors or format untouched files.
- Do not manually edit generated files unless required by the task.
- Do not overwrite local changes from another author.
- Do not run destructive Git commands without explicit instruction.
- Do not create commits, pushes, tags, releases, pull requests, CI/CD changes, infrastructure changes, or security-policy changes without explicit instruction and task relevance.
- Keep changes small and reviewable.
- Branch names MUST describe the work and MUST NOT include coding-agent or automation-tool branding.

## Agent Workflow

1. Read the task and acceptance criteria.
2. Read this file and the nearest local `AGENTS.md`, if any.
3. Identify and read only relevant skills from `.agent/skills/`.
4. Inspect only relevant files and existing patterns.
5. Evaluate architecture, dependencies, and risks.
6. Plan the smallest viable change.
7. Implement the change.
8. Add or update tests where behavior changes.
9. Run relevant validation commands.
10. Review the diff for unintended changes.
11. Report changes, validation, assumptions, and remaining risks concisely.

Agents MUST NOT begin implementation before checking relevant rules and skills.

## Definition of Done

A task is complete only when:

- Acceptance criteria are met.
- Architecture rules are followed.
- Relevant tests exist for behavior changes.
- Required validation succeeds or skipped checks are disclosed.
- No known unnecessary warnings remain.
- Security and privacy requirements are met.
- Documentation is updated where required.
- No unintended files changed.
- Assumptions and remaining risks are stated.
