package backend

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
)

const GitHubBackendID = "github"

// GitHubBackend implements the Backend, LabelManager, PullRequestManager, and
// RateLimitChecker interfaces by delegating to the existing internal/github
// package.
type GitHubBackend struct {
	runner environment.Runner
	logger *slog.Logger
}

// Compile-time interface checks.
var (
	_ Backend            = (*GitHubBackend)(nil)
	_ LabelManager       = (*GitHubBackend)(nil)
	_ PullRequestManager = (*GitHubBackend)(nil)
	_ RateLimitChecker   = (*GitHubBackend)(nil)
)

// NewGitHubBackend creates a GitHub backend using the provided runner.
func NewGitHubBackend(runner environment.Runner, logger *slog.Logger) *GitHubBackend {
	return &GitHubBackend{runner: runner, logger: logger}
}

func init() {
	Register(GitHubBackendID, func(logger *slog.Logger) Backend {
		return NewGitHubBackend(environment.ExecRunner{}, logger)
	})
}

// ---------------------------------------------------------------------------
// Backend interface
// ---------------------------------------------------------------------------

func (g *GitHubBackend) ID() string { return GitHubBackendID }

func (g *GitHubBackend) ResolveAssignee(ctx context.Context, assignee string) (string, error) {
	return ghcli.ResolveAssignee(ctx, g.runner, assignee)
}

func (g *GitHubBackend) ListWorkItems(ctx context.Context, target string, assignee string) ([]WorkItem, error) {
	issues, err := ghcli.ListOpenIssuesForAssignee(ctx, g.runner, target, assignee)
	if err != nil {
		return nil, err
	}
	items := make([]WorkItem, len(issues))
	for i, issue := range issues {
		items[i] = ghIssueToWorkItem(issue)
	}
	return items, nil
}

func (g *GitHubBackend) GetWorkItem(ctx context.Context, target string, number int) (*WorkItem, error) {
	details, err := ghcli.GetIssueDetails(ctx, g.runner, target, number)
	if err != nil {
		return nil, err
	}
	return ghIssueDetailsToWorkItem(details), nil
}

func (g *GitHubBackend) ListComments(ctx context.Context, target string, workItemID int) ([]Comment, error) {
	comments, err := ghcli.ListIssueComments(ctx, g.runner, target, workItemID)
	if err != nil {
		return nil, err
	}
	return ghCommentsToComments(comments), nil
}

func (g *GitHubBackend) PollComments(ctx context.Context, target string, workItemID int, purpose string) ([]Comment, error) {
	comments, err := ghcli.ListIssueCommentsForPolling(ctx, g.runner, target, workItemID, purpose, g.logger)
	if err != nil {
		return nil, err
	}
	return ghCommentsToComments(comments), nil
}

func (g *GitHubBackend) PostComment(ctx context.Context, target string, workItemID int, body string) error {
	return ghcli.CommentOnIssue(ctx, g.runner, target, workItemID, body)
}

func (g *GitHubBackend) AcknowledgeComment(ctx context.Context, target string, commentID int64, reaction string) error {
	return ghcli.AddIssueCommentReaction(ctx, g.runner, target, commentID, reaction)
}

func (g *GitHubBackend) CreateWorkItem(ctx context.Context, target string, title string, body string, labels []string, assignees []string) (*CreatedWorkItem, error) {
	created, err := ghcli.CreateIssue(ctx, g.runner, target, title, body, labels, assignees)
	if err != nil {
		return nil, err
	}
	return &CreatedWorkItem{Number: created.Number, URL: created.URL}, nil
}

func (g *GitHubBackend) CloseWorkItem(ctx context.Context, target string, number int) error {
	return ghcli.CloseIssueNotPlanned(ctx, g.runner, target, number)
}

func (g *GitHubBackend) IsWorkItemUnavailable(err error) bool {
	return ghcli.IsIssueUnavailableError(err)
}

// ---------------------------------------------------------------------------
// LabelManager interface
// ---------------------------------------------------------------------------

func (g *GitHubBackend) SyncWorkItemLabels(ctx context.Context, target string, number int, current []string, desired []string, managed []string) error {
	// Convert string slices to ghcli.Label slice for the existing API.
	labels := make([]ghcli.Label, len(current))
	for i, name := range current {
		labels[i] = ghcli.Label{Name: name}
	}
	return ghcli.SyncIssueLabels(ctx, g.runner, target, number, labels, desired, managed)
}

func (g *GitHubBackend) RemoveWorkItemLabel(ctx context.Context, target string, number int, label string) error {
	return ghcli.RemoveIssueLabel(ctx, g.runner, target, number, label)
}

func (g *GitHubBackend) EnsureProjectLabels(ctx context.Context, target string, specs []LabelSpec) error {
	ghSpecs := make([]ghcli.RepositoryLabelSpec, len(specs))
	for i, spec := range specs {
		ghSpecs[i] = ghcli.RepositoryLabelSpec{
			Name:        spec.Name,
			Color:       spec.Color,
			Description: spec.Description,
		}
	}
	return ghcli.EnsureRepositoryLabels(ctx, g.runner, target, ghSpecs)
}

func (g *GitHubBackend) LoadLabelSpecs() ([]LabelSpec, error) {
	ghSpecs, err := ghcli.LoadRepositoryLabelSpecs()
	if err != nil {
		return nil, err
	}
	specs := make([]LabelSpec, len(ghSpecs))
	for i, gh := range ghSpecs {
		specs[i] = LabelSpec{
			Name:        gh.Name,
			Color:       gh.Color,
			Description: gh.Description,
		}
	}
	return specs, nil
}

// ---------------------------------------------------------------------------
// PullRequestManager interface
// ---------------------------------------------------------------------------

func (g *GitHubBackend) FindPullRequestForBranch(ctx context.Context, target string, branch string) (*PullRequest, error) {
	pr, err := ghcli.FindPullRequestForBranch(ctx, g.runner, target, branch)
	if err != nil {
		return nil, err
	}
	if pr == nil {
		return nil, nil
	}
	return ghPullRequestToPullRequest(pr), nil
}

func (g *GitHubBackend) GetPullRequestDetails(ctx context.Context, target string, number int) (*PullRequest, error) {
	pr, err := ghcli.GetPullRequestDetails(ctx, g.runner, target, number)
	if err != nil {
		return nil, err
	}
	return ghPullRequestToPullRequest(pr), nil
}

func (g *GitHubBackend) MergePullRequestSquash(ctx context.Context, target string, number int) error {
	return ghcli.MergePullRequestSquash(ctx, g.runner, target, number)
}

func (g *GitHubBackend) ClosePullRequest(ctx context.Context, target string, number int) error {
	return ghcli.ClosePullRequest(ctx, g.runner, target, number)
}

func (g *GitHubBackend) DeleteRemoteBranch(ctx context.Context, repoPath string, branch string) error {
	return ghcli.DeleteRemoteBranch(ctx, g.runner, repoPath, branch)
}

// ---------------------------------------------------------------------------
// RateLimitChecker interface
// ---------------------------------------------------------------------------

func (g *GitHubBackend) CheckRateLimit(ctx context.Context) (*RateLimitSnapshot, error) {
	snapshot, err := ghcli.GetRateLimitSnapshot(ctx, g.runner)
	if err != nil {
		return nil, err
	}
	return ghRateLimitToSnapshot(snapshot), nil
}

// ---------------------------------------------------------------------------
// Type conversions: ghcli → backend
// ---------------------------------------------------------------------------

func ghIssueToWorkItem(issue ghcli.Issue) WorkItem {
	labels := make([]string, len(issue.Labels))
	for i, l := range issue.Labels {
		labels[i] = l.Name
	}
	return WorkItem{
		Number:    issue.Number,
		Title:     issue.Title,
		URL:       issue.URL,
		CreatedAt: issue.CreatedAt,
		Labels:    labels,
	}
}

func ghIssueDetailsToWorkItem(details *ghcli.IssueDetails) *WorkItem {
	labels := make([]string, len(details.Labels))
	for i, l := range details.Labels {
		labels[i] = l.Name
	}
	assignees := make([]string, len(details.Assignees))
	for i, a := range details.Assignees {
		assignees[i] = a.Login
	}
	return &WorkItem{
		Title:     details.Title,
		Body:      details.Body,
		URL:       details.URL,
		State:     details.State,
		Labels:    labels,
		Assignees: assignees,
	}
}

func ghCommentsToComments(ghComments []ghcli.IssueComment) []Comment {
	comments := make([]Comment, len(ghComments))
	for i, c := range ghComments {
		comments[i] = Comment{
			ID:        c.ID,
			Body:      c.Body,
			CreatedAt: c.CreatedAt,
			Author:    c.User.Login,
		}
	}
	return comments
}

func ghPullRequestToPullRequest(pr *ghcli.PullRequest) *PullRequest {
	labels := make([]string, len(pr.Labels))
	for i, l := range pr.Labels {
		labels[i] = l.Name
	}
	checks := make([]StatusCheck, len(pr.StatusCheckRollup))
	for i, c := range pr.StatusCheckRollup {
		checks[i] = StatusCheck{
			Context:    c.Context,
			Name:       c.Name,
			State:      c.State,
			Conclusion: c.Conclusion,
		}
	}
	return &PullRequest{
		Number:           pr.Number,
		Title:            pr.Title,
		Body:             pr.Body,
		URL:              pr.URL,
		State:            pr.State,
		BaseRefName:      pr.BaseRefName,
		MergedAt:         pr.MergedAt,
		Labels:           labels,
		IsDraft:          pr.IsDraft,
		Mergeable:        pr.Mergeable,
		MergeStateStatus: pr.MergeStateStatus,
		ReviewDecision:   pr.ReviewDecision,
		StatusChecks:     checks,
	}
}

func ghRateLimitToSnapshot(snapshot ghcli.RateLimitSnapshot) *RateLimitSnapshot {
	return &RateLimitSnapshot{
		Core:    ghRateLimitResource(snapshot.Core),
		Rate:    ghRateLimitResource(snapshot.Rate),
		GraphQL: ghRateLimitResource(snapshot.GraphQL),
		Search:  ghRateLimitResource(snapshot.Search),
	}
}

func ghRateLimitResource(r ghcli.RateLimitResource) RateLimitResource {
	return RateLimitResource{
		Limit:     r.Limit,
		Remaining: r.Remaining,
		ResetAt:   r.ResetAt,
	}
}

// ---------------------------------------------------------------------------
// FormatRateLimitSnapshot formats a backend RateLimitSnapshot for display,
// mirroring the GitHub-specific formatting.
// ---------------------------------------------------------------------------

func FormatRateLimitSnapshot(snapshot RateLimitSnapshot) string {
	rate := snapshot.Rate
	if rate.Limit == 0 {
		rate = snapshot.Core
	}
	usedCore := snapshot.Core.Limit - snapshot.Core.Remaining
	if usedCore < 0 {
		usedCore = 0
	}
	usedRate := rate.Limit - rate.Remaining
	if usedRate < 0 {
		usedRate = 0
	}
	usedGraphQL := snapshot.GraphQL.Limit - snapshot.GraphQL.Remaining
	if usedGraphQL < 0 {
		usedGraphQL = 0
	}
	usedSearch := snapshot.Search.Limit - snapshot.Search.Remaining
	if usedSearch < 0 {
		usedSearch = 0
	}
	fmtTime := func(t interface{ Format(string) string }) string {
		if ts, ok := t.(interface{ IsZero() bool }); ok && ts.IsZero() {
			return "unknown"
		}
		return t.Format("2006-01-02 15:04:05 -07:00")
	}
	lines := []string{
		"Rate limit snapshot:",
		"",
		fmt.Sprintf("  - core: %d/%d used, %d remaining, resets at %s", usedCore, snapshot.Core.Limit, snapshot.Core.Remaining, fmtTime(snapshot.Core.ResetAt)),
		fmt.Sprintf("  - rate (same as core): %d/%d used, %d remaining, resets at %s", usedRate, rate.Limit, rate.Remaining, fmtTime(rate.ResetAt)),
		fmt.Sprintf("  - graphql: %d/%d used, %d remaining, resets at %s", usedGraphQL, snapshot.GraphQL.Limit, snapshot.GraphQL.Remaining, fmtTime(snapshot.GraphQL.ResetAt)),
		fmt.Sprintf("  - search: %d/%d used, %d remaining, resets at %s", usedSearch, snapshot.Search.Limit, snapshot.Search.Remaining, fmtTime(snapshot.Search.ResetAt)),
	}
	return strings.Join(lines, "\n")
}

// ResolveIssueProviderLabel resolves the coding-agent provider by checking
// work-item labels against registered provider IDs.
func ResolveIssueProviderLabel(labels []string, providerIDs []string) (string, error) {
	matches := make([]string, 0, len(providerIDs))
	for _, id := range providerIDs {
		if HasLabel(labels, id) {
			matches = append(matches, id)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("multiple provider labels: %s", strings.Join(matches, ", "))
	}
}
