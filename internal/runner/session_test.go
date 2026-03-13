package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/repo"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestRunIssueSessionSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"codex --version": "codex 0.114.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"): "baseline ok",
			issuePromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"):     "done",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
	data, err := os.ReadFile(store.SessionLogPath(7))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "session succeeded") || !strings.Contains(string(data), "done") {
		t.Fatalf("unexpected log: %s", string(data))
	}
}

func TestRunIssueSessionFailureCommentsOnIssue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"codex --version": "codex 0.114.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Blocked",
				Emoji:      "🧱",
				Percent:    25,
				ETAMinutes: 15,
				Items: blockedPreflightItems(
					state.BlockedReason{
						Kind:    "validation_failed",
						Summary: "baseline validation failed: go test ./... exit status 1",
						Detail:  "baseline validation failed: go test ./... exit status 1",
					},
					"codex",
					"",
					"vigilante resume --repo owner/repo --issue 7",
				),
				Tagline: "Strong foundations make calm debugging sessions.",
			}): "ok",
		},
		Errors: map[string]error{
			preflightPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"): errors.New("baseline validation failed: go test ./... exit status 1"),
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusBlocked {
		t.Fatalf("unexpected status: %#v", got)
	}
	if !strings.Contains(got.LastError, "go test ./...") {
		t.Fatalf("unexpected error: %#v", got)
	}
	data, err := os.ReadFile(store.SessionLogPath(7))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "issue preflight failed") || !strings.Contains(string(data), "go test ./...") {
		t.Fatalf("unexpected log: %s", string(data))
	}
}

func TestRunConflictResolutionSessionFailureCommentsOnIssue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"codex --version": "codex 0.114.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Blocked",
				Emoji:      "🧯",
				Percent:    90,
				ETAMinutes: 12,
				Items: []string{
					"Conflict resolution for PR #17 on `vigilante/issue-7` did not complete.",
					"Cause class: `provider_runtime_error`.",
					"Next step: fix the blocker, then run `vigilante resume --repo owner/repo --issue 7` or request resume from GitHub.",
				},
				Tagline: "An obstacle is often a stepping stone.",
			}): "ok",
		},
		Errors: map[string]error{
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-conflict-resolution` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nPull Request: #17\nPull Request URL: https://github.com/owner/repo/pull/17\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nBase branch: origin/main\nResolve the current rebase conflicts in the assigned worktree, use `gh issue comment` for progress and failures, rerun `go test ./...` after conflict resolution if the rebase succeeds, and push the updated branch when finished.\nKeep the changes minimal and focused on getting the PR back to a merge-ready state.": errors.New("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := RunConflictResolutionSession(
		context.Background(),
		env,
		store,
		state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
		state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, IssueTitle: "Demo", IssueURL: "https://github.com/owner/repo/issues/7", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7"},
		ghcli.PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17"},
	)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunIssueSessionSuccessWithClaudeProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"claude --version": "Claude Code 1.4.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Claude Code`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			testutil.Key("claude", "--print", "--permission-mode", "acceptEdits", skill.BuildIssuePreflightPrompt(
				state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "claude"},
			)): "baseline ok",
			testutil.Key("claude", "--print", "--permission-mode", "acceptEdits", skill.BuildIssuePromptForRuntime(
				skill.RuntimeClaude,
				state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "claude"},
			)): "done",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "claude", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestRunIssueSessionSuccessWithGeminiProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gemini --version": "gemini 1.7.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Gemini CLI`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			testutil.Key("gemini", "--prompt", skill.BuildIssuePreflightPrompt(
				state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "gemini"},
			), "--yolo"): "baseline ok",
			testutil.Key("gemini", "--prompt", skill.BuildIssuePromptForRuntime(
				skill.RuntimeGemini,
				state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "gemini"},
			), "--yolo"): "done",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "gemini", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestRunIssueSessionUsesMonorepoSkillWhenClassified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape: repo.ShapeMonorepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"pnpm-workspace.yaml"},
				MultiPackageRoots:    []string{"apps", "packages"},
			},
		},
	}
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"codex --version": "codex 0.114.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"): "baseline ok",
			testutil.Key("codex", "exec", "--cd", "/tmp/worktree", "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
				target,
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "codex"},
			)): "done",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}

	got := RunIssueSession(context.Background(), env, store, target, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)

	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestRunIssueSessionFailsWhenProviderVersionIsIncompatible(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"codex --version": "codex 2.0.0",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}

	got := RunIssueSession(context.Background(), env, store, state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)

	if got.Status != state.SessionStatusFailed {
		t.Fatalf("unexpected status: %#v", got)
	}
	if !strings.Contains(got.LastError, "codex CLI version 2.0.0 is incompatible") {
		t.Fatalf("unexpected error: %#v", got)
	}
}

func TestClassifyBlockedFailureDetectsProviderQuota(t *testing.T) {
	err := errors.New("exit status 1")
	output := "You've hit your usage limit. Upgrade to Pro or purchase more credits. Try again at 2026-03-13 09:00 PDT."

	got := classifyBlockedFailure("issue_execution", "codex exec", output, err)

	if got.Kind != "provider_quota" {
		t.Fatalf("expected provider_quota, got %#v", got)
	}
	for _, want := range []string{
		"usage or subscription limit",
		"Try again at 2026-03-13 09:00 PDT.",
		"upgrading the subscription",
		"purchasing more credits",
	} {
		if !strings.Contains(got.Summary, want) {
			t.Fatalf("expected summary to contain %q, got %q", want, got.Summary)
		}
	}
	if !strings.Contains(got.Detail, "You've hit your usage limit.") {
		t.Fatalf("expected detail to preserve provider output, got %q", got.Detail)
	}
}

func TestBlockedPreflightItemsIncludeSummarizedOutputForValidationFailures(t *testing.T) {
	items := blockedPreflightItems(
		state.BlockedReason{
			Kind:    "validation_failed",
			Summary: "baseline validation failed: go test ./... exit status 1",
			Detail:  "line one\nline two",
		},
		"codex",
		"--- FAIL: TestCLIWatch\nwatch command failed\nFAIL\tgithub.com/nicobistolfi/vigilante/internal/app\t0.421s",
		"vigilante resume --repo owner/repo --issue 7",
	)

	for _, want := range []string{
		"Repository baseline validation failed before issue implementation began.",
		"Cause class: `validation_failed`.",
		"Failed validation: baseline validation failed: go test ./... exit status 1.",
		"Relevant preflight output: --- FAIL: TestCLIWatch watch command failed FAIL github.com/nicobistolfi/vigilante/internal/app 0.421s.",
	} {
		if !containsLine(items, want) {
			t.Fatalf("expected items to contain %q, got %#v", want, items)
		}
	}
}

func TestBlockedPreflightItemsBoundLongOutput(t *testing.T) {
	output := strings.Repeat("noisy log line ", 40)

	items := blockedPreflightItems(
		state.BlockedReason{
			Kind:    "validation_failed",
			Summary: "baseline validation failed: npm test exit status 1",
		},
		"codex",
		output,
		"vigilante resume --repo owner/repo --issue 7",
	)

	var relevant string
	for _, item := range items {
		if strings.HasPrefix(item, "Relevant preflight output: ") {
			relevant = item
			break
		}
	}
	if relevant == "" {
		t.Fatalf("expected relevant preflight output item, got %#v", items)
	}
	if len(relevant) > 320 {
		t.Fatalf("expected bounded output item, got length %d: %q", len(relevant), relevant)
	}
	if !strings.Contains(relevant, "...") {
		t.Fatalf("expected truncated output marker, got %q", relevant)
	}
}

func TestBlockedPreflightItemsSkipOutputWhenEmpty(t *testing.T) {
	items := blockedPreflightItems(
		state.BlockedReason{
			Kind:    "validation_failed",
			Summary: "baseline validation failed: go test ./... exit status 1",
		},
		"codex",
		"",
		"vigilante resume --repo owner/repo --issue 7",
	)

	if !containsLine(items, "Failed validation: baseline validation failed: go test ./... exit status 1.") {
		t.Fatalf("expected failed validation item, got %#v", items)
	}
	for _, item := range items {
		if strings.HasPrefix(item, "Relevant preflight output: ") {
			t.Fatalf("did not expect output item for empty preflight output, got %#v", items)
		}
	}
}

func TestAppendSessionLogUsesLocalTimezone(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("TEST", -8*60*60)
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	path := filepath.Join(t.TempDir(), "issue-7.log")
	appendSessionLog(path, "session started", state.Session{
		IssueNumber:  7,
		Provider:     "codex",
		Branch:       "vigilante/issue-7",
		WorktreePath: "/tmp/worktree",
		Status:       state.SessionStatusRunning,
	}, "")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "provider=codex") {
		t.Fatalf("expected provider in session log entry, got %q", text)
	}
	if !strings.Contains(text, "-08:00] session started") {
		t.Fatalf("expected local timezone offset in session log entry, got %q", text)
	}
	if strings.Contains(text, "Z] session started") {
		t.Fatalf("expected local timezone log entry, got %q", text)
	}
}

func preflightPromptCommand(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePreflightPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		state.Session{WorktreePath: worktreePath, Branch: branch},
	))
}

func issuePromptCommand(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		state.Session{WorktreePath: worktreePath, Branch: branch, Provider: "codex"},
	))
}

func containsLine(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
