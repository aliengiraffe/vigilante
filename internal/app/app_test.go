package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/nicobistolfi/vigilante/internal/telemetry"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

type analyticsBatchCapture struct {
	events []capturedAnalyticsEvent
}

type capturedAnalyticsEvent struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

func setupTelemetryCapture(t *testing.T) (*analyticsBatchCapture, func() error) {
	t.Helper()

	capture := &analyticsBatchCapture{}
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
		capture.events = capture.events[:0]
		for _, raw := range payload.Messages {
			var event capturedAnalyticsEvent
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatalf("parse telemetry event: %v", err)
			}
			capture.events = append(capture.events, event)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

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
	return capture, func() error {
		err := manager.Shutdown(context.Background())
		telemetry.SetDefault(nil)
		return err
	}
}

func TestRunSupportsTopLevelHelpFlags(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			app := New()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app.stdout = &stdout
			app.stderr = &stderr

			exitCode := app.Run(context.Background(), []string{arg})
			if exitCode != 0 {
				t.Fatalf("expected success exit code, got %d", exitCode)
			}
			if stderr.Len() != 0 {
				t.Fatalf("expected empty stderr, got %q", stderr.String())
			}
			for _, want := range []string{
				"usage:",
				"vigilante watch",
				"vigilante status",
				"vigilante service restart",
				"vigilante completion <bash|fish|zsh>",
				"vigilante <gh|git|docker> ...",
				`Use "vigilante <command> --help" for command-specific usage.`,
			} {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("expected help output to contain %q, got %q", want, stdout.String())
				}
			}
		})
	}
}

func TestRunProxiesSupportedToolCommands(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr
	var gotName string
	var gotArgs []string
	app.proxyExec = func(_ context.Context, _ io.Reader, out io.Writer, errOut io.Writer, name string, args ...string) (int, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		fmt.Fprint(out, "proxied stdout")
		fmt.Fprint(errOut, "proxied stderr")
		return 0, nil
	}

	exitCode := app.Run(context.Background(), []string{"gh", "repo", "view", "owner/repo"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if gotName != "gh" {
		t.Fatalf("proxy tool = %q, want %q", gotName, "gh")
	}
	if got, want := strings.Join(gotArgs, " "), "repo view owner/repo"; got != want {
		t.Fatalf("proxy args = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "proxied stdout"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "proxied stderr"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunReturnsUnderlyingProxyExitCode(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.proxyExec = func(context.Context, io.Reader, io.Writer, io.Writer, string, ...string) (int, error) {
		return 17, nil
	}

	if got := app.Run(context.Background(), []string{"git", "status"}); got != 17 {
		t.Fatalf("Run() = %d, want %d", got, 17)
	}
}

func TestDesiredSessionLabels(t *testing.T) {
	tests := []struct {
		name             string
		session          state.Session
		pr               *ghcli.PullRequest
		wantState        string
		wantIntervention string
	}{
		{
			name:             "running",
			session:          state.Session{Status: state.SessionStatusRunning},
			wantState:        labelRunning,
			wantIntervention: "",
		},
		{
			name:             "auto recovering",
			session:          state.Session{Status: state.SessionStatusResuming, LastResumeSource: autoRecoverySource},
			wantState:        labelRecovering,
			wantIntervention: "",
		},
		{
			name:             "blocked provider",
			session:          state.Session{Status: state.SessionStatusBlocked, BlockedReason: state.BlockedReason{Kind: "provider_auth"}},
			wantState:        labelBlocked,
			wantIntervention: labelNeedsProviderFix,
		},
		{
			name:             "blocked human input",
			session:          state.Session{Status: state.SessionStatusBlocked, BlockedReason: state.BlockedReason{Kind: "unknown_operator_action_required"}},
			wantState:        labelBlocked,
			wantIntervention: labelNeedsHumanInput,
		},
		{
			name:             "success ready for review",
			session:          state.Session{Status: state.SessionStatusSuccess},
			wantState:        labelReadyForReview,
			wantIntervention: "",
		},
		{
			name:    "success awaiting validation",
			session: state.Session{Status: state.SessionStatusSuccess},
			pr: &ghcli.PullRequest{
				Number:           17,
				ReviewDecision:   "APPROVED",
				MergeStateStatus: "CLEAN",
				StatusCheckRollup: []ghcli.StatusCheckRoll{
					{State: "COMPLETED", Conclusion: "SUCCESS"},
				},
			},
			wantState:        labelAwaitingUserValidation,
			wantIntervention: "",
		},
		{
			name:             "merged done",
			session:          state.Session{Status: state.SessionStatusSuccess, PullRequestMergedAt: "2026-03-17T18:00:00Z"},
			wantState:        labelDone,
			wantIntervention: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotState, gotIntervention := desiredSessionLabels(tc.session, tc.pr)
			if gotState != tc.wantState || gotIntervention != tc.wantIntervention {
				t.Fatalf("unexpected labels: got (%q, %q), want (%q, %q)", gotState, gotIntervention, tc.wantState, tc.wantIntervention)
			}
		})
	}
}

func TestSessionManagedLabelsIncludesIteratingWhenActive(t *testing.T) {
	labels := sessionManagedLabels(state.Session{
		Status:              state.SessionStatusRunning,
		IterationInProgress: true,
	}, nil)
	if len(labels) != 2 || labels[0] != labelRunning || labels[1] != labelIterating {
		t.Fatalf("unexpected labels: %#v", labels)
	}
}

func TestProcessGitHubIterationRequestsForTargetRejectsNonAssignee(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	repoPath := t.TempDir()
	stateRoot := t.TempDir()
	t.Setenv("VIGILANTE_HOME", stateRoot)

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.state = state.NewStore()
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1":          `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","labels":[{"name":"vigilante:ready-for-review"}],"assignees":[{"login":"nicobistolfi"}]}`,
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"@vigilanteai please revise this","created_at":"2026-03-19T11:59:00Z","user":{"login":"someoneelse"}}]`,
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Iteration Ignored",
				Emoji:      "🛂",
				Percent:    100,
				ETAMinutes: 1,
				Items: []string{
					"Ignored the latest `@vigilanteai` iteration request from `@someoneelse`.",
					"Only a current issue assignee can request another implementation iteration for this issue.",
					"Next step: ask an assignee to post the follow-up request if another pass is needed.",
				},
				Tagline: "Hands on the wheel, one driver at a time.",
			}): "ok",
		},
	}

	sessions := []state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		Provider:     "codex",
		IssueNumber:  1,
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Status:       state.SessionStatusSuccess,
		Branch:       "vigilante/issue-1-first",
		WorktreePath: filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1"),
	}}

	updated, started, err := app.processGitHubIterationRequestsForTarget(context.Background(), state.WatchTarget{
		Path:   repoPath,
		Repo:   "owner/repo",
		Branch: "main",
	}, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("expected no iteration dispatches, got %d", started)
	}
	if updated[0].LastIterationCommentID != 101 {
		t.Fatalf("expected rejected iteration comment to be recorded, got %#v", updated[0])
	}
}

func TestProcessGitHubIterationRequestsForTargetDispatchesAssigneeComment(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	repoPath := t.TempDir()
	stateRoot := t.TempDir()
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	t.Setenv("VIGILANTE_HOME", stateRoot)

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.state = state.NewStore()
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	app.repoLabelsProvisionedOnce["owner/repo"] = true

	iterationContext := buildIterationPromptContext([]ghcli.IssueComment{
		{
			ID:        101,
			Body:      "@vigilanteai first follow-up",
			CreatedAt: now.Add(-2 * time.Minute),
			User: struct {
				Login string `json:"login"`
			}{Login: "nicobistolfi"},
		},
		{
			ID:        102,
			Body:      "@vigilanteai tighten the validation path",
			CreatedAt: now.Add(-1 * time.Minute),
			User: struct {
				Login string `json:"login"`
			}{Login: "nicobistolfi"},
		},
	})
	startSession := state.Session{
		RepoPath:               repoPath,
		Repo:                   "owner/repo",
		Provider:               "codex",
		IssueNumber:            1,
		IssueTitle:             "first",
		IssueURL:               "https://github.com/owner/repo/issues/1",
		Status:                 state.SessionStatusSuccess,
		Branch:                 "vigilante/issue-1-first",
		WorktreePath:           worktreePath,
		IssueBody:              "Original body",
		IterationPromptContext: iterationContext,
		IterationInProgress:    true,
	}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1":          `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","labels":[{"name":"vigilante:ready-for-review"}],"assignees":[{"login":"nicobistolfi"}]}`,
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"@vigilanteai first follow-up","created_at":"2026-03-19T11:58:00Z","user":{"login":"nicobistolfi"}},{"id":102,"body":"@vigilanteai tighten the validation path","created_at":"2026-03-19T11:59:00Z","user":{"login":"nicobistolfi"}}]`,
			"gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/102/reactions -f content=eyes": "ok",
			"git worktree prune":                          "ok",
			"git fetch origin main":                       "ok",
			"git worktree list --porcelain":               "",
			"git branch -f main refs/remotes/origin/main": "ok",
			"git worktree add -b vigilante/issue-1-first " + worktreePath + " origin/main":                                                              "ok",
			"gh issue edit --repo owner/repo 1 --add-label vigilante:iterating --add-label vigilante:running --remove-label vigilante:ready-for-review": "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath, state.Session{
				Repo:                   "owner/repo",
				IssueNumber:            1,
				IssueTitle:             "first",
				IssueBody:              "Issue body",
				IssueURL:               "https://github.com/owner/repo/issues/1",
				BaseBranch:             "main",
				Branch:                 "vigilante/issue-1-first",
				WorktreePath:           worktreePath,
				Status:                 state.SessionStatusRunning,
				Provider:               "codex",
				IterationInProgress:    true,
				IterationPromptContext: iterationContext,
			}): "ok",
			preflightPromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"): "ok",
			issuePromptCommandForSession(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", state.Session{
				WorktreePath:           worktreePath,
				Branch:                 "vigilante/issue-1-first",
				Provider:               "codex",
				IssueBody:              "Issue body",
				IterationPromptContext: iterationContext,
			}): "done",
			"gh issue edit --repo owner/repo 1 --add-label vigilante:ready-for-review --remove-label vigilante:iterating --remove-label vigilante:running": "ok",
		},
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1-first": errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1":       errors.New("exit status 1"),
		},
	}

	sessions := []state.Session{startSession}
	updated, started, err := app.processGitHubIterationRequestsForTarget(context.Background(), state.WatchTarget{
		Path:   repoPath,
		Repo:   "owner/repo",
		Branch: "main",
	}, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if started != 1 {
		t.Fatalf("expected one iteration dispatch, got %d", started)
	}
	app.waitForSessions()

	saved, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected one saved session, got %#v", saved)
	}
	if saved[0].LastIterationCommentID != 102 {
		t.Fatalf("expected iteration comment tracking, got %#v", saved[0])
	}
	if saved[0].IssueBody != "Issue body" {
		t.Fatalf("expected updated issue body, got %#v", saved[0])
	}
	if strings.TrimSpace(saved[0].IterationPromptContext) == "" {
		t.Fatalf("expected iteration prompt context, got %#v", saved[0])
	}
	if saved[0].IterationInProgress {
		t.Fatalf("expected iteration flag to clear after successful run, got %#v", saved[0])
	}
	if updated[0].Status != state.SessionStatusRunning {
		t.Fatalf("expected in-memory session to be redispatched before completion, got %#v", updated[0])
	}
}

func TestProcessGitHubIterationRequestsForTargetReusesExistingWorktree(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	repoPath := t.TempDir()
	stateRoot := t.TempDir()
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	t.Setenv("VIGILANTE_HOME", stateRoot)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.state = state.NewStore()
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	app.repoLabelsProvisionedOnce["owner/repo"] = true

	iterationContext := buildIterationPromptContext([]ghcli.IssueComment{
		{
			ID:        102,
			Body:      "@vigilanteai continue from the existing worktree",
			CreatedAt: now.Add(-1 * time.Minute),
			User: struct {
				Login string `json:"login"`
			}{Login: "nicobistolfi"},
		},
	})
	startSession := state.Session{
		RepoPath:               repoPath,
		Repo:                   "owner/repo",
		Provider:               "codex",
		IssueNumber:            1,
		IssueTitle:             "first",
		IssueURL:               "https://github.com/owner/repo/issues/1",
		Status:                 state.SessionStatusBlocked,
		Branch:                 "vigilante/issue-1-first",
		WorktreePath:           worktreePath,
		BaseBranch:             "main",
		IssueBody:              "Original body",
		BlockedStage:           "issue_execution",
		BlockedReason:          state.BlockedReason{Kind: "provider_runtime_error", Operation: "codex exec", Summary: "transient failure"},
		ResumeRequired:         true,
		ResumeHint:             "vigilante resume --repo owner/repo --issue 1",
		IterationPromptContext: "Old iteration context",
		IterationInProgress:    true,
	}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1":          `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","labels":[{"name":"vigilante:blocked"}],"assignees":[{"login":"nicobistolfi"}]}`,
			"gh api repos/owner/repo/issues/1/comments": `[{"id":102,"body":"@vigilanteai continue from the existing worktree","created_at":"2026-03-19T11:59:00Z","user":{"login":"nicobistolfi"}}]`,
			"gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/102/reactions -f content=eyes": "ok",
			"git worktree prune": "ok",
			"gh issue edit --repo owner/repo 1 --add-label vigilante:iterating --add-label vigilante:running --remove-label vigilante:blocked": "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath, state.Session{
				Repo:                   "owner/repo",
				IssueNumber:            1,
				IssueTitle:             "first",
				IssueBody:              "Issue body",
				IssueURL:               "https://github.com/owner/repo/issues/1",
				BaseBranch:             "main",
				Branch:                 "vigilante/issue-1-first",
				WorktreePath:           worktreePath,
				Status:                 state.SessionStatusRunning,
				Provider:               "codex",
				IterationInProgress:    true,
				IterationPromptContext: iterationContext,
			}): "ok",
			preflightPromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"): "ok",
			issuePromptCommandForSession(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", state.Session{
				WorktreePath:           worktreePath,
				Branch:                 "vigilante/issue-1-first",
				Provider:               "codex",
				IssueBody:              "Issue body",
				IterationPromptContext: iterationContext,
			}): "done",
			"gh issue edit --repo owner/repo 1 --add-label vigilante:ready-for-review --remove-label vigilante:iterating --remove-label vigilante:running": "ok",
		},
	}

	sessions := []state.Session{startSession}
	updated, started, err := app.processGitHubIterationRequestsForTarget(context.Background(), state.WatchTarget{
		Path:   repoPath,
		Repo:   "owner/repo",
		Branch: "main",
	}, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if started != 1 {
		t.Fatalf("expected one iteration dispatch, got %d", started)
	}
	if updated[0].Status != state.SessionStatusRunning {
		t.Fatalf("expected reused iteration session to be running, got %#v", updated[0])
	}
	if updated[0].BlockedStage != "" || updated[0].ResumeRequired {
		t.Fatalf("expected reused iteration session to clear blocked state, got %#v", updated[0])
	}

	app.waitForSessions()

	saved, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 {
		t.Fatalf("expected one saved session, got %#v", saved)
	}
	if saved[0].LastIterationCommentID != 102 {
		t.Fatalf("expected iteration comment tracking, got %#v", saved[0])
	}
	if saved[0].IssueBody != "Issue body" {
		t.Fatalf("expected updated issue body, got %#v", saved[0])
	}
	if strings.TrimSpace(saved[0].IterationPromptContext) == "" {
		t.Fatalf("expected iteration prompt context, got %#v", saved[0])
	}
	if saved[0].IterationInProgress {
		t.Fatalf("expected iteration flag to clear after successful run, got %#v", saved[0])
	}
}

func TestDispatchIssueSessionRejectsUnsafeExistingIterationWorktree(t *testing.T) {
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	repoPath := t.TempDir()
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune": "ok",
		},
	}

	comment := &ghcli.IssueComment{
		ID:        101,
		Body:      "@vigilanteai continue",
		CreatedAt: now,
		User: struct {
			Login string `json:"login"`
		}{Login: "nicobistolfi"},
	}
	session, err := app.dispatchIssueSession(
		context.Background(),
		state.WatchTarget{Path: repoPath, Repo: "owner/repo", Branch: "main"},
		ghcli.Issue{Number: 1, Title: "first", URL: "https://github.com/owner/repo/issues/1"},
		"codex",
		state.Session{
			RepoPath:     repoPath,
			Repo:         "owner/repo",
			IssueNumber:  1,
			IssueTitle:   "first",
			IssueURL:     "https://github.com/owner/repo/issues/1",
			Status:       state.SessionStatusBlocked,
			WorktreePath: worktreePath,
		},
		"Issue body",
		"iteration context",
		comment,
	)
	if err == nil {
		t.Fatal("expected unsafe existing worktree reuse to fail")
	}
	if session.Status != state.SessionStatusBlocked {
		t.Fatalf("expected blocked session, got %#v", session)
	}
	if !strings.Contains(session.LastError, "existing session branch is empty") {
		t.Fatalf("expected actionable unsafe-worktree error, got %#v", session)
	}
	if !strings.Contains(session.BlockedReason.Summary, "existing session branch is empty") {
		t.Fatalf("expected blocked summary to explain refused reuse, got %#v", session)
	}
}

func TestSyncIssueManagedLabelsQueued(t *testing.T) {
	capture, shutdownTelemetry := setupTelemetryCapture(t)
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100":                                                     `[{"name":"bug"},{"name":"vigilante:queued"},{"name":"vigilante:running"},{"name":"vigilante:iterating"},{"name":"vigilante:blocked"},{"name":"vigilante:recovering"},{"name":"vigilante:ready-for-review"},{"name":"vigilante:awaiting-user-validation"},{"name":"vigilante:done"},{"name":"vigilante:needs-review"},{"name":"vigilante:needs-human-input"},{"name":"vigilante:needs-provider-fix"},{"name":"vigilante:needs-git-fix"},{"name":"codex"},{"name":"claude"},{"name":"gemini"},{"name":"vigilante:resume"},{"name":"vigilante:automerge"},{"name":"resume"}]`,
			"gh api repos/owner/repo/issues/7":                                                                `{"labels":[{"name":"bug"},{"name":"vigilante:running"}]}`,
			"gh issue edit --repo owner/repo 7 --add-label vigilante:queued --remove-label vigilante:running": "ok",
		},
	}

	if err := app.syncIssueManagedLabels(context.Background(), "owner/repo", 7, []string{labelQueued}); err != nil {
		t.Fatal(err)
	}
	if err := shutdownTelemetry(); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(capture.events))
	}
	if got, want := capture.events[0].Event, "github_issue_labels_synced"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["add_count"], float64(1); got != want {
		t.Fatalf("add_count = %v, want %v", got, want)
	}
	if got, want := capture.events[0].Properties["remove_count"], float64(1); got != want {
		t.Fatalf("remove_count = %v, want %v", got, want)
	}
}

func TestSyncIssueManagedLabelsNoopDoesNotEmitTelemetry(t *testing.T) {
	capture, shutdownTelemetry := setupTelemetryCapture(t)
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[{"name":"vigilante:queued"},{"name":"vigilante:running"},{"name":"vigilante:iterating"},{"name":"vigilante:blocked"},{"name":"vigilante:recovering"},{"name":"vigilante:ready-for-review"},{"name":"vigilante:awaiting-user-validation"},{"name":"vigilante:done"},{"name":"vigilante:needs-review"},{"name":"vigilante:needs-human-input"},{"name":"vigilante:needs-provider-fix"},{"name":"vigilante:needs-git-fix"},{"name":"codex"},{"name":"claude"},{"name":"gemini"},{"name":"vigilante:resume"},{"name":"vigilante:automerge"},{"name":"resume"}]`,
			"gh api repos/owner/repo/issues/7":            `{"labels":[{"name":"vigilante:queued"}]}`,
		},
	}

	if err := app.syncIssueManagedLabels(context.Background(), "owner/repo", 7, []string{labelQueued}); err != nil {
		t.Fatal(err)
	}
	if err := shutdownTelemetry(); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 0 {
		t.Fatalf("expected no telemetry events, got %#v", capture.events)
	}
}

func TestCommentOnIssueEmitsTypedTelemetry(t *testing.T) {
	capture, shutdownTelemetry := setupTelemetryCapture(t)
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 7 --body body": "ok",
		},
	}

	if err := app.commentOnIssue(context.Background(), "owner/repo", 7, "body", "dispatch_failure", "dispatch"); err != nil {
		t.Fatal(err)
	}
	if err := shutdownTelemetry(); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(capture.events))
	}
	if got, want := capture.events[0].Event, "github_issue_comment_emitted"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["comment_type"], "dispatch_failure"; got != want {
		t.Fatalf("comment_type = %v, want %q", got, want)
	}
}

func TestRecordSessionFailureEmitsSessionTransitionTelemetry(t *testing.T) {
	capture, shutdownTelemetry := setupTelemetryCapture(t)
	app := New()
	session := &state.Session{
		Repo:        "owner/repo",
		IssueNumber: 7,
		Provider:    "codex",
		Status:      state.SessionStatusRunning,
	}

	app.recordSessionFailure(session, "issue_execution", "git worktree add", errors.New("boom"))
	if err := shutdownTelemetry(); err != nil {
		t.Fatal(err)
	}
	if len(capture.events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(capture.events))
	}
	if got, want := capture.events[0].Event, "issue_session_transition"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["previous_status"], "running"; got != want {
		t.Fatalf("previous_status = %v, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["status"], "blocked"; got != want {
		t.Fatalf("status = %v, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["transition"], "blocked"; got != want {
		t.Fatalf("transition = %v, want %q", got, want)
	}
}

func TestSyncSessionIssueLabelsUsesPullRequestReviewState(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[{"name":"vigilante:queued"},{"name":"vigilante:running"},{"name":"vigilante:iterating"},{"name":"vigilante:blocked"},{"name":"vigilante:recovering"},{"name":"vigilante:ready-for-review"},{"name":"vigilante:awaiting-user-validation"},{"name":"vigilante:done"},{"name":"vigilante:needs-review"},{"name":"vigilante:needs-human-input"},{"name":"vigilante:needs-provider-fix"},{"name":"vigilante:needs-git-fix"},{"name":"codex"},{"name":"claude"},{"name":"gemini"},{"name":"vigilante:resume"},{"name":"vigilante:automerge"},{"name":"resume"}]`,
			"gh pr view --repo owner/repo 17 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": `{"number":17,"title":"Demo PR","body":"PR body","url":"https://github.com/owner/repo/pull/17","state":"OPEN","mergedAt":null,"labels":[],"isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"context":"test","state":"COMPLETED","conclusion":"SUCCESS"}]}`,
			"gh api repos/owner/repo/issues/7": `{"labels":[{"name":"vigilante:ready-for-review"},{"name":"vigilante:needs-review"}]}`,
			"gh issue edit --repo owner/repo 7 --add-label vigilante:awaiting-user-validation --remove-label vigilante:needs-review --remove-label vigilante:ready-for-review": "ok",
			"gh issue edit --repo owner/repo 7 --remove-label vigilante:needs-review":                                                                                          "ok",
		},
	}

	err := app.syncSessionIssueLabels(context.Background(), state.Session{
		Repo:              "owner/repo",
		IssueNumber:       7,
		Status:            state.SessionStatusSuccess,
		PullRequestNumber: 17,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSyncIssueManagedLabelsProvisionMissingRepositoryLabels(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[{"name":"bug"}]`,
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:queued -f color=BFDADC -f description=The issue is eligible for dispatch and waiting for a worker slot.":                                           "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:running -f color=0E8A16 -f description=A coding-agent session is currently executing for the issue.":                                               "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:iterating -f color=1D76DB -f description=An assignee-requested follow-up implementation iteration is actively in progress.":                        "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:blocked -f color=D93F0B -f description=Execution cannot continue until a blocker is resolved.":                                                     "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:recovering -f color=FBCA04 -f description=An automatic stale-session recovery attempt is actively rebuilding local execution state.":               "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:ready-for-review -f color=FBCA04 -f description=Implementation is complete enough for a human to review the resulting PR or branch.":               "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:awaiting-user-validation -f color=F9D0C4 -f description=Changes are ready for product or operator validation before the issue is considered done.": "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:done -f color=5319E7 -f description=Vigilante completed its work on the issue and no further automation is expected.":                              "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:needs-human-input -f color=F7C6C7 -f description=The agent is waiting on product, operator, or repository-owner guidance.":                         "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:needs-provider-fix -f color=E99695 -f description=Execution is blocked by provider auth, quota, or runtime setup issues.":                          "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:needs-git-fix -f color=C2E0C6 -f description=Execution is blocked by repository or git state that requires human repair.":                          "ok",
			"gh api --method POST repos/owner/repo/labels -f name=codex -f color=1D76DB -f description=Routes the issue to the Codex provider for execution.":                                                                  "ok",
			"gh api --method POST repos/owner/repo/labels -f name=claude -f color=0052CC -f description=Routes the issue to the Claude provider for execution.":                                                                "ok",
			"gh api --method POST repos/owner/repo/labels -f name=gemini -f color=006B75 -f description=Routes the issue to the Gemini provider for execution.":                                                                "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:resume -f color=C5DEF5 -f description=Requests that Vigilante resume a blocked session.":                                                           "ok",
			"gh api --method POST repos/owner/repo/labels -f name=vigilante:automerge -f color=0E8A16 -f description=Requests automatic squash merge once required checks and merge requirements are satisfied.":               "ok",
			"gh api --method POST repos/owner/repo/labels -f name=resume -f color=C5DEF5 -f description=Legacy compatibility alias for vigilante:resume.":                                                                      "ok",
			"gh api repos/owner/repo/issues/7":                               `{"labels":[{"name":"bug"}]}`,
			"gh issue edit --repo owner/repo 7 --add-label vigilante:queued": "ok",
		},
	}

	if err := app.syncIssueManagedLabels(context.Background(), "owner/repo", 7, []string{labelQueued}); err != nil {
		t.Fatal(err)
	}
}

func TestRunSupportsSubcommandHelp(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"watch", "--help"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	for _, want := range []string{
		"usage: vigilante watch",
		"Register a local repository for issue monitoring.",
		"-assignee",
		"-label",
		"-max-parallel",
		"-provider",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected help output to contain %q, got %q", want, stdout.String())
		}
	}
}

func TestRunSupportsDaemonHelp(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"daemon", "--help"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "usage:\n  vigilante daemon run [--once] [--interval duration]") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestRunSupportsServiceHelp(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"service", "--help"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "usage:\n  vigilante service restart") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestStatusCommandReportsServiceState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
		},
	}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	for _, want := range []string{
		"state: running",
		"manager: systemd",
		"service: vigilante.service",
		"path: " + unitPath,
		"installed: yes",
		"running: yes",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("expected output to contain %q, got %q", want, stdout.String())
		}
	}
}

func TestStatusCommandFailsOnUnsupportedOS(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr
	app.env.OS = "windows"
	app.env.Runner = testutil.FakeRunner{}

	exitCode := app.Run(context.Background(), []string{"status"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `error: unsupported OS "windows"`) {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestServiceRestartCommandRequestsRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"systemctl --user show --property=LoadState,ActiveState vigilante.service": "LoadState=loaded\nActiveState=active\n",
			"systemctl --user restart vigilante.service":                               "",
		},
	}

	exitCode := app.Run(context.Background(), []string{"service", "restart"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "service restart requested") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestServiceRestartCommandFailsWhenServiceIsNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr
	app.env.OS = "linux"
	app.env.Runner = testutil.FakeRunner{}

	exitCode := app.Run(context.Background(), []string{"service", "restart"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error: service is not installed") {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunCompletionCommandOutputsScripts(t *testing.T) {
	tests := []struct {
		shell string
		want  string
	}{
		{shell: "bash", want: "complete -F _vigilante vigilante"},
		{shell: "fish", want: "complete -c vigilante -f"},
		{shell: "zsh", want: "#compdef vigilante"},
	}

	for _, tc := range tests {
		t.Run(tc.shell, func(t *testing.T) {
			app := New()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			app.stdout = &stdout
			app.stderr = &stderr

			exitCode := app.Run(context.Background(), []string{"completion", tc.shell})
			if exitCode != 0 {
				t.Fatalf("expected success exit code, got %d", exitCode)
			}
			if stderr.Len() != 0 {
				t.Fatalf("expected empty stderr, got %q", stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Fatalf("expected completion output to contain %q, got %q", tc.want, stdout.String())
			}
		})
	}
}

func TestRunCompletionCommandRejectsUnsupportedShell(t *testing.T) {
	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"completion", "tcsh"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), `unsupported shell "tcsh" (supported: bash, fish, zsh)`) {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

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

func TestSetupCreatesStateLayoutAndInstallsBundledSkillsForAllProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_HOME", filepath.Join(home, ".claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(home, ".gemini"))

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 0.114.0",
			"gh auth status":  "ok",
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
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementation, "agents", "openai.yaml"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementationOnMonorepo, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementationOnTurborepo, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementationOnNx, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementationOnGradleMultiProject, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementationOnRushMonorepo, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteConflictResolution, "SKILL.md"),
		filepath.Join(app.state.CodexHome(), "skills", skill.DockerComposeLaunch, "SKILL.md"),
		filepath.Join(app.state.ClaudeHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.ClaudeHome(), "commands", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.GeminiHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(app.state.GeminiHome(), "commands", skill.VigilanteIssueImplementation+".toml")); !os.IsNotExist(err) {
		t.Fatalf("expected Gemini legacy command to be removed, got: %v", err)
	}
}

func TestSetupWithGeminiInstallsBundledSkillsForAllProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_HOME", filepath.Join(home, ".claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(home, ".gemini"))

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "gemini": "/usr/bin/gemini"},
		Outputs: map[string]string{
			"gemini --version": "gemini 0.34.0",
			"gh auth status":   "ok",
		},
	}

	if err := app.SetupWithProvider(context.Background(), false, "gemini"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(app.state.CodexHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.ClaudeHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.ClaudeHome(), "commands", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.GeminiHome(), "skills", skill.VigilanteIssueImplementation, "SKILL.md"),
		filepath.Join(app.state.GeminiHome(), "skills", skill.VigilanteIssueImplementationOnRushMonorepo, "SKILL.md"),
		filepath.Join(app.state.GeminiHome(), "skills", skill.VigilanteConflictResolution, "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(app.state.GeminiHome(), "commands", skill.VigilanteIssueImplementation+".toml"),
		filepath.Join(app.state.GeminiHome(), "commands", skill.VigilanteIssueImplementationOnRushMonorepo+".toml"),
		filepath.Join(app.state.GeminiHome(), "commands", skill.VigilanteConflictResolution+".toml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected Gemini legacy command to be removed: %s (%v)", path, err)
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
	if !strings.Contains(stdout.String(), "\"max_parallel_sessions\": 0") {
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
	executablePath := environment.ExecutablePath()
	resolvedExecutablePath, err := filepath.EvalSymlinks(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version":                   "codex 0.114.0",
			"gh auth status":                    "ok",
			`/bin/zsh -lic printf "%s" "$PATH"`: "/usr/bin:/bin:/Users/test/.local/bin",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" command -v 'git'`:            "/usr/bin/git\n",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" command -v 'gh'`:             "/usr/bin/gh\n",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" command -v 'codex'`:          "/Users/test/.local/bin/codex\n",
			`/bin/sh -lc PATH="/usr/bin:/bin:/Users/test/.local/bin" 'codex' --version`:           "codex 0.114.0\n",
			testutil.Key("xattr", resolvedExecutablePath):                                         "",
			testutil.Key("codesign", "--force", "--sign", "-", resolvedExecutablePath):            "",
			testutil.Key("spctl", "--assess", "--type", "execute", "-vv", resolvedExecutablePath): "",
			testutil.Key("launchctl", "unload", launchAgentPath):                                  "",
			testutil.Key("launchctl", "load", launchAgentPath):                                    "",
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                             "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                                    "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"):            "origin/main\n",
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
	if targets[0].MaxParallel != 0 {
		t.Fatalf("expected explicit zero max_parallel_sessions to update target to unlimited: %#v", targets[0])
	}
}

func TestWatchCommandWithoutMaxParallelPreservesExistingTargetValue(t *testing.T) {
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

	if err := app.Watch(context.Background(), repoPath, false, nil, "", 3); err != nil {
		t.Fatal(err)
	}
	if err := app.runCommand(context.Background(), []string{"watch", repoPath}); err != nil {
		t.Fatal(err)
	}

	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].MaxParallel != 3 {
		t.Fatalf("expected omitted max_parallel flag to preserve existing value: %#v", targets)
	}
}

func TestWatchRejectsNegativeMaxParallel(t *testing.T) {
	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}

	err := app.runCommand(context.Background(), []string{"watch", "--max-parallel", "-1", "/tmp/repo"})
	if err == nil || err.Error() != "max parallel must be at least 0" {
		t.Fatalf("expected negative max_parallel rejection, got %v", err)
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
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "claude": "/usr/bin/claude"},
		Outputs: map[string]string{
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                         "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
			"claude --version": "Claude Code 2.1.3",
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

func TestWatchPersistsRepoClassification(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(repoPath, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoPath, "packages", "shared"), 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "gemini": "/usr/bin/gemini"},
		Outputs: map[string]string{
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                         "git@github.com:nicobistolfi/vigilante.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
			"gemini --version": "gemini 1.7.0",
		},
	}

	if err := app.Watch(context.Background(), repoPath, false, nil, "", 0); err != nil {
		t.Fatal(err)
	}

	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("unexpected targets: %#v", targets)
	}
	if targets[0].Classification.Shape != repo.ShapeMonorepo {
		t.Fatalf("expected monorepo classification to persist: %#v", targets[0])
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

func TestSetupFailsWhenProviderVersionIsIncompatible(t *testing.T) {
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
			"codex --version": "codex 2.0.0",
		},
	}

	err := app.Setup(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "codex CLI version 2.0.0 is incompatible") {
		t.Fatalf("unexpected error: %v", err)
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

func TestClassifyBlockedReasonEmitsGitHubRateLimitTelemetry(t *testing.T) {
	capture, shutdownTelemetry := setupTelemetryCapture(t)

	_ = classifyBlockedReason("dispatch", "gh api", errors.New("API rate limit exceeded for user ID 12345"))
	if err := shutdownTelemetry(); err != nil {
		t.Fatalf("shutdown telemetry: %v", err)
	}

	if len(capture.events) != 1 {
		t.Fatalf("expected 1 telemetry event, got %d", len(capture.events))
	}
	if got, want := capture.events[0].Event, "downstream_service_rate_limited"; got != want {
		t.Fatalf("event = %q, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["service"], "github"; got != want {
		t.Fatalf("service = %v, want %q", got, want)
	}
	if got, want := capture.events[0].Properties["classification"], "rate_limit"; got != want {
		t.Fatalf("classification = %v, want %q", got, want)
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
			localCleanupCommentCommand("owner/repo", 44, state.Session{
				Branch:       "vigilante/issue-44",
				WorktreePath: worktreePath,
			}): "ok",
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

func TestCleanupSessionCommentsNoopForLocalCLIRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			localCleanupNoopCommentCommand("owner/repo", 44): "ok",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions(nil); err != nil {
		t.Fatal(err)
	}

	err := app.CleanupSession(context.Background(), "owner/repo", 44, "cli")
	if err == nil || !strings.Contains(err.Error(), "running session not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestCleanupSessionIgnoresLocalCommentFailure(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-44")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                                          "ok",
			"git worktree remove --force " + worktreePath:                 "ok",
			"git worktree list --porcelain":                               "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-44": "ok",
			"git branch -D vigilante/issue-44":                            "Deleted branch vigilante/issue-44\n",
		},
		Errors: map[string]error{
			localCleanupCommentCommand("owner/repo", 44, state.Session{
				Branch:       "vigilante/issue-44",
				WorktreePath: worktreePath,
			}): errors.New("comment failed"),
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

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "local cleanup result comment failed repo=owner/repo issue=44 err=comment failed") {
		t.Fatalf("expected cleanup comment failure log, got: %s", logData)
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

func TestRedispatchSessionRunningIssue(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-44")
	branch := "vigilante/issue-44-force-redispatch"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"git worktree prune":            "ok",
			"git worktree list --porcelain": "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-44-old-run":                                                                      "ok",
			"git branch -D vigilante/issue-44-old-run":                                                                                                 "Deleted branch vigilante/issue-44-old-run\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels":                            `[{"number":44,"title":"force redispatch","createdAt":"2026-03-12T12:00:00Z","url":"https://github.com/owner/repo/issues/44","labels":[]}]`,
			"git worktree add -b " + branch + " " + worktreePath + " origin/main":                                                                      "ok",
			sessionStartCommentCommand("owner/repo", 44, worktreePath, state.Session{Branch: branch}):                                                  "ok",
			preflightPromptCommand(worktreePath, "owner/repo", repoPath, 44, "force redispatch", "https://github.com/owner/repo/issues/44", branch):    "baseline ok",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 44, "force redispatch", "https://github.com/owner/repo/issues/44", branch):        "done",
			"gh issue comment --repo owner/repo 44 --body " + localRedispatchStartedComment(state.Session{Branch: branch, WorktreePath: worktreePath}): "ok",
		}),
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/" + branch:          errors.New("exit status 1"),
			"git show-ref --verify --quiet refs/heads/vigilante/issue-44": errors.New("exit status 1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{
		Path:     repoPath,
		Repo:     "owner/repo",
		Branch:   "main",
		Assignee: "nicobistolfi",
		Provider: "codex",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		Provider:     "codex",
		IssueNumber:  44,
		IssueTitle:   "force redispatch",
		IssueURL:     "https://github.com/owner/repo/issues/44",
		Status:       state.SessionStatusRunning,
		Branch:       "vigilante/issue-44-old-run",
		WorktreePath: worktreePath,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.RedispatchSession(context.Background(), "owner/repo", 44, "cli"); err != nil {
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
	if sessions[0].Status != state.SessionStatusSuccess || sessions[0].Branch != branch {
		t.Fatalf("expected fresh redispatched session, got: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "redispatched owner/repo issue #44 in "+worktreePath) {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestRedispatchSessionCleansBlockedIssueBranchCandidates(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-7")
	newBranch := "vigilante/issue-7-force-redispatch"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"git worktree prune":                                         "ok",
			"git worktree list --porcelain":                              "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/" + newBranch:      "ok",
			"git branch -D " + newBranch:                                 "Deleted branch " + newBranch + "\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-7": "ok",
			"git branch -D vigilante/issue-7":                            "Deleted branch vigilante/issue-7\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels":                              `[{"number":7,"title":"force redispatch","createdAt":"2026-03-12T12:00:00Z","url":"https://github.com/owner/repo/issues/7","labels":[]}]`,
			"git worktree add " + worktreePath + " " + newBranch:                                                                                         "ok",
			sessionStartCommentCommand("owner/repo", 7, worktreePath, state.Session{Branch: newBranch}):                                                  "ok",
			preflightPromptCommand(worktreePath, "owner/repo", repoPath, 7, "force redispatch", "https://github.com/owner/repo/issues/7", newBranch):     "baseline ok",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 7, "force redispatch", "https://github.com/owner/repo/issues/7", newBranch):         "done",
			"gh issue comment --repo owner/repo 7 --body " + localRedispatchStartedComment(state.Session{Branch: newBranch, WorktreePath: worktreePath}): "ok",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{
		Path:     repoPath,
		Repo:     "owner/repo",
		Branch:   "main",
		Assignee: "nicobistolfi",
		Provider: "codex",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		Provider:     "codex",
		IssueNumber:  7,
		IssueTitle:   "force redispatch",
		IssueURL:     "https://github.com/owner/repo/issues/7",
		Status:       state.SessionStatusBlocked,
		Branch:       "vigilante/issue-7",
		WorktreePath: worktreePath,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.RedispatchSession(context.Background(), "owner/repo", 7, "cli"); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Branch != newBranch {
		t.Fatalf("expected redispatched session with reused slug branch, got: %#v", sessions)
	}
}

func TestRedispatchSessionFailsWhenRepoIsUnwatched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	err := app.RedispatchSession(context.Background(), "owner/repo", 44, "cli")
	if err == nil || !strings.Contains(err.Error(), "watch target not found") {
		t.Fatalf("expected unwatched repo failure, got: %v", err)
	}
}

func TestRedispatchSessionFailsWhenCleanupFails(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-44")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune": "ok",
		},
		Errors: map[string]error{
			"git worktree remove --force " + worktreePath: errors.New("remove failed"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "nicobistolfi", Provider: "codex"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		IssueNumber:  44,
		IssueTitle:   "force redispatch",
		Status:       state.SessionStatusRunning,
		Branch:       "vigilante/issue-44-force-redispatch",
		WorktreePath: worktreePath,
	}}); err != nil {
		t.Fatal(err)
	}

	err := app.RedispatchSession(context.Background(), "owner/repo", 44, "cli")
	if err == nil || !strings.Contains(err.Error(), "remove failed") {
		t.Fatalf("expected cleanup failure, got: %v", err)
	}
}

func TestRedispatchSessionFailsWhenIssueIsNotEligible(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[]`,
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "nicobistolfi", Provider: "codex"}}); err != nil {
		t.Fatal(err)
	}

	err := app.RedispatchSession(context.Background(), "owner/repo", 44, "cli")
	if err == nil || !strings.Contains(err.Error(), "not open and eligible") {
		t.Fatalf("expected eligibility failure, got: %v", err)
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

func TestResumeSessionCommentsSuccessForLocalCLIRequest(t *testing.T) {
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
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): "done",
			localResumeSuccessCommentCommand("owner/repo", 1, state.Session{Branch: "vigilante/issue-1"}, "issue_execution", "provider_auth"):   "ok",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
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
		Provider:        "codex",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ResumeSession(context.Background(), "owner/repo", 1, "cli"); err != nil {
		t.Fatal(err)
	}
}

func TestResumeSessionCommentsNoopForLocalCLIRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			localResumeNoopCommentCommand("owner/repo", 44): "ok",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions(nil); err != nil {
		t.Fatal(err)
	}

	err := app.ResumeSession(context.Background(), "owner/repo", 44, "cli")
	if err == nil || !strings.Contains(err.Error(), "blocked session not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

func TestResumeSessionCommentsFailureForLocalCLIRequest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	session := state.Session{
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
		Provider:        "codex",
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			localResumeFailureCommentCommand("owner/repo", 1, failedResumeSession(session), "issue_execution"): "ok",
		},
		Errors: map[string]error{
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): errors.New("resume run failed"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{session}); err != nil {
		t.Fatal(err)
	}

	err := app.ResumeSession(context.Background(), "owner/repo", 1, "cli")
	if err == nil || !strings.Contains(err.Error(), "resume run failed") {
		t.Fatalf("expected resume failure, got: %v", err)
	}
}

func TestResumeSessionIgnoresLocalCommentFailure(t *testing.T) {
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
		LookPaths: map[string]string{"codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"codex --version": "codex 1.0.0",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1"): "done",
		},
		Errors: map[string]error{
			localResumeSuccessCommentCommand("owner/repo", 1, state.Session{Branch: "vigilante/issue-1"}, "issue_execution", "provider_auth"): errors.New("comment failed"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
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
		Provider:        "codex",
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ResumeSession(context.Background(), "owner/repo", 1, "cli"); err != nil {
		t.Fatal(err)
	}

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "local resume result comment failed repo=owner/repo issue=1 err=comment failed") {
		t.Fatalf("expected resume comment failure log, got: %s", logData)
	}
}

func TestScanOnceLogsResumeCommentPollingSummaryInsteadOfRawCommand(t *testing.T) {
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
	app.env.Runner = environment.LoggingRunner{
		Base: testutil.FakeRunner{
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
		},
		Logf: app.state.AppendDaemonLog,
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

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "issue comment poll repo=owner/repo issue=1 purpose=resume comments=1") {
		t.Fatalf("expected resume polling summary in daemon log: %s", logText)
	}
	if strings.Contains(logText, "command start dir=\"\" cmd=gh api repos/owner/repo/issues/1/comments") || strings.Contains(logText, "command ok cmd=gh api repos/owner/repo/issues/1/comments") {
		t.Fatalf("expected raw resume comment polling command logs to be suppressed: %s", logText)
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

func TestScanOnceLogsCleanupCommentPollingSummaryInsteadOfRawCommand(t *testing.T) {
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
	app.env.Runner = environment.LoggingRunner{
		Base: testutil.FakeRunner{
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
		},
		Logf: app.state.AppendDaemonLog,
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

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "issue comment poll repo=owner/repo issue=1 purpose=cleanup comments=1") {
		t.Fatalf("expected cleanup polling summary in daemon log: %s", logText)
	}
	if strings.Contains(logText, "command start dir=\"\" cmd=gh api repos/owner/repo/issues/1/comments") || strings.Contains(logText, "command ok cmd=gh api repos/owner/repo/issues/1/comments") {
		t.Fatalf("expected raw cleanup comment polling command logs to be suppressed: %s", logText)
	}
}

func TestScanOnceLogsCommentPollingFailuresWithPurpose(t *testing.T) {
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
	app.env.Runner = environment.LoggingRunner{
		Base: testutil.FakeRunner{
			LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
			Outputs: map[string]string{
				"gh api repos/owner/repo/issues/1": "{}",
				"gh api user --jq .login":          "nicobistolfi\n",
				"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
			},
			Errors: map[string]error{
				"gh api repos/owner/repo/issues/1/comments": errors.New("boom"),
			},
		},
		Logf: app.state.AppendDaemonLog,
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

	logData, err := os.ReadFile(app.state.DaemonLogPath())
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	if !strings.Contains(logText, "issue comment poll failed repo=owner/repo issue=1 purpose=resume err=boom output=<empty>") {
		t.Fatalf("expected comment polling failure summary in daemon log: %s", logText)
	}
	if !strings.Contains(logText, "resume comment lookup failed repo=owner/repo issue=1 err=boom") {
		t.Fatalf("expected higher-level resume failure log in daemon log: %s", logText)
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

func TestScanOnceSkipsClosedSessionsForCleanupCommentPolling(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	// Deliberately omit the comments API endpoint for the closed session.
	// If processGitHubCleanupRequests attempts to poll comments for the
	// closed session, the fake runner will return an error and the test will
	// fail, proving that closed sessions are properly skipped.
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
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
		RepoPath:           "/tmp/repo",
		Repo:               "owner/repo",
		IssueNumber:        1,
		Branch:             "vigilante/issue-1",
		WorktreePath:       filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
		Status:             state.SessionStatusClosed,
		CleanupCompletedAt: "2026-03-19T10:00:00Z",
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
	if sessions[0].Status != state.SessionStatusClosed {
		t.Fatalf("expected closed session to remain closed, got %q", sessions[0].Status)
	}
}

func TestBlockedSessionExceededInactivityTimeoutTreatsUserCommentAsActivity(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": `[{"id":101,"body":"Still blocked on my side.","created_at":"2026-03-12T17:50:00Z","user":{"login":"nicobistolfi"}}]`,
		},
	}

	inactive, err := app.blockedSessionExceededInactivityTimeout(context.Background(), state.Session{
		Repo:         "owner/repo",
		IssueNumber:  1,
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusBlocked,
		UpdatedAt:    old.Format(time.RFC3339),
	}, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if inactive {
		t.Fatal("expected recent user comment to prevent inactivity cleanup")
	}
}

func TestBlockedSessionExceededInactivityTimeoutTreatsRecentSessionUpdateAsActivity(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": "[]",
		},
	}

	inactive, err := app.blockedSessionExceededInactivityTimeout(context.Background(), state.Session{
		Repo:         "owner/repo",
		IssueNumber:  1,
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusBlocked,
		UpdatedAt:    now.Add(-10 * time.Minute).Format(time.RFC3339),
	}, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if inactive {
		t.Fatal("expected recent session update to prevent inactivity cleanup")
	}
}

func TestBlockedSessionExceededInactivityTimeoutTreatsRecentWorktreeChangeAsActivity(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour)
	recent := now.Add(-5 * time.Minute)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}
	worktreeFile := filepath.Join(worktreePath, "note.txt")
	if err := os.WriteFile(worktreeFile, []byte("recent"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(worktreeFile, recent, recent); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": "[]",
		},
	}

	inactive, err := app.blockedSessionExceededInactivityTimeout(context.Background(), state.Session{
		Repo:         "owner/repo",
		IssueNumber:  1,
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusBlocked,
		UpdatedAt:    old.Format(time.RFC3339),
	}, 20*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if inactive {
		t.Fatal("expected recent worktree change to prevent inactivity cleanup")
	}
}

func TestShouldAutoRecoverBlockedSession(t *testing.T) {
	tests := []struct {
		name    string
		session state.Session
		want    bool
	}{
		{
			name: "maintenance dirty worktree",
			session: state.Session{
				BlockedStage:  "pr_maintenance",
				BlockedReason: state.BlockedReason{Kind: "dirty_worktree"},
			},
			want: true,
		},
		{
			name: "conflict resolution dirty worktree detail",
			session: state.Session{
				BlockedStage:  "conflict_resolution",
				BlockedReason: state.BlockedReason{Summary: "worktree is not clean before PR maintenance"},
			},
			want: true,
		},
		{
			name: "maintenance provider auth",
			session: state.Session{
				BlockedStage:  "ci_remediation",
				BlockedReason: state.BlockedReason{Kind: "provider_auth"},
			},
			want: false,
		},
		{
			name: "non maintenance dirty worktree",
			session: state.Session{
				BlockedStage:  "issue_execution",
				BlockedReason: state.BlockedReason{Kind: "dirty_worktree"},
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoRecoverBlockedSession(tc.session); got != tc.want {
				t.Fatalf("unexpected result: got %v want %v", got, tc.want)
			}
		})
	}
}

func TestScanOnceCleansUpBlockedSessionAfterDefaultInactivityTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-45 * time.Minute)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments":                  "[]",
			"gh api repos/owner/repo/issues/1":                           "{}",
			"git worktree prune":                                         "ok",
			"git worktree remove --force " + worktreePath:                "ok",
			"git worktree list --porcelain":                              "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Inactive Blocked Session Cleaned Up",
				Emoji:      "🧹",
				Percent:    100,
				ETAMinutes: 1,
				Items: []string{
					"No qualifying user comments, session updates, or worktree changes were detected for `vigilante/issue-1` longer than `20m0s`.",
					"Vigilante cleaned up the local blocked-session artifacts conservatively.",
					"Next step: the issue is ready for a future redispatch in a fresh worktree.",
				},
				Tagline: "What is left idle grows loud.",
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
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusBlocked,
		BlockedAt:    old.Format(time.RFC3339),
		BlockedStage: "issue_execution",
		BlockedReason: state.BlockedReason{
			Kind:      "provider_auth",
			Operation: "codex exec",
			Summary:   "session expired",
			Detail:    "session expired",
		},
		UpdatedAt: old.Format(time.RFC3339),
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
	if sessions[0].Status != state.SessionStatusFailed || sessions[0].CleanupCompletedAt == "" || sessions[0].LastCleanupSource != "blocked_inactivity_timeout" {
		t.Fatalf("expected blocked session cleanup to complete: %#v", sessions[0])
	}
	if sessions[0].BlockedStage != "" || sessions[0].ResumeRequired {
		t.Fatalf("expected blocked state to be cleared after inactivity cleanup: %#v", sessions[0])
	}
}

func TestScanOnceUsesOverriddenBlockedSessionInactivityTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-30 * time.Minute)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": "[]",
			"gh api repos/owner/repo/issues/1":          "{}",
			"gh api user --jq .login":                   "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveServiceConfig(state.ServiceConfig{BlockedSessionInactivityTimeout: "45m"}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		IssueNumber:  1,
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusBlocked,
		BlockedAt:    old.Format(time.RFC3339),
		BlockedStage: "issue_execution",
		UpdatedAt:    old.Format(time.RFC3339),
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
	if sessions[0].Status != state.SessionStatusBlocked || sessions[0].CleanupCompletedAt != "" {
		t.Fatalf("expected overridden timeout to keep session blocked: %#v", sessions[0])
	}
}

func TestScanOnceLeavesBlockedSessionVisibleWhenInactivityCleanupFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-45 * time.Minute)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments": "[]",
			"gh api repos/owner/repo/issues/1":          "{}",
			"git worktree prune":                        "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Blocked",
				Emoji:      "🛠️",
				Percent:    85,
				ETAMinutes: 10,
				Items: []string{
					"The blocked session on `vigilante/issue-1` exceeded the inactivity timeout of `20m0s`.",
					"Automatic local cleanup failed: `exit status 1`.",
					"Next step: fix the local cleanup problem before redispatching the issue.",
				},
				Tagline: "A knot is patient until you pull it.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
		Errors: map[string]error{
			"git worktree remove --force " + worktreePath: errors.New("exit status 1"),
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
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Branch:       "vigilante/issue-1",
		WorktreePath: worktreePath,
		Status:       state.SessionStatusBlocked,
		BlockedAt:    old.Format(time.RFC3339),
		BlockedStage: "issue_execution",
		UpdatedAt:    old.Format(time.RFC3339),
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
	if sessions[0].Status != state.SessionStatusBlocked || sessions[0].CleanupError == "" || sessions[0].LastCleanupSource != "blocked_inactivity_timeout" {
		t.Fatalf("expected failed inactivity cleanup to leave a visible blocked state: %#v", sessions[0])
	}
}

func TestScanOnceAutoRecoversStaleBlockedMaintenanceSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 3, 12, 18, 0, 0, 0, time.UTC)
	old := now.Add(-15 * time.Minute)
	if err := os.Chtimes(worktreePath, old, old); err != nil {
		t.Fatal(err)
	}

	startComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Auto-Recovery In Progress",
		Emoji:      "♻️",
		Percent:    88,
		ETAMinutes: 8,
		Items: []string{
			"The blocked `pr_maintenance` session on `vigilante/issue-1` stayed inactive longer than `10m0s`.",
			"Vigilante is rebuilding the local worktree from the latest committed state of PR #31 on `vigilante/issue-1`.",
			"Next step: resume maintenance on the existing PR branch without opening a replacement PR.",
		},
		Tagline: "Reset the footing, keep the climb.",
	})
	successComment := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Recovered",
		Emoji:      "🫡",
		Percent:    95,
		ETAMinutes: 3,
		Items: []string{
			"Vigilante auto-recovered the stale `dirty_worktree` block on `vigilante/issue-1` after `10m0s` of inactivity.",
			"The local worktree was rebuilt from the latest committed state of PR #31 on the existing branch `vigilante/issue-1`.",
			"Next step: `pr_maintenance` resumed without creating a replacement PR.",
		},
		Tagline: "Same branch, cleaner footing.",
	})

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }
	app.env.Runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/1/comments":                                                          "[]",
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
			"gh issue comment --repo owner/repo 1 --body " + startComment:                                        "ok",
			"git worktree prune":                                         "ok",
			"git worktree remove --force " + worktreePath:                "ok",
			"git ls-remote --exit-code --heads origin vigilante/issue-1": "abc123\trefs/heads/vigilante/issue-1",
			"git fetch origin vigilante/issue-1:vigilante/issue-1":       "ok",
			"git worktree list --porcelain":                              "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/main\n",
			"git worktree add " + worktreePath + " vigilante/issue-1":    "ok",
			"git fetch origin main":                                      "ok",
			"git status --porcelain":                                     "",
			"git rebase origin/main":                                     "Current branch vigilante/issue-1 is up to date.\n",
			"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": `{"number":31,"title":"Test PR","body":"body","url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null,"labels":[],"isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","statusCheckRollup":[{"context":"test","state":"COMPLETED","conclusion":"SUCCESS"}]}`,
			"gh issue comment --repo owner/repo 1 --body " + successComment:                                                                                          "ok",
			"gh api repos/owner/repo/labels?per_page=100":                                                                                                            `[{"name":"vigilante:running"},{"name":"vigilante:blocked"},{"name":"vigilante:recovering"},{"name":"vigilante:ready-for-review"},{"name":"vigilante:awaiting-user-validation"},{"name":"vigilante:done"},{"name":"vigilante:needs-human-input"},{"name":"vigilante:needs-provider-fix"},{"name":"vigilante:needs-git-fix"},{"name":"vigilante:queued"},{"name":"codex"},{"name":"claude"},{"name":"gemini"},{"name":"vigilante:resume"},{"name":"vigilante:automerge"},{"name":"resume"}]`,
			"gh api repos/owner/repo/issues/1":                                                                                                                       `{"labels":[{"name":"vigilante:blocked"},{"name":"vigilante:needs-git-fix"}]}`,
			"gh issue edit --repo owner/repo 1 --add-label vigilante:recovering --remove-label vigilante:blocked --remove-label vigilante:needs-git-fix":             "ok",
			"gh issue edit --repo owner/repo 1 --add-label vigilante:awaiting-user-validation --remove-label vigilante:recovering":                                   "ok",
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
		RepoPath:          repoPath,
		Repo:              "owner/repo",
		IssueNumber:       1,
		IssueTitle:        "first",
		IssueURL:          "https://github.com/owner/repo/issues/1",
		Branch:            "vigilante/issue-1",
		WorktreePath:      worktreePath,
		Status:            state.SessionStatusBlocked,
		PullRequestNumber: 31,
		BlockedAt:         old.Format(time.RFC3339),
		BlockedStage:      "pr_maintenance",
		BlockedReason: state.BlockedReason{
			Kind:      "dirty_worktree",
			Operation: "git status --porcelain",
			Summary:   "worktree is not clean before PR maintenance",
			Detail:    "worktree is not clean before PR maintenance",
		},
		LastMaintenanceError: "worktree is not clean before PR maintenance",
		UpdatedAt:            old.Format(time.RFC3339),
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
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected session to recover successfully: %#v", sessions[0])
	}
	if sessions[0].RecoveredAt == "" || sessions[0].BlockedStage != "" || sessions[0].BlockedReason.Kind != "" {
		t.Fatalf("expected blocked state to be cleared after auto recovery: %#v", sessions[0])
	}
	if sessions[0].LastResumeSource != autoRecoverySource {
		t.Fatalf("expected auto recovery source to be recorded: %#v", sessions[0])
	}
}

func TestScanOnceSelectsEligibleIssueAndPersistsSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := New()
	app.clock = func() time.Time { return time.Date(2026, 3, 19, 21, 55, 0, 0, time.UTC) }
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	worktreePath := filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1")
	branch := "vigilante/issue-1-first"
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs("/tmp/repo", "main"), map[string]string{
			"gh auth status":          "ok",
			"gh api /rate_limit":      `{"resources":{"core":{"limit":5000,"remaining":150,"reset":1773961151},"rate":{"limit":5000,"remaining":150,"reset":1773961151},"graphql":{"limit":5000,"remaining":4557,"reset":1773961792},"search":{"limit":30,"remaining":30,"reset":1773961093}}}`,
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"to-do"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
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
		}),
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

func TestScanOncePausesWhenGitHubCoreRateLimitIsLowAndCommentsActiveIssues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	now := time.Date(2026, 3, 19, 21, 55, 0, 0, time.UTC)
	resetAt := time.Unix(1773961151, 0).Local()
	app := New()
	app.clock = func() time.Time { return now }
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"gh": "/usr/bin/gh"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
			"gh api /rate_limit": `{"resources":{"core":{"limit":5000,"remaining":4,"reset":1773961151},"rate":{"limit":5000,"remaining":4,"reset":1773961151},"graphql":{"limit":5000,"remaining":4557,"reset":1773961792},"search":{"limit":30,"remaining":30,"reset":1773961093}}}`,
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatGitHubRateLimitDelayComment(ghcli.RateLimitSnapshot{
				Core:    ghcli.RateLimitResource{Limit: 5000, Remaining: 4, ResetAt: resetAt},
				Rate:    ghcli.RateLimitResource{Limit: 5000, Remaining: 4, ResetAt: resetAt},
				GraphQL: ghcli.RateLimitResource{Limit: 5000, Remaining: 4557, ResetAt: time.Unix(1773961792, 0).Local()},
				Search:  ghcli.RateLimitResource{Limit: 30, Remaining: 30, ResetAt: time.Unix(1773961093, 0).Local()},
			}, githubCoreLowQuotaThreshold, now): "ok",
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
		IssueNumber:  7,
		IssueTitle:   "active work",
		IssueURL:     "https://github.com/owner/repo/issues/7",
		Branch:       "vigilante/issue-7",
		WorktreePath: "/tmp/repo/.worktrees/vigilante/issue-7",
		Status:       state.SessionStatusRunning,
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := stdout.String(); !strings.Contains(got, "scan paused: GitHub REST core quota is below the low-quota threshold") {
		t.Fatalf("unexpected stdout: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastGitHubDelayResetAt != resetAt.UTC().Format(time.RFC3339) && sessions[0].LastGitHubDelayResetAt != resetAt.Format(time.RFC3339) {
		t.Fatalf("expected dedupe reset marker, got %#v", sessions[0])
	}
	if sessions[0].ResumeAfter != resetAt.UTC().Format(time.RFC3339) && sessions[0].ResumeAfter != resetAt.Format(time.RFC3339) {
		t.Fatalf("expected explicit resume_after marker, got %#v", sessions[0])
	}
}

func TestScanOnceSuppressesDuplicateGitHubLowQuotaCommentsWithinSameResetWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	now := time.Date(2026, 3, 19, 21, 55, 0, 0, time.UTC)
	resetAt := time.Unix(1773961151, 0).Local()
	app := New()
	app.clock = func() time.Time { return now }
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"gh": "/usr/bin/gh"},
		Outputs: map[string]string{
			"gh api /rate_limit":      `{"resources":{"core":{"limit":5000,"remaining":4,"reset":1773961151},"rate":{"limit":5000,"remaining":4,"reset":1773961151},"graphql":{"limit":5000,"remaining":4557,"reset":1773961792},"search":{"limit":30,"remaining":30,"reset":1773961093}}}`,
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
			"gh issue comment --repo owner/repo 7 --body " + ghcli.FormatGitHubRateLimitDelayComment(ghcli.RateLimitSnapshot{
				Core:    ghcli.RateLimitResource{Limit: 5000, Remaining: 4, ResetAt: resetAt},
				Rate:    ghcli.RateLimitResource{Limit: 5000, Remaining: 4, ResetAt: resetAt},
				GraphQL: ghcli.RateLimitResource{Limit: 5000, Remaining: 4557, ResetAt: time.Unix(1773961792, 0).Local()},
				Search:  ghcli.RateLimitResource{Limit: 30, Remaining: 30, ResetAt: time.Unix(1773961093, 0).Local()},
			}, githubCoreLowQuotaThreshold, now): "ok",
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{{
		RepoPath:               "/tmp/repo",
		Repo:                   "owner/repo",
		IssueNumber:            7,
		IssueTitle:             "active work",
		IssueURL:               "https://github.com/owner/repo/issues/7",
		Branch:                 "vigilante/issue-7",
		WorktreePath:           "/tmp/repo/.worktrees/vigilante/issue-7",
		Status:                 state.SessionStatusRunning,
		LastGitHubDelayResetAt: resetAt.Format(time.RFC3339),
	}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestScanOnceResumesAfterGitHubRateLimitResetWindowPasses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	now := time.Date(2026, 3, 19, 23, 5, 0, 0, time.UTC)
	resetAt := time.Unix(1773961151, 0).Local()
	app := New()
	app.clock = func() time.Time { return now }
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.githubRateLimitState = githubRateLimitState{
		Active:  true,
		ResetAt: resetAt,
		Snapshot: ghcli.RateLimitSnapshot{
			Core: ghcli.RateLimitResource{Limit: 5000, Remaining: 95, ResetAt: resetAt},
		},
	}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"gh": "/usr/bin/gh"},
		Outputs: map[string]string{
			"gh api /rate_limit":      `{"resources":{"core":{"limit":5000,"remaining":150,"reset":1773964751},"rate":{"limit":5000,"remaining":150,"reset":1773964751},"graphql":{"limit":5000,"remaining":4557,"reset":1773961792},"search":{"limit":30,"remaining":30,"reset":1773961093}}}`,
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

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(stdout.String(), "scan paused: GitHub REST core quota is below the low-quota threshold") {
		t.Fatalf("expected scan to resume after reset, got: %s", stdout.String())
	}
	if app.githubRateLimitState.Active {
		t.Fatalf("expected in-memory rate-limit pause to clear after reset")
	}
}

func TestScanOnceClearsStaleGitHubRateLimitPauseWhenLiveQuotaRecovered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	now := time.Date(2026, 3, 21, 21, 51, 38, 0, time.FixedZone("PDT", -7*60*60))
	cachedResetAt := time.Date(2026, 3, 21, 22, 42, 41, 0, time.FixedZone("PDT", -7*60*60))
	liveResetAt := now.Add(52 * time.Minute)
	app := New()
	app.clock = func() time.Time { return now }
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.githubRateLimitState = githubRateLimitState{
		Active:  true,
		ResetAt: cachedResetAt,
		Snapshot: ghcli.RateLimitSnapshot{
			Core: ghcli.RateLimitResource{Limit: 5000, Remaining: 0, ResetAt: cachedResetAt},
		},
	}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"gh": "/usr/bin/gh"},
		Outputs: map[string]string{
			"gh api /rate_limit": fmt.Sprintf(`{"resources":{"core":{"limit":5000,"remaining":4991,"reset":%d},"rate":{"limit":5000,"remaining":4991,"reset":%d},"graphql":{"limit":5000,"remaining":4989,"reset":%d},"search":{"limit":30,"remaining":30,"reset":%d}}}`,
				liveResetAt.Unix(),
				liveResetAt.Unix(),
				liveResetAt.Unix(),
				now.Add(time.Minute).Unix(),
			),
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

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(stdout.String(), "scan paused: GitHub REST core quota is below the low-quota threshold") {
		t.Fatalf("expected live quota recovery to skip pause, got: %s", stdout.String())
	}
	if app.githubRateLimitState.Active {
		t.Fatalf("expected stale in-memory rate-limit pause to clear after live quota recovery")
	}
}

func TestScanOnceClearsExpiredSessionResumeAfterWhenQuotaRecovered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	now := time.Date(2026, 3, 19, 23, 5, 0, 0, time.UTC)
	resetAt := now.Add(-5 * time.Minute)
	app := New()
	app.clock = func() time.Time { return now }
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"gh": "/usr/bin/gh"},
		Outputs: map[string]string{
			"gh api /rate_limit":      `{"resources":{"core":{"limit":5000,"remaining":150,"reset":1773964751},"rate":{"limit":5000,"remaining":150,"reset":1773964751},"graphql":{"limit":5000,"remaining":4557,"reset":1773961792},"search":{"limit":30,"remaining":30,"reset":1773961093}}}`,
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
		IssueNumber:  7,
		IssueTitle:   "active work",
		IssueURL:     "https://github.com/owner/repo/issues/7",
		Branch:       "vigilante/issue-7",
		WorktreePath: "/tmp/repo/.worktrees/vigilante/issue-7",
		Status:       state.SessionStatusRunning,
		ResumeAfter:  resetAt.Format(time.RFC3339),
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
	if sessions[0].ResumeAfter != "" {
		t.Fatalf("expected expired resume_after to clear, got %#v", sessions[0])
	}
}

func TestIsStalledSessionIgnoresUpdatedAtWithoutHeartbeat(t *testing.T) {
	now := time.Date(2026, 3, 19, 23, 5, 0, 0, time.UTC)
	session := state.Session{
		WorktreePath:           t.TempDir(),
		ProcessID:              999999,
		StartedAt:              now.Add(-20 * time.Minute).Format(time.RFC3339),
		UpdatedAt:              now.Add(-1 * time.Minute).Format(time.RFC3339),
		LastGitHubDelayResetAt: now.Add(10 * time.Minute).Format(time.RFC3339),
		ResumeAfter:            now.Add(10 * time.Minute).Format(time.RFC3339),
	}

	stalled, reason := isStalledSession(session, now, 10*time.Minute)
	if !stalled {
		t.Fatalf("expected session to be stale when only updated_at changed: %#v", session)
	}
	if !strings.Contains(reason, "idle since") {
		t.Fatalf("expected idle-since reason, got %q", reason)
	}
}

func TestScanOnceReusesExistingRemoteIssueBranchAndPersistsDiffContext(t *testing.T) {
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
			"git ls-remote --exit-code --heads origin main":                                                                 "abcdef1234567890\trefs/heads/main\n",
			"git worktree prune":                              "ok",
			"git fetch origin " + branch + ":" + branch:       "ok",
			"git worktree add " + worktreePath + " " + branch: "ok",
			"git diff --stat main..." + branch:                "README.md | 2 ++\n 1 file changed, 2 insertions(+)\n",
			sessionStartCommentCommand("owner/repo", 1, worktreePath, state.Session{Branch: branch, BaseBranch: "main", ReusedRemoteBranch: branch, BranchDiffSummary: "README.md | 2 ++\n 1 file changed, 2 insertions(+)"}):                                                                                                                  "ok",
			preflightPromptCommandForSession(worktreePath, "owner/repo", "/tmp/repo", 1, "first", "https://github.com/owner/repo/issues/1", state.Session{WorktreePath: worktreePath, Branch: branch, BaseBranch: "main", ReusedRemoteBranch: branch}):                                                                                         "baseline ok",
			issuePromptCommandForSession(worktreePath, "owner/repo", "/tmp/repo", 1, "first", "https://github.com/owner/repo/issues/1", state.Session{WorktreePath: worktreePath, Branch: branch, BaseBranch: "main", ReusedRemoteBranch: branch, BranchDiffSummary: "README.md | 2 ++\n 1 file changed, 2 insertions(+)", Provider: "codex"}): "done",
		},
		Errors: map[string]error{
			"git ls-remote --exit-code --heads origin " + branch:         nil,
			"git ls-remote --exit-code --heads origin vigilante/issue-1": errors.New("exit status 2"),
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

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	if sessions[0].ReusedRemoteBranch != branch || sessions[0].BaseBranch != "main" {
		t.Fatalf("expected reused remote branch context to persist: %#v", sessions[0])
	}
	if !strings.Contains(sessions[0].BranchDiffSummary, "README.md | 2 ++") {
		t.Fatalf("expected branch diff summary to persist: %#v", sessions[0])
	}
}

func TestScanOnceBlocksIssueWhenReusedRemoteBranchDiffAnalysisFails(t *testing.T) {
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
			"git ls-remote --exit-code --heads origin main":                                                                 "abcdef1234567890\trefs/heads/main\n",
			"git worktree prune":                              "ok",
			"git fetch origin " + branch + ":" + branch:       "ok",
			"git worktree add " + worktreePath + " " + branch: "ok",
			"git worktree list --porcelain":                   "worktree /tmp/repo\nHEAD abcdef\nbranch refs/heads/main\n",
			"git branch -D " + branch:                         "Deleted branch " + branch,
		},
		Errors: map[string]error{
			"git ls-remote --exit-code --heads origin " + branch:         nil,
			"git ls-remote --exit-code --heads origin vigilante/issue-1": errors.New("exit status 2"),
			"git diff --stat main..." + branch:                           errors.New("diff failed"),
			"git show-ref --verify --quiet refs/heads/" + branch:         nil,
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

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != state.SessionStatusBlocked {
		t.Fatalf("expected blocked session after diff analysis failure: %#v", sessions)
	}
	if !strings.Contains(sessions[0].LastError, "analyze reused remote issue branch") {
		t.Fatalf("expected diff analysis failure to be recorded: %#v", sessions[0])
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[{"name":"codex"}]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch + " " + worktreePath + " origin/main":                                                  "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath, state.Session{Branch: branch}):                               "ok",
			issuePromptCommand(worktreePath, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", branch): "done",
		}),
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
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
			"git worktree add -b " + branch2 + " " + worktreePath2 + " origin/main": "ok",
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
		}),
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " origin/main":                                                                "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath1, state.Session{Branch: "vigilante/issue-1-first"}):                                   "ok",
			preflightPromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"): "baseline ok",
			issuePromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):     "done",
		}),
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

func TestScanOnceWithUnlimitedMaxParallelStartsAllEligibleIssues(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath1 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	worktreePath2 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-2")
	worktreePath3 := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-3")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]},{"number":3,"title":"third","createdAt":"2026-03-11T12:00:00Z","url":"https://github.com/owner/repo/issues/3","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " origin/main":                                                                  "ok",
			"git worktree add -b vigilante/issue-2-second " + worktreePath2 + " origin/main":                                                                 "ok",
			"git worktree add -b vigilante/issue-3-third " + worktreePath3 + " origin/main":                                                                  "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath1, state.Session{Branch: "vigilante/issue-1-first"}):                                     "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, state.Session{Branch: "vigilante/issue-2-second"}):                                    "ok",
			sessionStartCommentCommand("owner/repo", 3, worktreePath3, state.Session{Branch: "vigilante/issue-3-third"}):                                     "ok",
			preflightPromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):   "baseline ok",
			preflightPromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"): "baseline ok",
			preflightPromptCommand(worktreePath3, "owner/repo", repoPath, 3, "third", "https://github.com/owner/repo/issues/3", "vigilante/issue-3-third"):   "baseline ok",
			issuePromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):       "done",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"):     "done",
			issuePromptCommand(worktreePath3, "owner/repo", repoPath, 3, "third", "https://github.com/owner/repo/issues/3", "vigilante/issue-3-third"):       "done",
		}),
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", MaxParallel: 0}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath1) || !strings.Contains(got, "repo: owner/repo started issue #2 in "+worktreePath2) || !strings.Contains(got, "repo: owner/repo started issue #3 in "+worktreePath3) {
		t.Fatalf("unexpected output: %s", got)
	}
	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("unexpected sessions: %#v", sessions)
	}
	for _, session := range sessions {
		if session.Status != state.SessionStatusSuccess {
			t.Fatalf("expected successful sessions: %#v", sessions)
		}
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]},{"number":3,"title":"third","createdAt":"2026-03-11T12:00:00Z","url":"https://github.com/owner/repo/issues/3","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " origin/main":                                                                  "ok",
			"git worktree add -b vigilante/issue-2-second " + worktreePath2 + " origin/main":                                                                 "ok",
			sessionStartCommentCommand("owner/repo", 1, worktreePath1, state.Session{Branch: "vigilante/issue-1-first"}):                                     "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, state.Session{Branch: "vigilante/issue-2-second"}):                                    "ok",
			preflightPromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):   "baseline ok",
			preflightPromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"): "baseline ok",
			issuePromptCommand(worktreePath1, "owner/repo", repoPath, 1, "first", "https://github.com/owner/repo/issues/1", "vigilante/issue-1-first"):       "done",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"):     "done",
		}),
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]},{"number":3,"title":"third","createdAt":"2026-03-11T12:00:00Z","url":"https://github.com/owner/repo/issues/3","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-2-second " + worktreePath2 + " origin/main":                                                             "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, state.Session{Branch: "vigilante/issue-2-second"}):                                "ok",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", "vigilante/issue-2-second"): "done",
		}),
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
	blockedSession := blockedIssueSessionForDispatchFailure(
		state.WatchTarget{Path: repoPath, Repo: "owner/repo"},
		ghcli.Issue{Number: 1, Title: "first", URL: "https://github.com/owner/repo/issues/1"},
		"codex",
		errors.New("exit status 1: worktree add failed"),
		time.Date(2026, 3, 13, 20, 0, 0, 0, time.UTC),
	)
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]},{"number":2,"title":"second","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo/issues/2","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch2 + " " + worktreePath2 + " origin/main": "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatDispatchFailureComment(ghcli.DispatchFailureComment{
				Stage:        "dispatch",
				Summary:      dispatchFailureSummary(blockedSession),
				Branch:       blockedSession.Branch,
				WorktreePath: blockedSession.WorktreePath,
				NextStep:     dispatchFailureNextStep(blockedSession, "dispatch"),
			}): "ok",
			sessionStartCommentCommand("owner/repo", 2, worktreePath2, state.Session{Branch: branch2}):                                    "ok",
			preflightPromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", branch2): "baseline ok",
			issuePromptCommand(worktreePath2, "owner/repo", repoPath, 2, "second", "https://github.com/owner/repo/issues/2", branch2):     "done",
		}),
		Errors: map[string]error{
			"git worktree add -b vigilante/issue-1-first " + worktreePath1 + " origin/main": errors.New("exit status 1: worktree add failed"),
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
	if sessions[0].LastDispatchFailureFingerprint == "" || sessions[0].LastDispatchFailureCommentedAt == "" {
		t.Fatalf("expected dispatch failure comment tracking: %#v", sessions[0])
	}
	if sessions[1].IssueNumber != 2 || sessions[1].Status != state.SessionStatusSuccess {
		t.Fatalf("expected second issue to succeed, got: %#v", sessions[1])
	}
}

func TestScanOnceBlocksIssueWhenBaseRefreshFails(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
			"git worktree prune": "ok",
		}),
		Errors: map[string]error{
			"git fetch origin main": errors.New("exit status 1: fetch failed"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	got := stdout.String()
	if !strings.Contains(got, "repo: owner/repo blocked issue #1: exit status 1: fetch failed") {
		t.Fatalf("expected blocked output, got: %s", got)
	}

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Status != state.SessionStatusBlocked || sessions[0].LastError != "exit status 1: fetch failed" {
		t.Fatalf("expected blocked session from base refresh failure: %#v", sessions)
	}
}

func TestScanOnceCommentsOnProviderStartupFailure(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	branch := "vigilante/issue-1-first"
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	expectedSession := state.Session{
		RepoPath:     repoPath,
		Repo:         "owner/repo",
		Provider:     "codex",
		IssueNumber:  1,
		IssueTitle:   "first",
		IssueURL:     "https://github.com/owner/repo/issues/1",
		Branch:       branch,
		WorktreePath: worktreePath,
		Status:       state.SessionStatusFailed,
		LastError:    "codex CLI version 2.0.0 is incompatible with this Vigilante build (supported: >=0.114.0, <2.0.0); install a compatible codex CLI version or use a matching Vigilante build",
	}
	expectedSession.BlockedReason = classifyBlockedReason("issue_execution", "issue startup", errors.New(expectedSession.LastError))
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branch + " " + worktreePath + " origin/main": "ok",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatDispatchFailureComment(ghcli.DispatchFailureComment{
				Stage:        "issue_startup",
				Summary:      dispatchFailureSummary(expectedSession),
				Branch:       expectedSession.Branch,
				WorktreePath: expectedSession.WorktreePath,
				NextStep:     dispatchFailureNextStep(expectedSession, "issue_startup"),
			}): "ok",
			"codex --version": "codex 2.0.0",
		}),
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
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
	if sessions[0].Status != state.SessionStatusFailed {
		t.Fatalf("expected failed session, got: %#v", sessions[0])
	}
	if sessions[0].LastDispatchFailureFingerprint == "" || sessions[0].LastDispatchFailureCommentedAt == "" {
		t.Fatalf("expected startup failure comment tracking: %#v", sessions[0])
	}
}

func TestScanOnceSuppressesDuplicateDispatchFailureComment(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	now := time.Date(2026, 3, 13, 20, 0, 0, 0, time.UTC)
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.clock = func() time.Time { return now }

	session := blockedIssueSessionForDispatchFailure(
		state.WatchTarget{Path: repoPath, Repo: "owner/repo"},
		ghcli.Issue{Number: 1, Title: "first", URL: "https://github.com/owner/repo/issues/1"},
		"codex",
		errors.New("worktree already exists for issue #1"),
		now,
	)
	session.Status = state.SessionStatusFailed
	session.LastDispatchFailureFingerprint = dispatchFailureFingerprint(session, "dispatch")
	session.LastDispatchFailureCommentedAt = now.Format(time.RFC3339)

	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh"},
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPath, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
			"git worktree prune": "ok",
		}),
		Errors: map[string]error{
			"git worktree add -b vigilante/issue-1-first " + filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1") + " origin/main": errors.New("worktree already exists for issue #1"),
		},
	}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.SaveSessions([]state.Session{session}); err != nil {
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
	if sessions[0].LastDispatchFailureCommentedAt != now.Format(time.RFC3339) {
		t.Fatalf("expected duplicate dispatch comment to be suppressed: %#v", sessions[0])
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPathA, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo-a --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first-a","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo-a/issues/1","labels":[]},{"number":2,"title":"second-a","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo-a/issues/2","labels":[]}]`,
			"gh issue list --repo owner/repo-b --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":10,"title":"first-b","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo-b/issues/10","labels":[]},{"number":11,"title":"second-b","createdAt":"2026-03-10T12:00:00Z","url":"https://github.com/owner/repo-b/issues/11","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b vigilante/issue-1-first-a " + worktreeA1 + " origin/main":                                                                           "ok",
			"git worktree add -b vigilante/issue-2-second-a " + worktreeA2 + " origin/main":                                                                          "ok",
			"git worktree add -b vigilante/issue-10-first-b " + worktreeB10 + " origin/main":                                                                         "ok",
			sessionStartCommentCommand("owner/repo-a", 1, worktreeA1, state.Session{Branch: "vigilante/issue-1-first-a"}):                                            "ok",
			sessionStartCommentCommand("owner/repo-a", 2, worktreeA2, state.Session{Branch: "vigilante/issue-2-second-a"}):                                           "ok",
			sessionStartCommentCommand("owner/repo-b", 10, worktreeB10, state.Session{Branch: "vigilante/issue-10-first-b"}):                                         "ok",
			preflightPromptCommand(worktreeA1, "owner/repo-a", repoPathA, 1, "first-a", "https://github.com/owner/repo-a/issues/1", "vigilante/issue-1-first-a"):     "baseline ok",
			preflightPromptCommand(worktreeA2, "owner/repo-a", repoPathA, 2, "second-a", "https://github.com/owner/repo-a/issues/2", "vigilante/issue-2-second-a"):   "baseline ok",
			preflightPromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", "vigilante/issue-10-first-b"): "baseline ok",
			issuePromptCommand(worktreeA1, "owner/repo-a", repoPathA, 1, "first-a", "https://github.com/owner/repo-a/issues/1", "vigilante/issue-1-first-a"):         "done",
			issuePromptCommand(worktreeA2, "owner/repo-a", repoPathA, 2, "second-a", "https://github.com/owner/repo-a/issues/2", "vigilante/issue-2-second-a"):       "done",
			issuePromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", "vigilante/issue-10-first-b"):     "done",
		}),
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(repoPathB, "main"), map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo-b --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":10,"title":"first-b","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo-b/issues/10","labels":[]}]`,
			"git worktree prune": "ok",
			"git worktree add -b " + branchB10 + " " + worktreeB10 + " origin/main":                                                               "ok",
			sessionStartCommentCommand("owner/repo-b", 10, worktreeB10, state.Session{Branch: branchB10}):                                         "ok",
			preflightPromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", branchB10): "baseline ok",
			issuePromptCommand(worktreeB10, "owner/repo-b", repoPathB, 10, "first-b", "https://github.com/owner/repo-b/issues/10", branchB10):     "done",
		}),
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
			"git fetch origin main":  "ok",
			"git status --porcelain": "",
			"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "MERGEABLE", "CLEAN", "APPROVED", "COMPLETED", "SUCCESS"),
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
	if sessions[0].PullRequestHeadBranch != "vigilante/issue-1" || sessions[0].PullRequestBaseBranch != "main" {
		t.Fatalf("expected tracked pull request branches: %#v", sessions[0])
	}
	if sessions[0].PullRequestMergeable != "MERGEABLE" || sessions[0].PullRequestMergeStateStatus != "CLEAN" || sessions[0].PullRequestReviewDecision != "APPROVED" {
		t.Fatalf("expected tracked pull request maintenance details: %#v", sessions[0])
	}
	if sessions[0].PullRequestChecksState != "passing" || sessions[0].PullRequestStatusFingerprint == "" {
		t.Fatalf("expected tracked pull request fingerprint: %#v", sessions[0])
	}
	if sessions[0].LastMaintainedAt == "" {
		t.Fatalf("expected maintenance timestamp: %#v", sessions[0])
	}
	if sessions[0].LastMaintenanceError != "" {
		t.Fatalf("unexpected maintenance error: %#v", sessions[0])
	}
}

func TestScanOnceCleansUpClosedPullRequestSession(t *testing.T) {
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"CLOSED","mergedAt":null}]`,
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
	if sessions[0].PullRequestState != "CLOSED" {
		t.Fatalf("expected closed pull request state to be tracked: %#v", sessions[0])
	}
	if sessions[0].CleanupCompletedAt == "" || sessions[0].CleanupError != "" {
		t.Fatalf("expected successful cleanup for closed pull request: %#v", sessions[0])
	}
	if sessions[0].MonitoringStoppedAt != "" {
		t.Fatalf("expected closed pull request cleanup instead of monitoring stop: %#v", sessions[0])
	}
	if sessions[0].LastCleanupSource != "pull_request_closed" {
		t.Fatalf("expected pull_request_closed cleanup source: %#v", sessions[0])
	}
}

func TestScanOnceCleansUpClosedIssueSession(t *testing.T) {
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
			"gh api repos/owner/repo/issues/1":                           `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"closed","labels":[]}`,
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
	if sessions[0].CleanupCompletedAt == "" || sessions[0].CleanupError != "" {
		t.Fatalf("expected successful cleanup for closed issue: %#v", sessions[0])
	}
	if sessions[0].LastCleanupSource != "issue_closed" {
		t.Fatalf("expected issue_closed cleanup source: %#v", sessions[0])
	}
	if sessions[0].Status != state.SessionStatusClosed {
		t.Fatalf("expected session status to transition to closed after issue closure cleanup, got %q", sessions[0].Status)
	}
}

func TestScanOnceKeepsSuccessStatusForOpenIssue(t *testing.T) {
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": "[]",
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
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected session to remain success while issue is open, got %q", sessions[0].Status)
	}
}

func TestScanOnceDoesNotMarkClosedOnCleanupFailure(t *testing.T) {
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"closed","labels":[]}`,
			"gh api user --jq .login":          "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
		},
		Errors: map[string]error{
			"git worktree prune": errors.New("worktree prune failed"),
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
	if sessions[0].Status == state.SessionStatusClosed {
		t.Fatalf("session must not be marked closed when cleanup fails")
	}
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected session to remain success when cleanup fails, got %q", sessions[0].Status)
	}
}

func TestScanOncePRMergeAloneDoesNotTransitionToClosed(t *testing.T) {
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":10,"url":"https://github.com/owner/repo/pull/10","state":"MERGED","mergedAt":"2026-03-19T10:00:00Z"}]`,
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
		RepoPath:            "/tmp/repo",
		Repo:                "owner/repo",
		IssueNumber:         1,
		Branch:              "vigilante/issue-1",
		WorktreePath:        filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
		Status:              state.SessionStatusSuccess,
		PullRequestNumber:   10,
		PullRequestMergedAt: "2026-03-19T10:00:00Z",
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
	if sessions[0].Status == state.SessionStatusClosed {
		t.Fatalf("PR merge alone must not transition session to closed; issue is still open")
	}
}

func TestScanOnceDoesNotCleanUpOpenIssueSessionWithoutPullRequest(t *testing.T) {
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
			"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": "[]",
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
	if sessions[0].CleanupCompletedAt != "" || sessions[0].CleanupError != "" {
		t.Fatalf("expected open issue session to remain active: %#v", sessions[0])
	}
	if sessions[0].LastCleanupSource != "" {
		t.Fatalf("expected no cleanup source for open issue session: %#v", sessions[0])
	}
}

func TestScanOnceAutoSquashMergesLabeledPullRequestAfterChecksPass(t *testing.T) {
	app, stdout := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("automerge", "MERGEABLE", "CLEAN", "APPROVED", "COMPLETED", "SUCCESS"),
		"gh pr merge --repo owner/repo 31 --squash --delete-branch": "ok",
		"gh api user --jq .login":                                   "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "" {
		t.Fatalf("unexpected maintenance wait state: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (0 open)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceAutoSquashMergesPullRequestWithVigilanteAutomergeLabel(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("vigilante:automerge", "MERGEABLE", "CLEAN", "APPROVED", "COMPLETED", "SUCCESS"),
		"gh pr merge --repo owner/repo 31 --squash --delete-branch": "ok",
		"gh api user --jq .login":                                   "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "" {
		t.Fatalf("unexpected maintenance wait state: %#v", sessions[0])
	}
}

func TestScanOnceAutoSquashMergesWhenIssueHasVigilanteAutomergeLabel(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "MERGEABLE", "CLEAN", "APPROVED", "COMPLETED", "SUCCESS"),
		"gh api repos/owner/repo/issues/1":                          `{"title":"first","body":"Keep the original form state behavior intact.","html_url":"https://github.com/owner/repo/issues/1","labels":[{"name":"vigilante:automerge"}]}`,
		"gh pr merge --repo owner/repo 31 --squash --delete-branch": "ok",
		"gh api user --jq .login":                                   "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "" {
		t.Fatalf("unexpected maintenance wait state: %#v", sessions[0])
	}
}

func TestScanOnceAutomergeWaitsForPendingChecks(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("automerge", "MERGEABLE", "BLOCKED", "APPROVED", "PENDING", ""),
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "automerge waiting for required checks on PR #31" {
		t.Fatalf("expected pending-checks wait state, got: %#v", sessions[0])
	}
}

func TestScanOnceWaitsWhenReplacementCheckRunIsStillInProgress(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": `{"number":31,"title":"Test PR","body":"Test PR body","url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null,"labels":[],"isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"BLOCKED","reviewDecision":"APPROVED","statusCheckRollup":[{"context":"test","state":"COMPLETED","conclusion":"CANCELLED"},{"context":"test","state":"IN_PROGRESS","conclusion":""}]}`,
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected maintenance session to remain active while checks rerun: %#v", sessions[0])
	}
	if sessions[0].LastMaintenanceError != "pr maintenance waiting for required checks on PR #31" {
		t.Fatalf("expected rerunning checks to stay in wait state, got: %#v", sessions[0])
	}
	if sessions[0].BlockedStage != "" || sessions[0].BlockedReason.Kind != "" {
		t.Fatalf("expected no blocked state while replacement checks are running: %#v", sessions[0])
	}
}

func TestScanOnceFailingChecksTriggerCIRemediation(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "MERGEABLE", "BLOCKED", "APPROVED", "COMPLETED", "FAILURE"),
		"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "CI Remediation Started",
			Emoji:      "🛠️",
			Percent:    80,
			ETAMinutes: 12,
			Items: []string{
				"Vigilante detected failing required checks on PR #31 and is launching a focused remediation pass.",
				"Failing checks: test (failure)",
				"Next step: investigate the failure, apply the smallest fix that restores CI, and push to the existing PR branch.",
			},
			Tagline: "Tight loop, targeted repair.",
		}): "ok",
		"codex --version": "codex 0.114.0",
		ciRemediationPromptCommand(filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"), "owner/repo", "/tmp/repo", state.Session{
			IssueNumber:  1,
			IssueTitle:   "first",
			IssueURL:     "https://github.com/owner/repo/issues/1",
			WorktreePath: filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
			Branch:       "vigilante/issue-1",
			Provider:     "codex",
		}, ghcli.PullRequest{Number: 31, URL: "https://github.com/owner/repo/pull/31"}, []ghcli.StatusCheckRoll{{Context: "test", State: "COMPLETED", Conclusion: "FAILURE"}}): "fixed and pushed",
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected maintenance to stay active after remediation dispatch: %#v", sessions[0])
	}
	if sessions[0].LastMaintenanceError != "ci remediation dispatched for PR #31; waiting for updated checks" {
		t.Fatalf("expected remediation wait state, got: %#v", sessions[0])
	}
	if sessions[0].LastCIRemediationFingerprint == "" || sessions[0].LastCIRemediationAttemptedAt == "" {
		t.Fatalf("expected remediation fingerprint tracking, got: %#v", sessions[0])
	}
}

func TestScanOnceRepeatedIdenticalFailingChecksBlockForManualReview(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "MERGEABLE", "BLOCKED", "APPROVED", "COMPLETED", "FAILURE"),
		"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "CI Needs Manual Review",
			Emoji:      "🧱",
			Percent:    94,
			ETAMinutes: 10,
			Items: []string{
				"PR #31 still reports the same failing checks after an automated remediation attempt.",
				"Failing checks: test (failure)",
				"Next step: inspect the branch `vigilante/issue-1`, apply a manual fix, then run `vigilante resume --repo owner/repo --issue 1` or request resume from GitHub.",
			},
			Tagline: "One clean retry is enough to prove the point.",
		}): "ok",
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	sessions[0].LastCIRemediationFingerprint = ciFailureFingerprint(31, []ghcli.StatusCheckRoll{{Context: "test", State: "COMPLETED", Conclusion: "FAILURE"}})
	sessions[0].LastCIRemediationAttemptedAt = "2026-03-17T19:30:00Z"
	if err := app.state.SaveSessions(sessions); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err = app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusBlocked || sessions[0].BlockedStage != "ci_remediation" {
		t.Fatalf("expected manual-review block after repeated failure, got: %#v", sessions[0])
	}
	if sessions[0].ResumeHint != "vigilante resume --repo owner/repo --issue 1" {
		t.Fatalf("unexpected resume hint: %#v", sessions[0])
	}
}

func TestScanOnceAutomergeWaitsForMergeabilityConstraints(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("automerge", "MERGEABLE", "BLOCKED", "REVIEW_REQUIRED", "COMPLETED", "SUCCESS"),
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "automerge waiting for required reviews on PR #31" {
		t.Fatalf("expected mergeability wait state, got: %#v", sessions[0])
	}
}

func TestScanOnceDoesNotAutomergeUnlabeledPullRequest(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "MERGEABLE", "CLEAN", "APPROVED", "COMPLETED", "SUCCESS"),
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "" {
		t.Fatalf("expected unlabeled PR to skip automerge cleanly, got: %#v", sessions[0])
	}
}

func newPullRequestMaintenanceTestApp(t *testing.T, outputs map[string]string) (*App, *bytes.Buffer) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	var stdout bytes.Buffer
	app := New()
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	outputs = mergeStringMaps(map[string]string{
		"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Issue body","html_url":"https://github.com/owner/repo/issues/1","state":"open","labels":[]}`,
	}, outputs)
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs:   outputs,
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

	return app, &stdout
}

func automergePRDetailsJSON(label string, mergeable string, mergeState string, reviewDecision string, checkState string, conclusion string) string {
	labelJSON := "[]"
	if label != "" {
		labelJSON = fmt.Sprintf(`[{"name":"%s"}]`, label)
	}
	return fmt.Sprintf(`{"number":31,"title":"Test PR","body":"Test PR body","url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null,"labels":%s,"isDraft":false,"mergeable":"%s","mergeStateStatus":"%s","reviewDecision":"%s","statusCheckRollup":[{"context":"test","state":"%s","conclusion":"%s"}]}`, labelJSON, mergeable, mergeState, reviewDecision, checkState, conclusion)
}

func TestRequiredChecksState(t *testing.T) {
	tests := []struct {
		name   string
		checks []ghcli.StatusCheckRoll
		want   string
	}{
		{
			name: "no checks defaults passing",
			want: "passing",
		},
		{
			name: "queued checks are pending",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "QUEUED"},
			},
			want: "pending",
		},
		{
			name: "pending checks are pending",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "PENDING"},
			},
			want: "pending",
		},
		{
			name: "in progress checks are pending",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "IN_PROGRESS"},
			},
			want: "pending",
		},
		{
			name: "successful completed checks pass",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "COMPLETED", Conclusion: "SUCCESS"},
			},
			want: "passing",
		},
		{
			name: "failed completed checks fail",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "COMPLETED", Conclusion: "FAILURE"},
			},
			want: "failing",
		},
		{
			name: "cancelled completed checks fail when nothing is still running",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "COMPLETED", Conclusion: "CANCELLED"},
			},
			want: "failing",
		},
		{
			name: "timed out completed checks fail when nothing is still running",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "COMPLETED", Conclusion: "TIMED_OUT"},
			},
			want: "failing",
		},
		{
			name: "action required completed checks fail when nothing is still running",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "COMPLETED", Conclusion: "ACTION_REQUIRED"},
			},
			want: "failing",
		},
		{
			name: "active rerun takes precedence over cancelled prior attempt",
			checks: []ghcli.StatusCheckRoll{
				{Context: "test", State: "COMPLETED", Conclusion: "CANCELLED"},
				{Context: "test", State: "IN_PROGRESS"},
			},
			want: "pending",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requiredChecksState(tt.checks); got != tt.want {
				t.Fatalf("requiredChecksState() = %q, want %q", got, tt.want)
			}
		})
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

func TestScanOnceMarksStaleSessionPendingAutoRestart(t *testing.T) {
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
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": "[]",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
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
	if sessions[0].Status != state.SessionStatusRunning {
		t.Fatalf("expected running session to stay pending, got: %#v", sessions[0])
	}
	if sessions[0].StaleAutoRestartPendingSince != now.Format(time.RFC3339) {
		t.Fatalf("expected pending timestamp to be recorded, got: %#v", sessions[0])
	}
	if sessions[0].StaleAutoRestartAttempts != 0 {
		t.Fatalf("expected no restart attempts yet, got: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (1 open)") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceAutoRestartsStaleSessionAfterDelay(t *testing.T) {
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
		Outputs: mergeStringMaps(freshBaseBranchOutputs(filepath.Join(home, "repo"), "main"), map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": "[]",
			"git worktree prune":                                         "ok",
			"git worktree list --porcelain":                              "worktree /tmp/repo\nHEAD abcdef\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/vigilante/issue-1": "ok",
			"git branch -D vigilante/issue-1":                            "Deleted branch vigilante/issue-1\n",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Implementation In Progress",
				Emoji:      "♻️",
				Percent:    25,
				ETAMinutes: 15,
				Items: []string{
					"The previous local session on `vigilante/issue-1` stalled (worktree path is missing).",
					"Vigilante cleaned up the abandoned worktree and started automatic restart attempt 1/3.",
					"Next step: launch a fresh implementation session in a new worktree now.",
				},
				Tagline: "Try again, but keep count.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
			"git worktree add -b " + branch + " " + worktreePath + " origin/main":                                           "ok",
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
		}),
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
		RepoPath:                     filepath.Join(home, "repo"),
		Repo:                         "owner/repo",
		IssueNumber:                  1,
		IssueTitle:                   "first",
		IssueURL:                     "https://github.com/owner/repo/issues/1",
		Branch:                       "vigilante/issue-1",
		WorktreePath:                 worktreePath,
		Status:                       state.SessionStatusRunning,
		ProcessID:                    999999,
		StartedAt:                    now.Add(-20 * time.Minute).Format(time.RFC3339),
		LastHeartbeatAt:              now.Add(-20 * time.Minute).Format(time.RFC3339),
		UpdatedAt:                    now.Add(-20 * time.Minute).Format(time.RFC3339),
		StaleAutoRestartPendingSince: now.Add(-20 * time.Minute).Format(time.RFC3339),
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
	if sessions[0].StaleAutoRestartAttempts != 1 {
		t.Fatalf("expected one persisted auto-restart attempt, got: %#v", sessions[0])
	}
	if sessions[0].StaleAutoRestartPendingSince != "" {
		t.Fatalf("expected pending timestamp to clear after restart, got: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo started issue #1 in "+worktreePath) {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestScanOnceStopsAutoRestartAfterAttemptLimit(t *testing.T) {
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
			"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": "[]",
			"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Manual Intervention Required",
				Emoji:      "🧯",
				Percent:    85,
				ETAMinutes: 5,
				Items: []string{
					"The local session on `vigilante/issue-1` is still stale (worktree path is missing).",
					"Vigilante already used all 3 automatic stale-session restart attempts for this issue.",
					"Next step: inspect the local state and run `vigilante redispatch --repo owner/repo --issue 1` when it is safe to try again.",
				},
				Tagline: "No loops without consent.",
			}): "ok",
			"gh api user --jq .login": "nicobistolfi\n",
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":1,"title":"first","createdAt":"2026-03-09T12:00:00Z","url":"https://github.com/owner/repo/issues/1","labels":[]}]`,
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
		RepoPath:                     filepath.Join(home, "repo"),
		Repo:                         "owner/repo",
		IssueNumber:                  1,
		IssueTitle:                   "first",
		IssueURL:                     "https://github.com/owner/repo/issues/1",
		Branch:                       "vigilante/issue-1",
		WorktreePath:                 worktreePath,
		Status:                       state.SessionStatusRunning,
		ProcessID:                    999999,
		StartedAt:                    now.Add(-20 * time.Minute).Format(time.RFC3339),
		LastHeartbeatAt:              now.Add(-20 * time.Minute).Format(time.RFC3339),
		UpdatedAt:                    now.Add(-20 * time.Minute).Format(time.RFC3339),
		StaleAutoRestartAttempts:     3,
		StaleAutoRestartPendingSince: now.Add(-20 * time.Minute).Format(time.RFC3339),
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
	if sessions[0].Status != state.SessionStatusFailed {
		t.Fatalf("expected failed terminal session after limit, got: %#v", sessions[0])
	}
	if sessions[0].StaleAutoRestartStoppedAt == "" {
		t.Fatalf("expected stop marker after limit, got: %#v", sessions[0])
	}
	if sessions[0].StaleAutoRestartPendingSince != "" {
		t.Fatalf("expected pending timestamp to clear after limit, got: %#v", sessions[0])
	}
	if got := stdout.String(); !strings.Contains(got, "repo: owner/repo no eligible issues (1 open)") {
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
			"git fetch origin main":  "ok",
			"git status --porcelain": "",
			"git rebase origin/main": "Current branch vigilante/issue-1 is up to date.\n",
			"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "MERGEABLE", "CLEAN", "APPROVED", "COMPLETED", "SUCCESS"),
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

func TestScanOnceRoutesDirtyPullRequestToConflictResolution(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "CONFLICTING", "DIRTY", "APPROVED", "COMPLETED", "SUCCESS"),
		"gh api repos/owner/repo/issues/1": `{"title":"first","body":"Keep the original form state behavior intact.","html_url":"https://github.com/owner/repo/issues/1","labels":[]}`,
		"gh issue comment --repo owner/repo 1 --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Conflict Resolution Started",
			Emoji:      "⚔️",
			Percent:    78,
			ETAMinutes: 12,
			Items: []string{
				"Vigilante routed PR #31 into the dedicated conflict-resolution workflow.",
				"GitHub state: mergeable=CONFLICTING, mergeStateStatus=DIRTY. Base branch: `origin/main`.",
				"Next step: resolve the rebase commit by commit while preserving the original issue specification and branch intent.",
			},
			Tagline: "Keep the spec intact while the history moves.",
		}): "ok",
		"codex --version": "codex 0.114.0",
		conflictResolutionPromptCommand(filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"), "owner/repo", "/tmp/repo", state.Session{
			IssueNumber:  1,
			IssueTitle:   "first",
			IssueBody:    "Keep the original form state behavior intact.",
			IssueURL:     "https://github.com/owner/repo/issues/1",
			BaseBranch:   "main",
			WorktreePath: filepath.Join("/tmp/repo", ".worktrees", "vigilante", "issue-1"),
			Branch:       "vigilante/issue-1",
			Provider:     "codex",
		}, ghcli.PullRequest{Number: 31, Title: "Test PR", Body: "Test PR body", URL: "https://github.com/owner/repo/pull/31", Mergeable: "CONFLICTING", MergeStateStatus: "DIRTY"}): "resolved and pushed",
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].LastMaintenanceError != "conflict resolution dispatched for PR #31; waiting for updated branch state" {
		t.Fatalf("expected conflict-resolution wait state, got: %#v", sessions[0])
	}
	if sessions[0].LastMaintainedAt == "" {
		t.Fatalf("expected maintenance timestamp after conflict dispatch: %#v", sessions[0])
	}
	if sessions[0].PullRequestStatusFingerprint == "" || sessions[0].PullRequestMergeable != "CONFLICTING" || sessions[0].PullRequestMergeStateStatus != "DIRTY" {
		t.Fatalf("expected tracked conflict fingerprint: %#v", sessions[0])
	}
}

func TestScanOnceSkipsDuplicateConflictResolutionDispatchWhenPRFingerprintIsUnchanged(t *testing.T) {
	app, _ := newPullRequestMaintenanceTestApp(t, map[string]string{
		"gh pr list --repo owner/repo --head vigilante/issue-1 --state all --json number,url,state,mergedAt": `[{"number":31,"url":"https://github.com/owner/repo/pull/31","state":"OPEN","mergedAt":null}]`,
		"git fetch origin main":  "ok",
		"git status --porcelain": "",
		"gh pr view --repo owner/repo 31 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup": automergePRDetailsJSON("", "CONFLICTING", "DIRTY", "APPROVED", "COMPLETED", "SUCCESS"),
		"gh api user --jq .login": "nicobistolfi\n",
		"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": "[]",
	})

	sessions, err := app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	sessions[0].BaseBranch = "main"
	updatePullRequestMaintenanceSnapshot(&sessions[0], ghcli.PullRequest{
		Number:            31,
		Title:             "Test PR",
		Body:              "Test PR body",
		URL:               "https://github.com/owner/repo/pull/31",
		State:             "OPEN",
		Mergeable:         "CONFLICTING",
		MergeStateStatus:  "DIRTY",
		ReviewDecision:    "APPROVED",
		StatusCheckRollup: []ghcli.StatusCheckRoll{{Context: "test", State: "COMPLETED", Conclusion: "SUCCESS"}},
	})
	sessions[0].LastMaintenanceError = "conflict resolution dispatched for PR #31; waiting for updated branch state"
	if err := app.state.SaveSessions(sessions); err != nil {
		t.Fatal(err)
	}

	if err := app.ScanOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	app.waitForSessions()

	sessions, err = app.state.LoadSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusSuccess {
		t.Fatalf("expected maintenance session to stay active, got: %#v", sessions[0])
	}
	if sessions[0].LastMaintenanceError != "conflict resolution dispatched for PR #31; waiting for updated branch state" {
		t.Fatalf("expected prior conflict-resolution wait state to be preserved, got: %#v", sessions[0])
	}
	if sessions[0].PullRequestStatusFingerprint == "" {
		t.Fatalf("expected persisted fingerprint after duplicate-scan suppression: %#v", sessions[0])
	}
}

func sessionStartCommentCommand(repo string, issueNumber int, worktreePath string, session state.Session) string {
	items := []string{
		"Vigilante launched this implementation session in `" + worktreePath + "`.",
		"Branch: `" + session.Branch + "`.",
		"Current stage: handing the issue off to the configured coding agent (`Codex`) for investigation and implementation.",
	}
	if session.ReusedRemoteBranch != "" {
		base := session.BaseBranch
		if base == "" {
			base = "main"
		}
		items = []string{
			"Vigilante launched this implementation session in `" + worktreePath + "` from existing remote branch `origin/" + session.ReusedRemoteBranch + "`.",
			"Diff summary against `" + base + "`: " + session.BranchDiffSummary,
			"Current stage: handing the issue off to the configured coding agent (`Codex`) to continue the existing implementation.",
		}
	}
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Vigilante Session Start",
		Emoji:      "🧢",
		Percent:    20,
		ETAMinutes: 25,
		Items:      items,
		Tagline:    "Make it simple, but significant.",
	})
}

func localCleanupCommentCommand(repo string, issueNumber int, session state.Session) string {
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + localCleanupResultComment(session)
}

func localCleanupNoopCommentCommand(repo string, issueNumber int) string {
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + localCleanupNoopComment()
}

func localResumeSuccessCommentCommand(repo string, issueNumber int, session state.Session, previousStage string, previousKind string) string {
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + localResumeSuccessComment(session, previousStage, previousKind)
}

func localResumeFailureCommentCommand(repo string, issueNumber int, session state.Session, previousStage string) string {
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + localResumeFailureComment(session, previousStage)
}

func localResumeNoopCommentCommand(repo string, issueNumber int) string {
	return "gh issue comment --repo " + repo + " " + fmt.Sprintf("%d", issueNumber) + " --body " + localResumeNoopComment()
}

func failedResumeSession(session state.Session) state.Session {
	session.Status = state.SessionStatusBlocked
	session.LastResumeSource = "cli"
	session.LastError = "resume run failed"
	return session
}

func freshBaseBranchOutputs(repoPath string, branch string) map[string]string {
	return map[string]string{
		"git ls-remote --exit-code --heads origin " + branch: "abcdef1234567890\trefs/heads/" + branch + "\n",
		"git fetch origin " + branch:                         "ok",
		"git worktree list --porcelain":                      "worktree " + repoPath + "\nHEAD abcdef\nbranch refs/heads/" + branch + "\n",
		"git status --porcelain --untracked-files=no":        "",
		"git merge --ff-only origin/" + branch:               "Already up to date.\n",
	}
}

func mergeStringMaps(maps ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, current := range maps {
		for key, value := range current {
			merged[key] = value
		}
	}
	return merged
}

func issuePromptCommand(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, branch string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		state.Session{WorktreePath: worktreePath, Branch: branch, Provider: "codex"},
	))
}

func issuePromptCommandForSession(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, session state.Session) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		session,
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
	return preflightPromptCommandForSession(worktreePath, repo, repoPath, issueNumber, title, issueURL, state.Session{WorktreePath: worktreePath, Branch: branch})
}

func preflightPromptCommandForSession(worktreePath string, repo string, repoPath string, issueNumber int, title string, issueURL string, session state.Session) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildIssuePreflightPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		ghcli.Issue{Number: issueNumber, Title: title, URL: issueURL},
		session,
	))
}

func resumeDiagnosticSummaryCommand(worktreePath string, session state.Session, previousStage string) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", buildResumeFailureSummaryPrompt(session, previousStage))
}

func resumeDiagnosticSummaryCommandForProvider(worktreePath string, providerID string, session state.Session, previousStage string) string {
	switch providerID {
	case "claude":
		return testutil.Key("claude", "--print", "--dangerously-skip-permissions", buildResumeFailureSummaryPrompt(session, previousStage))
	case "gemini":
		return testutil.Key("gemini", "--prompt", buildResumeFailureSummaryPrompt(session, previousStage), "--yolo")
	default:
		return resumeDiagnosticSummaryCommand(worktreePath, session, previousStage)
	}
}

func ciRemediationPromptCommand(worktreePath string, repo string, repoPath string, session state.Session, pr ghcli.PullRequest, checks []ghcli.StatusCheckRoll) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildCIRemediationPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo},
		session,
		pr,
		checks,
	))
}

func conflictResolutionPromptCommand(worktreePath string, repo string, repoPath string, session state.Session, pr ghcli.PullRequest) string {
	return testutil.Key("codex", "exec", "--cd", worktreePath, "--dangerously-bypass-approvals-and-sandbox", skill.BuildConflictResolutionPrompt(
		state.WatchTarget{Path: repoPath, Repo: repo, Branch: session.BaseBranch},
		session,
		pr,
	))
}

func TestLogsCommandListsFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "vigilante.log"), []byte("daemon log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "owner-repo-issue-42.log"), []byte("session log content"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"logs"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	output := stdout.String()
	if !strings.Contains(output, "owner-repo-issue-42.log") {
		t.Fatalf("expected output to list session log, got %q", output)
	}
	if !strings.Contains(output, "vigilante.log") {
		t.Fatalf("expected output to list daemon log, got %q", output)
	}
}

func TestLogsCommandShowsSessionLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logContent := "[2026-03-24T10:00:00-07:00] session started issue=42 provider=claude\n"
	if err := os.WriteFile(filepath.Join(logsDir, "owner-repo-issue-42.log"), []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}

	exitCode := app.Run(context.Background(), []string{"logs", "--repo", "owner/repo", "--issue", "42"})
	if exitCode != 0 {
		t.Fatalf("expected success exit code, got %d", exitCode)
	}
	if stdout.String() != logContent {
		t.Fatalf("expected session log content %q, got %q", logContent, stdout.String())
	}
}

func TestLogsCommandMissingLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	logsDir := filepath.Join(home, ".vigilante", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	app := New()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app.stdout = &stdout
	app.stderr = &stderr

	exitCode := app.Run(context.Background(), []string{"logs", "--repo", "owner/repo", "--issue", "999"})
	if exitCode != 1 {
		t.Fatalf("expected failure exit code, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "no log found for owner/repo#999") {
		t.Fatalf("expected error message about missing log, got %q", stderr.String())
	}
}
