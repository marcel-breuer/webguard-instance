# Skill: Monitoring Runner Change

## Purpose

Modify monitoring execution behavior safely.

## When to Use

Use for response, SSL, DNS record, ping, port, keyword, domain-expiration, concurrency, scheduling, or result-posting behavior.

## When Not to Use

Do not use for Core API client-only changes or documentation-only tasks.

## Required Context

Read `AGENTS.md`, `internal/runner`, related package tests, and affected DTOs in `internal/monitor`.

## Relevant Project Areas

`internal/runner`, `internal/domainlookup`, `internal/target`, `internal/monitor`, `internal/scheduler`.

## Procedure

1. Identify the monitoring type and result payload affected.
2. Preserve maintenance handling, unsupported-type handling, worker limits, and context cancellation unless explicitly changed.
3. Isolate external systems with fakes, injected functions, or test servers.
4. Check timeout, retry, redirect, and command-execution behavior for regressions.
5. Add tests for success, failure, and edge cases.

## Validation

Run runner-related tests, `go test ./...`, formatting, and vet.

## Expected Output

Report monitoring behavior changed, tests added or updated, validations, and operational risks.

## Constraints

Do not make live network behavior required for tests. Do not log credentials or sensitive target data unnecessarily.

## Completion Criteria

Runner behavior is deterministic under test, context-aware, and validated.
