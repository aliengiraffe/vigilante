package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	if err := app.Watch(context.Background(), repoPath, false, []string{"to-do", "good first issue"}, ""); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := app.List(); err != nil {
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

	if err := app.Watch(context.Background(), repoPath, false, nil, "nicobistolfi"); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := app.Watch(context.Background(), repoPath, true, []string{"vibe-code", "vibe-code"}, ""); err != nil {
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
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh auth status":          "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"to-do"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1 " + worktreePath + " main":                                                                                       "ok",
			"gh issue comment --repo owner/repo 1 --body Vigilante started a Codex session for this issue in `" + worktreePath + "` on branch `vigilante/issue-1`.": "ok",
			"codex exec --cd " + worktreePath + " --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #1 - first\nIssue URL: https://github.com/owner/repo/issues/1\nWorktree path: " + worktreePath + "\nBranch: vigilante/issue-1\nUse `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
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
	if err := app.state.SaveSessions([]state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning}}); err != nil {
		t.Fatal(err)
	}
	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (1 open)") || !strings.Contains(got, "scanned 1 watch target(s), started 0 issue session(s)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceSkipsRedispatchForMaintainedIssueAndStartsNextEligibleIssue(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
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
			"gh issue comment --repo owner/repo 1 --body Vigilante rebased PR #31 onto the latest `origin/main`, reran `go test ./...`, and pushed `vigilante/issue-1`.": "ok",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels":                                              `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"to-do"}]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[{"name":"to-do"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-2 " + worktreePath2 + " main":                                                                                       "ok",
			"gh issue comment --repo owner/repo 2 --body Vigilante started a Codex session for this issue in `" + worktreePath2 + "` on branch `vigilante/issue-2`.": "ok",
			"codex exec --cd " + worktreePath2 + " --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: " + repoPath + "\nIssue: #2 - second\nIssue URL: https://github.com/owner/repo/issues/2\nWorktree path: " + worktreePath2 + "\nBranch: vigilante/issue-2\nUse `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.\nUse the issue as the source of truth for the requested behavior and keep the implementation minimal.": "done",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", Labels: []string{"to-do"}}}); err != nil {
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
			"gh issue comment --repo owner/repo 1 --body Vigilante rebased PR #31 onto the latest `origin/main`, reran `go test ./...`, and pushed `vigilante/issue-1`.": "ok",
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

func TestScanOnceReturnsErrorWhenResolvingDefaultAssigneeFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
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
	if err == nil {
		t.Fatal("expected assignee resolution error")
	}
	if got := err.Error(); got != `resolve assignee "me": context deadline exceeded` {
		t.Fatalf("unexpected error: %s", got)
	}
}
