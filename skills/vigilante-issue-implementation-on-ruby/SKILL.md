---
name: vigilante-issue-implementation-on-ruby
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Ruby repository with Bundler, test, lint, and security guidance.
---

# Vigilante Ruby Issue Implementation

## Focus
- Read the prompt for detected tech stacks, Ruby process hints, and Ruby security guidance before changing code.
- Prefer repository-defined Ruby workflows over guessed framework defaults.
- Keep changes scoped to the issue and do not broaden into unrelated lint, dependency, or style cleanup.

## Ruby Tooling Workflow
- **Bundler**: run Ruby tooling through Bundler-managed commands when the repository uses Bundler, such as `bundle exec <command>` or documented binstubs. Respect the committed lockfile and avoid ad hoc gem execution outside the repo's dependency context.
- **Testing**: start with the repository's standard test entrypoint for the touched area, such as `bundle exec rspec`, `bundle exec ruby -Itest`, `bundle exec rake test`, Rails test tasks, or another documented command. Prefer targeted suites first, then widen only when needed.
- **Linting and style**: use the repository's established lint or style tooling. When RuboCop is configured, run the repo-standard RuboCop command through Bundler. Do not introduce new lint tooling unless the issue specifically requires it.
- **Dependency and security audits**: run `bundle audit` or `bundler-audit` when it is installed or already part of the repository workflow and the change touches dependencies or security-sensitive code. For Rails applications, run Brakeman when it is already configured or documented for the repo. If those tools are not present, note that and continue with the other validation paths.
- **Dependencies**: keep `Gemfile.lock` or other Bundler lockfiles in sync with dependency changes. Prefer minimal dependency churn and respect the repo's existing gem sources and update policy.

## Ruby Secure Coding
- Avoid unsafe deserialization of untrusted data. Prefer safe formats and parser modes over `Marshal.load`, unrestricted YAML loading, or similar dangerous object materialization paths.
- Avoid shell injection by preferring APIs that accept explicit argv arrays and by validating or escaping untrusted input when shelling out is unavoidable.
- Do not store secrets, credentials, or tokens in source files, fixtures, seeds, or committed environment files.
- For Rails or Rack applications, preserve secure defaults around strong parameters, CSRF protections, escaping, and framework security configuration unless the issue explicitly requires changing them.
- Prefer current maintained gems and standard-library capabilities over custom security-sensitive code when the repository already has an established safe path.

## Mixed-Language Repositories
- A Ruby repository may include frontend assets, Node.js tooling, or other languages alongside the Ruby application.
- Scope Ruby validation to Ruby files and repo-defined Ruby commands for Ruby-scoped changes. When the issue also touches frontend or other language code, validate each side with its respective toolchain.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, framework docs, CI config) remain authoritative when they are more specific than the generic Ruby guidance in this skill.
