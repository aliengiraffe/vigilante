<p align="center">
  <img src=".github/assets/logo.png" alt="vigilante logo" width="240">
</p>

# vigilante

[![Release](https://img.shields.io/github/v/release/aliengiraffe/vigilante?display_name=tag)](https://github.com/aliengiraffe/vigilante/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/nicobistolfi/vigilante)](https://goreportcard.com/report/github.com/nicobistolfi/vigilante)
[![Go Package Search](https://img.shields.io/badge/go-package%20search-00ADD8?logo=go&logoColor=white)](https://pkg.go.dev/search?q=github.com%2Fnicobistolfi%2Fvigilante)
[![License](https://img.shields.io/github/license/aliengiraffe/vigilante)](https://github.com/aliengiraffe/vigilante/blob/main/LICENSE)
[![Release Workflow](https://img.shields.io/github/actions/workflow/status/aliengiraffe/vigilante/release.yml?label=release)](https://github.com/aliengiraffe/vigilante/actions/workflows/release.yml)

`vigilante` is a local control plane for autonomous software delivery. It watches repositories, selects eligible work items, prepares isolated git worktrees, launches a supported coding-agent CLI, and keeps the issue tracker updated while the work moves toward a pull request. It is calm under pressure, even when the issue queue is reproducing like gremlins after midnight.

It is the orchestration layer around agents such as `codex`, `claude`, and `gemini`, not the model itself. Vigilante owns scheduling, worktree isolation, backend coordination, and recovery so a repository checkout behaves like a controlled worker instead of a loose collection of scripts.

[Docs](DOCS.md) · [Closed Issues](https://github.com/aliengiraffe/vigilante/issues?q=is%3Aissue%20state%3Aclosed) · [Releases](https://github.com/aliengiraffe/vigilante/releases) · [Contributing](CONTRIBUTING.md)

Start here: install `vigilante`, run `vigilante setup`, then clone and auto-register a repo with `vigilante clone <repo>` or register an existing checkout with `vigilante watch /path/to/repo`.

## Install

Install with Homebrew:

```sh
brew install vigilante
```

Requirements:

- `git`
- `gh` authenticated against the GitHub account Vigilante should operate with
- one supported coding-agent CLI installed locally: `codex`, `claude`, or `gemini`

Recommended machine setup:

```sh
vigilante setup --provider codex
```

## Quick Start

Register a repository and let Vigilante manage it:

```sh
vigilante clone git@github.com:owner/repo.git
```

Useful follow-up commands:

```sh
vigilante list
vigilante status
vigilante service restart
vigilante daemon run --once
```

Typical first-run flow:

```sh
brew install vigilante
vigilante setup --provider codex
vigilante clone git@github.com:owner/hello-world-app.git
vigilante daemon run --once
```

## What Vigilante Does

- Treats project-management work items as the queue for autonomous software delivery.
- Selects eligible issues using repository configuration, assignees, labels, and concurrency limits.
- Creates one isolated git worktree per issue so the main checkout stays stable.
- Chooses the right execution skill from repository shape and local context.
- Launches a supported coding-agent CLI under a consistent lifecycle.
- Tracks progress, failures, and pull-request state through the issue tracker and local session state.
- Recovers, resumes, redispatches, and cleans up runs without duplicating work.

## How It Works

At a high level, Vigilante runs this loop for each watched repository:

1. Resolve the repository and discover its remote.
2. Read the configured issue-tracking backend and fetch open work items.
3. Filter to issues that are eligible and not already being handled.
4. Create an isolated worktree and issue branch.
5. Launch the selected coding-agent CLI with the repo-aware implementation skill.
6. Track progress locally and post execution updates back to the issue tracker.
7. Monitor the linked pull request and clean up or recover the session as needed.

GitHub is the only fully implemented backend today. The architecture already separates issue tracking, pull requests, labels, and rate limits so backends such as Linear and Jira can be added without rewriting the orchestration loop.

## Package Hardening

Vigilante includes a deterministic, code-driven package hardening scan for pull requests that modify `package.json` files. When a PR branch is pushed, Vigilante checks for lockfile presence, runs `npm audit`, flags non-exact dependency ranges, and verifies that CI workflows use deterministic install commands. If issues are found, Vigilante posts a structured comment on the PR with findings and applies the `vigilante:flagged-security-review` label. The comment includes an **implement fixes** checkbox that triggers an automated remediation session when checked.

> **Note:** Package hardening currently applies only to repositories with a supported JavaScript/TypeScript/Node.js tech stack. Support for additional ecosystems is expected to expand over time.

The feature is enabled by default and can be toggled with the `package_hardening_enabled` field in `config.json`. For operational details including trigger conditions, checks performed, and remediation flow, see [DOCS.md](DOCS.md).

## Key Commands

- `vigilante setup`: verify dependencies, install bundled skills, and install the managed service
- `vigilante clone <repo> [<path>]`: clone a repository with `git clone` semantics and auto-add it to watch targets
- `vigilante watch <path>`: register a local repository for issue monitoring
- `vigilante list`: show watched repositories
- `vigilante status`: show service health, watched repos, active sessions, and rate-limit state
- `vigilante logs`: inspect local daemon and per-issue logs
- `vigilante cleanup`, `vigilante redispatch`, `vigilante resume`: recover or restart stuck work safely
- `vigilante daemon run`: run the watcher loop in the foreground

### Fork Mode

Use fork mode when the authenticated GitHub identity should open pull requests from a fork instead of pushing issue branches directly to the upstream watched repository.

```sh
vigilante watch --fork ~/hello-world-app
```

What changes in fork mode:

- Vigilante uses `gh api user` to resolve the authenticated GitHub login when `--fork-owner` is not set.
- It creates or reuses `<fork-owner>/<repo>` through the GitHub API and verifies that the existing repository is actually a fork of the watched upstream repository.
- It adds or updates a local git remote named `fork` that points at the fork repository.
- Issue worktrees still use the upstream repository for issue context, base branch selection, and pull request target selection.
- Coding agents still use the normal issue-implementation skill flow, but pushes go to the `fork` remote and pull requests are opened back to the upstream repository.

Use an explicit owner when the fork should live under a bot or organization account:

```sh
vigilante watch --fork --fork-owner my-bot-org ~/hello-world-app
```

Operational notes:

- `--fork-owner` requires `--fork`.
- The authenticated `gh` account must be able to create or access the selected fork and open a pull request back to the upstream repository.
- Existing branch tracking stays deterministic inside the worktree: the issue branch pushes to `fork`, while the pull request base remains the watched repository's configured base branch.

For command details and full flags, see [DOCS.md](DOCS.md).

## Architecture At A Glance

Vigilante keeps orchestration backend-neutral through a small set of interfaces:

- `IssueTracker`: work item listing, details, comments, and operator commands
- `PullRequestManager`: PR discovery, merge state, and branch lifecycle
- `LabelManager`: repository and issue label synchronization
- `RateLimiter`: optional API quota awareness

That lets a watch target mix concerns such as issue tracking on one system and pull requests on another while keeping the execution loop stable.

## More Docs

The full reference moved to [DOCS.md](DOCS.md), including:

- installation details and development mode
- full command reference and expected behaviors
- local state layout and logs
- issue selection, labeling, and pull-request maintenance
- package hardening trigger conditions, checks, remediation flow, and config toggle
- headless agent execution contract
- GitHub integration, worktree strategy, and service behavior
- CI, releases, and implementation status notes
