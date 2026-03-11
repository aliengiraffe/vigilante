package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/repo"
	issuerunner "github.com/nicobistolfi/vigilante/internal/runner"
	"github.com/nicobistolfi/vigilante/internal/service"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/worktree"
)

const defaultScanInterval = 1 * time.Minute
const defaultAssigneeFilter = "me"
const defaultStalledSessionThreshold = 10 * time.Minute

type App struct {
	stdout io.Writer
	stderr io.Writer
	state  *state.Store
	clock  func() time.Time
	env    *environment.Environment
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return errors.New("label cannot be empty")
	}
	*f = append(*f, trimmed)
	return nil
}

func New() *App {
	store := state.NewStore()
	return &App{
		stdout: os.Stdout,
		stderr: os.Stderr,
		state:  store,
		clock:  time.Now().UTC,
		env: &environment.Environment{
			OS: runtime.GOOS,
			Runner: environment.LoggingRunner{
				Base: environment.ExecRunner{},
				Logf: store.AppendDaemonLog,
			},
		},
	}
}

func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.printUsage()
		return 1
	}

	if err := a.runCommand(ctx, args); err != nil {
		fmt.Fprintln(a.stderr, "error:", err)
		return 1
	}

	return 0
}

func (a *App) runCommand(ctx context.Context, args []string) error {
	switch args[0] {
	case "setup":
		fs := flag.NewFlagSet("setup", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		installDaemon := fs.Bool("d", false, "install daemon service")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return a.Setup(ctx, *installDaemon)
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		daemon := fs.Bool("d", false, "install and start daemon service")
		var labels stringListFlag
		fs.Var(&labels, "label", "allow only issues with this label; repeatable")
		assignee := fs.String("assignee", "", "issue assignee filter (defaults to me)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: vigilante watch [-d] [--label value] [--assignee value] <path>")
		}
		return a.Watch(ctx, fs.Arg(0), *daemon, labels, *assignee)
	case "unwatch":
		if len(args) != 2 {
			return errors.New("usage: vigilante unwatch <path>")
		}
		return a.Unwatch(args[1])
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		blockedOnly := fs.Bool("blocked", false, "show blocked sessions instead of watch targets")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return a.List(*blockedOnly)
	case "resume":
		return a.runResumeCommand(ctx, args[1:])
	case "daemon":
		return a.runDaemonCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func (a *App) runResumeCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	repo := fs.String("repo", "", "repository slug")
	issue := fs.Int("issue", 0, "issue number")
	allBlocked := fs.Bool("all-blocked", false, "resume all blocked sessions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *allBlocked {
		if *repo != "" || *issue != 0 {
			return errors.New("usage: vigilante resume --all-blocked")
		}
		return a.ResumeAllBlocked(ctx)
	}
	if *repo == "" || *issue <= 0 {
		return errors.New("usage: vigilante resume --repo <owner/name> --issue <n>")
	}
	return a.ResumeSession(ctx, *repo, *issue, "cli")
}

func (a *App) runDaemonCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: vigilante daemon run [--once]")
	}

	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	once := fs.Bool("once", false, "run a single scan")
	interval := fs.Duration("interval", defaultScanInterval, "scan interval")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	return a.DaemonRun(ctx, *interval, *once)
}

func (a *App) Setup(ctx context.Context, installDaemon bool) error {
	a.state.AppendDaemonLog("setup start install_daemon=%t", installDaemon)
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	if err := a.ensureTooling(ctx); err != nil {
		return err
	}
	if err := skill.EnsureInstalled(a.state.CodexHome()); err != nil {
		return err
	}
	if installDaemon {
		if err := service.Install(ctx, a.env, a.state); err != nil {
			return err
		}
	}
	a.state.AppendDaemonLog("setup complete install_daemon=%t", installDaemon)
	fmt.Fprintln(a.stdout, "setup complete")
	return nil
}

func (a *App) Watch(ctx context.Context, rawPath string, daemon bool, labels []string, assignee string) error {
	a.state.AppendDaemonLog("watch start raw_path=%q daemon=%t assignee=%q", rawPath, daemon, assignee)
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}

	repoPath, err := ExpandPath(rawPath)
	if err != nil {
		return err
	}

	info, err := repo.Discover(ctx, a.env.Runner, repoPath)
	if err != nil {
		return err
	}

	targets, err := a.state.LoadWatchTargets()
	if err != nil {
		return err
	}

	labels = normalizeLabels(labels)

	updated := false
	for i, target := range targets {
		if target.Path == info.Path {
			targets[i].Repo = info.Repo
			targets[i].Branch = info.Branch
			targets[i].Labels = labels
			if assignee != "" {
				targets[i].Assignee = assignee
			} else if targets[i].Assignee == "" {
				targets[i].Assignee = defaultAssigneeFilter
			}
			targets[i].DaemonEnabled = daemon
			updated = true
			break
		}
	}

	if !updated {
		target := state.WatchTarget{
			Path:          info.Path,
			Repo:          info.Repo,
			Branch:        info.Branch,
			Labels:        labels,
			Assignee:      assigneeOrDefault(assignee),
			DaemonEnabled: daemon,
			AddedAt:       a.clock().Format(time.RFC3339),
		}
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Path < targets[j].Path
	})
	if err := a.state.SaveWatchTargets(targets); err != nil {
		return err
	}

	if daemon {
		if err := a.Setup(ctx, true); err != nil {
			return err
		}
	}

	if updated {
		a.state.AppendDaemonLog("watch updated path=%s repo=%s branch=%s assignee=%s daemon=%t", info.Path, info.Repo, info.Branch, assigneeOrDefault(findWatchTargetAssignee(targets, info.Path)), daemon)
		fmt.Fprintln(a.stdout, "updated", info.Path)
	} else {
		a.state.AppendDaemonLog("watch added path=%s repo=%s branch=%s assignee=%s daemon=%t", info.Path, info.Repo, info.Branch, assigneeOrDefault(assignee), daemon)
		fmt.Fprintln(a.stdout, "watching", info.Path)
	}
	return nil
}

func (a *App) Unwatch(rawPath string) error {
	a.state.AppendDaemonLog("unwatch start raw_path=%q", rawPath)
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}

	path, err := ExpandPath(rawPath)
	if err != nil {
		return err
	}

	targets, err := a.state.LoadWatchTargets()
	if err != nil {
		return err
	}

	filtered := targets[:0]
	removed := false
	for _, target := range targets {
		if target.Path == path {
			removed = true
			continue
		}
		filtered = append(filtered, target)
	}
	if !removed {
		return fmt.Errorf("watch target not found for %s", path)
	}
	if err := a.state.SaveWatchTargets(filtered); err != nil {
		return err
	}
	a.state.AppendDaemonLog("unwatch removed path=%s", path)
	fmt.Fprintln(a.stdout, "removed", path)
	return nil
}

func (a *App) List(blockedOnly bool) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	if blockedOnly {
		return a.listBlockedSessions()
	}
	targets, err := a.state.LoadWatchTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Fprintln(a.stdout, "no watch targets configured")
		return nil
	}
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(targets)
}

func (a *App) DaemonRun(ctx context.Context, interval time.Duration, once bool) error {
	if interval <= 0 {
		return errors.New("interval must be positive")
	}
	a.state.AppendDaemonLog("daemon run start once=%t interval=%s", once, interval)

	if once {
		return a.ScanOnce(ctx)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := a.ScanOnce(ctx); err != nil {
			a.state.AppendDaemonLog("scan error err=%v", err)
			fmt.Fprintln(a.stderr, "scan error:", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *App) ScanOnce(ctx context.Context) error {
	a.state.AppendDaemonLog("scan start")
	locked, err := a.state.TryWithScanLock(func() error {
		if err := a.state.EnsureLayout(); err != nil {
			return err
		}

		targets, err := a.state.LoadWatchTargets()
		if err != nil {
			return err
		}
		sessions, err := a.state.LoadSessions()
		if err != nil {
			return err
		}
		sessions, err = a.recoverStalledSessions(ctx, sessions)
		if err != nil {
			return err
		}
		sessions, err = a.processGitHubResumeRequests(ctx, sessions)
		if err != nil {
			return err
		}
		sessions, err = a.maintainPullRequests(ctx, sessions)
		if err != nil {
			return err
		}
		if err := a.state.SaveSessions(sessions); err != nil {
			return err
		}
		startedCount := 0

		for i := range targets {
			target := &targets[i]
			target.Assignee = assigneeOrDefault(target.Assignee)
			a.state.AppendDaemonLog("scan repo start repo=%s path=%s", target.Repo, target.Path)
			issues, err := ghcli.ListOpenIssues(ctx, a.env.Runner, target.Repo, target.Assignee)
			target.LastScanAt = a.clock().Format(time.RFC3339)
			if err != nil {
				if saveErr := a.state.SaveWatchTargets(targets); saveErr != nil {
					return saveErr
				}
				return err
			}
			a.state.AppendDaemonLog("scan repo issues repo=%s open_issues=%d", target.Repo, len(issues))

			next := ghcli.SelectNextIssue(issues, sessions, *target)
			if next == nil {
				a.state.AppendDaemonLog("scan repo no eligible issues repo=%s", target.Repo)
				fmt.Fprintf(a.stdout, "repo: %s no eligible issues (%d open)\n", target.Repo, len(issues))
				continue
			}
			a.state.AppendDaemonLog("scan repo selected issue repo=%s issue=%d title=%q", target.Repo, next.Number, next.Title)

			wt, err := worktree.CreateIssueWorktree(ctx, a.env.Runner, *target, next.Number, next.Title)
			if err != nil {
				return err
			}
			a.state.AppendDaemonLog("scan repo worktree ready repo=%s issue=%d path=%s branch=%s", target.Repo, next.Number, wt.Path, wt.Branch)

			session := state.Session{
				RepoPath:        target.Path,
				Repo:            target.Repo,
				IssueNumber:     next.Number,
				IssueTitle:      next.Title,
				IssueURL:        next.URL,
				Branch:          wt.Branch,
				WorktreePath:    wt.Path,
				Status:          state.SessionStatusRunning,
				ProcessID:       os.Getpid(),
				StartedAt:       a.clock().Format(time.RFC3339),
				LastHeartbeatAt: a.clock().Format(time.RFC3339),
				UpdatedAt:       a.clock().Format(time.RFC3339),
			}
			sessions = upsertSession(sessions, session)
			if err := a.state.SaveSessions(sessions); err != nil {
				return err
			}
			startedCount++
			fmt.Fprintf(a.stdout, "repo: %s started issue #%d in %s\n", target.Repo, next.Number, wt.Path)

			result := issuerunner.RunIssueSession(ctx, a.env, a.state, *target, *next, session)
			sessions = upsertSession(sessions, result)
			if err := a.state.SaveSessions(sessions); err != nil {
				return err
			}
			a.state.AppendDaemonLog("scan repo session finished repo=%s issue=%d status=%s", target.Repo, next.Number, result.Status)
		}

		fmt.Fprintf(a.stdout, "scanned %d watch target(s), started %d issue session(s)\n", len(targets), startedCount)
		a.state.AppendDaemonLog("scan complete targets=%d started=%d", len(targets), startedCount)

		return a.state.SaveWatchTargets(targets)
	})
	if err != nil {
		return err
	}
	if !locked {
		a.state.AppendDaemonLog("scan skipped; another daemon process holds the scan lock")
		fmt.Fprintln(a.stdout, "scan skipped: another vigilante daemon is already scanning")
		return nil
	}
	return nil
}

func (a *App) recoverStalledSessions(ctx context.Context, sessions []state.Session) ([]state.Session, error) {
	threshold := stalledSessionThreshold()

	for i := range sessions {
		session := &sessions[i]
		if session.Status != state.SessionStatusRunning {
			continue
		}
		if sessionProcessAlive(session.ProcessID) {
			continue
		}

		stale, reason := isStalledSession(*session, a.clock(), threshold)
		if !stale {
			continue
		}

		pr, err := ghcli.FindPullRequestForBranch(ctx, a.env.Runner, session.Repo, session.Branch)
		if err != nil {
			return nil, err
		}
		if pr != nil {
			session.Status = state.SessionStatusSuccess
			session.ProcessID = 0
			session.LastHeartbeatAt = ""
			session.PullRequestNumber = pr.Number
			session.PullRequestURL = pr.URL
			session.PullRequestState = pr.State
			if pr.MergedAt != nil {
				session.PullRequestMergedAt = pr.MergedAt.UTC().Format(time.RFC3339)
			}
			session.LastError = ""
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			a.state.AppendDaemonLog("stalled session recovered to pr maintenance repo=%s issue=%d branch=%s reason=%q pr=%d", session.Repo, session.IssueNumber, session.Branch, reason, pr.Number)
			body := ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Implementation In Progress",
				Emoji:      "🔄",
				Percent:    70,
				ETAMinutes: 10,
				Items: []string{
					fmt.Sprintf("The previous local session on `%s` stalled (%s).", session.Branch, reason),
					fmt.Sprintf("An existing PR #%d was found, so Vigilante recovered this issue into PR maintenance.", pr.Number),
					"Next step: keep the PR merge-ready instead of redispatching a new implementation session.",
				},
				Tagline: "Fall seven times, stand up eight.",
			})
			if err := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); err != nil {
				return nil, err
			}
			continue
		}

		if err := worktree.CleanupIssueArtifacts(ctx, a.env.Runner, session.RepoPath, session.WorktreePath, session.Branch); err != nil {
			session.LastError = fmt.Sprintf("stalled session detected (%s) but cleanup failed: %v", reason, err)
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			session.CleanupError = err.Error()
			a.state.AppendDaemonLog("stalled session cleanup failed repo=%s issue=%d branch=%s reason=%q err=%v", session.Repo, session.IssueNumber, session.Branch, reason, err)
			body := ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Blocked",
				Emoji:      "🛠️",
				Percent:    65,
				ETAMinutes: 15,
				Items: []string{
					fmt.Sprintf("The local session on `%s` stalled (%s).", session.Branch, reason),
					fmt.Sprintf("Automatic cleanup failed: `%s`.", summarizeMaintenanceError(err)),
					"Next step: resolve the cleanup problem before redispatching the issue.",
				},
				Tagline: "The gem cannot be polished without friction.",
			})
			if commentErr := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); commentErr != nil {
				return nil, commentErr
			}
			continue
		}

		now := a.clock().Format(time.RFC3339)
		session.Status = state.SessionStatusFailed
		session.ProcessID = 0
		session.LastHeartbeatAt = ""
		session.CleanupError = ""
		session.EndedAt = now
		session.UpdatedAt = now
		session.LastError = fmt.Sprintf("stalled session recovered: %s", reason)
		a.state.AppendDaemonLog("stalled session recovered for redispatch repo=%s issue=%d branch=%s reason=%q", session.Repo, session.IssueNumber, session.Branch, reason)
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Implementation In Progress",
			Emoji:      "🧹",
			Percent:    15,
			ETAMinutes: 20,
			Items: []string{
				fmt.Sprintf("The previous local session on `%s` stalled (%s).", session.Branch, reason),
				"The abandoned worktree state was cleaned up successfully.",
				"Next step: the issue is ready to be redispatched in a fresh worktree.",
			},
			Tagline: "A smooth sea never made a skilled sailor.",
		})
		if err := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); err != nil {
			return nil, err
		}
	}

	return sessions, nil
}

func (a *App) maintainPullRequests(ctx context.Context, sessions []state.Session) ([]state.Session, error) {
	for i := range sessions {
		session := &sessions[i]
		if session.Status != state.SessionStatusSuccess || session.CleanupCompletedAt != "" || session.MonitoringStoppedAt != "" {
			continue
		}

		pr, err := ghcli.FindPullRequestForBranch(ctx, a.env.Runner, session.Repo, session.Branch)
		if err != nil {
			session.LastMaintenanceError = err.Error()
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			a.state.AppendDaemonLog("pr lookup failed repo=%s issue=%d branch=%s err=%v", session.Repo, session.IssueNumber, session.Branch, err)
			continue
		}
		if pr == nil {
			continue
		}

		session.PullRequestNumber = pr.Number
		session.PullRequestURL = pr.URL
		session.PullRequestState = pr.State
		if pr.MergedAt == nil {
			if pr.State != "OPEN" {
				session.MonitoringStoppedAt = a.clock().Format(time.RFC3339)
				session.LastMaintenanceError = ""
				session.UpdatedAt = session.MonitoringStoppedAt
				a.state.AppendDaemonLog("monitoring stopped repo=%s issue=%d pr=%d branch=%s state=%s", session.Repo, session.IssueNumber, pr.Number, session.Branch, pr.State)
				continue
			}
			if err := a.maintainOpenPullRequest(ctx, session, *pr); err != nil {
				session.UpdatedAt = a.clock().Format(time.RFC3339)
				a.state.AppendDaemonLog("pr maintenance failed repo=%s issue=%d pr=%d branch=%s err=%v", session.Repo, session.IssueNumber, pr.Number, session.Branch, err)
				if shouldCommentMaintenanceFailure(*session, err) {
					blocked := classifyBlockedReason("pr_maintenance", "git fetch origin main", err)
					markSessionBlocked(session, "pr_maintenance", blocked, a.clock())
					body := ghcli.FormatProgressComment(ghcli.ProgressComment{
						Stage:      "Blocked",
						Emoji:      "🧱",
						Percent:    85,
						ETAMinutes: 15,
						Items: []string{
							fmt.Sprintf("Vigilante could not keep PR #%d merge-ready on `%s`.", pr.Number, session.Branch),
							fmt.Sprintf("Cause class: `%s`.", blocked.Kind),
							fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub.", session.ResumeHint),
						},
						Tagline: "Difficulties strengthen the mind, as labor does the body.",
					})
					if commentErr := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); commentErr != nil {
						a.state.AppendDaemonLog("pr maintenance failure comment failed repo=%s issue=%d pr=%d err=%v", session.Repo, session.IssueNumber, pr.Number, commentErr)
					}
					session.LastMaintenanceError = err.Error()
				}
				continue
			}
			session.LastMaintenanceError = ""
			continue
		}

		session.PullRequestMergedAt = pr.MergedAt.UTC().Format(time.RFC3339)
		if err := worktree.CleanupIssueArtifacts(ctx, a.env.Runner, session.RepoPath, session.WorktreePath, session.Branch); err != nil {
			session.CleanupError = err.Error()
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			a.state.AppendDaemonLog("cleanup failed repo=%s issue=%d branch=%s worktree=%s err=%v", session.Repo, session.IssueNumber, session.Branch, session.WorktreePath, err)
			continue
		}

		session.CleanupCompletedAt = a.clock().Format(time.RFC3339)
		session.CleanupError = ""
		session.UpdatedAt = session.CleanupCompletedAt
		a.state.AppendDaemonLog(
			"cleanup complete repo=%s issue=%d pr=%d branch=%s worktree=%s merged_at=%s",
			session.Repo,
			session.IssueNumber,
			session.PullRequestNumber,
			session.Branch,
			session.WorktreePath,
			session.PullRequestMergedAt,
		)
	}

	return sessions, nil
}

func (a *App) maintainOpenPullRequest(ctx context.Context, session *state.Session, pr ghcli.PullRequest) error {
	if _, err := a.env.Runner.Run(ctx, session.WorktreePath, "git", "fetch", "origin", "main"); err != nil {
		return err
	}

	statusOutput, err := a.env.Runner.Run(ctx, session.WorktreePath, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(statusOutput) != "" {
		return errors.New("worktree is not clean before PR maintenance")
	}

	rebaseOutput, err := a.env.Runner.Run(ctx, session.WorktreePath, "git", "rebase", "origin/main")
	rebased := rebaseChangedHistory(rebaseOutput)
	if err != nil {
		if !isRebaseConflict(rebaseOutput, err) {
			return err
		}
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Implementation In Progress",
			Emoji:      "⚔️",
			Percent:    75,
			ETAMinutes: 12,
			Items: []string{
				fmt.Sprintf("Rebase conflicts appeared while updating PR #%d onto the latest `origin/main`.", pr.Number),
				fmt.Sprintf("Worktree: `%s`.", session.WorktreePath),
				"Next step: launch the dedicated conflict-resolution skill and continue from the rebased branch.",
			},
			Tagline: "Smooth roads never make skillful drivers.",
		})
		if commentErr := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); commentErr != nil {
			a.state.AppendDaemonLog("pr conflict comment failed repo=%s issue=%d pr=%d err=%v", session.Repo, session.IssueNumber, pr.Number, commentErr)
		}
		target := state.WatchTarget{Path: session.RepoPath, Repo: session.Repo, Branch: "main"}
		if conflictErr := issuerunner.RunConflictResolutionSession(ctx, a.env, a.state, target, *session, pr); conflictErr != nil {
			return conflictErr
		}
		rebased = true
	}

	session.LastMaintainedAt = a.clock().Format(time.RFC3339)
	session.UpdatedAt = session.LastMaintainedAt
	if !rebased {
		return nil
	}

	if _, err := a.env.Runner.Run(ctx, session.WorktreePath, "go", "test", "./..."); err != nil {
		return err
	}
	if _, err := a.env.Runner.Run(ctx, session.WorktreePath, "git", "push", "--force-with-lease", "origin", "HEAD:"+session.Branch); err != nil {
		return err
	}

	body := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Validation Passed",
		Emoji:      "✅",
		Percent:    90,
		ETAMinutes: 5,
		Items: []string{
			fmt.Sprintf("Rebased PR #%d onto the latest `origin/main`.", pr.Number),
			"Reran `go test ./...` after the rebase.",
			fmt.Sprintf("Pushed the updated branch `%s`.", session.Branch),
		},
		Tagline: "Success is where preparation and opportunity meet.",
	})
	return ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body)
}

func (a *App) listBlockedSessions() error {
	sessions, err := a.state.LoadSessions()
	if err != nil {
		return err
	}
	count := 0
	for _, session := range sessions {
		if session.Status != state.SessionStatusBlocked {
			continue
		}
		count++
		fmt.Fprintf(a.stdout, "%s issue #%d  %s\n", session.Repo, session.IssueNumber, blockedStateLabel(session))
		fmt.Fprintf(a.stdout, "  cause: %s\n", fallbackText(session.BlockedReason.Kind, "unknown_operator_action_required"))
		if session.BlockedReason.Operation != "" {
			fmt.Fprintf(a.stdout, "  failed op: %s\n", session.BlockedReason.Operation)
		}
		if session.BlockedAt != "" {
			fmt.Fprintf(a.stdout, "  blocked at: %s\n", session.BlockedAt)
		}
		if session.ResumeHint != "" {
			fmt.Fprintf(a.stdout, "  resume: %s\n", session.ResumeHint)
		}
		fmt.Fprintln(a.stdout, `  github resume: comment "@vigilanteai resume" or add label "resume"`)
	}
	if count == 0 {
		fmt.Fprintln(a.stdout, "no blocked sessions")
	}
	return nil
}

func (a *App) processGitHubResumeRequests(ctx context.Context, sessions []state.Session) ([]state.Session, error) {
	for i := range sessions {
		session := &sessions[i]
		if session.Status != state.SessionStatusBlocked {
			continue
		}

		details, err := ghcli.GetIssueDetails(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		if err != nil {
			return nil, err
		}
		if ghcli.HasAnyLabel(details.Labels, "resume", "vigilante:resume") {
			for _, label := range []string{"resume", "vigilante:resume"} {
				if ghcli.HasAnyLabel(details.Labels, label) {
					if err := ghcli.RemoveIssueLabel(ctx, a.env.Runner, session.Repo, session.IssueNumber, label); err != nil {
						return nil, err
					}
				}
			}
			if err := a.resumeBlockedSession(ctx, session, "label"); err != nil {
				return nil, err
			}
			continue
		}

		comments, err := ghcli.ListIssueComments(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		if err != nil {
			return nil, err
		}
		comment := ghcli.FindResumeComment(comments, session.LastResumeCommentID)
		if comment == nil {
			continue
		}
		if err := ghcli.AddIssueCommentReaction(ctx, a.env.Runner, session.Repo, comment.ID, "salute"); err != nil {
			return nil, err
		}
		session.LastResumeCommentID = comment.ID
		session.LastResumeCommentAt = comment.CreatedAt.UTC().Format(time.RFC3339)
		session.LastResumeSource = "comment"
		if err := a.resumeBlockedSession(ctx, session, "comment"); err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

func (a *App) ResumeAllBlocked(ctx context.Context) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	sessions, err := a.state.LoadSessions()
	if err != nil {
		return err
	}
	resumed := 0
	for i := range sessions {
		if sessions[i].Status != state.SessionStatusBlocked {
			continue
		}
		if err := a.resumeBlockedSession(ctx, &sessions[i], "cli"); err != nil {
			return err
		}
		resumed++
	}
	if err := a.state.SaveSessions(sessions); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "resumed %d blocked session(s)\n", resumed)
	return nil
}

func (a *App) ResumeSession(ctx context.Context, repo string, issue int, source string) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	sessions, err := a.state.LoadSessions()
	if err != nil {
		return err
	}
	found := false
	for i := range sessions {
		if sessions[i].Repo != repo || sessions[i].IssueNumber != issue {
			continue
		}
		found = true
		if sessions[i].Status != state.SessionStatusBlocked {
			return fmt.Errorf("issue #%d in %s is not blocked", issue, repo)
		}
		if err := a.resumeBlockedSession(ctx, &sessions[i], source); err != nil {
			return err
		}
		break
	}
	if !found {
		return fmt.Errorf("blocked session not found for %s issue #%d", repo, issue)
	}
	if err := a.state.SaveSessions(sessions); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "resume attempted for %s issue #%d\n", repo, issue)
	return nil
}

func (a *App) resumeBlockedSession(ctx context.Context, session *state.Session, source string) error {
	if session.Status != state.SessionStatusBlocked {
		return nil
	}
	session.Status = state.SessionStatusResuming
	session.LastResumeSource = source
	session.UpdatedAt = a.clock().Format(time.RFC3339)

	if err := a.preflightResume(ctx, *session); err != nil {
		blocked := classifyBlockedReason(session.BlockedStage, session.BlockedReason.Operation, err)
		markSessionBlocked(session, fallbackText(session.BlockedStage, "pr_maintenance"), blocked, a.clock())
		session.LastError = err.Error()
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧱",
			Percent:    88,
			ETAMinutes: 10,
			Items: []string{
				fmt.Sprintf("Resume preflight did not pass for `%s`.", session.Branch),
				fmt.Sprintf("Cause class: `%s`.", blocked.Kind),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub again.", session.ResumeHint),
			},
			Tagline: "Clear eyes, full hearts, can’t lose.",
		})
		return ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body)
	}

	var err error
	switch session.BlockedStage {
	case "pr_maintenance":
		err = a.resumeBlockedMaintenance(ctx, session)
	case "conflict_resolution":
		err = a.resumeBlockedConflictResolution(ctx, session)
	default:
		err = a.resumeBlockedIssueExecution(ctx, session)
	}
	if err != nil {
		blocked := classifyBlockedReason(session.BlockedStage, session.BlockedReason.Operation, err)
		markSessionBlocked(session, fallbackText(session.BlockedStage, "pr_maintenance"), blocked, a.clock())
		session.LastError = err.Error()
		body := ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Blocked",
			Emoji:      "🧱",
			Percent:    90,
			ETAMinutes: 12,
			Items: []string{
				fmt.Sprintf("Resume did not complete for `%s`.", session.Branch),
				fmt.Sprintf("Cause class: `%s`.", blocked.Kind),
				fmt.Sprintf("Next step: fix the blocker, then run `%s` or request resume from GitHub again.", session.ResumeHint),
			},
			Tagline: "The comeback is always stronger than the setback.",
		})
		return ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body)
	}

	previousKind := session.BlockedReason.Kind
	previousStage := session.BlockedStage
	clearBlockedState(session, a.clock(), source)
	body := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Recovered",
		Emoji:      "🫡",
		Percent:    92,
		ETAMinutes: 5,
		Items: []string{
			fmt.Sprintf("The previous `%s` block was cleared for `%s`.", fallbackText(previousKind, "unknown_operator_action_required"), session.Branch),
			fmt.Sprintf("Resume source: `%s`.", source),
			fmt.Sprintf("Next step: Vigilante resumed `%s` successfully.", fallbackText(previousStage, "issue_execution")),
		},
		Tagline: "Back on the wire.",
	})
	return ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body)
}

func (a *App) preflightResume(ctx context.Context, session state.Session) error {
	switch session.BlockedReason.Kind {
	case "git_auth":
		_, err := a.env.Runner.Run(ctx, session.WorktreePath, "git", "fetch", "origin", "main")
		return err
	case "gh_auth":
		if _, err := a.env.Runner.Run(ctx, "", "gh", "auth", "status"); err != nil {
			return err
		}
		_, err := ghcli.GetIssueDetails(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		return err
	case "provider_missing":
		_, err := a.env.Runner.LookPath("codex")
		return err
	case "provider_auth", "provider_runtime_error":
		if _, err := a.env.Runner.LookPath("codex"); err != nil {
			return err
		}
		_, err := a.env.Runner.Run(ctx, "", "codex", "--version")
		return err
	default:
		return nil
	}
}

func (a *App) resumeBlockedMaintenance(ctx context.Context, session *state.Session) error {
	pr, err := ghcli.FindPullRequestForBranch(ctx, a.env.Runner, session.Repo, session.Branch)
	if err != nil {
		return err
	}
	if pr == nil {
		return errors.New("no pull request found for blocked maintenance session")
	}
	session.PullRequestNumber = pr.Number
	session.PullRequestURL = pr.URL
	session.PullRequestState = pr.State
	if pr.State != "OPEN" {
		return fmt.Errorf("pull request #%d is not open", pr.Number)
	}
	if err := a.maintainOpenPullRequest(ctx, session, *pr); err != nil {
		return err
	}
	session.Status = state.SessionStatusSuccess
	session.LastMaintenanceError = ""
	return nil
}

func (a *App) resumeBlockedIssueExecution(ctx context.Context, session *state.Session) error {
	issue := ghcli.Issue{Number: session.IssueNumber, Title: session.IssueTitle, URL: session.IssueURL}
	target := state.WatchTarget{Path: session.RepoPath, Repo: session.Repo, Branch: "main"}
	prompt := skill.BuildIssuePrompt(target, issue, *session)
	output, err := a.env.Runner.Run(ctx, "", "codex", "exec", "--cd", session.WorktreePath, "--dangerously-bypass-approvals-and-sandbox", prompt)
	session.EndedAt = a.clock().Format(time.RFC3339)
	session.LastHeartbeatAt = session.EndedAt
	session.UpdatedAt = session.EndedAt
	if err != nil {
		a.state.AppendDaemonLog("resume issue execution failed repo=%s issue=%d err=%v output=%s", session.Repo, session.IssueNumber, err, summarizeText(output))
		return err
	}
	session.Status = state.SessionStatusSuccess
	session.LastError = ""
	a.state.AppendDaemonLog("resume issue execution succeeded repo=%s issue=%d output=%s", session.Repo, session.IssueNumber, summarizeText(output))
	return nil
}

func (a *App) resumeBlockedConflictResolution(ctx context.Context, session *state.Session) error {
	pr, err := ghcli.FindPullRequestForBranch(ctx, a.env.Runner, session.Repo, session.Branch)
	if err != nil {
		return err
	}
	if pr == nil {
		return errors.New("no pull request found for blocked conflict-resolution session")
	}
	target := state.WatchTarget{Path: session.RepoPath, Repo: session.Repo, Branch: "main"}
	if err := issuerunner.RunConflictResolutionSession(ctx, a.env, a.state, target, *session, *pr); err != nil {
		return err
	}
	session.Status = state.SessionStatusSuccess
	return nil
}

func stalledSessionThreshold() time.Duration {
	raw := strings.TrimSpace(os.Getenv("VIGILANTE_STALLED_SESSION_THRESHOLD"))
	if raw == "" {
		return defaultStalledSessionThreshold
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return defaultStalledSessionThreshold
	}
	return parsed
}

func isStalledSession(session state.Session, now time.Time, threshold time.Duration) (bool, string) {
	if _, err := os.Stat(session.WorktreePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, "worktree path is missing"
		}
	}

	lastActivity := sessionActivityTime(session)
	if lastActivity.IsZero() {
		return true, "session has no recorded heartbeat"
	}
	if now.Sub(lastActivity) < threshold {
		return false, ""
	}
	if session.ProcessID > 0 {
		return true, fmt.Sprintf("process %d is not running and the session has been idle since %s", session.ProcessID, lastActivity.Format(time.RFC3339))
	}
	return true, fmt.Sprintf("no active process is recorded and the session has been idle since %s", lastActivity.Format(time.RFC3339))
}

func sessionActivityTime(session state.Session) time.Time {
	for _, raw := range []string{session.LastHeartbeatAt, session.UpdatedAt, session.StartedAt} {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func sessionProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

func rebaseChangedHistory(output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	return text != "" && !strings.Contains(text, "up to date")
}

func isRebaseConflict(output string, err error) bool {
	text := strings.ToLower(strings.TrimSpace(output + "\n" + err.Error()))
	return strings.Contains(text, "conflict") || strings.Contains(text, "could not apply")
}

func shouldCommentMaintenanceFailure(session state.Session, err error) bool {
	return strings.TrimSpace(session.LastMaintenanceError) != strings.TrimSpace(err.Error())
}

func summarizeMaintenanceError(err error) string {
	return summarizeText(err.Error())
}

func summarizeText(text string) string {
	text = strings.TrimSpace(text)
	if len(text) > 400 {
		return text[:400]
	}
	return text
}

func classifyBlockedReason(stage string, operation string, err error) state.BlockedReason {
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	reason := state.BlockedReason{
		Kind:      "unknown_operator_action_required",
		Operation: operation,
		Summary:   summarizeMaintenanceError(err),
		Detail:    summarizeMaintenanceError(err),
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
	case strings.Contains(text, "go test") || strings.Contains(text, "validation"):
		reason.Kind = "validation_failed"
	case strings.Contains(text, "network is unreachable") || strings.Contains(text, "timed out"):
		reason.Kind = "network_unreachable"
	case stage == "issue_execution" || stage == "conflict_resolution":
		reason.Kind = "provider_runtime_error"
	}
	return reason
}

func markSessionBlocked(session *state.Session, stage string, blocked state.BlockedReason, now time.Time) {
	session.Status = state.SessionStatusBlocked
	session.BlockedAt = now.Format(time.RFC3339)
	session.BlockedStage = stage
	session.BlockedReason = blocked
	session.RetryPolicy = "paused"
	session.ResumeRequired = true
	session.ResumeHint = fmt.Sprintf("vigilante resume --repo %s --issue %d", session.Repo, session.IssueNumber)
	session.ProcessID = 0
}

func clearBlockedState(session *state.Session, now time.Time, source string) {
	session.Status = state.SessionStatusSuccess
	session.BlockedAt = ""
	session.BlockedReason = state.BlockedReason{}
	session.BlockedStage = ""
	session.RetryPolicy = ""
	session.ResumeRequired = false
	session.ResumeHint = ""
	session.RecoveredAt = now.Format(time.RFC3339)
	session.UpdatedAt = session.RecoveredAt
	session.LastError = ""
	session.LastMaintenanceError = ""
	session.LastResumeSource = source
}

func blockedStateLabel(session state.Session) string {
	switch session.BlockedReason.Kind {
	case "git_auth":
		return "blocked_waiting_for_credentials"
	case "gh_auth":
		return "blocked_waiting_for_github_auth"
	case "provider_auth":
		return "blocked_waiting_for_provider_auth"
	case "provider_missing":
		return "blocked_waiting_for_provider_binary"
	default:
		return "blocked_waiting_for_operator"
	}
}

func fallbackText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (a *App) ensureTooling(ctx context.Context) error {
	for _, tool := range []string{"git", "gh", "codex"} {
		if _, err := a.env.Runner.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required: %w", tool, err)
		}
	}
	if _, err := a.env.Runner.Run(ctx, "", "gh", "auth", "status"); err != nil {
		return fmt.Errorf("gh authentication check failed: %w", err)
	}
	return nil
}

func (a *App) printUsage() {
	fmt.Fprintln(a.stderr, "usage:")
	fmt.Fprintln(a.stderr, "  vigilante setup [-d]")
	fmt.Fprintln(a.stderr, "  vigilante watch [-d] [--label value] [--assignee value] <path>")
	fmt.Fprintln(a.stderr, "  vigilante unwatch <path>")
	fmt.Fprintln(a.stderr, "  vigilante list [--blocked]")
	fmt.Fprintln(a.stderr, "  vigilante resume --repo <owner/name> --issue <n>")
	fmt.Fprintln(a.stderr, "  vigilante resume --all-blocked")
	fmt.Fprintln(a.stderr, "  vigilante daemon run [--once] [--interval duration]")
}

func upsertSession(sessions []state.Session, session state.Session) []state.Session {
	for i := range sessions {
		if sessions[i].Repo == session.Repo && sessions[i].IssueNumber == session.IssueNumber {
			sessions[i] = session
			return sessions
		}
	}
	return append(sessions, session)
}

func ExpandPath(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(raw, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch raw {
		case "~":
			raw = home
		default:
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~/"))
		}
	}
	return filepath.Abs(raw)
}

func assigneeOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultAssigneeFilter
	}
	return value
}

func normalizeLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(labels))
	normalized := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" || seen[label] {
			continue
		}
		seen[label] = true
		normalized = append(normalized, label)
	}

	if len(normalized) == 0 {
		return nil
	}
	sort.Strings(normalized)
	return normalized
}

func findWatchTargetAssignee(targets []state.WatchTarget, path string) string {
	for _, target := range targets {
		if target.Path == path {
			return target.Assignee
		}
	}
	return ""
}
