---
name: vigilante-issue-implementation-on-docker
description: Implement a GitHub issue end-to-end when Vigilante dispatches work for a Docker-focused repository with Dockerfile best practices, image hardening, and secret-safe build guidance.
---

# Vigilante Docker Issue Implementation

## Focus
- Read the prompt for detected tech stacks, process hints, and Docker security guidance before changing code.
- Follow current Dockerfile best practices for image minimization, build efficiency, and secret-safe builds.
- Keep changes scoped to the issue and do not broaden into unrelated infrastructure redesign.

## Dockerfile Best Practices
- **Base images**: pin base images to specific versions or digests rather than mutable tags like `latest`. Prefer minimal base images (Alpine, distroless, scratch) to reduce attack surface. When the repository already uses digest-pinned or distroless images, preserve that convention.
- **Build structure**: use multi-stage builds to separate build dependencies from the final runtime image. Set an explicit `WORKDIR` rather than relying on the default. Combine related `RUN` commands to minimize layers. Order instructions from least to most frequently changing to maximize build cache efficiency. Copy dependency manifests and install dependencies before copying application source.
- **Package management**: minimize installed packages and remove package manager caches in the same `RUN` layer (e.g., `apt-get install -y --no-install-recommends ... && rm -rf /var/lib/apt/lists/*`). Do not install debug tools, editors, or shells in production images unless the repository explicitly requires them.
- **`.dockerignore`**: ensure `.dockerignore` excludes build artifacts, test fixtures, secrets, and version-control metadata that should not enter the build context.

## Secret-Safe Builds
- Never pass secrets through `ARG` or `ENV` instructions — they persist in image history and layer metadata.
- Use BuildKit secret mounts (`--mount=type=secret`) for build-time secrets when the build requires credentials.
- Do not copy secret files (`.env`, credentials, tokens, private keys) into the image.
- Ensure `.dockerignore` excludes sensitive files and directories.

## Runtime Security
- Run containers as a non-root user when practical — add a `USER` instruction after installing packages.
- Prefer read-only root filesystems where the application supports it.
- Expose only the ports the application requires.
- Do not use `--privileged` or add unnecessary Linux capabilities unless the issue specifically requires it.

## Validation
- When the repository defines image scanning, Docker build checks, buildx bake, provenance, or policy workflows, respect and preserve them.
- Run `docker build` or the repository's defined build command to verify Dockerfile changes compile successfully.
- Do not disable or weaken existing security scanning or build-check configurations.

## Mixed-Stack Repositories
- A Docker-focused repository may also contain application code in Go, Node.js, Python, or another language.
- Scope Docker guidance to Dockerfiles, Compose files, `.dockerignore`, and container build/deploy configuration.
- When the repository also has a language-specific toolchain, respect its own test, lint, and build workflow for application-scoped changes. Check the prompt for detected tech stacks and process hints.
- When an issue touches both Dockerfiles and application code, validate each side with its respective toolchain rather than validating only one side.

## Workflow
- Follow the base `vigilante-issue-implementation` workflow for issue comments, validation, push, and PR creation.
- Use `vigilante commit` for all commit-producing operations. Do not use `git commit` or GitHub CLI commit flows directly.
- Any commit or amend must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.
- Do not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.
- Repository-specific instructions (`AGENTS.md`, `README.md`, CI config) remain authoritative when they are more specific than the generic Docker guidance in this skill.
