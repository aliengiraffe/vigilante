package github

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestDiscoverCommands(t *testing.T) {
	ctx := context.Background()
	b := newTestBackend(testutil.FakeRunner{})
	logger := slog.New(slog.DiscardHandler)

	report := func(name string, err error) {
		fmt.Printf("DISCOVER\t%s\t%v\n", name, err)
	}

	_, err := b.GetWorkItemDetails(ctx, "owner/repo", 7)
	report("GetWorkItemDetails", err)
	_, err = b.ListWorkItemComments(ctx, "owner/repo", 7)
	report("ListWorkItemComments", err)
	_, err = b.ListWorkItemCommentsForPolling(ctx, "owner/repo", 7, "purpose", logger)
	report("ListWorkItemCommentsForPolling", err)
	report("AddCommentReaction", b.AddCommentReaction(ctx, "owner/repo", 99, "+1"))
	_, err = b.CreateWorkItem(ctx, "owner/repo", "title", "body", []string{"l1"}, []string{"a1"})
	report("CreateWorkItem", err)
	report("CloseWorkItem", b.CloseWorkItem(ctx, "owner/repo", 7))
	report("EnsureProjectLabels", b.EnsureProjectLabels(ctx, "owner/repo", []backend.RepositoryLabelSpec{{Name: "n", Color: "c", Description: "d"}}))
	report("SyncWorkItemLabels", b.SyncWorkItemLabels(ctx, "owner/repo", 7, []backend.Label{{Name: "old"}}, []string{"new"}, []string{"old"}))
	report("RemoveWorkItemLabel", b.RemoveWorkItemLabel(ctx, "owner/repo", 7, "lbl"))
	_, err = b.FindPullRequestForBranch(ctx, "owner/repo", "branch")
	report("FindPullRequestForBranch", err)
	_, err = b.GetPullRequestDetails(ctx, "owner/repo", 7)
	report("GetPullRequestDetails", err)
	report("MergePullRequest", b.MergePullRequest(ctx, "owner/repo", 7))
	report("ClosePullRequest", b.ClosePullRequest(ctx, "owner/repo", 7))
	report("DeleteRemoteBranch", b.DeleteRemoteBranch(ctx, "/repo", "origin", "branch"))
	_, err = b.ListOpenPullRequests(ctx, "owner/repo")
	report("ListOpenPullRequests", err)
	_, err = b.ListPullRequestFiles(ctx, "owner/repo", 7)
	report("ListPullRequestFiles", err)
	_, err = b.ListPullRequestComments(ctx, "owner/repo", 7)
	report("ListPullRequestComments", err)
	_, err = b.ListPullRequestCommentsForPolling(ctx, "owner/repo", 7, "purpose", logger)
	report("ListPullRequestCommentsForPolling", err)
	_, err = b.ListPullRequestReviewComments(ctx, "owner/repo", 7)
	report("ListPullRequestReviewComments", err)
	_, err = b.ListPullRequestReviewCommentsForPolling(ctx, "owner/repo", 7, "purpose", logger)
	report("ListPullRequestReviewCommentsForPolling", err)
	report("CommentOnPullRequest", b.CommentOnPullRequest(ctx, "owner/repo", 7, "body"))
	report("AddPullRequestCommentReaction", b.AddPullRequestCommentReaction(ctx, "owner/repo", 99, "+1"))
	report("AddPullRequestLabel", b.AddPullRequestLabel(ctx, "owner/repo", 7, "lbl"))
	_, err = b.GetRateLimitSnapshot(ctx)
	report("GetRateLimitSnapshot", err)
}
