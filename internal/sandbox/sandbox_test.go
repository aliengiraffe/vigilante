package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// mockDockerClient implements DockerClient for testing.
type mockDockerClient struct {
	mu         sync.Mutex
	containers map[string]bool // containerID -> running
	execOutput string
}

func (m *mockDockerClient) CreateContainer(_ context.Context, cfg ContainerConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.containers == nil {
		m.containers = make(map[string]bool)
	}
	id := "sha256:" + cfg.SessionID + "_container"
	m.containers[id] = false
	return id, nil
}

func (m *mockDockerClient) StartContainer(_ context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.containers[containerID]; !ok {
		return fmt.Errorf("container %q not found", containerID)
	}
	m.containers[containerID] = true
	return nil
}

func (m *mockDockerClient) StopContainer(_ context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.containers[containerID]; !ok {
		return nil
	}
	m.containers[containerID] = false
	return nil
}

func (m *mockDockerClient) RemoveContainer(_ context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.containers, containerID)
	return nil
}

func (m *mockDockerClient) ExecInContainer(_ context.Context, _ string, _ []string) (string, error) {
	return m.execOutput, nil
}

func (m *mockDockerClient) ListVigilanteContainers(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.containers {
		ids = append(ids, id)
	}
	return ids, nil
}

func TestProvisionAndTeardown(t *testing.T) {
	docker := &mockDockerClient{}
	mgr := NewManager(docker, slog.Default())

	session, err := mgr.Provision(context.Background(), ProvisionRequest{
		Repository:     "owner/repo",
		IssueNumber:    42,
		Provider:       "claude-code",
		TTLSeconds:     3600,
		ResourceLimits: DefaultResourceLimits(),
		WorktreePath:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if session.ID == "" {
		t.Fatal("session ID is empty")
	}
	if session.GetStatus() != StatusRunning {
		t.Errorf("status = %q, want %q", session.GetStatus(), StatusRunning)
	}
	if session.ContainerID == "" {
		t.Fatal("container ID is empty")
	}
	if session.ProxyPort == 0 {
		t.Fatal("proxy port is 0")
	}
	if session.Token == "" {
		t.Fatal("token is empty")
	}

	// Verify token validates.
	found, err := mgr.ValidateToken(session.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if found.ID != session.ID {
		t.Errorf("validated session ID = %q, want %q", found.ID, session.ID)
	}

	// Teardown.
	if err := mgr.Teardown(context.Background(), session.ID, "test"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if session.GetStatus() != StatusTerminated {
		t.Errorf("post-teardown status = %q, want %q", session.GetStatus(), StatusTerminated)
	}

	// Container should be removed.
	docker.mu.Lock()
	containerCount := len(docker.containers)
	docker.mu.Unlock()
	if containerCount != 0 {
		t.Errorf("containers remaining = %d, want 0", containerCount)
	}
}

func TestProvisionInvalidRequest(t *testing.T) {
	docker := &mockDockerClient{}
	mgr := NewManager(docker, slog.Default())

	tests := []struct {
		name string
		req  ProvisionRequest
	}{
		{"empty repo", ProvisionRequest{IssueNumber: 1, Provider: "claude"}},
		{"bad repo format", ProvisionRequest{Repository: "noslash", IssueNumber: 1, Provider: "claude"}},
		{"zero issue", ProvisionRequest{Repository: "a/b", IssueNumber: 0, Provider: "claude"}},
		{"empty provider", ProvisionRequest{Repository: "a/b", IssueNumber: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mgr.Provision(context.Background(), tt.req)
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestSessionExpiry(t *testing.T) {
	s := &Session{ExpiresAt: time.Now().Add(-time.Hour)}
	if !s.IsExpired() {
		t.Error("session should be expired")
	}

	s.ExpiresAt = time.Now().Add(time.Hour)
	if s.IsExpired() {
		t.Error("session should not be expired")
	}
}

func TestRefreshToken(t *testing.T) {
	docker := &mockDockerClient{}
	mgr := NewManager(docker, slog.Default())

	session, err := mgr.Provision(context.Background(), ProvisionRequest{
		Repository:     "owner/repo",
		IssueNumber:    1,
		Provider:       "claude",
		TTLSeconds:     3600,
		ResourceLimits: DefaultResourceLimits(),
		WorktreePath:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	originalExpiry := session.ExpiresAt
	if err := mgr.RefreshToken(session.ID, time.Hour); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}

	if !session.ExpiresAt.After(originalExpiry) {
		t.Error("expected expiry to be extended")
	}
}

func TestTeardownAll(t *testing.T) {
	docker := &mockDockerClient{}
	mgr := NewManager(docker, slog.Default())

	for i := 0; i < 3; i++ {
		_, err := mgr.Provision(context.Background(), ProvisionRequest{
			Repository:     fmt.Sprintf("owner/repo%d", i),
			IssueNumber:    i + 1,
			Provider:       "claude",
			TTLSeconds:     3600,
			ResourceLimits: DefaultResourceLimits(),
			WorktreePath:   t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Provision %d: %v", i, err)
		}
	}

	mgr.TeardownAll(context.Background())

	mgr.mu.Lock()
	for id, s := range mgr.sessions {
		if s.GetStatus() != StatusTerminated {
			t.Errorf("session %s status = %q, want %q", id, s.GetStatus(), StatusTerminated)
		}
	}
	mgr.mu.Unlock()
}

func TestValidateTokenTerminatedSession(t *testing.T) {
	docker := &mockDockerClient{}
	mgr := NewManager(docker, slog.Default())

	session, err := mgr.Provision(context.Background(), ProvisionRequest{
		Repository:     "owner/repo",
		IssueNumber:    1,
		Provider:       "claude",
		TTLSeconds:     3600,
		ResourceLimits: DefaultResourceLimits(),
		WorktreePath:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	token := session.Token
	_ = mgr.Teardown(context.Background(), session.ID, "test")

	if _, err := mgr.ValidateToken(token); err == nil {
		t.Error("expected error validating token for terminated session")
	}
}

func TestReconcileStale(t *testing.T) {
	docker := &mockDockerClient{
		containers: map[string]bool{
			"orphaned1": true,
			"orphaned2": false,
		},
	}
	mgr := NewManager(docker, slog.Default())

	if err := mgr.ReconcileStale(context.Background()); err != nil {
		t.Fatalf("ReconcileStale: %v", err)
	}

	docker.mu.Lock()
	if len(docker.containers) != 0 {
		t.Errorf("containers remaining = %d, want 0", len(docker.containers))
	}
	docker.mu.Unlock()
}

func TestGenerateSessionID(t *testing.T) {
	id := generateSessionID()
	if len(id) == 0 {
		t.Error("empty session ID")
	}
	if id[:4] != "sbx_" {
		t.Errorf("session ID %q does not start with sbx_", id)
	}
}

func TestDefaultResourceLimits(t *testing.T) {
	limits := DefaultResourceLimits()
	if limits.Memory != "8g" {
		t.Errorf("memory = %q, want %q", limits.Memory, "8g")
	}
	if limits.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", limits.CPUs)
	}
	if limits.Disk != "20g" {
		t.Errorf("disk = %q, want %q", limits.Disk, "20g")
	}
}
