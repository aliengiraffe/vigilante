package backend

import (
	"context"
	"log/slog"
)

// IssueTracker provides work item operations for a project management backend.
// The orchestration loop depends on this interface instead of calling
// GitHub-specific APIs directly.
type IssueTracker interface {
	// ID returns the backend identifier (e.g. "github", "linear", "jira").
	ID() BackendID

	// ResolveAssignee resolves a symbolic assignee reference to a concrete identity.
	// For GitHub, this resolves "me" to the authenticated user login.
	ResolveAssignee(ctx context.Context, assignee string) (string, error)

	// ListOpenWorkItems returns open work items for a project, optionally filtered
	// by assignee. Results are ordered by creation time ascending.
	ListOpenWorkItems(ctx context.Context, project string, assignee string) ([]WorkItem, error)

	// GetWorkItemDetails fetches the full details for a single work item.
	GetWorkItemDetails(ctx context.Context, project string, number int) (*WorkItemDetails, error)

	// ListWorkItemComments returns comments on a work item, ordered chronologically.
	ListWorkItemComments(ctx context.Context, project string, number int) ([]WorkItemComment, error)

	// ListWorkItemCommentsForPolling is a polling-safe variant of ListWorkItemComments
	// that logs failures without propagating through access logging.
	ListWorkItemCommentsForPolling(ctx context.Context, project string, number int, purpose string, logger *slog.Logger) ([]WorkItemComment, error)

	// CommentOnWorkItem posts a comment on a work item.
	CommentOnWorkItem(ctx context.Context, project string, number int, body string) error

	// AddCommentReaction adds a reaction to a comment.
	AddCommentReaction(ctx context.Context, project string, commentID int64, content string) error

	// CreateWorkItem creates a new work item on the project.
	CreateWorkItem(ctx context.Context, project string, title string, body string, labels []string, assignees []string) (*CreatedWorkItem, error)

	// CloseWorkItem closes a work item as not planned.
	CloseWorkItem(ctx context.Context, project string, number int) error

	// IsWorkItemUnavailable checks if an error indicates the work item
	// no longer exists (e.g. HTTP 404 or 410).
	IsWorkItemUnavailable(err error) bool
}

// LabelManager provides label operations on a project management backend.
// Not all backends support labels; backends that do not support labels
// should not implement this interface.
type LabelManager interface {
	// EnsureProjectLabels creates any labels from desired that do not already
	// exist on the project.
	EnsureProjectLabels(ctx context.Context, project string, desired []RepositoryLabelSpec) error

	// SyncWorkItemLabels synchronizes labels on a work item by adding desired
	// labels and removing stale managed labels.
	SyncWorkItemLabels(ctx context.Context, project string, number int, current []Label, desired []string, managed []string) error

	// RemoveWorkItemLabel removes a single label from a work item.
	RemoveWorkItemLabel(ctx context.Context, project string, number int, label string) error
}

// PullRequestManager provides pull request operations.
// The issue-tracking backend is allowed to differ from the pull request backend.
// For example, issues may come from Linear while pull requests remain on GitHub.
type PullRequestManager interface {
	// FindPullRequestForBranch finds a PR associated with a branch.
	FindPullRequestForBranch(ctx context.Context, repo string, branch string) (*PullRequest, error)

	// GetPullRequestDetails fetches full PR metadata by number.
	GetPullRequestDetails(ctx context.Context, repo string, number int) (*PullRequest, error)

	// MergePullRequest merges a PR using the backend default merge strategy.
	MergePullRequest(ctx context.Context, repo string, number int) error

	// ClosePullRequest closes a PR without merging.
	ClosePullRequest(ctx context.Context, repo string, number int) error

	// DeleteRemoteBranch deletes a remote branch from the repository.
	DeleteRemoteBranch(ctx context.Context, repoPath string, branch string) error
}

// RateLimiter provides rate limit awareness for backends that enforce API quotas.
// This is an optional capability. Backends that do not enforce rate limits
// do not need to implement this interface.
type RateLimiter interface {
	// GetRateLimitSnapshot returns the current rate limit state.
	GetRateLimitSnapshot(ctx context.Context) (RateLimitSnapshot, error)
}
