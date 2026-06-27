# Skill: Fix Bug

## Purpose

Diagnose and correct a defect with minimal, testable changes.

## When to Use

Use when behavior is incorrect, tests fail, operational output is wrong, or a regression is reported.

## When Not to Use

Do not use for feature work without a defect, broad refactors, or dependency-only changes.

## Required Context

Read `AGENTS.md`, the failing behavior description, relevant tests, and the smallest source area that can explain the bug.

## Relevant Project Areas

All Go packages under `cmd/` and `internal/`, plus Docker or CI files only if they are part of the failure.

## Procedure

1. Reproduce or reason from an existing failing test.
2. Identify the responsible boundary before editing.
3. Add a regression test where feasible.
4. Fix the root cause without unrelated cleanup.
5. Check nearby error handling, timeouts, and context cancellation when the bug involves external work.

## Validation

Run the targeted test first, then `go test ./...`; add formatting, vet, build, Docker, or CI validation when relevant.

## Expected Output

Report root cause, files changed, tests added or updated, validations, and residual risk.

## Constraints

Do not weaken tests, suppress errors, hide failures in logs, or broaden retries/timeouts without a reason.

## Completion Criteria

The bug is fixed, regression coverage exists where feasible, and relevant validation passes or skipped checks are disclosed.
