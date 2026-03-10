package ghcli

import (
	"context"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestListOpenIssuesAndSelectNext(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --json number,title,createdAt,url": `[{"number":2,"title":"newer","createdAt":"2026-03-10T12:00:00Z","url":"u2"},{"number":1,"title":"older","createdAt":"2026-03-09T12:00:00Z","url":"u1"}]`,
		},
	}
	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if issues[0].Number != 1 {
		t.Fatalf("expected oldest issue first: %#v", issues)
	}
	next := SelectNextIssue(issues, []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning}}, "owner/repo")
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}
}
