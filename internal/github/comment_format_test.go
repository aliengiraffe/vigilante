package ghcli

import (
	"strings"
	"testing"
)

func TestFormatProgressComment(t *testing.T) {
	comment := FormatProgressComment(ProgressComment{
		Stage:      "Validation Passed",
		Emoji:      "✅",
		Percent:    90,
		ETAMinutes: 5,
		Items: []string{
			"Ran `go test ./...`.",
			"Pushed `vigilante/issue-12`.",
		},
		Tagline: "Success is where preparation and opportunity meet.",
	})

	expected := "## ✅ Validation Passed\nProgress: [#########-] 90%\nETA: ~5 minutes\n- Ran `go test ./...`.\n- Pushed `vigilante/issue-12`.\n> \"Success is where preparation and opportunity meet.\""
	if comment != expected {
		t.Fatalf("unexpected comment:\n%s", comment)
	}
}

func TestFormatProgressCommentProgressBarAcrossPercentages(t *testing.T) {
	tests := []struct {
		name    string
		percent int
		want    string
	}{
		{name: "low", percent: 0, want: "Progress: [----------] 0%"},
		{name: "mid", percent: 50, want: "Progress: [#####-----] 50%"},
		{name: "high", percent: 100, want: "Progress: [##########] 100%"},
		{name: "clamped low", percent: -5, want: "Progress: [----------] 0%"},
		{name: "clamped high", percent: 135, want: "Progress: [##########] 100%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comment := FormatProgressComment(ProgressComment{
				Stage:      "Working",
				Percent:    tt.percent,
				ETAMinutes: 2,
			})

			lines := strings.Split(comment, "\n")
			if lines[1] != tt.want {
				t.Fatalf("unexpected progress line: got %q want %q", lines[1], tt.want)
			}
		})
	}
}
