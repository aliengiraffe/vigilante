package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/sandbox/token"
)

func TestProxyLifecycleAndRefresh(t *testing.T) {
	key, _ := token.GenerateSigningKey()
	p := New(key, &fakeRunner{}, slog.Default())
	if p.Addr() != "" {
		t.Fatalf("unstarted addr=%q", p.Addr())
	}
	entry := SessionEntry{SessionID: "session", Repository: "owner/repo", ExpiresAt: time.Now()}
	p.RegisterSession(entry)
	body, _ := json.Marshal(TokenRefreshRequest{SessionID: "session", ExtendSeconds: 60})
	w := httptest.NewRecorder()
	p.handleTokenRefresh(w, httptest.NewRequest(http.MethodPost, "/api/sandbox/token/refresh", bytes.NewReader(body)))
	if w.Code != http.StatusOK || !p.sessions["session"].ExpiresAt.Equal(entry.ExpiresAt.Add(time.Minute)) {
		t.Fatalf("refresh status=%d entry=%#v", w.Code, p.sessions["session"])
	}
	w = httptest.NewRecorder()
	p.handleTokenRefresh(w, httptest.NewRequest(http.MethodPost, "/api/sandbox/token/refresh", strings.NewReader("bad")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad refresh=%d", w.Code)
	}
	p.DeregisterSession("session")
	w = httptest.NewRecorder()
	p.handleTokenRefresh(w, httptest.NewRequest(http.MethodPost, "/api/sandbox/token/refresh", bytes.NewReader(body)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing refresh=%d", w.Code)
	}
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if p.Addr() == "" {
		t.Fatal("started proxy has empty address")
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGHEndpointInvalidMissingSessionAndRunnerError(t *testing.T) {
	key, _ := token.GenerateSigningKey()
	runner := &fakeRunner{err: fmt.Errorf("gh failed")}
	p := New(key, runner, slog.Default())
	w := httptest.NewRecorder()
	p.handleGH(w, httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", strings.NewReader("bad")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid=%d", w.Code)
	}
	tok, _ := token.Issue(key, token.Claims{SessionID: "missing", Repository: "owner/repo", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	body, _ := json.Marshal(GHRequest{Command: "issue list", Token: tok})
	w = httptest.NewRecorder()
	p.handleGH(w, httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", bytes.NewReader(body)))
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing=%d", w.Code)
	}
	p.RegisterSession(SessionEntry{SessionID: "missing", Repository: "owner/repo"})
	w = httptest.NewRecorder()
	p.handleGH(w, httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", bytes.NewReader(body)))
	var resp GHResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if w.Code != http.StatusOK || resp.ExitCode != 1 || !strings.Contains(resp.Stderr, "gh failed") {
		t.Fatalf("status=%d response=%#v", w.Code, resp)
	}
}

type fakeRunner struct {
	lastArgs  []string
	lastStdin string
	out       string
	err       error
}

func (f *fakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	f.lastArgs = append([]string{name}, args...)
	return f.out, f.err
}

func (f *fakeRunner) RunWithStdin(_ context.Context, stdin string, _ string, name string, args ...string) (string, error) {
	f.lastArgs = append([]string{name}, args...)
	f.lastStdin = stdin
	return f.out, f.err
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	return "/usr/bin/" + file, nil
}

func TestGHEndpoint(t *testing.T) {
	key, _ := token.GenerateSigningKey()
	runner := &fakeRunner{out: "result from gh\n"}
	logger := slog.Default()

	p := New(key, runner, logger)
	p.RegisterSession(SessionEntry{
		SessionID:  "sbx_test",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	tok, _ := token.Issue(key, token.Claims{
		SessionID:  "sbx_test",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
	})

	body, _ := json.Marshal(GHRequest{
		Command: "issue list",
		Token:   tok,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", bytes.NewReader(body))
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sandbox/gh", p.handleGH)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp GHResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0; stderr = %s", resp.ExitCode, resp.Stderr)
	}
	if !strings.Contains(resp.Stdout, "result from gh") {
		t.Errorf("stdout = %q, want to contain 'result from gh'", resp.Stdout)
	}
	// Verify --repo was injected
	joined := strings.Join(runner.lastArgs, " ")
	if !strings.Contains(joined, "--repo owner/repo") {
		t.Errorf("expected --repo injection, got: %s", joined)
	}
}

func TestGHEndpointForwardsStdin(t *testing.T) {
	key, _ := token.GenerateSigningKey()
	runner := &fakeRunner{out: "ok\n"}
	logger := slog.Default()

	p := New(key, runner, logger)
	p.RegisterSession(SessionEntry{
		SessionID:  "sbx_test",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	tok, _ := token.Issue(key, token.Claims{
		SessionID:  "sbx_test",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
	})

	body, _ := json.Marshal(GHRequest{
		Command: "issue comment 42 --body-file -",
		Token:   tok,
		Stdin:   "the funny sentence body",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", bytes.NewReader(body))
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sandbox/gh", p.handleGH)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if runner.lastStdin != "the funny sentence body" {
		t.Errorf("expected stdin forwarded to runner, got: %q", runner.lastStdin)
	}
	joined := strings.Join(runner.lastArgs, " ")
	if !strings.Contains(joined, "--body-file -") {
		t.Errorf("expected --body-file - in args, got: %s", joined)
	}
}

func TestGHEndpointRejectsCrossRepo(t *testing.T) {
	key, _ := token.GenerateSigningKey()
	runner := &fakeRunner{}
	logger := slog.Default()

	p := New(key, runner, logger)
	p.RegisterSession(SessionEntry{
		SessionID:  "sbx_test",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour),
	})

	tok, _ := token.Issue(key, token.Claims{
		SessionID:  "sbx_test",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(time.Hour).Unix(),
	})

	body, _ := json.Marshal(GHRequest{
		Command: "issue list --repo other/repo",
		Token:   tok,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", bytes.NewReader(body))
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sandbox/gh", p.handleGH)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestGHEndpointRejectsExpiredToken(t *testing.T) {
	key, _ := token.GenerateSigningKey()
	runner := &fakeRunner{}
	logger := slog.Default()

	p := New(key, runner, logger)

	tok, _ := token.Issue(key, token.Claims{
		SessionID:  "sbx_expired",
		Repository: "owner/repo",
		ExpiresAt:  time.Now().Add(-time.Hour).Unix(),
	})

	body, _ := json.Marshal(GHRequest{
		Command: "issue list",
		Token:   tok,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/sandbox/gh", bytes.NewReader(body))
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sandbox/gh", p.handleGH)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestIsCommandAllowed(t *testing.T) {
	p := &Proxy{}
	tests := []struct {
		command string
		repo    string
		want    bool
	}{
		{"issue list", "owner/repo", true},
		{"issue list --repo owner/repo", "owner/repo", true},
		{"issue list --repo other/repo", "owner/repo", false},
		{"issue list -R owner/repo", "owner/repo", true},
		{"issue list -R other/repo", "owner/repo", false},
		{"pr create --title test", "owner/repo", true},
	}

	for _, tt := range tests {
		got := p.isCommandAllowed(tt.command, tt.repo)
		if got != tt.want {
			t.Errorf("isCommandAllowed(%q, %q) = %v, want %v", tt.command, tt.repo, got, tt.want)
		}
	}
}

func TestBuildGHArgs(t *testing.T) {
	args := buildGHArgs("issue list", "owner/repo")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--repo owner/repo") {
		t.Errorf("expected --repo injection: %s", joined)
	}

	args = buildGHArgs("issue list --repo owner/repo", "owner/repo")
	count := 0
	for _, a := range args {
		if a == "--repo" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 --repo, got %d", count)
	}
}
