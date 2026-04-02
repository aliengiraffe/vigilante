---
name: vigilante-issue-implementation-on-java-kotlin
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Java or Kotlin repository with JVM build-tool, style, and secure-coding guidance.
---

# Vigilante Java/Kotlin Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and JVM build signals before changing code.
- Keep changes scoped to the issue and prefer the repository's actual Gradle or Maven workflow over invented commands.
- In mixed Java/Kotlin repositories, follow the conventions of each touched language instead of forcing one style across the whole repo.

## JVM Tooling Workflow
- **Build/test validation**: use repository-standard Gradle or Maven tasks for the smallest affected scope first, then broaden only when shared modules or integration boundaries require it. Prefer commands the repo already documents or wires into CI.
- **Formatting and style**: use the repository's existing formatter or style tooling when present. For Java, this may be Checkstyle, Spotless, or equivalent. For Kotlin, this may be ktlint, detekt formatting, Spotless, or equivalent. Do not introduce a new formatter or broad cleanup just because a tool exists in the ecosystem.
- **Static analysis**: run repo-standard static analysis when configured, such as SpotBugs, Checkstyle, detekt, ktlint, Error Prone, PMD, or equivalent. If none is configured, rely on the repository's normal build/test path rather than inventing a new lint stack.
- **Dependencies**: prefer the standard library or existing dependencies when they already cover the need. When adding or updating dependencies, keep Gradle or Maven metadata consistent and preserve any dependency-locking or version-catalog workflow already used by the repo.

## Java/Kotlin Style
- **Java**: follow standard Java conventions already used by the repository. Keep classes cohesive, prefer explicit names, use checked or unchecked exceptions consistently with local patterns, and avoid unnecessary framework abstraction.
- **Kotlin**: follow Kotlin coding conventions for naming, file organization, null-safety, and expression-oriented style. Prefer immutable values with `val` unless mutation is required. Use idiomatic scope functions only when they improve clarity.
- **Documentation**: when touching documented APIs, preserve the repository's documentation style. Use Javadoc or KDoc only where the codebase already expects it or where exported/public API changes need it.

## Security
- Prefer framework-native secure defaults when the repository uses Spring, Micronaut, Quarkus, or similar JVM frameworks.
- Avoid insecure deserialization patterns, especially Java native serialization or framework object binding on untrusted input without explicit constraints.
- Validate untrusted input at boundaries and watch for SSRF, path traversal, template injection, SQL injection, and unsafe reflection or classloading patterns.
- Use standard JDK/JVM security APIs correctly; avoid rolling custom crypto. Do not store secrets, tokens, or credentials in source files or test fixtures.
- Preserve dependency-locking, checksum, or version-catalog controls already used by the repository instead of bypassing them during implementation.

## Mixed-Language Repositories
- A JVM repository may also include JavaScript, TypeScript, Go, or infrastructure code.
- Scope JVM validation to the touched Gradle/Maven modules or source sets, and use other toolchains only for the files they own.
- When an issue touches both JVM code and another stack, validate each side with its native tooling instead of treating the JVM toolchain as universal.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config, Gradle conventions, Maven wrapper usage) remain authoritative when they are more specific than this generic JVM guidance.
