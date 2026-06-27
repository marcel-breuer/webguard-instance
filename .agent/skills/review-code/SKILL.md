# Skill: Review Code

## Purpose

Review a change for correctness, security, maintainability, and test coverage.

## When to Use

Use when asked to review a diff, branch, pull request, or local changes.

## When Not to Use

Do not use when the task is to implement changes directly unless review is also requested.

## Required Context

Read `AGENTS.md`, the diff, affected code, and relevant tests. Prefer targeted inspection over full repository scans.

## Relevant Project Areas

Any changed files, plus package owners and validation configuration that affect the change.

## Procedure

1. Identify behavior, security, and compatibility risks first.
2. Check tests for meaningful coverage.
3. Verify Docker, CI, or dependency changes against repository workflows.
4. Prioritize actionable findings with file and line references.
5. Avoid style-only findings unless they affect maintainability or configured checks.

## Validation

Run or inspect relevant commands only when useful for the review.

## Expected Output

Lead with findings ordered by severity, then open questions, then a brief summary if needed.

## Constraints

Do not include large code excerpts or duplicate findings. Do not invent requirements.

## Completion Criteria

Findings are specific, actionable, supported by repository context, and note missing validation or residual risk.
