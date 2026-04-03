package ghcli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// Type aliases for backward compatibility. The canonical types live in
// internal/backend so the orchestration loop can depend on backend-neutral
// definitions while existing callers continue to use ghcli names.
type Issue = backend.WorkItem
type Label = backend.Label
type PullRequest = backend.PullRequest
type StatusCheckRoll = backend.StatusCheck
type IssueComment = backend.WorkItemComment
type IssueDetails = backend.WorkItemDetails
type IssueUserRef = backend.UserRef
type RepositoryLabelDetails = backend.RepositoryLabelDetails
type RateLimitResource = backend.RateLimitResource
type RateLimitSnapshot = backend.RateLimitSnapshot

type rateLimitAPIResponse struct {
	Resources struct {
		Core    rateLimitAPIResource `json:"core"`
		Rate    rateLimitAPIResource `json:"rate"`
		GraphQL rateLimitAPIResource `json:"graphql"`
		Search  rateLimitAPIResource `json:"search"`
	} `json:"resources"`
}

type rateLimitAPIResource struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

func ListOpenIssues(ctx context.Context, runner environment.Runner, repo string, assignee string) ([]Issue, error) {
	resolvedAssignee, err := ResolveAssignee(ctx, runner, assignee)
	if err != nil {
		return nil, err
	}
	return ListOpenIssuesForAssignee(ctx, runner, repo, resolvedAssignee)
}

func ListOpenIssuesForAssignee(ctx context.Context, runner environment.Runner, repo string, assignee string) ([]Issue, error) {
	args := []string{"issue", "list", "--repo", repo, "--state", "open"}
	if assignee != "" {
		args = append(args, "--assignee", assignee)
	}
	args = append(args, "--json", "number,title,createdAt,url,labels")
	output, err := runner.Run(ctx, "", "gh", args...)

	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &issues); err != nil {
		return nil, fmt.Errorf("parse gh issue list output: %w", err)
	}
	sort.Slice(issues, func(i, j int) bool {
		return issues[i].CreatedAt.Before(issues[j].CreatedAt)
	})
	return issues, nil
}

func ResolveAssignee(ctx context.Context, runner environment.Runner, assignee string) (string, error) {
	if assignee != "me" {
		return assignee, nil
	}

	output, err := runner.Run(ctx, "", "gh", "api", "user", "--jq", ".login")
	if err != nil {
		return "", fmt.Errorf("resolve assignee %q: %w", assignee, err)
	}
	return strings.TrimSpace(output), nil
}

func SelectNextIssue(issues []Issue, sessions []state.Session, target state.WatchTarget) *Issue {
	selected := SelectIssues(issues, sessions, target, 1)
	if len(selected) == 0 {
		return nil
	}
	return &selected[0]
}

func SelectIssues(issues []Issue, sessions []state.Session, target state.WatchTarget, limit int) []Issue {
	if limit <= 0 {
		return nil
	}

	active := map[int]bool{}
	for _, session := range sessions {
		if session.Repo == target.Repo && sessionPreventsRedispatch(session) {
			active[session.IssueNumber] = true
		}
	}

	selected := make([]Issue, 0, limit)
	for i := range issues {
		if len(selected) >= limit {
			break
		}
		if active[issues[i].Number] {
			continue
		}
		if !matchesConfiguredStage(issues[i], target.EffectiveIssueStage()) {
			continue
		}
		if !matchesLabelAllowlist(issues[i], target.Labels) {
			continue
		}
		selected = append(selected, issues[i])
		active[issues[i].Number] = true
	}
	return selected
}

func ActiveSessionCount(sessions []state.Session, target state.WatchTarget) int {
	count := 0
	for _, session := range sessions {
		if session.Repo == target.Repo && sessionConsumesDispatchCapacity(session) {
			count++
		}
	}
	return count
}

func sessionPreventsRedispatch(session state.Session) bool {
	if session.StaleAutoRestartStoppedAt != "" {
		return true
	}
	if sessionConsumesDispatchCapacity(session) || session.Status == state.SessionStatusBlocked {
		return true
	}
	if session.Status != state.SessionStatusSuccess {
		return false
	}
	if session.CleanupCompletedAt != "" || session.MonitoringStoppedAt != "" {
		return false
	}
	return true
}

func sessionConsumesDispatchCapacity(session state.Session) bool {
	return session.Status == state.SessionStatusRunning || session.Status == state.SessionStatusResuming
}

func matchesLabelAllowlist(issue Issue, allowlist []string) bool {
	if len(allowlist) == 0 {
		return true
	}

	for _, configured := range allowlist {
		for _, label := range issue.Labels {
			if label.Name == configured {
				return true
			}
		}
	}
	return false
}

func matchesConfiguredStage(issue Issue, configured string) bool {
	configured = normalizeIssueStage(configured)
	if configured == "" {
		return true
	}
	return normalizeIssueStage(issue.Stage) == configured
}

func normalizeIssueStage(stage string) string {
	stage = strings.TrimSpace(strings.ToLower(stage))
	stage = strings.ReplaceAll(stage, "-", " ")
	stage = strings.Join(strings.Fields(stage), " ")
	return stage
}

func CommentOnIssue(ctx context.Context, runner environment.Runner, repo string, number int, body string) error {
	_, err := runner.Run(ctx, "", "gh", "issue", "comment", "--repo", repo, fmt.Sprintf("%d", number), "--body", SanitizeGitHubVisibleText(body))
	return err
}

func GetRateLimitSnapshot(ctx context.Context, runner environment.Runner) (RateLimitSnapshot, error) {
	output, err := runner.Run(ctx, "", "gh", "api", "/rate_limit")
	if err != nil {
		return RateLimitSnapshot{}, err
	}

	var response rateLimitAPIResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &response); err != nil {
		return RateLimitSnapshot{}, fmt.Errorf("parse gh rate limit output: %w", err)
	}

	snapshot := RateLimitSnapshot{
		Core:    normalizeRateLimitResource(response.Resources.Core),
		Rate:    normalizeRateLimitResource(response.Resources.Rate),
		GraphQL: normalizeRateLimitResource(response.Resources.GraphQL),
		Search:  normalizeRateLimitResource(response.Resources.Search),
	}
	if snapshot.Rate.Limit == 0 {
		snapshot.Rate = snapshot.Core
	}
	return snapshot, nil
}

func normalizeRateLimitResource(resource rateLimitAPIResource) RateLimitResource {
	snapshot := RateLimitResource{
		Limit:     resource.Limit,
		Remaining: resource.Remaining,
	}
	if resource.Reset > 0 {
		snapshot.ResetAt = time.Unix(resource.Reset, 0).Local()
	}
	return snapshot
}

func GetIssueDetails(ctx context.Context, runner environment.Runner, repo string, number int) (*IssueDetails, error) {
	output, err := runner.Run(ctx, "", "gh", "api", issueAPIPath(repo, number))
	if err != nil {
		output = strings.TrimSpace(output)
		if output != "" {
			return nil, fmt.Errorf("%w: %s", err, output)
		}
		return nil, err
	}

	var details IssueDetails
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &details); err != nil {
		return nil, fmt.Errorf("parse gh issue details output: %w", err)
	}
	return &details, nil
}

func ListIssueComments(ctx context.Context, runner environment.Runner, repo string, number int) ([]IssueComment, error) {
	output, err := runner.Run(ctx, "", "gh", "api", issueAPIPath(repo, number)+"/comments")
	if err != nil {
		return nil, err
	}

	var comments []IssueComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &comments); err != nil {
		return nil, fmt.Errorf("parse gh issue comments output: %w", err)
	}
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	return comments, nil
}

func ListIssueCommentsForPolling(ctx context.Context, runner environment.Runner, repo string, number int, purpose string, logger *slog.Logger) ([]IssueComment, error) {
	output, err := runIssueCommentsCommand(ctx, runner, repo, number)
	if err != nil {
		if logger != nil {
			logger.Error("issue comment poll failed", "repo", repo, "issue", number, "purpose", purpose, "err", err, "output", summarizeForLog(output))
		}
		return nil, err
	}

	comments, err := parseIssueComments(output)
	if err != nil {
		if logger != nil {
			logger.Error("issue comment poll parse failed", "repo", repo, "issue", number, "purpose", purpose, "err", err, "output", summarizeForLog(output))
		}
		return nil, err
	}

	if logger != nil {
		logger.Info("issue comment poll", "repo", repo, "issue", number, "purpose", purpose, "comments", len(comments))
	}
	return comments, nil
}

func AddIssueCommentReaction(ctx context.Context, runner environment.Runner, repo string, commentID int64, content string) error {
	_, err := runner.Run(
		ctx,
		"",
		"gh",
		"api",
		"--method", "POST",
		"-H", "Accept: application/vnd.github+json",
		fmt.Sprintf("repos/%s/issues/comments/%d/reactions", repo, commentID),
		"-f", "content="+content,
	)
	return err
}

func RemoveIssueLabel(ctx context.Context, runner environment.Runner, repo string, number int, label string) error {
	_, err := runner.Run(ctx, "", "gh", "issue", "edit", "--repo", repo, fmt.Sprintf("%d", number), "--remove-label", label)
	return err
}

func EnsureRepositoryLabels(ctx context.Context, runner environment.Runner, repo string, desired []RepositoryLabelSpec) error {
	current, err := ListRepositoryLabels(ctx, runner, repo)
	if err != nil {
		return fmt.Errorf("list repository labels: %w", err)
	}

	currentSet := make(map[string]struct{}, len(current))
	for _, label := range current {
		name := strings.TrimSpace(label.Name)
		if name == "" {
			continue
		}
		currentSet[name] = struct{}{}
	}

	for _, label := range desired {
		name := strings.TrimSpace(label.Name)
		if name == "" {
			continue
		}
		if _, ok := currentSet[name]; ok {
			continue
		}
		if err := CreateRepositoryLabel(ctx, runner, repo, label); err != nil {
			return fmt.Errorf("create repository label %q: %w", name, err)
		}
	}

	return nil
}

func ListRepositoryLabels(ctx context.Context, runner environment.Runner, repo string) ([]RepositoryLabelDetails, error) {
	output, err := runner.Run(ctx, "", "gh", "api", fmt.Sprintf("repos/%s/labels?per_page=100", repo))
	if err != nil {
		return nil, err
	}

	var labels []RepositoryLabelDetails
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &labels); err != nil {
		return nil, fmt.Errorf("parse gh repository labels output: %w", err)
	}
	return labels, nil
}

func CreateRepositoryLabel(ctx context.Context, runner environment.Runner, repo string, label RepositoryLabelSpec) error {
	args := []string{
		"api",
		"--method", "POST",
		fmt.Sprintf("repos/%s/labels", repo),
		"-f", "name=" + label.Name,
		"-f", "color=" + label.Color,
	}
	if label.Description != "" {
		args = append(args, "-f", "description="+label.Description)
	}
	_, err := runner.Run(ctx, "", "gh", args...)
	return err
}

func SyncIssueLabels(ctx context.Context, runner environment.Runner, repo string, number int, current []Label, desired []string, managed []string) error {
	toAdd, toRemove := PlanIssueLabelSync(current, desired, managed)
	if len(toAdd) == 0 && len(toRemove) == 0 {
		return nil
	}

	args := []string{"issue", "edit", "--repo", repo, fmt.Sprintf("%d", number)}
	for _, label := range toAdd {
		args = append(args, "--add-label", label)
	}
	for _, label := range toRemove {
		args = append(args, "--remove-label", label)
	}
	_, err := runner.Run(ctx, "", "gh", args...)
	return err
}

func PlanIssueLabelSync(current []Label, desired []string, managed []string) ([]string, []string) {
	managedSet := make(map[string]struct{}, len(managed))
	for _, label := range managed {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		managedSet[label] = struct{}{}
	}

	currentSet := make(map[string]struct{}, len(current))
	for _, label := range current {
		name := strings.TrimSpace(label.Name)
		if name == "" {
			continue
		}
		currentSet[name] = struct{}{}
	}

	desiredSet := make(map[string]struct{}, len(desired))
	for _, label := range desired {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		desiredSet[label] = struct{}{}
	}

	toAdd := make([]string, 0, len(desiredSet))
	for label := range desiredSet {
		if _, ok := currentSet[label]; ok {
			continue
		}
		toAdd = append(toAdd, label)
	}

	toRemove := make([]string, 0, len(managedSet))
	for label := range managedSet {
		if _, ok := currentSet[label]; !ok {
			continue
		}
		if _, ok := desiredSet[label]; ok {
			continue
		}
		toRemove = append(toRemove, label)
	}

	sort.Strings(toAdd)
	sort.Strings(toRemove)
	return toAdd, toRemove
}

func HasAnyLabel(labels []Label, wanted ...string) bool {
	for _, label := range labels {
		for _, candidate := range wanted {
			if label.Name == candidate {
				return true
			}
		}
	}
	return false
}

func FindResumeComment(comments []IssueComment, claimedCommentID int64) *IssueComment {
	return findCommandComment(comments, "@vigilanteai resume", claimedCommentID)
}

func FindCleanupComment(comments []IssueComment, claimedCommentID int64) *IssueComment {
	return findCommandComment(comments, "@vigilanteai cleanup", claimedCommentID)
}

func FindRecreateComment(comments []IssueComment, claimedCommentID int64) *IssueComment {
	return findCommandComment(comments, "@vigilanteai recreate", claimedCommentID)
}

func FindIterationComment(comments []IssueComment, claimedCommentID int64, claimedCommentAt string) *IssueComment {
	claimedAt := parseClaimedCommentTime(claimedCommentAt)
	for i := len(comments) - 1; i >= 0; i-- {
		if !isCommentNewerThanClaim(comments[i], claimedAt, claimedCommentID) {
			continue
		}
		if !IsIterationComment(comments[i]) {
			continue
		}
		return &comments[i]
	}
	return nil
}

func IsIterationComment(comment IssueComment) bool {
	body := normalizeVigilanteComment(comment.Body)
	if !strings.HasPrefix(body, "@vigilanteai") {
		return false
	}
	if IsKnownVigilanteCommandComment(body) {
		return false
	}
	return strings.TrimSpace(strings.TrimPrefix(body, "@vigilanteai")) != ""
}

func IsKnownVigilanteCommandComment(body string) bool {
	switch normalizeVigilanteComment(body) {
	case "@vigilanteai resume", "@vigilanteai cleanup", "@vigilanteai recreate":
		return true
	default:
		return false
	}
}

func AssigneeIterationComments(comments []IssueComment, assignees []string) []IssueComment {
	if len(assignees) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(assignees))
	for _, assignee := range assignees {
		login := strings.TrimSpace(strings.ToLower(assignee))
		if login == "" {
			continue
		}
		allowed[login] = struct{}{}
	}
	selected := make([]IssueComment, 0, len(comments))
	for _, comment := range comments {
		if !IsIterationComment(comment) {
			continue
		}
		if _, ok := allowed[strings.ToLower(strings.TrimSpace(comment.User.Login))]; !ok {
			continue
		}
		selected = append(selected, comment)
	}
	return selected
}

func LatestUserCommentTime(comments []IssueComment) time.Time {
	for i := len(comments) - 1; i >= 0; i-- {
		if IsUserComment(comments[i]) {
			return comments[i].CreatedAt.UTC()
		}
	}
	return time.Time{}
}

func IsUserComment(comment IssueComment) bool {
	body := strings.TrimSpace(comment.Body)
	if body == "" {
		return false
	}
	if strings.HasPrefix(body, "@vigilanteai ") {
		return true
	}
	return !isAutomationComment(body)
}

func findCommandComment(comments []IssueComment, command string, claimedCommentID int64) *IssueComment {
	want := normalizeVigilanteComment(command)
	for i := len(comments) - 1; i >= 0; i-- {
		body := normalizeVigilanteComment(comments[i].Body)
		if body != want {
			continue
		}
		if claimedCommentID != 0 && comments[i].ID == claimedCommentID {
			return nil
		}
		return &comments[i]
	}
	return nil
}

func normalizeVigilanteComment(body string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(body)))
	return strings.Join(fields, " ")
}

func parseClaimedCommentTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func isCommentNewerThanClaim(comment IssueComment, claimedAt time.Time, claimedCommentID int64) bool {
	if claimedCommentID == 0 && claimedAt.IsZero() {
		return true
	}
	commentAt := comment.CreatedAt.UTC()
	if !claimedAt.IsZero() {
		if commentAt.Before(claimedAt) {
			return false
		}
		if commentAt.After(claimedAt) {
			return true
		}
	}
	return comment.ID > claimedCommentID
}

func FindPullRequestForBranch(ctx context.Context, runner environment.Runner, repo string, branch string) (*PullRequest, error) {
	output, err := runner.Run(ctx, "", "gh", "pr", "list", "--repo", repo, "--head", branch, "--state", "all", "--json", "number,url,state,mergedAt")
	if err != nil {
		return nil, err
	}

	var prs []PullRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &prs); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

func GetPullRequestDetails(ctx context.Context, runner environment.Runner, repo string, number int) (*PullRequest, error) {
	output, err := runner.Run(
		ctx,
		"",
		"gh",
		"pr",
		"view",
		"--repo",
		repo,
		fmt.Sprintf("%d", number),
		"--json",
		"number,title,body,url,state,mergedAt,labels,isDraft,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup,baseRefName",
	)
	if err != nil {
		return nil, err
	}

	var pr PullRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &pr); err != nil {
		return nil, fmt.Errorf("parse gh pr view output: %w", err)
	}
	return &pr, nil
}

func IsIssueUnavailableError(err error) bool {
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	if text == "" {
		return false
	}
	return strings.Contains(text, "http 410") ||
		strings.Contains(text, "(410)") ||
		strings.Contains(text, " 410 ") ||
		strings.Contains(text, "gone") ||
		strings.Contains(text, "http 404") ||
		strings.Contains(text, "(404)") ||
		strings.Contains(text, "not found")
}

func MergePullRequestSquash(ctx context.Context, runner environment.Runner, repo string, number int) error {
	_, err := runner.Run(ctx, "", "gh", "pr", "merge", "--repo", repo, fmt.Sprintf("%d", number), "--squash", "--delete-branch")
	return err
}

type CreatedIssue = backend.CreatedWorkItem

func CreateIssue(ctx context.Context, runner environment.Runner, repo string, title string, body string, labels []string, assignees []string) (*CreatedIssue, error) {
	args := []string{"gh", "api", "--method", "POST", "-H", "Accept: application/vnd.github+json", "repos/" + repo + "/issues", "-f", "title=" + title, "-f", "body=" + SanitizeGitHubVisibleText(body)}
	for _, label := range labels {
		args = append(args, "-f", "labels[]="+label)
	}
	for _, assignee := range assignees {
		args = append(args, "-f", "assignees[]="+assignee)
	}
	output, err := runner.Run(ctx, "", args[0], args[1:]...)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	var created CreatedIssue
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &created); err != nil {
		return nil, fmt.Errorf("parse created issue: %w", err)
	}
	return &created, nil
}

func CloseIssueNotPlanned(ctx context.Context, runner environment.Runner, repo string, number int) error {
	_, err := runner.Run(ctx, "", "gh", "api", "--method", "PATCH", "-H", "Accept: application/vnd.github+json",
		issueAPIPath(repo, number), "-f", "state=closed", "-f", "state_reason=not_planned")
	return err
}

func ClosePullRequest(ctx context.Context, runner environment.Runner, repo string, number int) error {
	_, err := runner.Run(ctx, "", "gh", "pr", "close", "--repo", repo, fmt.Sprintf("%d", number))
	return err
}

func DeleteRemoteBranch(ctx context.Context, runner environment.Runner, repoPath string, remote string, branch string) error {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}
	_, err := runner.Run(ctx, repoPath, "git", "push", remote, "--delete", branch)
	return err
}

func issueAPIPath(repo string, number int) string {
	return "repos/" + repo + "/issues/" + fmt.Sprintf("%d", number)
}

func runIssueCommentsCommand(ctx context.Context, runner environment.Runner, repo string, number int) (string, error) {
	path := issueAPIPath(repo, number) + "/comments"
	switch typed := runner.(type) {
	case environment.LoggingRunner:
		return typed.Base.Run(ctx, "", "gh", "api", path)
	case *environment.LoggingRunner:
		return typed.Base.Run(ctx, "", "gh", "api", path)
	default:
		return runner.Run(ctx, "", "gh", "api", path)
	}
}

func parseIssueComments(output string) ([]IssueComment, error) {
	var comments []IssueComment
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &comments); err != nil {
		return nil, fmt.Errorf("parse gh issue comments output: %w", err)
	}
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	return comments, nil
}

func summarizeForLog(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "<empty>"
	}
	const limit = 300
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "...(truncated)"
}

func isAutomationComment(body string) bool {
	if !strings.HasPrefix(body, "## ") {
		return false
	}
	if strings.Contains(body, "\nProgress: [") && strings.Contains(body, "\n`ETA: ~") {
		return true
	}
	if strings.Contains(body, "\nWorking branch: `") || strings.Contains(body, "\nETA: ~") {
		return true
	}
	return false
}

// PullRequestFile represents a file changed in a pull request.
type PullRequestFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"` // added, removed, modified, renamed, etc.
}

// ListPullRequestFiles returns the files changed in a pull request.
func ListPullRequestFiles(ctx context.Context, runner environment.Runner, repo string, number int) ([]PullRequestFile, error) {
	output, err := runner.Run(ctx, "", "gh", "api", fmt.Sprintf("repos/%s/pulls/%d/files", repo, number), "--paginate")
	if err != nil {
		return nil, fmt.Errorf("list pr files: %w", err)
	}
	var files []PullRequestFile
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &files); err != nil {
		return nil, fmt.Errorf("parse pr files: %w", err)
	}
	return files, nil
}

// ListOpenPullRequests returns open pull requests for a repository.
func ListOpenPullRequests(ctx context.Context, runner environment.Runner, repo string) ([]PullRequest, error) {
	output, err := runner.Run(ctx, "", "gh", "pr", "list", "--repo", repo, "--state", "open", "--json", "number,title,url,labels,baseRefName")
	if err != nil {
		return nil, fmt.Errorf("list open prs: %w", err)
	}
	var prs []PullRequest
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &prs); err != nil {
		return nil, fmt.Errorf("parse pr list: %w", err)
	}
	return prs, nil
}

// AddPullRequestLabel adds a label to a pull request. On GitHub, PR labels
// use the same issue label API.
func AddPullRequestLabel(ctx context.Context, runner environment.Runner, repo string, number int, label string) error {
	_, err := runner.Run(ctx, "", "gh", "pr", "edit", "--repo", repo, fmt.Sprintf("%d", number), "--add-label", label)
	return err
}

// CommentOnPullRequest posts a comment on a pull request. On GitHub, PR
// comments use the same issue comment API.
func CommentOnPullRequest(ctx context.Context, runner environment.Runner, repo string, number int, body string) error {
	return CommentOnIssue(ctx, runner, repo, number, body)
}

// ListPullRequestComments returns comments on a pull request. On GitHub,
// PR issue comments use the same API as issue comments.
func ListPullRequestComments(ctx context.Context, runner environment.Runner, repo string, number int) ([]IssueComment, error) {
	output, err := runIssueCommentsCommand(ctx, runner, repo, number)
	if err != nil {
		return nil, err
	}
	return parseIssueComments(output)
}

// AddPullRequestCommentReaction adds a reaction emoji to a PR comment.
func AddPullRequestCommentReaction(ctx context.Context, runner environment.Runner, repo string, commentID int64, content string) error {
	return AddIssueCommentReaction(ctx, runner, repo, commentID, content)
}
