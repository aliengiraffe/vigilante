package sandbox

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsCommandScopedToRepo(t *testing.T) {
	tests := []struct {
		command string
		repo    string
		want    bool
	}{
		{"issue list", "owner/repo", true},
		{"issue list --repo owner/repo", "owner/repo", true},
		{"issue list --repo other/repo", "owner/repo", false},
		{"pr list -R owner/repo", "owner/repo", true},
		{"pr list -R other/repo", "owner/repo", false},
		{"issue list --repo=owner/repo", "owner/repo", true},
		{"issue list --repo=other/repo", "owner/repo", false},
		{"pr create --title fix --body ok", "owner/repo", true},
		{"", "owner/repo", false},
	}

	for _, tt := range tests {
		got := isCommandScopedToRepo(tt.command, tt.repo)
		if got != tt.want {
			t.Errorf("isCommandScopedToRepo(%q, %q) = %v, want %v", tt.command, tt.repo, got, tt.want)
		}
	}
}

func TestBuildScopedGHCommand(t *testing.T) {
	tests := []struct {
		command string
		repo    string
		want    string
	}{
		{"issue list", "owner/repo", "issue list --repo owner/repo"},
		{"issue list --repo owner/repo", "owner/repo", "issue list --repo owner/repo"},
		{"pr list -R owner/repo", "owner/repo", "pr list -R owner/repo"},
	}

	for _, tt := range tests {
		got := buildScopedGHCommand(tt.command, tt.repo)
		if got != tt.want {
			t.Errorf("buildScopedGHCommand(%q, %q) = %q, want %q", tt.command, tt.repo, got, tt.want)
		}
	}
}

func TestProxyGHEndpointDeniesInvalidToken(t *testing.T) {
	logger := slog.Default()
	mgr := NewManager(&mockDockerClient{}, logger)

	proxy := NewProxy(mgr, logger)

	body, _ := json.Marshal(GHRequest{
		Command: "issue list",
		Token:   "invalid.token.here",
	})

	req := httptest.NewRequest("POST", "/api/sandbox/gh", bytes.NewReader(body))
	w := httptest.NewRecorder()

	proxy.mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp GHResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", resp.ExitCode)
	}
}

func TestProxyGHEndpointDeniesRepoScopeViolation(t *testing.T) {
	logger := slog.Default()
	mgr := NewManager(&mockDockerClient{}, logger)

	// Create a valid session.
	session := &Session{
		ID:         "sbx_test123",
		Repository: "owner/repo",
		Status:     StatusRunning,
		ExpiresAt:  time.Now().Add(time.Hour),
		signingKey: make([]byte, 32),
	}
	copy(session.signingKey, "test-signing-key-32-bytes-long!!")
	mgr.mu.Lock()
	mgr.sessions["sbx_test123"] = session
	mgr.mu.Unlock()

	token, _ := MintToken(session.signingKey, "sbx_test123", "owner/repo", time.Now().Add(time.Hour))

	proxy := NewProxy(mgr, logger)

	body, _ := json.Marshal(GHRequest{
		Command: "issue list --repo other/repo",
		Token:   token,
	})

	req := httptest.NewRequest("POST", "/api/sandbox/gh", bytes.NewReader(body))
	w := httptest.NewRecorder()

	proxy.mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}
