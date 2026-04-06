# Sandbox

Sandbox is a security feature that runs coding agents inside isolated Docker containers on your local machine. Instead of giving agents direct access to the host environment, Vigilante uses the Docker API or a compatible container runtime API to spin up locked-down execution environments where agents can work on a repository without reaching anything else.

## Why Sandbox Exists

Coding agents need broad tool access to be effective — they read files, run tests, install packages, spin up services, and interact with GitHub. That breadth of access creates risk when the agent runs directly on the host. A misconfigured or misbehaving agent could read credentials, access unrelated repositories, or interact with services it was never intended to touch.

Sandbox draws a hard boundary. The agent operates inside a container that has everything it needs to do its job and nothing it does not.

## How It Works

When Vigilante dispatches an issue in sandbox mode, it provisions a Docker container through the Docker API or a compatible runtime. The container is purpose-built for coding-agent execution:

- **Pre-installed coding agents.** Codex, Claude Code, and Gemini CLI are available inside the container. The selected provider runs the same way it would on the host.
- **Volume sync at spinoff.** Vigilante mounts the repository code and each agent's configuration into the container at creation time. The agent starts with the same settings, credentials, and codebase state it would have locally.
- **Docker-in-Docker.** The container includes DinD capability, so the coding agent can spin up additional containers for databases, caches, message brokers, and other services required during implementation. Service dependencies stay inside the sandbox boundary.

## GitHub CLI Reverse Proxy

The most important security mechanism in sandbox mode is how GitHub access works inside the container.

The host machine's `gh` CLI is not mapped into the sandbox. Instead, the container receives a `gh` binary that acts as a mirror, forwarding CLI commands to a Vigilante API running on the host. That API enforces repository-scoped access control before executing anything.

What this means in practice:

- The coding agent uses `gh` normally inside the container. From the agent's perspective, the CLI behaves as expected.
- The Vigilante API intercepts every command and only permits operations against the specific repository the agent is working on.
- Issue listing, PR creation, comment posting, and code browsing are all scoped to the assigned repository.
- Requests targeting any other repository are rejected, regardless of what the host `gh` CLI has access to.

This reverse-proxy design ensures that even if the host GitHub identity has organization-wide or cross-repository access, the sandboxed agent cannot see issues, pull requests, or code from repositories outside its assignment.

## Security Model

What agents **can** do inside the sandbox:

- Read and modify the assigned repository codebase.
- Run the repository's build, test, and lint toolchains.
- Use Docker to start local service dependencies such as databases or caches.
- View issues and pull requests for the assigned repository.
- Post comments and push commits to the assigned repository.
- Use their own agent configuration and installed tools.

What agents **cannot** do:

- Access files, credentials, or processes on the host machine outside the mounted codebase.
- View or interact with issues, pull requests, or code from any other repository.
- Use the host `gh` CLI identity to perform operations beyond the assigned repository scope.
- Reach network services on the host or local network unless explicitly configured.
- Escape the container boundary to affect the host environment.

## Why It Matters

Sandbox mode lets Vigilante run coding agents with full tool access while keeping the blast radius of any single session limited to exactly one repository. The agent gets the environment it needs to be productive. The operator gets confidence that autonomous execution cannot leak across repository boundaries, access unrelated credentials, or interact with infrastructure it was never assigned to.
