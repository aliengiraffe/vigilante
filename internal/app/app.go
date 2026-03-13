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
	"sync"
	"syscall"
	"time"

	"github.com/nicobistolfi/vigilante/internal/blocking"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/provider"
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

	sessionMu sync.Mutex
	sessionWG sync.WaitGroup
	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc
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
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		state:   store,
		clock:   time.Now().UTC,
		cancels: make(map[string]context.CancelFunc),
		env: &environment.Environment{
			OS: runtime.GOOS,
			Runner: environment.LoggingRunner{
				Base:             environment.ExecRunner{},
				Logf:             store.AppendDaemonLog,
				LogSuccessOutput: os.Getenv("VIGILANTE_DEBUG_COMMAND_OUTPUT") == "1",
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
		selectedProvider := fs.String("provider", provider.DefaultID, "coding agent provider")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return a.SetupWithProvider(ctx, *installDaemon, *selectedProvider)
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		daemon := fs.Bool("d", false, "install and start daemon service")
		var labels stringListFlag
		fs.Var(&labels, "label", "allow only issues with this label; repeatable")
		assignee := fs.String("assignee", "", "issue assignee filter (defaults to me)")
		maxParallel := fs.Int("max-parallel", 0, "maximum concurrent issue sessions for this repository")
		selectedProvider := fs.String("provider", "", "coding agent provider")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: vigilante watch [-d] [--label value] [--assignee value] [--max-parallel value] [--provider value] <path>")
		}
		return a.WatchWithProvider(ctx, fs.Arg(0), *daemon, labels, *assignee, *maxParallel, *selectedProvider)
	case "unwatch":
		if len(args) != 2 {
			return errors.New("usage: vigilante unwatch <path>")
		}
		return a.Unwatch(args[1])
	case "list":
		fs := flag.NewFlagSet("list", flag.ContinueOnError)
		fs.SetOutput(a.stderr)
		blockedOnly := fs.Bool("blocked", false, "show blocked sessions instead of watch targets")
		runningOnly := fs.Bool("running", false, "show running sessions instead of watch targets")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *blockedOnly && *runningOnly {
			return errors.New("usage: vigilante list [--blocked | --running]")
		}
		return a.List(*blockedOnly, *runningOnly)
	case "cleanup":
		return a.runCleanupCommand(ctx, args[1:])
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

func (a *App) runCleanupCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	repo := fs.String("repo", "", "repository slug")
	issue := fs.Int("issue", 0, "issue number")
	all := fs.Bool("all", false, "clean up all running sessions")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch {
	case *all && (*repo != "" || *issue != 0):
		return errors.New("usage: vigilante cleanup --all")
	case *all:
		return a.CleanupAllRunningSessions(ctx, "cli")
	case *repo == "" && *issue == 0:
		return errors.New("usage: vigilante cleanup --repo <owner/name> [--issue <n>]")
	case *repo == "":
		return errors.New("usage: vigilante cleanup --repo <owner/name> --issue <n>")
	case *issue < 0:
		return errors.New("issue number must be positive")
	case *issue == 0:
		return a.CleanupRepoRunningSessions(ctx, *repo, "cli")
	default:
		return a.CleanupSession(ctx, *repo, *issue, "cli")
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
	return a.SetupWithProvider(ctx, installDaemon, provider.DefaultID)
}

func (a *App) SetupWithProvider(ctx context.Context, installDaemon bool, providerID string) error {
	a.state.AppendDaemonLog("setup start install_daemon=%t", installDaemon)
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	selectedProvider, err := provider.Resolve(providerID)
	if err != nil {
		return err
	}
	if err := a.ensureTooling(ctx, selectedProvider); err != nil {
		return err
	}
	if err := selectedProvider.EnsureRuntimeInstalled(a.state); err != nil {
		return err
	}
	if installDaemon {
		if err := service.Install(ctx, a.env, a.state, selectedProvider); err != nil {
			return err
		}
	}
	a.state.AppendDaemonLog("setup complete install_daemon=%t", installDaemon)
	fmt.Fprintln(a.stdout, "setup complete")
	return nil
}

func (a *App) Watch(ctx context.Context, rawPath string, daemon bool, labels []string, assignee string, maxParallel int) error {
	return a.WatchWithProvider(ctx, rawPath, daemon, labels, assignee, maxParallel, "")
}

func (a *App) WatchWithProvider(ctx context.Context, rawPath string, daemon bool, labels []string, assignee string, maxParallel int, providerID string) error {
	a.state.AppendDaemonLog("watch start raw_path=%q daemon=%t assignee=%q max_parallel=%d", rawPath, daemon, assignee, maxParallel)
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	if maxParallel < 0 {
		return errors.New("max parallel must be at least 1")
	}
	if strings.TrimSpace(providerID) != "" {
		resolvedProvider, err := provider.Resolve(providerID)
		if err != nil {
			return err
		}
		providerID = resolvedProvider.ID()
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
			targets[i].Classification = info.Classification
			if strings.TrimSpace(targets[i].Provider) == "" {
				targets[i].Provider = provider.DefaultID
			}
			if providerID != "" {
				targets[i].Provider = providerID
			}
			targets[i].Labels = labels
			if assignee != "" {
				targets[i].Assignee = assignee
			} else if targets[i].Assignee == "" {
				targets[i].Assignee = defaultAssigneeFilter
			}
			if maxParallel > 0 {
				targets[i].MaxParallel = maxParallel
			}
			targets[i].DaemonEnabled = daemon
			updated = true
			break
		}
	}

	if !updated {
		target := state.WatchTarget{
			Path:           info.Path,
			Repo:           info.Repo,
			Branch:         info.Branch,
			Classification: info.Classification,
			Provider:       provider.DefaultID,
			Labels:         labels,
			Assignee:       assigneeOrDefault(assignee),
			MaxParallel:    configuredMaxParallel(maxParallel),
			DaemonEnabled:  daemon,
			AddedAt:        a.clock().Format(time.RFC3339),
		}
		if providerID != "" {
			target.Provider = providerID
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
		setupProvider := providerID
		if setupProvider == "" {
			setupProvider = findWatchTargetProvider(targets, info.Path)
		}
		if err := a.SetupWithProvider(ctx, true, setupProvider); err != nil {
			return err
		}
	}

	if updated {
		a.state.AppendDaemonLog("watch updated path=%s repo=%s branch=%s assignee=%s max_parallel=%d daemon=%t", info.Path, info.Repo, info.Branch, assigneeOrDefault(findWatchTargetAssignee(targets, info.Path)), findWatchTargetMaxParallel(targets, info.Path), daemon)
		fmt.Fprintln(a.stdout, "updated", info.Path)
	} else {
		a.state.AppendDaemonLog("watch added path=%s repo=%s branch=%s assignee=%s max_parallel=%d daemon=%t", info.Path, info.Repo, info.Branch, assigneeOrDefault(assignee), configuredMaxParallel(maxParallel), daemon)
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

func (a *App) List(blockedOnly bool, runningOnly bool) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	if blockedOnly {
		return a.listBlockedSessions()
	}
	if runningOnly {
		return a.listRunningSessions()
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
		if err := a.ScanOnce(ctx); err != nil {
			return err
		}
		a.waitForSessions()
		return nil
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
		a.sessionMu.Lock()
		defer a.sessionMu.Unlock()
		sessions, err := a.state.LoadSessions()
		if err != nil {
			return err
		}
		sessions, err = a.processGitHubCleanupRequests(ctx, sessions)
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
			if strings.TrimSpace(target.Provider) == "" {
				target.Provider = provider.DefaultID
			}
			target.MaxParallel = configuredMaxParallel(target.MaxParallel)
			target.Classification = repo.Classify(target.Path)
			a.state.AppendDaemonLog("scan repo classified repo=%s shape=%s hints=%d/%d/%d", target.Repo, target.Classification.Shape, len(target.Classification.ProcessHints.WorkspaceConfigFiles), len(target.Classification.ProcessHints.WorkspaceManifestFiles), len(target.Classification.ProcessHints.MultiPackageRoots))
			a.state.AppendDaemonLog("scan repo start repo=%s path=%s max_parallel=%d", target.Repo, target.Path, target.MaxParallel)
			issues, err := ghcli.ListOpenIssues(ctx, a.env.Runner, target.Repo, target.Assignee)
			target.LastScanAt = a.clock().Format(time.RFC3339)
			if err != nil {
				a.state.AppendDaemonLog("scan repo issues failed repo=%s err=%v", target.Repo, err)
				fmt.Fprintf(a.stdout, "repo: %s scan failed: %s\n", target.Repo, summarizeText(err.Error()))
				continue
			}
			a.state.AppendDaemonLog("scan repo issues repo=%s open_issues=%d", target.Repo, len(issues))

			activeCount := ghcli.ActiveSessionCount(sessions, *target)
			availableSlots := target.MaxParallel - activeCount
			if availableSlots < 0 {
				availableSlots = 0
			}
			nextIssues := ghcli.SelectIssues(issues, sessions, *target, availableSlots)
			if len(nextIssues) == 0 {
				a.state.AppendDaemonLog("scan repo no eligible issues repo=%s", target.Repo)
				fmt.Fprintf(a.stdout, "repo: %s no eligible issues (%d open)\n", target.Repo, len(issues))
				continue
			}
			for _, next := range nextIssues {
				a.state.AppendDaemonLog("scan repo selected issue repo=%s issue=%d title=%q", target.Repo, next.Number, next.Title)

				selectedProvider, providerErr := resolveIssueProvider(*target, next)
				if providerErr != nil {
					a.state.AppendDaemonLog("scan repo issue provider conflict repo=%s issue=%d err=%v", target.Repo, next.Number, providerErr)
					fmt.Fprintf(a.stdout, "repo: %s skipped issue #%d: %s\n", target.Repo, next.Number, summarizeText(providerErr.Error()))
					continue
				}
				if selectedProvider != target.Provider {
					a.state.AppendDaemonLog("scan repo issue provider override repo=%s issue=%d provider=%s source=label", target.Repo, next.Number, selectedProvider)
				}
				a.state.AppendDaemonLog("scan repo issue skill repo=%s issue=%d skill=%s shape=%s", target.Repo, next.Number, skill.IssueImplementationSkill(*target), target.Classification.Shape)

				wt, err := worktree.CreateIssueWorktree(ctx, a.env.Runner, *target, next.Number, next.Title)
				if err != nil {
					session := blockedIssueSessionForDispatchFailure(*target, next, selectedProvider, err, a.clock())
					a.state.AppendDaemonLog("scan repo dispatch blocked repo=%s issue=%d err=%v", target.Repo, next.Number, err)
					sessions = upsertSession(sessions, session)
					if err := a.state.SaveSessions(sessions); err != nil {
						return err
					}
					fmt.Fprintf(a.stdout, "repo: %s blocked issue #%d: %s\n", target.Repo, next.Number, summarizeText(err.Error()))
					continue
				}
				a.state.AppendDaemonLog("scan repo worktree ready repo=%s issue=%d path=%s branch=%s", target.Repo, next.Number, wt.Path, wt.Branch)

				session := state.Session{
					RepoPath:        target.Path,
					Repo:            target.Repo,
					Provider:        selectedProvider,
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

				a.launchIssueSession(ctx, *target, next, session)
			}
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
			a.recordSessionFailure(session, "issue_execution", "gh pr list", err)
			a.state.AppendDaemonLog("stalled session pr lookup failed repo=%s issue=%d branch=%s err=%v", session.Repo, session.IssueNumber, session.Branch, err)
			continue
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
				session.LastError = err.Error()
				session.UpdatedAt = a.clock().Format(time.RFC3339)
				a.state.AppendDaemonLog("stalled session recovery comment failed repo=%s issue=%d branch=%s err=%v", session.Repo, session.IssueNumber, session.Branch, err)
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
				session.LastError = commentErr.Error()
				session.UpdatedAt = a.clock().Format(time.RFC3339)
				a.state.AppendDaemonLog("stalled session cleanup comment failed repo=%s issue=%d branch=%s err=%v", session.Repo, session.IssueNumber, session.Branch, commentErr)
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
			session.LastError = err.Error()
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			a.state.AppendDaemonLog("stalled session redispatch comment failed repo=%s issue=%d branch=%s err=%v", session.Repo, session.IssueNumber, session.Branch, err)
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
							maintenanceBlockedMessage(blocked, pr.Number, session.Branch),
							blocking.CauseLine(blocked),
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

func (a *App) launchIssueSession(ctx context.Context, target state.WatchTarget, issue ghcli.Issue, session state.Session) {
	runCtx, cancel := context.WithCancel(ctx)
	key := sessionKey(session.Repo, session.IssueNumber)
	a.cancelMu.Lock()
	a.cancels[key] = cancel
	a.cancelMu.Unlock()

	a.sessionWG.Add(1)
	go func() {
		defer a.sessionWG.Done()
		defer a.clearSessionCancel(key)

		result := issuerunner.RunIssueSession(runCtx, a.env, a.state, target, issue, session)

		a.sessionMu.Lock()
		defer a.sessionMu.Unlock()

		sessions, err := a.state.LoadSessions()
		if err != nil {
			a.state.AppendDaemonLog("session result load failed repo=%s issue=%d err=%v", target.Repo, issue.Number, err)
			return
		}
		if existing, ok := findSession(sessions, target.Repo, issue.Number); ok && existing.LastCleanupSource != "" {
			a.state.AppendDaemonLog("session result ignored after cleanup repo=%s issue=%d source=%s", target.Repo, issue.Number, existing.LastCleanupSource)
			return
		}
		sessions = upsertSession(sessions, result)
		if err := a.state.SaveSessions(sessions); err != nil {
			a.state.AppendDaemonLog("session result save failed repo=%s issue=%d err=%v", target.Repo, issue.Number, err)
			return
		}
		a.state.AppendDaemonLog("scan repo session finished repo=%s issue=%d status=%s", target.Repo, issue.Number, result.Status)
	}()
}

func (a *App) waitForSessions() {
	a.sessionWG.Wait()
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
		if session.BlockedReason.Summary != "" {
			fmt.Fprintf(a.stdout, "  summary: %s\n", session.BlockedReason.Summary)
		}
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

func (a *App) listRunningSessions() error {
	sessions, err := a.state.LoadSessions()
	if err != nil {
		return err
	}
	count := 0
	for _, session := range sessions {
		if session.Status != state.SessionStatusRunning {
			continue
		}
		count++
		fmt.Fprintf(a.stdout, "%s issue #%d  running\n", session.Repo, session.IssueNumber)
		fmt.Fprintf(a.stdout, "  branch: %s\n", session.Branch)
		fmt.Fprintf(a.stdout, "  worktree: %s\n", session.WorktreePath)
		if session.StartedAt != "" {
			fmt.Fprintf(a.stdout, "  started at: %s\n", session.StartedAt)
		}
	}
	if count == 0 {
		fmt.Fprintln(a.stdout, "no running sessions")
	}
	return nil
}

func (a *App) processGitHubCleanupRequests(ctx context.Context, sessions []state.Session) ([]state.Session, error) {
	for i := range sessions {
		session := &sessions[i]

		comments, err := ghcli.ListIssueComments(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		if err != nil {
			a.state.AppendDaemonLog("cleanup comment lookup failed repo=%s issue=%d err=%v", session.Repo, session.IssueNumber, err)
			session.LastError = err.Error()
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			continue
		}
		comment := ghcli.FindCleanupComment(comments, session.LastCleanupCommentID)
		if comment == nil {
			continue
		}
		if err := ghcli.AddIssueCommentReaction(ctx, a.env.Runner, session.Repo, comment.ID, "+1"); err != nil {
			a.state.AppendDaemonLog("cleanup reaction failed repo=%s issue=%d comment=%d err=%v", session.Repo, session.IssueNumber, comment.ID, err)
			session.LastError = err.Error()
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			continue
		}
		session.LastCleanupCommentID = comment.ID
		session.LastCleanupCommentAt = comment.CreatedAt.UTC().Format(time.RFC3339)

		if session.Status != state.SessionStatusRunning {
			session.LastCleanupSource = "comment"
			session.UpdatedAt = a.clock().Format(time.RFC3339)
			body := ghcli.FormatProgressComment(ghcli.ProgressComment{
				Stage:      "Cleanup Checked",
				Emoji:      "🧭",
				Percent:    100,
				ETAMinutes: 1,
				Items: []string{
					"Received `@vigilanteai cleanup` for this issue.",
					"No running Vigilante session matched the request, so there was nothing active to clean up.",
					"Next step: run `vigilante list --running` locally if dispatch still looks blocked.",
				},
				Tagline: "Trust, but verify.",
			})
			if err := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); err != nil {
				a.state.AppendDaemonLog("cleanup no-op comment failed repo=%s issue=%d comment=%d err=%v", session.Repo, session.IssueNumber, comment.ID, err)
				session.LastError = err.Error()
				session.UpdatedAt = a.clock().Format(time.RFC3339)
			}
			continue
		}

		cleanupErr := a.cleanupRunningSession(ctx, session, "comment")
		body := cleanupResultComment(*session, cleanupErr)
		if err := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); err != nil {
			a.state.AppendDaemonLog("cleanup result comment failed repo=%s issue=%d comment=%d err=%v", session.Repo, session.IssueNumber, comment.ID, err)
			session.LastError = err.Error()
			session.UpdatedAt = a.clock().Format(time.RFC3339)
		}
	}
	return sessions, nil
}

func (a *App) processGitHubResumeRequests(ctx context.Context, sessions []state.Session) ([]state.Session, error) {
	for i := range sessions {
		session := &sessions[i]
		if session.Status != state.SessionStatusBlocked {
			continue
		}

		details, err := ghcli.GetIssueDetails(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		if err != nil {
			a.recordSessionFailure(session, fallbackText(session.BlockedStage, "issue_execution"), "gh issue view", err)
			a.state.AppendDaemonLog("resume issue details failed repo=%s issue=%d err=%v", session.Repo, session.IssueNumber, err)
			continue
		}
		if ghcli.HasAnyLabel(details.Labels, "resume", "vigilante:resume") {
			labelRemovalFailed := false
			for _, label := range []string{"resume", "vigilante:resume"} {
				if ghcli.HasAnyLabel(details.Labels, label) {
					if err := ghcli.RemoveIssueLabel(ctx, a.env.Runner, session.Repo, session.IssueNumber, label); err != nil {
						a.recordSessionFailure(session, fallbackText(session.BlockedStage, "issue_execution"), "gh issue edit --remove-label", err)
						a.state.AppendDaemonLog("resume label removal failed repo=%s issue=%d label=%s err=%v", session.Repo, session.IssueNumber, label, err)
						labelRemovalFailed = true
						break
					}
				}
			}
			if labelRemovalFailed {
				continue
			}
			if err := a.resumeBlockedSession(ctx, session, "label"); err != nil {
				a.recordSessionFailure(session, fallbackText(session.BlockedStage, "issue_execution"), fallbackText(session.BlockedReason.Operation, "resume"), err)
				a.state.AppendDaemonLog("resume by label failed repo=%s issue=%d err=%v", session.Repo, session.IssueNumber, err)
			}
			continue
		}

		comments, err := ghcli.ListIssueComments(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		if err != nil {
			a.recordSessionFailure(session, fallbackText(session.BlockedStage, "issue_execution"), "gh issue comments", err)
			a.state.AppendDaemonLog("resume comment lookup failed repo=%s issue=%d err=%v", session.Repo, session.IssueNumber, err)
			continue
		}
		comment := ghcli.FindResumeComment(comments, session.LastResumeCommentID)
		if comment == nil {
			continue
		}
		if err := ghcli.AddIssueCommentReaction(ctx, a.env.Runner, session.Repo, comment.ID, "eyes"); err != nil {
			a.recordSessionFailure(session, fallbackText(session.BlockedStage, "issue_execution"), "gh api issue comment reactions", err)
			a.state.AppendDaemonLog("resume reaction failed repo=%s issue=%d comment=%d err=%v", session.Repo, session.IssueNumber, comment.ID, err)
			continue
		}
		session.LastResumeCommentID = comment.ID
		session.LastResumeCommentAt = comment.CreatedAt.UTC().Format(time.RFC3339)
		session.LastResumeSource = "comment"
		if err := a.resumeBlockedSession(ctx, session, "comment"); err != nil {
			a.recordSessionFailure(session, fallbackText(session.BlockedStage, "issue_execution"), fallbackText(session.BlockedReason.Operation, "resume"), err)
			a.state.AppendDaemonLog("resume by comment failed repo=%s issue=%d comment=%d err=%v", session.Repo, session.IssueNumber, comment.ID, err)
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

func (a *App) CleanupAllRunningSessions(ctx context.Context, source string) error {
	return a.cleanupSessions(ctx, source, "cleaned up %d running session(s)\n", func(session state.Session) bool {
		return session.Status == state.SessionStatusRunning
	})
}

func (a *App) CleanupRepoRunningSessions(ctx context.Context, repo string, source string) error {
	return a.cleanupSessions(ctx, source, "cleaned up %d running session(s) in %s\n", func(session state.Session) bool {
		return session.Status == state.SessionStatusRunning && session.Repo == repo
	}, repo)
}

func (a *App) CleanupSession(ctx context.Context, repo string, issue int, source string) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}

	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	sessions, err := a.state.LoadSessions()
	if err != nil {
		return err
	}

	found := false
	for i := range sessions {
		if sessions[i].Status != state.SessionStatusRunning || sessions[i].Repo != repo || sessions[i].IssueNumber != issue {
			continue
		}
		if err := a.cleanupRunningSession(ctx, &sessions[i], source); err != nil {
			return err
		}
		found = true
		break
	}
	if !found {
		return fmt.Errorf("running session not found for %s issue #%d", repo, issue)
	}
	if err := a.state.SaveSessions(sessions); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "cleaned up running session for %s issue #%d\n", repo, issue)
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

func (a *App) cleanupSessions(ctx context.Context, source string, successFormat string, match func(state.Session) bool, args ...any) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}

	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()

	sessions, err := a.state.LoadSessions()
	if err != nil {
		return err
	}

	cleaned := 0
	for i := range sessions {
		if !match(sessions[i]) {
			continue
		}
		if err := a.cleanupRunningSession(ctx, &sessions[i], source); err != nil {
			return err
		}
		cleaned++
	}
	if cleaned == 0 {
		if len(args) == 2 {
			return fmt.Errorf("running session not found for %s issue #%d", args[0], args[1])
		}
		if len(args) == 1 {
			return fmt.Errorf("no running sessions found for %s", args[0])
		}
		return errors.New("no running sessions found")
	}
	if err := a.state.SaveSessions(sessions); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, successFormat, append([]any{cleaned}, args...)...)
	return nil
}

func (a *App) cleanupRunningSession(ctx context.Context, session *state.Session, source string) error {
	if session.Status != state.SessionStatusRunning {
		return nil
	}

	a.cancelRunningSession(session.Repo, session.IssueNumber)

	now := a.clock().Format(time.RFC3339)
	err := worktree.CleanupIssueArtifacts(ctx, a.env.Runner, session.RepoPath, session.WorktreePath, session.Branch)
	session.Status = state.SessionStatusFailed
	session.ProcessID = 0
	session.LastHeartbeatAt = ""
	session.EndedAt = now
	session.UpdatedAt = now
	session.LastCleanupSource = source
	session.LastError = fmt.Sprintf("cleanup requested via %s", source)
	if err != nil {
		session.CleanupError = err.Error()
		session.LastError = fmt.Sprintf("cleanup requested via %s; artifact cleanup failed: %s", source, err)
		a.state.AppendDaemonLog("running session cleanup failed repo=%s issue=%d source=%s branch=%s worktree=%s err=%v", session.Repo, session.IssueNumber, source, session.Branch, session.WorktreePath, err)
		return nil
	}
	session.CleanupCompletedAt = now
	session.CleanupError = ""
	a.state.AppendDaemonLog("running session cleanup complete repo=%s issue=%d source=%s branch=%s worktree=%s", session.Repo, session.IssueNumber, source, session.Branch, session.WorktreePath)
	return nil
}

func (a *App) resumeBlockedSession(ctx context.Context, session *state.Session, source string) error {
	if session.Status != state.SessionStatusBlocked {
		return nil
	}
	previousStage := session.BlockedStage
	session.Status = state.SessionStatusResuming
	session.LastResumeSource = source
	session.UpdatedAt = a.clock().Format(time.RFC3339)

	if err := a.preflightResume(ctx, *session); err != nil {
		blocked := classifyBlockedReason(session.BlockedStage, session.BlockedReason.Operation, err)
		markSessionBlocked(session, fallbackText(session.BlockedStage, "pr_maintenance"), blocked, a.clock())
		session.LastError = err.Error()
		return a.commentResumeFailure(ctx, session, previousStage)
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
		return a.commentResumeFailure(ctx, session, previousStage)
	}

	previousKind := session.BlockedReason.Kind
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
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		return err
	}
	tool := providerRuntimeTool(selectedProvider)
	switch session.BlockedReason.Kind {
	case "git_auth":
		_, err = a.env.Runner.Run(ctx, session.WorktreePath, "git", "fetch", "origin", "main")
		return err
	case "gh_auth":
		if _, err := a.env.Runner.Run(ctx, "", "gh", "auth", "status"); err != nil {
			return err
		}
		_, err = ghcli.GetIssueDetails(ctx, a.env.Runner, session.Repo, session.IssueNumber)
		return err
	case "provider_missing":
		_, err = a.env.Runner.LookPath(tool)
		return err
	case "provider_auth", "provider_runtime_error":
		if _, err := a.env.Runner.LookPath(tool); err != nil {
			return err
		}
		_, err = a.env.Runner.Run(ctx, "", tool, "--version")
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
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		return err
	}
	if session.BlockedStage == "baseline_preflight" {
		preflightInvocation, err := selectedProvider.BuildIssuePreflightInvocation(provider.IssueTask{
			Target:  target,
			Issue:   issue,
			Session: *session,
		})
		if err != nil {
			return err
		}
		preflightOutput, err := a.env.Runner.Run(ctx, preflightInvocation.Dir, preflightInvocation.Name, preflightInvocation.Args...)
		if err != nil {
			a.state.AppendDaemonLog("resume issue preflight failed repo=%s issue=%d err=%v output=%s", session.Repo, session.IssueNumber, err, summarizeText(preflightOutput))
			return err
		}
		a.state.AppendDaemonLog("resume issue preflight succeeded repo=%s issue=%d output=%s", session.Repo, session.IssueNumber, summarizeText(preflightOutput))
	}
	invocation, err := selectedProvider.BuildIssueInvocation(provider.IssueTask{
		Target:  target,
		Issue:   issue,
		Session: *session,
	})
	if err != nil {
		return err
	}
	output, err := a.env.Runner.Run(ctx, invocation.Dir, invocation.Name, invocation.Args...)
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

func resolveIssueProvider(target state.WatchTarget, issue ghcli.Issue) (string, error) {
	selected := strings.TrimSpace(target.Provider)
	if selected == "" {
		selected = provider.DefaultID
	}

	override, err := provider.ResolveIssueLabel(issue.Labels)
	if err != nil {
		return "", fmt.Errorf("issue #%d has conflicting provider labels: %w", issue.Number, err)
	}
	if override == "" {
		return selected, nil
	}
	return override, nil
}

func blockedIssueSessionForDispatchFailure(target state.WatchTarget, issue ghcli.Issue, selectedProvider string, err error, now time.Time) state.Session {
	session := state.Session{
		RepoPath:     target.Path,
		Repo:         target.Repo,
		Provider:     selectedProvider,
		IssueNumber:  issue.Number,
		IssueTitle:   issue.Title,
		IssueURL:     issue.URL,
		Branch:       worktree.IssueBranchName(issue.Number, issue.Title),
		WorktreePath: worktree.IssueWorktreePath(target.Path, issue.Number),
		Status:       state.SessionStatusFailed,
		StartedAt:    now.Format(time.RFC3339),
		UpdatedAt:    now.Format(time.RFC3339),
		LastError:    err.Error(),
	}
	markSessionBlocked(&session, "issue_execution", classifyBlockedReason("issue_execution", "git worktree add", err), now)
	session.LastError = err.Error()
	session.UpdatedAt = now.Format(time.RFC3339)
	return session
}

func (a *App) recordSessionFailure(session *state.Session, stage string, operation string, err error) {
	markSessionBlocked(session, stage, classifyBlockedReason(stage, operation, err), a.clock())
	session.LastError = err.Error()
	session.UpdatedAt = a.clock().Format(time.RFC3339)
}

func classifyBlockedReason(stage string, operation string, err error) state.BlockedReason {
	return blocking.Classify(stage, operation, err.Error(), summarizeMaintenanceError(err))
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
	session.LastResumeFailureFingerprint = ""
	session.LastResumeFailureCommentedAt = ""
}

func blockedStateLabel(session state.Session) string {
	return blocking.StateLabel(session.BlockedReason.Kind)
}

func maintenanceBlockedMessage(blocked state.BlockedReason, prNumber int, branch string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("Vigilante could not keep PR #%d merge-ready on `%s` because the coding-agent account hit a usage or subscription limit.", prNumber, branch)
	}
	return fmt.Sprintf("Vigilante could not keep PR #%d merge-ready on `%s`.", prNumber, branch)
}

func resumePreflightBlockedMessage(blocked state.BlockedReason, branch string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("Resume preflight stopped for `%s` because the coding-agent account hit a usage or subscription limit.", branch)
	}
	return fmt.Sprintf("Resume preflight did not pass for `%s`.", branch)
}

func resumeBlockedMessage(blocked state.BlockedReason, branch string) string {
	if blocked.Kind == "provider_quota" {
		return fmt.Sprintf("Resume stopped for `%s` because the coding-agent account hit a usage or subscription limit.", branch)
	}
	return fmt.Sprintf("Resume did not complete for `%s`.", branch)
}

func cleanupResultComment(session state.Session, cleanupErr error) string {
	if cleanupErr == nil && session.CleanupError == "" {
		return ghcli.FormatProgressComment(ghcli.ProgressComment{
			Stage:      "Cleanup Completed",
			Emoji:      "🧹",
			Percent:    100,
			ETAMinutes: 1,
			Items: []string{
				fmt.Sprintf("Removed the running Vigilante session for `%s`.", session.Branch),
				fmt.Sprintf("Cleanup source: `%s`.", session.LastCleanupSource),
				fmt.Sprintf("Local worktree artifacts were cleaned up at `%s` when present.", session.WorktreePath),
			},
			Tagline: "Leave no loose ends.",
		})
	}
	return ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Cleanup Attempted",
		Emoji:      "🛠️",
		Percent:    100,
		ETAMinutes: 1,
		Items: []string{
			fmt.Sprintf("Removed the running-session blockage for `%s`.", session.Branch),
			fmt.Sprintf("Cleanup source: `%s`.", session.LastCleanupSource),
			fmt.Sprintf("Local artifact cleanup still needs attention: `%s`.", summarizeMaintenanceError(errors.New(fallbackText(session.CleanupError, "unknown cleanup error")))),
		},
		Tagline: "Progress over paralysis.",
	})
}

func fallbackText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

type resumeFailureDiagnostic struct {
	Step           string `json:"step"`
	Why            string `json:"why"`
	Classification string `json:"classification"`
	NextStep       string `json:"next_step"`
}

func (a *App) commentResumeFailure(ctx context.Context, session *state.Session, previousStage string) error {
	fingerprint := resumeFailureFingerprint(*session)
	if fingerprint == session.LastResumeFailureFingerprint {
		a.state.AppendDaemonLog("resume failure comment suppressed repo=%s issue=%d fingerprint=%s", session.Repo, session.IssueNumber, fingerprint)
		return nil
	}

	diagnostic, err := a.generateResumeFailureDiagnostic(ctx, *session, previousStage)
	if err != nil {
		a.state.AppendDaemonLog("resume failure diagnostic summary failed repo=%s issue=%d err=%v", session.Repo, session.IssueNumber, err)
		diagnostic = deterministicResumeFailureDiagnostic(*session, previousStage)
	}

	body := ghcli.FormatProgressComment(ghcli.ProgressComment{
		Stage:      "Resume Blocked",
		Emoji:      "🧱",
		Percent:    90,
		ETAMinutes: 10,
		Items: []string{
			diagnostic.Step,
			diagnostic.Why,
			fmt.Sprintf("Failure type: `%s` (`%s`). %s", diagnostic.Classification, fallbackText(session.BlockedReason.Kind, "unknown_operator_action_required"), diagnostic.NextStep),
		},
		Tagline: "No mystery errors left behind.",
	})
	if err := ghcli.CommentOnIssue(ctx, a.env.Runner, session.Repo, session.IssueNumber, body); err != nil {
		return err
	}
	session.LastResumeFailureFingerprint = fingerprint
	session.LastResumeFailureCommentedAt = a.clock().Format(time.RFC3339)
	return nil
}

func (a *App) generateResumeFailureDiagnostic(ctx context.Context, session state.Session, previousStage string) (resumeFailureDiagnostic, error) {
	workdir := strings.TrimSpace(session.WorktreePath)
	if workdir == "" {
		workdir = session.RepoPath
	}
	selectedProvider, err := provider.Resolve(session.Provider)
	if err != nil {
		return resumeFailureDiagnostic{}, err
	}
	invocation, err := buildResumeFailureSummaryInvocation(selectedProvider, workdir, buildResumeFailureSummaryPrompt(session, previousStage))
	if err != nil {
		return resumeFailureDiagnostic{}, err
	}
	output, err := a.env.Runner.Run(ctx, invocation.Dir, invocation.Name, invocation.Args...)
	if err != nil {
		return resumeFailureDiagnostic{}, err
	}

	var diagnostic resumeFailureDiagnostic
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &diagnostic); err != nil {
		return resumeFailureDiagnostic{}, fmt.Errorf("parse resume diagnostic summary: %w", err)
	}
	if strings.TrimSpace(diagnostic.Step) == "" || strings.TrimSpace(diagnostic.Why) == "" || strings.TrimSpace(diagnostic.Classification) == "" || strings.TrimSpace(diagnostic.NextStep) == "" {
		return resumeFailureDiagnostic{}, errors.New("resume diagnostic summary missing required fields")
	}
	return diagnostic, nil
}

func buildResumeFailureSummaryInvocation(selectedProvider provider.Provider, workdir string, prompt string) (provider.Invocation, error) {
	switch selectedProvider.ID() {
	case provider.ClaudeID:
		return provider.Invocation{
			Dir:  workdir,
			Name: "claude",
			Args: []string{
				"--print",
				"--permission-mode", "acceptEdits",
				prompt,
			},
		}, nil
	case provider.GeminiID:
		return provider.Invocation{
			Dir:  workdir,
			Name: "gemini",
			Args: []string{
				"--prompt",
				prompt,
				"--yolo",
			},
		}, nil
	case provider.DefaultID:
		fallthrough
	default:
		return provider.Invocation{
			Name: "codex",
			Args: []string{
				"exec",
				"--cd", workdir,
				"--dangerously-bypass-approvals-and-sandbox",
				prompt,
			},
		}, nil
	}
}

func buildResumeFailureSummaryPrompt(session state.Session, previousStage string) string {
	lines := []string{
		"You summarize failed Vigilante resume attempts for GitHub issue comments.",
		"Return only JSON with string fields: step, why, classification, next_step.",
		"Classification must be one of: transient, operator_fixable, provider_related.",
		"Keep every value concise, operator-facing, and under 140 characters. Do not use markdown.",
		fmt.Sprintf("Issue: #%d - %s", session.IssueNumber, fallbackText(session.IssueTitle, "unknown issue")),
		fmt.Sprintf("Branch: %s", fallbackText(session.Branch, "unknown branch")),
		fmt.Sprintf("Resume source: %s", fallbackText(session.LastResumeSource, "unknown")),
		fmt.Sprintf("Blocked stage before resume: %s", fallbackText(previousStage, "unknown")),
		fmt.Sprintf("Failed stage after resume: %s", fallbackText(session.BlockedStage, "unknown")),
		fmt.Sprintf("Failed operation: %s", fallbackText(session.BlockedReason.Operation, "resume")),
		fmt.Sprintf("Failure kind: %s", fallbackText(session.BlockedReason.Kind, "unknown_operator_action_required")),
		fmt.Sprintf("Summary: %s", fallbackText(session.BlockedReason.Summary, summarizeText(session.LastError))),
		fmt.Sprintf("Detail: %s", fallbackText(session.BlockedReason.Detail, summarizeText(session.LastError))),
		fmt.Sprintf("Resume hint: %s", fallbackText(session.ResumeHint, fmt.Sprintf("vigilante resume --repo %s --issue %d", session.Repo, session.IssueNumber))),
	}
	return strings.Join(lines, "\n")
}

func deterministicResumeFailureDiagnostic(session state.Session, previousStage string) resumeFailureDiagnostic {
	stage := fallbackText(previousStage, fallbackText(session.BlockedStage, "issue_execution"))
	operation := fallbackText(session.BlockedReason.Operation, "resume")
	why := fallbackText(session.BlockedReason.Detail, fallbackText(session.BlockedReason.Summary, summarizeText(session.LastError)))
	if why == "" {
		why = "Vigilante could not verify a recoverable state for the blocked session."
	}
	return resumeFailureDiagnostic{
		Step:           fmt.Sprintf("Resume stopped while running `%s` to continue `%s` for `%s`.", operation, stage, fallbackText(session.Branch, "the blocked branch")),
		Why:            fmt.Sprintf("Why it failed: %s", why),
		Classification: resumeFailureClassification(session.BlockedReason.Kind),
		NextStep:       resumeFailureNextStep(session),
	}
}

func resumeFailureClassification(kind string) string {
	switch strings.TrimSpace(kind) {
	case "provider_auth", "provider_missing", "provider_runtime_error":
		return "provider_related"
	case "network_unreachable":
		return "transient"
	default:
		return "operator_fixable"
	}
}

func resumeFailureNextStep(session state.Session) string {
	hint := fallbackText(session.ResumeHint, fmt.Sprintf("vigilante resume --repo %s --issue %d", session.Repo, session.IssueNumber))
	switch strings.TrimSpace(session.BlockedReason.Kind) {
	case "provider_auth":
		return fmt.Sprintf("Re-authenticate the coding agent locally, then retry with `%s` or `@vigilanteai resume`.", hint)
	case "provider_missing":
		return fmt.Sprintf("Install or restore the coding agent runtime, then retry with `%s` or `@vigilanteai resume`.", hint)
	case "git_auth":
		return fmt.Sprintf("Restore git remote access, then retry with `%s` or `@vigilanteai resume`.", hint)
	case "gh_auth":
		return fmt.Sprintf("Refresh GitHub CLI authentication, then retry with `%s` or `@vigilanteai resume`.", hint)
	case "network_unreachable":
		return fmt.Sprintf("Retry once network access is stable, then run `%s` or comment `@vigilanteai resume` again.", hint)
	case "dirty_worktree":
		return fmt.Sprintf("Clean the worktree state, then retry with `%s` or `@vigilanteai resume`.", hint)
	case "validation_failed":
		return fmt.Sprintf("Fix the failing validation, then retry with `%s` or `@vigilanteai resume`.", hint)
	default:
		return fmt.Sprintf("Fix the blocker, then retry with `%s` or `@vigilanteai resume`.", hint)
	}
}

func resumeFailureFingerprint(session state.Session) string {
	return strings.Join([]string{
		fallbackText(session.BlockedStage, "unknown"),
		fallbackText(session.BlockedReason.Kind, "unknown_operator_action_required"),
		fallbackText(session.BlockedReason.Operation, "resume"),
		fallbackText(session.BlockedReason.Summary, ""),
		fallbackText(session.BlockedReason.Detail, ""),
		fallbackText(session.LastError, ""),
	}, "|")
}

func sessionKey(repo string, issue int) string {
	return fmt.Sprintf("%s#%d", repo, issue)
}

func (a *App) cancelRunningSession(repo string, issue int) {
	key := sessionKey(repo, issue)
	a.cancelMu.Lock()
	cancel := a.cancels[key]
	delete(a.cancels, key)
	a.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) clearSessionCancel(key string) {
	a.cancelMu.Lock()
	delete(a.cancels, key)
	a.cancelMu.Unlock()
}

func findSession(sessions []state.Session, repo string, issue int) (state.Session, bool) {
	for _, session := range sessions {
		if session.Repo == repo && session.IssueNumber == issue {
			return session, true
		}
	}
	return state.Session{}, false
}

func (a *App) ensureTooling(ctx context.Context, selectedProvider provider.Provider) error {
	for _, tool := range provider.RequiredToolset(selectedProvider) {
		if _, err := a.env.Runner.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required: %w", tool, err)
		}
	}
	if _, err := a.env.Runner.Run(ctx, "", "gh", "auth", "status"); err != nil {
		return fmt.Errorf("gh authentication check failed: %w", err)
	}
	return nil
}

func providerRuntimeTool(selectedProvider provider.Provider) string {
	tools := selectedProvider.RequiredTools()
	if len(tools) == 0 {
		return selectedProvider.ID()
	}
	return tools[0]
}

func (a *App) printUsage() {
	fmt.Fprintln(a.stderr, "usage:")
	fmt.Fprintln(a.stderr, "  vigilante setup [-d] [--provider value]")
	fmt.Fprintln(a.stderr, "  vigilante watch [-d] [--label value] [--assignee value] [--max-parallel value] [--provider value] <path>")
	fmt.Fprintln(a.stderr, "  vigilante unwatch <path>")
	fmt.Fprintln(a.stderr, "  vigilante list [--blocked | --running]")
	fmt.Fprintln(a.stderr, "  vigilante cleanup --repo <owner/name> [--issue <n>]")
	fmt.Fprintln(a.stderr, "  vigilante cleanup --all")
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

func configuredMaxParallel(value int) int {
	if value <= 0 {
		return state.DefaultMaxParallelSessions
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

func findWatchTargetMaxParallel(targets []state.WatchTarget, path string) int {
	for _, target := range targets {
		if target.Path == path {
			return configuredMaxParallel(target.MaxParallel)
		}
	}
	return state.DefaultMaxParallelSessions
}

func findWatchTargetProvider(targets []state.WatchTarget, path string) string {
	for _, target := range targets {
		if target.Path == path {
			if strings.TrimSpace(target.Provider) == "" {
				return provider.DefaultID
			}
			return target.Provider
		}
	}
	return provider.DefaultID
}
