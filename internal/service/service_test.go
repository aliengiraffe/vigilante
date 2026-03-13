package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestRenderLaunchdPlist(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	cfg := Config{
		Executable: "/Users/test/.local/bin/vigilante",
		PathEnv:    "/opt/homebrew/bin:/usr/bin:/bin",
		HomeDir:    "/Users/test",
	}
	plist := RenderLaunchdPlist(store, cfg)
	if !strings.Contains(plist, "<string>daemon</string>") || !strings.Contains(plist, store.LogsDir()) {
		t.Fatalf("unexpected plist: %s", plist)
	}
	if !strings.Contains(plist, cfg.PathEnv) || !strings.Contains(plist, cfg.HomeDir) {
		t.Fatalf("plist missing environment variables: %s", plist)
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", t.TempDir())
	store := state.NewStore()
	cfg := Config{
		Executable: "/home/test/.local/bin/vigilante",
		PathEnv:    "/usr/local/bin:/usr/bin:/bin",
		HomeDir:    "/home/test",
	}
	unit := RenderSystemdUnit(store, cfg)
	if !strings.Contains(unit, "ExecStart=") || !strings.Contains(unit, store.LogsDir()) {
		t.Fatalf("unexpected unit: %s", unit)
	}
	if !strings.Contains(unit, "Environment=PATH="+cfg.PathEnv) || !strings.Contains(unit, "Environment=HOME="+cfg.HomeDir) {
		t.Fatalf("unit missing environment variables: %s", unit)
	}
}

func TestBuildConfigUsesShellPath(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				`/bin/zsh -lic printf "%s" "$PATH"`: "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'git'`:   "/opt/homebrew/bin/git\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'gh'`:    "/opt/homebrew/bin/gh\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'codex'`: "/Users/test/.local/bin/codex\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" 'codex' --version`:  "codex 0.114.0\n",
			},
		},
	}

	selectedProvider, err := provider.Resolve(provider.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildConfig(context.Background(), env, selectedProvider)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PathEnv != "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" {
		t.Fatalf("unexpected PATH: %#v", cfg)
	}
	if cfg.HomeDir != "/Users/test" {
		t.Fatalf("unexpected HOME: %#v", cfg)
	}
}

func TestBuildConfigFailsWhenDaemonPathCannotResolveTools(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	t.Setenv("SHELL", "/bin/zsh")

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				`/bin/zsh -lic printf "%s" "$PATH"`:                   "/usr/bin:/bin",
				`/bin/sh -lc PATH="/usr/bin:/bin" command -v 'git'`:   "/usr/bin/git\n",
				`/bin/sh -lc PATH="/usr/bin:/bin" command -v 'gh'`:    "",
				`/bin/sh -lc PATH="/usr/bin:/bin" command -v 'codex'`: "",
			},
			Errors: map[string]error{
				`/bin/sh -lc PATH="/usr/bin:/bin" command -v 'gh'`:    errors.New("missing"),
				`/bin/sh -lc PATH="/usr/bin:/bin" command -v 'codex'`: errors.New("missing"),
			},
		},
	}

	selectedProvider, err := provider.Resolve(provider.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildConfig(context.Background(), env, selectedProvider)
	if err == nil || !strings.Contains(err.Error(), "codex, gh") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildConfigSupportsClaudeProvider(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				`/bin/zsh -lic printf "%s" "$PATH"`: "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'git'`:    "/opt/homebrew/bin/git\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'gh'`:     "/opt/homebrew/bin/gh\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'claude'`: "/Users/test/.local/bin/claude\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" 'claude' --version`:  "Claude Code 1.4.0\n",
			},
		},
	}

	selectedProvider, err := provider.Resolve(provider.ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildConfig(context.Background(), env, selectedProvider)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PathEnv != "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" {
		t.Fatalf("unexpected PATH: %#v", cfg)
	}
}

func TestBuildConfigSupportsGeminiProvider(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", "/usr/bin:/bin")

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				`/bin/zsh -lic printf "%s" "$PATH"`: "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'git'`:    "/opt/homebrew/bin/git\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'gh'`:     "/opt/homebrew/bin/gh\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'gemini'`: "/Users/test/.local/bin/gemini\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" 'gemini' --version`:  "gemini 1.7.0\n",
			},
		},
	}

	selectedProvider, err := provider.Resolve(provider.GeminiID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := BuildConfig(context.Background(), env, selectedProvider)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PathEnv != "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" {
		t.Fatalf("unexpected PATH: %#v", cfg)
	}
}

func TestBuildConfigFailsWhenProviderVersionIsIncompatible(t *testing.T) {
	t.Setenv("HOME", "/Users/test")
	t.Setenv("SHELL", "/bin/zsh")

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				`/bin/zsh -lic printf "%s" "$PATH"`: "/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'git'`:   "/opt/homebrew/bin/git\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'gh'`:    "/opt/homebrew/bin/gh\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" command -v 'codex'`: "/Users/test/.local/bin/codex\n",
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" 'codex' --version`:  "codex 2.0.0\n",
			},
		},
	}

	selectedProvider, err := provider.Resolve(provider.DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BuildConfig(context.Background(), env, selectedProvider)
	if err == nil || !strings.Contains(err.Error(), "codex CLI version 2.0.0 is incompatible") {
		t.Fatalf("unexpected error: %v", err)
	}
}
