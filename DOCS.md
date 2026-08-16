# Vigilante Documentation

This file is the full reference for Vigilante. If you are new to the project, start with the shorter [README](README.md) first.

## What Vigilante Is

`vigilante` is the orchestration layer around coding agents. It is responsible for:

- treating project-management work items as the work queue (GitHub issues today; Linear and Jira planned)
- selecting eligible work based on labels, assignees, and repository limits
- creating dedicated git worktrees and issue branches for each session
- choosing the right execution playbook from repository classification
- launching supported coding-agent CLIs under a consistent lifecycle
- posting execution state back to GitHub through issue comments and PR tracking
- recovering, resuming, redispatching, and cleaning up local sessions safely

## What Vigilante Is Not

`vigilante` is not the code-generating model itself. Tools such as Codex, Claude Code, and Gemini are the execution engines that read prompts, edit code, run validation, and prepare pull requests. Keeping orchestration separate from code generation lets Vigilante stay provider-neutral while owning scheduling, worktree isolation, project-management coordination, and PR maintenance.

## Why Use Vigilante

Vigilante turns a repository checkout into a controlled autonomous worker instead of a loose collection of scripts.

- The project-management backend (GitHub today; Linear and Jira planned) stays the operator surface for issue intake, progress, resume commands, cleanup, and PR visibility.
- Each issue runs in an isolated worktree, which keeps the main checkout stable and makes unattended execution safer.
- Repository-aware skills let the same control plane adapt to standard repositories, monorepos, and supported build systems.
- Session state persists locally, so Vigilante can recover from failures, clean up stalled work, and avoid duplicate dispatch.
- Provider support is pluggable, so the orchestration layer remains stable even when teams change coding-agent runtimes.

## Project-Management Backend Architecture

Vigilante separates orchestration from backend-specific APIs through a provider abstraction layer. The core orchestration loop depends on backend interfaces rather than calling GitHub APIs directly:

- **Issue Tracker** (`IssueTracker`): work item listing, details, comments, and operator commands
- **Pull Request Manager** (`PullRequestManager`): PR discovery, status, merge, and branch lifecycle
- **Label Manager** (`LabelManager`): label provisioning and synchronization
- **Rate Limiter** (`RateLimiter`): optional API quota awareness

The issue-tracking backend is allowed to differ from the git-hosting and pull-request backends. A watch target can combine:

- GitHub issues + GitHub git remote + GitHub pull requests (current default)
- Linear issues + GitHub git remote + GitHub pull requests (planned)
- Jira issues + GitHub git remote + GitHub pull requests (planned)

GitHub is the only fully implemented backend. The `internal/backend/` package defines the interfaces and neutral types, while `internal/backend/github/` provides the GitHub implementation. Adding a new backend requires implementing the relevant interfaces without restructuring the core dispatch or session lifecycle.

Watch targets carry explicit backend identity fields (`issue_backend`, `git_backend`, `pr_backend`) that default to `"github"` when not set, preserving backward compatibility with existing configurations.

## Quickstart

Install with Homebrew:

```sh
brew install --cask aliengiraffe/spaceship/vigilante
```

Or from PyPI (macOS arm64/x86_64, Linux x86_64) — the wheel ships the same
prebuilt `vigilante` binary, and the command name is unchanged:

```sh
pipx install vigilante-cli   # or: pip install vigilante-cli / uv tool install vigilante-cli
```

Either way, `git`, an authenticated `gh`, and a coding-agent CLI must be
installed separately — see the requirements below.

Prepare the local machine. `--provider` defaults to `claude`, so pass the flag only when you want a different coding agent:

```sh
vigilante setup -d
```

Register a repository after setup installs the background service:

```sh
vigilante watch ~/path/to/repo
```

Useful follow-up commands:

```sh
vigilante list
vigilante list --running
vigilante status
vigilante service restart
vigilante daemon run --once
```

Quickstart requirements:

- `git`
- `gh` authenticated against the GitHub account you want Vigilante to operate with
- one supported coding-agent CLI installed locally: `claude` (the default), or `codex`, `gemini`, or `opencode` selected with `--provider`

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

In the current implementation, that worker loop already covers repository onboarding, issue intake, isolated worktrees, provider orchestration, repo-aware execution skills, local session recovery, and part of the pull request maintenance path. CI/CD promotion and richer deployment control are planned next-stage capabilities.

Vigilante also monitors GitHub REST API quota through `gh api /rate_limit` before GitHub-heavy orchestration work. When the REST core bucket drops below `100` remaining requests, Vigilante posts one delay notice per affected issue for that reset window, pauses additional GitHub-backed work, and resumes automatically after GitHub's reported reset time.

## Telemetry

Vigilante emits anonymous telemetry with two different purposes:

- PostHog analytics events track product usage, such as command starts and completions, using bounded properties like command name, feature area, result, platform, and distro.
- OTLP logs remain the operational debugging stream when log export is configured.

Analytics events intentionally avoid raw repository contents, issue text, file paths, and other free-form command arguments. Operators can disable both streams with `DO_NOT_TRACK=1` or `MYTOOL_NO_ANALYTICS=1`.

## Core Workflow

For each watched repository:

1. Validate that the folder is a git repository.
2. Inspect `origin` and infer the GitHub repository slug.
3. Ensure required tools are available:
   - `git`
   - `gh`
   - the configured coding-agent provider CLI (`codex`, `claude`, `gemini`, or `opencode`)
4. Ensure the bundled issue implementation skills from the repo `skills/` folder are installed during setup, including companion agent metadata.
5. Query GitHub for open issues.
6. Determine which issues are eligible for execution.
7. Create a git worktree for the selected issue.
8. Launch a supported coding agent headlessly in the worktree with a prompt that:
   - uses the repo-aware issue implementation skill selected from repository classification
   - passes the detected repo/process context into the prompt
   - instructs the agent to comment on the issue when work starts
   - instructs the agent to keep commenting as progress is made
   - instructs the agent to preserve the user's git author, committer, and signing identity for any commit-related operation
   - instructs the agent not to add agent `Co-authored by:` trailers or similar commit attribution
   - instructs the agent to report errors back to the issue
9. Track the session state locally so the daemon does not duplicate work.
10. Clean up or mark terminal states when the session exits.

## Commands

`vigilante --help` and `vigilante -h` print top-level usage. Each command also supports command-specific help, for example:

```sh
vigilante clone --help
vigilante watch --help
vigilante daemon run --help
```

### Shell completion

Generate a completion script for a supported shell and source or install it in your shell startup files:

```sh
vigilante completion bash
vigilante completion zsh
vigilante completion fish
```

Examples:

```sh
vigilante completion zsh > "${fpath[1]}/_vigilante"
autoload -Uz compinit && compinit
```

```sh
vigilante completion bash > ~/.local/share/bash-completion/completions/vigilante
```

```sh
vigilante completion fish > ~/.config/fish/completions/vigilante.fish
```

### Tool proxy commands

Vigilante can proxy a bounded set of external CLIs while emitting privacy-aware telemetry about only the tool and sanitized command shape:

- `vigilante gh ...`
- `vigilante git ...`
- `vigilante docker ...`

Examples:

```sh
vigilante gh repo view aliengiraffe/vigilante
vigilante git status --short
vigilante docker compose ps
```

The proxy preserves the underlying tool's stdout, stderr, and exit status. Telemetry intentionally avoids raw positional arguments, flag values, repo slugs, tokens, paths, prompts, and free-form text.

### Commit command

`vigilante commit` is the required commit entrypoint for all coding-agent workflows. It wraps `git commit` while preserving the user's configured git author, committer, and signing behavior. Agent attribution lines in commit messages are stripped automatically.

```sh
vigilante commit -m "Fix validation logic"
vigilante commit --amend --no-edit
```

Coding agents must use `vigilante commit` instead of `git commit` or GitHub CLI commit flows. This ensures that all agent-produced commits remain user-authored and signed.

### `vigilante clone [git-clone-flags...] [--] <repo> [<path>]`

Clone a repository using `git clone`, then automatically register the resulting local path as a Vigilante watch target.

Expected behavior:

- forwards clone flags and arguments through to `git clone`
- supports both explicit destination paths and git-derived default destination names
- adds the cloned repository to `~/.vigilante/watchlist.json` only after clone success
- reuses existing watch-target deduplication when the cloned repository is already watched
- returns a non-zero exit code if clone fails
- returns a non-zero exit code if clone succeeds but automatic watch-target registration fails

Examples:

```sh
vigilante clone git@github.com:owner/hello-world-app.git
```

```sh
vigilante clone --depth 1 https://github.com/owner/hello-world-app.git ~/src/hello-world-app
```

## Installation

Install `vigilante` with Homebrew:

```sh
brew install --cask aliengiraffe/spaceship/vigilante
```

Upgrade later with:

```sh
brew upgrade --cask vigilante
```

Or install from PyPI with a Python package manager:

```sh
pip install vigilante-cli
pipx install vigilante-cli
uv tool install vigilante-cli
```

The PyPI distribution is named `vigilante-cli` because `vigilante` on PyPI
belongs to an unrelated project; the installed command is still `vigilante`.
Each wheel contains no Python code — it embeds the prebuilt (and, on macOS,
signed and notarized) release binary, which pip places on `PATH`. Wheels are
published for macOS arm64, macOS x86_64, and Linux x86_64. On unsupported
platforms (Windows, Linux arm64) the install fails with instructions pointing
at Homebrew and the GitHub releases page rather than installing a broken
package. pip installs the binary only: `git`, an authenticated `gh`, and a
coding-agent CLI remain prerequisites, and `vigilante setup -d` is still the
bootstrap step.

### `vigilante watch [--assignee <value>] [--max-parallel <value>] [--provider <codex|claude|gemini|opencode>] [--issue-tracker <github|linear>] [--issue-tracker-stage <value>] [--branch <name> | --track-default-branch] <path>`

Register a local repository for issue monitoring.

Expected behavior:

- expands `~` and resolves the absolute path
- validates the folder is a git repository
- discovers the GitHub remote from git config
- defaults new watch targets to tracking the repository's current default branch automatically
- pins the base branch when `--branch <name>` is supplied
- switches an existing target back to default-branch tracking when `--track-default-branch` is supplied
- defaults the assignee filter to `me` unless overridden
- defaults `--max-parallel` to `0` when not configured, where `0` means unlimited
- defaults `--provider` to `claude` unless overridden; already-watched repositories keep the provider already recorded in `watchlist.json`
- defaults `--issue-tracker` to `github` unless overridden
- when `--issue-tracker linear` is selected, verifies the local `linear` CLI is installed and authenticated before saving the watch target
- accepts an optional `--issue-tracker-stage` filter; Linear defaults to `Todo` when the flag is omitted
- resolves `me` to the authenticated GitHub login at runtime before issue queries
- preserves an existing target's branch mode unless one of the branch flags is supplied
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
vigilante watch --max-parallel 0 ~/hello-world-app
```

```sh
vigilante watch --provider claude ~/hello-world-app
```

```sh
vigilante watch --provider gemini ~/hello-world-app
```

```sh
vigilante watch --provider opencode ~/hello-world-app
```

```sh
vigilante watch --branch develop ~/hello-world-app
```

```sh
vigilante watch --track-default-branch ~/hello-world-app
```

### `vigilante list`

Show the currently watched repositories and their metadata.

Expected fields:

- local path
- GitHub repository slug
- max parallel issue sessions
- daemon status
- last scan time
- active issue/session, if any
- effective base branch and whether it is `auto` or `pinned`

### `vigilante list --running`

Show currently running sessions with their repository, issue number, branch, and worktree path.

### `vigilante status [--plain] [-w]`

Show a compact operational overview of the Vigilante OS-managed user service, watched repositories, sessions, and GitHub rate limits.

On a terminal this renders a live dashboard that refreshes in place; `q`, `Esc`, and `Ctrl+C` exit. Anywhere else — a pipe, a redirect, a CI log — it prints the same information as plain text with no ANSI escape sequences, so existing scripts keep working unchanged.

Output modes:

| Command | Behavior |
|---|---|
| `vigilante status` | live dashboard on a terminal, plain text otherwise |
| `vigilante status --plain` | plain text, even on a terminal |
| `vigilante status --plain -w` | plain text refreshed in place about once per second |
| `vigilante status -w` | accepted; the dashboard is already live, so it has no additional effect |
| `vigilante status \| cat`, `vigilante status > file` | plain text |

Dashboard behavior:

- four panes — service, watched repositories, sessions, and GitHub rate limits — refresh in place without growing the scrollback
- the arrow keys, `j`/`k`, `PgUp`/`PgDn`, and `Home`/`End` scroll when the content is taller than the terminal
- session and watch-target data refreshes every second, service state every five seconds, and the GitHub rate-limit snapshot every minute, so the dashboard does not consume the rate limit it reports
- `NO_COLOR`, `TERM=dumb`, and low-color terminals degrade to uncolored output

Expected behavior:

- reports a stable `state` value of `running`, `stopped`, or `not-installed`
- includes the service manager, service identifier, and installed service file path
- includes a watched-repositories summary with key per-repo metadata such as repo slug, branch plus branch mode, provider, filters or limits, activity, and last scan time
- exits successfully when the service is not installed so operators and scripts can inspect the reported state
- fails with a clear error on unsupported operating systems or when the underlying service manager cannot be queried

### `vigilante logs [--access] [--repo <owner/name>] [--issue <n>]`

Inspect local log files under `~/.vigilante/logs/`.

Expected behavior:

- `vigilante logs` lists the available daemon, access, and per-issue session logs so an operator can see which local evidence exists before choosing a recovery action, including rotated backups such as `vigilante.log.1`
- `vigilante logs --access` prints the structured access log at `~/.vigilante/logs/access.jsonl`, where each JSON line records one subprocess execution with timing, execution context, repo or issue metadata when available, sanitized argv, and exit status
- `vigilante logs --repo <owner/name> --issue <n>` prints the log for one issue session so an operator can inspect the latest local execution details directly
- all read paths always show the current log file; rotated backups carry a numeric suffix and are listed but not concatenated into the current output
- both follow modes (`vigilante logs -f` and `vigilante logs --access -w`) reopen the log path on every poll, so following continues across a rotation — expect a visible seam where the current file restarts, not an error
- logs complement `vigilante list`, `vigilante status`, and GitHub issue comments; they do not replace session state or the remote audit trail

Log files are size-bounded. Every log write path rotates its file once it exceeds
`log_max_file_size`, and the total size of `~/.vigilante/logs/` is capped at
`log_max_total_size` (default 500MB). See
[Log Rotation and Disk Budget](#log-rotation-and-disk-budget).

### `vigilante service restart`

Restart the installed Vigilante user service through the operating system service manager.

Expected behavior:

- uses `launchctl` on macOS and `systemctl --user` on Linux
- restarts the installed managed service instead of launching an unmanaged background process
- fails clearly when the service is not installed or the platform is unsupported

### `vigilante cleanup --repo <owner/name> [--issue <n>]`

Clean up running sessions without touching unrelated historical session records.

Expected behavior:

- `--repo <owner/name> --issue <n>` cleans up one running session for a single issue
- `--repo <owner/name>` cleans up all running sessions for one repository
- removes the running-session blockage from local state
- removes the local worktree and issue branch when those artifacts are present and safe to delete
- retries cleanup even when automatic closed-issue cleanup already reached its three-attempt cap

When an issue is closed, the daemon automatically attempts to remove its local
worktree and branch up to three times. If all three attempts fail, Vigilante
comments with the concrete git error and recovery commands, applies the
`vigilante:needs-git-fix` label, and stops monitoring the terminal session while
preserving the cleanup error. After repairing the repository state by hand, run
`vigilante cleanup --repo <owner/name> --issue <n>` to force an uncapped retry.

### `vigilante cleanup --all`

Clean up all running sessions across all watched repositories.

### `vigilante redispatch --repo <owner/name> --issue <n>`

Force a fresh local restart for one watched issue.

Expected behavior:

- fails clearly when the target repository is not currently watched
- stops any active local session for the target issue before redispatching
- removes the target issue worktree and local issue branch artifacts when safe to do so
- clears stale local session state for the target issue only
- immediately launches a brand-new implementation session using the current watched-repo configuration
- does not delete remote pull requests or remote branches

### `vigilante recreate [--repo <owner/name>] --issue <n>`

Recreate a stuck issue as a fresh duplicate and clean up its stale artifacts.
`--issue` is always required. `--repo` may be omitted when the command runs
inside a git checkout whose `origin` matches a repository Vigilante watches;
an explicit `--repo` always takes precedence.

### `vigilante unwatch <path>`

Remove a repository from the watchlist without deleting the repository itself.

### `vigilante daemon run`

Run the long-lived watcher loop in the foreground. This is the process the OS service should execute.
By default it scans watched repositories every 1 minute. Use `--interval` to override that cadence for manual runs.

### `vigilante setup [--provider <codex|claude|gemini|opencode>]`

Prepare the machine for autonomous execution.

Expected behavior:

- defaults `--provider` to `claude` unless overridden
- creates `~/.vigilante/`
- initializes `watchlist.json`
- verifies `git`, `gh`, and the selected coding-agent provider CLI
- verifies the selected provider CLI reports a compatible build-supported version range, currently `>=0.114.0, <2.0.0` for `codex`, `>=2.0.0, <3.0.0` for `claude`, `>=0.34.0, <1.0.0` for `gemini`, and `>=1.0.0, <2.0.0` for `opencode`
- installs the bundled coding-agent skills for regular runtime use, including any companion files under each skill directory
  - `vigilante-issue-implementation`
  - `vigilante-issue-implementation-on-monorepo`
  - `vigilante-issue-implementation-on-turborepo`
  - `vigilante-issue-implementation-on-nx`
  - `vigilante-issue-implementation-on-rush`
  - `vigilante-issue-implementation-on-rush-monorepo`
  - `vigilante-issue-implementation-on-bazel`
  - `vigilante-issue-implementation-on-gradle`
  - `vigilante-issue-implementation-on-bazel-monorepo`
  - `vigilante-conflict-resolution`
  - `vigilante-create-issue`
  - `vigilante-local-service-dependencies`
  - `docker-compose-launch`
- installs or updates the daemon definition

On macOS, `vigilante setup` resolves Homebrew-style symlinks before it prepares the daemon binary. For Homebrew cask installs, Vigilante first clears `com.apple.provenance` and `com.apple.quarantine` recursively from the enclosing Caskroom version directory, then removes those same xattrs from the resolved binary when present, ad-hoc signs that binary, and runs `spctl --assess --type execute -vv` against the resolved path before loading the service.

If Gatekeeper still rejects the binary, the error now reports both the assessed path and the invoked path when they differ. A useful manual recovery sequence is:

```sh
realbin="$(python3 -c 'import os, sys; print(os.path.realpath(sys.argv[1]))' /opt/homebrew/bin/vigilante)"
xattr "$realbin"
xattr -dr com.apple.provenance "$(dirname "$realbin")" 2>/dev/null || true
xattr -dr com.apple.quarantine "$(dirname "$realbin")" 2>/dev/null || true
xattr -d com.apple.provenance "$realbin" 2>/dev/null || true
xattr -d com.apple.quarantine "$realbin" 2>/dev/null || true
codesign --force --sign - "$realbin"
spctl --assess --type execute -vv "$realbin"
```

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
- `task setup-daemon` runs a small wrapper around `~/.local/bin/vigilante setup` that retries once on macOS after cleaning up an existing `launchd` agent

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

On macOS, `vigilante setup` also prepares the installed daemon binary before reloading the LaunchAgent by clearing observed Gatekeeper xattrs from the Homebrew cask install root when applicable, clearing those xattrs from the resolved binary, applying ad-hoc signing, and validating the binary with `spctl`. If macOS still rejects the binary, setup exits with a code-signing error instead of leaving the agent stuck in `OS_REASON_CODESIGNING`.

Notes:

- foreground runs are the quickest way to iterate on scheduler, worktree, and coding-agent execution behavior
- when `vigilante` runs from a repository checkout, `setup` refreshes installed skills from the local repo `skills/` folder so skill edits are picked up immediately
- when `vigilante` runs as an installed binary outside the repo checkout, `setup` uses skills embedded in the binary so it works from any directory without depending on the source tree
- after changing service installation logic on macOS, rerun `setup` so the `launchd` plist is regenerated with the current shell-derived PATH
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
- an updated Homebrew cask in the `aliengiraffe/homebrew-spaceship` tap so `brew install --cask aliengiraffe/spaceship/vigilante` installs the tagged release
- prebuilt-binary wheels and an sdist on PyPI as `vigilante-cli`, repackaged from the published release archives after the GitHub release and cask are live (see [docs/releasing.md](docs/releasing.md))

The release workflow requires a GitHub App that can write to the tap repository:

- `APP_ID`: the GitHub App ID
- `APP_PRIVATE_KEY`: the GitHub App private key

During a tagged release, GitHub Actions exchanges those secrets for a short-lived token scoped to `aliengiraffe/homebrew-spaceship` and passes it to GoReleaser as `HOMEBREW_GITHUB_API_TOKEN`.

Pushes to `main` also publish a rolling prerelease channel without requiring GoReleaser Pro. The nightly workflow builds OSS snapshot archives, recreates the `main-nightly` GitHub prerelease with fresh assets, and updates a separate Homebrew cask in `aliengiraffe/homebrew-spaceship`.

Nightly install path:

```sh
brew tap aliengiraffe/spaceship
brew install --cask vigilante-nightly
```

Stable installs remain on the tagged release path:

```sh
brew install --cask aliengiraffe/spaceship/vigilante
```

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
- `logs/`: daemon and run logs, plus their rotated backups, size-capped at `log_max_total_size` (default 500MB) in total

Suggested `config.json` shape:

```json
{
  "blocked_session_inactivity_timeout": "20m",
  "log_max_total_size": "500MB",
  "log_max_file_size": "50MB",
  "log_max_backups": 3
}
```

Notes:

- `blocked_session_inactivity_timeout` is a service-level setting shared across all watched repositories.
- The default is `20m`.
- `log_max_total_size`, `log_max_file_size`, and `log_max_backups` bound local log growth; see [Log Rotation and Disk Budget](#log-rotation-and-disk-budget). All three are optional and default to `500MB`, `50MB`, and `3`.
- A blocked session is eligible for automatic local cleanup only after there have been no qualifying user comments on the issue, no session updates, and no worktree updates for longer than the configured timeout.
- This inactivity cleanup is conservative: it clears local blocked-session artifacts so the issue can be redispatched later, but it does not delete remote pull requests or remote branches automatically.
- Stale running implementation sessions use a separate recovery path: after Vigilante first confirms the run is stale, it waits 20 minutes before attempting an automatic fresh restart, persists the restart attempt count in session state, and stops after 3 automatic restarts until a human intervenes.
- When an issue looks blocked or the daemon appears unhealthy, inspect `vigilante logs` alongside `sessions.json`, `vigilante list`, `vigilante status`, and GitHub issue comments so recovery decisions use both local state and remote context.
- Local log evidence is size-bounded, so it does not persist indefinitely. When investigating an older failure, collect the relevant logs before continued daemon activity pushes the directory over budget and evicts them.

### Log Rotation and Disk Budget

Vigilante bounds the disk it uses for local logs so a long-running daemon cannot
fill the operator's disk. Two limits work together:

- **Per-file rotation.** Each log file rotates once a write would push it past
  `log_max_file_size` (default `50MB`). The current file is renamed to
  `<name>.1`, existing backups shift up (`<name>.1` becomes `<name>.2`, and so
  on), and the writer immediately reopens the original path. `log_max_backups`
  (default `3`) retained backups are kept per file; older generations are
  deleted. Backups are uncompressed, so they stay readable with ordinary tools.
- **Directory budget.** The total size of `~/.vigilante/logs/` is capped at
  `log_max_total_size` (default `500MB`). A sweep runs after every rotation, at
  daemon start, and on each scan tick. It evicts rotated backups first (oldest
  generation first), then logs for completed sessions by least recent
  modification.

The sweep never deletes the current `vigilante.log`, the current `access.jsonl`,
or the session log of a session that is still running, blocked, or resuming. If
the live files alone exceed the budget, the sweep stops rather than deleting a
log an active writer owns.

Rotations and evictions are recorded in the daemon log, including the reclaimed
bytes, so a missing log can be distinguished from a rotated one.

Notes:

- All three settings live in `~/.vigilante/config.json` and are optional.
  Existing config files without them keep working and are not rewritten.
- Sizes accept human-readable values (`"500MB"`, `"1GB"`, `"512KB"`) or a plain
  byte count. Suffixes are powers of 1024.
- An unparseable or invalid value falls back to that setting's default rather
  than disabling rotation — logging never prevents the daemon from starting.
- Enforcement covers every write path: the daemon log, the access log, and
  per-issue session logs, including the streaming provider output that dominates
  log volume.
- Upgrading with an already-oversized logs directory needs no manual step: the
  first daemon start sweeps it back within budget and logs the eviction.
- This is a size-driven policy only. There is no time-based retention, and
  `vigilante cleanup` semantics are unchanged — it does not delete logs.

### Operator Log Triage

Use logs to decide which recovery command fits the evidence already recorded locally.

1. Run `vigilante list --running` or `vigilante status` first to confirm whether the problem is scoped to one issue or the daemon as a whole.
2. Run `vigilante logs` with no flags when the daemon is stalled, scans are not happening, multiple repositories look affected, or you need to identify the relevant local log file quickly.
3. Run `vigilante logs --repo <owner/name> --issue <n>` when one issue is blocked, a resume attempt failed, or you need to inspect the latest session-specific failure before deciding what to do next.
4. If the per-issue log shows the agent can continue from the existing worktree and session state, prefer `vigilante resume --repo <owner/name> --issue <n>`.
5. If the per-issue log shows corrupted local state, a dead worktree, or a failed run that should be restarted cleanly, use `vigilante redispatch --repo <owner/name> --issue <n>`.
6. If the log shows the local session artifacts are stale and should be removed without starting new work immediately, use `vigilante cleanup --repo <owner/name> --issue <n>` or repo-wide cleanup as appropriate.
7. If the daemon log points to service-manager failures, scan-loop problems, or machine-level issues, troubleshoot the daemon with `vigilante service restart`, `vigilante setup`, or a foreground `vigilante daemon run --once` check before touching issue sessions.

Suggested `watchlist.json` shape:

```json
[
  {
    "path": "/Users/example/hello-world-app",
    "repo": "owner/hello-world-app",
    "branch_mode": "auto",
    "branch": "main",
    "assignee": "me",
    "max_parallel_sessions": 0,
    "last_scan_at": "2026-03-10T12:00:00Z"
  }
]
```

## Issue Selection Rules

The scheduler should stay conservative in the first version.

Initial rules:

- only consider open issues
- ignore pull requests
- enforce positive `max_parallel_sessions` independently for each watched repository
- treat `max_parallel_sessions: 0` as unlimited parallel issue dispatch for that repository
- count both running implementation sessions and open-PR maintenance sessions against that repository limit
- avoid duplicate work across multiple daemon scans
- allow an issue label that exactly matches a registered provider id, such as `codex`, `claude`, `gemini`, or `opencode`, to override the watch target provider for that issue only
- allow `claude:sonnet`, `claude:opus`, or `claude:fable` to route to Claude and select that model alias for the persisted session; the coding-agent launch comment echoes the active alias
- allow bare `claude` together with one Claude model label; reject multiple Claude model labels or a Claude model label combined with another provider label
- ignore unrecognized names such as `claude:haiku`, like any unrelated label
- prefer oldest eligible open issue first unless later prioritization rules are added

Future policy can expand to richer label filters, assignment rules, and priority queues.

## Pull Request Maintenance

For pull requests tied to an active Vigilante session:

- keep the branch updated against the session or pull request base branch through the existing maintenance loop instead of assuming `main`
- keep required checks under observation after a PR opens; queued and in-progress checks wait without dispatching work
- deduplicate CI remediation by PR head SHA and check-run generation so daemon restarts and repeated polls cannot launch concurrent fixes
- allow up to three consecutive focused remediation attempts across replacement CI runs; also stop after 30 minutes without a new head or check-run observation, then pause with persisted diagnostics and an explicit `vigilante resume` recovery path
- if either the source issue or the PR has `vigilante:automerge`, attempt a GitHub squash merge only after required checks pass and GitHub reports the PR is mergeable
- keep the legacy plain `automerge` PR label working as a compatibility alias during migration to the namespaced label
- never force through branch protection, required reviews, or failing checks

## Stacked-PR Base Branches

Issue authors can request that an issue be implemented on top of an existing in-flight branch (a stacked PR) by adding a single top-level line to the issue body of the form `Base branch: <branch-name>`. The label is case-insensitive and the branch name may be surrounded by backticks.

When that directive is present:

- the implementation skill fetches `origin/<branch-name>` and re-roots the work branch onto it before changing code
- the resulting pull request targets `<branch-name>` instead of the watch target's base branch
- the PR body and the PR-opened issue comment state that the PR is stacked on `<branch-name>`
- the maintenance loop keeps the branch updated against `<branch-name>` through the same path used for default-branch PRs

When the directive is absent, Vigilante uses the watch target's base branch as today. Only an explicit top-level `Base branch:` line triggers stacking; prose mentions of other branches, linked issue numbers, and native sub-issue relationships are ignored. If the specified branch does not exist on the remote, the session is reported as failed on the issue rather than silently falling back to the default branch.

## Package Hardening

Vigilante runs a deterministic, code-driven package hardening scan for watched repositories classified with the `nodejs` tech stack. The scan is not LLM-driven — all checks use static analysis of manifest files, lockfiles, and CI configuration.

### When the Scan Runs

The hardening scan triggers after a coding-agent session pushes a branch upstream. Vigilante uses `git diff` against the PR base branch to detect whether any `package.json` files were added or modified. If no `package.json` changes are found, the scan is skipped. The scan does not list all open PRs — it only runs in the context of a session that just pushed.

### Checks Performed

The scan performs the following deterministic checks:

- **Lockfile presence** (`lockfile-present`): Checks whether a `package-lock.json`, `pnpm-lock.yaml`, or `yarn.lock` exists at the repository root. A missing lockfile means dependency resolution is non-deterministic and is flagged as high severity.
- **npm audit** (`npm-audit-vulnerabilities`): When the detected package manager is npm and a lockfile is present, Vigilante runs `npm audit --json` in the worktree and reports known vulnerabilities. If the lockfile is missing, the audit is skipped with a medium-severity finding explaining why.
- **Non-exact dependency ranges** (`non-exact-ranges`): Flags dependencies in `package.json` that use range specifiers (`^`, `~`, `>`, `<`, `*`, `||`) instead of exact pinned versions. This is reported as low severity since lockfiles typically pin transitive versions.
- **CI deterministic install** (`ci-deterministic-install`): Scans GitHub Actions workflow files for deterministic install commands (`npm ci`, `pnpm install --frozen-lockfile`, `yarn install --immutable`). A missing deterministic install step is flagged as medium severity.
- **CI audit step** (`ci-audit-step`): Checks whether CI workflows include a dependency audit step. A missing audit step is flagged as low severity.

### PR Comment and Label Behavior

When findings are present, Vigilante:

1. Posts a structured PR comment with a findings table showing severity, check name, and details. The comment includes a collapsible remediation section with specific fix instructions for each finding.
2. Applies the `vigilante:flagged-security-review` label to the pull request.
3. Stores the comment ID and PR state in local hardening state to avoid duplicate comments on subsequent scans.

The comment is identified by the `<!-- vigilante:package-hardening -->` HTML marker so Vigilante can locate it when scanning for checkbox changes.

### Checkbox-Driven Remediation

The hardening comment includes an **implement fixes** checkbox:

```
- [ ] **implement fixes** — Vigilante will launch an automated remediation session for the findings above.
```

When a human checks this box, Vigilante detects the change during its next poll loop, reacts with an `eyes` emoji, and dispatches an agentic remediation session scoped to the PR branch. After the remediation session completes, Vigilante posts a follow-up result comment indicating whether remediation succeeded or was incomplete.

Vigilante monitors checkbox state only for PRs that already have a recorded hardening comment. It does not re-scan PRs that have already had a remediation attempt dispatched.

### Configuration

The feature is controlled by the `package_hardening_enabled` field in `~/.vigilante/config.json`:

```json
{
  "package_hardening_enabled": true
}
```

- When the field is absent or `null`, hardening defaults to **enabled**.
- Set to `false` to disable the feature entirely.

### Scope Limitation

Package hardening currently applies only to repositories classified with the `nodejs` tech stack. Repositories without this classification are silently skipped. Support for additional package ecosystems is expected to expand in future releases.

## Issue Labeling System

Vigilante should use a small issue-label taxonomy that complements issue comments instead of replacing them. The repository-owned proposal lives in [`.github/labels.json`](.github/labels.json).

Label ownership rules:

- Work-classification labels such as `bug`, `feature`, and `good first issue` remain repository-managed and should not be changed by Vigilante.
- `vigilante:*` lifecycle and intervention labels are primarily informational and should be set or cleared by Vigilante as the issue moves through execution.
- Provider-routing labels `codex`, `claude`, `gemini`, and `opencode` keep their existing control semantics and remain human-managed overrides. Human-managed `claude:sonnet`, `claude:opus`, and `claude:fable` labels additionally select a Claude model for the whole persisted session.
- `vigilante:resume` is the preferred control label for unblocking a paused session; `resume` remains a legacy-compatible alias.

Proposed groups:

- Execution state: `vigilante:queued`, `vigilante:running`, `vigilante:blocked`, `vigilante:ready-for-review`, `vigilante:awaiting-user-validation`, `vigilante:done`
- Human-intervention state: `vigilante:needs-human-input`, `vigilante:needs-provider-fix`, `vigilante:needs-git-fix`
- Provider routing controls: `codex`, `claude`, `gemini`, `opencode`, `claude:sonnet`, `claude:opus`, `claude:fable`
- Explicit control labels: `vigilante:resume` and legacy `resume`

Recommended lifecycle:

1. When an issue becomes eligible but has not started, add `vigilante:queued`.
2. When execution starts, replace `vigilante:queued` with `vigilante:running`.
3. If execution stalls on a known blocker, replace `vigilante:running` with `vigilante:blocked` and add exactly one matching `vigilante:needs-*` label when possible.
4. When implementation is ready for a human to inspect, replace blocked or running state with `vigilante:ready-for-review` as the single review-handoff label.
5. When code review is complete but a product or operator check is still required, use `vigilante:awaiting-user-validation`.
6. When the issue reaches a terminal successful state, clear transient labels and leave `vigilante:done`.

This keeps control semantics narrow while making the issue list readable at a glance. Existing label-based behaviors stay compatible: watch-target allowlists still match repository-managed labels, provider overrides still use provider ids, and blocked-session recovery still honors both `resume` and `vigilante:resume`.

## Headless Agent Execution Contract

When `vigilante` launches a coding agent for an issue, it should:

- create a dedicated git worktree for that issue
- pass a prompt that includes the repository, issue number, and local working directory
- ensure the issue implementation skill is available
- instruct the agent to post a GitHub comment when the session starts
- instruct the agent to post progress comments during execution
- instruct the agent to preserve the user's existing git identity and signing configuration for commits, amends, rebases, and other history edits
- instruct the agent not to add coding-agent `Co-authored by:` trailers or similar attribution
- instruct the agent to report failures on the issue if execution aborts

The agent invocation remains a subprocess wrapper around an installed coding CLI such as `codex`, `claude`, `gemini`, or `opencode`, while keeping the orchestration behavior provider-neutral.

## GitHub Integration

GitHub access uses `gh` rather than direct API client dependencies, routed through the `IssueTracker`, `PullRequestManager`, `LabelManager`, and `RateLimiter` backend interfaces.

Expected `gh` responsibilities (for the GitHub backend):

- detect authentication state
- list open issues for a repository
- post start/progress/error comments
- optionally inspect issue metadata needed for scheduling

This keeps the Go code smaller and delegates auth/session handling to the installed GitHub CLI. Future backends (Linear, Jira) will use their own API clients behind the same interfaces.

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
