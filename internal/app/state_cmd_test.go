package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestStateExportCommandWritesLegacyShapes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	app := New()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = testutil.IODiscard{}
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/repos/a", Repo: "owner/repo", Provider: "claude"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.state.ReplaceSessions([]state.Session{{Repo: "owner/repo", IssueNumber: 3, Provider: "claude", Status: state.SessionStatusBlocked}}); err != nil {
		t.Fatal(err)
	}

	if code := app.Run(context.Background(), []string{"state", "export"}); code != 0 {
		t.Fatalf("exit code %d: %s", code, stdout.String())
	}
	var export state.LegacyExport
	if err := json.Unmarshal(stdout.Bytes(), &export); err != nil {
		t.Fatalf("stdout is not the export JSON: %v\n%s", err, stdout.String())
	}
	if len(export.WatchTargets) != 1 || len(export.Sessions) != 1 || export.Sessions[0].IssueNumber != 3 {
		t.Fatalf("unexpected export: %#v", export)
	}
	if export.Config.BlockedSessionInactivityTimeout == "" {
		t.Fatalf("config defaults missing from export: %#v", export.Config)
	}

	dir := filepath.Join(home, "export")
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"state", "export", "--dir", dir}); code != 0 {
		t.Fatalf("exit code %d: %s", code, stdout.String())
	}
	for _, name := range []string{"watchlist.json", "sessions.json", "config.json", "hardening_state.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("export --dir missing %s: %v", name, err)
		}
	}
	if !strings.Contains(stdout.String(), "exported state to "+dir) {
		t.Fatalf("unexpected output: %s", stdout.String())
	}

	stdout.Reset()
	if code := app.Run(context.Background(), []string{"state", "bogus"}); code == 0 {
		t.Fatal("unknown subcommand must fail")
	}
	stdout.Reset()
	if code := app.Run(context.Background(), []string{"state", "export", "--help"}); code != 0 || !strings.Contains(stdout.String(), "usage: vigilante state export") {
		t.Fatalf("help: code=%d out=%s", code, stdout.String())
	}
}
