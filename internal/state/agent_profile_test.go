package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeServiceConfig(t *testing.T, body string) *Store {
	t.Helper()
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.WriteFile(store.serviceConfigPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestServiceConfigParsesAgentProfiles(t *testing.T) {
	store := writeServiceConfig(t, `{
		"agent_profiles": {
			"client-a": {
				"claude": {
					"command": "/opt/claude-v2/bin/claude",
					"args": ["--settings", "/srv/client-a/claude-settings.json"],
					"env": {"CLAUDE_CONFIG_DIR": "/srv/client-a/.claude"}
				},
				"codex": {"env": {"CODEX_HOME": "/srv/client-a/.codex"}}
			}
		}
	}`)
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if errs := config.AgentProfileErrors(); len(errs) != 0 {
		t.Fatalf("unexpected profile errors: %v", errs)
	}
	got, err := config.AgentInvocation("client-a", "claude")
	if err != nil {
		t.Fatal(err)
	}
	want := AgentInvocation{
		Command: "/opt/claude-v2/bin/claude",
		Args:    []string{"--settings", "/srv/client-a/claude-settings.json"},
		Env:     map[string]string{"CLAUDE_CONFIG_DIR": "/srv/client-a/.claude"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("claude invocation = %#v, want %#v", got, want)
	}
	// A provider the profile does not mention is left on its default invocation.
	gemini, err := config.AgentInvocation("client-a", "gemini")
	if err != nil || !gemini.IsZero() {
		t.Fatalf("gemini invocation = %#v, err %v; want zero value", gemini, err)
	}
}

func TestServiceConfigWithoutAgentProfilesLoadsUnchanged(t *testing.T) {
	store := writeServiceConfig(t, `{"sandbox_image": "img"}`)
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.AgentProfiles != nil || len(config.AgentProfileErrors()) != 0 {
		t.Fatalf("expected no agent profiles, got %#v", config.AgentProfiles)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "agent_profiles") {
		t.Fatalf("agent_profiles should be omitted when unset: %s", data)
	}
}

func TestServiceConfigUndefinedAgentProfileNamesIt(t *testing.T) {
	_, err := ServiceConfig{}.AgentInvocation("ghost", "claude")
	if err == nil || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("expected error naming the missing profile, got %v", err)
	}
}

// One malformed profile must be reported without failing the config load or
// hiding the valid profiles next to it.
func TestServiceConfigReportsInvalidAgentProfilesWithoutFailingLoad(t *testing.T) {
	store := writeServiceConfig(t, `{
		"sandbox_image": "img",
		"agent_profiles": {
			"good": {"codex": {"env": {"CODEX_HOME": "/srv/good"}}},
			"unknown-provider": {"cursor": {"command": "cursor"}},
			"wrong-type": {"claude": {"args": "--not-a-list"}},
			"typo-field": {"claude": {"comand": "claude"}},
			"bad-env": {"claude": {"env": {"A=B": "x"}}},
			"": {"claude": {"command": "claude"}}
		}
	}`)
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatalf("a malformed profile must not fail the whole config: %v", err)
	}
	if config.SandboxImage != "img" {
		t.Fatalf("unrelated settings lost: %#v", config)
	}
	if _, err := config.AgentInvocation("good", "codex"); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
	for name, wantFragment := range map[string]string{
		"unknown-provider": `unknown provider "cursor"`,
		"wrong-type":       "malformed profile",
		"typo-field":       "malformed profile",
		"bad-env":          `invalid env variable name "A=B"`,
		"":                 "is not defined",
	} {
		_, err := config.AgentProfile(name)
		if err == nil || !strings.Contains(err.Error(), wantFragment) {
			t.Errorf("profile %q: error = %v, want it to contain %q", name, err, wantFragment)
		}
	}
	errs := config.AgentProfileErrors()
	if len(errs) != 5 {
		t.Fatalf("expected 5 profile errors, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0].Error(), "names must be non-empty") {
		t.Fatalf("expected the empty profile name to be reported first, got %v", errs[0])
	}
}

// Saving the config must not rewrite a profile Vigilante could not decode.
func TestServiceConfigSavePreservesUndecodableAgentProfile(t *testing.T) {
	store := writeServiceConfig(t, `{"agent_profiles": {"broken": {"claude": {"args": "oops"}}, "ok": {"claude": {"command": "c"}}}}`)
	config, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServiceConfig(config); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.LoadServiceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.AgentProfile("broken"); err == nil || !strings.Contains(err.Error(), "malformed profile") {
		t.Fatalf("broken profile should survive a save still broken, got %v", err)
	}
	data, err := os.ReadFile(store.serviceConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"oops"`) {
		t.Fatalf("operator's original profile content was dropped: %s", data)
	}
	if got, err := reloaded.AgentInvocation("ok", "claude"); err != nil || got.Command != "c" {
		t.Fatalf("valid profile did not round-trip: %#v, %v", got, err)
	}
}

func TestWatchTargetAgentProfileRoundTrip(t *testing.T) {
	t.Setenv("VIGILANTE_HOME", filepath.Join(t.TempDir(), ".vigilante"))
	store := NewStore()
	t.Cleanup(func() { _ = store.Close() })
	targets := []WatchTarget{
		{Path: "/a", Repo: "o/a", Provider: "claude", AgentProfile: "client-a"},
		{Path: "/b", Repo: "o/b", Provider: "claude"},
	}
	if err := store.SaveWatchTargets(targets); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].AgentProfile != "client-a" || got[1].AgentProfile != "" {
		t.Fatalf("agent profile did not round-trip: %#v", got)
	}
	body, err := json.Marshal(got[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "agent_profile") {
		t.Fatalf("agent_profile should be omitted for targets without one: %s", body)
	}
}

// Rows written before the field existed must load with no profile.
func TestWatchTargetWithoutAgentProfileFieldLoads(t *testing.T) {
	var target WatchTarget
	if err := json.Unmarshal([]byte(`{"path":"/a","repo":"o/a","branch":"main","max_parallel_sessions":0}`), &target); err != nil {
		t.Fatal(err)
	}
	if target.AgentProfile != "" {
		t.Fatalf("unexpected agent profile %q", target.AgentProfile)
	}
}
