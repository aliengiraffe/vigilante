package github

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// This Backend is a delegation layer: its job is to route each backend-interface
// method to the right ghcli call with the right arguments. So the thing worth
// asserting is the exact gh invocation each method produces. A fixture keyed on
// the wrong command string makes the call fail, which is what catches a method
// wired to the wrong endpoint or dropping an argument.
func TestBackendDelegationTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		output  string
		invoke  func(*testing.T, *Backend) error
	}{
		{
			name:    "GetWorkItemDetails reads the issue API",
			command: "gh api repos/owner/repo/issues/7",
			output:  `{"number":7,"title":"t","body":"b","state":"open","user":{"login":"u"}}`,
			invoke: func(t *testing.T, b *Backend) error {
				details, err := b.GetWorkItemDetails(context.Background(), "owner/repo", 7)
				if err == nil && details.Title != "t" {
					t.Errorf("Title = %q, want %q", details.Title, "t")
				}
				return err
			},
		},
		{
			name:    "ListWorkItemComments reads issue comments",
			command: "gh api repos/owner/repo/issues/7/comments",
			output:  `[{"id":1,"body":"hi","user":{"login":"u"}}]`,
			invoke: func(t *testing.T, b *Backend) error {
				comments, err := b.ListWorkItemComments(context.Background(), "owner/repo", 7)
				if err == nil && len(comments) != 1 {
					t.Errorf("got %d comments, want 1", len(comments))
				}
				return err
			},
		},
		{
			name:    "ListWorkItemCommentsForPolling reads issue comments",
			command: "gh api repos/owner/repo/issues/7/comments",
			output:  `[{"id":1,"body":"hi","user":{"login":"u"}}]`,
			invoke: func(t *testing.T, b *Backend) error {
				_, err := b.ListWorkItemCommentsForPolling(context.Background(), "owner/repo", 7, "purpose", discardLogger())
				return err
			},
		},
		{
			name:    "AddCommentReaction posts to the issue-comment reactions API",
			command: "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/99/reactions -f content=+1",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.AddCommentReaction(context.Background(), "owner/repo", 99, "+1")
			},
		},
		{
			name:    "CreateWorkItem posts title, body, labels, and assignees",
			command: "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues -f title=title -f body=body -f labels[]=l1 -f assignees[]=a1",
			output:  `{"number":11,"html_url":"https://github.com/owner/repo/issues/11"}`,
			invoke: func(t *testing.T, b *Backend) error {
				created, err := b.CreateWorkItem(context.Background(), "owner/repo", "title", "body", []string{"l1"}, []string{"a1"})
				if err == nil && created.Number != 11 {
					t.Errorf("Number = %d, want 11", created.Number)
				}
				return err
			},
		},
		{
			name:    "CloseWorkItem closes as not planned rather than completed",
			command: "gh api --method PATCH -H Accept: application/vnd.github+json repos/owner/repo/issues/7 -f state=closed -f state_reason=not_planned",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.CloseWorkItem(context.Background(), "owner/repo", 7)
			},
		},
		{
			name:    "RemoveWorkItemLabel edits the issue",
			command: "gh issue edit --repo owner/repo 7 --remove-label lbl",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.RemoveWorkItemLabel(context.Background(), "owner/repo", 7, "lbl")
			},
		},
		{
			name:    "SyncWorkItemLabels adds desired and removes managed labels in one edit",
			command: "gh issue edit --repo owner/repo 7 --add-label new --remove-label old",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.SyncWorkItemLabels(context.Background(), "owner/repo", 7,
					[]backend.Label{{Name: "old"}}, []string{"new"}, []string{"old"})
			},
		},
		{
			name:    "FindPullRequestForBranch searches all states for the head branch",
			command: "gh pr list --repo owner/repo --head branch --state all --json number,url,state,mergedAt",
			output:  `[{"number":5,"url":"u","state":"OPEN"}]`,
			invoke: func(t *testing.T, b *Backend) error {
				pr, err := b.FindPullRequestForBranch(context.Background(), "owner/repo", "branch")
				if err == nil && (pr == nil || pr.Number != 5) {
					t.Errorf("pr = %#v, want number 5", pr)
				}
				return err
			},
		},
		{
			name:    "GetPullRequestDetails requests the review-relevant fields",
			command: "gh pr view --repo owner/repo 7 --json number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,baseRefName",
			output:  `{"number":7,"title":"t","state":"OPEN","baseRefName":"main"}`,
			invoke: func(t *testing.T, b *Backend) error {
				pr, err := b.GetPullRequestDetails(context.Background(), "owner/repo", 7)
				if err == nil && pr.BaseRefName != "main" {
					t.Errorf("BaseRefName = %q, want main", pr.BaseRefName)
				}
				return err
			},
		},
		{
			name:    "MergePullRequest squashes and deletes the branch",
			command: "gh pr merge --repo owner/repo 7 --squash --delete-branch",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.MergePullRequest(context.Background(), "owner/repo", 7)
			},
		},
		{
			name:    "ClosePullRequest closes the pull request",
			command: "gh pr close --repo owner/repo 7",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.ClosePullRequest(context.Background(), "owner/repo", 7)
			},
		},
		{
			// Note this one is git, not gh: branch deletion is a push, so a
			// reviewer should not expect a gh call here.
			name:    "DeleteRemoteBranch pushes a delete rather than calling gh",
			command: "git push origin --delete branch",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.DeleteRemoteBranch(context.Background(), "/repo", "origin", "branch")
			},
		},
		{
			name:    "ListOpenPullRequests lists only open pull requests",
			command: "gh pr list --repo owner/repo --state open --json number,title,url,labels,baseRefName",
			output:  `[{"number":3,"title":"t","url":"u","baseRefName":"main"}]`,
			invoke: func(t *testing.T, b *Backend) error {
				prs, err := b.ListOpenPullRequests(context.Background(), "owner/repo")
				if err == nil && len(prs) != 1 {
					t.Errorf("got %d prs, want 1", len(prs))
				}
				return err
			},
		},
		{
			name:    "ListPullRequestComments reads issue-style comments",
			command: "gh api repos/owner/repo/issues/7/comments",
			output:  `[{"id":1,"body":"hi","user":{"login":"u"}}]`,
			invoke: func(t *testing.T, b *Backend) error {
				_, err := b.ListPullRequestComments(context.Background(), "owner/repo", 7)
				return err
			},
		},
		{
			name:    "ListPullRequestCommentsForPolling reads issue-style comments",
			command: "gh api repos/owner/repo/issues/7/comments",
			output:  `[{"id":1,"body":"hi","user":{"login":"u"}}]`,
			invoke: func(_ *testing.T, b *Backend) error {
				_, err := b.ListPullRequestCommentsForPolling(context.Background(), "owner/repo", 7, "purpose", discardLogger())
				return err
			},
		},
		{
			// Review comments live on a different endpoint than issue comments.
			// Conflating the two silently loses inline review feedback.
			name:    "ListPullRequestReviewComments reads the pulls comments endpoint",
			command: "gh api repos/owner/repo/pulls/7/comments --paginate",
			output:  `[{"id":2,"body":"inline","user":{"login":"u"}}]`,
			invoke: func(t *testing.T, b *Backend) error {
				comments, err := b.ListPullRequestReviewComments(context.Background(), "owner/repo", 7)
				if err == nil && len(comments) != 1 {
					t.Errorf("got %d review comments, want 1", len(comments))
				}
				return err
			},
		},
		{
			name:    "ListPullRequestReviewCommentsForPolling reads the pulls comments endpoint",
			command: "gh api repos/owner/repo/pulls/7/comments --paginate",
			output:  `[{"id":2,"body":"inline","user":{"login":"u"}}]`,
			invoke: func(_ *testing.T, b *Backend) error {
				_, err := b.ListPullRequestReviewCommentsForPolling(context.Background(), "owner/repo", 7, "purpose", discardLogger())
				return err
			},
		},
		{
			name:    "CommentOnPullRequest uses the issue comment endpoint",
			command: "gh issue comment --repo owner/repo 7 --body body",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.CommentOnPullRequest(context.Background(), "owner/repo", 7, "body")
			},
		},
		{
			name:    "AddPullRequestCommentReaction posts to the issue-comment reactions API",
			command: "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/99/reactions -f content=+1",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.AddPullRequestCommentReaction(context.Background(), "owner/repo", 99, "+1")
			},
		},
		{
			name:    "AddPullRequestLabel edits the pull request",
			command: "gh pr edit --repo owner/repo 7 --add-label lbl",
			invoke: func(_ *testing.T, b *Backend) error {
				return b.AddPullRequestLabel(context.Background(), "owner/repo", 7, "lbl")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newTestBackend(testutil.FakeRunner{
				Outputs: map[string]string{tt.command: tt.output},
			})
			if err := tt.invoke(t, b); err != nil {
				t.Fatalf("delegation failed, so the method did not run %q: %v", tt.command, err)
			}
		})
	}
}

// Errors from the gh layer have to reach the caller. A delegation method that
// swallowed one would make the orchestration loop treat a failed API call as
// success.
func TestBackendDelegationPropagatesErrors(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("gh exploded")

	tests := []struct {
		name    string
		command string
		invoke  func(*Backend) error
	}{
		{
			name:    "GetWorkItemDetails",
			command: "gh api repos/owner/repo/issues/7",
			invoke: func(b *Backend) error {
				_, err := b.GetWorkItemDetails(context.Background(), "owner/repo", 7)
				return err
			},
		},
		{
			name:    "CommentOnWorkItem",
			command: "gh issue comment --repo owner/repo 7 --body body",
			invoke: func(b *Backend) error {
				return b.CommentOnWorkItem(context.Background(), "owner/repo", 7, "body")
			},
		},
		{
			name:    "MergePullRequest",
			command: "gh pr merge --repo owner/repo 7 --squash --delete-branch",
			invoke: func(b *Backend) error {
				return b.MergePullRequest(context.Background(), "owner/repo", 7)
			},
		},
		{
			name:    "ClosePullRequest",
			command: "gh pr close --repo owner/repo 7",
			invoke: func(b *Backend) error {
				return b.ClosePullRequest(context.Background(), "owner/repo", 7)
			},
		},
		{
			name:    "DeleteRemoteBranch",
			command: "git push origin --delete branch",
			invoke: func(b *Backend) error {
				return b.DeleteRemoteBranch(context.Background(), "/repo", "origin", "branch")
			},
		},
		{
			name:    "AddPullRequestLabel",
			command: "gh pr edit --repo owner/repo 7 --add-label lbl",
			invoke: func(b *Backend) error {
				return b.AddPullRequestLabel(context.Background(), "owner/repo", 7, "lbl")
			},
		},
		{
			name:    "GetRateLimitSnapshot",
			command: "gh api /rate_limit",
			invoke: func(b *Backend) error {
				_, err := b.GetRateLimitSnapshot(context.Background())
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newTestBackend(testutil.FakeRunner{
				Errors: map[string]error{tt.command: sentinel},
			})
			if err := tt.invoke(b); err == nil {
				t.Fatal("expected the gh error to propagate, got nil")
			}
		})
	}
}

// ListPullRequestFiles is the one method with real mapping logic rather than a
// straight forward, so it gets its own assertion on the converted fields.
func TestListPullRequestFilesMapsGhFieldsToBackendFields(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/pulls/7/files --paginate": `[
				{"filename":"go.mod","status":"modified"},
				{"filename":"main.go","status":"added"}
			]`,
		},
	})

	files, err := b.ListPullRequestFiles(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	if files[0].Filename != "go.mod" || files[0].Status != "modified" {
		t.Errorf("files[0] = %#v, want go.mod/modified", files[0])
	}
	if files[1].Filename != "main.go" || files[1].Status != "added" {
		t.Errorf("files[1] = %#v, want main.go/added", files[1])
	}
}

func TestListPullRequestFilesPropagatesErrors(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Errors: map[string]error{
			"gh api repos/owner/repo/pulls/7/files --paginate": errors.New("boom"),
		},
	})

	files, err := b.ListPullRequestFiles(context.Background(), "owner/repo", 7)
	if err == nil {
		t.Fatal("expected an error")
	}
	if files != nil {
		t.Fatalf("files must be nil on error, got %#v", files)
	}
}

func TestListPullRequestFilesHandlesEmptyList(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/pulls/7/files --paginate": `[]`,
		},
	})

	files, err := b.ListPullRequestFiles(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("got %d files, want 0", len(files))
	}
}

// EnsureProjectLabels reads the existing labels before reconciling, so the first
// call it makes is the list endpoint.
func TestEnsureProjectLabelsReadsExistingLabelsFirst(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/labels?per_page=100": `[{"name":"keep","color":"ffffff","description":"d"}]`,
		},
	})

	err := b.EnsureProjectLabels(context.Background(), "owner/repo", []backend.RepositoryLabelSpec{
		{Name: "keep", Color: "ffffff", Description: "d"},
	})
	if err != nil {
		t.Fatalf("a label already matching the spec should need no writes: %v", err)
	}
}

func TestEnsureProjectLabelsPropagatesListErrors(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Errors: map[string]error{
			"gh api repos/owner/repo/labels?per_page=100": errors.New("boom"),
		},
	})

	if err := b.EnsureProjectLabels(context.Background(), "owner/repo", nil); err == nil {
		t.Fatal("expected the list error to propagate")
	}
}

// IsWorkItemUnavailable is a pure predicate over the error text — gh reports a
// deleted or transferred issue as 404/410 rather than a typed error, so this
// matches on the message. It never touches the runner.
//
// It requires a non-nil error: the underlying ghcli predicate calls err.Error()
// unguarded. Every production call site is already inside an `if err != nil`
// block, so nil is unreachable there, and this test does not pass nil rather than
// asserting a nil-safety guarantee the code does not make.
func TestIsWorkItemUnavailable(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{})

	unavailable := []string{
		"HTTP 410: Gone",
		"gh: (410)",
		"failed with 410 status",
		"issue is gone",
		"HTTP 404: Not Found",
		"gh: (404)",
		"not found",
		"NOT FOUND",
	}
	for _, text := range unavailable {
		if !b.IsWorkItemUnavailable(errors.New(text)) {
			t.Errorf("%q should be reported as unavailable", text)
		}
	}

	available := []string{
		"HTTP 500: Internal Server Error",
		"connection reset by peer",
		"HTTP 403: rate limit exceeded",
	}
	for _, text := range available {
		if b.IsWorkItemUnavailable(errors.New(text)) {
			t.Errorf("%q must not be reported as unavailable", text)
		}
	}
}

// The backend reads the runner through a pointer so tests can swap it after
// construction. If that indirection regressed to a value copy, a test that
// replaces the runner would silently keep using the original.
func TestBackendReadsRunnerThroughPointerOnEachCall(t *testing.T) {
	t.Parallel()

	var runner environment.Runner = testutil.FakeRunner{
		Errors: map[string]error{"gh api /rate_limit": errors.New("first runner")},
	}
	b := NewBackend(&runner)

	if _, err := b.GetRateLimitSnapshot(context.Background()); err == nil {
		t.Fatal("expected the first runner to fail")
	}

	runner = testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api /rate_limit": `{"resources":{"core":{"limit":5000,"remaining":42,"reset":1700000000},"rate":{},"graphql":{},"search":{}}}`,
		},
	}

	snapshot, err := b.GetRateLimitSnapshot(context.Background())
	if err != nil {
		t.Fatalf("the replaced runner should be used: %v", err)
	}
	if snapshot.Core.Remaining != 42 {
		t.Fatalf("Core.Remaining = %d, want 42 from the replaced runner", snapshot.Core.Remaining)
	}
}

func TestGetWorkItemDetailsRejectsMalformedJSON(t *testing.T) {
	t.Parallel()

	b := newTestBackend(testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api repos/owner/repo/issues/7": "not json at all",
		},
	})

	if _, err := b.GetWorkItemDetails(context.Background(), "owner/repo", 7); err == nil {
		t.Fatal("expected a decode error for malformed gh output")
	} else if !strings.Contains(err.Error(), "issue") && !strings.Contains(err.Error(), "json") &&
		!strings.Contains(err.Error(), "unmarshal") && !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error should describe the decode failure, got %v", err)
	}
}
