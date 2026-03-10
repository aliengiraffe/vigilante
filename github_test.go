package main

import (
	"context"
	"testing"
)

func TestListOpenIssuesAndSelectNext(t *testing.T) {
	runner := fakeRunner{
		outputs: map[string]string{
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
	next := SelectNextIssue(issues, []Session{{Repo: "owner/repo", IssueNumber: 1, Status: SessionStatusRunning}}, "owner/repo")
	if next == nil || next.Number != 2 {
		t.Fatalf("unexpected next issue: %#v", next)
	}
}
