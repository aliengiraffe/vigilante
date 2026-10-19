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

// DefaultGitHubBudgetLimit is the hourly reservation ceiling for each ledger.
const DefaultGitHubBudgetLimit = 2000

// MaxGitHubBudgetLimit bounds operator configuration for each ledger.
const MaxGitHubBudgetLimit = 4000

// githubBudgetReserve is the live quota kept free for interactive and provider work.
const githubBudgetReserve = 100

// GitHubBudgetRunner serializes background GitHub commands through shared,
// restart-safe hourly allowances. GitHub meters REST (`core`) and GraphQL
// separately, so the runner keeps one ledger per API: explicit `gh api` REST
// calls reserve one REST unit, GraphQL-backed CLI subcommands such as
// `gh issue list` and `gh pr view` reserve one GraphQL unit and no REST units,
// and opaque subcommands whose transport is unknown reserve one of each.
// A `304 Not Modified` answer refunds its REST reservation because GitHub does
// not count it. Commands marked OperationIntake are charged but never deferred
// by the local ledgers, so a large session backlog cannot starve new-issue
// discovery; the live-quota reserve still applies to them. Child provider
// processes are outside this runner; live probes account for their consumption.
type GitHubBudgetRunner struct {
	Base             Runner
	Path             string
	Limit            int
	GraphQLLimit     int
	Clock            func() time.Time
	AccessLog        func(AccessLogEntry)
	mu               sync.Mutex
	loaded           bool
	state            githubBudgetState
	remaining        int
	graphqlRemaining int
	graphqlKnown     bool
	reset            time.Time
	checked          time.Time
	snapshotJSON     string
	probeErr         error
	retryAt          time.Time
	lastStatus       int
	lastETag         string
	headersRemaining bool
}

type githubBudgetState struct {
	Window      time.Time `json:"window"`
	Used        int       `json:"used"`
	GraphQLUsed int       `json:"graphql_used,omitempty"`
	PauseUntil  time.Time `json:"pause_until"`
}

// GitHubCommandCost is the reservation a background gh command requires.
type GitHubCommandCost struct {
	REST    int
	GraphQL int
}

// graphQLSubcommands are gh CLI commands implemented on the GraphQL API. They
// consume `resources.graphql`, never `resources.core`.
var graphQLSubcommands = map[string]bool{
	"issue list": true, "issue view": true, "issue edit": true, "issue status": true, "issue close": true, "issue reopen": true,
	"pr list": true, "pr view": true, "pr edit": true, "pr status": true, "pr checks": true, "pr merge": true, "pr close": true, "pr reopen": true, "pr ready": true,
	"repo view": true, "search issues": true, "search prs": true, "search repos": true, "search commits": true, "search code": true,
}

// ClassifyGitHubCommand returns the reservation for a gh command line.
func ClassifyGitHubCommand(args []string) GitHubCommandCost {
	if len(args) == 0 {
		return GitHubCommandCost{REST: 1, GraphQL: 1}
	}
	if args[0] == "api" {
		for _, a := range args[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if strings.HasPrefix(strings.TrimPrefix(a, "/"), "graphql") {
				return GitHubCommandCost{GraphQL: 1}
			}
			break
		}
		return GitHubCommandCost{REST: 1}
	}
	if len(args) > 1 && graphQLSubcommands[args[0]+" "+args[1]] {
		return GitHubCommandCost{GraphQL: 1}
	}
	return GitHubCommandCost{REST: 1, GraphQL: 1}
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

func intakeOperation(ctx context.Context) bool {
	meta, _ := ctx.Value(accessLogContextKey{}).(AccessLogContext)
	return meta.Operation == OperationIntake
}

func normalizeBudgetLimit(limit int) int {
	if limit <= 0 || limit > MaxGitHubBudgetLimit {
		return DefaultGitHubBudgetLimit
	}
	return limit
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
		r.state.GraphQLUsed = 0
	}
	// Unbounded CLI pagination cannot be reserved safely. Background callers must
	// issue pages separately so every page passes through this guard.
	for _, a := range args {
		if a == "--paginate" {
			return "", fmt.Errorf("%w: use individually budgeted pages", ErrGitHubDeferred)
		}
	}
	cost := ClassifyGitHubCommand(args)
	intake := intakeOperation(ctx)
	if !intake {
		if r.state.Used+cost.REST > normalizeBudgetLimit(r.Limit) || r.state.GraphQLUsed+cost.GraphQL > normalizeBudgetLimit(r.GraphQLLimit) {
			return "", ErrGitHubDeferred
		}
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
	if cost.REST > 0 && r.remaining-cost.REST < githubBudgetReserve {
		return "", ErrGitHubDeferred
	}
	if cost.GraphQL > 0 && r.graphqlKnown && r.graphqlRemaining-cost.GraphQL < githubBudgetReserve {
		return "", ErrGitHubDeferred
	}
	r.state.Used += cost.REST
	r.state.GraphQLUsed += cost.GraphQL
	r.remaining -= cost.REST
	if r.graphqlKnown {
		r.graphqlRemaining -= cost.GraphQL
	}
	// Persist before dispatch: a crash must not refund already dispatched work.
	if err := r.save(); err != nil {
		return "", fmt.Errorf("save GitHub budget: %w", err)
	}
	r.retryAt = time.Time{}
	r.lastStatus, r.lastETag, r.headersRemaining = 0, "", false
	output, err := call()
	if sink := ResponseHeadersFromContext(ctx); sink != nil {
		sink.set(r.lastStatus, r.lastETag)
	}
	if r.lastStatus == http.StatusNotModified {
		// `gh api` exits non-zero for any status above 299, including 304, so a
		// conditional request that found nothing new looks like a failure to the
		// shell. It is a success here, and GitHub does not count it: refund the
		// REST reservation. Response headers already reflect the live quota, so
		// the estimate is only adjusted when no quota header arrived.
		err = nil
		if cost.REST > 0 {
			r.state.Used -= cost.REST
			if !r.headersRemaining {
				r.remaining += cost.REST
			}
			if saveErr := r.save(); saveErr != nil {
				return output, fmt.Errorf("save GitHub budget: %w", saveErr)
			}
		}
	}
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
// conservative one-hour secondary-limit fallback. The status code and ETag are
// retained for the response header sink of the current call.
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
	r.lastStatus = response.StatusCode
	r.lastETag = response.Header.Get("ETag")
	if response.Header.Get("X-RateLimit-Resource") == "core" {
		if remaining, err := strconv.Atoi(response.Header.Get("X-RateLimit-Remaining")); err == nil {
			r.remaining = remaining
			r.headersRemaining = true
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
			GraphQL struct {
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
	r.graphqlKnown = v.Resources.GraphQL.Limit > 0
	r.graphqlRemaining = v.Resources.GraphQL.Remaining
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
	if bucket, ok := resources["graphql"].(map[string]any); ok && r.graphqlKnown {
		bucket["remaining"] = r.graphqlRemaining
	}
	data, _ := json.Marshal(snapshot)
	return string(data)
}

// BudgetStatus reports the background reservation ledgers, not exact HTTP usage.
type BudgetStatus struct {
	Used             int
	Limit            int
	GraphQLUsed      int
	GraphQLLimit     int
	WindowEnd        time.Time
	PauseUntil       time.Time
	CoreRemaining    int
	GraphQLRemaining int
	CoreReset        time.Time
}

// Status returns a consistent view of the request budget for scan summaries.
func (r *GitHubBudgetRunner) Status() BudgetStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return BudgetStatus{
		Used:             r.state.Used,
		Limit:            normalizeBudgetLimit(r.Limit),
		GraphQLUsed:      r.state.GraphQLUsed,
		GraphQLLimit:     normalizeBudgetLimit(r.GraphQLLimit),
		WindowEnd:        r.state.Window.Add(time.Hour),
		PauseUntil:       r.state.PauseUntil,
		CoreRemaining:    r.remaining,
		GraphQLRemaining: r.graphqlRemaining,
		CoreReset:        r.reset,
	}
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
