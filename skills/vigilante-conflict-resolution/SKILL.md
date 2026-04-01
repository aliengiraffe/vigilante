---
name: vigilante-conflict-resolution
description: Resolve rebase and merge conflicts for an already-open Vigilante pull request, keep the branch validated, and report progress back to GitHub.
---

# Vigilante Conflict Resolution

## Overview
Use this skill after Vigilante has already opened a pull request and a follow-up rebase onto `origin/main` hits conflicts. Work only inside the assigned worktree, resolve the conflicts with the smallest safe change, rerun validation, push the updated branch, and keep the linked GitHub issue informed.

## Workflow
1. Inspect the current rebase state in the assigned worktree and start the rebase onto the provided base branch if it has not already begun.
2. Read repository instructions that affect the touched files before editing.
3. Comment on the issue when conflict resolution begins and again for meaningful milestones or failures.
4. Resolve only the conflicts needed to complete the rebase cleanly, commit by commit, while preserving the original issue specification and branch intent.
5. Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
6. Preserve the user's existing git author, committer, and signing configuration for every rebase rewrite, amend, or conflict-resolution commit, and do not overwrite `git config` with a coding-agent identity.
7. Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
8. If the rebase fails, post-rebase validation fails, or the current session state is unclear, inspect `vigilante logs --repo <owner/name> --issue <n>` before retrying so the persisted session transcript becomes the factual source for the next safe action.
9. After inspecting the log, either continue with the smallest safe fix, rerun the requested validation, or report a blocker on the issue when the transcript shows the branch should not be retried automatically.
10. Push the updated branch back to GitHub after the rebase and requested validation succeed.
11. Report the final result clearly on the issue, including any remaining blocker if the conflicts cannot be resolved safely.

## Guardrails
- Stay inside the provided worktree.
- Do not broaden the change beyond the conflicting files unless required to restore build or test health.
- Keep the original issue behavior authoritative; do not drop commits or rewrite away issue scope just to remove conflicts.
- Do not rewrite commit authorship, committer identity, signing configuration, or commit trailers to attribute the branch history to the coding agent.
- Do not claim the branch is merge-ready unless the requested validation actually passed.
- Report blockers immediately with `vigilante gh issue comment`.
