package app

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/provider"
)

// reportCSVHeaders defines the stable column order for the report CSV.
var reportCSVHeaders = []string{
	"repository",
	"issue_number",
	"issue_title",
	"issue_url",
	"issue_closed_at",
	"coding_agent",
	"session_start_comment_timestamp",
	"pr_opened_comment_timestamp",
	"session_start_to_pr_opened_seconds",
	"linked_pr_number",
	"linked_pr_url",
	"pr_commit_count",
	"pr_files_changed",
	"pr_lines_added",
	"first_word_vigilante_comment_count",
}

// closedIssue is a GitHub issue in closed state with labels and timing metadata.
type closedIssue struct {
	Number   int             `json:"number"`
	Title    string          `json:"title"`
	URL      string          `json:"html_url"`
	ClosedAt string          `json:"closed_at"`
	Labels   []backend.Label `json:"labels"`
}

// linkedPREvent is a timeline event that references a cross-referenced PR.
type linkedPREvent struct {
	Event  string `json:"event"`
	Source struct {
		Issue struct {
			Number      int    `json:"number"`
			URL         string `json:"html_url"`
			PullRequest *struct {
				URL string `json:"html_url"`
			} `json:"pull_request"`
		} `json:"issue"`
	} `json:"source"`
}

// prDiffStats contains commit count, files changed, and lines added for a PR.
type prDiffStats struct {
	Commits    int
	Files      int
	LinesAdded int
}

func (a *App) runReportCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	configureFlagSet(fs, func(w io.Writer) {
		fmt.Fprintln(w, "usage: vigilante report --repo <owner/name>")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Analyze closed issues and emit a CSV report with execution timing,")
		fmt.Fprintln(w, "coding-agent attribution, linked PR metadata, and operator interaction volume.")
		fmt.Fprintln(w)
		fs.SetOutput(w)
		fs.PrintDefaults()
	})
	repoSlug := fs.String("repo", "", "repository slug (owner/name)")
	if err := parseFlagSet(fs, args, a.stdout); err != nil {
		if errors.Is(err, errHelpHandled) {
			return nil
		}
		return err
	}
	if *repoSlug == "" {
		return errors.New("usage: vigilante report --repo <owner/name>")
	}

	return a.generateReport(ctx, *repoSlug, a.stdout)
}

func (a *App) generateReport(ctx context.Context, repoSlug string, w io.Writer) error {
	runner := a.resolveRunner()

	// Resolve default provider from watch target if available.
	defaultProvider := ""
	targets, err := a.state.LoadWatchTargets()
	if err == nil {
		if target, ok := findWatchTargetByRepo(targets, repoSlug); ok {
			defaultProvider = target.Provider
		}
	}

	return generateReportWithRunner(ctx, runner, repoSlug, defaultProvider, w)
}

func generateReportWithRunner(ctx context.Context, runner environment.Runner, repoSlug string, defaultProvider string, w io.Writer) error {
	issues, err := listClosedIssues(ctx, runner, repoSlug)
	if err != nil {
		return fmt.Errorf("list closed issues: %w", err)
	}

	cw := csv.NewWriter(w)
	if err := cw.Write(reportCSVHeaders); err != nil {
		return err
	}

	for _, issue := range issues {
		row, err := buildReportRow(ctx, runner, repoSlug, issue, defaultProvider)
		if err != nil {
			return fmt.Errorf("issue #%d: %w", issue.Number, err)
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}

	cw.Flush()
	return cw.Error()
}

func buildReportRow(ctx context.Context, runner environment.Runner, repoSlug string, issue closedIssue, defaultProvider string) ([]string, error) {
	comments, err := listIssueCommentsForReport(ctx, runner, repoSlug, issue.Number)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}

	// Find timing markers.
	sessionStart, prOpened := findTimingMarkers(comments)

	// Compute duration.
	sessionStartTS := ""
	prOpenedTS := ""
	durationSeconds := ""
	if sessionStart != nil {
		sessionStartTS = sessionStart.CreatedAt.UTC().Format(time.RFC3339)
	}
	if prOpened != nil {
		prOpenedTS = prOpened.CreatedAt.UTC().Format(time.RFC3339)
	}
	if sessionStart != nil && prOpened != nil {
		seconds := prOpened.CreatedAt.Sub(sessionStart.CreatedAt).Seconds()
		durationSeconds = strconv.FormatFloat(seconds, 'f', 0, 64)
	}

	// Resolve coding agent: issue labels take precedence over default provider.
	codingAgent := resolveCodingAgent(issue.Labels, defaultProvider)

	// Count @vigilanteai first-word comments.
	vigilanteCount := countFirstWordVigilanteComments(comments)

	// Find linked PR.
	prNumber, prURL, stats := findLinkedPR(ctx, runner, repoSlug, issue.Number, comments)

	return []string{
		repoSlug,
		strconv.Itoa(issue.Number),
		issue.Title,
		issue.URL,
		issue.ClosedAt,
		codingAgent,
		sessionStartTS,
		prOpenedTS,
		durationSeconds,
		prNumber,
		prURL,
		stats.commitsStr(),
		stats.filesStr(),
		stats.linesAddedStr(),
		strconv.Itoa(vigilanteCount),
	}, nil
}

// resolveRunner returns the environment runner from the app, preferring the
// logging runner base when available.
func (a *App) resolveRunner() environment.Runner {
	if a.env != nil {
		return a.env.Runner
	}
	return environment.ExecRunner{}
}

// listClosedIssues fetches all closed issues for a repository via the GitHub API.
// It paginates through all pages to collect the full set.
func listClosedIssues(ctx context.Context, runner environment.Runner, repo string) ([]closedIssue, error) {
	var all []closedIssue
	page := 1
	for {
		path := fmt.Sprintf("repos/%s/issues?state=closed&per_page=100&page=%d", repo, page)
		output, err := runner.Run(ctx, "", "gh", "api", path)
		if err != nil {
			return nil, err
		}

		var batch []closedIssue
		if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &batch); err != nil {
			return nil, fmt.Errorf("parse closed issues page %d: %w", page, err)
		}
		if len(batch) == 0 {
			break
		}
		// Filter out pull requests that appear in the issues endpoint.
		for _, issue := range batch {
			all = append(all, issue)
		}
		if len(batch) < 100 {
			break
		}
		page++
	}
	// Sort by issue number ascending for stable output.
	sort.Slice(all, func(i, j int) bool {
		return all[i].Number < all[j].Number
	})
	return all, nil
}

// listIssueCommentsForReport fetches all comments for an issue, paginating.
func listIssueCommentsForReport(ctx context.Context, runner environment.Runner, repo string, number int) ([]backend.WorkItemComment, error) {
	var all []backend.WorkItemComment
	page := 1
	for {
		path := fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100&page=%d", repo, number, page)
		output, err := runner.Run(ctx, "", "gh", "api", path)
		if err != nil {
			return nil, err
		}

		var batch []backend.WorkItemComment
		if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &batch); err != nil {
			return nil, fmt.Errorf("parse comments page %d: %w", page, err)
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
		page++
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	return all, nil
}

// findTimingMarkers scans comments for the first "Vigilante Session Start" and
// the first subsequent "PR Opened" comment.
func findTimingMarkers(comments []backend.WorkItemComment) (sessionStart *backend.WorkItemComment, prOpened *backend.WorkItemComment) {
	for i := range comments {
		if sessionStart == nil && strings.Contains(comments[i].Body, "Vigilante Session Start") {
			sessionStart = &comments[i]
			continue
		}
		if sessionStart != nil && prOpened == nil && strings.Contains(comments[i].Body, "PR Opened") {
			prOpened = &comments[i]
			break
		}
	}
	return sessionStart, prOpened
}

// resolveCodingAgent determines the coding agent for an issue.
// Issue labels take precedence when they unambiguously identify a provider.
// Falls back to the default repo provider when labels are absent or ambiguous.
func resolveCodingAgent(labels []backend.Label, defaultProvider string) string {
	labelProvider, err := provider.ResolveIssueLabel(labels)
	if err == nil && labelProvider != "" {
		return labelProvider
	}
	return defaultProvider
}

// countFirstWordVigilanteComments counts comments where the first
// whitespace-delimited token is exactly "@vigilanteai".
func countFirstWordVigilanteComments(comments []backend.WorkItemComment) int {
	count := 0
	for _, c := range comments {
		body := strings.TrimSpace(c.Body)
		if body == "" {
			continue
		}
		fields := strings.Fields(body)
		if len(fields) > 0 && fields[0] == "@vigilanteai" {
			count++
		}
	}
	return count
}

// prStats holds optional PR statistics for CSV output.
type prStats struct {
	found      bool
	commits    int
	files      int
	linesAdded int
}

func (s prStats) commitsStr() string {
	if !s.found {
		return ""
	}
	return strconv.Itoa(s.commits)
}

func (s prStats) filesStr() string {
	if !s.found {
		return ""
	}
	return strconv.Itoa(s.files)
}

func (s prStats) linesAddedStr() string {
	if !s.found {
		return ""
	}
	return strconv.Itoa(s.linesAdded)
}

// findLinkedPR finds the first PR linked to an issue via timeline events.
// Selection rule: the first cross-referenced PR chronologically in the timeline.
// If timeline lookup fails, returns empty fields.
func findLinkedPR(ctx context.Context, runner environment.Runner, repo string, issueNumber int, comments []backend.WorkItemComment) (prNumber string, prURL string, stats prStats) {
	// Try timeline events first to find cross-referenced PRs.
	path := fmt.Sprintf("repos/%s/issues/%d/timeline?per_page=100", repo, issueNumber)
	output, err := runner.Run(ctx, "", "gh", "api", "-H", "Accept: application/vnd.github.mockingbird-preview+json", path)
	if err == nil {
		var events []linkedPREvent
		if json.Unmarshal([]byte(strings.TrimSpace(output)), &events) == nil {
			for _, evt := range events {
				if evt.Event != "cross-referenced" {
					continue
				}
				if evt.Source.Issue.PullRequest == nil {
					continue
				}
				num := evt.Source.Issue.Number
				url := evt.Source.Issue.URL
				if url == "" {
					url = evt.Source.Issue.PullRequest.URL
				}
				diff := fetchPRDiffStats(ctx, runner, repo, num)
				return strconv.Itoa(num), url, diff
			}
		}
	}

	// Fallback: look for PR number mentioned in "PR Opened" comment body.
	for _, c := range comments {
		if !strings.Contains(c.Body, "PR Opened") {
			continue
		}
		if num := extractPRNumberFromComment(c.Body, repo); num > 0 {
			url := fmt.Sprintf("https://github.com/%s/pull/%d", repo, num)
			diff := fetchPRDiffStats(ctx, runner, repo, num)
			return strconv.Itoa(num), url, diff
		}
	}

	return "", "", prStats{}
}

// extractPRNumberFromComment attempts to find a PR number in a comment body.
// Looks for patterns like "#123" or "pull/123".
func extractPRNumberFromComment(body string, repo string) int {
	// Look for "pull/<number>" pattern.
	idx := strings.Index(body, repo+"/pull/")
	if idx >= 0 {
		rest := body[idx+len(repo)+len("/pull/"):]
		numStr := ""
		for _, ch := range rest {
			if ch >= '0' && ch <= '9' {
				numStr += string(ch)
			} else {
				break
			}
		}
		if n, err := strconv.Atoi(numStr); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// fetchPRDiffStats fetches commit count, files changed, and lines added for a PR.
func fetchPRDiffStats(ctx context.Context, runner environment.Runner, repo string, number int) prStats {
	path := fmt.Sprintf("repos/%s/pulls/%d", repo, number)
	output, err := runner.Run(ctx, "", "gh", "api", path)
	if err != nil {
		return prStats{found: true}
	}

	var pr struct {
		Commits   int `json:"commits"`
		Changed   int `json:"changed_files"`
		Additions int `json:"additions"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &pr); err != nil {
		return prStats{found: true}
	}
	return prStats{
		found:      true,
		commits:    pr.Commits,
		files:      pr.Changed,
		linesAdded: pr.Additions,
	}
}
