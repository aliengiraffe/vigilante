package sandbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ProvisionRequest is the JSON body for POST /api/sandbox/provision.
type ProvisionRequest struct {
	Repository     string         `json:"repository"`
	IssueNumber    int            `json:"issue_number"`
	Provider       string         `json:"provider"`
	TTLSeconds     int64          `json:"ttl_seconds"`
	ResourceLimits ResourceLimits `json:"resource_limits"`
}

// ResourceLimits describes container resource constraints.
type ResourceLimits struct {
	Memory string `json:"memory"`
	CPUs   string `json:"cpus"`
	Disk   string `json:"disk"`
}

// ProvisionResponse is the JSON response for POST /api/sandbox/provision.
type ProvisionResponse struct {
	SessionID    string `json:"session_id"`
	ContainerID  string `json:"container_id"`
	SandboxToken string `json:"sandbox_token"`
	ProxyPort    int    `json:"proxy_port"`
	SSHPublicKey string `json:"ssh_public_key"`
	DeployKeyID  int64  `json:"deploy_key_id"`
	ExpiresAt    string `json:"expires_at"`
	Status       string `json:"status"`
}

// SessionResponse is the JSON response for GET /api/sandbox/sessions/:id.
type SessionResponse struct {
	SessionID   string         `json:"session_id"`
	Repository  string         `json:"repository"`
	IssueNumber int            `json:"issue_number"`
	Provider    string         `json:"provider"`
	Status      string         `json:"status"`
	CreatedAt   string         `json:"created_at"`
	ExpiresAt   string         `json:"expires_at"`
	ContainerID string         `json:"container_id"`
	Usage       *ResourceUsage `json:"resource_usage,omitempty"`
}

// ResourceUsage reports live container resource consumption.
type ResourceUsage struct {
	MemoryMB   int     `json:"memory_mb"`
	CPUPercent float64 `json:"cpu_percent"`
}

// TeardownRequest is the JSON body for POST /api/sandbox/sessions/:id/teardown.
type TeardownRequest struct {
	Reason           string `json:"reason"`
	ExtractArtifacts bool   `json:"extract_artifacts"`
}

// Handler returns an http.Handler that serves the sandbox management API.
func (m *Manager) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sandbox/provision", m.handleProvision)
	mux.HandleFunc("GET /api/sandbox/sessions/{id}", m.handleGetSession)
	mux.HandleFunc("POST /api/sandbox/sessions/{id}/teardown", m.handleTeardown)
	return mux
}

func (m *Manager) handleProvision(w http.ResponseWriter, r *http.Request) {
	var req ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Repository) == "" {
		http.Error(w, "repository is required", http.StatusBadRequest)
		return
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl == 0 {
		ttl = DefaultTTL
	}

	sess, err := m.Provision(r.Context(), SessionConfig{
		Repository:  req.Repository,
		IssueNumber: req.IssueNumber,
		Provider:    req.Provider,
		TTL:         ttl,
		MemoryLimit: req.ResourceLimits.Memory,
		CPUs:        req.ResourceLimits.CPUs,
		EnableDinD:  true,
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("provision failed: %s", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, ProvisionResponse{
		SessionID:    sess.ID,
		ContainerID:  sess.ContainerID,
		SandboxToken: sess.SandboxToken,
		ProxyPort:    sess.ProxyPort,
		SSHPublicKey: sess.SSHPublicKey,
		DeployKeyID:  sess.DeployKeyID,
		ExpiresAt:    sess.ExpiresAt.Format(time.RFC3339),
		Status:       sess.Status,
	})
}

func (m *Manager) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, ok := m.GetSession(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, SessionResponse{
		SessionID:   sess.ID,
		Repository:  sess.Repository,
		IssueNumber: sess.IssueNumber,
		Provider:    sess.Provider,
		Status:      sess.Status,
		CreatedAt:   sess.CreatedAt.Format(time.RFC3339),
		ExpiresAt:   sess.ExpiresAt.Format(time.RFC3339),
		ContainerID: sess.ContainerID,
	})
}

func (m *Manager) handleTeardown(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req TeardownRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	reason := req.Reason
	if reason == "" {
		reason = "api_teardown"
	}

	if err := m.Teardown(r.Context(), id, reason); err != nil {
		http.Error(w, fmt.Sprintf("teardown failed: %s", err), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "terminated"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
