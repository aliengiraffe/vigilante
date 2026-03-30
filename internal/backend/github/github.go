package github

import (
	"context"
	"log/slog"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
)

// Backend implements the backend interfaces for GitHub using the gh CLI.
// It wraps the existing internal/github (ghcli) functions to provide
// a clean interface boundary for the orchestration loop.
type Backend struct {
	runnerRef *environment.Runner
}

// NewBackend creates a GitHub backend that reads the runner from the given
// pointer on each call. This allows the runner to be replaced after creation
// (e.g., in tests) without rebuilding the backend.
func NewBackend(runner *environment.Runner) *Backend {
	return &Backend{runnerRef: runner}
}

func (b *Backend) runner() environment.Runner {
	return *b.runnerRef
}

// Verify interface compliance at compile time.
var (
	_ backend.IssueTracker       = (*Backend)(nil)
	_ backend.LabelManager       = (*Backend)(nil)
	_ backend.PullRequestManager = (*Backend)(nil)
	_ backend.RateLimiter        = (*Backend)(nil)
)

// ID returns the GitHub backend identifier.
func (b *Backend) ID() backend.BackendID {
	return backend.BackendGitHub
}

// --- IssueTracker ---

func (b *Backend) ResolveAssignee(ctx context.Context, assignee string) (string, error) {
	return ghcli.ResolveAssignee(ctx, b.runner(), assignee)
}

func (b *Backend) ListOpenWorkItems(ctx context.Context, project string, assignee string) ([]backend.WorkItem, error) {
	return ghcli.ListOpenIssuesForAssignee(ctx, b.runner(), project, assignee)
}

func (b *Backend) GetWorkItemDetails(ctx context.Context, project string, number int) (*backend.WorkItemDetails, error) {
	return ghcli.GetIssueDetails(ctx, b.runner(), project, number)
}

func (b *Backend) ListWorkItemComments(ctx context.Context, project string, number int) ([]backend.WorkItemComment, error) {
	return ghcli.ListIssueComments(ctx, b.runner(), project, number)
}

func (b *Backend) ListWorkItemCommentsForPolling(ctx context.Context, project string, number int, purpose string, logger *slog.Logger) ([]backend.WorkItemComment, error) {
	return ghcli.ListIssueCommentsForPolling(ctx, b.runner(), project, number, purpose, logger)
}

func (b *Backend) CommentOnWorkItem(ctx context.Context, project string, number int, body string) error {
	return ghcli.CommentOnIssue(ctx, b.runner(), project, number, body)
}

func (b *Backend) AddCommentReaction(ctx context.Context, project string, commentID int64, content string) error {
	return ghcli.AddIssueCommentReaction(ctx, b.runner(), project, commentID, content)
}

func (b *Backend) CreateWorkItem(ctx context.Context, project string, title string, body string, labels []string, assignees []string) (*backend.CreatedWorkItem, error) {
	return ghcli.CreateIssue(ctx, b.runner(), project, title, body, labels, assignees)
}

func (b *Backend) CloseWorkItem(ctx context.Context, project string, number int) error {
	return ghcli.CloseIssueNotPlanned(ctx, b.runner(), project, number)
}

func (b *Backend) IsWorkItemUnavailable(err error) bool {
	return ghcli.IsIssueUnavailableError(err)
}

// --- LabelManager ---

func (b *Backend) EnsureProjectLabels(ctx context.Context, project string, desired []backend.RepositoryLabelSpec) error {
	return ghcli.EnsureRepositoryLabels(ctx, b.runner(), project, desired)
}

func (b *Backend) SyncWorkItemLabels(ctx context.Context, project string, number int, current []backend.Label, desired []string, managed []string) error {
	return ghcli.SyncIssueLabels(ctx, b.runner(), project, number, current, desired, managed)
}

func (b *Backend) RemoveWorkItemLabel(ctx context.Context, project string, number int, label string) error {
	return ghcli.RemoveIssueLabel(ctx, b.runner(), project, number, label)
}

// --- PullRequestManager ---

func (b *Backend) FindPullRequestForBranch(ctx context.Context, repo string, branch string) (*backend.PullRequest, error) {
	return ghcli.FindPullRequestForBranch(ctx, b.runner(), repo, branch)
}

func (b *Backend) GetPullRequestDetails(ctx context.Context, repo string, number int) (*backend.PullRequest, error) {
	return ghcli.GetPullRequestDetails(ctx, b.runner(), repo, number)
}

func (b *Backend) MergePullRequest(ctx context.Context, repo string, number int) error {
	return ghcli.MergePullRequestSquash(ctx, b.runner(), repo, number)
}

func (b *Backend) ClosePullRequest(ctx context.Context, repo string, number int) error {
	return ghcli.ClosePullRequest(ctx, b.runner(), repo, number)
}

func (b *Backend) DeleteRemoteBranch(ctx context.Context, repoPath string, remote string, branch string) error {
	return ghcli.DeleteRemoteBranch(ctx, b.runner(), repoPath, remote, branch)
}

// --- RateLimiter ---

func (b *Backend) GetRateLimitSnapshot(ctx context.Context) (backend.RateLimitSnapshot, error) {
	return ghcli.GetRateLimitSnapshot(ctx, b.runner())
}
