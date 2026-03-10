package main

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
	"time"
)

const defaultScanInterval = 5 * time.Minute

type App struct {
	stdout io.Writer
	stderr io.Writer
	state  *StateStore
	clock  func() time.Time
	env    *Environment
}

func NewApp() *App {
	return &App{
		stdout: os.Stdout,
		stderr: os.Stderr,
		state:  NewStateStore(),
		clock:  time.Now().UTC,
		env:    NewEnvironment(runtime.GOOS),
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
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: vigilante watch [-d] <path>")
		}
		return a.Watch(ctx, fs.Arg(0), *daemon)
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
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}
	if err := a.ensureTooling(ctx); err != nil {
		return err
	}
	if err := EnsureSkillInstalled(a.state.CodexHome()); err != nil {
		return err
	}
	if installDaemon {
		if err := InstallService(ctx, a.env, a.state); err != nil {
			return err
		}
	}
	fmt.Fprintln(a.stdout, "setup complete")
	return nil
}

func (a *App) Watch(ctx context.Context, rawPath string, daemon bool) error {
	if err := a.state.EnsureLayout(); err != nil {
		return err
	}

	repoPath, err := ExpandPath(rawPath)
	if err != nil {
		return err
	}

	info, err := DiscoverRepository(ctx, a.env.Runner, repoPath)
	if err != nil {
		return err
	}

	targets, err := a.state.LoadWatchTargets()
	if err != nil {
		return err
	}

	updated := false
	for i, target := range targets {
		if target.Path == info.Path {
			targets[i].Repo = info.Repo
			targets[i].Branch = info.Branch
			targets[i].DaemonEnabled = daemon
			updated = true
			break
		}
	}

	if !updated {
		target := WatchTarget{
			Path:          info.Path,
			Repo:          info.Repo,
			Branch:        info.Branch,
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
		fmt.Fprintln(a.stdout, "updated", info.Path)
	} else {
		fmt.Fprintln(a.stdout, "watching", info.Path)
	}
	return nil
}

func (a *App) Unwatch(rawPath string) error {
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

	if once {
		return a.ScanOnce(ctx)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := a.ScanOnce(ctx); err != nil {
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

	for i := range targets {
		target := &targets[i]
		issues, err := ListOpenIssues(ctx, a.env.Runner, target.Repo)
		target.LastScanAt = a.clock().Format(time.RFC3339)
		if err != nil {
			if saveErr := a.state.SaveWatchTargets(targets); saveErr != nil {
				return saveErr
			}
			return err
		}

		next := SelectNextIssue(issues, sessions, target.Repo)
		if next == nil {
			continue
		}

		worktree, err := CreateIssueWorktree(ctx, a.env.Runner, *target, next.Number)
		if err != nil {
			return err
		}

		session := Session{
			RepoPath:     target.Path,
			Repo:         target.Repo,
			IssueNumber:  next.Number,
			IssueTitle:   next.Title,
			IssueURL:     next.URL,
			Branch:       worktree.Branch,
			WorktreePath: worktree.Path,
			Status:       SessionStatusRunning,
			StartedAt:    a.clock().Format(time.RFC3339),
			UpdatedAt:    a.clock().Format(time.RFC3339),
		}
		sessions = upsertSession(sessions, session)
		if err := a.state.SaveSessions(sessions); err != nil {
			return err
		}

		result := RunIssueSession(ctx, a.env, a.state, *target, *next, session)
		sessions = upsertSession(sessions, result)
		if err := a.state.SaveSessions(sessions); err != nil {
			return err
		}
	}

	return a.state.SaveWatchTargets(targets)
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
	fmt.Fprintln(a.stderr, "  vigilante watch [-d] <path>")
	fmt.Fprintln(a.stderr, "  vigilante unwatch <path>")
	fmt.Fprintln(a.stderr, "  vigilante list")
	fmt.Fprintln(a.stderr, "  vigilante daemon run [--once] [--interval duration]")
}

func upsertSession(sessions []Session, session Session) []Session {
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
