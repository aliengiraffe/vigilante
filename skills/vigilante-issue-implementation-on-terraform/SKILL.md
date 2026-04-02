---
name: vigilante-issue-implementation-on-terraform
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Terraform repository with fmt, validate, and secret-safe infrastructure guidance.
---

# Vigilante Terraform Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and Terraform security guidance before changing code.
- Follow idiomatic Terraform conventions for resource naming, file layout, and formatting.
- Keep changes scoped to the issue and do not broaden into unrelated refactoring or architecture redesign.

## Terraform Tooling Workflow
- **Formatting**: run `terraform fmt -recursive` on all touched Terraform directories before committing. Do not hand-format Terraform code — let the standard formatter handle layout.
- **Validation**: run `terraform validate` in each root module directory that contains changed files. Ensure provider and backend blocks are present or that validation is run with `-no-color` for clean output. If validation requires initialized providers, run `terraform init -backend=false` first to install providers without configuring remote state.
- **Planning**: do not run `terraform plan` or `terraform apply` unless the repository defines a safe local workflow for it (e.g., a Makefile target, CI script, or documented local plan process). Assume cloud credentials are not available in the agent environment.
- **Linting**: use the repository's established lint tooling. When `tflint` is configured (`.tflint.hcl`), run `tflint` on touched modules. When `tfsec` or `trivy` is configured, run the appropriate scanner. Do not introduce a different linter unless the issue specifically requires it. If no project linter is configured, `terraform validate` is sufficient.
- **Policy checks**: when the repository uses policy tools such as OPA/Conftest, Sentinel, or Checkov, respect their configuration and run them if a safe local path exists. Do not skip policy checks to make changes pass.

## Terraform Style
- Use `snake_case` for resource names, variable names, output names, and local values.
- Keep resource blocks focused — one logical resource per block.
- Group related resources in files named by purpose (e.g., `main.tf`, `variables.tf`, `outputs.tf`, `providers.tf`).
- Use `variables.tf` for input variable declarations and `outputs.tf` for output declarations.
- Pin provider versions with `required_providers` blocks and use pessimistic version constraints (e.g., `~> 5.0`).
- Pin module versions when sourcing from a registry. Avoid `ref=main` for git-sourced modules in production configurations.

## Security and State
- Do not store secrets, tokens, credentials, or sensitive values in `.tf` files or `terraform.tfvars` committed to the repository.
- Mark sensitive variables and outputs with `sensitive = true`.
- Do not configure remote state backends with inline credentials. Use environment variables or external credential helpers.
- Do not assume cloud credentials are available. If a change requires provider authentication, document the requirement rather than embedding credentials.
- Treat state files (`terraform.tfstate`, `*.tfstate.backup`) as sensitive — they must never be committed. Verify `.gitignore` covers state files.
- Use `prevent_destroy` lifecycle rules on critical resources when appropriate.
- Avoid wildcard IAM policies and overly permissive security group rules. Prefer least-privilege access patterns.

## Module Hygiene
- When creating or modifying modules, include `variables.tf`, `outputs.tf`, and a `README.md` if the module is intended for reuse.
- Validate module inputs with `validation` blocks on variables where constraints are meaningful.
- Prefer the Terraform registry or organization-internal module sources over ad-hoc inline resources for common patterns.

## Mixed-Language Repositories
- A Terraform repository may include application code, CI/CD configuration, scripts, or other IaC tools alongside Terraform.
- Scope Terraform tooling (`terraform fmt`, `terraform validate`, `tflint`, `tfsec`) to `.tf` files and Terraform directories only. Do not run Terraform tools against non-Terraform code.
- When the repository also has application code, respect its own toolchain for application-scoped changes.
- When an issue touches both Terraform and application code, validate each side with its own toolchain.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic Terraform guidance in this skill.
