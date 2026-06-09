---
name: vigilante-issue-implementation
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a watched repository. Use the provided worktree, respect repository instructions, comment on the issue as work progresses, and report failures back to GitHub.
---

# Vigilante Issue Implementation

## Overview
Implement one GitHub issue from Vigilante dispatch through validated code changes, a pushed branch, and an opened pull request from the provided worktree. Always work inside the assigned worktree, always respect repository instructions, and always keep the GitHub issue updated with start, plan, progress, PR, and failure comments.

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
  - other root or area-specific docs that affect touched files
- If repository instructions conflict, follow the more specific instruction.

2. Announce session start on GitHub
- Post a comment on the issue as soon as work begins using `vigilante gh issue comment`.
- Include that Vigilante launched the session, the working branch, and that implementation is in progress.

3. Post an implementation plan early
- After inspecting the issue and repository constraints, post a concise implementation plan to the issue using `vigilante gh issue comment`.
- The plan comment should describe the intended development steps before substantial code changes begin.
- Keep the plan concrete and short so readers can understand what will happen next.

4. Detect a stacked base branch
- Before implementing, scan the issue body for an explicit, single-line directive of the form `Base branch: <branch-name>` (label is case-insensitive; `<branch-name>` is trimmed and may be surrounded by backticks).
- Only honor the directive when it appears as its own top-level line in the issue body. Do not infer a stacked base from prose, linked issue numbers, native sub-issue relationships, or branch names mentioned elsewhere in the issue.
- If no such directive is present, follow the default workflow: branch from and target the watch target's base branch as today.
- If the directive is present:
  - Run `git fetch origin <base>` from inside the assigned worktree before any other code changes.
  - If `git fetch origin <base>` fails because the branch does not exist on the remote, stop work, post a failure comment on the issue explaining that the specified stacked base branch is missing on the remote, and do not silently fall back to the default branch.
  - When the fetch succeeds, re-root the work branch onto `origin/<base>` with either `git reset --hard origin/<base>` (preferred when the assigned branch has no commits yet) or `git rebase origin/<base>` (when commits already exist on the work branch).
  - Use `<base>` instead of the repository default branch when opening the pull request later in the workflow.
  - Mention the stacked base branch in the implementation-plan comment so reviewers see it before code changes start.

5. Implement inside the assigned worktree only
- Use only the provided worktree path.
- Never edit the root checkout when a worktree was assigned.
- Keep changes scoped to the issue.
- Prefer native repository tooling and avoid unnecessary new dependencies.
- Preserve existing coding patterns unless the issue requires a different approach.

Service dependencies:
- If app startup, migrations, or tests need local services, call the bundled `vigilante-local-service-dependencies` skill before inventing ad hoc setup steps.
- Prefer the skill's repository-native path first, and use its structured output to decide which env vars, commands, or cleanup steps the rest of the implementation should use.

6. Validate incrementally
- Run relevant tests, builds, or linters for the changed area before concluding work.
- Prefer targeted validation first, then broader validation when necessary.
- If a command fails, first inspect the per-issue session log with `vigilante logs --repo <owner/name> --issue <n>` to determine whether the problem is in the code, test setup, or environment before retrying.

7. Commit and push the branch
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Commit only issue-relevant changes in the assigned branch.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Push the assigned branch to the remote with `vigilante git push`.
- Do not leave completed implementation work only in the local worktree.

8. Open a pull request
- Always create a pull request for the completed change set.
- Target the repository default branch unless the issue specified a stacked base branch in step 4 or other repository instructions say otherwise.
- When a stacked base branch was specified, pass `--base <base>` to `vigilante gh pr create` and state in the PR body that the PR is stacked on `<base>` instead of the repository default branch.
- Include `Closes #<issue-number>` in the PR body as a required invariant.
- Include concise validation notes in the PR description.

9. Post progress comments at meaningful milestones
- Use `vigilante gh issue comment` for progress updates.
- Comment when investigation is complete and implementation starts.
- Comment when major milestones are reached, such as a core fix landing or tests passing.
- Comment when the branch has been pushed and the PR has been opened.
- When the PR is opened on a stacked base branch, the PR-opened comment must state which branch the PR is stacked on.
- Keep comments concise and factual.
- Do not spam the issue with low-signal updates.

10. Handle failures and blockers explicitly
- If tool setup fails, validation fails, the provider stops unexpectedly, a resumed session still looks unclear, or the issue is otherwise blocked, first inspect the per-issue session log with `vigilante logs --repo <owner/name> --issue <n>`.
- After checking the log, comment on the issue with the concrete problem using `vigilante gh issue comment`.
- Use `vigilante logs` for targeted local triage only when work is blocked or failing; do not turn it into routine log scraping on successful runs.
- Include enough detail for a human maintainer to understand the current state and next step.
- If work cannot proceed safely, stop and report the blocker instead of guessing.
- A `Base branch:` directive that points at a branch missing from the remote is one of these blockers: comment a failure on the issue and stop instead of falling back to the default branch.

11. Finish with a clear terminal state
- Leave the worktree in a coherent state.
- Ensure any executed validations are accurately reported.
- If the task completed successfully, summarize what changed, what was validated, and which PR was opened.
- If the task failed, summarize the failure clearly in the issue comment.

## GitHub Commenting Rules
- Use `vigilante gh issue comment` for all issue updates.
- Always comment when the session starts.
- For the coding-agent start comment, use a distinct launch title such as `## 🕹️ Coding Agent Launched: Codex` instead of a generic `Session Start` header.
- Always add a short implementation plan comment before substantial coding work begins.
- Add progress comments for non-trivial implementations as milestones are reached.
- Comment when the PR is opened.
- Comment immediately on any execution failure or blocking condition.
- When a failure or blocker is unclear locally, inspect `vigilante logs --repo <owner/name> --issue <n>` before reporting or retrying.
- Comments should be concise, concrete, and tied to real progress.
- Avoid generic status text that does not help the issue reader.

## Guardrails
- Never work outside the assigned worktree.
- Never ignore `AGENTS.md` or repository documentation that constrains implementation.
- Never make unrelated refactors unless they are required to complete the issue safely.
- Never rewrite commit authorship, committer identity, signing configuration, or commit trailers to attribute work to the coding agent.
- Never silently fail; report errors or blockers back to the issue.
- Never claim validation passed unless the corresponding command actually succeeded.
- Never stop at local-only code changes when the task is complete; push the branch and create the PR.

## Completion Criteria
- The issue received a start comment.
- The issue received a plan comment describing the intended development steps.
- Progress or failure comments were posted as appropriate for the work performed.
- Code changes are scoped to the issue and live in the assigned worktree.
- Relevant validation was run and accurately reported.
- The branch was pushed to the remote.
- A pull request was opened for the change.
- Final session state is clear to both Vigilante and the GitHub issue reader.

## Output Expectations
When using this skill, the agent should leave:

- code changes in the assigned worktree
- a pushed branch containing those changes
- an opened pull request for those changes
- a clear issue comment trail produced through `vigilante gh issue comment`
- accurate success or failure reporting
