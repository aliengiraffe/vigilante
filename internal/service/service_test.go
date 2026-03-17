package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/environment"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

type recordingRunner struct {
	testutil.FakeRunner
	calls []string
}

func (r *recordingRunner) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	r.calls = append(r.calls, testutil.Key(name, args...))
	return r.FakeRunner.Run(ctx, dir, name, args...)
}

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
				`/bin/sh -lc PATH="/opt/homebrew/bin:/Users/test/.local/bin:/usr/bin:/bin" 'claude' --version`:  "Claude Code 2.1.3\n",
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

func TestServiceStatusReportsLaunchdRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vigilante.agent.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				testutil.Key("launchctl", "print", launchdTarget()): "pid = 412\nstate = running\n",
			},
		},
	}

	status, err := ServiceStatus(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Running || status.State != StatusRunning {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.Manager != "launchd" || status.Service != launchdLabel || status.FilePath != plistPath {
		t.Fatalf("unexpected service metadata: %#v", status)
	}
}

func TestServiceStatusReportsLaunchdStoppedWhenServiceIsUnloaded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vigilante.agent.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &environment.Environment{
		OS: "darwin",
		Runner: testutil.FakeRunner{
			Errors: map[string]error{
				testutil.Key("launchctl", "print", launchdTarget()): errors.New("Could not find service"),
			},
		},
	}

	status, err := ServiceStatus(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.Running || status.State != StatusStopped {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestServiceStatusReportsSystemdRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &environment.Environment{
		OS: "linux",
		Runner: testutil.FakeRunner{
			Outputs: map[string]string{
				testutil.Key("systemctl", "--user", "show", "--property=LoadState,ActiveState", systemdUnitName): "LoadState=loaded\nActiveState=active\n",
			},
		},
	}

	status, err := ServiceStatus(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Running || status.State != StatusRunning {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestServiceStatusReportsNotInstalledWhenUnitFileIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	env := &environment.Environment{
		OS:     "linux",
		Runner: testutil.FakeRunner{},
	}

	status, err := ServiceStatus(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if status.Installed || status.Running || status.State != StatusNotInstalled {
		t.Fatalf("unexpected status: %#v", status)
	}
	if status.FilePath != filepath.Join(home, ".config", "systemd", "user", "vigilante.service") {
		t.Fatalf("unexpected service file path: %#v", status)
	}
}

func TestServiceStatusReturnsErrorWhenSystemdStatusFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	unitPath := filepath.Join(home, ".config", "systemd", "user", "vigilante.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitPath, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := &environment.Environment{
		OS: "linux",
		Runner: testutil.FakeRunner{
			Errors: map[string]error{
				testutil.Key("systemctl", "--user", "show", "--property=LoadState,ActiveState", systemdUnitName): errors.New("dbus unavailable"),
			},
		},
	}

	_, err := ServiceStatus(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "query systemd user service status") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRestartUsesLaunchctlKickstart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.vigilante.agent.plist")
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plistPath, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{
		FakeRunner: testutil.FakeRunner{
			Outputs: map[string]string{
				testutil.Key("launchctl", "print", launchdTarget()):           "pid = 412\nstate = running\n",
				testutil.Key("launchctl", "kickstart", "-k", launchdTarget()): "",
			},
		},
	}

	env := &environment.Environment{OS: "darwin", Runner: runner}
	if err := Restart(context.Background(), env); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{
		testutil.Key("launchctl", "print", launchdTarget()),
		testutil.Key("launchctl", "kickstart", "-k", launchdTarget()),
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("unexpected command sequence:\n got: %#v\nwant: %#v", runner.calls, wantCalls)
	}
}

func TestRestartReturnsErrorWhenServiceIsNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	env := &environment.Environment{OS: "linux", Runner: testutil.FakeRunner{}}
	err := Restart(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), "service is not installed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRestartReturnsUnsupportedOSError(t *testing.T) {
	env := &environment.Environment{OS: "windows", Runner: testutil.FakeRunner{}}
	err := Restart(context.Background(), env)
	if err == nil || !strings.Contains(err.Error(), `unsupported OS "windows"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareMacOSDaemonBinaryUsesResolvedPath(t *testing.T) {
	dir := t.TempDir()
	resolvedPath := filepath.Join(dir, "Caskroom", "vigilante", "1.2.3", "vigilante")
	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolvedPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(resolvedPath)
	if err != nil {
		t.Fatal(err)
	}

	invokedPath := filepath.Join(dir, "bin", "vigilante")
	if err := os.MkdirAll(filepath.Dir(invokedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(resolvedPath, invokedPath); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{
		FakeRunner: testutil.FakeRunner{
			Outputs: map[string]string{
				testutil.Key("xattr", resolvedPath):                                         "com.apple.provenance\ncom.apple.quarantine\ncom.example.keep\n",
				testutil.Key("xattr", "-d", "com.apple.provenance", resolvedPath):           "",
				testutil.Key("xattr", "-d", "com.apple.quarantine", resolvedPath):           "",
				testutil.Key("codesign", "--force", "--sign", "-", resolvedPath):            "",
				testutil.Key("spctl", "--assess", "--type", "execute", "-vv", resolvedPath): "",
			},
		},
	}

	if err := prepareMacOSDaemonBinary(context.Background(), runner, invokedPath); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{
		testutil.Key("xattr", resolvedPath),
		testutil.Key("xattr", "-d", "com.apple.provenance", resolvedPath),
		testutil.Key("xattr", "-d", "com.apple.quarantine", resolvedPath),
		testutil.Key("codesign", "--force", "--sign", "-", resolvedPath),
		testutil.Key("spctl", "--assess", "--type", "execute", "-vv", resolvedPath),
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("unexpected command sequence:\n got: %#v\nwant: %#v", runner.calls, wantCalls)
	}
}

func TestPrepareMacOSDaemonBinarySkipsMissingKnownAttrs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vigilante")
	if err := os.WriteFile(path, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{
		FakeRunner: testutil.FakeRunner{
			Outputs: map[string]string{
				testutil.Key("xattr", path):                                         "com.example.keep\n",
				testutil.Key("codesign", "--force", "--sign", "-", path):            "",
				testutil.Key("spctl", "--assess", "--type", "execute", "-vv", path): "",
			},
		},
	}

	if err := prepareMacOSDaemonBinary(context.Background(), runner, path); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{
		testutil.Key("xattr", path),
		testutil.Key("codesign", "--force", "--sign", "-", path),
		testutil.Key("spctl", "--assess", "--type", "execute", "-vv", path),
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("unexpected command sequence:\n got: %#v\nwant: %#v", runner.calls, wantCalls)
	}
}

func TestPrepareMacOSDaemonBinaryReportsSymlinkContextOnSpctlFailure(t *testing.T) {
	dir := t.TempDir()
	resolvedPath := filepath.Join(dir, "Caskroom", "vigilante", "1.2.3", "vigilante")
	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolvedPath, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedPath, err := filepath.EvalSymlinks(resolvedPath)
	if err != nil {
		t.Fatal(err)
	}

	invokedPath := filepath.Join(dir, "bin", "vigilante")
	if err := os.MkdirAll(filepath.Dir(invokedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(resolvedPath, invokedPath); err != nil {
		t.Fatal(err)
	}

	runner := &recordingRunner{
		FakeRunner: testutil.FakeRunner{
			Outputs: map[string]string{
				testutil.Key("xattr", resolvedPath):                              "",
				testutil.Key("codesign", "--force", "--sign", "-", resolvedPath): "",
			},
			Errors: map[string]error{
				testutil.Key("spctl", "--assess", "--type", "execute", "-vv", resolvedPath): errors.New("exit status 3 (/opt/homebrew/bin/vigilante: rejected)"),
			},
		},
	}

	err = prepareMacOSDaemonBinary(context.Background(), runner, invokedPath)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), resolvedPath) {
		t.Fatalf("error missing resolved path: %v", err)
	}
	if !strings.Contains(err.Error(), invokedPath) {
		t.Fatalf("error missing invoked path: %v", err)
	}
	if !strings.Contains(err.Error(), "removed_xattrs=none") {
		t.Fatalf("error missing xattr context: %v", err)
	}
}
