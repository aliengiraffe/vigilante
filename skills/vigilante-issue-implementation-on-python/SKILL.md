---
name: vigilante-issue-implementation-on-python
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Python repository with idiomatic tooling and security guidance.
---

# Vigilante Python Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and Python security guidance before changing code.
- Follow idiomatic Python conventions already established by the repository and keep changes scoped to the issue.
- Prefer the repository's existing environment, dependency, formatting, linting, typing, and test workflows over inventing a new stack.

## Python Tooling Workflow
- **Environment**: use the repository's documented bootstrap or environment workflow first. When no repo-specific workflow exists, prefer isolated environments such as `venv` rather than ad hoc global installs.
- **Formatting and linting**: run the repository's established formatter and linter for touched files. When the repository already uses Ruff, prefer `ruff format` and `ruff check`; when it uses Black, use `black`. Do not introduce repo-wide formatting churn unrelated to the issue.
- **Typing**: run the repository's existing typing checks when present, such as `mypy`, pyright-style tooling, or equivalent configured commands.
- **Testing**: run targeted `pytest` or repo-standard test commands for the changed area first, then broaden scope when needed.
- **Dependency and package security**: when dependency or packaging changes are involved, run repo-standard audit tooling. Use `pip-audit` when it is already part of the repo workflow or otherwise clearly available and relevant.
- **Dependencies**: prefer standard library modules when they cover the need. Keep dependency manifests and lockfiles consistent when adding or updating packages.

## Idiomatic Python
- Follow existing project conventions for layout, imports, and naming instead of forcing a new style.
- Prefer small, explicit functions, early returns, and straightforward exception handling.
- Add or update docstrings only where the repository already expects them or where a public API change needs clear documentation.
- Avoid broad cleanup or modernization unrelated to the issue.

## Security
- Prefer `secrets` over `random` for security-sensitive values.
- Avoid unsafe `pickle` usage or deserializing untrusted data with Python-native object loaders.
- Be careful with `subprocess`: prefer explicit argument lists, avoid `shell=True` unless required and safely constrained, and validate any external input passed to commands.
- Treat file paths and untrusted input defensively to avoid traversal, injection, and unintended file access.
- Do not store secrets, tokens, or credentials in source files.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.

## Repository Overrides
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config, tool config files) remain authoritative when they are more specific than the generic Python guidance in this skill.
