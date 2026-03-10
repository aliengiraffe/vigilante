package ghcli

import (
	"context"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestListOpenIssuesAndSelectNext(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --assignee me --json number,title,createdAt,url,labels": `[{"number":2,"title":"newer","createdAt":"2026-03-10T12:00:00Z","url":"u2","labels":[{"name":"to-do"}]},{"number":1,"title":"older","createdAt":"2026-03-09T12:00:00Z","url":"u1","labels":[{"name":"bug"}]}]`,
		},
	}
	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo", "me")
	if err != nil {
		t.Fatal(err)
	}
	if issues[0].Number != 1 {
		t.Fatalf("expected oldest issue first: %#v", issues)
	}
	next := SelectNextIssue(issues, []state.Session{{Repo: "owner/repo", IssueNumber: 1, Status: state.SessionStatusRunning}}, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do"}})
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}
}

func TestSelectNextIssueRespectsConfiguredLabels(t *testing.T) {
	issues := []Issue{
		{Number: 1, Labels: []Label{{Name: "bug"}}},
		{Number: 2, Labels: []Label{{Name: "to-do"}}},
		{Number: 3, Labels: []Label{{Name: "good first issue"}, {Name: "help wanted"}}},
	}

	next := SelectNextIssue(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"to-do", "good first issue"}})
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}

	next = SelectNextIssue(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"good first issue"}})
	if next == nil || next.Number != 3 {
		t.Fatalf("unexpected next issue for second label match: %#v", next)
	}

	next = SelectNextIssue(issues, nil, state.WatchTarget{Repo: "owner/repo", Labels: []string{"vibe-code"}})
	if next != nil {
		t.Fatalf("expected no matching issue, got %#v", next)
	}
}

func TestListOpenIssuesSupportsExplicitAssignee(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --assignee nicobistolfi --json number,title,createdAt,url,labels": `[{"number":3,"title":"mine","createdAt":"2026-03-08T12:00:00Z","url":"u3","labels":[]}]`,
		},
	}

	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo", "nicobistolfi")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 3 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestListOpenIssuesAllowsNoAssigneeFilter(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --json number,title,createdAt,url,labels": `[{"number":4,"title":"unassigned","createdAt":"2026-03-08T12:00:00Z","url":"u4","labels":[]}]`,
		},
	}

	issues, err := ListOpenIssues(context.Background(), runner, "owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Number != 4 {
		t.Fatalf("unexpected issues: %#v", issues)
	}
}

func TestFindPullRequestForBranch(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh pr list --repo owner/repo --head vigilante/issue-7 --state all --json number,url,mergedAt": `[{"number":17,"url":"https://github.com/owner/repo/pull/17","mergedAt":"2026-03-10T14:00:00Z"}]`,
		},
	}

	pr, err := FindPullRequestForBranch(context.Background(), runner, "owner/repo", "vigilante/issue-7")
	if err != nil {
		t.Fatal(err)
	}
	if pr == nil {
		t.Fatal("expected pull request")
	}
	if pr.Number != 17 || pr.URL != "https://github.com/owner/repo/pull/17" {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
	if pr.MergedAt == nil || !pr.MergedAt.Equal(time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected merged time: %#v", pr.MergedAt)
	}
}
