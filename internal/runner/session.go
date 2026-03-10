package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func RunIssueSession(ctx context.Context, env *environment.Environment, store *state.Store, target state.WatchTarget, issue ghcli.Issue, session state.Session) state.Session {
	logPath := store.SessionLogPath(issue.Number)
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	startBody := fmt.Sprintf("Vigilante started a Codex session for this issue in `%s` on branch `%s`.", session.WorktreePath, session.Branch)
	appendSessionLog(logPath, "session started", session, "")
	if err := ghcli.CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, startBody); err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "start comment failed", session, err.Error())
		return session
	}

	prompt := skill.BuildIssuePrompt(target, issue, session)
	output, err := env.Runner.Run(
		ctx,
		"",
		"codex",
		"exec",
		"--cd", session.WorktreePath,
		"--dangerously-bypass-approvals-and-sandbox",
		prompt,
	)
	session.EndedAt = time.Now().UTC().Format(time.RFC3339)
	session.UpdatedAt = session.EndedAt
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		appendSessionLog(logPath, "session failed", session, combineLogDetails(output, err.Error()))
		body := fmt.Sprintf("Vigilante Codex session failed for this issue: %s", summarizeError(err))
		_ = ghcli.CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, body)
		return session
	}

	session.Status = state.SessionStatusSuccess
	appendSessionLog(logPath, "session succeeded", session, output)
	return session
}

func RunConflictResolutionSession(ctx context.Context, env *environment.Environment, store *state.Store, target state.WatchTarget, session state.Session, pr ghcli.PullRequest) error {
	logPath := store.SessionLogPath(session.IssueNumber)
	appendSessionLog(logPath, "conflict resolution started", session, fmt.Sprintf("pr=%d url=%s", pr.Number, pr.URL))

	prompt := skill.BuildConflictResolutionPrompt(target, session, pr)
	output, err := env.Runner.Run(
		ctx,
		"",
		"codex",
		"exec",
		"--cd", session.WorktreePath,
		"--dangerously-bypass-approvals-and-sandbox",
		prompt,
	)
	if err != nil {
		appendSessionLog(logPath, "conflict resolution failed", session, combineLogDetails(output, err.Error()))
		body := fmt.Sprintf("Vigilante conflict-resolution session failed for PR #%d on branch `%s`: %s", pr.Number, session.Branch, summarizeError(err))
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

func appendSessionLog(path string, event string, session state.Session, details string) {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = fmt.Fprintf(f, "[%s] %s issue=%d branch=%s worktree=%s status=%s\n",
		time.Now().UTC().Format(time.RFC3339),
		event,
		session.IssueNumber,
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
