package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type apiTestRunner struct {
	calls []string
	out   string
	err   error
}

func (r *apiTestRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	// When ssh-keygen is called, create a fake keypair so the manager can read it.
	if name == "ssh-keygen" {
		for i, a := range args {
			if a == "-f" && i+1 < len(args) {
				keyPath := args[i+1]
				os.MkdirAll(filepath.Dir(keyPath), 0o700)
				os.WriteFile(keyPath, []byte("fake-private-key"), 0o600)
				os.WriteFile(keyPath+".pub", []byte("ssh-ed25519 AAAA...fake test@host\n"), 0o644)
				break
			}
		}
	}
	// Deploy key API calls return JSON with an id field.
	cmd := name + " " + strings.Join(args, " ")
	if strings.Contains(cmd, "/keys") && strings.Contains(cmd, "POST") {
		return `{"id":99999,"title":"test","read_only":false}`, r.err
	}
	if strings.Contains(cmd, "/keys/") && strings.Contains(cmd, "DELETE") {
		return "", r.err
	}
	return r.out, r.err
}

func (r *apiTestRunner) LookPath(file string) (string, error) {
	return "/usr/bin/" + file, nil
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	runner := &apiTestRunner{out: "fake-container-id\n"}
	logger := slog.Default()
	mgr, err := NewManager(runner, logger, t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.StartProxy("127.0.0.1:0"); err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	t.Cleanup(func() { mgr.StopProxy(context.Background()) })
	return mgr
}

func TestHandleProvisionSuccess(t *testing.T) {
	mgr := newTestManager(t)
	handler := mgr.Handler()

	body, _ := json.Marshal(ProvisionRequest{
		Repository:  "owner/repo",
		IssueNumber: 42,
		Provider:    "claude-code",
		TTLSeconds:  3600,
		ResourceLimits: ResourceLimits{
			Memory: "4g",
			CPUs:   "2",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sandbox/provision", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	var resp ProvisionResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.SessionID == "" {
		t.Error("expected non-empty session_id")
	}
	if resp.Status != "provisioned" {
		t.Errorf("status = %q, want %q", resp.Status, "provisioned")
	}
	if resp.ProxyPort == 0 {
		t.Error("expected non-zero proxy_port")
	}
	if resp.ExpiresAt == "" {
		t.Error("expected non-empty expires_at")
	}
}

func TestHandleProvisionMissingRepository(t *testing.T) {
	mgr := newTestManager(t)
	handler := mgr.Handler()

	body, _ := json.Marshal(ProvisionRequest{
		IssueNumber: 1,
		Provider:    "codex",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sandbox/provision", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestHandleGetSession(t *testing.T) {
	mgr := newTestManager(t)
	handler := mgr.Handler()

	// First provision a session.
	body, _ := json.Marshal(ProvisionRequest{
		Repository:  "owner/repo",
		IssueNumber: 7,
		Provider:    "codex",
	})
	provReq := httptest.NewRequest(http.MethodPost, "/api/sandbox/provision", bytes.NewReader(body))
	provW := httptest.NewRecorder()
	handler.ServeHTTP(provW, provReq)
	if provW.Code != http.StatusCreated {
		t.Fatalf("provision status = %d", provW.Code)
	}
	var provResp ProvisionResponse
	json.NewDecoder(provW.Body).Decode(&provResp)

	// Now GET the session.
	getReq := httptest.NewRequest(http.MethodGet, "/api/sandbox/sessions/"+provResp.SessionID, nil)
	getW := httptest.NewRecorder()
	handler.ServeHTTP(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body = %s", getW.Code, getW.Body.String())
	}
	var sessResp SessionResponse
	json.NewDecoder(getW.Body).Decode(&sessResp)
	if sessResp.SessionID != provResp.SessionID {
		t.Errorf("session_id = %q, want %q", sessResp.SessionID, provResp.SessionID)
	}
	if sessResp.Repository != "owner/repo" {
		t.Errorf("repository = %q, want %q", sessResp.Repository, "owner/repo")
	}
	if sessResp.IssueNumber != 7 {
		t.Errorf("issue_number = %d, want 7", sessResp.IssueNumber)
	}
}

func TestHandleGetSessionNotFound(t *testing.T) {
	mgr := newTestManager(t)
	handler := mgr.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/sandbox/sessions/sbx_nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHandleTeardown(t *testing.T) {
	mgr := newTestManager(t)
	handler := mgr.Handler()

	// Provision a session.
	body, _ := json.Marshal(ProvisionRequest{
		Repository:  "owner/repo",
		IssueNumber: 10,
		Provider:    "codex",
	})
	provReq := httptest.NewRequest(http.MethodPost, "/api/sandbox/provision", bytes.NewReader(body))
	provW := httptest.NewRecorder()
	handler.ServeHTTP(provW, provReq)
	var provResp ProvisionResponse
	json.NewDecoder(provW.Body).Decode(&provResp)

	// Tear it down.
	tdBody, _ := json.Marshal(TeardownRequest{
		Reason:           "completed",
		ExtractArtifacts: false,
	})
	tdReq := httptest.NewRequest(http.MethodPost, "/api/sandbox/sessions/"+provResp.SessionID+"/teardown", bytes.NewReader(tdBody))
	tdW := httptest.NewRecorder()
	handler.ServeHTTP(tdW, tdReq)

	if tdW.Code != http.StatusOK {
		t.Fatalf("teardown status = %d, want 200; body = %s", tdW.Code, tdW.Body.String())
	}

	// Session should be gone.
	getReq := httptest.NewRequest(http.MethodGet, "/api/sandbox/sessions/"+provResp.SessionID, nil)
	getW := httptest.NewRecorder()
	handler.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusNotFound {
		t.Fatalf("get after teardown: status = %d, want 404", getW.Code)
	}
}
