package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These Effective* accessors are the defaulting layer between a sparse config file
// and the daemon. A wrong default silently changes where vigilante pushes or which
// issues it picks up, so each fallback is pinned explicitly.

func TestEffectiveBranchMode(t *testing.T) {
	t.Parallel()

	// Anything other than an explicit "auto" must be pinned. Defaulting an
	// unrecognized value to auto would let vigilante invent branch names on a
	// repository the operator wanted pinned.
	tests := map[BranchMode]BranchMode{
		BranchModeAuto:      BranchModeAuto,
		BranchModePinned:    BranchModePinned,
		BranchMode(""):      BranchModePinned,
		BranchMode("bogus"): BranchModePinned,
		BranchMode("AUTO"):  BranchModePinned,
	}
	for input, want := range tests {
		got := WatchTarget{BranchMode: input}.EffectiveBranchMode()
		if got != want {
			t.Errorf("BranchMode %q -> %q, want %q", input, got, want)
		}
	}
}

func TestEffectivePushRemote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target WatchTarget
		want   string
	}{
		{
			name:   "explicit remote wins",
			target: WatchTarget{PushRemote: "upstream", ForkMode: true},
			want:   "upstream",
		},
		{
			name:   "explicit remote is trimmed",
			target: WatchTarget{PushRemote: "  upstream  "},
			want:   "upstream",
		},
		{
			// Fork mode has to push to the fork remote, not origin, or the branch
			// lands on the upstream repository the operator was avoiding.
			name:   "fork mode defaults to the fork remote",
			target: WatchTarget{ForkMode: true},
			want:   "fork",
		},
		{
			name:   "blank remote in fork mode still resolves to fork",
			target: WatchTarget{PushRemote: "   ", ForkMode: true},
			want:   "fork",
		},
		{
			name:   "default is origin",
			target: WatchTarget{},
			want:   "origin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.target.EffectivePushRemote(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEffectiveProjectRef(t *testing.T) {
	t.Parallel()

	if got := (WatchTarget{ProjectRef: "team/board", Repo: "owner/repo"}).EffectiveProjectRef(); got != "team/board" {
		t.Errorf("got %q, want the explicit project ref", got)
	}
	if got := (WatchTarget{ProjectRef: "  team/board  ", Repo: "owner/repo"}).EffectiveProjectRef(); got != "team/board" {
		t.Errorf("got %q, want the trimmed project ref", got)
	}
	if got := (WatchTarget{Repo: "owner/repo"}).EffectiveProjectRef(); got != "owner/repo" {
		t.Errorf("got %q, want the repo fallback", got)
	}
	if got := (WatchTarget{ProjectRef: "   ", Repo: "owner/repo"}).EffectiveProjectRef(); got != "owner/repo" {
		t.Errorf("got %q, want the repo fallback for a blank ref", got)
	}
}

// Sandbox mode is a security posture, so an unset value must default to off
// rather than silently claiming isolation the operator never enabled.
func TestIsSandboxEnabled(t *testing.T) {
	t.Parallel()

	if (ServiceConfig{}).IsSandboxEnabled() {
		t.Error("an unset sandbox flag must default to disabled")
	}

	enabled := true
	if !(ServiceConfig{SandboxEnabled: &enabled}).IsSandboxEnabled() {
		t.Error("an explicit true must enable the sandbox")
	}

	disabled := false
	if (ServiceConfig{SandboxEnabled: &disabled}).IsSandboxEnabled() {
		t.Error("an explicit false must disable the sandbox")
	}
}

func TestIsPackageHardeningEnabled(t *testing.T) {
	t.Parallel()

	// Unlike the sandbox, hardening defaults on, so an old config keeps the
	// feature rather than losing it on upgrade.
	if !(ServiceConfig{}).IsPackageHardeningEnabled() {
		t.Error("an unset hardening flag must default to enabled")
	}

	disabled := false
	if (ServiceConfig{PackageHardeningEnabled: &disabled}).IsPackageHardeningEnabled() {
		t.Error("an explicit false must disable hardening")
	}
}

// A malformed size in the config must not disable rotation: the daemon would then
// grow its log without bound.
func TestLogRotationLimitsFallBackOnBadValues(t *testing.T) {
	t.Parallel()

	defaults := (ServiceConfig{}).LogRotationLimits()

	bad := ServiceConfig{
		LogMaxTotalSize: "not-a-size",
		LogMaxFileSize:  "also-bad",
	}
	got := bad.LogRotationLimits()
	if got.MaxTotalSize != defaults.MaxTotalSize {
		t.Errorf("MaxTotalSize = %d, want the default %d", got.MaxTotalSize, defaults.MaxTotalSize)
	}
	if got.MaxFileSize != defaults.MaxFileSize {
		t.Errorf("MaxFileSize = %d, want the default %d", got.MaxFileSize, defaults.MaxFileSize)
	}

	// Zero and negative sizes are equally unusable and must also fall back.
	zeroed := ServiceConfig{LogMaxTotalSize: "0", LogMaxFileSize: "0"}
	got = zeroed.LogRotationLimits()
	if got.MaxTotalSize != defaults.MaxTotalSize || got.MaxFileSize != defaults.MaxFileSize {
		t.Errorf("zero sizes should fall back to defaults, got %#v", got)
	}
}

func TestLogRotationLimitsHonorsValidValues(t *testing.T) {
	t.Parallel()

	backups := 3
	cfg := ServiceConfig{
		LogMaxTotalSize: "10MB",
		LogMaxFileSize:  "1MB",
		LogMaxBackups:   &backups,
	}
	got := cfg.LogRotationLimits()

	if got.MaxTotalSize <= 0 || got.MaxFileSize <= 0 {
		t.Fatalf("valid sizes were not applied: %#v", got)
	}
	if got.MaxBackups != 3 {
		t.Errorf("MaxBackups = %d, want 3", got.MaxBackups)
	}

	// Zero backups is meaningful (keep none) and must be honored, not treated as
	// unset.
	none := 0
	if got := (ServiceConfig{LogMaxBackups: &none}).LogRotationLimits(); got.MaxBackups != 0 {
		t.Errorf("MaxBackups = %d, want 0 to be honored", got.MaxBackups)
	}

	// A negative count is nonsense and must fall back.
	negative := -1
	defaults := (ServiceConfig{}).LogRotationLimits()
	if got := (ServiceConfig{LogMaxBackups: &negative}).LogRotationLimits(); got.MaxBackups != defaults.MaxBackups {
		t.Errorf("MaxBackups = %d, want the default %d", got.MaxBackups, defaults.MaxBackups)
	}
}

func TestStoreRoot(t *testing.T) {
	t.Parallel()

	store := &Store{root: "/some/root"}
	if got := store.Root(); got != "/some/root" {
		t.Fatalf("Root() = %q", got)
	}
}

// Each agent home honors its own environment override so an operator can point
// vigilante at a non-default agent installation.
func TestAgentHomeOverrides(t *testing.T) {
	// No t.Parallel(): these use t.Setenv.
	store := &Store{root: "/state/root"}

	tests := []struct {
		env    string
		value  string
		getter func() string
	}{
		{env: "CODEX_HOME", value: "/custom/codex", getter: store.CodexHome},
		{env: "CLAUDE_HOME", value: "/custom/claude", getter: store.ClaudeHome},
		{env: "GEMINI_HOME", value: "/custom/gemini", getter: store.GeminiHome},
		{env: "OPENCODE_HOME", value: "/custom/opencode", getter: store.OpenCodeHome},
	}

	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			t.Setenv(tt.env, tt.value)
			if got := tt.getter(); got != tt.value {
				t.Fatalf("%s = %q, want %q", tt.env, got, tt.value)
			}
		})
	}
}

func TestAgentHomeDefaults(t *testing.T) {
	// No t.Parallel(): t.Setenv.
	store := &Store{root: "/state/root"}

	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_HOME", "")
	t.Setenv("GEMINI_HOME", "")
	t.Setenv("OPENCODE_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}

	tests := map[string]struct {
		got  string
		want string
	}{
		"codex":    {got: store.CodexHome(), want: filepath.Join(home, ".codex")},
		"claude":   {got: store.ClaudeHome(), want: filepath.Join(home, ".claude")},
		"gemini":   {got: store.GeminiHome(), want: filepath.Join(home, ".gemini")},
		"opencode": {got: store.OpenCodeHome(), want: filepath.Join(home, ".config", "opencode")},
	}
	for name, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s home = %q, want %q", name, tt.got, tt.want)
		}
	}
}

// EnsureLayout has to be idempotent: it runs on every command, not just first
// setup, and must not clobber existing state files.
func TestEnsureLayoutIsIdempotentAndPreservesState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := &Store{root: root}

	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	targets := []WatchTarget{{Path: "/repo", Repo: "owner/repo", Branch: "main"}}
	if err := store.SaveWatchTargets(targets); err != nil {
		t.Fatal(err)
	}

	// Second call must not reset the watchlist to an empty array.
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].Repo != "owner/repo" {
		t.Fatalf("watch targets = %#v, want the saved target preserved", loaded)
	}

	if _, err := os.Stat(store.LogsDir()); err != nil {
		t.Fatalf("logs dir should exist: %v", err)
	}
}

func TestSaveAndLoadWatchTargetsRoundTrip(t *testing.T) {
	t.Parallel()

	store := &Store{root: t.TempDir()}
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}

	want := []WatchTarget{
		{Path: "/a", Repo: "owner/a", Branch: "main", Provider: "claude", ForkMode: true},
		{Path: "/b", Repo: "owner/b", Branch: "develop", BranchMode: BranchModeAuto},
	}
	if err := store.SaveWatchTargets(want); err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d targets, want 2", len(got))
	}
	if got[0].Repo != "owner/a" || !got[0].ForkMode || got[0].Provider != "claude" {
		t.Errorf("targets[0] = %#v", got[0])
	}
	if got[1].BranchMode != BranchModeAuto {
		t.Errorf("targets[1].BranchMode = %q, want auto to survive the round trip", got[1].BranchMode)
	}
}

// Saving an empty list must produce a valid empty array rather than a truncated
// or absent file, or the next load fails.
func TestSaveWatchTargetsEmptyList(t *testing.T) {
	t.Parallel()

	store := &Store{root: t.TempDir()}
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWatchTargets(nil); err != nil {
		t.Fatal(err)
	}

	got, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatalf("an empty watchlist must load cleanly: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d targets, want 0", len(got))
	}
}

func TestHardeningPRKeyUniqueness(t *testing.T) {
	t.Parallel()

	got := HardeningPRKey("owner/repo", 42)
	if !strings.Contains(got, "owner/repo") || !strings.Contains(got, "42") {
		t.Fatalf("key = %q, want it to include both the repo and the number", got)
	}
	// Distinct inputs must not collide, since this keys persisted hardening state.
	if HardeningPRKey("owner/repo", 42) == HardeningPRKey("owner/repo", 43) {
		t.Fatal("different PR numbers must produce different keys")
	}
	if HardeningPRKey("owner/a", 1) == HardeningPRKey("owner/b", 1) {
		t.Fatal("different repos must produce different keys")
	}
}
