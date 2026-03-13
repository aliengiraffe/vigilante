package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestRunDaemonCommandUsesDefaultScanInterval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}

	if err := app.runDaemonCommand(context.Background(), []string{"run", "--once"}); err != nil {
		t.Fatal(err)
	}

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "daemon run start once=true interval=1m0s") {
		t.Fatalf("unexpected daemon log: %s", logData)
	}
}

func TestRunDaemonCommandKeepsIntervalOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}

	if err := app.runDaemonCommand(context.Background(), []string{"run", "--once", "--interval", "30s"}); err != nil {
		t.Fatal(err)
	}

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "daemon run start once=true interval=30s") {
		t.Fatalf("unexpected daemon log: %s", logData)
	}
}

func TestSetupCreatesStateLayoutAndSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh auth status": "ok",
		},
	}

	if err := app.Setup(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(app.state.Root(), "watchlist.json"),
		filepath.Join(app.state.Root(), "sessions.json"),
		filepath.Join(app.state.Root(), "logs"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteConflictResolution, "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestSetupWithGeminiCreatesGeminiSkillAssets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("GEMINI_HOME", filepath.Join(home, ".gemini"))

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "gemini": "/usr/bin/gemini"},
		Outputs: map[string]string{
			"gh auth status": "ok",
		},
	}

	if err := app.SetupWithProvider(context.Background(), false, "gemini"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(app.state.GeminiHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.GeminiHome(), "commands", skill.VigilanteIssueImplementation+".toml"),
		filepath.Join(app.state.GeminiHome(), "skills", skill.VigilanteConflictResolution, "SKILL.md"),
		filepath.Join(app.state.GeminiHome(), "commands", skill.VigilanteConflictResolution+".toml"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestWatchListAndUnwatch(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                         "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
		},
	}

	if err := app.Watch(context.Background(), repoPath, false, []string{"to-do", "good first issue"}, "", 0); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := app.List(false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "\"repo\": \"nicobistolfi/vigilante\"") {
		t.Fatalf("unexpected list output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "\"labels\": [") || !strings.Contains(stdout.String(), "\"to-do\"") {
		t.Fatalf("expected labels in list output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "\"assignee\": \"me\"") {
		t.Fatalf("expected default assignee in list output: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "\"max_parallel_sessions\": 3") {
		t.Fatalf("expected default max_parallel_sessions in list output: %s", stdout.String())
	}

	if err := app.Unwatch(repoPath); err != nil {
		t.Fatal(err)
	}
}

func TestWatchUpdatesExistingTarget(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "darwin"
	launchAgentPath := filepath.Join(home, "Library", "LaunchAgents", "com.vigilante.agent.plist")
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh auth status":                    "ok",
			`/bin/zsh -lic printf "%s" "$PATH"`: "/usr/bin:/bin:/Users/test/.local/bin",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" command -v 'git'`:   "/usr/bin/git\n",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" command -v 'gh'`:    "/usr/bin/gh\n",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" command -v 'codex'`: "/Users/test/.local/bin/codex\n",
			testutil.Key("launchctl", "unload", launchAgentPath):                         "",
			testutil.Key("launchctl", "load", launchAgentPath):                           "",
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                    "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                           "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"):   "origin/main\n",
		},
	}

	if err := app.Watch(context.Background(), repoPath, false, nil, "nicobistolfi", 3); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := app.Watch(context.Background(), repoPath, true, []string{"vibe-code", "vibe-code"}, "", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "updated "+repoPath) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}

	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if !targets[0].DaemonEnabled {
		t.Fatalf("expected daemon_enabled to be updated: %#v", targets[0])
	}
	if len(targets[0].Labels) != 1 || targets[0].Labels[0] != "vibe-code" {
		t.Fatalf("expected labels to be updated: %#v", targets[0])
	}
	if targets[0].Assignee != "nicobistolfi" {
		t.Fatalf("expected assignee to be preserved: %#v", targets[0])
	}
	if targets[0].MaxParallel != 3 {
		t.Fatalf("expected max_parallel_sessions to be preserved: %#v", targets[0])
	}
}

func TestWatchWithProviderPersistsClaudeSelection(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                         "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
		},
	}

	if err := app.WatchWithProvider(context.Background(), repoPath, false, nil, "", 0, "claude"); err != nil {
		t.Fatal(err)
	}

	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Provider != "claude" {
		t.Fatalf("expected claude provider to persist: %#v", targets)
	}
}

func TestWatchWithGeminiProviderPersistsSelection(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                         "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
		},
	}

	if err := app.WatchWithProvider(context.Background(), repoPath, false, nil, "", 0, "gemini"); err != nil {
		t.Fatal(err)
	}

	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Provider != "gemini" {
		t.Fatalf("expected gemini provider to persist: %#v", targets)
	}
}

func TestListBlockedSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		Repo:                 "owner/repo",
		IssueNumber:          44,
		Status:               state.SessionStatusBlocked,
		BlockedAt:            "2026-03-11T13:20:13Z",
		BlockedStage:         "pr_maintenance",
		BlockedReason:        state.BlockedReason{Kind: "git_auth", Operation: "git fetch origin main"},
		ResumeHint:           "vigilante resume --repo owner/repo --issue 44",
		ResumeRequired:       true,
		RetryPolicy:          "paused",
		WorktreePath:         "/tmp/repo/.worktrees/vigilante/issue-44",
		Branch:               "vigilante/issue-44",
		LastMaintenanceError: "git fetch origin main: exit status 128",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.List(true, false); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	for _, want := range []string{
		"owner/repo issue #44  blocked_waiting_for_credentials",
		"cause: git_auth",
		"failed op: git fetch origin main",
		"resume: vigilante resume --repo owner/repo --issue 44",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected blocked list output to contain %q, got: %s", want, got)
		}
	}
}

func TestListBlockedSessionsShowsProviderQuotaSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		Repo:         "owner/repo",
		IssueNumber:  45,
		Status:       state.SessionStatusBlocked,
		BlockedAt:    "2026-03-11T13:20:13Z",
		BlockedStage: "issue_execution",
		BlockedReason: state.BlockedReason{
			Kind:      "provider_quota",
			Operation: "codex exec",
			Summary:   "Coding-agent account hit a usage or subscription limit. Try again at 2026-03-13 09:00 PDT.",
		},
		ResumeHint:     "vigilante resume --repo owner/repo --issue 45",
		ResumeRequired: true,
		RetryPolicy:    "paused",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.List(true, false); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	for _, want := range []string{
		"owner/repo issue #45  blocked_waiting_for_provider_quota",
		"cause: provider_quota",
		"summary: Coding-agent account hit a usage or subscription limit. Try again at 2026-03-13 09:00 PDT.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected blocked list output to contain %q, got: %s", want, got)
		}
	}
}

func TestClassifyBlockedReasonDetectsProviderQuota(t *testing.T) {
	err := errors.New("You've hit your usage limit. Upgrade to Pro or purchase more credits. Try again at 2026-03-13 09:00 PDT.")

	got := classifyBlockedReason("issue_execution", "codex exec", err)

	if got.Kind != "provider_quota" {
		t.Fatalf("expected provider_quota, got %#v", got)
	}
	if !strings.Contains(got.Summary, "Try again at 2026-03-13 09:00 PDT.") {
		t.Fatalf("expected retry hint in summary, got %q", got.Summary)
	}
}

func TestListRunningSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{
		{
			Repo:         "owner/repo",
			IssueNumber:  44,
			Status:       state.SessionStatusRunning,
			Branch:       "vigilante/issue-44",
			WorktreePath: "/tmp/repo/.worktrees/vigilante/issue-44",
			StartedAt:    "2026-03-11T13:20:13Z",
		},
		{
			Repo:        "owner/repo",
			IssueNumber: 45,
			Status:      state.SessionStatusBlocked,
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.List(false, true); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	for _, want := range []string{
		"owner/repo issue #44  running",
		"branch: vigilante/issue-44",
		"worktree: /tmp/repo/.worktrees/vigilante/issue-44",
		"started at: 2026-03-11T13:20:13Z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected running list output to contain %q, got: %s", want, got)
		}
	}
	if strings.Contains(got, "issue #45") {
		t.Fatalf("unexpected non-running session in output: %s", got)
	}
}

func TestCleanupSessionByIssue(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-44")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                                          "ok",
			"git worktree remove --force " + worktreePath:                 "ok",
			"git worktree list --porcelain":                               "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-44": "ok",
			"git branch -D vigilante/issue-44":                            "Deleted branch vigilante/issue-44\n",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		IssueNumber:  44,
		Status:       state.SessionStatusRunning,
		Branch:       "vigilante/issue-44",
		WorktreePath: worktreePath,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.CleanupSession(context.Background(), "owner/repo", 44, "cli"); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Status != state.SessionStatusFailed || sessions[0].CleanupCompletedAt == "" || sessions[0].LastCleanupSource != "cli" {
		t.Fatalf("expected cleaned session metadata, got: %#v", sessions[0])
	}
	if sessions[0].CleanupError != "" {
		t.Fatalf("unexpected cleanup error: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "cleaned up running session for owner/repo issue #44") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestCleanupRepoRunningSessions(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	for _, path := range []string{worktreePath1, worktreePath2} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                                         "ok",
			"git worktree remove --force " + worktreePath1:               "ok",
			"git worktree remove --force " + worktreePath2:               "ok",
			"git worktree list --porcelain":                              "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-2": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"git branch -D vigilante/issue-2":                            "Deleted branch vigilante/issue-2\n",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{
		{RepoPath: repoPath, Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning, Branch: "vigilante/issue-1", WorktreePath: worktreePath1},
		{RepoPath: repoPath, Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusRunning, Branch: "vigilante/issue-2", WorktreePath: worktreePath2},
		{RepoPath: repoPath, Repo: "owner/other", IssueNumber: 3, Status: state.SessionStatusRunning, Branch: "vigilante/issue-3", WorktreePath: filepath.Join(repoPath, ".worktrees", "vigilante", "issue-3")},
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.CleanupRepoRunningSessions(context.Background(), "owner/repo", "cli"); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusFailed || sessions[1].Status != state.SessionStatusFailed || sessions[2].Status != state.SessionStatusRunning {
		t.Fatalf("unexpected cleanup result: %#v", sessions)
	}
	if got := stdout.String(); !strings.Contains(got, "cleaned up 2 running session(s) in owner/repo") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestCleanupAllRunningSessions(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	for _, path := range []string{worktreePath1, worktreePath2} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                                         "ok",
			"git worktree remove --force " + worktreePath1:               "ok",
			"git worktree remove --force " + worktreePath2:               "ok",
			"git worktree list --porcelain":                              "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-2": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"git branch -D vigilante/issue-2":                            "Deleted branch vigilante/issue-2\n",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{
		{RepoPath: repoPath, Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning, Branch: "vigilante/issue-1", WorktreePath: worktreePath1},
		{RepoPath: repoPath, Repo: "owner/other", IssueNumber: 2, Status: state.SessionStatusRunning, Branch: "vigilante/issue-2", WorktreePath: worktreePath2},
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.CleanupAllRunningSessions(context.Background(), "cli"); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusFailed || sessions[1].Status != state.SessionStatusFailed {
		t.Fatalf("unexpected cleanup result: %#v", sessions)
	}
	if got := stdout.String(); !strings.Contains(got, "cleaned up 2 running session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceProcessesGitHubCommentResumeRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1":          `{"labels":[]}`,
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"@vigilanteai resume","created_at":"2026-03-10T12:30:00Z","user":{"login":"nicobistolfi"}}]`,
			"gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/101/reactions -f content=eyes": "{}",
			"codex --version": "codex 1.0.0",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): "done",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Recovered",
				Emoji:      "🫡",
				Percent:    92,
				ETAMinutes: 5,
				Items: []string{
					"The previous `provider_auth` block was cleared for `vigilante/issue-1`.",
					"Resume source: `comment`.",
					"Next step: Vigilante resumed `issue_execution` successfully.",
				},
				Tagline: "Back on the wire.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:        repoPath,
		Repo:            "owner/repo",
		IssueNumber:     1,
		IssueTitle:      "first",
		IssueURL:        "https://github.com/owner/repo/issues/1",
		Branch:          "vigilante/issue-1",
		WorktreePath:    worktreePath,
		Status:          state.SessionStatusBlocked,
		BlockedAt:       "2026-03-11T13:19:12Z",
		BlockedStage:    "issue_execution",
		BlockedReason:   state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired", Detail: "session expired"},
		RetryPolicy:     "paused",
		ResumeRequired:  true,
		ResumeHint:      "vigilante resume --repo owner/repo --issue 1",
		UpdatedAt:       "2026-03-11T13:19:12Z",
		LastHeartbeatAt: "2026-03-11T13:19:12Z",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected resumed session to be successful: %#v", sessions[0])
	}
	if sessions[0].LastResumeCommentID != 101 || sessions[0].LastResumeSource != "comment" {
		t.Fatalf("expected claimed comment metadata to be persisted: %#v", sessions[0])
	}
	if sessions[0].RecoveredAt == "" {
		t.Fatalf("expected recovery timestamp to be recorded: %#v", sessions[0])
	}
}

func TestScanOncePostsDiagnosticCommentWhenGitHubCommentResumeFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	resumeSummary := resumeFailureDiagnostic{
		Step:           "Resume could not rerun `codex exec` for `vigilante/issue-1`.",
		Why:            "Codex reported an expired session, so Vigilante could not continue the blocked work.",
		Classification: "provider_related",
		NextStep:       "Re-authenticate Codex locally, then retry `@vigilanteai resume`.",
	}
	expectedComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Resume Blocked",
		Emoji:      "🧱",
		Percent:    90,
		ETAMinutes: 10,
		Items: []string{
			resumeSummary.Step,
			resumeSummary.Why,
			"Failure type: `provider_related` (`provider_auth`). " + resumeSummary.NextStep,
		},
		Tagline: "No mystery errors left behind.",
	})

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1":          `{"labels":[]}`,
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"@vigilanteai resume","created_at":"2026-03-10T12:30:00Z","user":{"login":"nicobistolfi"}}]`,
			"gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/101/reactions -f content=eyes": "{}",
			"codex --version": "codex 1.0.0",
			resumeDiagnosticSummaryCommand(worktreePath, state.Session{
				Repo:             "owner/repo",
				IssueNumber:      1,
				IssueTitle:       "first",
				Branch:           "vigilante/issue-1",
				WorktreePath:     worktreePath,
				BlockedStage:     "issue_execution",
				BlockedReason:    state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired again", Detail: "session expired again"},
				ResumeHint:       "vigilante resume --repo owner/repo --issue 1",
				LastResumeSource: "comment",
				LastError:        "session expired again",
			}, "issue_execution"): `{"step":"Resume could not rerun ` + "`codex exec`" + ` for ` + "`vigilante/issue-1`" + `.","why":"Codex reported an expired session, so Vigilante could not continue the blocked work.","classification":"provider_related","next_step":"Re-authenticate Codex locally, then retry ` + "`@vigilanteai resume`" + `."}`,
			"gh issue comment --repo owner/repo 1 --body " + expectedComment: "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
		Errors: map[string]error{
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): errors.New("session expired again"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:        repoPath,
		Repo:            "owner/repo",
		IssueNumber:     1,
		IssueTitle:      "first",
		IssueURL:        "https://github.com/owner/repo/issues/1",
		Branch:          "vigilante/issue-1",
		WorktreePath:    worktreePath,
		Status:          state.SessionStatusBlocked,
		BlockedAt:       "2026-03-11T13:19:12Z",
		BlockedStage:    "issue_execution",
		BlockedReason:   state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired", Detail: "session expired"},
		RetryPolicy:     "paused",
		ResumeRequired:  true,
		ResumeHint:      "vigilante resume --repo owner/repo --issue 1",
		UpdatedAt:       "2026-03-11T13:19:12Z",
		LastHeartbeatAt: "2026-03-11T13:19:12Z",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Status != state.SessionStatusBlocked {
		t.Fatalf("expected blocked session after failed resume: %#v", sessions[0])
	}
	if sessions[0].LastResumeFailureFingerprint == "" || sessions[0].LastResumeFailureCommentedAt == "" {
		t.Fatalf("expected resume failure comment tracking: %#v", sessions[0])
	}
}

func TestResumeBlockedSessionFallsBackWhenDiagnosticSummaryFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	session := state.Session{
		RepoPath:       repoPath,
		Repo:           "owner/repo",
		Provider:       "codex",
		IssueNumber:    1,
		IssueTitle:     "first",
		IssueURL:       "https://github.com/owner/repo/issues/1",
		Branch:         "vigilante/issue-1",
		WorktreePath:   worktreePath,
		Status:         state.SessionStatusBlocked,
		BlockedStage:   "issue_execution",
		BlockedReason:  state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired", Detail: "session expired"},
		ResumeRequired: true,
		ResumeHint:     "vigilante resume --repo owner/repo --issue 1",
	}
	fallbackSession := session
	fallbackSession.LastResumeSource = "comment"
	fallbackSession.LastError = "session expired again"
	fallbackSession.BlockedReason = state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired again", Detail: "session expired again"}
	fallbackDiagnostic := deterministicResumeFailureDiagnostic(fallbackSession, "issue_execution")
	expectedComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Resume Blocked",
		Emoji:      "🧱",
		Percent:    90,
		ETAMinutes: 10,
		Items: []string{
			fallbackDiagnostic.Step,
			fallbackDiagnostic.Why,
			"Failure type: `provider_related` (`provider_auth`). " + fallbackDiagnostic.NextStep,
		},
		Tagline: "No mystery errors left behind.",
	})

	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			"gh issue comment --repo owner/repo 1 --body " + expectedComment: "ok",
		},
		Errors: map[string]error{
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): errors.New("session expired again"),
			resumeDiagnosticSummaryCommand(worktreePath, fallbackSession, "issue_execution"):                                                    errors.New("summary failed"),
		},
	}

	if err := app.resumeBlockedSession(context.Background(), &session, "comment"); err != nil {
		t.Fatal(err)
	}
	if session.Status != state.SessionStatusBlocked {
		t.Fatalf("expected session to remain blocked: %#v", session)
	}
	if session.LastResumeFailureFingerprint == "" || session.LastResumeFailureCommentedAt == "" {
		t.Fatalf("expected fallback comment metadata to be tracked: %#v", session)
	}
}

func TestResumeBlockedSessionUsesGeminiForDiagnosticSummary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	session := state.Session{
		RepoPath:       repoPath,
		Repo:           "owner/repo",
		Provider:       "gemini",
		IssueNumber:    1,
		IssueTitle:     "first",
		IssueURL:       "https://github.com/owner/repo/issues/1",
		Branch:         "vigilante/issue-1",
		WorktreePath:   worktreePath,
		Status:         state.SessionStatusBlocked,
		BlockedStage:   "issue_execution",
		BlockedReason:  state.BlockedReason{Kind: "provider_auth", Operation: "gemini --prompt", Summary: "session expired", Detail: "session expired"},
		ResumeRequired: true,
		ResumeHint:     "vigilante resume --repo owner/repo --issue 1",
	}
	failedSession := session
	failedSession.LastResumeSource = "comment"
	failedSession.LastError = "session expired again"
	failedSession.BlockedReason = state.BlockedReason{Kind: "provider_auth", Operation: "gemini --prompt", Summary: "session expired again", Detail: "session expired again"}
	expectedComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Resume Blocked",
		Emoji:      "🧱",
		Percent:    90,
		ETAMinutes: 10,
		Items: []string{
			"Resume could not rerun `gemini --prompt` for `vigilante/issue-1`.",
			"Gemini reported an expired session, so Vigilante could not continue the blocked work.",
			"Failure type: `provider_related` (`provider_auth`). Re-authenticate Gemini locally, then retry `@vigilanteai resume`.",
		},
		Tagline: "No mystery errors left behind.",
	})

	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"gemini": "/usr/bin/gemini"},
		Outputs: map[string]string{
			"gemini --version": "gemini 1.0.0",
			resumeDiagnosticSummaryCommandForProvider(worktreePath, "gemini", failedSession, "issue_execution"): `{"step":"Resume could not rerun ` + "`gemini --prompt`" + ` for ` + "`vigilante/issue-1`" + `.","why":"Gemini reported an expired session, so Vigilante could not continue the blocked work.","classification":"provider_related","next_step":"Re-authenticate Gemini locally, then retry ` + "`@vigilanteai resume`" + `."}`,
			"gh issue comment --repo owner/repo 1 --body " + expectedComment:                                    "ok",
		},
		Errors: map[string]error{
			issuePromptCommandForProvider("gemini", worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): errors.New("session expired again"),
		},
	}

	if err := app.resumeBlockedSession(context.Background(), &session, "comment"); err != nil {
		t.Fatal(err)
	}
	if session.Status != state.SessionStatusBlocked {
		t.Fatalf("expected session to remain blocked: %#v", session)
	}
	if session.LastResumeFailureFingerprint == "" || session.LastResumeFailureCommentedAt == "" {
		t.Fatalf("expected Gemini failure comment metadata to be tracked: %#v", session)
	}
}

func TestResumeBlockedSessionSuppressesDuplicateDiagnosticComment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	session := state.Session{
		RepoPath:       repoPath,
		Repo:           "owner/repo",
		Provider:       "codex",
		IssueNumber:    1,
		IssueTitle:     "first",
		IssueURL:       "https://github.com/owner/repo/issues/1",
		Branch:         "vigilante/issue-1",
		WorktreePath:   worktreePath,
		Status:         state.SessionStatusBlocked,
		BlockedStage:   "issue_execution",
		BlockedReason:  state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired", Detail: "session expired"},
		ResumeRequired: true,
		ResumeHint:     "vigilante resume --repo owner/repo --issue 1",
	}
	firstFailureSession := session
	firstFailureSession.LastResumeSource = "comment"
	firstFailureSession.LastError = "session expired again"
	firstFailureSession.BlockedReason = state.BlockedReason{Kind: "provider_auth", Operation: "codex exec", Summary: "session expired again", Detail: "session expired again"}
	diagnostic := resumeFailureDiagnostic{
		Step:           "Resume could not rerun `codex exec` for `vigilante/issue-1`.",
		Why:            "Codex reported an expired session, so Vigilante could not continue the blocked work.",
		Classification: "provider_related",
		NextStep:       "Re-authenticate Codex locally, then retry `@vigilanteai resume`.",
	}
	expectedComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Resume Blocked",
		Emoji:      "🧱",
		Percent:    90,
		ETAMinutes: 10,
		Items: []string{
			diagnostic.Step,
			diagnostic.Why,
			"Failure type: `provider_related` (`provider_auth`). " + diagnostic.NextStep,
		},
		Tagline: "No mystery errors left behind.",
	})
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			resumeDiagnosticSummaryCommand(worktreePath, firstFailureSession, "issue_execution"): `{"step":"Resume could not rerun ` + "`codex exec`" + ` for ` + "`vigilante/issue-1`" + `.","why":"Codex reported an expired session, so Vigilante could not continue the blocked work.","classification":"provider_related","next_step":"Re-authenticate Codex locally, then retry ` + "`@vigilanteai resume`" + `."}`,
			"gh issue comment --repo owner/repo 1 --body " + expectedComment:                     "ok",
		},
		Errors: map[string]error{
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): errors.New("session expired again"),
		},
	}

	if err := app.resumeBlockedSession(context.Background(), &session, "comment"); err != nil {
		t.Fatal(err)
	}
	firstCommentedAt := session.LastResumeFailureCommentedAt
	now = now.Add(5 * time.Minute)
	if err := app.resumeBlockedSession(context.Background(), &session, "comment"); err != nil {
		t.Fatal(err)
	}
	if session.LastResumeFailureCommentedAt != firstCommentedAt {
		t.Fatalf("expected duplicate resume failure comment to be suppressed: %#v", session)
	}
}

func TestScanOnceProcessesGitHubCommentCleanupRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"@vigilanteai cleanup","created_at":"2026-03-10T12:30:00Z","user":{"login":"nicobistolfi"}}]`,
			"gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/101/reactions -f content=+1": "{}",
			"git worktree prune":                                         "ok",
			"git worktree remove --force " + worktreePath:                "ok",
			"git worktree list --porcelain":                              "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Cleanup Completed",
				Emoji:      "🧹",
				Percent:    100,
				ETAMinutes: 1,
				Items: []string{
					"Removed the running Vigilante session for `vigilante/issue-1`.",
					"Cleanup source: `comment`.",
					"Local worktree artifacts were cleaned up at `" + worktreePath + "` when present.",
				},
				Tagline: "Leave no loose ends.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		IssueNumber:  1,
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusRunning,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Status != state.SessionStatusFailed || sessions[0].CleanupCompletedAt == "" {
		t.Fatalf("expected cleanup to remove running session: %#v", sessions[0])
	}
	if sessions[0].LastCleanupSource != "comment" || sessions[0].LastCleanupCommentID != 101 {
		t.Fatalf("expected cleanup comment metadata to be recorded: %#v", sessions[0])
	}
}

func TestScanOnceReportsNoMatchingRunningSessionForGitHubCleanupRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"@vigilanteai cleanup","created_at":"2026-03-10T12:30:00Z","user":{"login":"nicobistolfi"}}]`,
			"gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/101/reactions -f content=+1": "{}",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Cleanup Checked",
				Emoji:      "🧭",
				Percent:    100,
				ETAMinutes: 1,
				Items: []string{
					"Received `@vigilanteai cleanup` for this issue.",
					"No running Vigilante session matched the request, so there was nothing active to clean up.",
					"Next step: run `vigilante list --running` locally if dispatch still looks blocked.",
				},
				Tagline: "Trust, but verify.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     "/tmp/repo",
		Repo:         "owner/repo",
		IssueNumber:  1,
		Branch:       "vigilante/issue-1",
		WorktreePath: filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
		Status:       state.SessionStatusBlocked,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusBlocked {
		t.Fatalf("expected non-running session to remain unchanged: %#v", sessions[0])
	}
	if sessions[0].LastCleanupCommentID != 101 || sessions[0].LastCleanupSource != "comment" {
		t.Fatalf("expected cleanup request to be recorded: %#v", sessions[0])
	}
}

func TestScanOnceSelectsEligibleIssueAndPersistsSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	worktreePath := filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1")
	branch := "vigilante/issue-1-first"
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh auth status":          "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"to-do"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch + " " + worktreePath + " main": "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `" + worktreePath + "`.",
					"Branch: `" + branch + "`.",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand(worktreePath, "owner/repo", "/tmp/repo", 1, "first", "https://github.com/owner/repo/issues/1", branch): "baseline ok",
			issuePromptCommand(worktreePath, "owner/repo", "/tmp/repo", 1, "first", "https://github.com/owner/repo/issues/1", branch):     "done",
		},
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:         errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main", Assignee: "me", Labels: []string{"to-do"}}}); err != nil {
		t.Fatal(err)
	}
	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()
	if got := app.stdout.(*bytes.Buffer).String(); !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath) || !strings.Contains(got, "scanned 1 watch target(s), started 1 issue session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestScanOnceUsesProviderLabelOverrideForSession(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	branch := "vigilante/issue-1-first"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"codex"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch + " " + worktreePath + " main":                                                         "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath, branch):                                                      "ok",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", branch): "done",
		},
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:         errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", Provider: "claude"}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Provider != "codex" {
		t.Fatalf("expected issue label override to persist codex provider: %#v", sessions[0])
	}
}

func TestScanOncePrintsNoEligibleIssues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main", Assignee: "me", Labels: []string{"to-do"}}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		Repo:            "owner/repo",
		IssueNumber:     1,
		Status:          state.SessionStatusRunning,
		ProcessID:       os.Getpid(),
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
		LastHeartbeatAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (1 open)") || !strings.Contains(got, "scanned 1 watch target(s), started 0 issue session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceMaintainedIssueDoesNotConsumeOnlyDispatchSlot(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	branch2 := "vigilante/issue-2-second"
	if err := os.MkdirAll(worktreePath1, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
			"git fetch origin main":  "ok",
			"git status --porcelain": "",
			"git rebase origin/main": "Successfully rebased and updated refs/heads/vigilante/issue-1.\n",
			"go test ./...":          "ok",
			"git push --force-with-lease origin HEAD:vigilante/issue-1": "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Validation Passed",
				Emoji:      "✅",
				Percent:    90,
				ETAMinutes: 5,
				Items: []string{
					"Rebased PR #31 onto the latest `origin/main`.",
					"Reran `go test ./...` after the rebase.",
					"Pushed the updated branch `vigilante/issue-1`.",
				},
				Tagline: "Success is where preparation and opportunity meet.",
			}): "ok",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"to-do"}]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[{"name":"to-do"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch2 + " " + worktreePath2 + " main": "ok",
			"gh issue comment --repo owner/repo 2 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `" + worktreePath2 + "`.",
					"Branch: `" + branch2 + "`.",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", branch2): "baseline ok",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", branch2):     "done",
		},
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch2:        errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-2": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", Labels: []string{"to-do"}, MaxParallel: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		IssueNumber:  1,
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath1,
		Status:       state.SessionStatusSuccess,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo started issue #2 in "+worktreePath2) || !strings.Contains(got, "scanned 1 watch target(s), started 1 issue session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].IssueNumber != 1 || sessions[0].PullRequestState != "OPEN" {
		t.Fatalf("expected issue #1 to stay under maintenance: %#v", sessions[0])
	}
	if sessions[1].IssueNumber != 2 || sessions[1].Status != state.SessionStatusSuccess {
		t.Fatalf("expected issue #2 to complete a new session: %#v", sessions[1])
	}
}

func TestScanOnceWithMaxParallelOnePreservesSerialBehavior(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " main":                                                                       "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath1, "vigilante/issue-1-first"):                                                          "ok",
			preflightPromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"): "baseline ok",
			issuePromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):     "done",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", MaxParallel: 1}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath1) || strings.Contains(got, "issue #2") {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].IssueNumber != 1 || sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestScanOnceStartsMultipleIssuesUpToConfiguredLimit(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]},{"number":3,"title":"third","createdAt":"2026-03-11T12:00:00Z","url":"https://github.com/owner/repo/issues/3","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " main":                                                                         "ok",
			"git worktree add -b vigilante/issue-2-second " + worktreePath2 + " main":                                                                        "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath1, "vigilante/issue-1-first"):                                                            "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, "vigilante/issue-2-second"):                                                           "ok",
			preflightPromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):   "baseline ok",
			preflightPromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"): "baseline ok",
			issuePromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):       "done",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"):     "done",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", MaxParallel: 2}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath1) || !strings.Contains(got, "repo: owner/repo started issue #2 in "+worktreePath2) || strings.Contains(got, "issue #3") {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	for _, session := range sessions {
		if session.Status != state.SessionStatusSuccess {
			t.Fatalf("expected successful sessions: %#v", sessions)
		}
	}
}

func TestScanOnceDoesNotExceedConfiguredLimit(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]},{"number":3,"title":"third","createdAt":"2026-03-11T12:00:00Z","url":"https://github.com/owner/repo/issues/3","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-2-second " + worktreePath2 + " main":                                                                    "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, "vigilante/issue-2-second"):                                                       "ok",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"): "done",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", MaxParallel: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:        repoPath,
		Repo:            "owner/repo",
		IssueNumber:     1,
		Branch:          "vigilante/issue-1",
		WorktreePath:    filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1"),
		Status:          state.SessionStatusRunning,
		ProcessID:       os.Getpid(),
		StartedAt:       time.Now().UTC().Format(time.RFC3339),
		LastHeartbeatAt: time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo started issue #2 in "+worktreePath2) || strings.Contains(got, "issue #3") {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestScanOnceBlocksFailedIssueDispatchAndContinuesToNextIssue(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	branch2 := "vigilante/issue-2-second"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch2 + " " + worktreePath2 + " main":                                                              "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, branch2):                                                           "ok",
			preflightPromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", branch2): "baseline ok",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", branch2):     "done",
		},
		Errors: map[string]error{
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " main": errors.New("exit status 1: worktree add failed"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", MaxParallel: 2}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo blocked issue #1: exit status 1: worktree add failed") {
		t.Fatalf("expected blocked issue output, got: %s", got)
	}
	if !strings.Contains(got, "repo: owner/repo started issue #2 in "+worktreePath2) {
		t.Fatalf("expected second issue to start, got: %s", got)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].IssueNumber != 1 || sessions[0].Status != state.SessionStatusBlocked {
		t.Fatalf("expected first issue to be blocked, got: %#v", sessions[0])
	}
	if sessions[1].IssueNumber != 2 || sessions[1].Status != state.SessionStatusSuccess {
		t.Fatalf("expected second issue to succeed, got: %#v", sessions[1])
	}
}

func TestScanOnceEnforcesLimitsIndependentlyAcrossRepositories(t *testing.T) {
	home := t.TempDir()
	repoPathA := filepath.Join(home, "repo-a")
	repoPathB := filepath.Join(home, "repo-b")
	worktreeA1 := filepath.Join(repoPathA, ".worktrees", "vigilante", "issue-1")
	worktreeA2 := filepath.Join(repoPathA, ".worktrees", "vigilante", "issue-2")
	worktreeB10 := filepath.Join(repoPathB, ".worktrees", "vigilante", "issue-10")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo-a --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first-a","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo-a/issues/1","labels":[]},{"number":2,"title":"second-a","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo-a/issues/2","labels":[]}]`,
			"gh issue list --repo owner/repo-b --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":10,"title":"first-b","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo-b/issues/10","labels":[]},{"number":11,"title":"second-b","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo-b/issues/11","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first-a " + worktreeA1 + " main":                                                                                  "ok",
			"git worktree add -b vigilante/issue-2-second-a " + worktreeA2 + " main":                                                                                 "ok",
			"git worktree add -b vigilante/issue-10-first-b " + worktreeB10 + " main":                                                                                "ok",
			sessionStartCommentCommand("owner/repo-a", 1, worktreeA1, "vigilante/issue-1-first-a"):                                                                   "ok",
			sessionStartCommentCommand("owner/repo-a", 2, worktreeA2, "vigilante/issue-2-second-a"):                                                                  "ok",
			sessionStartCommentCommand("owner/repo-b", 10, worktreeB10, "vigilante/issue-10-first-b"):                                                                "ok",
			preflightPromptCommand(worktreeA1, "owner/repo-a", repoPathA, 1, "first-a", "https://github.com/owner/repo-a/issues/1", "vigilante/issue-1-first-a"):     "baseline ok",
			preflightPromptCommand(worktreeA2, "owner/repo-a", repoPathA, 2, "second-a", "https://github.com/owner/repo-a/issues/2", "vigilante/issue-2-second-a"):   "baseline ok",
			preflightPromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", "vigilante/issue-10-first-b"): "baseline ok",
			issuePromptCommand(worktreeA1, "owner/repo-a", repoPathA, 1, "first-a", "https://github.com/owner/repo-a/issues/1", "vigilante/issue-1-first-a"):         "done",
			issuePromptCommand(worktreeA2, "owner/repo-a", repoPathA, 2, "second-a", "https://github.com/owner/repo-a/issues/2", "vigilante/issue-2-second-a"):       "done",
			issuePromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", "vigilante/issue-10-first-b"):     "done",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{
		{Path: repoPathA, Repo: "owner/repo-a", Branch: "main", Assignee: "me", MaxParallel: 2},
		{Path: repoPathB, Repo: "owner/repo-b", Branch: "main", Assignee: "me", MaxParallel: 1},
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo-a started issue #1 in "+worktreeA1) || !strings.Contains(got, "repo: owner/repo-a started issue #2 in "+worktreeA2) || !strings.Contains(got, "repo: owner/repo-b started issue #10 in "+worktreeB10) || strings.Contains(got, "issue #11") {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestScanOnceContinuesWhenOneRepositoryScanFails(t *testing.T) {
	home := t.TempDir()
	repoPathA := filepath.Join(home, "repo-a")
	repoPathB := filepath.Join(home, "repo-b")
	worktreeB10 := filepath.Join(repoPathB, ".worktrees", "vigilante", "issue-10")
	branchB10 := "vigilante/issue-10-first-b"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo-b --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":10,"title":"first-b","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo-b/issues/10","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branchB10 + " " + worktreeB10 + " main":                                                                      "ok",
			sessionStartCommentCommand("owner/repo-b", 10, worktreeB10, branchB10):                                                                "ok",
			preflightPromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", branchB10): "baseline ok",
			issuePromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", branchB10):     "done",
		},
		Errors: map[string]error{
			"gh issue list --repo owner/repo-a --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": errors.New("gh auth status failed"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{
		{Path: repoPathA, Repo: "owner/repo-a", Branch: "main", Assignee: "me", MaxParallel: 1},
		{Path: repoPathB, Repo: "owner/repo-b", Branch: "main", Assignee: "me", MaxParallel: 1},
	}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo-a scan failed: gh auth status failed") {
		t.Fatalf("expected repo-a scan failure output, got: %s", got)
	}
	if !strings.Contains(got, "repo: owner/repo-b started issue #10 in "+worktreeB10) {
		t.Fatalf("expected repo-b issue to start, got: %s", got)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Repo != "owner/repo-b" || sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
}

func TestScanOnceCleansUpMergedIssueSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"MERGED","mergedAt":"2026-03-10T15:00:00Z"}]`,
			"git worktree prune":                                         "ok",
			"git worktree list --porcelain":                              "worktree /tmp/repo\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"gh api user --jq .login":                                    "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     "/tmp/repo",
		Repo:         "owner/repo",
		IssueNumber:  1,
		Branch:       "vigilante/issue-1",
		WorktreePath: filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
		Status:       state.SessionStatusSuccess,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].PullRequestNumber != 31 || sessions[0].PullRequestURL != "https://github.com/owner/repo/pull/31" {
		t.Fatalf("expected pull request to be tracked: %#v", sessions[0])
	}
	if sessions[0].PullRequestState != "MERGED" {
		t.Fatalf("expected merged pull request state to be tracked: %#v", sessions[0])
	}
	if sessions[0].PullRequestMergedAt != "2026-03-10T15:00:00Z" {
		t.Fatalf("expected merged time to be tracked: %#v", sessions[0])
	}
	if sessions[0].CleanupCompletedAt == "" {
		t.Fatalf("expected cleanup to complete: %#v", sessions[0])
	}
	if sessions[0].CleanupError != "" {
		t.Fatalf("unexpected cleanup error: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (0 open)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceMaintainsOpenPullRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
			"git fetch origin main":  "ok",
			"git status --porcelain": "",
			"git rebase origin/main": "Successfully rebased and updated refs/heads/vigilante/issue-1.\n",
			"go test ./...":          "ok",
			"git push --force-with-lease origin HEAD:vigilante/issue-1": "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Validation Passed",
				Emoji:      "✅",
				Percent:    90,
				ETAMinutes: 5,
				Items: []string{
					"Rebased PR #31 onto the latest `origin/main`.",
					"Reran `go test ./...` after the rebase.",
					"Pushed the updated branch `vigilante/issue-1`.",
				},
				Tagline: "Success is where preparation and opportunity meet.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     "/tmp/repo",
		Repo:         "owner/repo",
		IssueNumber:  1,
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Branch:       "vigilante/issue-1",
		WorktreePath: filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
		Status:       state.SessionStatusSuccess,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].PullRequestNumber != 31 || sessions[0].PullRequestState != "OPEN" {
		t.Fatalf("expected open pull request tracking: %#v", sessions[0])
	}
	if sessions[0].LastMaintainedAt == "" {
		t.Fatalf("expected maintenance timestamp: %#v", sessions[0])
	}
	if sessions[0].LastMaintenanceError != "" {
		t.Fatalf("unexpected maintenance error: %#v", sessions[0])
	}
}

func TestScanOnceSkipsWhenAnotherProcessHoldsScanLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	locked, err := app.state.TryWithScanLock(func() error {
		if err := app.ScanOnce(context.Background()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("expected outer lock to be acquired")
	}
	if got := stdout.String(); !strings.Contains(got, "scan skipped: another vigilante daemon is already scanning") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceUsesExplicitAssigneeFilter(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main", Assignee: "nicobistolfi"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (0 open)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceReportsRepoScanFailureWhenResolvingDefaultAssigneeFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Errors: map[string]error{
			"gh api user --jq .login": context.DeadlineExceeded,
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}

	err := app.ScanOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, `repo: owner/repo scan failed: resolve assignee "me": context deadline exceeded`) {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceRecoversStalledSessionAndRedispatchesIssue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	now := time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)
	app.clock = func() time.Time { return now }

	worktreePath := filepath.Join(home, "repo", ".worktrees", "vigilante", "issue-1")
	branch := "vigilante/issue-1-first"
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": "[]",
			"git worktree prune":                                         "ok",
			"git worktree list --porcelain":                              "worktree /tmp/repo\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Implementation In Progress",
				Emoji:      "🧹",
				Percent:    15,
				ETAMinutes: 20,
				Items: []string{
					"The previous local session on `vigilante/issue-1` stalled (worktree path is missing).",
					"The abandoned worktree state was cleaned up successfully.",
					"Next step: the issue is ready to be redispatched in a fresh worktree.",
				},
				Tagline: "A smooth sea never made a skilled sailor.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
			"git worktree add -b " + branch + " " + worktreePath + " main":                                                  "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `" + worktreePath + "`.",
					"Branch: `" + branch + "`.",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand(worktreePath, "owner/repo", filepath.Join(home, "repo"), 1, "first", "https://github.com/owner/repo/issues/1", branch): "baseline ok",
			issuePromptCommand(worktreePath, "owner/repo", filepath.Join(home, "repo"), 1, "first", "https://github.com/owner/repo/issues/1", branch):     "done",
		},
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:         errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: filepath.Join(home, "repo"), Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:        filepath.Join(home, "repo"),
		Repo:            "owner/repo",
		IssueNumber:     1,
		IssueTitle:      "first",
		IssueURL:        "https://github.com/owner/repo/issues/1",
		Branch:          "vigilante/issue-1",
		WorktreePath:    worktreePath,
		Status:          state.SessionStatusRunning,
		ProcessID:       999999,
		StartedAt:       now.Add(-20 * time.Minute).Format(time.RFC3339),
		LastHeartbeatAt: now.Add(-20 * time.Minute).Format(time.RFC3339),
		UpdatedAt:       now.Add(-20 * time.Minute).Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath) {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceRecoversStalledSessionIntoPRMaintenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	now := time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)
	app.clock = func() time.Time { return now }

	worktreePath := filepath.Join(home, "repo", ".worktrees", "vigilante", "issue-1")
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Implementation In Progress",
				Emoji:      "🔄",
				Percent:    70,
				ETAMinutes: 10,
				Items: []string{
					"The previous local session on `vigilante/issue-1` stalled (worktree path is missing).",
					"An existing PR #31 was found, so Vigilante recovered this issue into PR maintenance.",
					"Next step: keep the PR merge-ready instead of redispatching a new implementation session.",
				},
				Tagline: "Fall seven times, stand up eight.",
			}): "ok",
			"git fetch origin main":   "ok",
			"git status --porcelain":  "",
			"git rebase origin/main":  "Current branch vigilante/issue-1 is up to date.\n",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: filepath.Join(home, "repo"), Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:        filepath.Join(home, "repo"),
		Repo:            "owner/repo",
		IssueNumber:     1,
		IssueTitle:      "first",
		IssueURL:        "https://github.com/owner/repo/issues/1",
		Branch:          "vigilante/issue-1",
		WorktreePath:    worktreePath,
		Status:          state.SessionStatusRunning,
		ProcessID:       999999,
		StartedAt:       now.Add(-20 * time.Minute).Format(time.RFC3339),
		LastHeartbeatAt: now.Add(-20 * time.Minute).Format(time.RFC3339),
		UpdatedAt:       now.Add(-20 * time.Minute).Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected success session after recovery: %#v", sessions[0])
	}
	if sessions[0].PullRequestNumber != 31 || sessions[0].LastMaintainedAt == "" {
		t.Fatalf("expected PR maintenance tracking after recovery: %#v", sessions[0])
	}
}

func sessionStartCommentCommand(repo string, issueNumber int, worktreePath string, branch string) string {
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Vigilante Session Start",
		Emoji:      "🧢",
		Percent:    20,
		ETAMinutes: 25,
		Items: []string{
			"Vigilante launched this implementation session in `" + worktreePath + "`.",
			"Branch: `" + branch + "`.",
			"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
		},
		Tagline: "Make it simple, but significant.",
	})
}

func issuePromptCommand(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		state.Session{WorktreePath: worktreePath, Branch: branch, Provider: "codex"},
	))
}

func issuePromptCommandForProvider(providerID string, worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	switch providerID {
	case "gemini":
		return testutil.Key("gemini", "--prompt", skill.BuildIssuePromptForRuntime(
			skill.RuntimeGemini,
			state.WatchTarget{Path: repoPath, Repo: repo},
			ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
			state.Session{WorktreePath: worktreePath, Branch: branch, Provider: "gemini"},
		), "--yolo")
	default:
		return issuePromptCommand(worktreePath, repo, repoPath, issueNumber, title, issueURL, branch)
	}
}

func preflightPromptCommand(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePreflightPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		state.Session{WorktreePath: worktreePath, Branch: branch},
	))
}

func resumeDiagnosticSummaryCommand(worktreePath string, session state.Session, previousStage string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", buildResumeFailureSummaryPrompt(session, previousStage))
}

func resumeDiagnosticSummaryCommandForProvider(worktreePath string, providerID string, session state.Session, previousStage string) string {
	switch providerID {
	case "claude":
		return testutil.Key("claude", "--print", "--permission-mode", "acceptEdits", buildResumeFailureSummaryPrompt(session, previousStage))
	case "gemini":
		return testutil.Key("gemini", "--prompt", buildResumeFailureSummaryPrompt(session, previousStage), "--yolo")
	default:
		return resumeDiagnosticSummaryCommand(worktreePath, session, previousStage)
	}
}
