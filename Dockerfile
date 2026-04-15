# syntax=docker/dockerfile:1

# Stage 1: Build the Vigilante binaries.
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -o /gh-sandbox ./cmd/gh-sandbox
RUN CGO_ENABLED=0 go build -o /vigilante ./cmd/vigilante

# Stage 2: Sandbox runtime image.
FROM debian:bookworm-slim

# Install git, ssh, Docker CLI, Node.js, npm, and dev tools for coding-agent execution.
RUN apt-get update && apt-get install -y --no-install-recommends \
        bubblewrap \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        nodejs \
        npm \
        openssh-client \
        ripgrep \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
    && chmod a+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        docker-ce-cli \
        docker-compose-plugin \
    && npm install -g @openai/codex \
    && rm -rf /var/lib/apt/lists/*

# Install Go toolchain from the builder stage. Symlink the binaries into
# /usr/local/bin so login shells (`bash -l`) — which reset PATH from
# /etc/profile and ignore the container's ENV PATH — can still find them.
COPY --from=builder /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
RUN ln -s /usr/local/go/bin/go /usr/local/bin/go \
    && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Install the gh-sandbox binary as the gh CLI replacement.
COPY --from=builder /gh-sandbox /usr/local/bin/gh
COPY --from=builder /vigilante /usr/local/bin/vigilante

# Create directories for mounted credentials and workspace.
RUN mkdir -p /etc/vigilante/ssh /workspace /root/.ssh

# Configure git for the sandbox environment. Do NOT set gpg.format or
# gpg.ssh.program here: git rejects an empty string as `invalid value for
# 'gpg.format': ''` and aborts every commit. Signing is disabled instead via
# commit.gpgsign / tag.gpgsign, which is enough as long as the operator's
# global gitconfig is sanitized before being mounted into the container
# (see app.sandboxConfigMounts).
#
# The ssh command offers the ephemeral key first (registered as a deploy key
# when the operator has admin access to the repo). When deploy key registration
# fails (collaborator-level access), the container falls back to the forwarded
# SSH agent (SSH_AUTH_SOCK set by the host). The ephemeral key is listed first
# via -i so it's tried before agent identities.
RUN git config --system core.sshCommand \
    "ssh -i /root/.ssh/id_ed25519 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null" \
    && git config --system commit.gpgsign false \
    && git config --system tag.gpgsign false

WORKDIR /workspace

# Keep the sandbox available for docker exec-based agent sessions.
CMD ["sleep", "infinity"]
