// Package sandbox implements isolated Docker container execution for coding
// agents. It provisions containers, manages scoped credentials, runs a
// reverse proxy for gh CLI access control, and handles teardown.
package sandbox

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SessionStatus tracks the lifecycle phase of a sandbox session.
type SessionStatus string

const (
	StatusProvisioning SessionStatus = "provisioning"
	StatusReady        SessionStatus = "ready"
	StatusRunning      SessionStatus = "running"
	StatusCompleting   SessionStatus = "completing"
	StatusTimedOut     SessionStatus = "timed_out"
	StatusFailed       SessionStatus = "failed"
	StatusTerminated   SessionStatus = "terminated"
)

// ResourceLimits constrains what the sandbox container may consume.
type ResourceLimits struct {
	Memory string `json:"memory"`
	CPUs   int    `json:"cpus"`
	Disk   string `json:"disk"`
}

// DefaultResourceLimits returns sensible defaults for sandbox containers.
func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		Memory: "8g",
		CPUs:   4,
		Disk:   "20g",
	}
}

// ProvisionRequest describes what the caller wants from a new sandbox.
type ProvisionRequest struct {
	Repository     string         `json:"repository"`
	IssueNumber    int            `json:"issue_number"`
	Provider       string         `json:"provider"`
	TTLSeconds     int            `json:"ttl_seconds"`
	ResourceLimits ResourceLimits `json:"resource_limits"`
	WorktreePath   string         `json:"worktree_path"`
	BaseImage      string         `json:"base_image"`
}

// Session holds runtime state for a single sandbox execution.
type Session struct {
	mu sync.Mutex

	ID           string         `json:"session_id"`
	Repository   string         `json:"repository"`
	IssueNumber  int            `json:"issue_number"`
	Provider     string         `json:"provider"`
	Status       SessionStatus  `json:"status"`
	ContainerID  string         `json:"container_id"`
	ProxyPort    int            `json:"proxy_port"`
	Token        string         `json:"sandbox_token"`
	DeployKeyID  int64          `json:"deploy_key_id"`
	SSHPublicKey string         `json:"ssh_public_key"`
	Limits       ResourceLimits `json:"resource_limits"`
	CreatedAt    time.Time      `json:"created_at"`
	ExpiresAt    time.Time      `json:"expires_at"`
	EndedAt      *time.Time     `json:"ended_at,omitempty"`
	TeardownErr  string         `json:"teardown_error,omitempty"`

	sshPrivateKey []byte
	signingKey    []byte
	proxyListener net.Listener
}

// SetStatus updates the session status under lock.
func (s *Session) SetStatus(status SessionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Status = status
}

// GetStatus reads the session status under lock.
func (s *Session) GetStatus() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
}

// IsExpired reports whether the session TTL has elapsed.
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// Manager orchestrates sandbox session lifecycle.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	docker   DockerClient
	logger   *slog.Logger
}

// NewManager creates a sandbox manager backed by the given Docker client.
func NewManager(docker DockerClient, logger *slog.Logger) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		docker:   docker,
		logger:   logger,
	}
}

// Provision creates a new sandbox session: generates credentials, creates a
// Docker container, starts the reverse proxy, and transitions the session to
// Ready.
func (m *Manager) Provision(ctx context.Context, req ProvisionRequest) (*Session, error) {
	if err := validateProvisionRequest(req); err != nil {
		return nil, fmt.Errorf("sandbox provision: %w", err)
	}

	sessionID := generateSessionID()
	signingKey := make([]byte, 32)
	if _, err := rand.Read(signingKey); err != nil {
		return nil, fmt.Errorf("sandbox provision: generate signing key: %w", err)
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	now := time.Now().UTC()

	token, err := MintToken(signingKey, sessionID, req.Repository, now.Add(ttl))
	if err != nil {
		return nil, fmt.Errorf("sandbox provision: mint token: %w", err)
	}

	sshPub, sshPriv, err := generateSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("sandbox provision: ssh keygen: %w", err)
	}

	session := &Session{
		ID:            sessionID,
		Repository:    req.Repository,
		IssueNumber:   req.IssueNumber,
		Provider:      req.Provider,
		Status:        StatusProvisioning,
		Token:         token,
		SSHPublicKey:  string(sshPub),
		Limits:        req.ResourceLimits,
		CreatedAt:     now,
		ExpiresAt:     now.Add(ttl),
		sshPrivateKey: sshPriv,
		signingKey:    signingKey,
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	m.logger.Info("sandbox provisioning", "session_id", sessionID, "repo", req.Repository, "issue", req.IssueNumber, "ttl", ttl)

	// Start the reverse proxy listener on a random available port.
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		session.SetStatus(StatusFailed)
		return nil, fmt.Errorf("sandbox provision: start proxy listener: %w", err)
	}
	session.proxyListener = proxyListener
	session.ProxyPort = proxyListener.Addr().(*net.TCPAddr).Port

	proxy := NewProxy(m, m.logger)
	go proxy.Serve(proxyListener)

	// Prepare SSH key mount directory.
	sshDir, err := prepareSSHMount(sessionID, sshPriv)
	if err != nil {
		_ = proxyListener.Close()
		session.SetStatus(StatusFailed)
		return nil, fmt.Errorf("sandbox provision: prepare ssh mount: %w", err)
	}

	containerID, err := m.docker.CreateContainer(ctx, ContainerConfig{
		Image:      effectiveBaseImage(req.BaseImage),
		SessionID:  sessionID,
		Repository: req.Repository,
		Provider:   req.Provider,
		Token:      token,
		ProxyURL:   fmt.Sprintf("http://host.docker.internal:%d", session.ProxyPort),
		SSHDir:     sshDir,
		Worktree:   req.WorktreePath,
		Limits:     req.ResourceLimits,
	})
	if err != nil {
		_ = proxyListener.Close()
		_ = os.RemoveAll(sshDir)
		session.SetStatus(StatusFailed)
		return nil, fmt.Errorf("sandbox provision: create container: %w", err)
	}
	session.ContainerID = containerID

	if err := m.docker.StartContainer(ctx, containerID); err != nil {
		_ = m.docker.RemoveContainer(ctx, containerID)
		_ = proxyListener.Close()
		_ = os.RemoveAll(sshDir)
		session.SetStatus(StatusFailed)
		return nil, fmt.Errorf("sandbox provision: start container: %w", err)
	}

	session.SetStatus(StatusRunning)
	m.logger.Info("sandbox running", "session_id", sessionID, "container", containerID[:12], "proxy_port", session.ProxyPort)

	return session, nil
}

// GetSession returns the session for the given ID or nil.
func (m *Manager) GetSession(sessionID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

// ValidateToken checks a sandbox token against the session it claims to
// belong to. Returns an error describing the failure when invalid.
func (m *Manager) ValidateToken(tokenStr string) (*Session, error) {
	claims, err := ParseToken(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("token parse: %w", err)
	}

	session := m.GetSession(claims.SessionID)
	if session == nil {
		return nil, fmt.Errorf("token validation: session %q not found", claims.SessionID)
	}

	if err := VerifyToken(session.signingKey, tokenStr); err != nil {
		return nil, fmt.Errorf("token validation: %w", err)
	}

	if session.GetStatus() == StatusTerminated {
		return nil, fmt.Errorf("token validation: session %q is terminated", claims.SessionID)
	}

	return session, nil
}

// Teardown performs orderly shutdown of a sandbox session: extracts artifacts,
// stops and removes the container, cleans up credentials, and marks the
// session terminated.
func (m *Manager) Teardown(ctx context.Context, sessionID string, reason string) error {
	session := m.GetSession(sessionID)
	if session == nil {
		return fmt.Errorf("sandbox teardown: session %q not found", sessionID)
	}

	m.logger.Info("sandbox teardown", "session_id", sessionID, "reason", reason)

	var errs []string

	// 1. Invalidate token by marking terminated.
	session.SetStatus(StatusTerminated)

	// 2. Stop container gracefully (10s timeout).
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := m.docker.StopContainer(stopCtx, session.ContainerID); err != nil {
		errs = append(errs, fmt.Sprintf("stop container: %v", err))
	}
	cancel()

	// 3. Remove container and anonymous volumes.
	if err := m.docker.RemoveContainer(ctx, session.ContainerID); err != nil {
		errs = append(errs, fmt.Sprintf("remove container: %v", err))
	}

	// 4. Close the proxy listener.
	if session.proxyListener != nil {
		if err := session.proxyListener.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("close proxy: %v", err))
		}
	}

	// 5. Clean up SSH mount.
	sshDir := sshMountPath(sessionID)
	if err := os.RemoveAll(sshDir); err != nil {
		errs = append(errs, fmt.Sprintf("remove ssh dir: %v", err))
	}

	// 6. Destroy signing material.
	session.mu.Lock()
	for i := range session.signingKey {
		session.signingKey[i] = 0
	}
	now := time.Now().UTC()
	session.EndedAt = &now
	session.mu.Unlock()

	if len(errs) > 0 {
		combined := strings.Join(errs, "; ")
		session.mu.Lock()
		session.TeardownErr = combined
		session.mu.Unlock()
		m.logger.Error("sandbox teardown partial failure", "session_id", sessionID, "errors", combined)
		return fmt.Errorf("sandbox teardown: %s", combined)
	}

	m.logger.Info("sandbox teardown complete", "session_id", sessionID)
	return nil
}

// TeardownAll tears down all active sessions. Used during daemon shutdown.
func (m *Manager) TeardownAll(ctx context.Context) {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id, s := range m.sessions {
		if s.GetStatus() != StatusTerminated {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			_ = m.Teardown(ctx, sid, "daemon_shutdown")
		}(id)
	}
	wg.Wait()
}

// ReconcileStale finds containers from previous daemon runs that were not
// cleaned up and tears them down.
func (m *Manager) ReconcileStale(ctx context.Context) error {
	containers, err := m.docker.ListVigilanteContainers(ctx)
	if err != nil {
		return fmt.Errorf("reconcile stale sandboxes: %w", err)
	}

	for _, c := range containers {
		m.logger.Info("reconciling orphaned sandbox container", "container", c)
		if err := m.docker.StopContainer(ctx, c); err != nil {
			m.logger.Error("reconcile stop failed", "container", c, "err", err)
		}
		if err := m.docker.RemoveContainer(ctx, c); err != nil {
			m.logger.Error("reconcile remove failed", "container", c, "err", err)
		}
	}

	return nil
}

// RefreshToken extends a session's TTL by the given duration. Only callable
// by the daemon, not from inside the container.
func (m *Manager) RefreshToken(sessionID string, extendBy time.Duration) error {
	session := m.GetSession(sessionID)
	if session == nil {
		return fmt.Errorf("refresh token: session %q not found", sessionID)
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	if session.Status == StatusTerminated {
		return fmt.Errorf("refresh token: session %q is terminated", sessionID)
	}

	session.ExpiresAt = session.ExpiresAt.Add(extendBy)
	newToken, err := MintToken(session.signingKey, sessionID, session.Repository, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("refresh token: %w", err)
	}
	session.Token = newToken
	return nil
}

func validateProvisionRequest(req ProvisionRequest) error {
	if strings.TrimSpace(req.Repository) == "" {
		return fmt.Errorf("repository is required")
	}
	parts := strings.SplitN(req.Repository, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("repository must be in owner/repo format")
	}
	if req.IssueNumber <= 0 {
		return fmt.Errorf("issue_number must be positive")
	}
	if strings.TrimSpace(req.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	return nil
}

func generateSessionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("sbx_%x", b)
}

func generateSSHKeypair() (pubKey []byte, privKey []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	pubBytes := ssh.MarshalAuthorizedKey(sshPub)

	privBytes, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, nil, err
	}
	privPEM := pem.EncodeToMemory(privBytes)

	return pubBytes, privPEM, nil
}

func prepareSSHMount(sessionID string, privKey []byte) (string, error) {
	dir := sshMountPath(sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyPath, privKey, 0o600); err != nil {
		return "", err
	}
	return dir, nil
}

func sshMountPath(sessionID string) string {
	return filepath.Join(os.TempDir(), "vigilante-sandbox", sessionID, "ssh")
}

func effectiveBaseImage(image string) string {
	if strings.TrimSpace(image) != "" {
		return image
	}
	return "vigilante/sandbox:latest"
}
