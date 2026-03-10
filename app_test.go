package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCreatesStateLayoutAndSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	app := NewApp()
	app.stdout = ioDiscard{}
	app.stderr = ioDiscard{}
	app.env.Runner = fakeRunner{
		lookPath: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "codex": "/usr/bin/codex"},
		outputs: map[string]string{
			"gh auth status": "ok",
		},
	}

	if err := app.Setup(context.Background(), false); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(app.state.Root(), "watchlist.json"),
		filepath.Join(app.state.Root(), "sessions.json"),
		filepath.Join(app.state.Root(), "logs"),
		filepath.Join(app.state.Root(), "worktrees"),
		filepath.Join(app.state.CodexHome(), "skills", vigilanteSkillName, "SKILL.md"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

func TestWatchListAndUnwatch(t *testing.T) {
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	var stdout bytes.Buffer
	app.stdout = &stdout
	app.stderr = ioDiscard{}
	app.env.Runner = fakeRunner{
		outputs: map[string]string{
			key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			key("git", "remote", "get-url", "origin"):                         "git@github.com:nicobistolfi/vigilante.git\n",
			key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
		},
	}

	if err := app.Watch(context.Background(), repoPath, false); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	if err := app.List(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "\"repo\": \"nicobistolfi/vigilante\"") {
		t.Fatalf("unexpected list output: %s", stdout.String())
	}

	if err := app.Unwatch(repoPath); err != nil {
		t.Fatal(err)
	}
}
