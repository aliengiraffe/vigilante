package github

import (
	"context"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func newTestBackend(runner environment.Runner) *Backend {
	return NewBackend(&runner)
}

func TestBackendImplementsIssueTracker(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{})
	var _ backend.IssueTracker = b
	if b.ID() != backend.BackendGitHub {
		t.Fatalf("expected backend ID %q, got %q", backend.BackendGitHub, b.ID())
	}
}

func TestBackendImplementsLabelManager(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{})
	var _ backend.LabelManager = b
}

func TestBackendImplementsPullRequestManager(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{})
	var _ backend.PullRequestManager = b
}

func TestBackendImplementsRateLimiter(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{})
	var _ backend.RateLimiter = b
}

func TestListOpenWorkItemsDelegatesToGhcli(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue list --repo owner/repo --state open --assignee user --json number,title,createdAt,url,labels": `[{"number":1,"title":"test","createdAt":"2026-03-10T12:00:00Z","url":"u1","labels":[]}]`,
		},
	})
	items, err := b.ListOpenWorkItems(context.Background(), "owner/repo", "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Number != 1 {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestResolveAssigneeDelegatesToGhcli(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api user --jq .login": "testuser\n",
		},
	})
	login, err := b.ResolveAssignee(context.Background(), "me")
	if err != nil {
		t.Fatal(err)
	}
	if login != "testuser" {
		t.Fatalf("expected testuser, got %q", login)
	}
}

func TestCommentOnWorkItemDelegatesToGhcli(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh issue comment --repo owner/repo 42 --body hello": "",
		},
	})
	if err := b.CommentOnWorkItem(context.Background(), "owner/repo", 42, "hello"); err != nil {
		t.Fatal(err)
	}
}

func TestGetRateLimitSnapshotDelegatesToGhcli(t *testing.T) {
	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api /rate_limit": `{"resources":{"core":{"limit":5000,"remaining":4999,"reset":1700000000},"rate":{},"graphql":{"limit":5000,"remaining":5000,"reset":1700000000},"search":{"limit":30,"remaining":30,"reset":1700000000}}}`,
		},
	})
	snapshot, err := b.GetRateLimitSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Core.Remaining != 4999 {
		t.Fatalf("unexpected core remaining: %d", snapshot.Core.Remaining)
	}
}
