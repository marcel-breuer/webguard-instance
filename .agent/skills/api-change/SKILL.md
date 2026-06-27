# Skill: API Change

## Purpose

Change HTTP behavior or WebGuard Core integration contracts safely.

## When to Use

Use for changes to `internal/core`, `internal/monitor`, `internal/server`, endpoint paths, headers, JSON payloads, status handling, or health endpoints.

## When Not to Use

Do not use for runner-only logic that does not alter API contracts.

## Required Context

Read `AGENTS.md`, affected API code, existing client/server tests, and README API notes.

## Relevant Project Areas

`internal/core`, `internal/monitor`, `internal/server`, `cmd/webguard-instance`, README API contract section.

## Procedure

1. Identify whether the change affects Core API compatibility, worker health endpoints, or internal DTO parsing.
2. Preserve authentication headers unless explicitly changing auth.
3. Add tests for request method, path, query, headers, body, response parsing, and error handling where relevant.
4. Keep timeouts and context propagation explicit.
5. Update documentation when public behavior or environment requirements change.

## Validation

Run targeted client/server tests, `go test ./...`, and vet/build when implementation changed.

## Expected Output

Report contract changes, compatibility impact, changed files, validations, and migration risks.

## Constraints

Do not silently change JSON field names, endpoint paths, auth headers, or accepted methods.

## Completion Criteria

API behavior is tested, documented if externally visible, and compatible unless a breaking change was explicitly requested.
