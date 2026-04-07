package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	githubbackend "github.com/nicobistolfi/vigilante/internal/backend/github"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/repo"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/telemetry"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestRunIssueSessionSuccess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	baseRunner := testutil.FakeRunner{
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"): "baseline ok",
			issuePromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"):     "done",
		},
	}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	env := &environment.Environment{
		OS: "darwin",
		Runner: environment.LoggingRunner{
			Base:      baseRunner,
			AccessLog: store.AppendAccessLogEntry,
		},
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	// No PR was tracked, so the session is incomplete rather than success.
	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status: %s (expected incomplete without PR)", got.Status)
	}
	if got.IncompleteReason == "" {
		t.Fatal("expected IncompleteReason to be set")
	}
	data, err := os.ReadFile(store.SessionLogPath("owner/repo", 7))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "session incomplete") || !strings.Contains(string(data), "done") {
		t.Fatalf("unexpected log: %s", string(data))
	}
	accessLog, err := os.ReadFile(store.AccessLogPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(accessLog)
	if !strings.Contains(text, `"context":"session"`) {
		t.Fatalf("expected session context in access log, got %s", text)
	}
	if !strings.Contains(text, `"repo":"owner/repo"`) || !strings.Contains(text, `"issue_number":7`) {
		t.Fatalf("expected repo and issue metadata in access log, got %s", text)
	}
}

func TestRunIssueSessionSuccessWithPR(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	baseRunner := testutil.FakeRunner{
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"): "baseline ok",
			issuePromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"):     "done",
		},
	}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	env := &environment.Environment{
		OS: "darwin",
		Runner: environment.LoggingRunner{
			Base:      baseRunner,
			AccessLog: store.AppendAccessLogEntry,
		},
	}
	// Session already has a tracked PR, so progress evaluation should classify it as success.
	session := state.Session{
		RepoPath:          "/tmp/repo",
		IssueNumber:       7,
		WorktreePath:      "/tmp/worktree",
		Branch:            "vigilante/issue-7",
		Status:            state.SessionStatusRunning,
		PullRequestNumber: 42,
	}
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %s (expected success with tracked PR)", got.Status)
	}
	if got.IncompleteReason != "" {
		t.Fatalf("expected empty IncompleteReason, got %q", got.IncompleteReason)
	}
}

func TestRunIssueSessionSuccessInSandboxUsesDockerExec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	baseRunner := testutil.FakeRunner{
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			testutil.Key("docker", "exec", "-w", "/workspace", "vigilante-sandbox-sbx_test", "codex", "exec", "--cd", "/workspace", "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePreflightPrompt(
				state.WatchTarget{Path: "/workspace", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/workspace", Branch: "vigilante/issue-7", Provider: "codex", SandboxMode: true, SandboxContainerName: "vigilante-sandbox-sbx_test"},
			)): "baseline ok",
			testutil.Key("docker", "exec", "-w", "/workspace", "vigilante-sandbox-sbx_test", "codex", "exec", "--cd", "/workspace", "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
				state.WatchTarget{Path: "/workspace", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/workspace", Branch: "vigilante/issue-7", Provider: "codex", SandboxMode: true, SandboxContainerName: "vigilante-sandbox-sbx_test"},
			)): "done",
		},
	}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	env := &environment.Environment{
		OS: "darwin",
		Runner: environment.LoggingRunner{
			Base:      baseRunner,
			AccessLog: store.AppendAccessLogEntry,
		},
	}
	session := state.Session{
		RepoPath:             "/tmp/repo",
		IssueNumber:          7,
		WorktreePath:         "/tmp/worktree",
		Branch:               "vigilante/issue-7",
		Status:               state.SessionStatusRunning,
		SandboxMode:          true,
		SandboxContainerName: "vigilante-sandbox-sbx_test",
	}
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
	data, err := os.ReadFile(store.SessionLogPath("owner/repo", 7))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "cmd=docker") {
		t.Fatalf("expected sandbox session log to record docker exec command, got %s", text)
	}
	if !strings.Contains(text, "arg[0]=exec") || !strings.Contains(text, "arg[4]=codex") {
		t.Fatalf("expected sandbox docker exec args in session log, got %s", text)
	}
	if !strings.Contains(text, "baseline ok") || !strings.Contains(text, "done") {
		t.Fatalf("expected sandbox command output in session log, got %s", text)
	}
}

func TestRunIssueSessionSandboxExit137IncludesOOMHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	baseRunner := testutil.FakeRunner{
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			testutil.Key("docker", "exec", "-w", "/workspace", "vigilante-sandbox-sbx_test", "codex", "exec", "--cd", "/workspace", "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePreflightPrompt(
				state.WatchTarget{Path: "/workspace", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/workspace", Branch: "vigilante/issue-7", Provider: "codex", SandboxMode: true, SandboxContainerName: "vigilante-sandbox-sbx_test"},
			)): "baseline ok",
		},
		Errors: map[string]error{
			testutil.Key("docker", "exec", "-w", "/workspace", "vigilante-sandbox-sbx_test", "codex", "exec", "--cd", "/workspace", "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
				state.WatchTarget{Path: "/workspace", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/workspace", Branch: "vigilante/issue-7", Provider: "codex", SandboxMode: true, SandboxContainerName: "vigilante-sandbox-sbx_test"},
			)): errors.New("docker [exec ...]: exit status 137"),
		},
	}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	env := &environment.Environment{OS: "darwin", Runner: baseRunner}
	session := state.Session{
		RepoPath:             "/tmp/repo",
		IssueNumber:          7,
		WorktreePath:         "/tmp/worktree",
		Branch:               "vigilante/issue-7",
		Status:               state.SessionStatusRunning,
		SandboxMode:          true,
		SandboxContainerName: "vigilante-sandbox-sbx_test",
	}
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusBlocked {
		t.Fatalf("expected blocked status, got %#v", got)
	}
	if !strings.Contains(got.LastError, "likely memory pressure/OOM") {
		t.Fatalf("expected OOM hint in last error, got %q", got.LastError)
	}
}

func TestRunIssueSessionStartCommentIncludesReusedRemoteBranchContext(t *testing.T) {
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
					"Vigilante launched this implementation session in `/tmp/worktree` from existing remote branch `origin/vigilante/issue-7-demo`.",
					"Diff summary against `main`: README.md | 2 ++",
					"Current stage: handing the issue off to the configured coding agent (`Codex`) to continue the existing implementation.",
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommandForSession("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7-demo", BaseBranch: "main", ReusedRemoteBranch: "vigilante/issue-7-demo"}):                                                       "baseline ok",
			issuePromptCommandForSession("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7-demo", BaseBranch: "main", ReusedRemoteBranch: "vigilante/issue-7-demo", BranchDiffSummary: "README.md | 2 ++", Provider: "codex"}): "done",
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	session := state.Session{
		RepoPath:           "/tmp/repo",
		IssueNumber:        7,
		WorktreePath:       "/tmp/worktree",
		Branch:             "vigilante/issue-7-demo",
		BaseBranch:         "main",
		ReusedRemoteBranch: "vigilante/issue-7-demo",
		BranchDiffSummary:  "README.md | 2 ++",
		Status:             state.SessionStatusRunning,
	}
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
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
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusBlocked {
		t.Fatalf("unexpected status: %#v", got)
	}
	if !strings.Contains(got.LastError, "go test ./...") {
		t.Fatalf("unexpected error: %#v", got)
	}
	data, err := os.ReadFile(store.SessionLogPath("owner/repo", 7))
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
			conflictResolutionPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, IssueTitle: "Demo", IssueBody: "Preserve the requested behavior.", IssueURL: "https://github.com/owner/repo/issues/7", BaseBranch: "main", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7"}, ghcli.PullRequest{Number: 17, Title: "Demo PR", Body: "PR body", URL: "https://github.com/owner/repo/pull/17", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}): errors.New("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
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
		githubbackend.NewBackend(&env.Runner),
		state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"},
		state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, IssueTitle: "Demo", IssueBody: "Preserve the requested behavior.", IssueURL: "https://github.com/owner/repo/issues/7", BaseBranch: "main", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7"},
		ghcli.PullRequest{Number: 17, Title: "Demo PR", Body: "PR body", URL: "https://github.com/owner/repo/pull/17", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"},
	)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunCIRemediationSessionFailureCommentsOnIssue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"codex --version": "codex 0.114.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "CI Remediation Blocked",
				Emoji:      "🧯",
				Percent:    92,
				ETAMinutes: 10,
				Items: []string{
					"CI remediation for PR #17 on `vigilante/issue-7` did not complete automatically.",
					"Cause class: `provider_runtime_error`.",
					"Next step: fix the blocker, then run `vigilante resume --repo owner/repo --issue 7` or request resume from GitHub.",
				},
				Tagline: "Stop the loop before it turns into noise.",
			}): "ok",
		},
		Errors: map[string]error{
			"codex exec --cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox Use the `vigilante-issue-implementation` skill for this task.\nRepository: owner/repo\nLocal repository path: /tmp/repo\nIssue: #7 - Demo\nIssue URL: https://github.com/owner/repo/issues/7\nPull Request: #17\nPull Request URL: https://github.com/owner/repo/pull/17\nWorktree path: /tmp/worktree\nBranch: vigilante/issue-7\nCI remediation context: GitHub reported failing required checks for this existing PR.\nFailing check: test (state=COMPLETED conclusion=FAILURE)\nInvestigate the failing CI checks, reproduce the problem locally when practical, and make the minimal code or configuration fix needed to get the PR green again.\nAny commit, amend, rebase rewrite, or conflict-resolution commit must preserve the user's existing git author, committer, and signing configuration. Commit on behalf of the user and do not overwrite `git config` with a coding-agent identity.\nDo not add `Co-authored by:` trailers or any other agent attribution for Codex, Claude, Gemini, or similar coding-agent identities.\nUse `gh issue comment` for progress updates and blockers, push any successful fix to the existing PR branch, and do not open a new pull request.\nIf GitHub exposes a failing check summary or log URL during your investigation, use it. At minimum, work from the failing check identifiers listed above.\nIf you cannot fix the failure safely, leave a concise GitHub comment explaining the blocker and exit with a non-zero status so Vigilante can stop and hand off to a human.\nKeep the changes minimal and focused on restoring CI for the existing pull request.": errors.New("codex exec [--cd /tmp/worktree --dangerously-bypass-approvals-and-sandbox prompt]: exit status 1"),
		},
	}
	env := &environment.Environment{OS: "darwin", Runner: runner}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := RunCIRemediationSession(
		context.Background(),
		env,
		store,
		githubbackend.NewBackend(&env.Runner),
		state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
		state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, IssueTitle: "Demo", IssueURL: "https://github.com/owner/repo/issues/7", WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7"},
		ghcli.PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17"},
		[]ghcli.StatusCheckRoll{{Context: "test", State: "COMPLETED", Conclusion: "FAILURE"}},
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
			"claude --version": "Claude Code 2.1.3",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Claude Code`) for investigation and implementation.",
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			testutil.Key("claude", "--print", "--dangerously-skip-permissions", skill.BuildIssuePreflightPrompt(
				state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"},
				ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
				state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: "claude"},
			)): "baseline ok",
			testutil.Key("claude", "--print", "--dangerously-skip-permissions", skill.BuildIssuePromptForRuntime(
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
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
	}
}

func TestRunIssueSessionSuccessWithGeminiProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gemini --version": "gemini 0.34.0",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Vigilante Session Start",
				Emoji:      "🧢",
				Percent:    20,
				ETAMinutes: 25,
				Items: []string{
					"Vigilante launched this implementation session in `/tmp/worktree`.",
					"Branch: `vigilante/issue-7`.",
					"Current stage: handing the issue off to the configured coding agent (`Gemini CLI`) for investigation and implementation.",
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
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
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
	}
}

func TestRunIssueSessionUsesMonorepoSkillWhenClassified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackTurborepo,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"pnpm-workspace.yaml", "turbo.json"},
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
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

	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), target, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)

	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
	}
}

func TestRunIssueSessionUsesNxSkillWhenClassified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackNx,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"nx.json", "pnpm-workspace.yaml"},
				MultiPackageRoots:    []string{"apps", "libs"},
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
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

	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), target, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)

	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
	}
}

func TestRunIssueSessionUsesRushMonorepoSkillWhenClassified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	target := state.WatchTarget{
		Path: "/tmp/repo",
		Repo: "owner/repo",
		Classification: repo.Classification{
			Shape:         repo.ShapeMonorepo,
			MonorepoStack: repo.MonorepoStackRush,
			ProcessHints: repo.ProcessHints{
				WorkspaceConfigFiles: []string{"rush.json"},
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
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

	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), target, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)

	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
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

	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)

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

func TestClassifyBlockedFailureEmitsProviderQuotaTelemetry(t *testing.T) {
	var events []struct {
		Event      string         `json:"event"`
		Properties map[string]any `json:"properties"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read telemetry request: %v", err)
		}
		var payload struct {
			Messages []json.RawMessage `json:"batch"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("parse telemetry batch: %v", err)
		}
		events = events[:0]
		for _, raw := range payload.Messages {
			var event struct {
				Event      string         `json:"event"`
				Properties map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatalf("parse telemetry event: %v", err)
			}
			events = append(events, event)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	manager, err := telemetry.Setup(context.Background(), telemetry.SetupConfig{
		BuildInfo: telemetry.BuildInfo{
			Version:           "1.2.3",
			Distro:            "direct",
			TelemetryEndpoint: server.URL,
			TelemetryToken:    "token",
		},
		StateRoot: t.TempDir(),
		EnvLookup: func(string) string { return "" },
	})
	if err != nil {
		t.Fatalf("setup telemetry: %v", err)
	}
	telemetry.SetDefault(manager)
	t.Cleanup(func() {
		telemetry.SetDefault(nil)
	})

	_ = classifyBlockedFailure("issue_execution", "codex exec", "You've hit your usage limit. Purchase more credits with token sk-live-secret.", errors.New("exit status 1"))
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown telemetry: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(events))
	}
	if got, want := events[0].Event, "downstream_service_rate_limited"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
	if got, want := events[0].Properties["service"], "provider"; got != want {
		t.Fatalf("service = %v, want %q", got, want)
	}
	if got, want := events[0].Properties["classification"], "quota"; got != want {
		t.Fatalf("classification = %v, want %q", got, want)
	}
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "sk-live-secret") {
		t.Fatalf("expected bounded telemetry payload, got %s", encoded)
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
	return preflightPromptCommandForSession(worktreePath, repo, repoPath, issueNumber, title, issueURL, state.Session{WorktreePath: worktreePath, Branch: branch})
}

func preflightPromptCommandForSession(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, session state.Session) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePreflightPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		session,
	))
}

func issuePromptCommand(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	return issuePromptCommandForSession(worktreePath, repo, repoPath, issueNumber, title, issueURL, state.Session{WorktreePath: worktreePath, Branch: branch, Provider: "codex"})
}

func issuePromptCommandForSession(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, session state.Session) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		session,
	))
}

func conflictResolutionPromptCommand(worktreePath string, repo string, repoPath string, session state.Session, pr ghcli.PullRequest) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildConflictResolutionPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo, Branch: session.BaseBranch},
		session,
		pr,
	))
}

func TestRunIssueSessionWritesLifecycleEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	baseRunner := testutil.FakeRunner{
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
					"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
				},
				Tagline: "Make it simple, but significant.",
			}): "ok",
			preflightPromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"): "baseline ok",
			issuePromptCommand("/tmp/worktree", "owner/repo", "/tmp/repo", 7, "Demo", "https://github.com/owner/repo/issues/7", "vigilante/issue-7"):     "done",
		},
	}
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	env := &environment.Environment{
		OS: "darwin",
		Runner: environment.LoggingRunner{
			Base:      baseRunner,
			AccessLog: store.AppendAccessLogEntry,
		},
	}
	session := state.Session{RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Status: state.SessionStatusRunning}
	got := RunIssueSession(context.Background(), env, store, githubbackend.NewBackend(&env.Runner), state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}, ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}, session)
	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("unexpected status (expected incomplete without PR): %s", got.Status)
	}
	data, err := os.ReadFile(store.SessionLogPath("owner/repo", 7))
	if err != nil {
		t.Fatal(err)
	}
	logContent := string(data)
	for _, want := range []string{
		"[vigilante ",
		"session started provider=",
		"preflight invocation starting",
		"preflight succeeded",
		"implementation invocation starting",
		"session completed status=incomplete",
	} {
		if !strings.Contains(logContent, want) {
			t.Errorf("expected session log to contain %q, got:\n%s", want, logContent)
		}
	}
}

func TestWriteLifecycleEventFormat(t *testing.T) {
	var buf strings.Builder
	writeLifecycleEvent(&buf, "test event key=value")
	output := buf.String()
	if !strings.HasPrefix(output, "[vigilante ") {
		t.Fatalf("expected [vigilante prefix, got %q", output)
	}
	if !strings.Contains(output, "test event key=value") {
		t.Fatalf("expected event message in output, got %q", output)
	}
	if !strings.HasSuffix(output, "\n") {
		t.Fatalf("expected trailing newline, got %q", output)
	}
}

func TestWriteLifecycleEventNilWriter(t *testing.T) {
	// Should not panic with a nil writer.
	writeLifecycleEvent(nil, "ignored event")
}

func TestDescribeExitErrorOOM(t *testing.T) {
	desc := describeExitError(errors.New("exit status 137"))
	// Regular error, not an exec.ExitError — just returns the error string.
	if !strings.Contains(desc, "exit status 137") {
		t.Fatalf("expected error string, got %q", desc)
	}
}

func containsLine(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
