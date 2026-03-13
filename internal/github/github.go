package ghcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type Issue struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	URL       string    `json:"url"`
	Labels    []Label   `json:"labels"`
}

type Label struct {
	Name string `json:"name"`
}

type PullRequest struct {
	Number   int        `json:"number"`
	URL      string     `json:"url"`
	State    string     `json:"state"`
	MergedAt *time.Time `json:"mergedAt"`
}

type IssueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

type IssueDetails struct {
	Labels []Label `json:"labels"`
}

func ListOpenIssues(ctx context.Context, runner environment.Runner, repo string, assignee string) ([]Issue, error) {
	resolvedAssignee, err := resolveAssignee(ctx, runner, assignee)
	if err != nil {
		return nil, err
	}

	args := []string{"issue", "list", "--repo", repo, "--state", "open"}
	if resolvedAssignee != "" {
		args = append(args, "--assignee", resolvedAssignee)
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

func resolveAssignee(ctx context.Context, runner environment.Runner, assignee string) (string, error) {
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

func CommentOnIssue(ctx context.Context, runner environment.Runner, repo string, number int, body string) error {
	_, err := runner.Run(ctx, "", "gh", "issue", "comment", "--repo", repo, fmt.Sprintf("%d", number), "--body", body)
	return err
}

func GetIssueDetails(ctx context.Context, runner environment.Runner, repo string, number int) (*IssueDetails, error) {
	output, err := runner.Run(ctx, "", "gh", "api", issueAPIPath(repo, number))
	if err != nil {
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

func ListIssueCommentsForPolling(ctx context.Context, runner environment.Runner, repo string, number int, purpose string, logf func(format string, args ...any)) ([]IssueComment, error) {
	output, err := runIssueCommentsCommand(ctx, runner, repo, number)
	if err != nil {
		if logf != nil {
			logf("issue comment poll failed repo=%s issue=%d purpose=%s err=%v output=%s", repo, number, purpose, err, summarizeForLog(output))
		}
		return nil, err
	}

	comments, err := parseIssueComments(output)
	if err != nil {
		if logf != nil {
			logf("issue comment poll parse failed repo=%s issue=%d purpose=%s err=%v output=%s", repo, number, purpose, err, summarizeForLog(output))
		}
		return nil, err
	}

	if logf != nil {
		logf("issue comment poll repo=%s issue=%d purpose=%s comments=%d", repo, number, purpose, len(comments))
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
	for i := len(comments) - 1; i >= 0; i-- {
		body := strings.TrimSpace(comments[i].Body)
		if body != command {
			continue
		}
		if claimedCommentID != 0 && comments[i].ID == claimedCommentID {
			return nil
		}
		return &comments[i]
	}
	return nil
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
