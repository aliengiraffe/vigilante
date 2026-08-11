package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestCoverageFailureGuidanceHelpers(t *testing.T) {
	base := state.Session{Repo: "owner/repo", IssueNumber: 42, Provider: "codex", Branch: "branch", WorktreePath: "/tmp/worktree"}
	for _, tc := range []struct{ kind, stage, text string }{
		{"provider_auth", "startup", "re-authenticate"}, {"provider_quota", "startup", "capacity"},
		{"provider_missing", "startup", "runtime"}, {"provider_runtime_error", "startup", "runtime"},
		{"", "dispatch", "worktree setup"}, {"", "startup", "startup problem"},
	} {
		s := base
		s.BlockedReason.Kind = tc.kind
		if got := dispatchFailureNextStep(s, tc.stage); !strings.Contains(got, tc.text) {
			t.Errorf("dispatch %s/%s=%q", tc.kind, tc.stage, got)
		}
	}
	s := base
	s.LastError = "worktree already exists"
	if got := dispatchFailureNextStep(s, "dispatch"); !strings.Contains(got, "cleanup") {
		t.Fatalf("stale worktree=%q", got)
	}
	for _, tc := range []struct{ kind, text string }{
		{"provider_auth", "Re-authenticate"}, {"provider_missing", "Install"}, {"git_auth", "git remote"}, {"gh_auth", "GitHub CLI"},
		{"network_unreachable", "network"}, {"dirty_worktree", "worktree"}, {"validation_failed", "validation"}, {"other", "Fix the blocker"},
	} {
		s := base
		s.BlockedReason.Kind = tc.kind
		if got := resumeFailureNextStep(s); !strings.Contains(got, tc.text) {
			t.Errorf("resume %s=%q", tc.kind, got)
		}
	}
	if !strings.Contains(dispatchFailureFingerprint(base, "dispatch"), "branch") || !strings.Contains(resumeFailureFingerprint(base), "unknown") {
		t.Fatal("fingerprints omit stable fields")
	}
}

func TestCoveragePathThresholdAndCompletionHelpers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for raw, want := range map[string]string{"~": home, "~/repo": filepath.Join(home, "repo")} {
		got, err := ExpandPath(raw)
		if err != nil || got != want {
			t.Errorf("ExpandPath(%q)=%q,%v want %q", raw, got, err, want)
		}
	}
	if _, err := ExpandPath(""); err == nil {
		t.Fatal("expected empty path error")
	}
	rel, err := ExpandPath("relative")
	if err != nil || !filepath.IsAbs(rel) {
		t.Fatalf("relative=%q err=%v", rel, err)
	}
	for raw, want := range map[string]time.Duration{"": defaultStalledSessionThreshold, "bad": defaultStalledSessionThreshold, "0s": defaultStalledSessionThreshold, "90s": 90 * time.Second} {
		t.Setenv("VIGILANTE_STALLED_SESSION_THRESHOLD", raw)
		if got := stalledSessionThreshold(); got != want {
			t.Errorf("threshold %q=%s want %s", raw, got, want)
		}
	}
	for _, shell := range []string{"bash", "fish", "zsh"} {
		var out bytes.Buffer
		a := &App{stdout: &out}
		if err := a.runCompletionCommand([]string{shell}); err != nil || out.Len() == 0 {
			t.Errorf("completion %s len=%d err=%v", shell, out.Len(), err)
		}
	}
	var out bytes.Buffer
	a := &App{stdout: &out}
	if err := a.runCompletionCommand([]string{"powershell"}); err == nil {
		t.Fatal("expected unsupported shell")
	}
	if err := a.runCompletionCommand(nil); err == nil {
		t.Fatal("expected missing shell")
	}
}

func TestCoverageSessionMessageAndStallBoundaries(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{{"provider_quota", "usage or subscription"}, {"other", "merge-ready"}} {
		blocked := state.BlockedReason{Kind: tc.kind}
		if got := maintenanceBlockedMessage(blocked, 42, "branch"); !strings.Contains(got, tc.want) {
			t.Errorf("maintenance=%q", got)
		}
		if got := resumePreflightBlockedMessage(blocked, "branch"); !strings.Contains(got, map[bool]string{true: "usage or subscription", false: "did not pass"}[tc.kind == "provider_quota"]) {
			t.Errorf("preflight=%q", got)
		}
		if got := resumeBlockedMessage(blocked, "branch"); !strings.Contains(got, map[bool]string{true: "usage or subscription", false: "did not complete"}[tc.kind == "provider_quota"]) {
			t.Errorf("resume=%q", got)
		}
	}
	for kind, want := range map[string]string{"provider_auth": "provider_related", "provider_missing": "provider_related", "provider_runtime_error": "provider_related", "network_unreachable": "transient", "other": "operator_fixable"} {
		if got := resumeFailureClassification(kind); got != want {
			t.Errorf("classification %s=%s", kind, got)
		}
	}
	now := time.Now().UTC()
	path := t.TempDir()
	for _, tc := range []struct {
		session   state.Session
		threshold time.Duration
		stalled   bool
		want      string
	}{
		{state.Session{WorktreePath: filepath.Join(path, "missing")}, time.Hour, true, "missing"},
		{state.Session{WorktreePath: path}, time.Hour, true, "heartbeat"},
		{state.Session{WorktreePath: path, LastHeartbeatAt: now.Format(time.RFC3339)}, time.Hour, false, ""},
		{state.Session{WorktreePath: path, LastHeartbeatAt: now.Add(-2 * time.Hour).Format(time.RFC3339), ProcessID: 123}, time.Hour, true, "process 123"},
		{state.Session{WorktreePath: path, LastHeartbeatAt: now.Add(-2 * time.Hour).Format(time.RFC3339)}, time.Hour, true, "idle"},
	} {
		stalled, reason := isStalledSession(tc.session, now, tc.threshold)
		if stalled != tc.stalled || !strings.Contains(reason, tc.want) {
			t.Errorf("stalled=%v reason=%q want %v/%q", stalled, reason, tc.stalled, tc.want)
		}
	}
}

func TestRecoveryCommandArgumentValidation(t *testing.T) {
	ctx := context.Background()
	for name, fn := range map[string]func([]string) error{
		"resume": func(args []string) error {
			var out bytes.Buffer
			return (&App{stdout: &out}).runResumeCommand(ctx, args)
		},
		"redispatch": func(args []string) error {
			var out bytes.Buffer
			return (&App{stdout: &out}).runRedispatchCommand(ctx, args)
		},
		"cleanup": func(args []string) error {
			var out bytes.Buffer
			return (&App{stdout: &out}).runCleanupCommand(ctx, args)
		},
	} {
		if err := fn([]string{"--help"}); err != nil {
			t.Errorf("%s help=%v", name, err)
		}
		if err := fn([]string{"--unknown"}); err == nil {
			t.Errorf("%s accepted unknown flag", name)
		}
	}
	for _, args := range [][]string{nil, {"--repo", "owner/repo"}, {"--issue", "1"}, {"--all-blocked", "--repo", "owner/repo"}} {
		var out bytes.Buffer
		if err := (&App{stdout: &out}).runResumeCommand(ctx, args); err == nil {
			t.Errorf("resume accepted %#v", args)
		}
	}
	for _, args := range [][]string{nil, {"--repo", "owner/repo"}, {"--issue", "1"}} {
		var out bytes.Buffer
		if err := (&App{stdout: &out}).runRedispatchCommand(ctx, args); err == nil {
			t.Errorf("redispatch accepted %#v", args)
		}
	}
	for _, args := range [][]string{nil, {"--issue", "1"}, {"--repo", "owner/repo", "--issue", "-1"}, {"--all", "--repo", "owner/repo"}} {
		var out bytes.Buffer
		if err := (&App{stdout: &out}).runCleanupCommand(ctx, args); err == nil {
			t.Errorf("cleanup accepted %#v", args)
		}
	}
}

// stringListFlag backs repeatable --label flags. Rejecting an empty value here is
// what stops `--label ""` from asking GitHub to apply a nameless label.
func TestStringListFlag(t *testing.T) {
	t.Parallel()

	var flag stringListFlag

	if got := flag.String(); got != "" {
		t.Fatalf("empty flag String() = %q, want empty", got)
	}

	if err := flag.Set("bug"); err != nil {
		t.Fatal(err)
	}
	if err := flag.Set("  padded  "); err != nil {
		t.Fatal(err)
	}
	if got := flag.String(); got != "bug,padded" {
		t.Fatalf("String() = %q, want comma-joined trimmed values", got)
	}

	for _, empty := range []string{"", "   "} {
		if err := flag.Set(empty); err == nil {
			t.Errorf("Set(%q) should be rejected", empty)
		}
	}
	// A rejected value must not be appended.
	if got := flag.String(); got != "bug,padded" {
		t.Fatalf("String() = %q after rejected values, want unchanged", got)
	}
}

func TestCommandExitError(t *testing.T) {
	t.Parallel()

	err := commandExitError{code: 3}
	if !strings.Contains(err.Error(), "3") {
		t.Fatalf("Error() = %q, want it to include the exit code", err.Error())
	}
}

// A rebase that stops on conflicts is recoverable via the conflict-resolution
// flow, so misclassifying it as a hard failure would abandon work that could be
// finished. Both the command output and the error text are searched because git
// puts the useful wording in either depending on the failure.
func TestIsRebaseConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		err    error
		want   bool
	}{
		{
			name:   "conflict in output",
			output: "CONFLICT (content): Merge conflict in main.go",
			err:    errors.New("exit status 1"),
			want:   true,
		},
		{
			name:   "could not apply in output",
			output: "error: could not apply abc123... commit subject",
			err:    errors.New("exit status 1"),
			want:   true,
		},
		{
			name:   "conflict only in the error text",
			output: "",
			err:    errors.New("rebase failed with CONFLICT"),
			want:   true,
		},
		{
			name:   "case-insensitive match",
			output: "Conflict detected",
			err:    errors.New("exit status 1"),
			want:   true,
		},
		{
			name:   "unrelated failure",
			output: "fatal: not a git repository",
			err:    errors.New("exit status 128"),
			want:   false,
		},
		{
			name:   "empty output and generic error",
			output: "",
			err:    errors.New("exit status 1"),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isRebaseConflict(tt.output, tt.err); got != tt.want {
				t.Fatalf("isRebaseConflict(%q, %v) = %v, want %v", tt.output, tt.err, got, tt.want)
			}
		})
	}
}

// Repeating the same maintenance failure on every scan would spam the issue, so a
// comment is only warranted when the error text changed.
func TestShouldCommentMaintenanceFailure(t *testing.T) {
	t.Parallel()

	session := state.Session{LastMaintenanceError: "boom"}

	if shouldCommentMaintenanceFailure(session, errors.New("boom")) {
		t.Error("an unchanged error must not comment again")
	}
	if shouldCommentMaintenanceFailure(session, errors.New("  boom  ")) {
		t.Error("whitespace-only differences must not count as a new error")
	}
	if !shouldCommentMaintenanceFailure(session, errors.New("different")) {
		t.Error("a changed error should comment")
	}
	if !shouldCommentMaintenanceFailure(state.Session{}, errors.New("first failure")) {
		t.Error("the first failure should comment")
	}
}

// The wait reason is what an operator reads to learn why automerge has not fired,
// so each blocking condition has to be distinguishable, and a mergeable PR must
// produce no reason at all.
func TestAutomergeWaitReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pr       ghcli.PullRequest
		want     string
		contains string
	}{
		{
			name: "clean pull request has no wait reason",
			pr:   ghcli.PullRequest{Number: 1, MergeStateStatus: "CLEAN"},
			want: "",
		},
		{
			name: "empty merge state has no wait reason",
			pr:   ghcli.PullRequest{Number: 1},
			want: "",
		},
		{
			name:     "draft takes precedence over everything else",
			pr:       ghcli.PullRequest{Number: 2, IsDraft: true, MergeStateStatus: "BLOCKED", ReviewDecision: "CHANGES_REQUESTED"},
			contains: "leave draft state",
		},
		{
			name:     "changes requested",
			pr:       ghcli.PullRequest{Number: 3, ReviewDecision: "CHANGES_REQUESTED"},
			contains: "review changes",
		},
		{
			name:     "review required",
			pr:       ghcli.PullRequest{Number: 4, ReviewDecision: "REVIEW_REQUIRED"},
			contains: "required reviews",
		},
		{
			name:     "blocked mergeability",
			pr:       ghcli.PullRequest{Number: 6, MergeStateStatus: "BLOCKED"},
			contains: "blocked",
		},
		{
			name:     "behind base",
			pr:       ghcli.PullRequest{Number: 7, MergeStateStatus: "BEHIND"},
			contains: "behind base",
		},
		{
			name:     "dirty means conflicts",
			pr:       ghcli.PullRequest{Number: 8, MergeStateStatus: "DIRTY"},
			contains: "merge conflicts detected",
		},
		{
			name:     "draft merge state",
			pr:       ghcli.PullRequest{Number: 9, MergeStateStatus: "DRAFT"},
			contains: "pull request is draft",
		},
		{
			name:     "pre-merge hooks",
			pr:       ghcli.PullRequest{Number: 10, MergeStateStatus: "HAS_HOOKS"},
			contains: "pre-merge hooks",
		},
		{
			name:     "unknown state is reported verbatim in lower case",
			pr:       ghcli.PullRequest{Number: 11, MergeStateStatus: "UNKNOWN"},
			contains: "state unknown",
		},
		{
			name:     "unstable state",
			pr:       ghcli.PullRequest{Number: 12, MergeStateStatus: "UNSTABLE"},
			contains: "state unstable",
		},
		{
			name:     "an unrecognized state still produces a reason",
			pr:       ghcli.PullRequest{Number: 13, MergeStateStatus: "SOMETHING_NEW"},
			contains: "state something_new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := automergeWaitReason(tt.pr)
			if tt.contains == "" {
				if got != tt.want {
					t.Fatalf("automergeWaitReason() = %q, want %q", got, tt.want)
				}
				return
			}
			if !strings.Contains(got, tt.contains) {
				t.Fatalf("automergeWaitReason() = %q, want it to contain %q", got, tt.contains)
			}
		})
	}
}

func TestPullRequestNeedsConflictResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pr   ghcli.PullRequest
		want bool
	}{
		{name: "conflicting mergeable", pr: ghcli.PullRequest{Mergeable: "CONFLICTING"}, want: true},
		{name: "lower case conflicting", pr: ghcli.PullRequest{Mergeable: "conflicting"}, want: true},
		{name: "padded conflicting", pr: ghcli.PullRequest{Mergeable: "  CONFLICTING  "}, want: true},
		{name: "dirty merge state", pr: ghcli.PullRequest{MergeStateStatus: "DIRTY"}, want: true},
		{name: "mergeable and clean", pr: ghcli.PullRequest{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN"}, want: false},
		{name: "empty", pr: ghcli.PullRequest{}, want: false},
		{name: "behind is not a conflict", pr: ghcli.PullRequest{MergeStateStatus: "BEHIND"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := pullRequestNeedsConflictResolution(tt.pr); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// Quota blocks get their own wording because the operator action differs: wait or
// upgrade, rather than investigate a failure.
func TestResumeBlockedMessages(t *testing.T) {
	t.Parallel()

	quota := state.BlockedReason{Kind: "provider_quota"}
	other := state.BlockedReason{Kind: "git_auth"}

	t.Run("preflight", func(t *testing.T) {
		t.Parallel()

		got := resumePreflightBlockedMessage(quota, "br")
		if !strings.Contains(got, "usage or subscription limit") || !strings.Contains(got, "br") {
			t.Fatalf("quota message = %q", got)
		}
		got = resumePreflightBlockedMessage(other, "br")
		if !strings.Contains(got, "did not pass") || strings.Contains(got, "usage or subscription") {
			t.Fatalf("non-quota message = %q", got)
		}
	})

	t.Run("resume", func(t *testing.T) {
		t.Parallel()

		got := resumeBlockedMessage(quota, "br")
		if !strings.Contains(got, "usage or subscription limit") || !strings.Contains(got, "br") {
			t.Fatalf("quota message = %q", got)
		}
		got = resumeBlockedMessage(other, "br")
		if !strings.Contains(got, "did not complete") || strings.Contains(got, "usage or subscription") {
			t.Fatalf("non-quota message = %q", got)
		}
	})
}

// A session whose watch target has no explicit provider must fall back to the
// default rather than to an empty provider id, which would fail to launch.
func TestFindWatchTargetProvider(t *testing.T) {
	t.Parallel()

	targets := []state.WatchTarget{
		{Path: "/a", Provider: "codex"},
		{Path: "/b", Provider: "   "},
		{Path: "/c"},
	}

	if got := findWatchTargetProvider(targets, "/a"); got != "codex" {
		t.Errorf("explicit provider = %q, want codex", got)
	}
	if got := findWatchTargetProvider(targets, "/b"); got != provider.DefaultID {
		t.Errorf("blank provider = %q, want the default %q", got, provider.DefaultID)
	}
	if got := findWatchTargetProvider(targets, "/c"); got != provider.DefaultID {
		t.Errorf("missing provider = %q, want the default %q", got, provider.DefaultID)
	}
	if got := findWatchTargetProvider(targets, "/unknown"); got != provider.DefaultID {
		t.Errorf("unknown path = %q, want the default %q", got, provider.DefaultID)
	}
	if got := findWatchTargetProvider(nil, "/a"); got != provider.DefaultID {
		t.Errorf("no targets = %q, want the default %q", got, provider.DefaultID)
	}
}

func TestSummarizeMaintenanceError(t *testing.T) {
	t.Parallel()

	// summarizeText trims the ends and caps length; it deliberately does not
	// compact interior whitespace, so the message stays byte-faithful.
	if got := summarizeMaintenanceError(errors.New("  boom  ")); got != "boom" {
		t.Fatalf("summary = %q, want %q", got, "boom")
	}

	long := strings.Repeat("x", 500)
	got := summarizeMaintenanceError(errors.New(long))
	if len(got) != 400 {
		t.Fatalf("summary length = %d, want the 400-character cap", len(got))
	}
}

func TestIsSupportedProxyTool(t *testing.T) {
	t.Parallel()

	// The proxy allowlist is a security boundary: anything not on it must not be
	// proxied into the sandbox.
	for _, tool := range []string{"gh", "git", "docker", "  gh  "} {
		if !isSupportedProxyTool(tool) {
			t.Errorf("%q should be supported", tool)
		}
	}
	for _, tool := range []string{"", "curl", "bash", "sh", "npm", "GH"} {
		if isSupportedProxyTool(tool) {
			t.Errorf("%q must not be supported", tool)
		}
	}
}

// labelManagerForSession resolves through the session's fallback watch target, so
// a session with no matching target still has to produce a usable manager rather
// than nil.
func TestLabelManagerForSessionAlwaysResolves(t *testing.T) {
	// No t.Parallel(): the shared harness calls t.Setenv, which forbids it.
	app, _ := newPlainStatusTestApp(t)

	if got := app.labelManagerForSession(state.Session{Repo: "owner/repo"}); got == nil {
		t.Fatal("labelManagerForSession returned nil")
	}
	if got := app.prManagerForSession(state.Session{Repo: "owner/repo"}); got == nil {
		t.Fatal("prManagerForSession returned nil")
	}
}
