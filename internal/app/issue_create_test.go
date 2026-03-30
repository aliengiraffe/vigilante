package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestIssueCreateCommandHelp(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"issue", "--help"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	for _, want := range []string{
		"vigilante issue create",
		"--repo",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected help output to contain %q, got %q", want, stdout.String())
		}
	}
}

func TestIssueCreateSubcommandHelp(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--help"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	for _, want := range []string{
		"vigilante issue create --repo",
		"--provider",
		"vigilante-create-issue",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected help output to contain %q, got %q", want, stdout.String())
		}
	}
}

func TestIssueCreateCommandRejectsMissingRepo(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"issue", "create", "some prompt"})
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for missing --repo")
	}
}

func TestIssueCreateCommandRejectsMissingPrompt(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo"})
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for missing prompt")
	}
}

func TestIssueCreateCommandRejectsUnknownRepo(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets([]state.WatchTarget{}); err != nil {
		t.Fatal(err)
	}

	app := &App{
		stdout: testutil.IODiscard{},
		stderr: testutil.IODiscard{},
		clock:  func() time.Time { return time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC) },
		state:  store,
		env: &environment.Environment{
			OS:     "linux",
			Runner: testutil.FakeRunner{},
		},
	}

	var stderr bytes.Buffer
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo", "add dark mode"})
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for unknown repo")
	}
}

func TestIssueCreateCommandResolvesWatchTargetAndInvokesProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", home)
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	if err := store.SaveWatchTargets([]state.WatchTarget{
		{
			Path:     repoPath,
			Repo:     "owner/repo",
			Provider: "claude",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Create skill directory so the prompt builder can find it
	skillDir := repoPath
	os.MkdirAll(skillDir, 0o755)

	var capturedDir string
	var capturedName string
	var capturedArgs []string

	runner := testutil.FakeRunner{
		Outputs: map[string]string{},
		LookPaths: map[string]string{
			"git":    "/usr/bin/git",
			"gh":     "/usr/bin/gh",
			"claude": "/usr/bin/claude",
		},
	}

	env := &environment.Environment{
		OS:     "linux",
		Runner: runner,
	}

	app := &App{
		stdout: testutil.IODiscard{},
		stderr: testutil.IODiscard{},
		clock:  func() time.Time { return time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC) },
		state:  store,
		env:    env,
	}

	var stdout bytes.Buffer
	app.stdout = &stdout

	// Override the runner to capture the invocation
	captureRunner := &invocationCapture{
		base:      runner,
		onCapture: func(dir, name string, args []string) { capturedDir = dir; capturedName = name; capturedArgs = args },
	}
	app.env.Runner = captureRunner

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo", "add", "dark", "mode"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d; output: %s", exitCode, stdout.String())
	}

	if capturedName != "claude" {
		t.Fatalf("expected invocation name %q, got %q", "claude", capturedName)
	}
	if capturedDir != repoPath {
		t.Fatalf("expected invocation dir %q, got %q", repoPath, capturedDir)
	}

	promptArg := capturedArgs[len(capturedArgs)-1]
	if !strings.Contains(promptArg, "add dark mode") {
		t.Fatalf("expected prompt to contain user text, got: %s", promptArg)
	}
	if !strings.Contains(promptArg, "owner/repo") {
		t.Fatalf("expected prompt to contain repo slug, got: %s", promptArg)
	}
	if !strings.Contains(promptArg, "vigilante-create-issue") {
		t.Fatalf("expected prompt to reference create-issue skill, got: %s", promptArg)
	}
}

func TestIssueCreateCommandUsesLinearBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", home)
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets([]state.WatchTarget{
		{
			Path:         t.TempDir(),
			Repo:         "owner/repo",
			Provider:     "claude",
			IssueBackend: "linear",
		},
	}); err != nil {
		t.Fatal(err)
	}

	captureRunner := &invocationCapture{
		base: testutil.FakeRunner{
			LookPaths: map[string]string{
				"git":    "/usr/bin/git",
				"gh":     "/usr/bin/gh",
				"claude": "/usr/bin/claude",
			},
		},
	}

	app := &App{
		stdout: testutil.IODiscard{},
		stderr: testutil.IODiscard{},
		clock:  func() time.Time { return time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC) },
		state:  store,
		env: &environment.Environment{
			OS:     "linux",
			Runner: captureRunner,
		},
	}

	var stdout bytes.Buffer
	app.stdout = &stdout

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo", "fix login bug"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}

	if !strings.Contains(stdout.String(), "linear") {
		t.Fatalf("expected output to mention linear backend, got: %s", stdout.String())
	}
}

func TestIssueCreateCommandRejectsUnsupportedBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", home)
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets([]state.WatchTarget{
		{
			Path:         t.TempDir(),
			Repo:         "owner/repo",
			Provider:     "claude",
			IssueBackend: "jira",
		},
	}); err != nil {
		t.Fatal(err)
	}

	app := &App{
		stdout: testutil.IODiscard{},
		stderr: testutil.IODiscard{},
		clock:  func() time.Time { return time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC) },
		state:  store,
		env: &environment.Environment{
			OS:     "linux",
			Runner: testutil.FakeRunner{},
		},
	}

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo", "fix login bug"})
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for unsupported backend")
	}
}

func TestIssueCreateCommandRejectsMissingProviderTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", home)
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets([]state.WatchTarget{
		{
			Path:     t.TempDir(),
			Repo:     "owner/repo",
			Provider: "claude",
		},
	}); err != nil {
		t.Fatal(err)
	}

	app := &App{
		stdout: testutil.IODiscard{},
		stderr: testutil.IODiscard{},
		clock:  func() time.Time { return time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC) },
		state:  store,
		env: &environment.Environment{
			OS: "linux",
			Runner: testutil.FakeRunner{
				LookPaths: map[string]string{}, // no tools available
			},
		},
	}

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo", "fix login bug"})
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code when provider tool is not found")
	}
}

func TestIssueCreateCommandWithProviderOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", home)
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets([]state.WatchTarget{
		{
			Path:     t.TempDir(),
			Repo:     "owner/repo",
			Provider: "codex",
		},
	}); err != nil {
		t.Fatal(err)
	}

	captureRunner := &invocationCapture{
		base: testutil.FakeRunner{
			LookPaths: map[string]string{
				"git":    "/usr/bin/git",
				"gh":     "/usr/bin/gh",
				"claude": "/usr/bin/claude",
			},
		},
	}

	app := &App{
		stdout: testutil.IODiscard{},
		stderr: testutil.IODiscard{},
		clock:  func() time.Time { return time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC) },
		state:  store,
		env: &environment.Environment{
			OS:     "linux",
			Runner: captureRunner,
		},
	}

	exitCode := app.Run(context.Background(), []string{"issue", "create", "--repo", "owner/repo", "--provider", "claude", "fix bug"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}

	if captureRunner.capturedName != "claude" {
		t.Fatalf("expected provider override to use claude, got %q", captureRunner.capturedName)
	}
}

func TestIssueSubcommandUnknown(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"issue", "unknown"})
	if exitCode == 0 {
		t.Fatal("expected non-zero exit code for unknown issue subcommand")
	}
}

// invocationCapture is a test runner that captures the last command invocation.
type invocationCapture struct {
	base         testutil.FakeRunner
	onCapture    func(dir, name string, args []string)
	capturedDir  string
	capturedName string
	capturedArgs []string
}

func (r *invocationCapture) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	r.capturedDir = dir
	r.capturedName = name
	r.capturedArgs = append([]string(nil), args...)
	if r.onCapture != nil {
		r.onCapture(dir, name, r.capturedArgs)
	}
	out, err := r.base.Run(ctx, dir, name, args...)
	if err != nil {
		// For invocations we want to capture but not fail on, return empty output
		return "", nil
	}
	return out, nil
}

func (r *invocationCapture) LookPath(file string) (string, error) {
	return r.base.LookPath(file)
}
