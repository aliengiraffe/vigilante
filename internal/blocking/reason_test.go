package blocking

import (
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
)

// Classification order matters: real provider output often trips several
// substring checks at once, and the first matching case wins. These cases pin the
// precedence that operators depend on when they read a blocked reason.
func TestClassifyKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stage     string
		operation string
		text      string
		want      string
	}{
		{
			name: "ssh publickey failure is git auth",
			text: "git@github.com: Permission denied (publickey).",
			want: "git_auth",
		},
		{
			name: "sign_and_send_pubkey is git auth",
			text: "sign_and_send_pubkey: signing failed for RSA key",
			want: "git_auth",
		},
		{
			name: "unreadable remote is git auth",
			text: "fatal: Could not read from remote repository.",
			want: "git_auth",
		},
		{
			name: "gh auth prompt is gh auth",
			text: "To get started with GitHub CLI, please run: gh auth login",
			want: "gh_auth",
		},
		{
			name: "not logged into is gh auth",
			text: "You are not logged into any GitHub hosts.",
			want: "gh_auth",
		},
		{
			name: "authentication failed is gh auth",
			text: "remote: Authentication failed for repository",
			want: "gh_auth",
		},
		{
			name: "session expired is provider auth",
			text: "Your session expired, please sign in again.",
			want: "provider_auth",
		},
		{
			name: "unauthorized is provider auth",
			text: "401 Unauthorized",
			want: "provider_auth",
		},
		{
			name: "usage limit is provider quota",
			text: "You have reached your usage limit for this month.",
			want: "provider_quota",
		},
		{
			name: "rate limit reached is provider quota",
			text: "Rate limit reached for requests.",
			want: "provider_quota",
		},
		{
			name: "quota exceeded is provider quota",
			text: "Quota exceeded for this organization.",
			want: "provider_quota",
		},
		{
			name: "missing binary is provider missing",
			text: `exec: "codex": executable file not found in $PATH`,
			want: "provider_missing",
		},
		{
			name: "missing path is provider missing",
			text: "open /usr/local/bin/claude: no such file or directory",
			want: "provider_missing",
		},
		{
			name: "dirty worktree",
			text: "worktree is not clean; commit or stash your changes",
			want: "dirty_worktree",
		},
		{
			name: "failing go test is validation failed",
			text: "go test ./... exited with status 1",
			want: "validation_failed",
		},
		{
			name: "build failed is validation failed",
			text: "build failed: undefined reference",
			want: "validation_failed",
		},
		{
			name: "unreachable network",
			text: "dial tcp: connect: network is unreachable",
			want: "network_unreachable",
		},
		{
			name: "timeout is network unreachable",
			text: "context deadline exceeded: request timed out",
			want: "network_unreachable",
		},
		{
			name:  "unrecognized text in issue_execution falls back to provider runtime error",
			stage: "issue_execution",
			text:  "something entirely unexpected happened",
			want:  "provider_runtime_error",
		},
		{
			name:  "unrecognized text in conflict_resolution falls back to provider runtime error",
			stage: "conflict_resolution",
			text:  "unexpected provider output",
			want:  "provider_runtime_error",
		},
		{
			name:  "unrecognized text in baseline_preflight falls back to provider runtime error",
			stage: "baseline_preflight",
			text:  "unexpected provider output",
			want:  "provider_runtime_error",
		},
		{
			name:  "unrecognized text in an unknown stage needs an operator",
			stage: "some_other_stage",
			text:  "unexpected provider output",
			want:  "unknown_operator_action_required",
		},
		{
			name: "empty text needs an operator",
			text: "",
			want: "unknown_operator_action_required",
		},
		// Precedence guards. Each of these matches more than one case arm, and
		// the earlier arm has to win or the operator gets pointed at the wrong fix.
		{
			name: "git auth beats gh auth when both appear",
			text: "Permission denied (publickey). Try gh auth login.",
			want: "git_auth",
		},
		{
			name:  "recognized cause beats the stage fallback",
			stage: "issue_execution",
			text:  "fatal: Could not read from remote repository.",
			want:  "git_auth",
		},
		{
			name: "provider auth beats quota when both appear",
			text: "Unauthorized: your usage limit was also reached.",
			want: "provider_auth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Classify(tt.stage, tt.operation, tt.text, "fallback summary")
			if got.Kind != tt.want {
				t.Fatalf("Classify(%q) kind = %q, want %q", tt.text, got.Kind, tt.want)
			}
		})
	}
}

func TestClassifyCarriesOperationThrough(t *testing.T) {
	t.Parallel()

	got := Classify("issue_execution", "git push", "boom", "summary")
	if got.Operation != "git push" {
		t.Fatalf("Operation = %q, want %q", got.Operation, "git push")
	}
}

// The summary is what lands in an issue comment and the detail is the raw text.
// A non-quota failure must keep the caller's summary rather than inventing one.
func TestClassifyUsesFallbackSummaryAndDetail(t *testing.T) {
	t.Parallel()

	got := Classify("", "", "  fatal:   Could not read\nfrom remote repository.  ", "push failed")

	if got.Summary != "push failed" {
		t.Fatalf("Summary = %q, want the caller's fallback", got.Summary)
	}
	// Detail is whitespace-compacted, so newlines and runs of spaces collapse.
	if got.Detail != "fatal: Could not read from remote repository." {
		t.Fatalf("Detail = %q, want the compacted text", got.Detail)
	}
}

// Both fields feed operator-facing output, so neither may be left empty when the
// other has content.
func TestClassifyBackfillsEmptyFields(t *testing.T) {
	t.Parallel()

	t.Run("empty summary falls back to detail", func(t *testing.T) {
		t.Parallel()

		got := Classify("", "", "worktree is not clean", "")
		if got.Summary != "worktree is not clean" {
			t.Fatalf("Summary = %q, want the detail", got.Summary)
		}
	})

	t.Run("empty detail falls back to summary", func(t *testing.T) {
		t.Parallel()

		got := Classify("", "", "", "only a summary")
		if got.Detail != "only a summary" {
			t.Fatalf("Detail = %q, want the summary", got.Detail)
		}
	})

	t.Run("both empty stay empty", func(t *testing.T) {
		t.Parallel()

		got := Classify("", "", "", "")
		if got.Summary != "" || got.Detail != "" {
			t.Fatalf("Summary = %q, Detail = %q, want both empty", got.Summary, got.Detail)
		}
	})
}

// 400 characters is the cap. Session state is persisted and echoed into issue
// comments, so an unbounded provider dump would bloat both.
func TestClassifyTruncatesLongText(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 900)
	got := Classify("", "", long, long)

	if len(got.Detail) != 400 {
		t.Fatalf("Detail length = %d, want 400", len(got.Detail))
	}
	if len(got.Summary) != 400 {
		t.Fatalf("Summary length = %d, want 400", len(got.Summary))
	}
}

func TestClassifyKeepsTextAtExactlyTheLimit(t *testing.T) {
	t.Parallel()

	exact := strings.Repeat("y", 400)
	got := Classify("", "", exact, exact)

	if got.Detail != exact {
		t.Fatalf("text at exactly 400 characters must be preserved, got length %d", len(got.Detail))
	}
}

// Quota is the one kind that replaces the caller's summary, because the provider's
// own message carries the retry time and remediation the operator needs.
func TestClassifyQuotaSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		contains []string
		absent   []string
	}{
		{
			name:     "bare usage limit gets the base sentence only",
			text:     "You have reached your usage limit.",
			contains: []string{"Coding-agent account hit a usage or subscription limit."},
			absent:   []string{"upgrading", "purchasing"},
		},
		{
			name:     "retry hint is surfaced",
			text:     "Usage limit reached. Try again at 3pm UTC",
			contains: []string{"Try again at 3pm UTC."},
		},
		{
			name:     "reset hint is surfaced",
			text:     "Quota exceeded. Resets at midnight",
			contains: []string{"Resets at midnight."},
		},
		{
			name:     "retry after hint is surfaced",
			text:     "Rate limit reached. Retry after 60 seconds",
			contains: []string{"Retry after 60 seconds."},
		},
		{
			name:     "upgrade suggestion is surfaced",
			text:     "Usage limit reached. Upgrade to Pro for more usage.",
			contains: []string{"Provider suggests upgrading the subscription."},
		},
		{
			name:     "purchase suggestion is surfaced",
			text:     "Usage limit reached. Please purchase more credits.",
			contains: []string{"Provider suggests purchasing more credits."},
		},
		{
			name:     "buy credits suggestion is surfaced",
			text:     "Quota exceeded. You can buy more credits anytime.",
			contains: []string{"Provider suggests purchasing more credits."},
		},
		{
			name: "all hints combine",
			text: "Usage limit reached. Try again at noon. Upgrade to Max or purchase more credits.",
			contains: []string{
				"Coding-agent account hit a usage or subscription limit.",
				"Provider suggests upgrading the subscription.",
				"Provider suggests purchasing more credits.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Classify("", "", tt.text, "ignored fallback")
			if got.Kind != "provider_quota" {
				t.Fatalf("Kind = %q, want provider_quota", got.Kind)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got.Summary, want) {
					t.Errorf("Summary %q does not contain %q", got.Summary, want)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got.Summary, absent) {
					t.Errorf("Summary %q unexpectedly contains %q", got.Summary, absent)
				}
			}
		})
	}
}

// A provider that says "purchase more credits" twice must not produce the
// remediation sentence twice.
func TestClassifyQuotaSummaryDeduplicatesHints(t *testing.T) {
	t.Parallel()

	got := Classify("", "", "Usage limit reached. Purchase more credits. You can also buy more credits.", "")

	if n := strings.Count(got.Summary, "Provider suggests purchasing more credits."); n != 1 {
		t.Fatalf("purchase sentence appeared %d times in %q, want 1", n, got.Summary)
	}
}

// The upgrade/credits wording is only a quota signal alongside a usage-related
// word. A plain marketing line must not be misread as a quota block, or a genuine
// runtime error gets reported as a billing problem.
func TestClassifyQuotaRequiresUsageContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "upgrade with credits context is quota",
			text: "Upgrade to Pro to get more credits.",
			want: "provider_quota",
		},
		{
			name: "upgrade with usage context is quota",
			text: "Upgrade to Pro for higher usage.",
			want: "provider_quota",
		},
		{
			name: "upgrade with quota context is quota",
			text: "Upgrade to Pro to raise your quota.",
			want: "provider_quota",
		},
		{
			name: "upgrade alone is not quota",
			text: "Upgrade to version 2 of the CLI.",
			want: "unknown_operator_action_required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := Classify("", "", tt.text, ""); got.Kind != tt.want {
				t.Fatalf("Classify(%q) kind = %q, want %q", tt.text, got.Kind, tt.want)
			}
		})
	}
}

func TestStateLabel(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"git_auth":                         "blocked_waiting_for_credentials",
		"gh_auth":                          "blocked_waiting_for_github_auth",
		"provider_auth":                    "blocked_waiting_for_provider_auth",
		"provider_quota":                   "blocked_waiting_for_provider_quota",
		"provider_missing":                 "blocked_waiting_for_provider_binary",
		"validation_failed":                "blocked_waiting_for_operator",
		"dirty_worktree":                   "blocked_waiting_for_operator",
		"network_unreachable":              "blocked_waiting_for_operator",
		"provider_runtime_error":           "blocked_waiting_for_operator",
		"unknown_operator_action_required": "blocked_waiting_for_operator",
		"":                                 "blocked_waiting_for_operator",
	}

	for kind, want := range tests {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			if got := StateLabel(kind); got != want {
				t.Fatalf("StateLabel(%q) = %q, want %q", kind, got, want)
			}
		})
	}
}

// Every classified kind must map to a label. A new kind that fell through to the
// generic operator label silently would lose the routing this package exists for.
func TestStateLabelCoversEveryClassifiedKind(t *testing.T) {
	t.Parallel()

	specific := map[string]bool{
		"git_auth":         true,
		"gh_auth":          true,
		"provider_auth":    true,
		"provider_quota":   true,
		"provider_missing": true,
	}
	for kind := range specific {
		if got := StateLabel(kind); got == "blocked_waiting_for_operator" {
			t.Errorf("kind %q should map to a specific label, got the generic one", kind)
		}
	}
}

func TestCauseLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason state.BlockedReason
		want   string
	}{
		{
			name:   "non-quota kind reports only the class",
			reason: state.BlockedReason{Kind: "git_auth", Summary: "ignored"},
			want:   "Cause class: `git_auth`.",
		},
		{
			name:   "quota kind appends the provider detail",
			reason: state.BlockedReason{Kind: "provider_quota", Summary: "Try again at noon."},
			want:   "Cause class: `provider_quota`. Provider detail: `Try again at noon.`.",
		},
		{
			name:   "quota kind with an empty summary omits the detail",
			reason: state.BlockedReason{Kind: "provider_quota", Summary: "   "},
			want:   "Cause class: `provider_quota`.",
		},
		{
			name:   "empty kind falls back to the unknown class",
			reason: state.BlockedReason{},
			want:   "Cause class: `unknown_operator_action_required`.",
		},
		{
			name:   "blank kind falls back to the unknown class",
			reason: state.BlockedReason{Kind: "   "},
			want:   "Cause class: `unknown_operator_action_required`.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CauseLine(tt.reason); got != tt.want {
				t.Fatalf("CauseLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Classify feeds CauseLine in production, so the two must agree end to end rather
// than only in isolation.
func TestClassifyThenCauseLine(t *testing.T) {
	t.Parallel()

	reason := Classify("issue_execution", "run provider", "Usage limit reached. Try again at 5pm.", "provider failed")
	line := CauseLine(reason)

	if !strings.Contains(line, "provider_quota") {
		t.Fatalf("CauseLine = %q, want the quota class", line)
	}
	if !strings.Contains(line, "Provider detail:") {
		t.Fatalf("CauseLine = %q, want a provider detail section", line)
	}
}
