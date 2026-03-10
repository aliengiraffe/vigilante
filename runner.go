package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func RunIssueSession(ctx context.Context, env *Environment, state *StateStore, target WatchTarget, issue GitHubIssue, session Session) Session {
	session.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	startBody := fmt.Sprintf("Vigilante started a Codex session for this issue in `%s` on branch `%s`.", session.WorktreePath, session.Branch)
	if err := CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, startBody); err != nil {
		session.Status = SessionStatusFailed
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		return session
	}

	prompt := BuildIssuePrompt(target, issue, session)
	_, err := env.Runner.Run(
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
		session.Status = SessionStatusFailed
		session.LastError = err.Error()
		body := fmt.Sprintf("Vigilante Codex session failed for this issue: %s", summarizeError(err))
		_ = CommentOnIssue(ctx, env.Runner, target.Repo, issue.Number, body)
		return session
	}

	session.Status = SessionStatusSuccess
	return session
}

func summarizeError(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 400 {
		return text[:400]
	}
	return text
}
