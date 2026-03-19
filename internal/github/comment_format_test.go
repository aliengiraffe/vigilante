package ghcli

import (
	"strings"
	"testing"
	"time"
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

func TestFormatGitHubRateLimitDelayComment(t *testing.T) {
	now := time.Date(2026, 3, 19, 21, 55, 0, 0, time.UTC)
	snapshot := RateLimitSnapshot{
		Core:    RateLimitResource{Limit: 5000, Remaining: 95, ResetAt: time.Date(2026, 3, 19, 14, 59, 11, 0, time.FixedZone("PDT", -7*60*60))},
		Rate:    RateLimitResource{Limit: 5000, Remaining: 95, ResetAt: time.Date(2026, 3, 19, 14, 59, 11, 0, time.FixedZone("PDT", -7*60*60))},
		GraphQL: RateLimitResource{Limit: 5000, Remaining: 4557, ResetAt: time.Date(2026, 3, 19, 15, 9, 52, 0, time.FixedZone("PDT", -7*60*60))},
		Search:  RateLimitResource{Limit: 30, Remaining: 30, ResetAt: time.Date(2026, 3, 19, 14, 58, 13, 0, time.FixedZone("PDT", -7*60*60))},
	}

	comment := FormatGitHubRateLimitDelayComment(snapshot, 100, now)
	for _, want := range []string{
		"## ⏸️ GitHub Delay Window",
		"Progress: [#######---] 70%",
		"`ETA: ~5 minutes`",
		"gh api /rate_limit returned:",
		"  - core: 4905/5000 used, 95 remaining, resets at 2026-03-19 14:59:11 -07:00",
		"  - rate (same as core): 4905/5000 used, 95 remaining, resets at 2026-03-19 14:59:11 -07:00",
		"  - graphql: 443/5000 used, 4557 remaining, resets at 2026-03-19 15:09:52 -07:00",
		"  - search: 0/30 used, 30 remaining, resets at 2026-03-19 14:58:13 -07:00",
		"Automatic resume is scheduled after `2026-03-19 14:59:11 -07:00`.",
	} {
		if !strings.Contains(comment, want) {
			t.Fatalf("expected comment to contain %q, got:\n%s", want, comment)
		}
	}
}

func firstBodyLine(comment string) string {
	lines := strings.Split(comment, "\n")
	if len(lines) < 2 {
		return ""
	}
	return lines[1]
}
