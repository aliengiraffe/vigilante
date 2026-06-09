---
name: vigilante-issue-implementation-on-bazel-monorepo
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Bazel-based monorepo. Use the provided worktree, respect repository instructions, comment on the issue as work progresses, and report failures back to GitHub.
---

# Vigilante Bazel Monorepo Issue Implementation

## Overview
Implement one GitHub issue from Vigilante dispatch through validated code changes, a pushed branch, and an opened pull request from the provided worktree. Always work inside the assigned worktree, respect repository instructions, and keep the GitHub issue updated with start, plan, progress, PR, and failure comments.

## Bazel Focus
- Read the repo/process context supplied in the prompt before changing code, especially Bazel markers and package-root hints.
- Work in terms of Bazel packages and targets rather than generic workspace or repo-wide commands.
- Choose the smallest explainable Bazel target scope that covers the files you touched, then widen only if validation or dependency structure requires it.
- Log which Bazel target or package scope you selected and why.
- Avoid blanket repo-wide Bazel validation unless it is truly required to prove the change.

## Workflow
1. Inspect issue and repository constraints
- Read the issue details supplied by Vigilante and confirm the issue scope before coding.
- Read development constraints from repository markdown files before making changes:
  - `AGENTS.md` when present
  - `README.md`
  - Bazel docs, developer docs, and package-local docs that affect the touched targets
- If repository instructions conflict, follow the more specific instruction.

2. Announce session start on GitHub
- Post a comment on the issue as soon as work begins using `vigilante gh issue comment`.
- Include that Vigilante launched the session, the working branch, and that implementation is in progress.

3. Post an implementation plan early
- After inspecting the issue and repository constraints, post a concise implementation plan to the issue using `vigilante gh issue comment`.
- The plan comment should describe the intended development steps before substantial coding work begins.

4. Detect a stacked base branch
- Scan the issue body for an explicit, single-line directive of the form `Base branch: <branch-name>` (label is case-insensitive; `<branch-name>` is trimmed and may be surrounded by backticks).
- Only honor the directive when it appears as its own top-level line in the issue body. Do not infer a stacked base from prose or sub-issue links.
- If no directive is present, branch from and target the watch target's base branch as today.
- When the directive is present:
  - Run `git fetch origin <base>` inside the assigned worktree before code changes.
  - If the fetch fails because the branch does not exist on the remote, stop work, post a failure comment on the issue, and do not silently fall back to the default branch.
  - Re-root the work branch onto `origin/<base>` with `git reset --hard origin/<base>` (no commits yet) or `git rebase origin/<base>` (commits present).
  - Use `<base>` as the PR target later and mention it in the implementation-plan comment.

5. Implement inside the assigned worktree only
- Use only the provided worktree path.
- Never edit the root checkout when a worktree was assigned.
- Keep changes scoped to the Bazel packages and source files required for the issue.
- Prefer repo-native Bazel commands such as `bazel test`, `bazel build`, `bazel run`, or documented wrappers.
- If the affected app or test flow needs local database services, invoke `docker-compose-launch` when the repository expects it, or call the bundled `vigilante-local-service-dependencies` skill before inventing ad hoc setup.

6. Validate incrementally
- Start with the smallest relevant Bazel target, package pattern, or documented wrapper command for the touched area.
- Expand to closely related targets only when the first target scope is insufficient or shared code requires it.
- If validation fails, first inspect the per-issue session log with `vigilante logs --repo <owner/name> --issue <n>` to determine whether the problem is in the code, target selection, test setup, or environment before retrying.

7. Commit, push, and open a pull request
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Commit only issue-relevant changes in the assigned branch.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Push the assigned branch to the remote.
- Open a pull request targeting the repository default branch unless the issue specified a stacked base branch in step 4 or other repository instructions say otherwise.
- When a stacked base branch was specified, pass `--base <base>` to `vigilante gh pr create` and state in the PR body and the PR-opened issue comment that the PR is stacked on `<base>`.

8. Report progress and failures clearly
- Use `vigilante gh issue comment` for progress updates, milestone updates, PR creation, and execution failures.
- If execution is blocked, validation fails, or a resumed session is unclear, inspect `vigilante logs --repo <owner/name> --issue <n>` before retrying or reporting the blocker.
- A `Base branch:` directive that points at a branch missing from the remote is a blocker: comment a failure on the issue and stop instead of falling back to the default branch.
- Keep comments concise, factual, and tied to real progress.
