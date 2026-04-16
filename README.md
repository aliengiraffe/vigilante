<p align="center">
  <img src=".github/assets/logo_transparent.png" alt="vigilante logo" width="240">
</p>

# vigilante

[![Release](https://img.shields.io/github/v/release/aliengiraffe/vigilante?display_name=tag)](https://github.com/aliengiraffe/vigilante/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/nicobistolfi/vigilante)](https://goreportcard.com/report/github.com/nicobistolfi/vigilante)
[![Go Package Search](https://img.shields.io/badge/go-package%20search-00ADD8?logo=go&logoColor=white)](https://pkg.go.dev/search?q=github.com%2Fnicobistolfi%2Fvigilante)
[![License](https://img.shields.io/github/license/aliengiraffe/vigilante)](https://github.com/aliengiraffe/vigilante/blob/main/LICENSE)
[![Release Workflow](https://img.shields.io/github/actions/workflow/status/aliengiraffe/vigilante/release.yml?label=release)](https://github.com/aliengiraffe/vigilante/actions/workflows/release.yml)

`vigilante` is a sandbox-first orchestration layer for coding agents. It treats agents as untrusted by default, isolates every issue in its own git worktree, enforces operator-controlled workflow boundaries, and drives work from GitHub issue to pull request with an audit trail.

Vigilante is the control plane around tools such as `codex`, `claude`, and `gemini`, not the model itself. It owns issue selection, worktree setup, lifecycle management, recovery, and guardrails so a repository checkout behaves like a controlled worker instead of a loose collection of scripts.

[Docs](DOCS.md) · [Sandbox](SANDBOX.md) · [Closed Issues](https://github.com/aliengiraffe/vigilante/issues?q=is%3Aissue%20state%3Aclosed) · [Releases](https://github.com/aliengiraffe/vigilante/releases) · [Contributing](CONTRIBUTING.md)

## Why Vigilante Exists

Coding agents need broad file, shell, network, and GitHub access to be useful. That is also what makes them risky.

Vigilante narrows that risk by making the orchestrator, not the agent, responsible for policy:

- one isolated git worktree per issue
- issue-tracker-driven execution instead of ad hoc prompts
- explicit lifecycle controls for start, resume, redispatch, and cleanup
- scoped execution with optional Docker sandboxing
- issue and PR updates that leave an operator-visible trail

## How It Works

Vigilante keeps the path from issue to PR short and deterministic:

1. Watch a local repository and connect it to GitHub.
2. Select eligible issues using labels, assignees, and concurrency rules.
3. Create a fresh branch in an isolated git worktree for that issue.
4. Launch a supported coding-agent CLI with repository-aware instructions.
5. Track progress, post issue comments, open or monitor the pull request, and recover or clean up sessions when needed.

GitHub is the fully implemented backend today. The backend interfaces are separated so issue tracking, pull requests, labels, and rate limits do not have to be hard-wired into the core orchestration loop.

## Guardrails And Capabilities

- **Worktree isolation.** Every issue gets its own branch and worktree so the main checkout stays untouched.
- **Operator-driven workflow.** Issues are the queue, comments are the control surface, and PRs remain the review boundary.
- **Provider-neutral execution.** Use supported coding-agent CLIs such as `codex`, `claude`, or `gemini`.
- **Recovery built in.** Resume, redispatch, recreate, and cleanup flows are part of the lifecycle instead of afterthought scripts.
- **Auditability.** Vigilante keeps local session state and posts progress back to the issue tracker.
- **Optional sandbox mode.** Run sessions inside isolated Docker containers for a second isolation layer beyond git worktrees.
- **Package hardening checks.** Pull requests that change `package.json` can trigger deterministic dependency and workflow checks for JavaScript and TypeScript repositories.

## Quick Start

Install Vigilante:

```sh
brew install vigilante
```

Requirements:

- `git`
- `gh` authenticated for the GitHub account Vigilante should operate with
- one supported coding-agent CLI installed locally: `codex`, `claude`, or `gemini`

Prepare the machine:

```sh
vigilante setup --provider codex
```

Register a repository:

```sh
vigilante watch ~/path/to/repo
```

Run the watcher once in the foreground:

```sh
vigilante daemon run --once
```

Useful follow-up commands:

```sh
vigilante list
vigilante status
vigilante logs
vigilante service restart
```

If you want Vigilante to clone and register the repository in one step:

```sh
vigilante clone git@github.com:owner/repo.git
```

## Sandbox Mode

Sandbox mode adds a Docker isolation layer on top of worktree isolation:

- the coding agent runs inside an isolated container
- the assigned repository worktree is mounted into that container
- GitHub access is mediated by Vigilante rather than handed over as broad host access
- session cleanup is tied to the issue lifecycle

Enable sandbox mode for a watched repository:

```sh
vigilante watch --sandbox ~/path/to/repo
```

Or set it globally in `~/.vigilante/config.json`:

```json
{
  "sandbox_enabled": true,
  "sandbox_image": "vigilante-sandbox:latest"
}
```

Build the default sandbox image locally:

```sh
docker build -t vigilante-sandbox:latest .
```

For the deeper architecture, credential model, and design notes, see [SANDBOX.md](SANDBOX.md).

## Key Commands

- `vigilante setup`: verify local prerequisites and prepare the machine
- `vigilante clone <repo> [<path>]`: clone a repository and register it as a watch target
- `vigilante watch <path>`: register an existing local repository
- `vigilante list`: show watched repositories
- `vigilante status`: show watched repos, service health, and running sessions
- `vigilante logs`: inspect daemon and per-issue logs
- `vigilante cleanup`, `vigilante redispatch`, `vigilante recreate`, `vigilante resume`: recover stuck or failed work safely
- `vigilante daemon run`: run the watcher loop in the foreground
- `vigilante service restart`: restart the installed background service
- `vigilante issue create`: turn a prompt into an implementation-ready GitHub issue

## More Docs

The detailed reference lives in [DOCS.md](DOCS.md), including:

- command reference and operational behavior
- watch target configuration, issue selection, and backend support
- fork mode and branch strategy
- local state, logs, and recovery flows
- package hardening behavior and configuration
- provider execution contract and repository-aware skills
- GitHub integration, PR lifecycle, and service behavior
