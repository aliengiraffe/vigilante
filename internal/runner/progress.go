package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/state"
)

// ProgressSignal describes the deterministic progress classification of a
// session after the coding agent exits successfully.
type ProgressSignal struct {
	// HasPullRequest is true when the session already tracks a PR number.
	HasPullRequest bool
	// HasNewCommits is true when the issue branch has commits ahead of the base branch.
	HasNewCommits bool
	// HasWorktreeChanges is true when git status reports modified/staged files.
	HasWorktreeChanges bool
	// Fingerprint identifies the branch HEAD and worktree status at the end
	// of the attempt. It is empty when HEAD could not be read.
	Fingerprint string
}

const (
	incompleteReasonNoDurableProgress = "no_durable_progress"
	incompleteReasonUncommittedOnly   = "uncommitted_changes"
	incompleteReasonCommitsWithoutPR  = "commits_without_pr"
)

// Blocked reason kinds for issue sessions that stopped making progress or
// whose coding agent declared a blocker.
const (
	BlockedKindNoProgressRepeated = "no_progress_repeated"
	BlockedKindRerunLimit         = "incomplete_rerun_limit"
	BlockedKindAgentDeclared      = "agent_declared"
	BlockedKindDependencyMissing  = "dependency_missing"
	BlockedKindIssueLabel         = "issue_labeled_blocked"
)

// AgentBlockedSignalPath is where, relative to the worktree root, a coding
// agent writes a JSON object {"kind", "summary", "detail"} to tell Vigilante it
// stopped on a blocker it cannot resolve.
const AgentBlockedSignalPath = ".vigilante/blocked.json"

const maxAgentBlockedSignalBytes = 64 << 10

// EvaluateSessionProgress inspects deterministic local and git signals to
// classify what durable progress a session made. The caller uses the result
// to decide between SessionStatusSuccess and SessionStatusIncomplete.
func EvaluateSessionProgress(ctx context.Context, runner environment.Runner, session state.Session) ProgressSignal {
	signal := ProgressSignal{
		HasPullRequest: session.PullRequestNumber > 0,
	}
	if signal.HasPullRequest {
		return signal
	}
	signal.HasNewCommits = detectNewCommits(ctx, runner, session)
	status, statusOK := worktreeStatus(ctx, runner, session)
	signal.HasWorktreeChanges = statusOK && strings.TrimSpace(status) != ""
	if head := headSHA(ctx, runner, session); head != "" && statusOK {
		sum := sha256.Sum256([]byte(strings.TrimSpace(status)))
		signal.Fingerprint = head + ":" + hex.EncodeToString(sum[:8])
	}
	return signal
}

// RecordIncompleteAttempt stores the fingerprint of an attempt that ended
// without a pull request. It returns a blocked reason when the attempt left
// HEAD and the worktree exactly as the previous attempt did, meaning a rerun
// made no progress and another one would not either.
func RecordIncompleteAttempt(session *state.Session, signal ProgressSignal) *state.BlockedReason {
	previous := session.LastAttemptFingerprint
	session.LastAttemptFingerprint = signal.Fingerprint
	if signal.Fingerprint == "" || signal.Fingerprint != previous {
		return nil
	}
	return &state.BlockedReason{
		Kind:      BlockedKindNoProgressRepeated,
		Operation: "incomplete_rerun",
		Summary:   "the rerun ended with the same branch HEAD and worktree as the previous attempt",
		Detail:    fmt.Sprintf("no progress since the previous attempt (fingerprint %s)", signal.Fingerprint),
	}
}

// ResetIncompleteRerunTracking clears the rerun counter and progress
// fingerprint, as after a pull request is opened or a human resumes.
func ResetIncompleteRerunTracking(session *state.Session) {
	session.IncompleteRerunCount = 0
	session.LastAttemptFingerprint = ""
}

type agentBlockedSignal struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
}

// ConsumeAgentBlockedSignal reads and removes the agent blocked signal file
// from worktreePath. It returns nil when the agent did not write one. A file
// that cannot be parsed still blocks the session: the agent's intent to stop
// is clear even when its explanation is not.
func ConsumeAgentBlockedSignal(worktreePath string) *state.BlockedReason {
	if strings.TrimSpace(worktreePath) == "" {
		return nil
	}
	path := filepath.Join(worktreePath, filepath.FromSlash(AgentBlockedSignalPath))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	raw, readErr := readLimited(path, maxAgentBlockedSignalBytes)
	_ = os.Remove(path)
	_ = os.Remove(filepath.Dir(path)) // only succeeds when empty
	reason := &state.BlockedReason{Kind: BlockedKindAgentDeclared, Operation: "agent_blocked_signal"}
	var signal agentBlockedSignal
	if readErr == nil {
		readErr = json.Unmarshal(raw, &signal)
	}
	if readErr != nil {
		reason.Summary = "the coding agent declared a blocker"
		reason.Detail = truncateSignalText("unreadable "+AgentBlockedSignalPath+": "+readErr.Error(), 500)
		return reason
	}
	if kind := strings.TrimSpace(signal.Kind); kind == BlockedKindDependencyMissing {
		reason.Kind = kind
	}
	reason.Summary = truncateSignalText(fallbackSessionText(strings.TrimSpace(signal.Summary), "the coding agent declared a blocker"), 300)
	reason.Detail = truncateSignalText(strings.TrimSpace(signal.Detail), 2000)
	return reason
}

func readLimited(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("signal file is too large")
	}
	return raw, nil
}

func truncateSignalText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return strings.TrimSpace(text[:limit]) + "…"
}

var baseBranchDirectivePattern = regexp.MustCompile("(?im)^[ \\t]*base branch:[ \\t]*`?([^`\\s]+)`?[ \\t\\r]*$")

// ParseBaseBranchDirective returns the branch named by a single-line
// `Base branch: <name>` directive in an issue body, or "" when there is none.
// The label is case-insensitive and the name may be wrapped in backticks.
func ParseBaseBranchDirective(body string) string {
	match := baseBranchDirectivePattern.FindStringSubmatch(body)
	if match == nil {
		return ""
	}
	name := strings.TrimSpace(match[1])
	if strings.HasPrefix(name, "-") || strings.Contains(name, "..") {
		return ""
	}
	return name
}

// ProgressBaseBranch is the branch a session's new commits are counted
// against: the issue's stacked `Base branch:` directive, then the pull request
// base, then the session's base branch, then main.
func ProgressBaseBranch(session state.Session) string {
	for _, candidate := range []string{session.StackedBaseBranch, session.PullRequestBaseBranch, session.BaseBranch} {
		if branch := strings.TrimSpace(candidate); branch != "" {
			return branch
		}
	}
	return "main"
}

// ClassifyIncompleteReason returns the deterministic reason string for a
// session that exited successfully but did not create a PR.
func ClassifyIncompleteReason(signal ProgressSignal) string {
	switch {
	case signal.HasNewCommits:
		return incompleteReasonCommitsWithoutPR
	case signal.HasWorktreeChanges:
		return incompleteReasonUncommittedOnly
	default:
		return incompleteReasonNoDurableProgress
	}
}

// RerunAction is what the scan loop should do with an incomplete session.
type RerunAction int

const (
	// RerunSkip leaves the session alone (not eligible).
	RerunSkip RerunAction = iota
	// RerunWait leaves the session incomplete until its backoff elapses.
	RerunWait
	// RerunLaunch relaunches the session.
	RerunLaunch
	// RerunBlock moves the session to blocked because it used its reruns.
	RerunBlock
)

// RerunDecision decides whether an incomplete session is relaunched now,
// waits out its backoff, or is blocked because it reached limit reruns. It
// returns the time the next rerun becomes due when the action is RerunWait.
func RerunDecision(session state.Session, now time.Time, limit int, backoff time.Duration) (RerunAction, time.Time) {
	if !IsRerunEligible(session) {
		return RerunSkip, time.Time{}
	}
	if session.IncompleteRerunCount >= limit {
		return RerunBlock, time.Time{}
	}
	ended := firstParsedTime(session.EndedAt, session.UpdatedAt)
	if ended.IsZero() {
		return RerunLaunch, time.Time{}
	}
	due := ended.Add(state.IncompleteRerunDelay(backoff, session.IncompleteRerunCount))
	if now.Before(due) {
		return RerunWait, due
	}
	return RerunLaunch, time.Time{}
}

// RerunLimitBlockedReason describes a session blocked after limit reruns.
func RerunLimitBlockedReason(limit int) state.BlockedReason {
	return state.BlockedReason{
		Kind:      BlockedKindRerunLimit,
		Operation: "incomplete_rerun",
		Summary:   fmt.Sprintf("stopped after %d automatic reruns without opening a pull request", limit),
	}
}

func firstParsedTime(values ...string) time.Time {
	for _, value := range values {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// IsRerunEligible returns true when a session is incomplete for a reason that
// makes rerunning in the same worktree worthwhile. It does not apply the rerun
// cap or backoff; see RerunDecision.
func IsRerunEligible(session state.Session) bool {
	if session.Status != state.SessionStatusIncomplete {
		return false
	}
	return session.IncompleteReason == incompleteReasonCommitsWithoutPR ||
		session.IncompleteReason == incompleteReasonUncommittedOnly ||
		session.IncompleteReason == incompleteReasonNoDurableProgress
}

// detectNewCommits returns true if the issue branch has commits ahead of the
// base branch (or origin/base).
func detectNewCommits(ctx context.Context, runner environment.Runner, session state.Session) bool {
	if strings.TrimSpace(session.WorktreePath) == "" {
		return false
	}
	baseBranch := ProgressBaseBranch(session)
	output, err := runner.Run(ctx, session.WorktreePath, "git", "rev-list", "--count", "origin/"+baseBranch+"..HEAD")
	if err != nil {
		return false
	}
	return strings.TrimSpace(output) != "0" && strings.TrimSpace(output) != ""
}

// worktreeStatus returns `git status --porcelain` for the worktree and
// whether it could be read.
func worktreeStatus(ctx context.Context, runner environment.Runner, session state.Session) (string, bool) {
	if strings.TrimSpace(session.WorktreePath) == "" {
		return "", false
	}
	output, err := runner.Run(ctx, session.WorktreePath, "git", "status", "--porcelain")
	if err != nil {
		return "", false
	}
	return output, true
}

// detectWorktreeChanges returns true when there are uncommitted modifications
// (staged or unstaged) in the worktree.
func detectWorktreeChanges(ctx context.Context, runner environment.Runner, session state.Session) bool {
	status, ok := worktreeStatus(ctx, runner, session)
	return ok && strings.TrimSpace(status) != ""
}

// headSHA returns the worktree's HEAD commit, or "" when it cannot be read.
func headSHA(ctx context.Context, runner environment.Runner, session state.Session) string {
	if strings.TrimSpace(session.WorktreePath) == "" {
		return ""
	}
	output, err := runner.Run(ctx, session.WorktreePath, "git", "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}
