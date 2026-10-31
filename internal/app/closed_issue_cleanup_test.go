package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestClosedIssueCleanupRetiresEveryInactiveStatus(t *testing.T) {
	for _, status := range []state.SessionStatus{state.SessionStatusFailed, state.SessionStatusBlocked, state.SessionStatusSuccess, state.SessionStatusIncomplete, state.SessionStatusClosed} {
		for _, unavailable := range []bool{false, true} {
			name := string(status)
			if unavailable {
				name += "/unavailable"
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
				repoPath := t.TempDir()
				worktreePath := filepath.Join(repoPath, "worktree")
				if err := os.Mkdir(worktreePath, 0o755); err != nil {
					t.Fatal(err)
				}
				runner := &countingRunner{base: testutil.FakeRunner{Outputs: map[string]string{
					"gh api repos/owner/repo/issues/1":                 `{"state":"closed","labels":[]}`,
					"git worktree prune":                               "",
					"git worktree remove --force " + worktreePath:      "",
					"git worktree list --porcelain":                    "worktree " + repoPath + "\nbranch refs/heads/main\n",
					"git show-ref --verify --quiet refs/heads/issue-1": "",
					"git branch -D issue-1":                            "",
				}}}
				if unavailable {
					runner.base.Errors = map[string]error{"gh api repos/owner/repo/issues/1": errors.New("HTTP 404: Not Found")}
				}
				app := New()
				app.env.Runner = runner
				app.stdout, app.stderr = testutil.IODiscard{}, testutil.IODiscard{}
				sessions := []state.Session{{Repo: "owner/repo", RepoPath: repoPath, IssueNumber: 1, Branch: "issue-1", WorktreePath: worktreePath, Status: status, PullRequestNumber: 2, PullRequestState: "OPEN"}}
				if err := app.state.EnsureLayout(); err != nil {
					t.Fatal(err)
				}
				if err := app.state.SaveSessions(sessions); err != nil {
					t.Fatal(err)
				}
				if err := app.ScanOnce(context.Background()); err != nil {
					t.Fatal(err)
				}
				sessions, err := app.state.LoadSessions()
				if err != nil {
					t.Fatal(err)
				}
				if sessions[0].Status != state.SessionStatusClosed || sessions[0].CleanupCompletedAt == "" || sessions[0].LastCleanupSource != "issue_closed" {
					t.Fatalf("cleanup did not finish: %+v", sessions[0])
				}
				for _, cmd := range []string{"git worktree remove --force " + worktreePath, "git branch -D issue-1"} {
					if runner.counts[cmd] != 1 {
						t.Fatalf("%s: %d calls", cmd, runner.counts[cmd])
					}
				}
				runner.counts = make(map[string]int)
				if err := app.ScanOnce(context.Background()); err != nil {
					t.Fatal(err)
				}
				for cmd := range runner.counts {
					if strings.Contains(cmd, "issues/1") || strings.Contains(cmd, "pulls/2") || strings.HasPrefix(cmd, "gh pr ") || strings.HasPrefix(cmd, "git ") {
						t.Fatalf("closed session polled or cleaned again: %s", cmd)
					}
				}
			})
		}
	}
}

func TestClosedIssueCleanupProtectsActiveSessions(t *testing.T) {
	for _, session := range []state.Session{
		{Status: state.SessionStatusRunning},
		{Status: state.SessionStatusResuming},
		{Status: state.SessionStatusSuccess, PullRequestMaintenanceInFlight: true},
	} {
		t.Run(string(session.Status), func(t *testing.T) {
			runner := &countingRunner{}
			app := New()
			app.env.Runner = runner
			sessions := []state.Session{session}
			if _, err := app.cleanupClosedIssueSessions(context.Background(), sessions, nil); err != nil {
				t.Fatal(err)
			}
			app.closeIssueSession(context.Background(), &sessions[0], nil, nil)
			if len(runner.counts) != 0 || sessions[0].Status != session.Status {
				t.Fatalf("active session touched: %+v, %v", sessions[0], runner.counts)
			}
		})
	}
}
