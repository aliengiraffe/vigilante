---
name: vigilante-issue-implementation-on-gradle
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Gradle monorepo.
---

# Vigilante Gradle Monorepo Issue Implementation

## Focus
- Read the prompt for the detected stack, workspace hints, and shared local-service contract before changing code.
- Limit edits and validation to the affected Gradle projects unless broader changes are required.
- If local services are required, call `vigilante-local-service-dependencies` first and then use the shared `docker-compose-launch` contract instead of writing ad hoc Compose logic.

## Workflow
- Follow the base `vigilante-issue-implementation-on-monorepo` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for every commit, amend, rebase rewrite, or conflict-resolution commit. Do not use `git commit`, GitHub CLI commit flows, or other direct commit commands.
- `vigilante commit` must preserve the user's existing git author, committer, and signing configuration so commits remain user-authored and signed according to the user's git configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Use Gradle-native project commands when they exist and match the touched modules.
- Keep service startup scoped to the assigned worktree and only for implementation or test dependencies.
