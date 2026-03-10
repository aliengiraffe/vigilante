# vigilante

`vigilante` is a Go CLI and background service that watches local Git repositories, discovers their GitHub remotes, monitors open issues with the GitHub CLI, and launches headless Codex sessions to implement issues in isolated git worktrees.

The initial target platforms are macOS and Ubuntu. The first implementation should keep dependencies minimal and lean on existing system tools where possible: `git`, `gh`, `codex`, and native service managers.

## Product Goal

Turn a local checkout into an autonomous issue worker:

```sh
vigilante watch ~/hello-world-app
```

Once a folder is registered, `vigilante` should:

1. Resolve the repository path and detect the GitHub remote.
2. Poll or subscribe for open GitHub issues through `gh`.
3. Select issues that are ready to work and not already being handled.
4. Launch a headless Codex session in YOLO mode against a dedicated git worktree.
5. Use the Codex issue implementation skill from the repo `skills/` folder as part of the execution prompt.
6. Post progress comments back to the GitHub issue, including session start and failures.
7. Track watched repositories locally and optionally run as a daemon.

## Core Workflow

For each watched repository:

1. Validate that the folder is a git repository.
2. Inspect `origin` and infer the GitHub repository slug.
3. Ensure required tools are available:
   - `git`
   - `gh`
   - `codex`
4. Ensure the Codex issue implementation skill from `skills/vigilante-issue-implementation/` is installed during setup, including its companion agent metadata.
5. Query GitHub for open issues.
6. Determine which issues are eligible for execution.
7. Create a git worktree for the selected issue.
8. Launch Codex headlessly in the worktree with a prompt that:
   - uses the issue implementation skill
   - instructs Codex to comment on the issue when work starts
   - instructs Codex to keep commenting as progress is made
   - instructs Codex to report errors back to the issue
9. Track the session state locally so the daemon does not duplicate work.
10. Clean up or mark terminal states when the session exits.

## Commands

### `vigilante watch [--assignee <value>] <path>`

Register a local repository for issue monitoring.

Expected behavior:

- expands `~` and resolves the absolute path
- validates the folder is a git repository
- discovers the GitHub remote from git config
- defaults the assignee filter to `me` unless overridden
- stores the target in `~/.vigilante/watchlist.json`

Example:

```sh
vigilante watch ~/hello-world-app
```

```sh
vigilante watch --assignee nicobistolfi ~/hello-world-app
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
- daemon status
- last scan time
- active issue/session, if any

### `vigilante unwatch <path>`

Remove a repository from the watchlist without deleting the repository itself.

### `vigilante daemon run`

Run the long-lived watcher loop in the foreground. This is the process the OS service should execute.

### `vigilante setup`

Prepare the machine for autonomous execution.

Expected behavior:

- creates `~/.vigilante/`
- initializes `watchlist.json`
- verifies `git`, `gh`, and `codex`
- installs the Codex issue implementation skill from the repo `skills/` folder if missing, including any companion files under that skill directory
- installs or updates the daemon definition when requested

## Development Mode

For fast local iteration, prefer running `vigilante` in the foreground instead of going through the installed OS service on every change.

Recommended loop:

```sh
go test ./...
go build -o ./vigilante ./cmd/vigilante
./vigilante setup
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

- rebuild the installed binary and refresh the installed Codex skill:

```sh
go build -o /Users/$USER/.local/bin/vigilante ./cmd/vigilante
/Users/$USER/.local/bin/vigilante setup
```

- reinstall the OS service after changing daemon or service behavior:

```sh
/Users/$USER/.local/bin/vigilante setup -d
```

Notes:

- foreground runs are the quickest way to iterate on scheduler, worktree, and Codex execution behavior
- `setup` refreshes the installed skill from the repo `skills/` folder
- after changing service installation logic on macOS, rerun `setup -d` so the `launchd` plist is regenerated with the current shell-derived PATH
- the CLI entrypoint lives in `cmd/vigilante/`, while non-exported implementation packages live under `internal/`

## Local State

`vigilante` should maintain its local state under:

```text
~/.vigilante/
```

Initial files:

- `watchlist.json`: configured repositories being monitored
- `sessions.json`: active or recent issue execution sessions
- `logs/`: daemon and run logs

Suggested `watchlist.json` shape:

```json
[
  {
    "path": "/Users/example/hello-world-app",
    "repo": "owner/hello-world-app",
    "branch": "main",
    "assignee": "me",
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
- ignore issues already assigned to a running local session
- avoid duplicate work across multiple daemon scans
- prefer oldest eligible open issue first unless later prioritization rules are added

Future policy can expand to label filters, assignment rules, priority queues, and concurrency limits.

## Codex Execution Contract

When `vigilante` launches Codex for an issue, it should:

- create a dedicated git worktree for that issue
- pass a prompt that includes the repository, issue number, and local working directory
- ensure the Codex issue implementation skill is available
- instruct Codex to post a GitHub comment when the session starts
- instruct Codex to post progress comments during execution
- instruct Codex to report failures on the issue if execution aborts

The first implementation can treat the Codex invocation as a subprocess wrapper around the installed `codex` CLI.

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

- branch: `vigilante/issue-<number>`
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
- comment on the GitHub issue when the Codex session fails to start
- comment on the GitHub issue when a running session exits with error

## Development Plan

The initial implementation should be split into issues covering:

1. CLI scaffolding and config/state management
2. Git repository and GitHub remote discovery
3. GitHub issue polling through `gh`
4. Codex skill installation and prompt assembly
5. Worktree lifecycle management
6. Headless Codex session runner with GitHub progress comments
7. Daemon loop and scheduler
8. macOS and Ubuntu service installation

## Current Status

The repository currently contains the initial Go module and a placeholder CLI. The feature set described above is the target specification that should now be implemented incrementally through GitHub issues.
