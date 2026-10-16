package environment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrGitHubDeferred means work must wait for the local budget or GitHub reset.
var ErrGitHubDeferred = errors.New("GitHub request deferred by quota budget")

// GitHubBudgetRunner serializes background GitHub commands through a shared,
// restart-safe hourly allowance. Opaque gh commands reserve ten request units;
// explicit API calls reserve one. The quota probe itself is primary-quota free.
// Child provider processes are outside this runner; live probes account for
// their consumption and that of other clients.
type GitHubBudgetRunner struct {
	Base         Runner
	Path         string
	Limit        int
	Clock        func() time.Time
	AccessLog    func(AccessLogEntry)
	mu           sync.Mutex
	loaded       bool
	state        githubBudgetState
	remaining    int
	reset        time.Time
	checked      time.Time
	snapshotJSON string
	probeErr     error
	retryAt      time.Time
}

type githubBudgetState struct {
	Window     time.Time `json:"window"`
	Used       int       `json:"used"`
	PauseUntil time.Time `json:"pause_until"`
}

func (r *GitHubBudgetRunner) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

func backgroundGitHub(ctx context.Context, name string) bool {
	meta, _ := ctx.Value(accessLogContextKey{}).(AccessLogContext)
	return filepath.Base(name) == "gh" && (meta.ExecutionContext == "scan" || meta.ExecutionContext == "maintenance")
}

// Run executes a command after reserving background quota, when applicable.
func (r *GitHubBudgetRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	return r.run(ctx, dir, name, args, func() (string, error) {
		if backgroundGitHub(ctx, name) && len(args) > 1 && args[0] == "api" && args[1] != "/rate_limit" {
			included := append(append([]string(nil), args...), "--include")
			output, err := r.Base.Run(ctx, dir, name, included...)
			return r.readHeaders(output), err
		}
		return r.Base.Run(ctx, dir, name, args...)
	})
}

func (r *GitHubBudgetRunner) run(ctx context.Context, dir, name string, args []string, call func() (string, error)) (string, error) {
	if !backgroundGitHub(ctx, name) {
		return call()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !r.loaded {
		if r.Path != "" {
			data, err := os.ReadFile(r.Path)
			if err == nil {
				if err = json.Unmarshal(data, &r.state); err != nil {
					return "", fmt.Errorf("load GitHub budget: %w", err)
				}
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		r.loaded = true
	}
	probe := len(args) == 2 && args[0] == "api" && args[1] == "/rate_limit"
	if probe {
		if !r.checked.IsZero() && now.Sub(r.checked) < time.Minute {
			return r.snapshot(), r.probeErr
		}
		output, err := call()
		r.checked = now
		if err == nil {
			err = r.observeSnapshot(output, now)
		}
		r.probeErr = err
		return output, err
	}
	if now.Before(r.state.PauseUntil) {
		return "", ErrGitHubDeferred
	}
	if r.state.Window.IsZero() || now.Sub(r.state.Window) >= time.Hour {
		r.state.Window = now
		r.state.Used = 0
	}
	limit := r.Limit
	if limit <= 0 || limit > 4000 {
		limit = 2000
	}
	cost := 10
	if len(args) > 0 && args[0] == "api" {
		cost = 1
	}
	// Unbounded CLI pagination cannot be reserved safely. Background callers must
	// issue pages separately so every page passes through this guard.
	for _, a := range args {
		if a == "--paginate" {
			return "", fmt.Errorf("%w: use individually budgeted pages", ErrGitHubDeferred)
		}
	}
	if r.state.Used+cost > limit {
		return "", ErrGitHubDeferred
	}
	if r.checked.IsZero() || now.Sub(r.checked) >= time.Minute {
		started := r.now()
		output, err := r.Base.Run(ctx, "", "gh", "api", "/rate_limit")
		if r.AccessLog != nil {
			r.AccessLog(buildAccessLogEntry(ctx, "", "gh", []string{"api", "/rate_limit"}, started, r.now(), "", err))
		}
		r.checked = now
		if err == nil {
			err = r.observeSnapshot(output, now)
		}
		r.probeErr = err
	}
	if r.probeErr != nil {
		return "", fmt.Errorf("%w: quota probe failed", ErrGitHubDeferred)
	}
	if r.remaining-cost < 100 {
		return "", ErrGitHubDeferred
	}
	r.state.Used += cost
	r.remaining -= cost
	// Persist before dispatch: a crash must not refund already dispatched work.
	if err := r.save(); err != nil {
		return "", fmt.Errorf("save GitHub budget: %w", err)
	}
	r.retryAt = time.Time{}
	output, err := call()
	if err != nil {
		lower := strings.ToLower(output + " " + err.Error())
		if strings.Contains(lower, "rate limit") || strings.Contains(lower, "abuse detection") || strings.Contains(lower, "http 429") {
			until := r.reset
			if strings.Contains(lower, "secondary") || strings.Contains(lower, "abuse") || !until.After(now) {
				until = now.Add(time.Hour)
			}
			if r.retryAt.After(until) {
				until = r.retryAt
			}
			r.state.PauseUntil = until
			if saveErr := r.save(); saveErr != nil {
				return output, errors.Join(ErrGitHubDeferred, err, saveErr)
			}
			return output, errors.Join(ErrGitHubDeferred, err)
		}
	}
	return output, err
}

// readHeaders consumes response headers internally; callers and access logs see
// only the ordinary gh body. Retry-After is honored even when it exceeds the
// conservative one-hour secondary-limit fallback.
func (r *GitHubBudgetRunner) readHeaders(output string) string {
	response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(output)), nil)
	if err != nil {
		return output
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return output
	}
	if response.Header.Get("X-RateLimit-Resource") == "core" {
		if remaining, err := strconv.Atoi(response.Header.Get("X-RateLimit-Remaining")); err == nil {
			r.remaining = remaining
		}
		if reset, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			r.reset = time.Unix(reset, 0)
		}
	}
	retry := response.Header.Get("Retry-After")
	if seconds, err := strconv.Atoi(retry); err == nil && seconds > 0 {
		r.retryAt = r.now().Add(time.Duration(seconds) * time.Second)
	} else if until, err := http.ParseTime(retry); err == nil {
		r.retryAt = until
	}
	return string(body)
}

func (r *GitHubBudgetRunner) observeSnapshot(output string, now time.Time) error {
	var v struct {
		Resources struct {
			Core struct {
				Limit, Remaining int
				Reset            int64
			}
		}
	}
	if err := json.Unmarshal([]byte(output), &v); err != nil || v.Resources.Core.Limit <= 0 {
		return fmt.Errorf("%w: invalid quota snapshot", ErrGitHubDeferred)
	}
	r.remaining = v.Resources.Core.Remaining
	r.reset = time.Unix(v.Resources.Core.Reset, 0)
	r.checked = now
	r.snapshotJSON = output
	return nil
}

func (r *GitHubBudgetRunner) snapshot() string {
	var snapshot map[string]any
	if json.Unmarshal([]byte(r.snapshotJSON), &snapshot) != nil {
		return r.snapshotJSON
	}
	resources, _ := snapshot["resources"].(map[string]any)
	for _, key := range []string{"core", "rate"} {
		if bucket, ok := resources[key].(map[string]any); ok {
			bucket["remaining"] = r.remaining
		}
	}
	data, _ := json.Marshal(snapshot)
	return string(data)
}

// BudgetStatus reports the background reservation ledger, not exact HTTP usage.
type BudgetStatus struct {
	Used          int
	Limit         int
	WindowEnd     time.Time
	PauseUntil    time.Time
	CoreRemaining int
	CoreReset     time.Time
}

// Status returns a consistent view of the request budget for scan summaries.
func (r *GitHubBudgetRunner) Status() BudgetStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := r.Limit
	if limit <= 0 || limit > 4000 {
		limit = 2000
	}
	return BudgetStatus{r.state.Used, limit, r.state.Window.Add(time.Hour), r.state.PauseUntil, r.remaining, r.reset}
}

func (r *GitHubBudgetRunner) save() error {
	if r.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(r.Path), ".github-budget-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), r.Path)
}

// LookPath delegates executable discovery.
func (r *GitHubBudgetRunner) LookPath(file string) (string, error) { return r.Base.LookPath(file) }

// RunWithStdin applies the same budget to commands with stdin.
func (r *GitHubBudgetRunner) RunWithStdin(ctx context.Context, stdin, dir, name string, args ...string) (string, error) {
	return r.run(ctx, dir, name, args, func() (string, error) {
		if base, ok := r.Base.(interface {
			RunWithStdin(context.Context, string, string, string, ...string) (string, error)
		}); ok {
			return base.RunWithStdin(ctx, stdin, dir, name, args...)
		}
		return "", errors.New("runner does not support stdin")
	})
}

// RunStreaming preserves streaming for provider processes and budgets gh calls.
func (r *GitHubBudgetRunner) RunStreaming(ctx context.Context, dir string, w io.Writer, name string, args ...string) (string, error) {
	return r.run(ctx, dir, name, args, func() (string, error) {
		if base, ok := r.Base.(StreamingRunner); ok {
			return base.RunStreaming(ctx, dir, w, name, args...)
		}
		output, err := r.Base.Run(ctx, dir, name, args...)
		if w != nil {
			_, _ = io.WriteString(w, output)
		}
		return output, err
	})
}
