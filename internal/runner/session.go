package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/logtime"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func RunIssueSession(ctx context.Context, env *environment.Environment, store *state.Store, target state.WatchTarget, issue ghcli.Issue, session state.Session) state.Session {
	logPath := store.SessionLogPath(issue.Number)
	session.ProcessID = os.Getpid()
	session.LastHeartbeatAt = time.Now().UTC().Format(time.RFC3339)
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	startBody := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Session Start",
		Emoji:      "🚦",
		Percent:    20,
		ETAMinutes: 25,
		Items: []string{
			fmt.Sprintf("Vigilante launched this implementation session in `%s`.", session.WorktreePath),
			fmt.Sprintf("Branch: `%s`.", session.Branch),
			"Current stage: handing the issue off to Codex for investigation and implementation.",
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
	session.LastHeartbeatAt = session.EndedAt
	session.UpdatedAt = session.EndedAt
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.LastError = err.Error()
		appendSessionLog(logPath, "session failed", session, combineLogDetails(output, err.Error()))
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🛑",
			Percent:    95,
			ETAMinutes: 10,
			Items: []string{
				"Codex execution stopped before the issue implementation completed.",
				fmt.Sprintf("Failure detail: `%s`.", summarizeError(err)),
				"Next step: inspect the failing command or environment and redispatch once the blocker is resolved.",
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
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧯",
			Percent:    90,
			ETAMinutes: 12,
			Items: []string{
				fmt.Sprintf("Conflict resolution for PR #%d on `%s` did not complete.", pr.Number, session.Branch),
				fmt.Sprintf("Failure detail: `%s`.", summarizeError(err)),
				"Next step: review the rebase state in the worktree and rerun the dedicated conflict-resolution flow.",
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
		logtime.FormatLocal(time.Now()),
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
