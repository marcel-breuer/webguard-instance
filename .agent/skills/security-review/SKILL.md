# Skill: Security Review

## Purpose

Assess or improve security-sensitive repository behavior.

## When to Use

Use for authentication, authorization assumptions, secrets, env handling, external HTTP calls, DNS/command execution, logging, Docker hardening, or CI permissions.

## When Not to Use

Do not use for general code review unless security risk is in scope.

## Required Context

Read `AGENTS.md`, the affected code or config, related tests, and relevant Docker or workflow files.

## Relevant Project Areas

`internal/core`, `internal/config`, `internal/runner`, `internal/server`, Docker files, GitHub Actions, `.env.example`.

## Procedure

1. Identify trust boundaries and sensitive values.
2. Check for secret exposure, unsafe logging, missing timeouts, unsafe command execution, and unvalidated external input.
3. Verify auth headers and Core API configuration are preserved.
4. Prefer restrictive defaults and explicit errors.
5. Add tests for security-relevant behavior where feasible.

## Validation

Run relevant tests and build checks for changed code; inspect Docker and workflow permissions for config-only changes.

## Expected Output

Report risks, changes or findings, validations, and residual assumptions.

## Constraints

Do not publish secrets, weaken security controls, add telemetry, or upload repository content externally without approval.

## Completion Criteria

Security-sensitive behavior is explicit, tested where feasible, and does not weaken existing protections.
