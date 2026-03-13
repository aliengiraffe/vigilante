<p align="center">
  <img src=".github/assets/logo.png" alt="vigilante logo" width="240">
</p>

# vigilante

`vigilante` is a Go CLI and background service that watches local Git repositories, discovers their GitHub remotes, monitors open issues with the GitHub CLI, and orchestrates headless coding agent sessions in isolated git worktrees.

The initial target platforms are macOS and Ubuntu. The first implementation should keep dependencies minimal and lean on existing system tools where possible: `git`, `gh`, one or more headless coding agent CLIs such as `claude code`, `codex`, and `gemini`.

## What Vigilante Is

`vigilante` is the control plane around headless coding agents. It watches repositories, chooses eligible issues, prepares isolated worktrees, launches agent sessions, tracks their lifecycle, reports progress back to GitHub, and keeps automation running as a daemon.

## What Vigilante Is Not

`vigilante` is not itself the code-generating agent. Tools such as Codex, Claude Code, and similar headless coding CLIs are the execution engines that read the prompt, edit code, run checks, and prepare pull requests. Keeping orchestration separate from code generation makes the system easier to evolve: Vigilante can handle scheduling, worktree isolation, PR maintenance, repo monitoring, and GitHub status reporting while remaining flexible about which provider actually performs the implementation work.

## Product Goal

Turn a local checkout into an autonomous coding-agent worker:

```sh
vigilante watch ~/hello-world-app
```

Once a folder is registered, `vigilante` should:

1. Resolve the repository path and detect the GitHub remote.
2. Poll or subscribe for open GitHub issues through `gh`.
3. Select issues that are ready to work and not already being handled.
4. Launch a headless coding agent session in YOLO mode against a dedicated git worktree.
5. Classify the watched repository and use the matching issue implementation skill from the repo `skills/` folder as part of the execution prompt.
6. Post progress comments back to the GitHub issue, including session start and failures.
7. Track watched repositories locally and optionally run as a daemon.

## Core Workflow

For each watched repository:

1. Validate that the folder is a git repository.
2. Inspect `origin` and infer the GitHub repository slug.
3. Ensure required tools are available:
   - `git`
   - `gh`
   - the configured coding-agent provider CLI (`codex`, `claude`, or `gemini`)
4. Ensure the bundled issue implementation skills from the repo `skills/` folder are installed during setup, including companion agent metadata.
5. Query GitHub for open issues.
6. Determine which issues are eligible for execution.
7. Create a git worktree for the selected issue.
8. Launch a supported coding agent headlessly in the worktree with a prompt that:
   - uses the repo-aware issue implementation skill selected from repository classification
   - passes the detected repo/process context into the prompt
   - instructs the agent to comment on the issue when work starts
   - instructs the agent to keep commenting as progress is made
   - instructs the agent to report errors back to the issue
9. Track the session state locally so the daemon does not duplicate work.
10. Clean up or mark terminal states when the session exits.

## Commands

## Installation

Install `vigilante` from the existing Homebrew tap:

```sh
brew tap aliengiraffe/spaceship
brew install --cask vigilante
```

Upgrade later with:

```sh
brew upgrade --cask vigilante
```

### `vigilante watch [--assignee <value>] [--max-parallel <value>] [--provider <codex|claude|gemini>] <path>`

Register a local repository for issue monitoring.

Expected behavior:

- expands `~` and resolves the absolute path
- validates the folder is a git repository
- discovers the GitHub remote from git config
- defaults the assignee filter to `me` unless overridden
- defaults `--max-parallel` to `3` when not configured
- defaults `--provider` to `codex` unless overridden
- resolves `me` to the authenticated GitHub login at runtime before issue queries
- stores the target in `~/.vigilante/watchlist.json`

Example:

```sh
vigilante watch ~/hello-world-app
```

```sh
vigilante watch --assignee nicobistolfi ~/hello-world-app
```

```sh
vigilante watch --max-parallel 3 ~/hello-world-app
```

```sh
vigilante watch --provider claude ~/hello-world-app
```

```sh
vigilante watch --provider gemini ~/hello-world-app
```

### `vigilante watch -d <path>`

Register a repository and ensure the daemon/service is installed and started.

Expected behavior:

- adds the target to the watchlist
- installs the `vigilante` daemon on the current operating system
- starts or reloads the service

### `vigilante list`

Show the currently watched repositories and their metadata.

Expected fields:

- local path
- GitHub repository slug
- max parallel issue sessions
- daemon status
- last scan time
- active issue/session, if any

### `vigilante list --running`

Show currently running sessions with their repository, issue number, branch, and worktree path.

### `vigilante cleanup --repo <owner/name> [--issue <n>]`

Clean up running sessions without touching unrelated historical session records.

Expected behavior:

- `--repo <owner/name> --issue <n>` cleans up one running session for a single issue
- `--repo <owner/name>` cleans up all running sessions for one repository
- removes the running-session blockage from local state
- removes the local worktree and issue branch when those artifacts are present and safe to delete

### `vigilante cleanup --all`

Clean up all running sessions across all watched repositories.

### `vigilante unwatch <path>`

Remove a repository from the watchlist without deleting the repository itself.

### `vigilante daemon run`

Run the long-lived watcher loop in the foreground. This is the process the OS service should execute.
By default it scans watched repositories every 1 minute. Use `--interval` to override that cadence for manual runs.

### `vigilante setup [--provider <codex|claude|gemini>]`

Prepare the machine for autonomous execution.

Expected behavior:

- creates `~/.vigilante/`
- initializes `watchlist.json`
- verifies `git`, `gh`, and the selected coding-agent provider CLI
- installs the bundled coding-agent skills for regular runtime use, including any companion files under each skill directory
  - `vigilante-issue-implementation`
  - `vigilante-issue-implementation-on-monorepo`
  - `vigilante-conflict-resolution`
  - `vigilante-create-issue`
- installs or updates the daemon definition when requested

## Development Mode

For fast local iteration, prefer running `vigilante` in the foreground instead of going through the installed OS service on every change.

If you use [`go-task`](https://taskfile.dev/), the repository includes a root `Taskfile.yml` for the main local workflows. Install `task` with either:

```sh
brew install go-task/tap/go-task
```

or:

```sh
go install github.com/go-task/task/v3/cmd/task@latest
```

Primary tasks:

- `task test` runs `go test ./...`
- `task build` builds `./vigilante`
- `task install` copies the built binary to `~/.local/bin/vigilante`
- `task setup` runs `./vigilante setup`
- `task install-setup` runs `~/.local/bin/vigilante setup`
- `task setup-daemon` runs a small wrapper around `~/.local/bin/vigilante setup -d` that retries once on macOS after cleaning up an existing `launchd` agent

Recommended loop:

```sh
task test
task build
task setup
./vigilante watch /path/to/repo
./vigilante daemon run --once
```

Useful development commands:

- run a single scan without installing the daemon:

```sh
go run ./cmd/vigilante daemon run --once
```

- run the foreground daemon loop directly from source:

```sh
go run ./cmd/vigilante daemon run --interval 30s
```

- rebuild the installed binary and refresh the installed provider skills:

```sh
task install
task install-setup
```

- reinstall the OS service after changing daemon or service behavior:

```sh
task setup-daemon
```

On macOS, `task setup-daemon` now performs one explicit recovery attempt when an existing `com.vigilante.agent` launch agent is already present. If the first refresh fails, the task cleans up the existing launch agent, retries once, and prints a short manual `launchctl bootout ...` hint if recovery still fails.

Notes:

- foreground runs are the quickest way to iterate on scheduler, worktree, and coding-agent execution behavior
- when `vigilante` runs from a repository checkout, `setup` refreshes installed skills from the local repo `skills/` folder so skill edits are picked up immediately
- when `vigilante` runs as an installed binary outside the repo checkout, `setup` uses skills embedded in the binary so it works from any directory without depending on the source tree
- after changing service installation logic on macOS, rerun `setup -d` so the `launchd` plist is regenerated with the current shell-derived PATH
- the CLI entrypoint lives in `cmd/vigilante/`, while non-exported implementation packages live under `internal/`

## CI and Releases

Pull requests are validated in GitHub Actions with native Go commands:

- `gofmt -l .`
- `go vet ./...`
- `go test ./...`
- `go build ./...`
- `goreleaser check`

Tagged releases are built and published with GoReleaser. Pushing a version tag that matches `{x}.{y}.{z}` and points to a commit already reachable from `main` creates a GitHub Release with:

- `darwin/amd64`
- `darwin/arm64`
- `linux/amd64`
- a `checksums.txt` file for the published archives
- an updated Homebrew cask in `aliengiraffe/homebrew-spaceship` so `brew install --cask vigilante` installs the tagged release from `aliengiraffe/spaceship`

The release workflow requires a GitHub App that can write to the tap repository:

- `APP_ID`: the GitHub App ID
- `APP_PRIVATE_KEY`: the GitHub App private key

During a tagged release, GitHub Actions exchanges those secrets for a short-lived token scoped to `aliengiraffe/homebrew-spaceship` and passes it to GoReleaser as `HOMEBREW_GITHUB_API_TOKEN`.

Recommended release flow:

```sh
git checkout main
git pull --ff-only
git tag 1.2.3
git push origin 1.2.3
```

Tags that do not match the required version format, such as `v1.2.3` or `release-1.2.3`, may start the release workflow but are rejected by the tag validation step before GoReleaser publishes artifacts. The release workflow also validates that the tagged commit is already merged into `main` before publishing to GitHub Releases.

Before cutting a release, validate the packaging config locally with:

```sh
goreleaser check
```

You can also confirm the Homebrew cask will target the published release archive names by checking the GoReleaser archive template:

- `vigilante_<version>_macOS_amd64.tar.gz`
- `vigilante_<version>_macOS_arm64.tar.gz`
- `vigilante_<version>_Linux_amd64.tar.gz`

## Local State

`vigilante` should maintain its local state under:

```text
~/.vigilante/
```

Initial files:

- `config.json`: service-level daemon configuration
- `watchlist.json`: configured repositories being monitored
- `sessions.json`: active or recent issue execution sessions
- `logs/`: daemon and run logs

Suggested `config.json` shape:

```json
{
  "blocked_session_inactivity_timeout": "20m"
}
```

Notes:

- `blocked_session_inactivity_timeout` is a service-level setting shared across all watched repositories.
- The default is `20m`.
- A blocked session is eligible for automatic local cleanup only after there have been no qualifying user comments on the issue, no session updates, and no worktree updates for longer than the configured timeout.
- This inactivity cleanup is conservative: it clears local blocked-session artifacts so the issue can be redispatched later, but it does not delete remote pull requests or remote branches automatically.

Suggested `watchlist.json` shape:

```json
[
  {
    "path": "/Users/example/hello-world-app",
    "repo": "owner/hello-world-app",
    "branch": "main",
    "assignee": "me",
    "max_parallel_sessions": 3,
    "daemon_enabled": true,
    "last_scan_at": "2026-03-10T12:00:00Z"
  }
]
```

## Issue Selection Rules

The scheduler should stay conservative in the first version.

Initial rules:

- only consider open issues
- ignore pull requests
- enforce `max_parallel_sessions` independently for each watched repository
- count both running implementation sessions and open-PR maintenance sessions against that repository limit
- avoid duplicate work across multiple daemon scans
- allow an issue label that exactly matches a registered provider id, such as `codex`, `claude`, or `gemini`, to override the watch target provider for that issue only
- if more than one provider-id label is present on the same issue, skip dispatch instead of choosing a provider arbitrarily
- prefer oldest eligible open issue first unless later prioritization rules are added

Future policy can expand to richer label filters, assignment rules, and priority queues.

## Headless Agent Execution Contract

When `vigilante` launches a coding agent for an issue, it should:

- create a dedicated git worktree for that issue
- pass a prompt that includes the repository, issue number, and local working directory
- ensure the issue implementation skill is available
- instruct the agent to post a GitHub comment when the session starts
- instruct the agent to post progress comments during execution
- instruct the agent to report failures on the issue if execution aborts

The agent invocation remains a subprocess wrapper around an installed coding CLI such as `codex`, `claude`, or `gemini`, while keeping the orchestration behavior provider-neutral.

## GitHub Integration

GitHub access should use `gh` rather than direct API client dependencies.

Expected `gh` responsibilities:

- detect authentication state
- list open issues for a repository
- post start/progress/error comments
- optionally inspect issue metadata needed for scheduling

This keeps the Go code smaller and delegates auth/session handling to the installed GitHub CLI.

## Worktree Strategy

Each issue run should get an isolated worktree to prevent branch collisions and dirty working trees.

Suggested naming:

- branch: `vigilante/issue-<number>-<title-slug>` with fallback compatibility for legacy `vigilante/issue-<number>` branches
- worktree path: a repo-local path such as `<repo>/.worktrees/vigilante/issue-<number>`

The daemon must track which worktrees are active so duplicate launches do not happen.

## Daemon and Service Installation

Initial supported operating systems:

- macOS via `launchd`
- Ubuntu via `systemd --user`

Service responsibilities:

- start `vigilante daemon run`
- restart on failure
- read the persisted watchlist
- write logs to `~/.vigilante/logs/`

## Error Handling

Failures should be visible both locally and on GitHub.

Minimum error reporting behavior:

- write structured local logs
- mark the local session as failed
- comment on the GitHub issue when the coding-agent session fails to start
- comment on the GitHub issue when a running session exits with error

## Development Plan

The initial implementation should be split into issues covering:

1. CLI scaffolding and config/state management
2. Git repository and GitHub remote discovery
3. GitHub issue polling through `gh`
4. Coding-agent skill installation and prompt assembly
5. Worktree lifecycle management
6. Headless coding-agent session runner with GitHub progress comments
7. Daemon loop and scheduler
8. macOS and Ubuntu service installation

## Current Status

The repository currently contains the initial Go module and a placeholder CLI. The feature set described above is the target specification that should now be implemented incrementally through GitHub issues.
