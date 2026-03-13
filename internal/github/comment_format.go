package ghcli

import (
	"fmt"
	"strings"
)

type ProgressComment struct {
	Stage      string
	Emoji      string
	Percent    int
	ETAMinutes int
	Items      []string
	Tagline    string
}

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

type DispatchFailureComment struct {
	Stage        string
	Summary      string
	Branch       string
	WorktreePath string
	NextStep     string
}

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
