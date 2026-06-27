# Skill: Implement Feature

## Purpose

Add repository behavior while preserving the Go worker architecture and WebGuard Core contract.

## When to Use

Use for new monitoring behavior, configuration, CLI commands, health behavior, Core integration behavior, or operational behavior.

## When Not to Use

Do not use for pure documentation, dependency-only, CI-only, Docker-only, or review-only tasks.

## Required Context

Read `AGENTS.md`, the affected package, existing tests for that package, and any related skill such as `api-change` or `monitoring-runner-change`.

## Relevant Project Areas

`cmd/webguard-instance`, `internal/config`, `internal/core`, `internal/monitor`, `internal/runner`, `internal/scheduler`, `internal/server`, `internal/target`.

## Procedure

1. Identify the package that owns the behavior.
2. Follow existing constructor, interface, DTO, and test patterns.
3. Keep environment parsing in `internal/config`.
4. Keep Core API details in `internal/core` and DTOs in `internal/monitor`.
5. Add the smallest implementation that satisfies the task.
6. Add or update behavior-focused tests.

## Validation

Run the formatting check, `go vet ./...`, `go test ./...`, and the relevant build command inside Docker when possible.

## Expected Output

Report changed behavior, changed files, validations, and any skipped checks or risks.

## Constraints

Do not change public contracts, endpoint paths, JSON field names, authentication headers, or scheduling behavior unless explicitly required.

## Completion Criteria

The feature works through observable behavior, relevant tests cover it, and required validation passes or skipped checks are disclosed.
