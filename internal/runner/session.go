package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/logtime"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func RunIssueSession(ctx context.Context, env *environment.Environment, store *state.Store, target state.WatchTarget, issue ghcli.Issue, session state.Session) state.Session {
	logPath := store.SessionLogPath(issue.Number)
	if session.Repo == "" {
		session.Repo = target.Repo
	}
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "session provider resolution failed", session, err.Error())
		return session
	}
	session.Provider = selectedProvider.ID()
	session.ProcessID = os.Getpid()
	session.LastHeartbeatAt = time.Now().UTC().Format(time.RFC3339)
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	startBody := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Vigilante Session Start",
		Emoji:      "🧢",
		Percent:    20,
		ETAMinutes: 25,
		Items: []string{
			fmt.Sprintf("Vigilante launched this implementation session in `%s`.", session.WorktreePath),
			fmt.Sprintf("Branch: `%s`.", session.Branch),
			fmt.Sprintf("Current stage: handing the issue off to the configured coding agent (`%s`) for investigation and implementation.", selectedProvider.DisplayName()),
		},
		Tagline: "Make it simple, but significant.",
	})
	appendSessionLog(logPath, "session started", session, "")
	if err := ghcli.CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, startBody); err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "start comment failed", session, err.Error())
		return session
	}

	preflightInvocation, err := selectedProvider.BuildIssuePreflightInvocation(provider.IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.LastHeartbeatAt = session.EndedAt
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "issue preflight invocation build failed", session, err.Error())
		return session
	}
	preflightOutput, err := env.Runner.Run(ctx, preflightInvocation.Dir, preflightInvocation.Name, preflightInvocation.Args...)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			session.Status = state.SessionStatusFailed
			session.LastError = "session canceled"
			session.EndedAt = time.Now().UTC().Format(time.RFC3339)
			session.LastHeartbeatAt = session.EndedAt
			session.UpdatedAt = session.EndedAt
			appendSessionLog(logPath, "issue preflight canceled", session, combineLogDetails(preflightOutput, err.Error()))
			return session
		}
		blocked := classifyBlockedFailure("baseline_preflight", preflightInvocation.Name, preflightOutput, err)
		markSessionBlocked(&session, "baseline_preflight", blocked, time.Now().UTC())
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.LastHeartbeatAt = session.EndedAt
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "issue preflight failed", session, combineLogDetails(preflightOutput, err.Error()))
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧱",
			Percent:    25,
			ETAMinutes: 15,
			Items: []string{
				"Repository baseline validation failed before issue implementation began.",
				fmt.Sprintf("Cause class: `%s`.", blocked.Kind),
				fmt.Sprintf("Next step: restore the repository baseline, then run `%s` or request resume from GitHub.", session.ResumeHint),
			},
			Tagline: "Strong foundations make calm debugging sessions.",
		})
		_ = ghcli.CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, body)
		return session
	}
	appendSessionLog(logPath, "issue preflight succeeded", session, preflightOutput)

	invocation, err := selectedProvider.BuildIssueInvocation(provider.IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.LastHeartbeatAt = session.EndedAt
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "issue invocation build failed", session, err.Error())
		return session
	}
	output, err := env.Runner.Run(ctx, invocation.Dir, invocation.Name, invocation.Args...)
	session.EndedAt = time.Now().UTC().Format(time.RFC3339)
	session.LastHeartbeatAt = session.EndedAt
	session.UpdatedAt = session.EndedAt
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			session.Status = state.SessionStatusFailed
			session.LastError = "session canceled"
			appendSessionLog(logPath, "session canceled", session, combineLogDetails(output, err.Error()))
			return session
		}
		blocked := classifyBlockedFailure("issue_execution", invocation.Name, output, err)
		markSessionBlocked(&session, "issue_execution", blocked, time.Now().UTC())
		session.LastError = err.Error()
		appendSessionLog(logPath, "session failed", session, combineLogDetails(output, err.Error()))
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🛑",
			Percent:    95,
			ETAMinutes: 10,
			Items: []string{
				fmt.Sprintf("The `%s` provider stopped before the issue implementation completed.", selectedProvider.ID()),
				fmt.Sprintf("Cause class: `%s`.", blocked.Kind),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub.", session.ResumeHint),
			},
			Tagline: "Plans are only good intentions unless they immediately degenerate into hard work.",
		})
		_ = ghcli.CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, body)
		return session
	}

	session.Status = state.SessionStatusSuccess
	appendSessionLog(logPath, "session succeeded", session, output)
	return session
}

func RunConflictResolutionSession(ctx context.Context, env *environment.Environment, store *state.Store, target state.WatchTarget, session state.Session, pr ghcli.PullRequest) error {
	logPath := store.SessionLogPath(session.IssueNumber)
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		appendSessionLog(logPath, "conflict resolution provider resolution failed", session, err.Error())
		return err
	}
	session.Provider = selectedProvider.ID()
	appendSessionLog(logPath, "conflict resolution started", session, fmt.Sprintf("pr=%d url=%s", pr.Number, pr.URL))

	invocation, err := selectedProvider.BuildConflictResolutionInvocation(provider.ConflictTask{Target: target, Session: session, PR: pr})
	if err != nil {
		appendSessionLog(logPath, "conflict resolution invocation build failed", session, err.Error())
		return err
	}
	output, err := env.Runner.Run(ctx, invocation.Dir, invocation.Name, invocation.Args...)
	if err != nil {
		appendSessionLog(logPath, "conflict resolution failed", session, combineLogDetails(output, err.Error()))
		blocked := classifyBlockedFailure("conflict_resolution", invocation.Name, output, err)
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧯",
			Percent:    90,
			ETAMinutes: 12,
			Items: []string{
				fmt.Sprintf("Conflict resolution for PR #%d on `%s` through provider `%s` did not complete.", pr.Number, session.Branch, selectedProvider.ID()),
				fmt.Sprintf("Cause class: `%s`.", blocked.Kind),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub.", buildResumeHint(session)),
			},
			Tagline: "An obstacle is often a stepping stone.",
		})
		_ = ghcli.CommentOnIssue(ctx, env.Runner, target.Repo, session.IssueNumber, body)
		return err
	}

	appendSessionLog(logPath, "conflict resolution succeeded", session, output)
	return nil
}

func summarizeError(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 400 {
		return text[:400]
	}
	return text
}

func markSessionBlocked(session *state.Session, stage string, blocked state.BlockedReason, now time.Time) {
	session.Status = state.SessionStatusBlocked
	session.BlockedAt = now.Format(time.RFC3339)
	session.BlockedStage = stage
	session.BlockedReason = blocked
	session.RetryPolicy = "paused"
	session.ResumeRequired = true
	session.ResumeHint = buildResumeHint(*session)
	session.ProcessID = 0
	session.RecoveredAt = ""
}

func buildResumeHint(session state.Session) string {
	return fmt.Sprintf("vigilante resume --repo %s --issue %d", session.Repo, session.IssueNumber)
}

func classifyBlockedFailure(stage string, operation string, output string, err error) state.BlockedReason {
	text := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	reason := state.BlockedReason{
		Kind:      "unknown_operator_action_required",
		Operation: operation,
		Summary:   summarizeError(err),
		Detail:    summarizeError(errors.New(strings.TrimSpace(output))),
	}
	switch {
	case strings.Contains(text, "permission denied (publickey)") || strings.Contains(text, "sign_and_send_pubkey") || strings.Contains(text, "could not read from remote repository"):
		reason.Kind = "git_auth"
	case strings.Contains(text, "gh auth") || strings.Contains(text, "not logged into") || strings.Contains(text, "authentication failed"):
		reason.Kind = "gh_auth"
	case strings.Contains(text, "session expired") || strings.Contains(text, "re-auth") || strings.Contains(text, "login required") || strings.Contains(text, "unauthorized"):
		reason.Kind = "provider_auth"
	case strings.Contains(text, "executable file not found") || strings.Contains(text, "no such file or directory"):
		reason.Kind = "provider_missing"
	case strings.Contains(text, "worktree is not clean"):
		reason.Kind = "dirty_worktree"
	case strings.Contains(text, "go test") || strings.Contains(text, "validation") || strings.Contains(text, "build failed") || strings.Contains(text, "tests failed"):
		reason.Kind = "validation_failed"
	case strings.Contains(text, "network is unreachable") || strings.Contains(text, "timed out"):
		reason.Kind = "network_unreachable"
	case strings.Contains(strings.ToLower(stage), "issue") || strings.Contains(strings.ToLower(stage), "conflict") || strings.Contains(strings.ToLower(stage), "preflight"):
		reason.Kind = "provider_runtime_error"
	}
	if reason.Detail == "" {
		reason.Detail = reason.Summary
	}
	return reason
}

func appendSessionLog(path string, event string, session state.Session, details string) {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = fmt.Fprintf(f, "[%s] %s issue=%d provider=%s branch=%s worktree=%s status=%s\n",
		logtime.FormatLocal(time.Now()),
		event,
		session.IssueNumber,
		session.Provider,
		session.Branch,
		session.WorktreePath,
		session.Status,
	)
	if strings.TrimSpace(details) != "" {
		_, _ = fmt.Fprintln(f, strings.TrimSpace(details))
	}
	_, _ = fmt.Fprintln(f)
}

func combineLogDetails(output string, errText string) string {
	output = strings.TrimSpace(output)
	errText = strings.TrimSpace(errText)
	switch {
	case output == "":
		return errText
	case errText == "":
		return output
	default:
		return output + "\n" + errText
	}
}

func filepathDir(path string) string {
	last := strings.LastIndex(path, "/")
	if last <= 0 {
		return "."
	}
	return path[:last]
}
