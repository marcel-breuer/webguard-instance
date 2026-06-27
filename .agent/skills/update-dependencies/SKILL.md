# Skill: Update Dependencies

## Purpose

Add, remove, or update Go module dependencies safely.

## When to Use

Use for changes to `go.mod` or `go.sum`.

## When Not to Use

Do not use for ordinary code changes that do not affect dependencies.

## Required Context

Read `AGENTS.md`, `go.mod`, relevant imports, and tests for code using the dependency.

## Relevant Project Areas

`go.mod`, `go.sum`, Go packages importing the dependency, Docker build behavior.

## Procedure

1. Confirm the dependency is necessary and not covered by the standard library or existing packages.
2. Check maintenance, license, security posture, size, and transitive dependencies.
3. Make only the requested dependency changes.
4. Keep lockfile changes tied to dependency changes.
5. Update code and tests that rely on the dependency.

## Validation

Run dependency download, `go test ./...`, relevant build, and production image build when dependency changes can affect containers.

## Expected Output

Report dependency changes, reason, validations, and any security or maintenance risk.

## Constraints

No unrelated upgrades, duplicate libraries, unrequested major-version changes, or lockfile churn.

## Completion Criteria

Dependencies are justified, module files are consistent, and validation passes.
