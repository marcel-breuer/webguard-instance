# Skill: Write Tests

## Purpose

Add or improve deterministic Go tests for repository behavior.

## When to Use

Use when adding coverage, protecting a bug fix, updating behavior, or reviewing insufficient tests.

## When Not to Use

Do not use for changes where no behavior changes and existing validation is enough.

## Required Context

Read `AGENTS.md`, the package under test, existing `*_test.go` files in that package, and related DTO or interface definitions.

## Relevant Project Areas

Go tests under `cmd/` and `internal/`.

## Procedure

1. Match existing table-test and fake-client patterns.
2. Test observable package behavior.
3. Use `httptest`, fake interfaces, or injected executors instead of real external systems.
4. Cover errors, edge cases, and compatibility parsing when relevant.
5. Keep tests deterministic and isolated.

## Validation

Run the targeted package test and `go test ./...`; run formatting check when Go files changed.

## Expected Output

Report covered behavior, changed test files, validation results, and remaining coverage gaps if material.

## Constraints

Do not update snapshots blindly, skip tests without reason, depend on live WebGuard Core, or require external network access unless explicitly approved.

## Completion Criteria

Tests fail for the wrong behavior where feasible, pass after the change, and remain deterministic.
