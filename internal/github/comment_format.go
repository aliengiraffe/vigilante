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
		fmt.Sprintf("`%s %d%%`", progressBar(comment.Percent), clampPercent(comment.Percent)),
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

func progressBar(percent int) string {
	percent = clampPercent(percent)
	filled := percent / 10
	return strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
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
