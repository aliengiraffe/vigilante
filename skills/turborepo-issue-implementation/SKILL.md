---
name: turborepo-issue-implementation
description: Implement a GitHub issue end-to-end for a pnpm/workspace-based Turborepo. Select the smallest relevant workspace scope, use workspace-aware commands, and start local database services when required.
---

# Turborepo Issue Implementation

## Overview
Implement one GitHub issue from Vigilante dispatch through validated code changes, a pushed branch, and an opened pull request from the provided worktree. This skill is for Turborepo repositories and should keep the work scoped to the smallest relevant app or package instead of defaulting to repo-wide commands.

## Inputs
Require these inputs from Vigilante:

- issue number
- issue title and URL
- repository slug
- local repository path
- assigned worktree path
- branch name

## Workflow
1. Inspect issue and repository constraints
- Read the issue details supplied by Vigilante and confirm the issue scope before coding.
- Read development constraints from repository markdown files before making changes:
  - `AGENTS.md` when present
  - `README.md`
  - app/package docs that affect the touched workspace
- Confirm the repository is a Turborepo by checking for `turbo.json` plus a workspace manifest such as `pnpm-workspace.yaml` or root `package.json` workspaces.

2. Announce session start on GitHub
- Post a comment on the issue as soon as work begins using `gh issue comment`.
- Include that Vigilante launched the session, the working branch, and that implementation is in progress.

3. Post an implementation plan early
- After inspecting the issue and repository constraints, post a concise implementation plan to the issue using `gh issue comment`.
- State which workspace or workspaces are likely in scope and why.

4. Choose the smallest relevant workspace scope
- Inspect common Turborepo locations such as `apps/*`, `packages/*`, and `services/*`.
- Use the issue title, body, touched files, package names, and repo scripts to decide which workspace is most relevant.
- Prefer one workspace when possible; include additional workspaces only when the issue clearly crosses boundaries.
- Surface the selected workspace(s) in progress comments or logs.

5. Implement inside the assigned worktree only
- Use only the provided worktree path.
- Keep changes scoped to the issue.
- Prefer existing repo scripts and task names over invented command sequences.

6. Run workspace-aware validation
- Prefer `pnpm` commands that target the selected workspace, such as filtered runs or package-scoped scripts.
- Common examples include `pnpm --filter <workspace> test`, `pnpm --filter <workspace> build`, and `pnpm --filter <workspace> lint` when those scripts exist.
- Use Turborepo tasks when the repo defines them and prefer scoped execution over whole-repo runs.
- Common examples include `turbo run build --filter <workspace>` and `turbo run test --filter <workspace>` when those tasks are defined by the repo.
- Run the smallest validation set that credibly covers the change: build, lint, test, typecheck, or app-specific verification as defined by the repo.
- If the selected workspace requires local database services for validation, run `docker-compose-launch` before the affected checks.

7. Commit, push, and open a pull request
- Commit only issue-relevant changes in the assigned branch.
- Push the assigned branch to the remote.
- Open a pull request against the repository default branch unless repository instructions say otherwise.
- Include concise validation notes and mention the selected workspace scope in the PR description.

8. Keep the issue updated
- Use `gh issue comment` for progress updates.
- Comment when workspace investigation is complete and implementation starts.
- Comment when major milestones are reached, such as the core fix landing or scoped validation passing.
- Comment when the branch has been pushed and the PR has been opened.

9. Handle failures and blockers explicitly
- If workspace selection is ambiguous, validation fails, or local services cannot be started, comment on the issue with the concrete blocker using `gh issue comment`.
- Include enough detail for a human maintainer to understand the current state and next step.

## Guardrails
- Never work outside the assigned worktree.
- Never ignore repository instructions.
- Never run repo-wide Turborepo tasks by default when a narrower workspace target is available.
- Never claim validation passed unless the corresponding command actually succeeded.
- Never skip `docker-compose-launch` when the selected workspace depends on local database services for validation.

## Completion Criteria
- The issue received a start comment.
- The issue received a plan comment describing the intended development steps and targeted workspace scope.
- Progress or failure comments were posted as appropriate.
- Relevant workspace-aware validation was run and accurately reported.
- The branch was pushed to the remote.
- A pull request was opened for the change.
