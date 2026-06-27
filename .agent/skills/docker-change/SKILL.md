# Skill: Docker Change

## Purpose

Change container build or Compose runtime behavior safely.

## When to Use

Use for `Dockerfile`, `compose.yml`, `docker-compose.override.yml`, `.dockerignore`, healthchecks, ports, images, or container commands.

## When Not to Use

Do not use for Go code changes unless container behavior also changes.

## Required Context

Read `AGENTS.md`, Docker files, README Docker commands, and CI image-build steps.

## Relevant Project Areas

`Dockerfile`, `compose.yml`, `docker-compose.override.yml`, `.dockerignore`, `start-dev.sh`, `.github/workflows/ci.yml`, `.github/workflows/docker-image.yml`.

## Procedure

1. Preserve development, builder, and production target separation.
2. Keep production image minimal and non-root unless explicitly changed.
3. Keep healthchecks aligned with `/health`.
4. Preserve Compose environment and port behavior unless the task requires changes.
5. Avoid baking secrets or local `.env` values into images.

## Validation

Run compose validation and production image build; run Go tests if Docker changes affect build or runtime behavior.

## Expected Output

Report container behavior changed, validations, and operational risks.

## Constraints

Do not expose new ports, alter publish targets, change base images, or weaken healthchecks without a concrete reason.

## Completion Criteria

Docker behavior is validated, compatible with CI, and documented if user-facing.
