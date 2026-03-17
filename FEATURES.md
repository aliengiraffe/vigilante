# Vigilante Features

Vigilante is the GitHub-native control plane for autonomous software delivery. It watches repositories, selects executable issues, prepares isolated implementation environments, dispatches headless coding agents with repo-aware workflow skills, maintains pull requests, and should evolve into the orchestration layer that also drives deployment promotion and release operations through GitHub.

## Status Legend

- `Implemented`: present in the repository today
- `Partial`: present in a meaningful but incomplete form
- `Target`: intended product capability not yet fully implemented

## Operating Model

Vigilante is not the code-generating model itself. It owns orchestration. Coding-agent CLIs such as Codex, Claude Code, and Gemini act as interchangeable execution engines inside Vigilante-managed worktrees and under Vigilante-managed lifecycle rules.

GitHub is the primary operator surface:

- Issues are the work queue.
- Labels, assignees, and issue comments influence routing and control.
- Pull requests are the review and merge unit.
- Issue comments provide the execution audit trail.
- CI and deployment status should flow back into the same GitHub-centered loop.

## Feature Modules

### 1. Repository Onboarding and Classification
Status: `Implemented`

Vigilante can register local repositories for continuous monitoring and determine how they should be handled.

Key capabilities:

- Register and unregister watched repositories.
- Resolve absolute local paths and discover the GitHub `owner/name` remote.
- Detect the effective default branch.
- Classify repositories by shape:
  - traditional repository
  - generic monorepo
  - Gradle multi-project repository
- Detect monorepo stacks and process hints for:
  - Turborepo
  - Nx
  - Rush
  - Bazel
  - Gradle
- Persist watch configuration, provider selection, labels, assignee filters, and parallelism limits.

Why it matters:

This gives Vigilante enough repository context to route work automatically instead of treating every issue as a generic prompt.

### 2. Issue Intake and Dispatch Control
Status: `Implemented`

Vigilante continuously scans watched repositories and selects issues that are eligible for automated work.

Key capabilities:

- Poll open GitHub issues via `gh`.
- Resolve the `me` assignee dynamically at runtime.
- Filter by assignee and optional label allowlists.
- Support provider overrides through issue labels.
- Prevent duplicate dispatch when an issue already has an active or blocked local session.
- Respect per-repository max parallel session limits.
- Skip conflicting or ineligible issues instead of dispatching blindly.

Why it matters:

This is the scheduling layer that turns GitHub issue queues into a controlled, multi-repo worker system.

### 3. Isolated Execution Environments
Status: `Implemented`

Each issue runs inside a dedicated git worktree with predictable branch semantics.

Key capabilities:

- Create per-issue worktrees under `.worktrees/vigilante/`.
- Generate deterministic issue branch names from issue number and title.
- Reuse an existing remote issue branch when one already exists.
- Analyze reused branch diffs against the repository base branch.
- Clean up worktrees and local branches safely.
- Redispatch an issue into a fresh worktree without deleting remote PRs or branches.

Why it matters:

Isolation is the boundary that lets Vigilante run unattended while keeping the primary checkout stable.

### 4. Agent Runtime Orchestration
Status: `Implemented`

Vigilante abstracts over multiple coding-agent CLIs while keeping a consistent lifecycle.

Key capabilities:

- Provider abstraction for Codex, Claude Code, and Gemini.
- Runtime compatibility checks with supported version contracts.
- Provider-specific invocation builders for:
  - issue preflight
  - issue implementation
  - conflict resolution
- Shared prompt assembly with repo context, issue context, and operational instructions.
- Skill installation into provider-specific runtime homes during setup.

Why it matters:

The orchestration layer stays stable while agent runtimes can change independently.

### 5. Repo-Aware Execution Playbooks
Status: `Implemented`

Vigilante ships workflow skills that adapt execution behavior to repository topology and operational needs.

Key capabilities:

- Base issue-implementation workflow for standard repositories.
- Monorepo-specific workflow guidance.
- Specialized workflows for:
  - Turborepo
  - Nx
  - Rush
  - Bazel
  - Gradle monorepos
  - Gradle multi-project repositories
- Dedicated conflict-resolution workflow for rebases and merge conflicts.
- Local service dependency workflow for repository-native service startup.
- Docker Compose helper workflow for worktree-scoped local services.
- Standardized expectations for issue comments, validation, branch push, and PR creation.

Why it matters:

This is how Vigilante becomes more than a generic scheduler. Skills encode execution policy so the orchestration core remains modular.

### 6. GitHub Collaboration Layer
Status: `Partial`

Vigilante already treats GitHub as the human-visible execution log and control surface, but not every desired operator flow is implemented yet.

Key capabilities today:

- Post structured issue comments for:
  - session start
  - blocked states
  - recovery events
  - cleanup results
  - maintenance outcomes
- Poll issue comments to detect operator commands.
- Support GitHub-driven resume via:
  - `@vigilanteai resume`
  - `resume` or `vigilante:resume` labels
- Support GitHub-driven cleanup via:
  - `@vigilanteai cleanup`
- Add reactions to acknowledge GitHub-issued commands.

Target additions:

- richer PR-linked status summaries
- explicit deployment status reporting back to issues and PRs
- release and environment notifications in GitHub
- broader operator command vocabulary for reruns, promotions, rollbacks, and incident handling

Why it matters:

GitHub should remain the single place where humans can observe and steer autonomous execution.

### 7. Session State, Recovery, and Operator Controls
Status: `Implemented`

Vigilante persists orchestration state locally and uses it to recover from failures without losing context.

Key capabilities:

- Persistent state store for watch targets, sessions, config, and logs.
- Session lifecycle tracking:
  - running
  - blocked
  - resuming
  - success
  - failed
- Blocked-reason classification for provider, auth, git, GitHub, quota, validation, and runtime failures.
- CLI controls for `list`, `cleanup`, `resume`, and `redispatch`.
- Detection and cleanup of stalled sessions.
- Inactivity timeout cleanup for long-blocked sessions.
- Suppression of duplicate failure commentary for repeated resume failures.

Why it matters:

Autonomous orchestration only works if the control plane can distinguish transient failure, operator action required, and safe recovery.

### 8. Pull Request Maintenance
Status: `Partial`

Vigilante can already track PRs and keep them moving, but the maintenance loop is currently opinionated and limited.

Key capabilities today:

- Discover PRs for issue branches.
- Track PR number, URL, state, and merge time in session state.
- Rebase open PR branches onto `origin/main`.
- Detect rebase conflicts and dispatch the conflict-resolution workflow.
- Rerun `go test ./...` after successful rebases.
- Force-push rebased branches.
- Support squash automerge when the PR carries an `automerge` label and GitHub mergeability conditions are met.
- Stop monitoring closed, non-merged PRs.
- Clean up merged worktrees and local branches.

Target additions:

- base-branch awareness beyond `main`
- repository-specific validation commands instead of the current Go-centric maintenance path
- required-check introspection that can drive maintenance decisions more precisely
- PR review remediation and post-review task handling

Why it matters:

Implementation is only half the job. Vigilante should own the merge-readiness loop until the change lands.

### 9. CI/CD and Deployment Orchestration
Status: `Target`

Vigilante should evolve from code-and-PR orchestration into delivery orchestration.

Target capabilities:

- Detect repository CI/CD topology from GitHub Actions and repository conventions.
- Monitor required checks and deployment workflows as first-class orchestration signals.
- Trigger or coordinate staging deployments after implementation and PR validation.
- Support production promotion after merge, approval, tag, or release conditions.
- Report deployment progress, success, and failure back to GitHub issues and PRs.
- Expose GitHub-driven controls for redeploy, promote, pause, and rollback.
- Preserve environment-specific guardrails and approval requirements.

Why it matters:

A fully managed GitHub workflow does not stop at opening a pull request. Vigilante should own the handoff from merged code to running software.

### 10. Release and Environment Management
Status: `Target`

Deployment orchestration requires first-class release and environment concepts.

Target capabilities:

- Model environments such as development, staging, preview, and production.
- Track what version or commit is deployed where.
- Coordinate promotion policies between environments.
- Surface release notes, tags, and change windows through GitHub.
- Integrate secrets/config requirements into deployment readiness checks.
- Retain deployment history and rollback points.

Why it matters:

Without environment awareness, Vigilante cannot be the end-to-end orchestrator for software delivery.

### 11. Observability, Audit, and Governance
Status: `Partial`

Vigilante already records local state and logs, but broader governance is still a target area.

Key capabilities today:

- Daemon log stream and per-issue session logs.
- Local session history with timestamps and failure state.
- GitHub issue comments as a human-readable audit trail.
- Single-scan locking to prevent overlapping daemon scans.
- Daemon installation on macOS and Linux user environments.

Target additions:

- richer metrics and operational dashboards
- policy controls for approval-sensitive operations
- clearer separation between autonomous actions and operator-approved actions
- deployment and release audit views

Why it matters:

A long-running autonomous worker needs traceability, control boundaries, and operator confidence.

### 12. Extensibility Model
Status: `Implemented`

Vigilante is designed so orchestration, runtime choice, and repo-specific execution behavior can evolve independently.

Key capabilities:

- Pluggable provider model for coding-agent CLIs.
- Bundled skill system as modular execution policy.
- Repo classification feeding skill selection automatically.
- Separate service-management skills from scheduling logic.
- Dedicated CLI and daemon surfaces over a shared orchestration core.

Why it matters:

This modularity is what allows Vigilante to expand from issue execution into full software delivery without becoming a monolith of special cases.

## GitHub-Driven Workflow

The intended end-to-end workflow for a fully managed repository is:

1. A GitHub issue is authored with enough behavioral detail to be executable.
2. Vigilante detects the issue in a watched repository and determines whether it is eligible for work.
3. Vigilante selects the correct provider, skill set, branch, and worktree.
4. The coding agent executes inside the assigned worktree and reports progress back to the GitHub issue.
5. The agent pushes the branch and opens a pull request.
6. Vigilante monitors and maintains the PR, including rebases, conflict handling, validation reruns, and merge readiness.
7. CI and deployment workflows report status back into GitHub.
8. Vigilante coordinates staging and production promotion according to repository policy.
9. After merge and successful promotion, Vigilante cleans up local artifacts and preserves the GitHub audit trail.

## Success Criteria For A Fully Managed Repository

Vigilante should be considered fully end-to-end for a repository when:

- GitHub issues can be turned into scoped implementation sessions without manual local setup.
- The correct execution workflow is chosen automatically from repository context.
- Progress, blockers, and recovery are visible in GitHub without requiring local log access.
- Pull requests are opened, maintained, and merged with minimal operator intervention.
- CI and deployment outcomes are part of the same orchestration loop.
- Operators can resume, clean up, redispatch, promote, or roll back through GitHub-native controls.
- Local worktrees and session state remain consistent even across crashes, stalls, or provider failures.

