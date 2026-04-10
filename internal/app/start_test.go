package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// repoDiscoverOutputs returns the common git outputs needed for repo.Discover
// to succeed against a test repository path.
func repoDiscoverOutputs(repoPath string, repoSlug string, branch string) map[string]string {
	return map[string]string{
		"git rev-parse --is-inside-work-tree": "true",
		"git remote get-url origin":           "https://github.com/" + repoSlug + ".git",
		"git ls-remote --symref origin HEAD":  "ref: refs/heads/" + branch + "\tHEAD\nabcdef1234567890\tHEAD\n",
	}
}

func TestStartOneOffSessionSuccess(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-10")
	branch := "vigilante/issue-10-fix-something"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC) }
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(
			repoDiscoverOutputs(repoPath, "owner/repo", "main"),
			freshBaseBranchOutputs(repoPath, "main"),
			map[string]string{
				"git worktree prune":            "ok",
				"git worktree list --porcelain": "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
				"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
				"gh api repos/owner/repo/issues/10":                                   `{"title":"fix something","body":"fix it","html_url":"https://github.com/owner/repo/issues/10","state":"open","labels":[],"assignees":[]}`,
				"gh auth status":                                                      "Logged in",
				sessionStartCommentCommand("owner/repo", 10, worktreePath, state.Session{Branch: branch}):                                            "ok",
				preflightPromptCommand(worktreePath, "owner/repo", repoPath, 10, "fix something", "https://github.com/owner/repo/issues/10", branch): "baseline ok",
				issuePromptCommand(worktreePath, "owner/repo", repoPath, 10, "fix something", "https://github.com/owner/repo/issues/10", branch):     "done",
			},
		),
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:          errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-10": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	// Verify no watch targets exist before the call.
	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatal("expected no watch targets")
	}

	err = app.StartOneOffSession(context.Background(), repoPath, 10, "")
	// Without a tracked PR the session is incomplete, which StartOneOffSession reports as an error.
	if err == nil {
		t.Fatal("expected error for incomplete session without PR")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected incomplete error, got: %s", err)
	}

	// Verify session was saved.
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Status != state.SessionStatusIncomplete {
		t.Fatalf("expected incomplete, got %s", sessions[0].Status)
	}
	if sessions[0].Repo != "owner/repo" || sessions[0].IssueNumber != 10 {
		t.Fatalf("unexpected session: %#v", sessions[0])
	}

	// Verify no watch targets were added.
	targets, err = app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("expected no watch targets after start, got %d", len(targets))
	}

	output := stdout.String()
	if !strings.Contains(output, "one-off session") {
		t.Fatalf("expected one-off note in output, got: %s", output)
	}
	if !strings.Contains(output, "incomplete") {
		t.Fatalf("expected incomplete message, got: %s", output)
	}
}

func TestStartOneOffSessionDoesNotAddToWatchlist(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-5")
	branch := "vigilante/issue-5-test-issue"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC) }
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(
			repoDiscoverOutputs(repoPath, "owner/repo", "main"),
			freshBaseBranchOutputs(repoPath, "main"),
			map[string]string{
				"git worktree prune":            "ok",
				"git worktree list --porcelain": "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
				"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
				"gh api repos/owner/repo/issues/5":                                    `{"title":"test issue","body":"test","html_url":"https://github.com/owner/repo/issues/5","state":"open","labels":[],"assignees":[]}`,
				"gh auth status":                                                      "Logged in",
				sessionStartCommentCommand("owner/repo", 5, worktreePath, state.Session{Branch: branch}):                                        "ok",
				preflightPromptCommand(worktreePath, "owner/repo", repoPath, 5, "test issue", "https://github.com/owner/repo/issues/5", branch): "ok",
				issuePromptCommand(worktreePath, "owner/repo", repoPath, 5, "test issue", "https://github.com/owner/repo/issues/5", branch):     "done",
			},
		),
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:         errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-5": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	// Pre-populate a watch target for a different repo.
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{
		Path: "/other/repo", Repo: "other/repo", Branch: "main", Provider: "codex",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.StartOneOffSession(context.Background(), repoPath, 5, ""); err == nil {
		t.Fatal("expected error for incomplete session without PR")
	}

	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Repo != "other/repo" {
		t.Fatalf("watchlist was mutated: %#v", targets)
	}
}

func TestStartOneOffSessionInvalidRepoPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Errors: map[string]error{
			"git rev-parse --is-inside-work-tree": errors.New("not a git repo"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), filepath.Join(home, "nonexistent"), 1, "")
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("expected git repo error, got: %v", err)
	}
}

func TestStartOneOffSessionIssueNotOpen(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: mergeStringMaps(
			repoDiscoverOutputs(repoPath, "owner/repo", "main"),
			map[string]string{
				"gh api repos/owner/repo/issues/99": `{"title":"closed issue","body":"","html_url":"https://github.com/owner/repo/issues/99","state":"closed","labels":[],"assignees":[]}`,
			},
		),
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), repoPath, 99, "")
	if err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("expected not-open error, got: %v", err)
	}
}

func TestStartOneOffSessionIssueResolutionFailure(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: repoDiscoverOutputs(repoPath, "owner/repo", "main"),
		Errors: map[string]error{
			"gh api repos/owner/repo/issues/404": errors.New("HTTP 404"),
		},
		ErrorOutputs: map[string]string{
			"gh api repos/owner/repo/issues/404": "Not Found",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), repoPath, 404, "")
	if err == nil || !strings.Contains(err.Error(), "could not be resolved") {
		t.Fatalf("expected resolution failure, got: %v", err)
	}
}

func TestStartOneOffSessionReusesExistingSessionOrchestration(t *testing.T) {
	// This test verifies the start command goes through the same
	// issuerunner.RunIssueSession path as watched-repo sessions by
	// checking that session state transitions follow the same pattern.
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-7")
	branch := "vigilante/issue-7-blocked-test"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC) }

	// Set up runner where the preflight succeeds but the main invocation fails,
	// which should result in a blocked session (same as watched repo behavior).
	blockedComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Blocked",
		Emoji:      "🛑",
		Percent:    95,
		ETAMinutes: 10,
		Items: []string{
			fmt.Sprintf("The `codex` provider stopped before the issue implementation completed."),
			"Cause: unknown (no specific cause category detected).",
			fmt.Sprintf("Next step: fix the blocker, then run `vigilante resume --repo owner/repo --issue 7` or request resume from GitHub."),
		},
		Tagline: "Plans are only good intentions unless they immediately degenerate into hard work.",
	})
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(
			repoDiscoverOutputs(repoPath, "owner/repo", "main"),
			freshBaseBranchOutputs(repoPath, "main"),
			map[string]string{
				"git worktree prune":            "ok",
				"git worktree list --porcelain": "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
				"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
				"gh api repos/owner/repo/issues/7":                                    `{"title":"blocked test","body":"","html_url":"https://github.com/owner/repo/issues/7","state":"open","labels":[],"assignees":[]}`,
				"gh auth status":                                                      "Logged in",
				sessionStartCommentCommand("owner/repo", 7, worktreePath, state.Session{Branch: branch}):                                          "ok",
				preflightPromptCommand(worktreePath, "owner/repo", repoPath, 7, "blocked test", "https://github.com/owner/repo/issues/7", branch): "baseline ok",
				"gh issue comment --repo owner/repo 7 --body " + blockedComment:                                                                   "ok",
			},
		),
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:                                                                          errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-7":                                                                  errors.New("exit status 1"),
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 7, "blocked test", "https://github.com/owner/repo/issues/7", branch): errors.New("provider crashed"),
		},
		ErrorOutputs: map[string]string{
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 7, "blocked test", "https://github.com/owner/repo/issues/7", branch): "some error output",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), repoPath, 7, "")
	if err == nil {
		t.Fatal("expected error for blocked session")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked error, got: %v", err)
	}

	// Verify session was saved with blocked status.
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Status != state.SessionStatusBlocked {
		t.Fatalf("expected blocked, got %s", sessions[0].Status)
	}
}

func TestStartCommandParsing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing repo path",
			args:    []string{"--issue", "1"},
			wantErr: "usage:",
		},
		{
			name:    "missing issue flag",
			args:    []string{"/some/path"},
			wantErr: "usage:",
		},
		{
			name:    "zero issue number",
			args:    []string{"/some/path", "--issue", "0"},
			wantErr: "usage:",
		},
		{
			name:    "negative issue number",
			args:    []string{"/some/path", "--issue", "-1"},
			wantErr: "usage:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout.Reset()
			err := app.runStartCommand(context.Background(), tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestStartCommandHelp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	if err := app.runStartCommand(context.Background(), []string{"--help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "one-off") {
		t.Fatalf("expected help text with one-off, got: %s", stdout.String())
	}
}

func TestStartOneOffSessionWithCustomProvider(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-3")
	branch := "vigilante/issue-3-custom-provider"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_HOME", filepath.Join(home, ".claude"))

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return time.Date(2026, 4, 6, 12, 0, 0, 0, time.UTC) }

	claudeSession := state.Session{WorktreePath: worktreePath, Branch: branch, Provider: "claude"}
	claudeStartComment := sessionStartCommentForProvider("claude", "owner/repo", 3, worktreePath, claudeSession)
	claudePreflightCmd := claudePreflightCommand(worktreePath, "owner/repo", repoPath, 3, "custom provider", "https://github.com/owner/repo/issues/3", claudeSession)
	claudeIssueCmd := claudeIssueCommand(worktreePath, "owner/repo", repoPath, 3, "custom provider", "https://github.com/owner/repo/issues/3", claudeSession)

	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"claude": "/usr/bin/claude"},
		Outputs: mergeStringMaps(
			repoDiscoverOutputs(repoPath, "owner/repo", "main"),
			freshBaseBranchOutputs(repoPath, "main"),
			map[string]string{
				"git worktree prune":            "ok",
				"git worktree list --porcelain": "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
				"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
				"gh api repos/owner/repo/issues/3":                                    `{"title":"custom provider","body":"","html_url":"https://github.com/owner/repo/issues/3","state":"open","labels":[],"assignees":[]}`,
				"gh auth status":                                                      "Logged in",
				"claude --version":                                                    "claude 2.1.0",
				claudeStartComment:                                                    "ok",
				claudePreflightCmd:                                                    "baseline ok",
				claudeIssueCmd:                                                        "done",
			},
		),
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:         errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-3": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), repoPath, 3, "claude")
	if err == nil {
		t.Fatal("expected error for incomplete session without PR")
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Provider != "claude" {
		t.Fatalf("expected claude provider, got %s", sessions[0].Provider)
	}
}

func TestStartUsageAppearsInHelp(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	app.printUsage(&stdout)
	if !strings.Contains(stdout.String(), "vigilante start") {
		t.Fatalf("expected 'vigilante start' in usage, got: %s", stdout.String())
	}
}

func TestStartOneOffSessionRejectsInvalidProvider(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: repoDiscoverOutputs(repoPath, "owner/repo", "main"),
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.StartOneOffSession(context.Background(), repoPath, 1, "nonexistent-provider")
	if err == nil {
		t.Fatal("expected error for invalid provider")
	}
}

// sessionStartCommentForProvider builds the expected session start comment command
// for a specific provider.
func sessionStartCommentForProvider(providerID string, repoSlug string, issueNumber int, worktreePath string, session state.Session) string {
	displayName := "Codex"
	switch providerID {
	case "claude":
		displayName = "Claude Code"
	case "gemini":
		displayName = "Gemini CLI"
	}
	items := []string{
		"Vigilante launched this implementation session in `" + worktreePath + "`.",
		"Branch: `" + session.Branch + "`.",
		"Current stage: handing the issue off to the configured coding agent (`" + displayName + "`) for investigation and implementation.",
		"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
	}
	return "gh issue comment --repo " + repoSlug + " " + fmt.Sprintf("%d", issueNumber) + " --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Vigilante Session Start",
		Emoji:      "🧢",
		Percent:    20,
		ETAMinutes: 25,
		Items:      items,
		Tagline:    "Make it simple, but significant.",
	})
}

// claudePreflightCommand builds the expected claude preflight command.
func claudePreflightCommand(worktreePath string, repoSlug string, repoPath string, issueNumber int, title string, issueURL string, session state.Session) string {
	return testutil.Key("claude", "--print", "--dangerously-skip-permissions", skill.BuildIssuePreflightPrompt(
		state.WatchTarget{Path: repoPath, Repo: repoSlug},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		session,
	))
}

// claudeIssueCommand builds the expected claude issue command.
func claudeIssueCommand(worktreePath string, repoSlug string, repoPath string, issueNumber int, title string, issueURL string, session state.Session) string {
	return testutil.Key("claude", "--print", "--dangerously-skip-permissions", skill.BuildIssuePromptForRuntime(
		skill.RuntimeClaude,
		state.WatchTarget{Path: repoPath, Repo: repoSlug},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		session,
	))
}
