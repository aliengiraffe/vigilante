package runner

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestSessionPullRequestTrackingHelpers(t *testing.T) {
	for _, tc := range []struct {
		session state.Session
		want    string
	}{
		{state.Session{}, ""}, {state.Session{Branch: " branch "}, "branch"},
		{state.Session{Branch: "branch", ForkOwner: "alice", PushRemote: "fork"}, "alice:branch"},
		{state.Session{Branch: "branch", ForkOwner: "alice", PushRemote: "origin"}, "branch"},
	} {
		if got := sessionPullRequestHeadSelector(tc.session); got != tc.want {
			t.Errorf("selector=%q want %q", got, tc.want)
		}
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pr := backend.PullRequest{Number: 42, URL: " url ", State: " OPEN ", BaseRefName: " main ", MergedAt: &now}
	var session state.Session
	session.Branch = "branch"
	updateSessionPullRequestTracking(&session, pr)
	if session.PullRequestNumber != 42 || session.PullRequestURL != "url" || session.PullRequestState != "OPEN" || session.PullRequestBaseBranch != "main" || session.PullRequestMergedAt != now.Format(time.RFC3339) {
		t.Fatalf("session=%#v", session)
	}
	pr.MergedAt = nil
	updateSessionPullRequestTracking(&session, pr)
	if session.PullRequestMergedAt != "" {
		t.Fatalf("mergedAt=%q", session.PullRequestMergedAt)
	}
	for _, tc := range []struct {
		pr   backend.PullRequest
		want bool
	}{{backend.PullRequest{State: "open"}, true}, {backend.PullRequest{State: "closed", MergedAt: &now}, true}, {backend.PullRequest{State: "closed"}, false}} {
		if got := pullRequestCountsAsCompletedImplementation(tc.pr); got != tc.want {
			t.Errorf("counts=%v want %v", got, tc.want)
		}
	}
}

func TestProviderInvocationExecutionHelpers(t *testing.T) {
	target := state.WatchTarget{Path: "/host/repo"}
	session := state.Session{WorktreePath: "/host/worktree"}
	invocation := provider.Invocation{Dir: "/host/worktree", Name: "codex", Args: []string{"exec", "--cd", "/host/worktree", "prompt /host/repo"}}
	if got := providerInvocationForExecution(target, session, invocation); !reflect.DeepEqual(got, invocation) {
		t.Fatalf("host invocation=%#v", got)
	}
	session.SandboxMode = true
	session.SandboxContainerName = "sandbox-1"
	want := provider.Invocation{Name: "docker", Args: []string{"exec", "-w", "/workspace", "sandbox-1", "codex", "exec", "--cd", "/workspace", "prompt /workspace"}}
	if got := providerInvocationForExecution(target, session, invocation); !reflect.DeepEqual(got, want) {
		t.Fatalf("sandbox invocation=%#v want %#v", got, want)
	}
	runner := testutil.FakeRunner{Outputs: map[string]string{"codex exec --cd /host/worktree prompt /host/repo": "ok"}}
	session.SandboxMode = false
	if got, err := runProviderInvocation(context.Background(), runner, target, session, invocation); err != nil || got != "ok" {
		t.Fatalf("run=%q err=%v", got, err)
	}
}

func TestFallbackSessionText(t *testing.T) {
	if fallbackSessionText(" ", "fallback") != "fallback" || fallbackSessionText(" value ", "fallback") != " value " {
		t.Fatal("fallback boundaries")
	}
}

func TestMaintenanceSessionsSuccessAndProviderFailures(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	session := state.Session{Repo: "owner/repo", RepoPath: "/tmp/repo", IssueNumber: 7, WorktreePath: "/tmp/worktree", Branch: "branch", Provider: "codex"}
	pr := ghcli.PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17"}
	checks := []ghcli.StatusCheckRoll{{Context: "test", Conclusion: "FAILURE"}}
	p, _ := provider.Resolve(provider.CodexID)
	conflict, _ := p.BuildConflictResolutionInvocation(provider.ConflictTask{Target: target, Session: session, PR: pr})
	ci, _ := p.BuildCIRemediationInvocation(provider.CIRemediationTask{Target: target, Session: session, PR: pr, FailingChecks: checks})
	r := testutil.FakeRunner{Outputs: map[string]string{"codex --version": "codex 0.114.0", testutil.Key(conflict.Name, conflict.Args...): "resolved", testutil.Key(ci.Name, ci.Args...): "fixed"}}
	env := &environment.Environment{Runner: r}
	if err := RunConflictResolutionSession(context.Background(), env, store, nil, target, session, pr); err != nil {
		t.Fatal(err)
	}
	if err := RunCIRemediationSession(context.Background(), env, store, nil, target, session, pr, checks); err != nil {
		t.Fatal(err)
	}
	invalid := session
	invalid.Provider = "unknown"
	if err := RunConflictResolutionSession(context.Background(), env, store, nil, target, invalid, pr); err == nil {
		t.Fatal("expected conflict provider error")
	}
	if err := RunCIRemediationSession(context.Background(), env, store, nil, target, invalid, pr, checks); err == nil {
		t.Fatal("expected CI provider error")
	}
	badEnv := &environment.Environment{Runner: testutil.FakeRunner{Outputs: map[string]string{"codex --version": "codex 9.0.0"}}}
	if err := RunConflictResolutionSession(context.Background(), badEnv, store, nil, target, session, pr); err == nil {
		t.Fatal("expected conflict compatibility error")
	}
	if err := RunCIRemediationSession(context.Background(), badEnv, store, nil, target, session, pr, checks); err == nil {
		t.Fatal("expected CI compatibility error")
	}
}
