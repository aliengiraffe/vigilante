---
name: vigilante-issue-implementation-on-dotnet
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a .NET/C# repository with analyzer, test, and security guidance.
---

# Vigilante .NET Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and .NET security guidance before changing code.
- Follow the repository's existing .NET workflow, solution or project structure, analyzer configuration, and formatting conventions.
- Keep changes scoped to the issue and do not broaden into unrelated cleanup or package churn.

## .NET Tooling Workflow
- **Formatting**: use the repository's established formatting path. When `dotnet format` is the standard SDK workflow, run it on the affected project or solution scope before committing.
- **Testing**: run targeted `dotnet test` for the affected test project, project, or solution first, then broaden only when the change crosses project boundaries or the repository workflow requires it.
- **Analyzers**: respect built-in .NET analyzers, nullable warnings, `.editorconfig`, `Directory.Build.props`, and repo-defined warning severities. Do not weaken analyzer settings without a narrow, documented reason.
- **Package auditing**: when dependency changes are involved or package risk is relevant, use the repository's standard NuGet restore or audit flow and review advisories when auditing is enabled.
- **Dependencies**: prefer framework or standard-library features when they cover the need. Avoid unrelated package version churn.

## .NET Coding Style
- Follow existing project patterns for namespaces, file layout, and test structure.
- Preserve nullable reference type correctness and prefer explicit null handling over suppression.
- Prefer straightforward error handling and clear APIs over reflection-heavy or overly abstracted changes unless the issue requires it.
- Keep public API changes minimal and aligned with the issue.

## Security
- Do not commit secrets, tokens, certificates, or environment-specific credentials.
- Prefer configuration binding, options patterns, environment variables, or local user-secrets workflows over hard-coded settings.
- In ASP.NET Core code, preserve secure defaults for authentication, authorization, antiforgery or CSRF protections where applicable, HTTPS, and sensitive-data handling.
- Respect security analyzers and dependency advisories when the repository enables them.

## Mixed-Language Repositories
- A .NET repository may also include frontend or other non-.NET code in the same checkout.
- Scope `.NET` validation (`dotnet test`, `dotnet format`, analyzers, restore or audit flows) to affected .NET projects or solutions only.
- When the issue also touches another stack, validate that side with its own native tooling rather than assuming `dotnet` covers the whole repository.
- Do not assume a .NET repository is C#-only; use the prompt's detected tech stacks and process hints.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config, solution-level build props) remain authoritative when they are more specific than the generic .NET guidance in this skill.
