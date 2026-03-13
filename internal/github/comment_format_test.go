package ghcli

import (
	"strings"
	"testing"
)

func TestFormatProgressComment(t *testing.T) {
	t.Run("formats full comment with markdown-safe progress line", func(t *testing.T) {
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

		expected := "## ✅ Validation Passed\nProgress: [#########-] 90%\n`ETA: ~5 minutes`\n- Ran `go test ./...`.\n- Pushed `vigilante/issue-12`.\n> \"Success is where preparation and opportunity meet.\""
		if comment != expected {
			t.Fatalf("unexpected comment:\n%s", comment)
		}
	})

	t.Run("supports multiple percentages", func(t *testing.T) {
		cases := []struct {
			name    string
			percent int
			want    string
		}{
			{name: "low", percent: 0, want: "Progress: [----------] 0%"},
			{name: "mid", percent: 50, want: "Progress: [#####-----] 50%"},
			{name: "high", percent: 100, want: "Progress: [##########] 100%"},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				comment := FormatProgressComment(ProgressComment{Stage: "Working", Percent: tc.percent, ETAMinutes: 2})
				if got := firstBodyLine(comment); got != tc.want {
					t.Fatalf("unexpected progress line: got %q want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("clamps percent and avoids inline-code progress formatting", func(t *testing.T) {
		comment := FormatProgressComment(ProgressComment{Stage: "Working", Percent: 135, ETAMinutes: 2})
		if got := firstBodyLine(comment); got != "Progress: [##########] 100%" {
			t.Fatalf("unexpected progress line: %q", got)
		}
	})
}

func TestFormatDispatchFailureComment(t *testing.T) {
	comment := FormatDispatchFailureComment(DispatchFailureComment{
		Stage:        "issue_startup",
		Summary:      "codex CLI version 2.0.0 is incompatible with this Vigilante build",
		Branch:       "vigilante/issue-149",
		WorktreePath: "/tmp/repo/.worktrees/vigilante/issue-149",
		NextStep:     "fix the local `codex` runtime, then run `vigilante resume --repo owner/repo --issue 149` or request resume from GitHub.",
	})

	expected := "## 🧱 Blocked\nProgress: [#---------] 15%\n`ETA: ~10 minutes`\n- Failure stage: `issue startup`. Summary: `codex CLI version 2.0.0 is incompatible with this Vigilante build`.\n- Branch: `vigilante/issue-149`. Worktree: `/tmp/repo/.worktrees/vigilante/issue-149`.\n- Next step: fix the local `codex` runtime, then run `vigilante resume --repo owner/repo --issue 149` or request resume from GitHub.\n> \"No silent stalls.\""
	if comment != expected {
		t.Fatalf("unexpected comment:\n%s", comment)
	}
}

func firstBodyLine(comment string) string {
	lines := strings.Split(comment, "\n")
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}
