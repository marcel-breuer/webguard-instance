# Skill: Refactor Code

## Purpose

Improve code structure without changing observable behavior.

## When to Use

Use for scoped simplification, extracting duplication, improving testability, or aligning code with existing package responsibilities.

## When Not to Use

Do not use when behavior must change, tests are missing for risky behavior, or the task only needs documentation.

## Required Context

Read `AGENTS.md`, the affected package, tests covering the current behavior, and nearby usage sites.

## Relevant Project Areas

Go code under `cmd/` and `internal/`.

## Procedure

1. Define the behavior that must remain unchanged.
2. Prefer small mechanical changes.
3. Keep package boundaries intact.
4. Add tests before refactoring if existing coverage is insufficient.
5. Review the diff for accidental contract or log changes.

## Validation

Run formatting, targeted tests, and `go test ./...`; run `go vet ./...` for nontrivial refactors.

## Expected Output

Report the structural change, unchanged behavior, validations, and any coverage limitations.

## Constraints

Do not introduce speculative abstractions, rename public API casually, or mix refactoring with unrelated feature work.

## Completion Criteria

Behavior is preserved, the code is simpler or better isolated, and relevant validation passes.
