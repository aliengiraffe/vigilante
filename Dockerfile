# syntax=docker/dockerfile:1

# Stage 1: Build the gh-sandbox mirror binary.
FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 go build -o /gh-sandbox ./cmd/gh-sandbox

# Stage 2: Sandbox runtime image.
FROM debian:bookworm-slim

# Install git, ssh, and Docker CLI for Docker-in-Docker support.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        openssh-client \
    && install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc \
    && chmod a+r /etc/apt/keyrings/docker.asc \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian bookworm stable" \
       > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        docker-ce-cli \
        docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

# Install the gh-sandbox binary as the gh CLI replacement.
COPY --from=builder /gh-sandbox /usr/local/bin/gh

# Create directories for mounted credentials and workspace.
RUN mkdir -p /etc/vigilante/ssh /workspace

# Configure git to use the ephemeral SSH key when present.
RUN git config --system core.sshCommand \
    "ssh -i /etc/vigilante/ssh/id_ed25519 -o StrictHostKeyChecking=no"

WORKDIR /workspace
