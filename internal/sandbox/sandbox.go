// Package sandbox orchestrates containerized coding-agent execution.
//
// It ties together credential minting, container lifecycle, and the
// reverse proxy to provide isolated sandbox sessions per SANDBOX.md.
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/sandbox/container"
	"github.com/nicobistolfi/vigilante/internal/sandbox/proxy"
	"github.com/nicobistolfi/vigilante/internal/sandbox/token"
)

// DefaultTTL is the default maximum sandbox session lifetime.
const DefaultTTL = 2 * time.Hour

// DefaultImage is the default sandbox container image.
const DefaultImage = "vigilante-sandbox:latest"

// DefaultMemoryLimit is the default container memory constraint.
const DefaultMemoryLimit = "8g"

// DefaultCPUs is the default CPU limit for sandbox containers.
const DefaultCPUs = "4"

// SessionConfig describes the parameters for provisioning a sandbox.
type SessionConfig struct {
	Repository   string
	IssueNumber  int
	Provider     string
	WorktreePath string
	// RepoPath is the host path to the parent repository checkout. When the
	// worktree is a separate git worktree (WorktreePath != RepoPath), the
	// worktree's `.git` file contains an absolute `gitdir:` pointer back into
	// `<RepoPath>/.git/worktrees/<name>`. We bind-mount that parent `.git`
	// directory at the same absolute host path inside the container so the
	// indirection resolves; otherwise git inside the sandbox sees a dangling
	// gitdir reference and refuses to operate.
	RepoPath    string
	TTL         time.Duration
	Image       string
	MemoryLimit string
	CPUs        string
	EnableDinD  bool
	Mounts      []container.Mount
	// ExtraEnv adds environment variables to the sandbox container on top of
	// the hardcoded VIGILANTE_* variables. Used for things like
	// SSH_AUTH_SOCK when the host SSH agent is forwarded into the sandbox.
	ExtraEnv map[string]string
}

// Session represents an active sandbox session.
type Session struct {
	ID            string
	Repository    string
	IssueNumber   int
	Provider      string
	ContainerName string
	ContainerID   string
	SandboxToken  string
	ProxyPort     int
	SSHKeyDir     string
	SSHPublicKey  string
	DeployKeyID   int64
	Status        string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// proxyPort extracts the port number from an address string like "127.0.0.1:9821".
func proxyPort(addr string) int {
	if addr == "" {
		return 0
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}

// Manager orchestrates sandbox session lifecycle.
type Manager struct {
	signingKey []byte
	runner     environment.Runner
	proxy      *proxy.Proxy
	logger     *slog.Logger
	stateDir   string

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewManager creates a sandbox manager. The stateDir is used for
// ephemeral credential storage (SSH keys).
func NewManager(runner environment.Runner, logger *slog.Logger, stateDir string) (*Manager, error) {
	signingKey, err := token.GenerateSigningKey()
	if err != nil {
		return nil, fmt.Errorf("sandbox manager: %w", err)
	}
	p := proxy.New(signingKey, runner, logger)
	return &Manager{
		signingKey: signingKey,
		runner:     runner,
		proxy:      p,
		logger:     logger,
		stateDir:   stateDir,
		sessions:   make(map[string]*Session),
	}, nil
}

// StartProxy starts the reverse proxy on the given address (e.g. "127.0.0.1:0").
func (m *Manager) StartProxy(addr string) error {
	return m.proxy.Start(addr)
}

// StopProxy gracefully shuts down the reverse proxy.
func (m *Manager) StopProxy(ctx context.Context) error {
	return m.proxy.Stop(ctx)
}

// ProxyAddr returns the proxy listener address.
func (m *Manager) ProxyAddr() string {
	return m.proxy.Addr()
}

// Provision creates a new sandbox session: mints credentials, creates the
// container, and registers the session with the reverse proxy.
func (m *Manager) Provision(ctx context.Context, cfg SessionConfig) (*Session, error) {
	sessionID := "sbx_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]

	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	image := cfg.Image
	if image == "" {
		image = DefaultImage
	}
	memLimit := cfg.MemoryLimit
	if memLimit == "" {
		memLimit = DefaultMemoryLimit
	}
	cpus := cfg.CPUs
	if cpus == "" {
		cpus = DefaultCPUs
	}

	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	// Mint sandbox token.
	claims := token.Claims{
		SessionID:  sessionID,
		Repository: cfg.Repository,
		ExpiresAt:  expiresAt.Unix(),
	}
	sandboxToken, err := token.Issue(m.signingKey, claims)
	if err != nil {
		return nil, fmt.Errorf("mint sandbox token: %w", err)
	}

	// Generate ephemeral SSH keypair using ssh-keygen.
	sshDir := filepath.Join(m.stateDir, "sandbox", sessionID, "ssh")
	pubKeyStr, err := generateSSHKeyPair(ctx, m.runner, sshDir)
	if err != nil {
		return nil, fmt.Errorf("generate ssh keypair: %w", err)
	}

	// Register the ephemeral public key as a write-access deploy key on the
	// target repository so the container can `git push` over SSH using its own
	// short-lived identity. The key is removed on teardown; if teardown fails,
	// ReconcileStale handles cleanup via the GitHub API.
	//
	// Deploy key registration requires admin access to the repo. When the
	// authenticated user only has write/collaborator access, the API returns
	// 404 or 403. In that case we fall back to SSH agent forwarding (the
	// ExtraEnv SSH_AUTH_SOCK set by the caller) — git push will then use the
	// operator's host SSH agent instead of the ephemeral key.
	var deployKeyID int64
	deployKeyTitle := "vigilante-sandbox-" + sessionID
	keyID, deployErr := ghcli.AddDeployKey(ctx, m.runner, cfg.Repository, deployKeyTitle, pubKeyStr, false)
	if deployErr != nil {
		m.logger.Warn("deploy key registration failed, falling back to ssh agent forwarding",
			"session_id", sessionID, "repo", cfg.Repository, "err", deployErr)
	} else {
		deployKeyID = keyID
		m.logger.Info("deploy key registered", "session_id", sessionID, "repo", cfg.Repository, "key_id", deployKeyID)
	}

	containerName := "vigilante-sandbox-" + sessionID
	proxyAddr := m.proxy.Addr()
	port := proxyPort(proxyAddr)

	// Build the proxy URL the container will use. Inside Docker the
	// host loopback is not reachable, so we route via host.docker.internal.
	containerProxyURL := "http://" + proxyAddr
	if port > 0 {
		containerProxyURL = fmt.Sprintf("http://host.docker.internal:%d", port)
	}

	mounts := append([]container.Mount(nil), cfg.Mounts...)
	if mount, ok := worktreeGitdirMount(cfg.RepoPath, cfg.WorktreePath); ok {
		mounts = append(mounts, mount)
	}

	envVars := map[string]string{
		"VIGILANTE_SESSION_ID":    sessionID,
		"VIGILANTE_SANDBOX_TOKEN": sandboxToken,
		"VIGILANTE_PROXY_URL":     containerProxyURL,
	}
	for k, v := range cfg.ExtraEnv {
		if strings.TrimSpace(k) == "" {
			continue
		}
		envVars[k] = v
	}

	// GIT_SSH_COMMAND overrides core.sshCommand from the system gitconfig.
	// We set it per-session based on whether the deploy key was registered:
	//   - deploy key registered: IdentitiesOnly=yes + ephemeral key only.
	//     Guarantees the repo-scoped key is used, no agent identities tried.
	//   - deploy key failed: no -i, let ssh use the forwarded agent only.
	//     Avoids burning GitHub's per-connection auth retry budget on the
	//     unauthorized ephemeral key before the agent's GitHub key is offered
	//     (1Password agents commonly carry many keys).
	if deployKeyID > 0 {
		envVars["GIT_SSH_COMMAND"] = "ssh -i /etc/vigilante/ssh/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
	} else {
		envVars["GIT_SSH_COMMAND"] = "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"
	}

	// Create the container.
	containerID, err := container.Create(ctx, m.runner, container.Config{
		Image:        image,
		Name:         containerName,
		WorktreePath: cfg.WorktreePath,
		SSHKeyPath:   filepath.Join(sshDir, "id_ed25519"),
		ProxyPort:    port,
		EnvVars:      envVars,
		MemoryLimit:  memLimit,
		CPUs:         cpus,
		EnableDinD:   cfg.EnableDinD,
		Mounts:       mounts,
	})
	if err != nil {
		return nil, fmt.Errorf("create sandbox container: %w", err)
	}

	// Register session with the reverse proxy.
	m.proxy.RegisterSession(proxy.SessionEntry{
		SessionID:  sessionID,
		Repository: cfg.Repository,
		ExpiresAt:  expiresAt,
	})

	sess := &Session{
		ID:            sessionID,
		Repository:    cfg.Repository,
		IssueNumber:   cfg.IssueNumber,
		Provider:      cfg.Provider,
		ContainerName: containerName,
		ContainerID:   containerID,
		SandboxToken:  sandboxToken,
		ProxyPort:     proxyPort(proxyAddr),
		SSHKeyDir:     sshDir,
		SSHPublicKey:  pubKeyStr,
		DeployKeyID:   deployKeyID,
		Status:        "provisioned",
		CreatedAt:     now,
		ExpiresAt:     expiresAt,
	}

	m.mu.Lock()
	m.sessions[sessionID] = sess
	m.mu.Unlock()

	m.logger.Info("sandbox session provisioned",
		"session_id", sessionID,
		"repo", cfg.Repository,
		"issue", cfg.IssueNumber,
		"container", containerName,
		"expires_at", expiresAt.Format(time.RFC3339),
	)

	return sess, nil
}

// Start starts the container for a provisioned session.
func (m *Manager) Start(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}

	if err := container.Start(ctx, m.runner, sess.ContainerName); err != nil {
		return err
	}

	m.mu.Lock()
	sess.Status = "running"
	m.mu.Unlock()

	m.logger.Info("sandbox session started", "session_id", sessionID)
	return nil
}

// GetSession returns the session for the given ID.
func (m *Manager) GetSession(sessionID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	return sess, ok
}

// StopContainer stops the container for the given session without removing it.
// This releases resources like port mappings while keeping the container
// available for inspection via docker logs/cp/start.
func (m *Manager) StopContainer(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	m.proxy.DeregisterSession(sessionID)
	return container.Stop(ctx, m.runner, sess.ContainerName, 10)
}

// Teardown performs the full teardown sequence: extract artifacts, revoke
// credentials, stop and remove the container.
func (m *Manager) Teardown(ctx context.Context, sessionID string, reason string) error {
	m.mu.Lock()
	sess, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}

	m.logger.Info("sandbox teardown started", "session_id", sessionID, "reason", reason)

	// Step 1: Extract artifacts (best-effort).
	artifacts, err := container.ExtractArtifacts(ctx, m.runner, sess.ContainerName)
	if err != nil {
		m.logger.Warn("artifact extraction failed", "session_id", sessionID, "err", err)
	} else {
		m.logger.Info("artifacts extracted", "session_id", sessionID, "summary_len", len(artifacts))
	}

	// Step 2: Invalidate token in proxy.
	m.proxy.DeregisterSession(sessionID)

	// Step 3: Revoke the ephemeral deploy key so the session's SSH identity
	// can no longer push to the repository.
	if sess.DeployKeyID > 0 {
		if err := ghcli.RemoveDeployKey(ctx, m.runner, sess.Repository, sess.DeployKeyID); err != nil {
			m.logger.Warn("deploy key removal failed", "session_id", sessionID, "repo", sess.Repository, "key_id", sess.DeployKeyID, "err", err)
		} else {
			m.logger.Info("deploy key removed", "session_id", sessionID, "repo", sess.Repository, "key_id", sess.DeployKeyID)
		}
	}

	// Step 4: Stop container (10s grace period).
	if err := container.Stop(ctx, m.runner, sess.ContainerName, 10); err != nil {
		m.logger.Warn("container stop failed", "session_id", sessionID, "err", err)
	}

	// Step 4: Remove container and anonymous volumes.
	if err := container.Remove(ctx, m.runner, sess.ContainerName); err != nil {
		m.logger.Warn("container remove failed", "session_id", sessionID, "err", err)
	}

	// Step 5: Clean up SSH key material.
	if sess.SSHKeyDir != "" {
		if err := os.RemoveAll(sess.SSHKeyDir); err != nil {
			m.logger.Warn("ssh key cleanup failed", "session_id", sessionID, "err", err)
		}
	}

	// Step 6: Update session state.
	m.mu.Lock()
	sess.Status = "terminated"
	delete(m.sessions, sessionID)
	m.mu.Unlock()

	m.logger.Info("sandbox teardown complete", "session_id", sessionID, "reason", reason)
	return nil
}

// ReconcileStale finds and cleans up orphaned sandbox containers that
// no longer have active sessions. This runs on daemon startup.
func (m *Manager) ReconcileStale(ctx context.Context) error {
	containers, err := container.ListSandboxContainers(ctx, m.runner)
	if err != nil {
		return fmt.Errorf("reconcile stale sandboxes: %w", err)
	}

	m.mu.Lock()
	activeNames := make(map[string]bool, len(m.sessions))
	for _, sess := range m.sessions {
		activeNames[sess.ContainerName] = true
	}
	m.mu.Unlock()

	for _, name := range containers {
		if activeNames[name] {
			continue
		}
		m.logger.Info("cleaning up orphaned sandbox container", "name", name)
		_ = container.Stop(ctx, m.runner, name, 5)
		_ = container.Remove(ctx, m.runner, name)
	}
	return nil
}

// ActiveSessions returns a snapshot of all active session IDs.
func (m *Manager) ActiveSessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

// worktreeGitdirMount returns a bind mount that exposes the parent
// repository's `.git` directory inside the container at the same absolute
// host path. Git worktrees use a `.git` *file* whose `gitdir:` line is an
// absolute pointer back into `<repoPath>/.git/worktrees/<name>`; without
// this mount the indirection dangles inside the sandbox and every git
// operation fails. Returns ok=false when no separate worktree is in use or
// when the parent `.git` directory is missing on disk.
func worktreeGitdirMount(repoPath, worktreePath string) (container.Mount, bool) {
	repoPath = strings.TrimSpace(repoPath)
	worktreePath = strings.TrimSpace(worktreePath)
	if repoPath == "" || worktreePath == "" || repoPath == worktreePath {
		return container.Mount{}, false
	}
	gitDir := filepath.Join(repoPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return container.Mount{}, false
	}
	return container.Mount{Source: gitDir, Target: gitDir}, true
}

// generateSSHKeyPair shells out to ssh-keygen to create an Ed25519 keypair
// in the given directory. This avoids importing golang.org/x/crypto/ssh.
func generateSSHKeyPair(ctx context.Context, runner environment.Runner, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	keyPath := filepath.Join(dir, "id_ed25519")
	_, err := runner.Run(ctx, "", "ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-q")
	if err != nil {
		return "", fmt.Errorf("ssh-keygen: %w", err)
	}

	pubBytes, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		return "", fmt.Errorf("read public key: %w", err)
	}
	return strings.TrimSpace(string(pubBytes)), nil
}
