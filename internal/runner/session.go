package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/blocking"
	"github.com/nicobistolfi/vigilante/internal/environment"
	forkmode "github.com/nicobistolfi/vigilante/internal/fork"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/logtime"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/sandbox/container"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/telemetry"
)

func RunIssueSession(ctx context.Context, env *environment.Environment, store *state.Store, issueTracker backend.IssueTracker, target state.WatchTarget, issue ghcli.Issue, session state.Session) state.Session {
	if session.Repo == "" {
		session.Repo = target.Repo
	}
	ctx = environment.WithAccessLogContext(ctx, environment.AccessLogContext{
		ExecutionContext: "session",
		Repo:             session.Repo,
		IssueNumber:      issue.Number,
		Branch:           session.Branch,
		WorktreePath:     session.WorktreePath,
	})
	logPath := store.SessionLogPath(session.Repo, issue.Number)
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.IterationInProgress = false
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
	if err := provider.ValidateRuntimeCompatibility(ctx, env.Runner, selectedProvider); err != nil {
		session.Status = state.SessionStatusFailed
		session.IterationInProgress = false
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "session provider compatibility failed", session, err.Error())
		return session
	}
	if err := forkmode.ConfigureWorktree(ctx, env.Runner, session); err != nil {
		session.Status = state.SessionStatusFailed
		session.IterationInProgress = false
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "fork worktree configuration failed", session, err.Error())
		return session
	}
	if session.ForkMode && strings.TrimSpace(session.ContributingGuide) == "" {
		guide, guidePath, _ := forkmode.DiscoverContributingGuide(ctx, env.Runner, session.WorktreePath)
		session.ContributingGuide = guide
		session.ContributingGuidePath = guidePath
	}
	startItems := []string{
		fmt.Sprintf("Vigilante launched this implementation session in `%s`.", session.WorktreePath),
		fmt.Sprintf("Branch: `%s`.", session.Branch),
		fmt.Sprintf("Current stage: handing the issue off to the configured coding agent (`%s`) for investigation and implementation.", selectedProvider.DisplayName()),
		"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
	}
	if strings.TrimSpace(session.PushRemote) != "" && strings.TrimSpace(session.PushRemote) != "origin" {
		startItems = append(startItems[:2], append([]string{
			fmt.Sprintf("Push target: `%s` (`%s`). Upstream issue and PR context remain `%s`.", session.PushRemote, fallbackSessionText(session.PushRepo, "fork repo pending"), session.Repo),
		}, startItems[2:]...)...)
	}
	if strings.TrimSpace(session.ReusedRemoteBranch) != "" {
		reusedRemote := fallbackSessionText(session.PushRemote, "origin")
		if reusedRemote == "" {
			reusedRemote = "origin"
		}
		baseBranch := strings.TrimSpace(session.BaseBranch)
		if baseBranch == "" {
			baseBranch = "main"
		}
		startItems = []string{
			fmt.Sprintf("Vigilante launched this implementation session in `%s` from existing remote branch `%s/%s`.", session.WorktreePath, reusedRemote, session.ReusedRemoteBranch),
			fmt.Sprintf("Diff summary against `%s`: %s", baseBranch, fallbackSessionText(session.BranchDiffSummary, "diff summary unavailable")),
			fmt.Sprintf("Current stage: handing the issue off to the configured coding agent (`%s`) to continue the existing implementation.", selectedProvider.DisplayName()),
			"Common issue-comment commands: `@vigilanteai resume` retries a blocked session after the underlying problem is fixed, and `@vigilanteai cleanup` removes the local session state for this issue.",
		}
		if strings.TrimSpace(session.PushRemote) != "" && strings.TrimSpace(session.PushRemote) != "origin" {
			startItems = append(startItems[:2], append([]string{
				fmt.Sprintf("Push target: `%s` (`%s`). Upstream issue and PR context remain `%s`.", session.PushRemote, fallbackSessionText(session.PushRepo, "fork repo pending"), session.Repo),
			}, startItems[2:]...)...)
		}
	}
	startBody := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Vigilante Session Start",
		Emoji:      "🧢",
		Percent:    20,
		ETAMinutes: 25,
		Items:      startItems,
		Tagline:    "Make it simple, but significant.",
	})
	// Open the session log writer for real-time streaming and lifecycle events.
	logWriter, logWriterErr := openSessionLogWriter(logPath)
	if logWriterErr == nil {
		defer logWriter.Close()
	}
	writeLifecycleEvent(logWriter, fmt.Sprintf("session started provider=%s issue=%d worktree=%s",
		session.Provider, issue.Number, session.WorktreePath))

	appendSessionLog(logPath, "session started", session, "")
	if err := issueTracker.CommentOnWorkItem(ctx, target.Repo, issue.Number, startBody); err != nil {
		session.Status = state.SessionStatusFailed
		session.IterationInProgress = false
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "start comment failed", session, err.Error())
		return session
	}

	preflightInvocation, err := selectedProvider.BuildIssuePreflightInvocation(provider.IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		session.Status = state.SessionStatusFailed
		session.IterationInProgress = false
		session.LastError = err.Error()
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.LastHeartbeatAt = session.EndedAt
		session.UpdatedAt = session.EndedAt
		appendSessionLog(logPath, "issue preflight invocation build failed", session, err.Error())
		return session
	}
	appendSessionLog(logPath, "issue preflight invocation starting", session, formatInvocationDebug(providerInvocationForExecution(target, session, preflightInvocation)))
	writeLifecycleEvent(logWriter, "preflight invocation starting")
	preflightStart := time.Now()
	preflightOutput, err := runProviderInvocationStreaming(ctx, env.Runner, target, session, preflightInvocation, logWriter)
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			session.Status = state.SessionStatusFailed
			session.IterationInProgress = false
			session.LastError = "session canceled"
			session.EndedAt = time.Now().UTC().Format(time.RFC3339)
			session.LastHeartbeatAt = session.EndedAt
			session.UpdatedAt = session.EndedAt
			writeLifecycleEvent(logWriter, "preflight canceled")
			appendSessionLog(logPath, "issue preflight canceled", session, combineLogDetails(preflightOutput, err.Error()))
			return session
		}
		blocked := classifyBlockedFailure("baseline_preflight", preflightInvocation.Name, preflightOutput, err)
		markSessionBlocked(&session, "baseline_preflight", blocked, time.Now().UTC())
		session.LastError = describeProviderFailure(session, err)
		session.EndedAt = time.Now().UTC().Format(time.RFC3339)
		session.LastHeartbeatAt = session.EndedAt
		session.UpdatedAt = session.EndedAt
		writeLifecycleEvent(logWriter, fmt.Sprintf("preflight failed duration=%s reason=%s", time.Since(preflightStart).Truncate(time.Second), describeExitError(err)))
		appendSessionLog(logPath, "issue preflight failed", session, combineLogDetails(preflightOutput, err.Error()))
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧱",
			Percent:    25,
			ETAMinutes: 15,
			Items:      blockedPreflightItems(blocked, selectedProvider.ID(), preflightOutput, session.ResumeHint),
			Tagline:    "Strong foundations make calm debugging sessions.",
		})
		_ = issueTracker.CommentOnWorkItem(ctx, target.Repo, issue.Number, body)
		return session
	}
	writeLifecycleEvent(logWriter, fmt.Sprintf("preflight succeeded duration=%s", time.Since(preflightStart).Truncate(time.Second)))
	appendSessionLog(logPath, fmt.Sprintf("issue preflight succeeded duration=%s output_bytes=%d", time.Since(preflightStart).Truncate(time.Second), len(preflightOutput)), session, preflightOutput)

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
	appendSessionLog(logPath, "issue invocation starting", session, formatInvocationDebug(providerInvocationForExecution(target, session, invocation)))
	writeLifecycleEvent(logWriter, "implementation invocation starting")
	invocationStart := time.Now()
	output, err := runProviderInvocationStreaming(ctx, env.Runner, target, session, invocation, logWriter)
	session.EndedAt = time.Now().UTC().Format(time.RFC3339)
	session.LastHeartbeatAt = session.EndedAt
	session.UpdatedAt = session.EndedAt
	if err != nil {
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			session.Status = state.SessionStatusFailed
			session.IterationInProgress = false
			session.LastError = "session canceled"
			writeLifecycleEvent(logWriter, "session canceled")
			appendSessionLog(logPath, "session canceled", session, combineLogDetails(output, err.Error()))
			return session
		}
		blocked := classifyBlockedFailure("issue_execution", invocation.Name, output, err)
		markSessionBlocked(&session, "issue_execution", blocked, time.Now().UTC())
		session.LastError = describeProviderFailure(session, err)
		writeLifecycleEvent(logWriter, fmt.Sprintf("implementation failed duration=%s reason=%s", time.Since(invocationStart).Truncate(time.Second), describeExitError(err)))
		appendSessionLog(logPath, fmt.Sprintf("session failed duration=%s output_bytes=%d", time.Since(invocationStart).Truncate(time.Second), len(output)), session, combineLogDetails(output, err.Error()))
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🛑",
			Percent:    95,
			ETAMinutes: 10,
			Items: []string{
				blockedExecutionMessage(blocked, selectedProvider.ID()),
				blocking.CauseLine(blocked),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub.", session.ResumeHint),
			},
			Tagline: "Plans are only good intentions unless they immediately degenerate into hard work.",
		})
		_ = issueTracker.CommentOnWorkItem(ctx, target.Repo, issue.Number, body)
		return session
	}

	session.IterationInProgress = false

	signal := EvaluateSessionProgress(ctx, env.Runner, session)
	if !signal.HasPullRequest {
		pr, err := reconcileSessionPullRequest(ctx, issueTracker, session)
		if err != nil {
			appendSessionLog(logPath, "pull request reconciliation failed", session, err.Error())
		} else if pr != nil {
			updateSessionPullRequestTracking(&session, *pr)
			signal.HasPullRequest = true
		}
	}
	if signal.HasPullRequest {
		session.Status = state.SessionStatusSuccess
		session.IncompleteReason = ""
		writeLifecycleEvent(logWriter, fmt.Sprintf("session completed status=success duration=%s", time.Since(invocationStart).Truncate(time.Second)))
		appendSessionLog(logPath, fmt.Sprintf("session succeeded duration=%s output_bytes=%d", time.Since(invocationStart).Truncate(time.Second), len(output)), session, output)
	} else {
		reason := ClassifyIncompleteReason(signal)
		session.Status = state.SessionStatusIncomplete
		session.IncompleteReason = reason
		writeLifecycleEvent(logWriter, fmt.Sprintf("session completed status=incomplete reason=%s duration=%s commits=%t worktree_changes=%t",
			reason, time.Since(invocationStart).Truncate(time.Second), signal.HasNewCommits, signal.HasWorktreeChanges))
		appendSessionLog(logPath, fmt.Sprintf("session incomplete reason=%s duration=%s output_bytes=%d", reason, time.Since(invocationStart).Truncate(time.Second), len(output)), session, output)
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Incomplete",
			Emoji:      "⚠️",
			Percent:    90,
			ETAMinutes: 5,
			Items:      incompleteSessionItems(session, signal),
			Tagline:    "Progress saved — not done yet.",
		})
		_ = issueTracker.CommentOnWorkItem(ctx, target.Repo, issue.Number, body)
	}
	return session
}

func reconcileSessionPullRequest(ctx context.Context, issueTracker backend.IssueTracker, session state.Session) (*backend.PullRequest, error) {
	prManager, ok := issueTracker.(backend.PullRequestManager)
	if !ok || session.Repo == "" {
		return nil, nil
	}
	head := sessionPullRequestHeadSelector(session)
	if head == "" {
		return nil, nil
	}
	pr, err := prManager.FindPullRequestForBranch(ctx, session.Repo, head)
	if err != nil || pr == nil {
		return pr, err
	}
	return prManager.GetPullRequestDetails(ctx, session.Repo, pr.Number)
}

func updateSessionPullRequestTracking(session *state.Session, pr backend.PullRequest) {
	session.PullRequestNumber = pr.Number
	session.PullRequestURL = strings.TrimSpace(pr.URL)
	session.PullRequestState = strings.TrimSpace(pr.State)
	session.PullRequestHeadBranch = strings.TrimSpace(session.Branch)
	if baseRef := strings.TrimSpace(pr.BaseRefName); baseRef != "" {
		session.PullRequestBaseBranch = baseRef
	}
	if pr.MergedAt != nil {
		session.PullRequestMergedAt = pr.MergedAt.UTC().Format(time.RFC3339)
	} else {
		session.PullRequestMergedAt = ""
	}
}

func sessionPullRequestHeadSelector(session state.Session) string {
	branch := strings.TrimSpace(session.Branch)
	if branch == "" {
		return ""
	}
	if owner := strings.TrimSpace(session.ForkOwner); owner != "" && strings.TrimSpace(session.PushRemote) != "" && strings.TrimSpace(session.PushRemote) != "origin" {
		return owner + ":" + branch
	}
	return branch
}

func fallbackSessionText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func runProviderInvocation(ctx context.Context, runner environment.Runner, target state.WatchTarget, session state.Session, invocation provider.Invocation) (string, error) {
	effective := providerInvocationForExecution(target, session, invocation)
	return runner.Run(ctx, effective.Dir, effective.Name, effective.Args...)
}

func runProviderInvocationStreaming(ctx context.Context, runner environment.Runner, target state.WatchTarget, session state.Session, invocation provider.Invocation, w io.Writer) (string, error) {
	effective := providerInvocationForExecution(target, session, invocation)
	return runStreaming(ctx, runner, effective.Dir, w, effective.Name, effective.Args...)
}

func providerInvocationForExecution(target state.WatchTarget, session state.Session, invocation provider.Invocation) provider.Invocation {
	if session.SandboxMode && strings.TrimSpace(session.SandboxContainerName) != "" {
		return sandboxInvocation(target, session, invocation)
	}
	return invocation
}

func sandboxInvocation(target state.WatchTarget, session state.Session, invocation provider.Invocation) provider.Invocation {
	containerPath := container.ContainerRepoPathDefault
	args := make([]string, 0, len(invocation.Args))
	for _, arg := range invocation.Args {
		updated := strings.ReplaceAll(arg, session.WorktreePath, containerPath)
		updated = strings.ReplaceAll(updated, target.Path, containerPath)
		args = append(args, updated)
	}
	execArgs := []string{"exec", "-w", containerPath, session.SandboxContainerName, invocation.Name}
	execArgs = append(execArgs, args...)
	return provider.Invocation{
		Name: "docker",
		Args: execArgs,
	}
}

func RunConflictResolutionSession(ctx context.Context, env *environment.Environment, store *state.Store, issueTracker backend.IssueTracker, target state.WatchTarget, session state.Session, pr ghcli.PullRequest) error {
	repoSlug := session.Repo
	if repoSlug == "" {
		repoSlug = target.Repo
	}
	ctx = environment.WithAccessLogContext(ctx, environment.AccessLogContext{
		ExecutionContext: "maintenance",
		Repo:             repoSlug,
		IssueNumber:      session.IssueNumber,
		Branch:           session.Branch,
		WorktreePath:     session.WorktreePath,
	})
	logPath := store.SessionLogPath(repoSlug, session.IssueNumber)
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		appendSessionLog(logPath, "conflict resolution provider resolution failed", session, err.Error())
		return err
	}
	session.Provider = selectedProvider.ID()
	logWriter, logWriterErr := openSessionLogWriter(logPath)
	if logWriterErr == nil {
		defer logWriter.Close()
	}
	writeLifecycleEvent(logWriter, fmt.Sprintf("conflict resolution started pr=%d", pr.Number))
	appendSessionLog(logPath, "conflict resolution started", session, fmt.Sprintf("pr=%d url=%s", pr.Number, pr.URL))
	if err := provider.ValidateRuntimeCompatibility(ctx, env.Runner, selectedProvider); err != nil {
		appendSessionLog(logPath, "conflict resolution provider compatibility failed", session, err.Error())
		return err
	}

	invocation, err := selectedProvider.BuildConflictResolutionInvocation(provider.ConflictTask{Target: target, Session: session, PR: pr})
	if err != nil {
		appendSessionLog(logPath, "conflict resolution invocation build failed", session, err.Error())
		return err
	}
	appendSessionLog(logPath, "conflict resolution invocation starting", session, formatInvocationDebug(providerInvocationForExecution(target, session, invocation)))
	writeLifecycleEvent(logWriter, "conflict resolution invocation starting")
	output, err := runProviderInvocationStreaming(ctx, env.Runner, target, session, invocation, logWriter)
	if err != nil {
		writeLifecycleEvent(logWriter, fmt.Sprintf("conflict resolution failed reason=%s", describeExitError(err)))
		appendSessionLog(logPath, "conflict resolution failed", session, combineLogDetails(output, describeProviderFailure(session, err)))
		blocked := classifyBlockedFailure("conflict_resolution", invocation.Name, output, err)
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧯",
			Percent:    90,
			ETAMinutes: 12,
			Items: []string{
				blockedConflictMessage(blocked, pr.Number, session.Branch, selectedProvider.ID()),
				blocking.CauseLine(blocked),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub.", buildResumeHint(session)),
			},
			Tagline: "An obstacle is often a stepping stone.",
		})
		_ = issueTracker.CommentOnWorkItem(ctx, target.Repo, session.IssueNumber, body)
		return err
	}

	writeLifecycleEvent(logWriter, "conflict resolution succeeded")
	appendSessionLog(logPath, "conflict resolution succeeded", session, output)
	return nil
}

func RunCIRemediationSession(ctx context.Context, env *environment.Environment, store *state.Store, issueTracker backend.IssueTracker, target state.WatchTarget, session state.Session, pr ghcli.PullRequest, failingChecks []ghcli.StatusCheckRoll) error {
	repoSlug := session.Repo
	if repoSlug == "" {
		repoSlug = target.Repo
	}
	ctx = environment.WithAccessLogContext(ctx, environment.AccessLogContext{
		ExecutionContext: "maintenance",
		Repo:             repoSlug,
		IssueNumber:      session.IssueNumber,
		Branch:           session.Branch,
		WorktreePath:     session.WorktreePath,
	})
	logPath := store.SessionLogPath(repoSlug, session.IssueNumber)
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		appendSessionLog(logPath, "ci remediation provider resolution failed", session, err.Error())
		return err
	}
	session.Provider = selectedProvider.ID()
	logWriter, logWriterErr := openSessionLogWriter(logPath)
	if logWriterErr == nil {
		defer logWriter.Close()
	}
	writeLifecycleEvent(logWriter, fmt.Sprintf("ci remediation started pr=%d", pr.Number))
	appendSessionLog(logPath, "ci remediation started", session, fmt.Sprintf("pr=%d url=%s", pr.Number, pr.URL))
	if err := provider.ValidateRuntimeCompatibility(ctx, env.Runner, selectedProvider); err != nil {
		appendSessionLog(logPath, "ci remediation provider compatibility failed", session, err.Error())
		return err
	}

	invocation, err := selectedProvider.BuildCIRemediationInvocation(provider.CIRemediationTask{Target: target, Session: session, PR: pr, FailingChecks: failingChecks})
	if err != nil {
		appendSessionLog(logPath, "ci remediation invocation build failed", session, err.Error())
		return err
	}
	appendSessionLog(logPath, "ci remediation invocation starting", session, formatInvocationDebug(providerInvocationForExecution(target, session, invocation)))
	writeLifecycleEvent(logWriter, "ci remediation invocation starting")
	output, err := runProviderInvocationStreaming(ctx, env.Runner, target, session, invocation, logWriter)
	if err != nil {
		writeLifecycleEvent(logWriter, fmt.Sprintf("ci remediation failed reason=%s", describeExitError(err)))
		appendSessionLog(logPath, "ci remediation failed", session, combineLogDetails(output, describeProviderFailure(session, err)))
		blocked := classifyBlockedFailure("ci_remediation", invocation.Name, output, err)
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "CI Remediation Blocked",
			Emoji:      "🧯",
			Percent:    92,
			ETAMinutes: 10,
			Items: []string{
				blockedCIRemediationMessage(blocked, pr.Number, session.Branch, selectedProvider.ID()),
				blocking.CauseLine(blocked),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub.", buildResumeHint(session)),
			},
			Tagline: "Stop the loop before it turns into noise.",
		})
		_ = issueTracker.CommentOnWorkItem(ctx, target.Repo, session.IssueNumber, body)
		return err
	}

	writeLifecycleEvent(logWriter, "ci remediation succeeded")
	appendSessionLog(logPath, "ci remediation succeeded", session, output)
	return nil
}

func summarizeError(err error) string {
	text := strings.TrimSpace(err.Error())
	if len(text) > 400 {
		return text[:400]
	}
	return text
}

func describeProviderFailure(session state.Session, err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if session.SandboxMode && strings.Contains(text, "exit status 137") {
		return text + " (sandboxed coding-agent process was killed; likely memory pressure/OOM inside the container)"
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
	diagnostic := strings.TrimSpace(output + "\n" + err.Error())
	blocked := blocking.Classify(stage, operation, diagnostic, summarizeError(err))
	telemetry.CaptureDownstreamRateLimit(stage, operation, blocked, diagnostic)
	return blocked
}

func blockedPreflightMessage(blocked state.BlockedReason, providerID string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("The `%s` provider hit a usage or subscription limit during issue preflight.", providerID)
	}
	return "Repository baseline validation failed before issue implementation began."
}

func blockedCIRemediationMessage(blocked state.BlockedReason, prNumber int, branch string, providerID string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("CI remediation for PR #%d on `%s` stopped because the `%s` account hit a usage or subscription limit.", prNumber, branch, providerID)
	}
	return fmt.Sprintf("CI remediation for PR #%d on `%s` did not complete automatically.", prNumber, branch)
}

func blockedPreflightItems(blocked state.BlockedReason, providerID string, preflightOutput string, resumeHint string) []string {
	items := []string{
		blockedPreflightMessage(blocked, providerID),
		blocking.CauseLine(blocked),
	}
	if blocked.Kind == "validation_failed" {
		if detail := blockedValidationDetail(blocked); detail != "" {
			items = append(items, fmt.Sprintf("Failed validation: %s.", detail))
		}
		if output := summarizePreflightOutput(preflightOutput); output != "" {
			items = append(items, fmt.Sprintf("Relevant preflight output: %s.", output))
		}
	}
	items = append(items, fmt.Sprintf("Next step: restore the repository baseline, then run `%s` or request resume from GitHub.", resumeHint))
	return items
}

func incompleteSessionItems(session state.Session, signal ProgressSignal) []string {
	items := []string{
		fmt.Sprintf("The coding agent exited successfully but no pull request was created for `%s`.", session.Branch),
	}
	switch {
	case signal.HasNewCommits && signal.HasWorktreeChanges:
		items = append(items, "New commits were pushed to the branch and uncommitted changes remain in the worktree.")
	case signal.HasNewCommits:
		items = append(items, "New commits were pushed to the branch but no PR was opened.")
	case signal.HasWorktreeChanges:
		items = append(items, "Uncommitted changes exist in the worktree but no commits were made.")
	default:
		items = append(items, "No durable progress was detected: no new commits and no modified files.")
	}
	items = append(items, "Next step: Vigilante will attempt to rerun the session in the existing worktree to continue from the current state.")
	return items
}

func blockedExecutionMessage(blocked state.BlockedReason, providerID string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("The `%s` provider hit a usage or subscription limit before the issue implementation completed.", providerID)
	}
	return fmt.Sprintf("The `%s` provider stopped before the issue implementation completed.", providerID)
}

func blockedConflictMessage(blocked state.BlockedReason, prNumber int, branch string, providerID string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("Conflict resolution for PR #%d on `%s` stopped because provider `%s` hit a usage or subscription limit.", prNumber, branch, providerID)
	}
	return fmt.Sprintf("Conflict resolution for PR #%d on `%s` through provider `%s` did not complete.", prNumber, branch, providerID)
}

func blockedValidationDetail(blocked state.BlockedReason) string {
	for _, candidate := range []string{blocked.Summary, blocked.Detail} {
		if text := sanitizeDiagnosticText(candidate, 220); text != "" {
			return text
		}
	}
	return ""
}

func summarizePreflightOutput(output string) string {
	return sanitizeDiagnosticText(output, 280)
}

func sanitizeDiagnosticText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	text = strings.Join(fields, " ")
	text = strings.TrimSpace(strings.TrimRight(text, ".!?"))
	if len(text) > limit {
		if limit <= 3 {
			return text[:limit]
		}
		return strings.TrimSpace(text[:limit-3]) + "..."
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

func formatInvocationDebug(inv provider.Invocation) string {
	lines := []string{
		fmt.Sprintf("dir=%s", inv.Dir),
		fmt.Sprintf("cmd=%s", inv.Name),
	}
	for i, arg := range inv.Args {
		if len(arg) > 200 {
			lines = append(lines, fmt.Sprintf("arg[%d]=(%d bytes) %s...", i, len(arg), arg[:200]))
		} else {
			lines = append(lines, fmt.Sprintf("arg[%d]=%s", i, arg))
		}
	}
	return strings.Join(lines, "\n")
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

// openSessionLogWriter opens the session log file for appending and returns it
// as an io.WriteCloser suitable for streaming provider output.
func openSessionLogWriter(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// writeLifecycleEvent writes a [vigilante HH:MM:SS] prefixed event into the
// session log writer. These events are interleaved with provider output so
// operators see a single chronological timeline.
func writeLifecycleEvent(w io.Writer, msg string) {
	if w == nil {
		return
	}
	ts := logtime.FormatLocal(time.Now())
	_, _ = fmt.Fprintf(w, "[vigilante %s] %s\n", ts, msg)
}

// runStreaming attempts to use StreamingRunner for real-time output, falling
// back to the standard Run method if the runner does not implement it.
func runStreaming(ctx context.Context, runner environment.Runner, dir string, w io.Writer, name string, args ...string) (string, error) {
	if sr, ok := runner.(environment.StreamingRunner); ok && w != nil {
		return sr.RunStreaming(ctx, dir, w, name, args...)
	}
	return runner.Run(ctx, dir, name, args...)
}

// describeExitError returns a human-readable description of the exit code.
// For exit code 137 in sandboxed execution it adds an OOM annotation.
func describeExitError(err error) string {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err.Error()
	}
	code := exitErr.ExitCode()
	if code == 137 {
		return fmt.Sprintf("exit code %d (likely OOM — container killed by kernel)", code)
	}
	return fmt.Sprintf("exit code %d", code)
}
