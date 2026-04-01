---
name: vigilante-issue-implementation-on-kubernetes
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Kubernetes-focused repository with manifest hardening and workload security guidance.
---

# Vigilante Kubernetes Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and Kubernetes security guidance before changing manifests.
- Treat Kubernetes manifest changes as operationally sensitive — not as generic YAML edits.
- Keep changes scoped to the issue and do not broaden into unrelated cluster-ops or security redesign work.

## Manifest Hygiene
- Use recommended Kubernetes labels (`app.kubernetes.io/name`, `app.kubernetes.io/version`, etc.) on all new resources.
- Validate manifest YAML syntax before committing. Use `kubectl --dry-run=client -o yaml` or an equivalent offline validator when available.
- Prefer `kustomize` overlays or Helm values for environment-specific configuration rather than duplicating manifests.
- Keep resource definitions focused — one resource per file when practical, with clear naming.

## Service Account Hygiene
- Do not use the `default` service account for workloads. Create dedicated service accounts scoped to the workload's needs.
- Set `automountServiceAccountToken: false` on pods and service accounts that do not need API access.
- When a workload requires API access, bind the minimum RBAC permissions to its dedicated service account.

## Pod and Container Security Context
- Set `runAsNonRoot: true` and specify a numeric `runAsUser` in the pod or container `securityContext`.
- Set `allowPrivilegeEscalation: false` on containers.
- Set `readOnlyRootFilesystem: true` where the application supports it, using `emptyDir` volumes for writable paths.
- Drop all capabilities and add back only what is required: `securityContext.capabilities.drop: ["ALL"]`.
- Prefer `Restricted` Pod Security Standards when the workload allows it.

## RBAC and Permissions
- Follow least-privilege principles: prefer `Role` and `RoleBinding` (namespace-scoped) over `ClusterRole` and `ClusterRoleBinding` unless the workload genuinely needs cluster-wide access.
- Avoid wildcards (`*`) in RBAC rules for verbs, resources, or API groups.
- When the issue only requires application-level changes, do not introduce or modify cluster-scoped resources.

## Image Security
- Use image digests or pinned tags rather than `latest` or other mutable tags.
- Prefer images from trusted registries. Note when an image source is unverified.
- Be aware of image scanning and admission policies when the repository documents them.

## Network Policy and Resource Management
- When touching network-facing workloads, check whether a `NetworkPolicy` exists and preserve or extend it rather than removing restrictions.
- Set resource `requests` and `limits` on containers to prevent unbounded resource consumption.
- Do not remove existing resource constraints without explicit justification in the issue.

## Scope Guardrails
- Do not make broad cluster-wide changes when the issue only requires application-level manifest updates.
- Do not introduce cluster-admin RBAC, node-level access, or host-namespace usage unless the issue specifically requires it.
- Preserve existing security posture — improve it where relevant to the issue, but do not weaken it.

## Mixed-Stack Repositories
- A Kubernetes repository may also contain application code (Go, Node.js, Python, etc.) alongside manifests.
- Scope Kubernetes manifest guidance to YAML/Helm/Kustomize files. Do not apply manifest validation to application source code.
- When an issue touches both application code and Kubernetes manifests, validate each side with its appropriate toolchain.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic Kubernetes guidance in this skill.
