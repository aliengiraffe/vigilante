package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

var profileTestTarget = state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo", Branch: "main"}
var profileTestIssue = ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}
var profileTestSession = state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Model: "opus"}

// allInvocations builds every invocation type a provider supports.
func allInvocations(t *testing.T, p Provider) map[string]Invocation {
	t.Helper()
	issueTask := IssueTask{Target: profileTestTarget, Issue: profileTestIssue, Session: profileTestSession}
	builders := map[string]func() (Invocation, error){
		"preflight": func() (Invocation, error) { return p.BuildIssuePreflightInvocation(issueTask) },
		"issue":     func() (Invocation, error) { return p.BuildIssueInvocation(issueTask) },
		"conflict": func() (Invocation, error) {
			return p.BuildConflictResolutionInvocation(ConflictTask{Target: profileTestTarget, Session: profileTestSession, PR: ghcli.PullRequest{Number: 9}})
		},
		"ci": func() (Invocation, error) {
			return p.BuildCIRemediationInvocation(CIRemediationTask{Target: profileTestTarget, Session: profileTestSession, PR: ghcli.PullRequest{Number: 9}})
		},
		"issue-create": func() (Invocation, error) {
			return p.BuildIssueCreateInvocation(IssueCreateTask{Target: profileTestTarget, Prompt: "make an issue"})
		},
		"package-remediation": func() (Invocation, error) {
			return p.BuildPackageRemediationInvocation(PackageRemediationTask{Target: profileTestTarget, PRNumber: 9, PRBranch: "main", FindingsCount: 2})
		},
	}
	out := map[string]Invocation{}
	for name, build := range builders {
		inv, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out[name] = inv
	}
	return out
}

func TestAgentProfileProviderIDsMatchRegistry(t *testing.T) {
	if got, want := state.AgentProfileProviderIDs(), RegisteredIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("state agent profile providers %v drifted from the provider registry %v", got, want)
	}
}

// Targets without a profile must get the very same provider and invocations
// as before agent profiles existed.
func TestEmptyProfileYieldsIdenticalInvocations(t *testing.T) {
	for _, id := range RegisteredIDs() {
		base, err := Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		wrapped := WithProfile(base, state.AgentInvocation{})
		if wrapped != base {
			t.Fatalf("%s: zero profile should return the provider itself", id)
		}
		want := allInvocations(t, base)
		for name, inv := range allInvocations(t, wrapped) {
			if !reflect.DeepEqual(inv, want[name]) {
				t.Errorf("%s %s: invocation changed: %#v != %#v", id, name, inv, want[name])
			}
			if inv.Env != nil {
				t.Errorf("%s %s: expected nil Env without a profile", id, name)
			}
			if applied := ApplyProfile(wrapped, inv); !reflect.DeepEqual(applied, inv) {
				t.Errorf("%s %s: ApplyProfile changed an unprofiled invocation", id, name)
			}
		}
	}
}

func TestForTargetWithoutProfileDoesNotReadConfig(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), "missing-root"))
	base, _ := Resolve(ClaudeID)
	got, err := ForTarget(state.NewStore(), profileTestTarget, base)
	if err != nil || got != base {
		t.Fatalf("ForTarget = %v, %v; want the base provider untouched", got, err)
	}
}

func TestProfileOverridesCommandArgsAndEnvForEveryInvocation(t *testing.T) {
	profile := state.AgentInvocation{
		Command: "/opt/agent/bin/agent",
		Args:    []string{"--settings", "/srv/settings.json"},
		Env:     map[string]string{"CLAUDE_CONFIG_DIR": "/srv/.claude", "EXTRA": "1"},
	}
	for _, id := range RegisteredIDs() {
		base, _ := Resolve(id)
		wrapped := WithProfile(base, profile)
		if wrapped.ID() != id || wrapped.DisplayName() != base.DisplayName() {
			t.Fatalf("%s: identity changed", id)
		}
		if tools := wrapped.RequiredTools(); len(tools) != 1 || tools[0] != profile.Command {
			t.Fatalf("%s: RequiredTools = %v, want the profile command", id, tools)
		}
		baseInvs := allInvocations(t, base)
		for name, inv := range allInvocations(t, wrapped) {
			baseInv := baseInvs[name]
			if inv.Name != profile.Command {
				t.Errorf("%s %s: Name = %q, want profile command", id, name, inv.Name)
			}
			if inv.Dir != baseInv.Dir {
				t.Errorf("%s %s: Dir changed", id, name)
			}
			if !reflect.DeepEqual(inv.Env, profile.Env) {
				t.Errorf("%s %s: Env = %v, want %v", id, name, inv.Env, profile.Env)
			}
			if len(inv.Args) != len(baseInv.Args)+2 {
				t.Fatalf("%s %s: args = %v", id, name, inv.Args)
			}
			at := indexOf(inv.Args, "--settings")
			if at < 0 || inv.Args[at+1] != "/srv/settings.json" {
				t.Fatalf("%s %s: profile args missing or split: %v", id, name, inv.Args)
			}
			// Removing the profile block must give back Vigilante's own args,
			// in their original order.
			without := append(append([]string{}, inv.Args[:at]...), inv.Args[at+2:]...)
			if !reflect.DeepEqual(without, baseInv.Args) {
				t.Errorf("%s %s: Vigilante args were removed or reordered:\n got %v\nwant %v", id, name, without, baseInv.Args)
			}
			// The profile block sits directly before the prompt.
			if id == GeminiID {
				if inv.Args[at+2] != "--prompt" {
					t.Errorf("%s %s: profile args should precede --prompt: %v", id, name, inv.Args[:at+3])
				}
			} else if at+2 != len(inv.Args)-1 {
				t.Errorf("%s %s: profile args should sit directly before the final prompt argument", id, name)
			}
		}
	}
	// The profile's maps and slices must not be aliased by built invocations.
	if profile.Env["EXTRA"] != "1" || len(profile.Args) != 2 {
		t.Fatalf("profile mutated: %#v", profile)
	}
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

func TestClaudeProfileArgsFollowVigilanteFlags(t *testing.T) {
	base, _ := Resolve(ClaudeID)
	wrapped := WithProfile(base, state.AgentInvocation{Args: []string{"--verbose"}})
	inv, err := wrapped.BuildIssueInvocation(IssueTask{Target: profileTestTarget, Issue: profileTestIssue, Session: profileTestSession})
	if err != nil {
		t.Fatal(err)
	}
	prompt := inv.Args[len(inv.Args)-1]
	want := []string{"--model", "opus", "--print", "--dangerously-skip-permissions", "--verbose", prompt}
	if inv.Name != "claude" || !reflect.DeepEqual(inv.Args, want) {
		t.Fatalf("invocation = %s %v, want claude %v", inv.Name, inv.Args, want)
	}
	if inv.Env != nil {
		t.Fatalf("args-only profile should not set Env, got %v", inv.Env)
	}
}

func TestApplyProfileOnExternallyBuiltInvocation(t *testing.T) {
	base, _ := Resolve(CodexID)
	wrapped := WithProfile(base, state.AgentInvocation{Command: "codex-nightly", Args: []string{"-c", "x=1"}, Env: map[string]string{"CODEX_HOME": "/srv/.codex"}})
	got := ApplyProfile(wrapped, Invocation{Name: "codex", Args: []string{"exec", "--cd", "/w", "prompt"}})
	want := Invocation{Name: "codex-nightly", Args: []string{"exec", "--cd", "/w", "-c", "x=1", "prompt"}, Env: map[string]string{"CODEX_HOME": "/srv/.codex"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyProfile = %#v, want %#v", got, want)
	}
}

func profileStore(t *testing.T, config state.ServiceConfig) *state.Store {
	t.Helper()
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SaveServiceConfig(config); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestForTargetInstallsSkillsIntoProfileConfigDir(t *testing.T) {
	claudeDir := filepath.Join(t.TempDir(), "client-a", ".claude")
	codexDir := filepath.Join(t.TempDir(), "client-a", ".codex")
	store := profileStore(t, state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{
		"client-a": {Providers: map[string]state.AgentInvocation{
			ClaudeID: {Env: map[string]string{"CLAUDE_CONFIG_DIR": claudeDir}},
			CodexID:  {Env: map[string]string{"CODEX_HOME": codexDir}},
		}},
	}})
	target := profileTestTarget
	target.AgentProfile = "client-a"

	for id, dir := range map[string]string{ClaudeID: claudeDir, CodexID: codexDir} {
		base, _ := Resolve(id)
		got, err := ForTarget(store, target, base)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		skillFile := filepath.Join(dir, "skills", skill.VigilanteIssueImplementation, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			t.Fatalf("%s: expected skill installed under the profile config dir: %v", id, err)
		}
		inv, err := got.BuildIssueInvocation(IssueTask{Target: target, Issue: profileTestIssue, Session: profileTestSession})
		if err != nil {
			t.Fatal(err)
		}
		if len(inv.Env) != 1 {
			t.Fatalf("%s: Env = %v", id, inv.Env)
		}

		// A second resolution must leave already-installed skills untouched, so
		// a session starting cannot pull skills out from under a running one.
		marker := filepath.Join(dir, "skills", skill.VigilanteIssueImplementation, "marker")
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ForTarget(store, target, base); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("%s: installed skill was rewritten on a later session start: %v", id, err)
		}
	}

	// A provider the profile does not configure keeps its default invocation.
	gemini, _ := Resolve(GeminiID)
	if got, err := ForTarget(store, target, gemini); err != nil || got != gemini {
		t.Fatalf("gemini: got %v, %v; want base provider", got, err)
	}
}

func TestForTargetRejectsUnusableProfiles(t *testing.T) {
	store := profileStore(t, state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{
		"relative": {Providers: map[string]state.AgentInvocation{ClaudeID: {Env: map[string]string{"CLAUDE_CONFIG_DIR": "relative/dir"}}}},
		"bad":      {Providers: map[string]state.AgentInvocation{"cursor": {Command: "cursor"}}},
	}})
	base, _ := Resolve(ClaudeID)
	for name, want := range map[string]string{
		"removed":  `agent profile "removed" is not defined`,
		"relative": "CLAUDE_CONFIG_DIR must be an absolute path",
		"bad":      `agent profile "bad" in config.json is invalid`,
	} {
		target := profileTestTarget
		target.AgentProfile = name
		got, err := ForTarget(store, target, base)
		if err == nil || got != nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, %v; want error containing %q", name, got, err, want)
		}
	}
}

func TestProfiledProviderEnsureRuntimeInstalledCoversProfileDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("CODEX_HOME", filepath.Join(home, "default-codex"))
	profileDir := filepath.Join(home, "profile-codex")
	base, _ := Resolve(CodexID)
	wrapped := WithProfile(base, state.AgentInvocation{Env: map[string]string{"CODEX_HOME": profileDir}})
	if err := wrapped.EnsureRuntimeInstalled(state.NewStore()); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(home, "default-codex"), profileDir} {
		if _, err := os.Stat(filepath.Join(dir, "skills", skill.VigilanteIssueImplementation, "SKILL.md")); err != nil {
			t.Errorf("skills missing under %s: %v", dir, err)
		}
	}
}
