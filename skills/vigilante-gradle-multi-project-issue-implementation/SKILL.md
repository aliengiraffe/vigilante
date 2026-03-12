---
name: vigilante-gradle-multi-project-issue-implementation
description: Implement a GitHub issue end-to-end for Gradle multi-project repositories dispatched by Vigilante. Detect the relevant subproject scope, use repo-defined Gradle tasks, start required database services with docker-compose-launch, and keep the issue updated with progress.
---

# Vigilante Gradle Multi-Project Issue Implementation

## Overview
Implement one GitHub issue from Vigilante dispatch through validated code changes, a pushed branch, and an opened pull request from the provided worktree. This skill is specific to Gradle multi-project repositories and should avoid JS workspace assumptions.

## Workflow
1. Confirm scope from the issue and repository docs
- Read the issue text first and treat it as the source of truth.
- Read `AGENTS.md`, `README.md`, and any area-specific docs that constrain the touched subprojects.

2. Map the Gradle project shape before coding
- Inspect `settings.gradle` or `settings.gradle.kts` to identify included subprojects.
- Identify the smallest relevant Gradle subproject set for the issue and keep implementation scoped there unless shared build logic requires a broader change.
- Log the selected subproject(s) and the Gradle task scope you plan to use in your progress updates.

3. Use repo-defined Gradle commands
- Prefer checked-in Gradle wrappers and existing repo tasks over guessed commands.
- Run targeted Gradle tasks for the affected subproject(s) when possible, such as `:service:test` or `:app:check`, instead of whole-repo builds.
- Only widen validation scope when the repo structure or changed files require it.

4. Start required local services when tests or boot flows need them
- If the affected service relies on local databases such as Postgres or MySQL, invoke `docker-compose-launch` before running the dependent validation or manual verification flow.
- Use the repository's documented service profile or compose command instead of inventing one.

5. Keep the standard Vigilante delivery contract
- Comment on the GitHub issue when work starts, when the plan is ready, at meaningful milestones, on failures, and when the pull request is opened.
- Push the assigned branch and open a pull request when implementation and validation are complete.

## Guardrails
- Stay inside the provided worktree.
- Keep validation scoped to the relevant Gradle subproject(s) whenever possible.
- Do not assume Android, JVM, or module naming conventions beyond what the repository already defines.
- Do not skip `docker-compose-launch` when the repository depends on local database services for the touched flow.
