package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nicobistolfi/vigilante/internal/backend"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// countingRateLimiter records how many GitHub rate-limit calls the dashboard makes.
type countingRateLimiter struct {
	mu    sync.Mutex
	calls int
}

func (c *countingRateLimiter) GetRateLimitSnapshot(context.Context) (backend.RateLimitSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return backend.RateLimitSnapshot{
		Core: backend.RateLimitResource{Limit: 5000, Remaining: 4999, ResetAt: time.Now().Add(time.Hour)},
	}, nil
}

func (c *countingRateLimiter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// runStatusCmd expands a batched command and returns every message that is
// delivered promptly. Refresh tickers are timer-backed, so they never deliver
// within the budget and are therefore excluded.
func runStatusCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	leaves := []tea.Cmd{cmd}
	var msgs []tea.Msg
	for len(leaves) > 0 {
		next := leaves[0]
		leaves = leaves[1:]
		if next == nil {
			continue
		}
		result := make(chan tea.Msg, 1)
		go func() { result <- next() }()
		select {
		case msg := <-result:
			if batch, ok := msg.(tea.BatchMsg); ok {
				leaves = append(leaves, batch...)
				continue
			}
			if msg != nil {
				msgs = append(msgs, msg)
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	return msgs
}

func applyStatusMsgs(m statusModel, msgs []tea.Msg) statusModel {
	for _, msg := range msgs {
		updated, _ := m.Update(msg)
		m = updated.(statusModel)
	}
	return m
}

func loadedStatusModel() statusModel {
	return statusModel{
		service: statusServiceSnapshot{
			Info:   serviceStatusInfo{State: "running", Manager: "systemd", Service: "vigilante.service", FilePath: "/tmp/vigilante.service", Installed: true, Running: true, DaemonVersion: "1.2.3"},
			Loaded: true,
		},
		sessions: statusSessionsSnapshot{
			Repos: []watchedRepoStatus{{
				Target:      state.WatchTarget{Repo: "owner/repo", Path: "/tmp/repo", Branch: "main", Provider: "claude"},
				ActiveCount: 1,
			}},
			Groups: []sessionGroup{{
				Label:    "Actively working",
				Sessions: []state.Session{{Repo: "owner/repo", IssueNumber: 42, Status: state.SessionStatusRunning}},
			}},
			Count:  1,
			Loaded: true,
		},
		rateLimit: statusRateLimitSnapshot{
			Snapshot:  ghcli.RateLimitSnapshot{Core: ghcli.RateLimitResource{Limit: 5000, Remaining: 4000, ResetAt: time.Now().Add(30 * time.Minute)}},
			Available: true,
			Loaded:    true,
		},
		width:  100,
		height: 40,
	}
}

func TestStdoutIsTerminalRejectsNonFileWriters(t *testing.T) {
	app := New()
	app.stdout = &bytes.Buffer{}
	if app.stdoutIsTerminal() {
		t.Fatal("expected a buffer stdout to not be a terminal")
	}
}

func TestStdoutIsTerminalAcceptsCharacterDevice(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	app := New()
	app.stdout = devNull
	if !app.stdoutIsTerminal() {
		t.Fatal("expected a character device stdout to be treated as a terminal")
	}
}

func TestStdoutIsTerminalRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.txt")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })

	app := New()
	app.stdout = file
	if app.stdoutIsTerminal() {
		t.Fatal("expected a redirected file stdout to not be a terminal")
	}
}

func TestStatusUsesDashboardRouting(t *testing.T) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = devNull.Close() })

	for _, tc := range []struct {
		name  string
		out   interface{ Write([]byte) (int, error) }
		plain bool
		want  bool
	}{
		{name: "tty without plain uses dashboard", out: devNull, plain: false, want: true},
		{name: "tty with plain stays plain", out: devNull, plain: true, want: false},
		{name: "non-tty without plain stays plain", out: &bytes.Buffer{}, plain: false, want: false},
		{name: "non-tty with plain stays plain", out: &bytes.Buffer{}, plain: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := New()
			app.stdout = tc.out
			if got := app.statusUsesDashboard(tc.plain); got != tc.want {
				t.Fatalf("statusUsesDashboard(%v) = %v, want %v", tc.plain, got, tc.want)
			}
		})
	}
}

// newPlainStatusTestApp builds an app whose stdout is a buffer, so every
// status invocation takes the plain-text path.
func newPlainStatusTestApp(t *testing.T) (*App, *bytes.Buffer) {
	t.Helper()
	return newPlainStatusTestAppInHome(t, t.TempDir())
}

func newPlainStatusTestAppInHome(t *testing.T, home string) (*App, *bytes.Buffer) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	if err := os.MkdirAll(filepath.Join(home, ".vigilante"), 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	stdout := &bytes.Buffer{}
	app.stdout = stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Errors: map[string]error{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": errors.New("not installed"),
			"gh api /rate_limit": errors.New("rate limit unavailable"),
		},
	}
	return app, stdout
}

func TestStatusCommandOnNonTerminalEmitsNoANSI(t *testing.T) {
	app, stdout := newPlainStatusTestApp(t)

	if exitCode := app.Run(context.Background(), []string{"status"}); exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if strings.Contains(stdout.String(), "\x1b") {
		t.Fatalf("expected no ANSI escape sequences, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Watched repositories") {
		t.Fatalf("expected plain-text output, got %q", stdout.String())
	}
}

func TestStatusCommandPlainMatchesDefaultOnNonTerminal(t *testing.T) {
	home := t.TempDir()
	appDefault, defaultOut := newPlainStatusTestAppInHome(t, home)
	if exitCode := appDefault.Run(context.Background(), []string{"status"}); exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}

	appPlain, plainOut := newPlainStatusTestAppInHome(t, home)
	if exitCode := appPlain.Run(context.Background(), []string{"status", "--plain"}); exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}

	if defaultOut.String() != plainOut.String() {
		t.Fatalf("--plain output differs from default plain output:\n%q\n%q", defaultOut.String(), plainOut.String())
	}
}

func TestStatusCommandPlainWatchRefreshesInPlace(t *testing.T) {
	app, stdout := newPlainStatusTestApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if err := app.statusWithOptions(ctx, true, true); err != nil {
		t.Fatalf("plain watch returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "\033[H\033[2J") {
		t.Fatalf("expected the refresh clear sequence, got %q", stdout.String())
	}
}

func TestStatusCommandAcceptsWatchWithoutPlain(t *testing.T) {
	app, _ := newPlainStatusTestApp(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if exitCode := app.Run(ctx, []string{"status", "-w"}); exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
}

func TestStatusModelQuitKeys(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyCtrlC},
	} {
		t.Run(key.String(), func(t *testing.T) {
			updated, cmd := loadedStatusModel().Update(key)
			model := updated.(statusModel)
			if !model.quitting {
				t.Fatalf("expected %q to set the quitting state", key.String())
			}
			if cmd == nil {
				t.Fatalf("expected %q to return a quit command", key.String())
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatalf("expected %q to return tea.Quit", key.String())
			}
			if model.View() != "" {
				t.Fatalf("expected an empty view while quitting, got %q", model.View())
			}
		})
	}
}

func TestStatusModelRendersLoadedData(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("PDT", -7*60*60)
	t.Cleanup(func() { time.Local = originalLocal })

	model := loadedStatusModel()
	model.sessions.Repos[0].Target.LastScanAt = "2026-08-07T19:25:00Z"
	view := model.View()
	for _, want := range []string{"Service", "running", "owner/repo", "last scan 2026-08-07 12:25 PDT", "Actively working", "Issue #42", "GitHub rate limits", "4000/5000"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected view to contain %q, got:\n%s", want, view)
		}
	}
}

func TestStatusModelRefreshMessageUpdatesContent(t *testing.T) {
	model := statusModel{width: 100, height: 40}
	if strings.Contains(model.View(), "owner/repo") {
		t.Fatal("expected the initial view to have no session content")
	}

	updated, _ := model.Update(statusSessionsMsg(statusSessionsSnapshot{
		Repos: []watchedRepoStatus{{Target: state.WatchTarget{Repo: "owner/repo", Branch: "main"}}},
		Groups: []sessionGroup{{
			Label:    "Actively working",
			Sessions: []state.Session{{Repo: "owner/repo", IssueNumber: 7, Status: state.SessionStatusRunning}},
		}},
		Count: 1,
	}))
	view := updated.(statusModel).View()
	for _, want := range []string{"owner/repo", "Issue #7", "Sessions (1)"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected refreshed view to contain %q, got:\n%s", want, view)
		}
	}
}

func TestStatusModelEmptyState(t *testing.T) {
	model := statusModel{width: 80, height: 30}
	model.sessions = statusSessionsSnapshot{Loaded: true}
	model.service = statusServiceSnapshot{Info: serviceStatusInfo{State: "not-installed", Manager: "systemd"}, Loaded: true}
	model.rateLimit = statusRateLimitSnapshot{Loaded: true}

	view := model.View()
	for _, want := range []string{"none configured", "no active sessions", "unavailable", "Sessions (0)"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected empty-state view to contain %q, got:\n%s", want, view)
		}
	}
}

func TestStatusModelToleratesTinyTerminals(t *testing.T) {
	for _, size := range []tea.WindowSizeMsg{{Width: 0, Height: 0}, {Width: 1, Height: 1}, {Width: 3, Height: 2}, {Width: 200, Height: 0}} {
		updated, _ := loadedStatusModel().Update(size)
		_ = updated.(statusModel).View()
	}
}

func TestStatusModelScrollClampsToContent(t *testing.T) {
	model := loadedStatusModel()

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if got := updated.(statusModel).offset; got != 0 {
		t.Fatalf("expected scrolling up at the top to stay at 0, got %d", got)
	}

	small := loadedStatusModel()
	small.height = 5
	updated, _ = small.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if got := updated.(statusModel).offset; got != 1 {
		t.Fatalf("expected scrolling down to advance the offset, got %d", got)
	}

	updated, _ = small.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	end := updated.(statusModel)
	if end.offset <= 0 || end.offset > len(end.contentLines()) {
		t.Fatalf("expected the end key to clamp inside the content, got %d", end.offset)
	}
}

func TestStatusDashboardQuitsOnKeyPress(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	if err := os.MkdirAll(filepath.Join(home, ".vigilante"), 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdin = strings.NewReader("q")
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{}
	app.rateLimiter = &countingRateLimiter{}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- app.statusDashboard(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dashboard returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dashboard did not exit after the quit key")
	}
}

func TestStatusDashboardDoesNotPollRateLimitEveryTick(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	if err := os.MkdirAll(filepath.Join(home, ".vigilante"), 0o755); err != nil {
		t.Fatal(err)
	}

	limiter := &countingRateLimiter{}
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{}
	app.rateLimiter = limiter

	model := app.newStatusModel(context.Background())
	if model.rateLimitEvery <= model.sessionsEvery {
		t.Fatalf("expected the rate-limit cadence (%s) to be slower than the session cadence (%s)", model.rateLimitEvery, model.sessionsEvery)
	}

	model = applyStatusMsgs(model, runStatusCmd(model.Init()))
	if limiter.count() != 1 {
		t.Fatalf("expected one rate-limit call after init, got %d", limiter.count())
	}

	const ticks = 10
	for range ticks {
		_, cmd := model.Update(statusSessionsTickMsg(time.Now()))
		model = applyStatusMsgs(model, runStatusCmd(cmd))
	}
	if limiter.count() != 1 {
		t.Fatalf("expected %d session ticks to make no additional rate-limit calls, got %d calls", ticks, limiter.count())
	}
	if !model.sessions.Loaded {
		t.Fatal("expected session ticks to refresh the local session data")
	}
}
