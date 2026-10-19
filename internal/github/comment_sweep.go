package ghcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// repoSweepPageSize is the largest page GitHub serves for comment listings.
const repoSweepPageSize = 100

// RepoComment is one comment returned by a repository-wide sweep, tagged with
// the issue or pull request it belongs to and the surface it was read from.
type RepoComment struct {
	IssueComment
	UpdatedAt time.Time
	// Number is the issue or pull request number the comment belongs to. Issue
	// and pull request numbers share one sequence per repository.
	Number int
	// Surface is "issue" for issue and PR conversation comments, "review" for
	// inline pull request review comments.
	Surface string
}

// RepoCommentSweep is the result of one repository sweep.
type RepoCommentSweep struct {
	Comments []RepoComment
	Cursor   state.CommentSweepCursor
	// Requests counts HTTP requests made; NotModified counts those answered 304.
	Requests    int
	NotModified int
}

type sweptIssueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	IssueURL       string `json:"issue_url"`
	PullRequestURL string `json:"pull_request_url"`
}

// SweepRepoComments reads every issue, pull request conversation, and pull
// request review comment in repo updated since the cursor using two conditional
// list requests (plus pagination when a page is full). It replaces per-issue
// comment polling so command detection costs O(repositories), not O(sessions).
// initialSince is used when the cursor has no position yet.
func SweepRepoComments(ctx context.Context, runner environment.Runner, repo string, cursor state.CommentSweepCursor, initialSince time.Time) (RepoCommentSweep, error) {
	result := RepoCommentSweep{Cursor: cursor}
	since := strings.TrimSpace(cursor.Since)
	if since == "" {
		since = initialSince.UTC().Format(time.RFC3339)
		result.Cursor.Since = since
	}
	latest, err := time.Parse(time.RFC3339, since)
	if err != nil {
		latest = initialSince.UTC()
		since = latest.Format(time.RFC3339)
		result.Cursor.Since = since
	}

	issuePath := fmt.Sprintf("repos/%s/issues/comments?since=%s&sort=updated&direction=asc&per_page=%d", repo, since, repoSweepPageSize)
	issueComments, issueETag, err := sweepSurface(ctx, runner, issuePath, cursor.IssueCommentsETag, &result)
	if err != nil {
		return result, fmt.Errorf("sweep issue comments: %w", err)
	}
	if issueETag != "" {
		result.Cursor.IssueCommentsETag = issueETag
	}
	reviewPath := fmt.Sprintf("repos/%s/pulls/comments?since=%s&sort=updated&direction=asc&per_page=%d", repo, since, repoSweepPageSize)
	reviewComments, reviewETag, err := sweepSurface(ctx, runner, reviewPath, cursor.ReviewCommentsETag, &result)
	if err != nil {
		return result, fmt.Errorf("sweep review comments: %w", err)
	}
	if reviewETag != "" {
		result.Cursor.ReviewCommentsETag = reviewETag
	}

	for _, raw := range issueComments {
		if comment, ok := toRepoComment(raw, "issue"); ok {
			result.Comments = append(result.Comments, comment)
		}
	}
	for _, raw := range reviewComments {
		if comment, ok := toRepoComment(raw, "review"); ok {
			result.Comments = append(result.Comments, comment)
		}
	}
	sort.SliceStable(result.Comments, func(i, j int) bool {
		return result.Comments[i].CreatedAt.Before(result.Comments[j].CreatedAt)
	})
	// Advance the cursor only when comments arrived so the request URL, and thus
	// the ETag, stays stable while nothing changes. GitHub's `since` is
	// inclusive; comments at exactly this instant are returned again and are
	// deduplicated by callers through comment IDs.
	advanced := false
	for _, comment := range result.Comments {
		if comment.UpdatedAt.After(latest) {
			latest = comment.UpdatedAt
			advanced = true
		}
	}
	if advanced {
		result.Cursor.Since = latest.UTC().Format(time.RFC3339)
		// The ETags belonged to the previous URL.
		result.Cursor.IssueCommentsETag = ""
		result.Cursor.ReviewCommentsETag = ""
	}
	return result, nil
}

func toRepoComment(raw sweptIssueComment, surface string) (RepoComment, bool) {
	source := raw.IssueURL
	if surface == "review" {
		source = raw.PullRequestURL
	}
	number := trailingNumber(source)
	if number <= 0 {
		return RepoComment{}, false
	}
	comment := RepoComment{UpdatedAt: raw.UpdatedAt, Number: number, Surface: surface}
	comment.ID = raw.ID
	comment.Body = raw.Body
	comment.CreatedAt = raw.CreatedAt
	comment.User.Login = raw.User.Login
	if comment.UpdatedAt.IsZero() {
		comment.UpdatedAt = comment.CreatedAt
	}
	return comment, true
}

func trailingNumber(url string) int {
	url = strings.TrimSuffix(strings.TrimSpace(url), "/")
	idx := strings.LastIndex(url, "/")
	if idx < 0 {
		return 0
	}
	number, err := strconv.Atoi(url[idx+1:])
	if err != nil {
		return 0
	}
	return number
}

// sweepSurface fetches every page for one endpoint. The first page is a
// conditional request when an ETag is known; a 304 answer means no changes.
func sweepSurface(ctx context.Context, runner environment.Runner, path string, etag string, result *RepoCommentSweep) ([]sweptIssueComment, string, error) {
	runner = quietRunner(runner)
	var all []sweptIssueComment
	newETag := ""
	for page := 1; ; page++ {
		endpoint := path
		if page > 1 {
			endpoint = fmt.Sprintf("%s&page=%d", path, page)
		}
		args := []string{"api", endpoint, "--include"}
		if page == 1 && etag != "" {
			args = append(args, "-H", "If-None-Match: "+etag)
		}
		pageCtx, sink := environment.WithResponseHeaders(ctx)
		output, err := runner.Run(pageCtx, "", "gh", args...)
		result.Requests++
		status, pageETag, body := splitResponse(output)
		if status == 0 {
			status, pageETag = sink.Snapshot()
		}
		// `gh api` exits non-zero for 304 Not Modified; the status says otherwise.
		if err != nil && status != http.StatusNotModified {
			return nil, "", err
		}
		if page == 1 && pageETag != "" {
			newETag = pageETag
		}
		if status == http.StatusNotModified {
			result.NotModified++
			return nil, newETag, nil
		}
		var items []sweptIssueComment
		if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &items); err != nil {
			return nil, "", fmt.Errorf("parse comment sweep page: %w", err)
		}
		all = append(all, items...)
		if len(items) < repoSweepPageSize {
			break
		}
	}
	return all, newETag, nil
}

// splitResponse separates `gh api --include` output into status, ETag, and body
// when the headers are still present. The budget runner strips headers for
// background calls and reports them through the context sink instead; in that
// case status is 0 and body is the output unchanged.
func splitResponse(output string) (int, string, string) {
	if !strings.HasPrefix(output, "HTTP/") {
		return 0, "", output
	}
	response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(output)), nil)
	if err != nil {
		return 0, "", output
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", output
	}
	return response.StatusCode, response.Header.Get("ETag"), string(body)
}

// quietRunner suppresses verbose command logging while keeping access logging
// and the quota guard, matching fetchCommentPages.
func quietRunner(runner environment.Runner) environment.Runner {
	switch r := runner.(type) {
	case environment.LoggingRunner:
		r.Logger = nil
		r.LogSuccessOutput = false
		return r
	case *environment.LoggingRunner:
		copy := *r
		copy.Logger = nil
		copy.LogSuccessOutput = false
		return copy
	}
	return runner
}
