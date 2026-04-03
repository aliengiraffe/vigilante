---
name: vigilante-issue-implementation-on-rust
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Rust repository with Cargo, Clippy, fmt, and security guidance.
---

# Vigilante Rust Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and Rust security guidance before changing code.
- Follow idiomatic Rust conventions and keep changes scoped to the issue rather than broad cleanup.
- Preserve repository-specific toolchain, workspace, lint, and MSRV constraints when they are defined.

## Rust Tooling Workflow
- **Formatting**: run `cargo fmt` on touched Rust code before committing. Respect `rustfmt.toml` when present and do not hand-format Rust code.
- **Testing**: run targeted `cargo test` commands for the affected crate, package, or workspace member first, then broaden scope only when changes cross crate boundaries or the correct target is unclear. Respect repository feature flags and default workspace behavior.
- **Linting**: run `cargo clippy` when it is available and relevant to the changed Rust code. Prefer the repository's existing Clippy configuration and fix meaningful warnings instead of suppressing them broadly.
- **Security and supply-chain checks**: run `cargo-audit` or `cargo deny` when those tools are already installed or configured and the change touches dependencies or security-sensitive code. If they are unavailable, note that and continue with the repository's existing validation rather than fabricating output.
- **Dependencies**: keep dependency additions minimal, review default features carefully, and prefer the Rust standard library when it covers the need. Respect `Cargo.lock`, workspace dependency policy, and any MSRV or toolchain pinning before introducing new crates or language features.

## Rust Safety and Code Quality
- Keep `unsafe` code minimal, justified, and tightly scoped. Do not introduce `unsafe` when a safe standard approach is available.
- Prefer returning `Result` and propagating errors cleanly over panicking in normal control flow.
- Use feature flags deliberately. Avoid enabling broad optional features unless the issue requires them.
- Respect existing lint levels and crate-level policies such as `#![forbid(unsafe_code)]`, `#![deny(warnings)]`, or repository-specific Clippy settings.

## Workspace and Mixed-Language Repositories
- Follow the repository's Cargo workspace layout and package-selection conventions rather than defaulting to full-workspace commands.
- Use `Cargo.toml`, `Cargo.lock`, `rust-toolchain.toml`, and related repo config as the source of truth for package boundaries, toolchain version, and validation scope.
- When the repository also contains other stacks such as Node.js or Go, scope Cargo commands to Rust crates only and validate the other side with its own toolchain only when the issue actually touches it.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- If validation, tool setup, or push/PR execution fails, inspect `vigilante logs --repo <owner/name> --issue <n>` before retrying or reporting the blocker so the session transcript guides the next safe step.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic Rust guidance in this skill.
