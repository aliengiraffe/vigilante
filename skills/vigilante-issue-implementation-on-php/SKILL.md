---
name: vigilante-issue-implementation-on-php
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a PHP repository with Composer, static analysis, and security guidance.
---

# Vigilante PHP Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and PHP security guidance before changing code.
- Follow repo-standard Composer, testing, formatting, and static-analysis workflows.
- Prefer repo-defined framework and tooling conventions over forcing a universal PHP stack.
- Keep changes scoped to the issue and do not broaden into unrelated style or lint fixes.

## PHP Tooling Workflow
- **Composer**: use Composer-managed commands and dependency workflows. Run `composer install` for reproducible installs from `composer.lock`. Run `composer update` only when intentionally upgrading dependencies.
- **Testing**: run targeted tests for changed code first using `vendor/bin/phpunit --filter ClassName` or the framework-native test command (e.g., `php artisan test`, `vendor/bin/pest`). Use broader `vendor/bin/phpunit` when changes cross module boundaries. Respect the repository's test configuration (`phpunit.xml`, `phpunit.xml.dist`).
- **Static analysis**: use the repository's established static-analysis tools. When PHPStan is configured (`phpstan.neon`, `phpstan.neon.dist`), run `vendor/bin/phpstan analyse`. When Psalm is configured (`psalm.xml`, `psalm.xml.dist`), run `vendor/bin/psalm`. Do not introduce a different analyzer unless the issue specifically requires it.
- **Formatting**: use the repository's established code-style tool. When PHP CS Fixer is configured (`.php-cs-fixer.php`, `.php-cs-fixer.dist.php`), run `vendor/bin/php-cs-fixer fix`. When PHP_CodeSniffer is configured (`phpcs.xml`, `phpcs.xml.dist`, `.phpcs.xml`), run `vendor/bin/phpcs` to check and `vendor/bin/phpcbf` to fix. Do not hand-format PHP code when an automated tool is available.
- **Dependencies**: run `composer audit` after dependency changes to check for known vulnerabilities. Review `composer.lock` changes for unexpected additions or version shifts.

## Security
- Use `password_hash()` with `PASSWORD_DEFAULT` or `PASSWORD_BCRYPT` for password storage, and `password_verify()` to check passwords. Never use `md5()`, `sha1()`, or `crypt()` directly for passwords.
- Use parameterized queries or the framework's query builder to prevent SQL injection — never interpolate user input into raw SQL.
- Use context-appropriate output encoding (`htmlspecialchars()` with `ENT_QUOTES`, framework template escaping) to prevent XSS.
- Avoid `unserialize()` on untrusted data — use `json_decode()` and `json_encode()` for data interchange. When `unserialize()` is unavoidable, restrict allowed classes with the `allowed_classes` option.
- Do not store secrets, tokens, or credentials in source files. Use environment variables or framework-native secret management.
- Use framework-provided CSRF protection for state-changing requests.

## Mixed-Language Repositories
- A PHP repository may include a frontend layer such as a React, Vue, or other JavaScript framework colocated with the PHP backend.
- Scope PHP tooling (Composer, PHPUnit, PHPStan, Psalm, PHP CS Fixer) to PHP source files only. Do not run PHP tools against frontend code.
- When the repository also has a Node.js or TypeScript frontend, respect its own toolchain (package manager, bundler, linter, test runner) for frontend-scoped changes. Check the prompt for detected tech stacks and process hints.
- When an issue touches both PHP backend and frontend code, validate each side with its own toolchain rather than validating only one side.
- Do not assume a PHP repository is PHP-only. Read process hints and workspace signals in the prompt to understand the full repository structure.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic PHP guidance in this skill.
