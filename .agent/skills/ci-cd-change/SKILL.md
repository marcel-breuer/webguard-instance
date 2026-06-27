# Skill: CI/CD Change

## Purpose

Modify GitHub Actions or release automation safely.

## When to Use

Use for `.github/workflows/`, `.github/scripts/`, release notes, changelog automation, permissions, or container publishing changes.

## When Not to Use

Do not use for application-only, Docker-only, or documentation-only changes unless CI behavior also changes.

## Required Context

Read `AGENTS.md`, affected workflow or script, and commands the workflow runs.

## Relevant Project Areas

`.github/workflows/ci.yml`, `.github/workflows/docker-image.yml`, `.github/scripts/update-changelog.sh`, `Dockerfile`, Go validation commands.

## Procedure

1. Preserve least-privilege workflow permissions.
2. Keep CI steps aligned with local validation commands.
3. Avoid changing release or publish behavior unless explicitly required.
4. Validate shell syntax and command availability where possible.
5. Document any intentional change in required checks.

## Validation

Run local equivalents of changed workflow commands when possible, plus Docker or Go checks affected by the workflow.

## Expected Output

Report workflow changes, validation, and any CI-only assumptions.

## Constraints

Do not add secrets, broaden permissions, or change publish targets without explicit approval.

## Completion Criteria

CI/CD behavior is minimal, reviewable, and locally validated where possible.
