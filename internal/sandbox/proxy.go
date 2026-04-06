package sandbox

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// GHRequest is the payload sent by the gh mirror binary to the reverse proxy.
type GHRequest struct {
	Command string `json:"command"`
	Token   string `json:"token"`
}

// GHResponse is what the proxy returns to the gh mirror binary.
type GHResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// Proxy is the reverse proxy HTTP server that intercepts gh CLI calls from
// inside the sandbox container and enforces repository-scoped access control.
type Proxy struct {
	manager *Manager
	logger  *slog.Logger
	mux     *http.ServeMux
}

// NewProxy creates a reverse proxy backed by the sandbox manager.
func NewProxy(manager *Manager, logger *slog.Logger) *Proxy {
	p := &Proxy{
		manager: manager,
		logger:  logger,
		mux:     http.NewServeMux(),
	}
	p.mux.HandleFunc("POST /api/sandbox/gh", p.handleGH)
	p.mux.HandleFunc("GET /api/sandbox/sessions/{session_id}", p.handleGetSession)
	p.mux.HandleFunc("POST /api/sandbox/sessions/{session_id}/teardown", p.handleTeardown)
	p.mux.HandleFunc("POST /api/sandbox/token/refresh", p.handleTokenRefresh)
	return p
}

// Serve starts serving on the given listener. Blocks until the listener closes.
func (p *Proxy) Serve(ln net.Listener) {
	server := &http.Server{Handler: p.mux}
	_ = server.Serve(ln)
}

func (p *Proxy) handleGH(w http.ResponseWriter, r *http.Request) {
	var req GHRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, GHResponse{
			ExitCode: 1,
			Stderr:   "sandbox: invalid request body",
		})
		return
	}

	session, err := p.manager.ValidateToken(req.Token)
	if err != nil {
		p.logger.Warn("sandbox gh request denied", "err", err)
		writeJSON(w, http.StatusForbidden, GHResponse{
			ExitCode: 1,
			Stderr:   fmt.Sprintf("sandbox: %v", err),
		})
		return
	}

	// Check that the command targets the allowed repository.
	if !isCommandScopedToRepo(req.Command, session.Repository) {
		p.logger.Warn("sandbox repo scope violation", "session_id", session.ID, "command", req.Command, "allowed_repo", session.Repository)
		writeJSON(w, http.StatusForbidden, GHResponse{
			ExitCode: 1,
			Stderr:   fmt.Sprintf("sandbox: operation denied — target repository is outside session scope (allowed: %s)", session.Repository),
		})
		return
	}

	// Execute the gh command on the host, scoped to the allowed repo.
	cmd := buildScopedGHCommand(req.Command, session.Repository)
	output, execErr := execGHCommand(r.Context(), cmd)

	exitCode := 0
	stderr := ""
	if execErr != nil {
		exitCode = 1
		stderr = execErr.Error()
	}

	writeJSON(w, http.StatusOK, GHResponse{
		ExitCode: exitCode,
		Stdout:   output,
		Stderr:   stderr,
	})
}

func (p *Proxy) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	session := p.manager.GetSession(sessionID)
	if session == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	writeJSON(w, http.StatusOK, session)
}

func (p *Proxy) handleTeardown(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")

	var req struct {
		Reason           string `json:"reason"`
		ExtractArtifacts bool   `json:"extract_artifacts"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Reason == "" {
		req.Reason = "api_request"
	}

	if err := p.manager.Teardown(r.Context(), sessionID, req.Reason); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "terminated"})
}

func (p *Proxy) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID     string `json:"session_id"`
		ExtendSeconds int    `json:"extend_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if err := p.manager.RefreshToken(req.SessionID, time.Duration(req.ExtendSeconds)*time.Second); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	session := p.manager.GetSession(req.SessionID)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": req.SessionID,
		"expires_at": session.ExpiresAt,
	})
}

// isCommandScopedToRepo checks whether a gh command targets the allowed repo
// or is repo-agnostic. Commands that explicitly target a different repo are
// rejected.
func isCommandScopedToRepo(command string, allowedRepo string) bool {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return false
	}

	for i, part := range parts {
		if part == "--repo" || part == "-R" {
			if i+1 < len(parts) {
				targetRepo := parts[i+1]
				return strings.EqualFold(targetRepo, allowedRepo)
			}
		}
		if strings.HasPrefix(part, "--repo=") {
			targetRepo := strings.TrimPrefix(part, "--repo=")
			return strings.EqualFold(targetRepo, allowedRepo)
		}
		if strings.HasPrefix(part, "-R=") {
			targetRepo := strings.TrimPrefix(part, "-R=")
			return strings.EqualFold(targetRepo, allowedRepo)
		}
	}

	// No explicit repo flag — the command will use the default, which is fine
	// since gh inside the sandbox has no independent auth.
	return true
}

// buildScopedGHCommand ensures the --repo flag is set to the allowed repo.
func buildScopedGHCommand(command string, repo string) string {
	parts := strings.Fields(command)
	hasRepo := false
	for _, p := range parts {
		if p == "--repo" || p == "-R" || strings.HasPrefix(p, "--repo=") || strings.HasPrefix(p, "-R=") {
			hasRepo = true
			break
		}
	}
	if !hasRepo {
		return command + " --repo " + repo
	}
	return command
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
