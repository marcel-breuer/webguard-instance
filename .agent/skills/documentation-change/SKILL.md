# Skill: Documentation Change

## Purpose

Update repository documentation accurately and concisely.

## When to Use

Use for README, `.env.example` explanations, governance files, skill files, or operational documentation.

## When Not to Use

Do not use when code behavior changes require implementation skills first.

## Required Context

Read `AGENTS.md`, the documentation being changed, and source/config files needed to verify claims.

## Relevant Project Areas

`README.md`, `.env.example`, `AGENTS.md`, `.agent/skills/`, adapter files, workflow or Docker files referenced by docs.

## Procedure

1. Verify every technical statement against repository files.
2. Document only existing commands and behavior unless the same task adds them.
3. Keep documentation concise and task-focused.
4. Avoid duplicating canonical rules across adapter files.
5. Check for stale command references.

## Validation

Run `git diff --check`; run compose validation when docs reference Docker behavior.

## Expected Output

Report changed docs, validation, and any verified documentation gaps left unresolved.

## Constraints

Do not edit application code, dependencies, CI, or Docker files for documentation-only tasks unless explicitly requested.

## Completion Criteria

Documentation is accurate, concise, and aligned with current repository behavior.
