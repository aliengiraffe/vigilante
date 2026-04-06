// Package proxy implements the sandbox reverse proxy that intercepts gh CLI
// commands from sandbox containers and enforces repository-scoped access.
//
// The proxy validates every request's sandbox token, checks that the target
// repository matches the session scope, and executes the gh command on the
// host using the operator's authenticated gh CLI.
package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/sandbox/token"
)

// GHRequest is the request body sent by the gh mirror binary.
type GHRequest struct {
	Command string `json:"command"`
	Token   string `json:"token"`
}

// GHResponse is the response returned to the gh mirror binary.
type GHResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// TokenRefreshRequest is used by the daemon to extend a session token's TTL.
type TokenRefreshRequest struct {
	SessionID     string `json:"session_id"`
	ExtendSeconds int64  `json:"extend_seconds"`
}

// SessionEntry tracks an active sandbox session in the proxy.
type SessionEntry struct {
	SessionID  string
	Repository string
	ExpiresAt  time.Time
}

// Proxy is the sandbox reverse proxy server.
type Proxy struct {
	signingKey []byte
	runner     environment.Runner
	logger     *slog.Logger

	mu       sync.RWMutex
	sessions map[string]SessionEntry

	server   *http.Server
	listener net.Listener
}

// New creates a new sandbox reverse proxy.
func New(signingKey []byte, runner environment.Runner, logger *slog.Logger) *Proxy {
	p := &Proxy{
		signingKey: signingKey,
		runner:     runner,
		logger:     logger,
		sessions:   make(map[string]SessionEntry),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sandbox/gh", p.handleGH)
	mux.HandleFunc("POST /api/sandbox/token/refresh", p.handleTokenRefresh)
	p.server = &http.Server{Handler: mux}
	return p
}

// RegisterSession adds a session to the proxy's active set.
func (p *Proxy) RegisterSession(entry SessionEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sessions[entry.SessionID] = entry
}

// DeregisterSession removes a session from the proxy's active set.
func (p *Proxy) DeregisterSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.sessions, sessionID)
}

// Start binds the proxy to the given address and serves requests.
func (p *Proxy) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy listen %s: %w", addr, err)
	}
	p.listener = ln
	p.logger.Info("sandbox proxy started", "addr", ln.Addr().String())
	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			p.logger.Error("sandbox proxy serve error", "err", err)
		}
	}()
	return nil
}

// Addr returns the listener address, or empty if not started.
func (p *Proxy) Addr() string {
	if p.listener == nil {
		return ""
	}
	return p.listener.Addr().String()
}

// Stop gracefully shuts down the proxy.
func (p *Proxy) Stop(ctx context.Context) error {
	return p.server.Shutdown(ctx)
}

func (p *Proxy) handleGH(w http.ResponseWriter, r *http.Request) {
	var req GHRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeGHResponse(w, http.StatusBadRequest, GHResponse{
			ExitCode: 1,
			Stderr:   "sandbox: invalid request body",
		})
		return
	}

	claims, err := token.Validate(p.signingKey, req.Token, time.Now())
	if err != nil {
		p.logger.Warn("sandbox token validation failed", "err", err)
		writeGHResponse(w, http.StatusForbidden, GHResponse{
			ExitCode: 1,
			Stderr:   fmt.Sprintf("sandbox: token validation failed — %s", err),
		})
		return
	}

	p.mu.RLock()
	session, ok := p.sessions[claims.SessionID]
	p.mu.RUnlock()
	if !ok {
		writeGHResponse(w, http.StatusForbidden, GHResponse{
			ExitCode: 1,
			Stderr:   "sandbox: session not found or deregistered",
		})
		return
	}

	if !p.isCommandAllowed(req.Command, session.Repository) {
		writeGHResponse(w, http.StatusForbidden, GHResponse{
			ExitCode: 1,
			Stderr:   fmt.Sprintf("sandbox: operation denied — command targets a repository outside session scope (%s)", session.Repository),
		})
		return
	}

	// Execute the gh command on the host, scoped to the allowed repository.
	ghArgs := buildGHArgs(req.Command, session.Repository)
	out, execErr := p.runner.Run(r.Context(), "", "gh", ghArgs...)

	resp := GHResponse{Stdout: out}
	if execErr != nil {
		resp.ExitCode = 1
		resp.Stderr = execErr.Error()
	}
	writeGHResponse(w, http.StatusOK, resp)
}

func (p *Proxy) handleTokenRefresh(w http.ResponseWriter, r *http.Request) {
	var req TokenRefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	session, ok := p.sessions[req.SessionID]
	if ok {
		session.ExpiresAt = session.ExpiresAt.Add(time.Duration(req.ExtendSeconds) * time.Second)
		p.sessions[req.SessionID] = session
	}
	p.mu.Unlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "extended"})
}

// isCommandAllowed checks whether a gh command is permitted for the given repo.
// Commands that explicitly target a different repository are rejected.
func (p *Proxy) isCommandAllowed(command string, allowedRepo string) bool {
	parts := strings.Fields(command)
	for i, part := range parts {
		if part == "--repo" || part == "-R" {
			if i+1 < len(parts) {
				target := parts[i+1]
				return strings.EqualFold(target, allowedRepo)
			}
		}
		if strings.HasPrefix(part, "--repo=") {
			target := strings.TrimPrefix(part, "--repo=")
			return strings.EqualFold(target, allowedRepo)
		}
		if strings.HasPrefix(part, "-R=") {
			target := strings.TrimPrefix(part, "-R=")
			return strings.EqualFold(target, allowedRepo)
		}
	}
	// No explicit repo target — allowed (proxy injects --repo).
	return true
}

// buildGHArgs splits the command string and ensures --repo is set.
func buildGHArgs(command string, repo string) []string {
	parts := strings.Fields(command)
	hasRepo := false
	for _, p := range parts {
		if p == "--repo" || p == "-R" || strings.HasPrefix(p, "--repo=") || strings.HasPrefix(p, "-R=") {
			hasRepo = true
			break
		}
	}
	if !hasRepo {
		parts = append(parts, "--repo", repo)
	}
	return parts
}

func writeGHResponse(w http.ResponseWriter, status int, resp GHResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}
