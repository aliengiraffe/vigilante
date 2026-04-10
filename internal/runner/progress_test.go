package runner

import (
	"context"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestEvaluateSessionProgressWithPR(t *testing.T) {
	session := state.Session{
		PullRequestNumber: 42,
		WorktreePath:      "/tmp/worktree",
		Branch:            "vigilante/issue-1",
		BaseBranch:        "main",
	}
	signal := EvaluateSessionProgress(context.Background(), testutil.FakeRunner{}, session)
	if !signal.HasPullRequest {
		t.Fatal("expected HasPullRequest to be true")
	}
}

func TestEvaluateSessionProgressNewCommitsNoPR(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git rev-list --count origin/main..HEAD": "3",
			"git status --porcelain":                 "",
		},
	}
	session := state.Session{
		WorktreePath: "/tmp/worktree",
		Branch:       "vigilante/issue-1",
		BaseBranch:   "main",
	}
	signal := EvaluateSessionProgress(context.Background(), runner, session)
	if signal.HasPullRequest {
		t.Fatal("expected HasPullRequest to be false")
	}
	if !signal.HasNewCommits {
		t.Fatal("expected HasNewCommits to be true")
	}
	if signal.HasWorktreeChanges {
		t.Fatal("expected HasWorktreeChanges to be false")
	}
}

func TestEvaluateSessionProgressUncommittedChanges(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git rev-list --count origin/main..HEAD": "0",
			"git status --porcelain":                 " M file.go\n",
		},
	}
	session := state.Session{
		WorktreePath: "/tmp/worktree",
		Branch:       "vigilante/issue-1",
		BaseBranch:   "main",
	}
	signal := EvaluateSessionProgress(context.Background(), runner, session)
	if signal.HasPullRequest {
		t.Fatal("expected HasPullRequest to be false")
	}
	if signal.HasNewCommits {
		t.Fatal("expected HasNewCommits to be false")
	}
	if !signal.HasWorktreeChanges {
		t.Fatal("expected HasWorktreeChanges to be true")
	}
}

func TestEvaluateSessionProgressNoDurableProgress(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git rev-list --count origin/main..HEAD": "0",
			"git status --porcelain":                 "",
		},
	}
	session := state.Session{
		WorktreePath: "/tmp/worktree",
		Branch:       "vigilante/issue-1",
		BaseBranch:   "main",
	}
	signal := EvaluateSessionProgress(context.Background(), runner, session)
	if signal.HasPullRequest || signal.HasNewCommits || signal.HasWorktreeChanges {
		t.Fatal("expected no progress signals")
	}
}

func TestClassifyIncompleteReason(t *testing.T) {
	tests := []struct {
		name   string
		signal ProgressSignal
		want   string
	}{
		{
			name:   "commits without PR",
			signal: ProgressSignal{HasNewCommits: true},
			want:   "commits_without_pr",
		},
		{
			name:   "uncommitted changes only",
			signal: ProgressSignal{HasWorktreeChanges: true},
			want:   "uncommitted_changes",
		},
		{
			name:   "commits and uncommitted changes",
			signal: ProgressSignal{HasNewCommits: true, HasWorktreeChanges: true},
			want:   "commits_without_pr",
		},
		{
			name:   "no durable progress",
			signal: ProgressSignal{},
			want:   "no_durable_progress",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyIncompleteReason(tc.signal)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsRerunEligible(t *testing.T) {
	tests := []struct {
		name    string
		session state.Session
		want    bool
	}{
		{
			name:    "incomplete with commits",
			session: state.Session{Status: state.SessionStatusIncomplete, IncompleteReason: "commits_without_pr"},
			want:    true,
		},
		{
			name:    "incomplete with uncommitted changes",
			session: state.Session{Status: state.SessionStatusIncomplete, IncompleteReason: "uncommitted_changes"},
			want:    true,
		},
		{
			name:    "incomplete no progress",
			session: state.Session{Status: state.SessionStatusIncomplete, IncompleteReason: "no_durable_progress"},
			want:    true,
		},
		{
			name:    "success status",
			session: state.Session{Status: state.SessionStatusSuccess},
			want:    false,
		},
		{
			name:    "blocked status",
			session: state.Session{Status: state.SessionStatusBlocked},
			want:    false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsRerunEligible(tc.session)
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
