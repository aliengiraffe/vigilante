package app

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
)

// TestMain runs the package with comment commands enabled so the existing
// command-handling tests keep exercising that path while it is switched off in
// production. Tests for the disabled behavior use disableCommentCommands.
func TestMain(m *testing.M) {
	commentCommandsEnabled = true
	os.Exit(m.Run())
}

func disableCommentCommands(t *testing.T) {
	t.Helper()
	commentCommandsEnabled = false
	t.Cleanup(func() { commentCommandsEnabled = true })
}

func TestDisabledCommentCommandsSkipCommentReads(t *testing.T) {
	disableCommentCommands(t)
	now := time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)
	app, base := pollingTestApp(t, &now)
	targets := []state.WatchTarget{{Repo: "owner/repo", Path: t.TempDir(), Branch: "main", BranchMode: state.BranchModePinned, Assignee: "alice"}}
	sessions := []state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusFailed, EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{Repo: "owner/repo", IssueNumber: 2, Status: state.SessionStatusFailed, PullRequestNumber: 20, PullRequestState: "OPEN", EndedAt: now.Add(-time.Hour).Format(time.RFC3339)},
	}
	stamp := now.Add(-30 * time.Second).Format(time.RFC3339)
	base.repoComments = fmt.Sprintf(`[{"id":101,"body":"@vigilanteai cleanup","created_at":%[1]q,"updated_at":%[1]q,"user":{"login":"alice"},"issue_url":"https://api.github.com/repos/owner/repo/issues/1"}]`, stamp)
	base.comments = fmt.Sprintf(`[{"id":102,"body":"@vigilanteai recreate","created_at":%[1]q,"updated_at":%[1]q,"user":{"login":"alice"}}]`, stamp)

	ctx, finish := app.prepareCommentPolling(context.Background(), sessions)
	app.runCommentSweeps(ctx, targets, sessions)
	var err error
	sessions, err = app.processGitHubCleanupRequests(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err = app.processGitHubRecreateRequests(ctx, sessions)
	if err != nil {
		t.Fatal(err)
	}
	finish(sessions)

	if n := base.countCalls("comments"); n != 0 {
		t.Fatalf("disabled comment commands still read comments %d times", n)
	}
	if n := base.countCalls("reactions"); n != 0 {
		t.Fatalf("disabled comment commands still reacted %d times", n)
	}
	if targets[0].CommentSweep != nil {
		t.Fatalf("disabled sweep must not advance the cursor: %+v", targets[0].CommentSweep)
	}
	for _, s := range sessions {
		if s.LastCleanupCommentID != 0 || s.LastRecreateCommentID != 0 {
			t.Fatalf("issue %d: command applied while comment commands are disabled", s.IssueNumber)
		}
	}
	if reason := app.iterationBlocksAutomerge(context.Background(), &sessions[1]); reason != "" {
		t.Fatalf("automerge must not wait on iteration comments while disabled, got %q", reason)
	}
	if n := base.countCalls("comments"); n != 0 {
		t.Fatalf("automerge iteration check read comments %d times", n)
	}
}
