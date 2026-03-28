// Package backend defines the project-management and repository-host
// abstraction layers.
//
// Vigilante's orchestration loop depends on these interfaces instead of
// calling GitHub-specific APIs directly.  The two primary interfaces are:
//
//   - IssueTracker: manages work items, comments, labels, and assignees.
//     Implementations: GitHub Issues, and in the future Linear, Jira, etc.
//   - RepoHost: manages pull requests, branches, and code-review state.
//     Implementations: GitHub, and in the future GitLab, Bitbucket, etc.
//
// The issue-tracking backend may differ from the repository-management
// backend.  For example, a team could track work items in Linear while
// hosting code and pull requests on GitHub.
package backend

import (
	"context"
	"log/slog"
	"time"
)

// WorkItem is a backend-neutral representation of an issue, ticket, or work
// item from any project-management system.
type WorkItem struct {
	Number    int
	Title     string
	Body      string
	URL       string
	State     string
	CreatedAt time.Time
	Labels    []string
	Assignees []string
}

// Comment represents a comment on a work item.
type Comment struct {
	ID        int64
	Body      string
	CreatedAt time.Time
	Author    string
}

// PullRequest represents a pull request or merge request associated with a
// work item.
type PullRequest struct {
	Number           int
	Title            string
	Body             string
	URL              string
	State            string
	BaseRefName      string
	MergedAt         *time.Time
	Labels           []string
	IsDraft          bool
	Mergeable        string
	MergeStateStatus string
	ReviewDecision   string
	StatusChecks     []StatusCheck
}

// StatusCheck represents a CI status check on a pull request.
type StatusCheck struct {
	Context    string
	Name       string
	State      string
	Conclusion string
}

// LabelSpec defines a label to provision in a project.
type LabelSpec struct {
	Name        string
	Color       string
	Description string
}

// RateLimitSnapshot captures the current API rate-limit state for backends
// that expose quota information.
type RateLimitSnapshot struct {
	Core    RateLimitResource
	Rate    RateLimitResource
	GraphQL RateLimitResource
	Search  RateLimitResource
}

// RateLimitResource describes a single rate-limit bucket.
type RateLimitResource struct {
	Limit     int
	Remaining int
	ResetAt   time.Time
}

// CreatedWorkItem holds the result of creating a new work item.
type CreatedWorkItem struct {
	Number int
	URL    string
}

// ---------------------------------------------------------------------------
// IssueTracker — project-management / issue-tracking abstraction
// ---------------------------------------------------------------------------

// IssueTracker is the core interface every project-management backend must
// implement.  It covers the minimum operations the orchestration loop needs
// for work-item management: resolving assignees, listing and inspecting work
// items, reading and posting comments, detecting operator commands, and
// managing work-item lifecycle.
//
// Implementations include GitHub Issues, and in the future Linear, Jira, etc.
type IssueTracker interface {
	// ID returns the backend identifier (e.g. "github", "linear", "jira").
	ID() string

	// ResolveAssignee resolves special assignee tokens such as "me" into an
	// actual user identifier.
	ResolveAssignee(ctx context.Context, assignee string) (string, error)

	// ListWorkItems returns open work items for the given target, optionally
	// filtered by assignee.  Results are sorted oldest-first.
	ListWorkItems(ctx context.Context, target string, assignee string) ([]WorkItem, error)

	// GetWorkItem fetches full details for a single work item.
	GetWorkItem(ctx context.Context, target string, number int) (*WorkItem, error)

	// ListComments returns comments on a work item sorted oldest-first.
	ListComments(ctx context.Context, target string, workItemID int) ([]Comment, error)

	// PollComments is like ListComments but intended for background polling
	// loops where noisy logging should be suppressed.  The purpose string
	// is used only for diagnostic logging.
	PollComments(ctx context.Context, target string, workItemID int, purpose string) ([]Comment, error)

	// PostComment adds a comment to a work item.
	PostComment(ctx context.Context, target string, workItemID int, body string) error

	// AcknowledgeComment marks a comment as processed, for example by
	// adding a reaction.  The meaning of reaction is backend-specific.
	AcknowledgeComment(ctx context.Context, target string, commentID int64, reaction string) error

	// CreateWorkItem creates a new work item and returns its number and URL.
	CreateWorkItem(ctx context.Context, target string, title string, body string, labels []string, assignees []string) (*CreatedWorkItem, error)

	// CloseWorkItem closes a work item as not planned / won't do.
	CloseWorkItem(ctx context.Context, target string, number int) error

	// IsWorkItemUnavailable reports whether err indicates the work item no
	// longer exists (e.g. HTTP 404 or 410).
	IsWorkItemUnavailable(err error) bool
}

// Backend is an alias for IssueTracker preserved for backward compatibility
// during the transition period.  New code should use IssueTracker directly.
type Backend = IssueTracker

// ---------------------------------------------------------------------------
// RepoHost — repository / code-management abstraction
// ---------------------------------------------------------------------------

// RepoHost is the interface for repository-management backends that handle
// pull requests, branches, and code-review state.  The repo host may differ
// from the issue tracker: for example, issues could live in Linear while
// code and PRs live on GitHub.
//
// Implementations include GitHub, and in the future GitLab, Bitbucket, etc.
type RepoHost interface {
	// ID returns the repo-host identifier (e.g. "github", "gitlab").
	ID() string

	// FindPullRequestForBranch looks up a PR by its head branch name.
	FindPullRequestForBranch(ctx context.Context, target string, branch string) (*PullRequest, error)

	// GetPullRequestDetails fetches full details for a pull request.
	GetPullRequestDetails(ctx context.Context, target string, number int) (*PullRequest, error)

	// MergePullRequestSquash merges and deletes the branch via squash.
	MergePullRequestSquash(ctx context.Context, target string, number int) error

	// ClosePullRequest closes the pull request without merging.
	ClosePullRequest(ctx context.Context, target string, number int) error

	// DeleteRemoteBranch deletes a branch from the remote.
	DeleteRemoteBranch(ctx context.Context, repoPath string, branch string) error
}

// ---------------------------------------------------------------------------
// Optional capability interfaces
// ---------------------------------------------------------------------------

// LabelManager is implemented by issue trackers that support label operations
// on work items and projects.
type LabelManager interface {
	// SyncWorkItemLabels reconciles the labels on a work item.  current is
	// the set of labels currently on the item, desired is the target set,
	// and managed lists the labels Vigilante is allowed to add or remove.
	SyncWorkItemLabels(ctx context.Context, target string, number int, current []string, desired []string, managed []string) error

	// RemoveWorkItemLabel removes a single label from a work item.
	RemoveWorkItemLabel(ctx context.Context, target string, number int, label string) error

	// EnsureProjectLabels provisions the given label specs in the project,
	// creating any that do not already exist.
	EnsureProjectLabels(ctx context.Context, target string, specs []LabelSpec) error

	// LoadLabelSpecs returns the built-in Vigilante label definitions that
	// should be provisioned in every watched project.
	LoadLabelSpecs() ([]LabelSpec, error)
}

// PullRequestManager is kept as a compatibility alias for RepoHost during the
// transition.  New code should use RepoHost directly.
type PullRequestManager = RepoHost

// RateLimitChecker is implemented by backends that expose API quota
// information so the orchestration loop can pause before hitting limits.
// Both IssueTracker and RepoHost implementations may implement this.
type RateLimitChecker interface {
	CheckRateLimit(ctx context.Context) (*RateLimitSnapshot, error)
}

// ---------------------------------------------------------------------------
// Capability helpers
// ---------------------------------------------------------------------------

// AsLabelManager returns the LabelManager if the issue tracker supports it.
func AsLabelManager(b IssueTracker) (LabelManager, bool) {
	lm, ok := b.(LabelManager)
	return lm, ok
}

// AsPullRequestManager returns the RepoHost if the issue tracker also
// implements it.  This is a backward-compatibility helper for backends like
// GitHub that serve as both issue tracker and repo host in a single type.
func AsPullRequestManager(b IssueTracker) (RepoHost, bool) {
	prm, ok := b.(RepoHost)
	return prm, ok
}

// AsRateLimitChecker returns the RateLimitChecker if the backend supports it.
// Works with both IssueTracker and RepoHost implementations.
func AsRateLimitChecker(b any) (RateLimitChecker, bool) {
	rlc, ok := b.(RateLimitChecker)
	return rlc, ok
}

// ---------------------------------------------------------------------------
// IssueTracker registry
// ---------------------------------------------------------------------------

// IssueTrackerFactory creates an IssueTracker.  The logger may be nil.
type IssueTrackerFactory func(logger *slog.Logger) IssueTracker

// BackendFactory is a compatibility alias for IssueTrackerFactory.
type BackendFactory = IssueTrackerFactory

var issueTrackerRegistry = map[string]IssueTrackerFactory{}

// Register adds an issue-tracker factory to the global registry.
func Register(id string, factory IssueTrackerFactory) {
	issueTrackerRegistry[id] = factory
}

// Lookup returns the issue-tracker factory for the given backend ID.
func Lookup(id string) (IssueTrackerFactory, bool) {
	f, ok := issueTrackerRegistry[id]
	return f, ok
}

// ---------------------------------------------------------------------------
// RepoHost registry
// ---------------------------------------------------------------------------

// RepoHostFactory creates a RepoHost.  The logger may be nil.
type RepoHostFactory func(logger *slog.Logger) RepoHost

var repoHostRegistry = map[string]RepoHostFactory{}

// RegisterRepoHost adds a repo-host factory to the global registry.
func RegisterRepoHost(id string, factory RepoHostFactory) {
	repoHostRegistry[id] = factory
}

// LookupRepoHost returns the repo-host factory for the given backend ID.
func LookupRepoHost(id string) (RepoHostFactory, bool) {
	f, ok := repoHostRegistry[id]
	return f, ok
}

// ---------------------------------------------------------------------------
// Default backend IDs
// ---------------------------------------------------------------------------

// DefaultBackendID is the issue-tracker backend used when no explicit backend
// is configured.
const DefaultBackendID = "github"

// DefaultRepoHostID is the repo-host backend used when no explicit repo host
// is configured.
const DefaultRepoHostID = "github"
