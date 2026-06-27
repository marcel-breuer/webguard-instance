# Shared Agent Skills

Read the applicable `AGENTS.md` first, identify relevant skills, then read only those skills. `AGENTS.md` remains authoritative. If instructions conflict, report the conflict and apply the stricter or safer rule.

| Skill | Purpose | Use when | File |
| --- | --- | --- | --- |
| Implement Feature | Add behavior to the Go worker | Adding monitoring, CLI, health, config, or integration behavior | `implement-feature/SKILL.md` |
| Fix Bug | Correct broken behavior | Reproducing and fixing a defect | `fix-bug/SKILL.md` |
| Write Tests | Add or improve tests | Coverage is missing or behavior needs regression protection | `write-tests/SKILL.md` |
| Refactor Code | Improve structure without behavior changes | Simplifying or reorganizing existing code | `refactor-code/SKILL.md` |
| Review Code | Review a diff or branch | Producing implementation, risk, and test findings | `review-code/SKILL.md` |
| Update Dependencies | Change Go modules | Adding, removing, or upgrading dependencies | `update-dependencies/SKILL.md` |
| API Change | Change HTTP/API contracts | Updating WebGuard Core client or health endpoint behavior | `api-change/SKILL.md` |
| Monitoring Runner Change | Change check execution | Updating response, SSL, DNS, ping, port, keyword, or domain checks | `monitoring-runner-change/SKILL.md` |
| Security Review | Assess security risk | Reviewing auth, secrets, input handling, external calls, or command execution | `security-review/SKILL.md` |
| Documentation Change | Update docs only | Changing README, examples, or governance docs | `documentation-change/SKILL.md` |
| CI/CD Change | Update automation | Changing GitHub Actions or release automation | `ci-cd-change/SKILL.md` |
| Docker Change | Update container behavior | Changing Dockerfile, Compose, healthchecks, or runtime image behavior | `docker-change/SKILL.md` |
