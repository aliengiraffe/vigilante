package app

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nicobistolfi/vigilante/internal/backend"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// blockingTracker parks the scan inside ListOpenWorkItems until released, so a
// test can act while a scan is mid-flight exactly where it would be waiting on
// GitHub.
type blockingTracker struct {
	backend.IssueTracker
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingTracker) ListOpenWorkItems(ctx context.Context, _ string, _ string) ([]backend.WorkItem, error) {
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil, nil
}

// A session that finishes while a scan is blocked on GitHub must be persisted
// immediately, and the scan's own commit must not undo it afterwards.
func TestSessionResultPersistsWhileScanIsBlocked(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)

	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		Outputs: map[string]string{
			"gh api user --jq .login": "nicobistolfi\n",
		},
	}
	tracker := &blockingTracker{IssueTracker: app.issueTracker, entered: make(chan struct{}), release: make(chan struct{})}
	app.issueTracker = tracker
	app.issueTrackers[string(backend.BackendGitHub)] = tracker

	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	disabled := false
	if err := app.state.SaveServiceConfig(state.ServiceConfig{PackageHardeningEnabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	target := state.WatchTarget{Path: repoPath, Repo: "owner/repo", Branch: "main", Assignee: "me", Provider: "codex"}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{target}); err != nil {
		t.Fatal(err)
	}

	scanDone := make(chan error, 1)
	go func() { scanDone <- app.ScanOnce(context.Background()) }()
	select {
	case <-tracker.entered:
	case err := <-scanDone:
		t.Fatalf("scan finished before reaching the tracker: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("scan never reached the tracker")
	}

	result := state.Session{Repo: "owner/repo", IssueNumber: 7, Provider: "codex", Status: state.SessionStatusSuccess, StartedAt: "2026-10-26T10:00:00Z", EndedAt: "2026-10-26T10:05:00Z", Branch: "vigilante/issue-7", PullRequestNumber: 70}
	recorded := make(chan struct{})
	go func() {
		app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 7, Title: "seven"}, result)
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(10 * time.Second):
		t.Fatal("session result was not recorded while the scan was blocked")
	}
	stored, ok, err := app.state.GetSession("owner/repo", 7)
	if err != nil || !ok || stored.Status != state.SessionStatusSuccess || stored.PullRequestNumber != 70 {
		t.Fatalf("result not persisted mid-scan: ok=%v err=%v session=%#v", ok, err, stored)
	}
	select {
	case err := <-scanDone:
		t.Fatalf("scan must still be blocked, finished with %v", err)
	default:
	}

	close(tracker.release)
	select {
	case err := <-scanDone:
		if err != nil {
			t.Fatalf("scan failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("scan did not finish after release")
	}
	app.waitForSessions()
	stored, ok, err = app.state.GetSession("owner/repo", 7)
	if err != nil || !ok || stored.Status != state.SessionStatusSuccess {
		t.Fatalf("scan commit must not undo the result: ok=%v err=%v session=%#v", ok, err, stored)
	}
}

// A result for a run that a newer run superseded is dropped, and a result for
// a session that was cleaned up meanwhile is dropped, in both cases without
// touching the stored row.
func TestRecordIssueSessionResultIgnoresSupersededAndCleanedRuns(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	app := New()
	app.stdout = &bytes.Buffer{}
	app.stderr = testutil.IODiscard{}
	app.env.Runner = testutil.FakeRunner{}
	target := state.WatchTarget{Repo: "owner/repo", Provider: "codex"}

	if err := app.state.ReplaceSessions([]state.Session{
		{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusRunning, StartedAt: "2026-10-26T11:00:00Z"},
		{Repo: "owner/repo", IssueNumber: 2, Provider: "codex", Status: state.SessionStatusClosed, LastCleanupSource: "cli"},
	}); err != nil {
		t.Fatal(err)
	}
	app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 1}, state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusFailed, StartedAt: "2026-10-26T10:00:00Z"})
	app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 2}, state.Session{Repo: "owner/repo", IssueNumber: 2, Provider: "codex", Status: state.SessionStatusFailed})
	sessions, err := app.state.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if sessions[0].Status != state.SessionStatusRunning || sessions[1].Status != state.SessionStatusClosed {
		t.Fatalf("stale results must be ignored: %#v", sessions)
	}

	// The current run's result is written.
	app.recordIssueSessionResult(context.Background(), target, ghcli.Issue{Number: 1}, state.Session{Repo: "owner/repo", IssueNumber: 1, Provider: "codex", Status: state.SessionStatusIncomplete, StartedAt: "2026-10-26T11:00:00Z"})
	got, _, _ := app.state.GetSession("owner/repo", 1)
	if got.Status != state.SessionStatusIncomplete {
		t.Fatalf("current run result not written: %#v", got)
	}
}
