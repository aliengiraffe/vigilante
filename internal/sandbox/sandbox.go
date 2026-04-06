// Package sandbox orchestrates containerized coding-agent execution.
//
// It ties together credential minting, container lifecycle, and the
// reverse proxy to provide isolated sandbox sessions per SANDBOX.md.
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/vigilante/internal/environment"
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
	TTL          time.Duration
	Image        string
	MemoryLimit  string
	CPUs         string
	EnableDinD   bool
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

	containerName := "vigilante-sandbox-" + sessionID
	proxyAddr := m.proxy.Addr()

	// Create the container.
	containerID, err := container.Create(ctx, m.runner, container.Config{
		Image:        image,
		Name:         containerName,
		WorktreePath: cfg.WorktreePath,
		SSHKeyPath:   filepath.Join(sshDir, "id_ed25519"),
		EnvVars: map[string]string{
			"VIGILANTE_SESSION_ID":    sessionID,
			"VIGILANTE_SANDBOX_TOKEN": sandboxToken,
			"VIGILANTE_PROXY_URL":     "http://" + proxyAddr,
		},
		MemoryLimit: memLimit,
		CPUs:        cpus,
		EnableDinD:  cfg.EnableDinD,
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
		SSHKeyDir:     sshDir,
		SSHPublicKey:  pubKeyStr,
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

	// Step 3: Stop container (10s grace period).
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
