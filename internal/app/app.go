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
		return a.List()
	case "daemon":
		return a.runDaemonCommand(ctx, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
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

func (a *App) List() error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
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

			wt, err := worktree.CreateIssueWorktree(ctx, a.env.Runner, *target, next.Number)
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
					body := ghcli.FormatProgressComment(ghcli.ProgressComment{
						Stage:      "Blocked",
						Emoji:      "🧱",
						Percent:    85,
						ETAMinutes: 15,
						Items: []string{
							fmt.Sprintf("Vigilante could not keep PR #%d merge-ready on `%s`.", pr.Number, session.Branch),
							fmt.Sprintf("Failure detail: `%s`.", summarizeMaintenanceError(err)),
							"Next step: inspect the branch state, fix the maintenance failure, and rerun validation.",
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
	text := strings.TrimSpace(err.Error())
	if len(text) > 400 {
		return text[:400]
	}
	return text
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
	fmt.Fprintln(a.stderr, "  vigilante list")
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
