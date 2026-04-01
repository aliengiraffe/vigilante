---
name: vigilante-issue-implementation-on-github-actions
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a repository with GitHub Actions workflows, applying workflow hardening, pinned actions, and secret-safe automation practices.
---

# Vigilante GitHub Actions Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and security guidance before changing workflow files.
- Keep changes scoped to the issue and do not broaden into unrelated workflow or repository changes.
- Treat `.github/workflows/` as a security-sensitive surface. Every workflow edit should consider permissions, secret exposure, and supply-chain risk.

## Workflow File Conventions
- Use `.yml` or `.yaml` consistently with the repository's existing convention. Do not mix extensions within the same repository.
- Validate workflow syntax before committing. Use `actionlint` when it is available in the repository or installed locally. If `actionlint` is not available, note its absence and continue — do not fabricate output.
- Keep workflow files readable: use clear job and step names, add inline comments for non-obvious logic, and prefer reusable workflows or composite actions over duplicated step blocks.

## Pinned Actions
- Pin third-party actions to full commit SHAs, not mutable tags or branch references. Example: `uses: actions/checkout@<full-sha>` with a trailing version comment.
- When updating an action version, verify the new SHA corresponds to a reviewed release or tag.
- First-party GitHub actions (`actions/*`) should also be pinned to SHAs for consistency and supply-chain safety.
- When adding a new third-party action, prefer well-maintained actions with high community adoption. Avoid actions that request broad permissions or lack clear provenance.

## Least-Privilege Permissions
- Always declare a top-level `permissions:` block in workflow files. Default to the most restrictive set needed.
- Use read-only `contents: read` unless the workflow must write (e.g., creating releases, pushing tags, commenting on PRs).
- Scope token permissions per job when different jobs need different access levels.
- Never use `permissions: write-all` or leave permissions unspecified, which defaults to broad access in some repository configurations.

## Secret and Credential Safety
- Never echo, log, or interpolate secrets directly in `run:` shell commands. Pass secrets through environment variables.
- Use `::add-mask::` to mask dynamic values that may appear in logs.
- Prefer OIDC-based authentication (e.g., `aws-actions/configure-aws-credentials` with `role-to-assume`) over long-lived cloud credentials stored as repository secrets.
- Do not store secrets, tokens, or credentials in workflow files or committed configuration.
- When a workflow needs elevated access, document why in a comment and scope the access as narrowly as possible.

## Safe Workflow Authoring
- Never interpolate untrusted event data (such as `${{ github.event.pull_request.title }}` or `${{ github.event.issue.body }}`) directly into `run:` shell scripts. Use an intermediate environment variable to prevent script injection.
- Prefer `pull_request` over `pull_request_target` unless cross-fork access is explicitly required and the workflow is hardened against injection.
- Use `concurrency` groups to prevent redundant or conflicting workflow runs.
- Set appropriate `timeout-minutes` on jobs to prevent hung runners from consuming resources.

## Reusable Workflows and Composite Actions
- Prefer the repository's existing reusable workflows and composite actions over duplicating logic.
- When creating new reusable workflows, define clear `inputs` and `secrets` contracts.
- Respect the repository's branch-protection rules and required status checks when adding or modifying workflows.

## Mixed-Stack Repositories
- A repository with GitHub Actions workflows often also contains application code in Go, Node.js, Python, or other languages.
- Scope workflow-specific guidance to `.github/workflows/` and related CI/CD configuration only. Do not apply workflow linting or hardening rules to application source code.
- When an issue touches both workflow files and application code, validate each side with its appropriate toolchain.
- Check the prompt for additional detected tech stacks and follow their respective guidance for non-workflow changes.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic GitHub Actions guidance in this skill.
