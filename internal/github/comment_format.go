package ghcli

import (
	"fmt"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
)

type ProgressComment = backend.ProgressComment

func FormatProgressComment(comment ProgressComment) string {
	header := strings.TrimSpace(comment.Stage)
	if emoji := strings.TrimSpace(comment.Emoji); emoji != "" {
		header = strings.TrimSpace(fmt.Sprintf("%s %s", emoji, header))
	}

	lines := []string{
		fmt.Sprintf("## %s", header),
		progressLine(comment.Percent),
		fmt.Sprintf("`ETA: ~%s`", formatMinutes(comment.ETAMinutes)),
	}
	for _, item := range comment.Items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s", item))
	}
	if tagline := strings.TrimSpace(comment.Tagline); tagline != "" {
		lines = append(lines, fmt.Sprintf("> %q", tagline))
	}
	return strings.Join(lines, "\n")
}

type DispatchFailureComment = backend.DispatchFailureComment

func FormatDispatchFailureComment(comment DispatchFailureComment) string {
	stage := strings.TrimSpace(comment.Stage)
	if stage == "" {
		stage = "dispatch"
	}
	stage = strings.ReplaceAll(stage, "_", " ")

	summary := strings.TrimSpace(comment.Summary)
	if summary == "" {
		summary = "Vigilante hit a local failure before implementation could proceed."
	}

	items := []string{
		fmt.Sprintf("Failure stage: `%s`. Summary: `%s`.", stage, summary),
	}
	if branch := strings.TrimSpace(comment.Branch); branch != "" || strings.TrimSpace(comment.WorktreePath) != "" {
		items = append(items, fmt.Sprintf("Branch: `%s`. Worktree: `%s`.", fallbackCommentValue(branch, "not created"), fallbackCommentValue(comment.WorktreePath, "not created")))
	}
	if next := strings.TrimSpace(comment.NextStep); next != "" {
		items = append(items, fmt.Sprintf("Next step: %s", next))
	}

	return FormatProgressComment(ProgressComment{
		Stage:      "Blocked",
		Emoji:      "🧱",
		Percent:    15,
		ETAMinutes: 10,
		Items:      items,
		Tagline:    "No silent stalls.",
	})
}

func FormatGitHubRateLimitDelayComment(snapshot RateLimitSnapshot, threshold int, now time.Time) string {
	lines := []string{
		"## ⏸️ GitHub Delay Window",
		progressLine(70),
		fmt.Sprintf("`ETA: ~%s`", formatMinutes(minutesUntil(now, snapshot.Core.ResetAt))),
		fmt.Sprintf("- GitHub REST core quota fell below the safety threshold (`%d` remaining), so Vigilante is pausing GitHub-backed work for this issue.", threshold),
		fmt.Sprintf("- Automatic resume is scheduled after `%s`.", formatAbsoluteTime(snapshot.Core.ResetAt)),
		"- Vigilante will resume automatically after the GitHub-provided reset time without manual intervention.",
		"",
		FormatGitHubRateLimitSnapshot(snapshot),
		"",
		"> \"Waiting beats failing at the limit.\"",
	}
	return strings.Join(lines, "\n")
}

func FormatGitHubRateLimitSnapshot(snapshot RateLimitSnapshot) string {
	rate := snapshot.Rate
	if rate.Limit == 0 {
		rate = snapshot.Core
	}
	lines := []string{
		"gh api /rate_limit returned:",
		"",
		fmt.Sprintf("  - core: %d/%d used, %d remaining, resets at %s", usedRequests(snapshot.Core), snapshot.Core.Limit, snapshot.Core.Remaining, formatAbsoluteTime(snapshot.Core.ResetAt)),
		fmt.Sprintf("  - rate (same as core): %d/%d used, %d remaining, resets at %s", usedRequests(rate), rate.Limit, rate.Remaining, formatAbsoluteTime(rate.ResetAt)),
		fmt.Sprintf("  - graphql: %d/%d used, %d remaining, resets at %s", usedRequests(snapshot.GraphQL), snapshot.GraphQL.Limit, snapshot.GraphQL.Remaining, formatAbsoluteTime(snapshot.GraphQL.ResetAt)),
		fmt.Sprintf("  - search: %d/%d used, %d remaining, resets at %s", usedRequests(snapshot.Search), snapshot.Search.Limit, snapshot.Search.Remaining, formatAbsoluteTime(snapshot.Search.ResetAt)),
	}
	return strings.Join(lines, "\n")
}

func fallbackCommentValue(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func progressLine(percent int) string {
	percent = clampPercent(percent)
	return fmt.Sprintf("Progress: [%s] %d%%", progressBar(percent), percent)
}

func progressBar(percent int) string {
	percent = clampPercent(percent)
	filled := percent / 10
	return strings.Repeat("#", filled) + strings.Repeat("-", 10-filled)
}

func clampPercent(percent int) int {
	switch {
	case percent < 0:
		return 0
	case percent > 100:
		return 100
	default:
		return percent
	}
}

func formatMinutes(minutes int) string {
	if minutes <= 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}

func usedRequests(resource RateLimitResource) int {
	used := resource.Limit - resource.Remaining
	if used < 0 {
		return 0
	}
	return used
}

func formatAbsoluteTime(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.Format("2006-01-02 15:04:05 -07:00")
}

func minutesUntil(now time.Time, resetAt time.Time) int {
	if resetAt.IsZero() {
		return 1
	}
	if !resetAt.After(now) {
		return 1
	}
	duration := resetAt.Sub(now)
	minutes := int(duration / time.Minute)
	if duration%time.Minute != 0 {
		minutes++
	}
	if minutes < 1 {
		return 1
	}
	return minutes
}
