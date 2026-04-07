# Planning

## Summary
This document captures the highest-priority root issues exposed by the recent sandbox execution failures and defines the remediation direction. The goal is to make sandboxed Vigilante sessions reliable enough to run full issue workflows end to end, not just start containers successfully.

## Root Issues

### 1. Broken worktree and git metadata inside the sandbox
- The mounted worktree entered the container with a `.git` file pointing to a host path that did not exist in the container.
- This caused normal repository operations such as `git status` to fail before implementation work even started.
- Result: preflight could not validate baseline state, and the coding agent had to attempt local repair work that should never be necessary.

### 2. Incomplete sandbox toolchain
- The sandbox image did not provide required repository tooling such as `go`, `task`, and `rg`.
- This prevented repository-native validation commands from running even when the repo clearly defined them.
- Result: preflight produced false-environment failures instead of meaningful validation results.

### 3. Broken GitHub integration inside the sandbox
- `vigilante gh ...` calls failed because the local GitHub proxy endpoint was unavailable from the sandboxed execution path.
- Direct `git push` fallback also failed because GitHub credentials were not usable in the sandbox.
- Result: the agent could modify and commit locally but could not report progress, push branches, or open pull requests.

### 4. Preflight failure did not stop the main execution
- Preflight correctly detected that baseline validation had failed.
- Vigilante still continued into the main issue implementation step instead of terminating the session as blocked or failed.
- Result: the session reported success despite running in a degraded environment and missing required GitHub-side completion steps.

## Priority Order

### Priority 1: Make the worktree valid inside the sandbox
- Define a deterministic strategy for sandbox git metadata.
- Avoid mounting a worktree whose `.git` indirection points outside the container unless that external path is also mounted and valid.
- Preferred outcomes:
  - either mount the backing git metadata needed by the worktree
  - or materialize a sandbox-safe checkout/worktree layout that is self-contained inside the container
- Add validation before session start so sandbox provisioning fails early when the mounted checkout is not a usable git repository.

### Priority 2: Make the sandbox image repository-capable
- Expand the sandbox image so it can run the validation and implementation commands expected by supported repositories.
- For the current Go-based Vigilante repo, that means at minimum:
  - `go`
  - `git`
  - `rg`
  - shell utilities already expected by repo workflows
- Decide whether `task` should be included directly or whether repository-native commands should avoid depending on it inside the sandbox.
- Add an explicit compatibility checklist for supported stack/tool combinations so sandbox sessions fail with a clear reason when the image does not meet repo requirements.

### Priority 3: Restore GitHub and push/PR operations inside the sandbox
- Make `vigilante gh` work reliably from inside sandbox sessions.
- Verify that the GitHub proxy is reachable from the container whenever sandbox mode is active.
- Ensure the sandbox has a deterministic auth path for:
  - issue comments
  - PR creation
  - branch push operations
- Do not rely on ad hoc direct-host fallbacks as the primary model.
- If proxy connectivity fails, surface a hard failure before implementation begins rather than letting the session proceed partway.

### Priority 4: Enforce preflight as a hard gate
- If baseline validation fails, the main issue implementation step must not run.
- The session should terminate as `blocked` or `failed` with a clear summary of why execution did not continue.
- This rule should apply consistently to both host-side and sandboxed sessions.

## Proposed Workstreams

### Workstream A: Sandbox checkout integrity
- Audit how worktrees are mounted into the container today.
- Decide whether the correct fix is:
  - mounting backing `.git/worktrees/...` metadata
  - rewriting `.git` to a valid in-container target
  - or creating a sandbox-local checkout from the branch/ref
- Add a pre-run repository health check:
  - `git rev-parse --is-inside-work-tree`
  - `git status --short --branch`
  - branch/ref sanity

### Workstream B: Sandbox runtime dependencies
- Define a minimal required tool inventory by supported stack.
- For the current repository and early rollout, include the tools needed for Go validation and common shell/file inspection.
- Add tests that verify the sandbox image can execute representative repo commands, not just that the container starts.

### Workstream C: Sandbox GitHub transport and credentials
- Trace the `vigilante gh` proxy path from container to host and make proxy readiness observable.
- Validate that push and PR operations have a supported auth path in sandbox mode.
- Fail fast when GitHub-side completion cannot succeed due to missing connectivity or credentials.

### Workstream D: Session lifecycle correctness
- Change orchestration so preflight failure blocks the main invocation.
- Ensure final session status reflects degraded execution honestly.
- Do not mark a session successful when required issue/PR steps were not completed.

## Acceptance Criteria
- A sandboxed session starts with a valid git repository and does not require the coding agent to repair `.git` state.
- Repository-native validation commands can run in the sandbox for supported repositories.
- `vigilante gh issue comment`, push, and PR creation work from inside sandbox sessions, or the session fails before implementation starts with a clear reason.
- Preflight failure prevents main issue implementation from running.
- Sandbox session results do not report success when required GitHub-side completion steps were skipped or impossible.

## Testing Expectations
- Add tests covering valid and invalid worktree mounts in sandbox mode.
- Add sandbox integration tests that verify git health checks from inside the container.
- Add tests proving required tool availability for supported repository stacks.
- Add tests for GitHub proxy reachability and failure handling in sandbox mode.
- Add orchestration tests proving that failed preflight stops the main issue invocation.

## Notes
- This document is intentionally limited to the most important root issues surfaced by the recent sandbox execution.
- It does not attempt to redesign sandbox mode broadly beyond what is necessary to make issue execution reliable.
