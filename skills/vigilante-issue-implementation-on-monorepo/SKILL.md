---
name: vigilante-issue-implementation-on-monorepo
description: Implement a GitHub issue end-to-end for a Vigilante-dispatched monorepo. Use the provided worktree, keep changes scoped to the affected package or app, comment on the issue as work progresses, and report failures back to GitHub.
---

# Vigilante Issue Implementation On Monorepo

## Overview
Implement one GitHub issue from Vigilante dispatch through validated code changes, a pushed branch, and an opened pull request from the provided worktree. Always work inside the assigned worktree, keep changes focused on the affected app or package, respect repository instructions, and keep the GitHub issue updated with start, plan, progress, PR, and failure comments.

## Workflow
1. Inspect issue and repository constraints
- Read the issue details supplied by Vigilante and confirm the issue scope before coding.
- Read `AGENTS.md`, `README.md`, and any touched-area docs before making changes.
- Use the repo classification and process hints in the prompt to identify the most likely app/package boundary before editing.

2. Announce session start and plan on GitHub
- Post a start comment with `gh issue comment` as soon as work begins.
- Post a concise implementation plan before substantial coding.

3. Implement inside the assigned worktree only
- Use only the provided worktree path.
- Keep changes scoped to the relevant app/package/workspace unless the issue requires a broader cross-cutting fix.
- Prefer existing monorepo tooling and scripts instead of inventing new entrypoints.

4. Validate incrementally
- Run the narrowest relevant checks first for the touched package/app, then broader workspace validation only when needed.
- If the monorepo provides filtered or targeted commands, prefer those over whole-repo commands.

5. Finish the issue lifecycle
- Push the assigned branch.
- Open a pull request against the default branch unless repo instructions say otherwise.
- Comment on meaningful milestones and report any blocker or validation failure back to the issue.

## Guardrails
- Never work outside the assigned worktree.
- Never ignore repository instructions or area-specific docs.
- Never broaden the change beyond the issue without a concrete reason.
- Never claim validation passed unless the corresponding command actually succeeded.

## Output Expectations
- code changes in the assigned worktree
- a pushed branch containing those changes
- an opened pull request for those changes
- a clear issue comment trail produced through `gh issue comment`
- accurate success or failure reporting
