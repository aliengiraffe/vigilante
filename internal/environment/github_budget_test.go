package environment

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/nicobistolfi/vigilante/internal/testutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type budgetFake struct {
	mu               sync.Mutex
	probes, requests int
	remaining        int
	reset            time.Time
	failure          string
	headers          string
	probeFailure     bool
}

func (f *budgetFake) LookPath(s string) (string, error) { return s, nil }
func (f *budgetFake) Run(_ context.Context, _ string, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(args) == 2 && args[1] == "/rate_limit" {
		f.probes++
		if f.probeFailure {
			return "", errors.New("offline")
		}
		return fmt.Sprintf(`{"resources":{"core":{"limit":5000,"remaining":%d,"reset":%d},"graphql":{"limit":5000,"remaining":4999,"reset":%d}}}`, f.remaining, f.reset.Unix(), f.reset.Unix()), nil
	}
	f.requests++
	if f.failure != "" {
		return f.headers + f.failure, errors.New("HTTP 403")
	}
	return f.headers + `[]`, nil
}
func budgetContext() context.Context {
	return WithAccessLogContext(context.Background(), AccessLogContext{ExecutionContext: "scan"})
}

func TestGitHubBudgetPersistsAllowanceAndResumes(t *testing.T) {
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	base := &budgetFake{remaining: 5000, reset: now.Add(time.Hour)}
	path := filepath.Join(t.TempDir(), "budget.json")
	makeRunner := func() *GitHubBudgetRunner {
		return &GitHubBudgetRunner{Base: base, Limit: 2, Path: path, Clock: func() time.Time { return now }}
	}
	r := makeRunner()
	for i := 0; i < 2; i++ {
		if _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/1"); err != nil {
			t.Fatal(err)
		}
	}
	r = makeRunner() // Restart must not refund reservations.
	if _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/1"); !errors.Is(err, ErrGitHubDeferred) {
		t.Fatal(err)
	}
	if base.requests != 2 {
		t.Fatalf("dispatched %d requests", base.requests)
	}
	now = now.Add(time.Hour)
	if _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/1"); err != nil {
		t.Fatal(err)
	}
	if r.Status().Used != 1 {
		t.Fatal(r.Status())
	}
}

func TestGitHubBudgetStopsMidPassAndPersistsPause(t *testing.T) {
	for _, failure := range []string{"API rate limit exceeded", "secondary rate limit exceeded", "abuse detection", "HTTP 429"} {
		t.Run(failure, func(t *testing.T) {
			now := time.Now()
			base := &budgetFake{remaining: 5000, reset: now.Add(20 * time.Minute), failure: failure}
			path := filepath.Join(t.TempDir(), "budget.json")
			r := &GitHubBudgetRunner{Base: base, Path: path, Clock: func() time.Time { return now }}
			if _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/1"); !errors.Is(err, ErrGitHubDeferred) {
				t.Fatal(err)
			}
			r = &GitHubBudgetRunner{Base: base, Path: path, Clock: func() time.Time { return now }}
			for i := 0; i < 50; i++ {
				if _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/2"); !errors.Is(err, ErrGitHubDeferred) {
					t.Fatal(err)
				}
			}
			if base.requests != 1 {
				t.Fatalf("continued after exhaustion: %d", base.requests)
			}
			now = now.Add(2 * time.Hour)
			base.failure = ""
			if _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/2"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGitHubBudgetExternalConsumptionAndProbeFailures(t *testing.T) {
	now := time.Now()
	base := &budgetFake{remaining: 5000, reset: now.Add(time.Hour)}
	r := &GitHubBudgetRunner{Base: base, Clock: func() time.Time { return now }}
	run := func() error { _, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/1"); return err }
	if err := run(); err != nil {
		t.Fatal(err)
	}
	base.remaining = 5
	now = now.Add(time.Minute)
	if err := run(); !errors.Is(err, ErrGitHubDeferred) {
		t.Fatal(err)
	}
	if base.requests != 1 {
		t.Fatal("dispatched with only five remaining")
	}
	base.probeFailure = true
	now = now.Add(time.Minute)
	for i := 0; i < 20; i++ {
		if err := run(); !errors.Is(err, ErrGitHubDeferred) {
			t.Fatal(err)
		}
	}
	if base.probes != 3 {
		t.Fatalf("probe storm: %d", base.probes)
	}
	base.probeFailure = false
	base.remaining = 5000
	now = now.Add(time.Minute)
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubBudgetSerializesMaintenanceAndCountsOpaqueCommands(t *testing.T) {
	now := time.Now()
	base := &budgetFake{remaining: 5000, reset: now.Add(time.Hour)}
	r := &GitHubBudgetRunner{Base: base, Limit: 50, Clock: func() time.Time { return now }}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := WithAccessLogContext(context.Background(), AccessLogContext{ExecutionContext: "maintenance"})
			_, _ = r.Run(ctx, "", "gh", "pr", "view", "1")
		}()
	}
	wg.Wait()
	if base.requests != 5 || r.Status().Used != 50 {
		t.Fatalf("requests=%d status=%+v", base.requests, r.Status())
	}
	// Interactive commands remain available; they are not background allowance.
	if _, err := r.Run(context.Background(), "", "gh", "pr", "view", "1"); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubBudgetHeadersAndRetryAfter(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	base := &budgetFake{remaining: 5000, reset: now.Add(time.Hour), failure: `{"message":"secondary rate limit"}`, headers: "HTTP/1.1 403 Forbidden\r\nRetry-After: 7200\r\nX-RateLimit-Resource: core\r\nX-RateLimit-Remaining: 4000\r\n\r\n"}
	r := &GitHubBudgetRunner{Base: base, Clock: func() time.Time { return now }}
	output, err := r.Run(budgetContext(), "", "gh", "api", "repos/o/r/issues/1")
	if !errors.Is(err, ErrGitHubDeferred) || strings.Contains(output, "Retry-After") {
		t.Fatalf("%s %v", output, err)
	}
	if !r.Status().PauseUntil.Equal(now.Add(2 * time.Hour)) {
		t.Fatal(r.Status())
	}
	if r.Status().CoreRemaining != 4000 {
		t.Fatal(r.Status())
	}
}

func TestGitHubBudgetProbeCacheKeepsOtherResources(t *testing.T) {
	now := time.Now()
	base := &budgetFake{remaining: 5000, reset: now.Add(time.Hour)}
	r := &GitHubBudgetRunner{Base: base, Clock: func() time.Time { return now }}
	for i := 0; i < 4; i++ {
		out, err := r.Run(budgetContext(), "", "gh", "api", "/rate_limit")
		if err != nil || !strings.Contains(out, `"graphql"`) {
			t.Fatalf("%s %v", out, err)
		}
	}
	if base.probes != 1 || r.Status().Used != 0 {
		t.Fatal(base.probes, r.Status())
	}
	if _, err := r.Run(budgetContext(), "", "gh", "api", "x", "--paginate"); !errors.Is(err, ErrGitHubDeferred) {
		t.Fatal(err)
	}
}

func TestGitHubBudgetPreservesRunnerCapabilities(t *testing.T) {
	base := testutil.FakeRunner{Outputs: map[string]string{"agent run": "streamed", "gh api test": "stdin-result"}, LookPaths: map[string]string{"agent": "/bin/agent"}}
	r := &GitHubBudgetRunner{Base: base}
	if path, err := r.LookPath("agent"); err != nil || path != "/bin/agent" {
		t.Fatal(path, err)
	}
	var output bytes.Buffer
	got, err := r.RunStreaming(context.Background(), "", &output, "agent", "run")
	if err != nil || got != "streamed" || output.String() != got {
		t.Fatal(got, err)
	}
	got, err = r.RunWithStdin(context.Background(), "input", "", "gh", "api", "test")
	if err != nil || got != "stdin-result" {
		t.Fatal(got, err)
	}
	fallback := &GitHubBudgetRunner{Base: &budgetFake{}}
	output.Reset()
	if _, err = fallback.RunStreaming(context.Background(), "", &output, "agent", "run"); err != nil || output.String() != "[]" {
		t.Fatal(output.String(), err)
	}
	if _, err = fallback.RunWithStdin(context.Background(), "input", "", "agent", "run"); err == nil {
		t.Fatal("silently dropped stdin")
	}
}

func TestGitHubBudgetFailsClosedOnCorruptLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	base := &budgetFake{}
	r := &GitHubBudgetRunner{Base: base, Path: path}
	if _, err := r.Run(budgetContext(), "", "gh", "api", "test"); err == nil {
		t.Fatal("corrupt ledger accepted")
	}
	if base.requests+base.probes != 0 {
		t.Fatal("dispatched before loading ledger")
	}
}

func TestGitHubBudgetHeadersUpdateCoreResetAndDateRetry(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r := &GitHubBudgetRunner{Clock: func() time.Time { return now }}
	reset := now.Add(time.Hour)
	raw := fmt.Sprintf("HTTP/1.1 200 OK\r\nX-RateLimit-Resource: core\r\nX-RateLimit-Remaining: 105\r\nX-RateLimit-Reset: %d\r\nRetry-After: %s\r\n\r\n[]", reset.Unix(), reset.UTC().Format(http.TimeFormat))
	if body := r.readHeaders(raw); body != "[]" || r.remaining != 105 || !r.reset.Equal(reset) || !r.retryAt.Equal(reset) {
		t.Fatalf("body=%q remaining=%d reset=%v retry=%v", body, r.remaining, r.reset, r.retryAt)
	}
	if err := r.observeSnapshot("broken", now); !errors.Is(err, ErrGitHubDeferred) {
		t.Fatal(err)
	}
	if r.now().IsZero() {
		t.Fatal("missing clock")
	}
}
