package app

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/sandbox/container"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

const agentProfileSecret = "s3cr3t-token-value"

// newAgentProfileApp returns an app whose config.json defines the "client-a"
// profile plus one invalid profile, along with a path that can be watched.
func newAgentProfileApp(t *testing.T) (*App, string, string) {
	t.Helper()
	home := t.TempDir()
	repoPath := filepath.Join(home, "repo")
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	t.Setenv("HOME", home)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatal(err)
	}
	app := New()
	t.Cleanup(func() { _ = app.state.Close() })
	app.stdout = testutil.IODiscard{}
	app.stderr = testutil.IODiscard{}
	if err := app.state.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(home, "client-a", ".claude")
	if err := app.state.SaveServiceConfig(state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{
		"client-a": {Providers: map[string]state.AgentInvocation{
			"claude": {
				Command: "claude-client-a",
				Args:    []string{"--settings", "/srv/client-a/settings.json"},
				Env:     map[string]string{"CLAUDE_CONFIG_DIR": claudeDir, "CLIENT_TOKEN": agentProfileSecret},
			},
		}},
		"broken": {Providers: map[string]state.AgentInvocation{"cursor": {Command: "cursor"}}},
	}}); err != nil {
		t.Fatal(err)
	}
	app.env.Runner = testutil.FakeRunner{
		LookPaths: map[string]string{"git": "/usr/bin/git", "gh": "/usr/bin/gh", "claude": "/usr/bin/claude"},
		Outputs: map[string]string{
			testutil.Key("git", "rev-parse", "--is-inside-work-tree"):                  "true\n",
			testutil.Key("git", "remote", "get-url", "origin"):                         "git@github.com:owner/repo.git\n",
			testutil.Key("git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"): "origin/main\n",
		},
	}
	return app, repoPath, claudeDir
}

func loadSingleTarget(t *testing.T, app *App) state.WatchTarget {
	t.Helper()
	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected one watch target, got %#v", targets)
	}
	return targets[0]
}

func TestWatchAgentProfileFlagPersistsKnownProfile(t *testing.T) {
	app, repoPath, _ := newAgentProfileApp(t)
	ctx := context.Background()

	if err := app.runCommand(ctx, []string{"watch", "--agent-profile", "client-a", repoPath}); err != nil {
		t.Fatal(err)
	}
	if got := loadSingleTarget(t, app).AgentProfile; got != "client-a" {
		t.Fatalf("agent profile = %q, want client-a", got)
	}

	// Re-watching without the flag keeps the profile.
	if err := app.runCommand(ctx, []string{"watch", repoPath}); err != nil {
		t.Fatal(err)
	}
	if got := loadSingleTarget(t, app).AgentProfile; got != "client-a" {
		t.Fatalf("omitting --agent-profile should preserve it, got %q", got)
	}

	// `list` and `status` show the profile in use.
	var out bytes.Buffer
	app.stdout = &out
	if err := app.List(false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"agent_profile": "client-a"`) {
		t.Fatalf("list output should show the profile: %s", out.String())
	}
	row := formatWatchTargetRow(watchedRepoStatus{Target: loadSingleTarget(t, app)})
	if !strings.Contains(row, "agent profile client-a") {
		t.Fatalf("status row should show the profile: %s", row)
	}
	app.stdout = testutil.IODiscard{}

	// An explicit empty value clears it.
	if err := app.runCommand(ctx, []string{"watch", "--agent-profile", "", repoPath}); err != nil {
		t.Fatal(err)
	}
	target := loadSingleTarget(t, app)
	if target.AgentProfile != "" {
		t.Fatalf("empty --agent-profile should clear it, got %q", target.AgentProfile)
	}
	if row := formatWatchTargetRow(watchedRepoStatus{Target: target}); strings.Contains(row, "agent profile") {
		t.Fatalf("status row should not mention a profile for targets without one: %s", row)
	}
}

func TestWatchAgentProfileFlagRejectsUnusableProfiles(t *testing.T) {
	app, repoPath, _ := newAgentProfileApp(t)
	for name, want := range map[string]string{
		"ghost":  `agent profile "ghost" is not defined in config.json`,
		"broken": `agent profile "broken" in config.json is invalid`,
	} {
		err := app.runCommand(context.Background(), []string{"watch", "--agent-profile", name, repoPath})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: expected error containing %q, got %v", name, want, err)
		}
	}
	targets, err := app.state.LoadWatchTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("a rejected profile must not register the target: %#v", targets)
	}
}

func TestStartAgentProfileFlagRejectsUnknownProfile(t *testing.T) {
	app, repoPath, _ := newAgentProfileApp(t)
	err := app.runCommand(context.Background(), []string{"start", "--issue", "7", "--agent-profile", "ghost", repoPath})
	if err == nil || !strings.Contains(err.Error(), `agent profile "ghost" is not defined`) {
		t.Fatalf("expected error naming the missing profile, got %v", err)
	}
}

func TestWatchTargetForSessionCarriesAgentProfile(t *testing.T) {
	session := state.Session{Repo: "owner/repo", AgentProfile: "one-off"}
	if got := watchTargetForSession(session, state.WatchTarget{AgentProfile: "client-a"}).AgentProfile; got != "client-a" {
		t.Fatalf("watch target profile should win, got %q", got)
	}
	if got := watchTargetForSession(session, state.WatchTarget{}).AgentProfile; got != "one-off" {
		t.Fatalf("one-off session should keep its own profile, got %q", got)
	}
	if got := watchTargetForSession(state.Session{}, state.WatchTarget{}).AgentProfile; got != "" {
		t.Fatalf("expected no profile, got %q", got)
	}
}

func TestApplySandboxAgentProfileMountsConfigDirAndRewritesEnv(t *testing.T) {
	defaults := []container.Mount{
		{Source: "/home/me/.codex", Target: "/root/.codex"},
		{Source: "/home/me/.claude", Target: "/root/.claude"},
		{Source: "/home/me/.gitconfig", Target: "/root/.gitconfig", ReadOnly: true},
	}
	extraEnv := map[string]string{"SSH_AUTH_SOCK": "/ssh-agent"}
	profile := state.AgentInvocation{Env: map[string]string{"CLAUDE_CONFIG_DIR": "/srv/client-a/.claude", "CLIENT_TOKEN": "t"}}

	got := applySandboxAgentProfile("claude", profile, defaults, extraEnv)
	want := []container.Mount{
		{Source: "/home/me/.codex", Target: "/root/.codex"},
		{Source: "/home/me/.gitconfig", Target: "/root/.gitconfig", ReadOnly: true},
		{Source: "/srv/client-a/.claude", Target: "/root/.claude"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mounts = %#v, want %#v", got, want)
	}
	wantEnv := map[string]string{"SSH_AUTH_SOCK": "/ssh-agent", "CLAUDE_CONFIG_DIR": "/root/.claude", "CLIENT_TOKEN": "t"}
	if !reflect.DeepEqual(extraEnv, wantEnv) {
		t.Fatalf("extra env = %v, want %v", extraEnv, wantEnv)
	}
	if profile.Env["CLAUDE_CONFIG_DIR"] != "/srv/client-a/.claude" {
		t.Fatal("profile env must not be rewritten in place")
	}

	// Codex is mounted at its own container path.
	codexEnv := map[string]string{}
	codexMounts := applySandboxAgentProfile("codex", state.AgentInvocation{Env: map[string]string{"CODEX_HOME": "/srv/client-a/.codex"}}, defaults, codexEnv)
	if codexEnv["CODEX_HOME"] != "/root/.codex" || codexMounts[len(codexMounts)-1] != (container.Mount{Source: "/srv/client-a/.codex", Target: "/root/.codex"}) {
		t.Fatalf("codex: env=%v mounts=%#v", codexEnv, codexMounts)
	}

	// No profile, or a profile without a config dir, leaves the mounts alone.
	plainEnv := map[string]string{}
	if got := applySandboxAgentProfile("claude", state.AgentInvocation{}, defaults, plainEnv); !reflect.DeepEqual(got, defaults) || len(plainEnv) != 0 {
		t.Fatalf("no profile should change nothing: %#v %v", got, plainEnv)
	}
	envOnly := map[string]string{}
	if got := applySandboxAgentProfile("gemini", state.AgentInvocation{Env: map[string]string{"X": "1"}}, defaults, envOnly); !reflect.DeepEqual(got, defaults) || envOnly["X"] != "1" {
		t.Fatalf("env-only profile: %#v %v", got, envOnly)
	}
}

func TestInstallBundledSkillsCoversAgentProfileConfigDirs(t *testing.T) {
	app, _, claudeDir := newAgentProfileApp(t)
	var stderr bytes.Buffer
	app.stderr = &stderr
	if err := app.installBundledSkills(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{app.state.ClaudeHome(), claudeDir} {
		if _, err := os.Stat(filepath.Join(dir, "skills", skill.VigilanteIssueImplementation, "SKILL.md")); err != nil {
			t.Errorf("skills missing under %s: %v", dir, err)
		}
	}
	if !strings.Contains(stderr.String(), `agent profile "broken" in config.json is invalid`) {
		t.Fatalf("the invalid profile should be reported, got %q", stderr.String())
	}
}

// Daemon start must report broken profiles and targets that reference a
// removed one, without failing.
func TestCheckAgentProfilesReportsProblemsWithoutFailing(t *testing.T) {
	app, _, _ := newAgentProfileApp(t)
	if err := app.state.SaveWatchTargets([]state.WatchTarget{
		{Path: "/a", Repo: "owner/ok", Provider: "claude", AgentProfile: "client-a"},
		{Path: "/b", Repo: "owner/removed", Provider: "claude", AgentProfile: "removed"},
		{Path: "/c", Repo: "owner/plain", Provider: "claude"},
	}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	app.stderr = &stderr
	app.checkAgentProfiles()
	out := stderr.String()
	for _, want := range []string{
		`config error: agent profile "broken" in config.json is invalid: unknown provider "cursor"`,
		`warning: owner/removed: agent profile "removed" is not defined`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "owner/ok") || strings.Contains(out, "owner/plain") {
		t.Fatalf("healthy targets should not be reported:\n%s", out)
	}
}

type agentProfileCall struct {
	name string
	args []string
	env  map[string]string
}

// agentProfileRunner accepts every command and records agent launches.
type agentProfileRunner struct {
	calls []agentProfileCall
}

func (r *agentProfileRunner) record(env map[string]string, name string, args []string) string {
	r.calls = append(r.calls, agentProfileCall{name: name, args: args, env: env})
	if len(args) == 1 && args[0] == "--version" {
		return "2.1.3 (Claude Code)"
	}
	return "ok"
}

func (r *agentProfileRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	return r.record(nil, name, args), nil
}

func (r *agentProfileRunner) RunStreamingWithEnv(_ context.Context, _ string, env map[string]string, _ io.Writer, name string, args ...string) (string, error) {
	return r.record(env, name, args), nil
}

func (r *agentProfileRunner) LookPath(file string) (string, error) { return "/usr/bin/" + file, nil }

func (r *agentProfileRunner) agentCalls() []agentProfileCall {
	var out []agentProfileCall
	for _, call := range r.calls {
		if strings.HasPrefix(call.name, "claude") && !(len(call.args) == 1 && call.args[0] == "--version") {
			out = append(out, call)
		}
	}
	return out
}

func assertAgentProfileCall(t *testing.T, calls []agentProfileCall, claudeDir string) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("expected one agent call, got %#v", calls)
	}
	call := calls[0]
	wantEnv := map[string]string{"CLAUDE_CONFIG_DIR": claudeDir, "CLIENT_TOKEN": agentProfileSecret}
	if call.name != "claude-client-a" || !reflect.DeepEqual(call.env, wantEnv) {
		t.Fatalf("expected profile command and env, got %s %v", call.name, call.env)
	}
	n := len(call.args)
	if n < 3 || call.args[n-3] != "--settings" || call.args[n-2] != "/srv/client-a/settings.json" {
		t.Fatalf("profile args should sit directly before the prompt: %v", call.args[:n-1])
	}
}

func TestIssueCreateAppliesAgentProfile(t *testing.T) {
	app, _, claudeDir := newAgentProfileApp(t)
	runner := &agentProfileRunner{}
	app.env.Runner = runner
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Provider: "claude", AgentProfile: "client-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := app.issueCreate(context.Background(), "owner/repo", "", "add a feature"); err != nil {
		t.Fatal(err)
	}
	assertAgentProfileCall(t, runner.agentCalls(), claudeDir)

	// A removed profile fails the command instead of using the default account.
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Provider: "claude", AgentProfile: "removed"}}); err != nil {
		t.Fatal(err)
	}
	runner.calls = nil
	err := app.issueCreate(context.Background(), "owner/repo", "", "add a feature")
	if err == nil || !strings.Contains(err.Error(), `agent profile "removed" is not defined`) {
		t.Fatalf("expected error naming the removed profile, got %v", err)
	}
	if len(runner.agentCalls()) != 0 {
		t.Fatalf("agent must not run: %#v", runner.calls)
	}
}

func TestPackageRemediationAppliesAgentProfile(t *testing.T) {
	app, _, claudeDir := newAgentProfileApp(t)
	runner := &agentProfileRunner{}
	app.env.Runner = runner
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo", Provider: "claude", AgentProfile: "client-a"}
	app.dispatchPackageRemediation(context.Background(), target, backend.PullRequest{Number: 9, BaseRefName: "main"}, &state.HardeningPRState{FindingsCount: 2})
	assertAgentProfileCall(t, runner.agentCalls(), claudeDir)

	// A removed profile must not panic the scan loop or run the agent.
	runner.calls = nil
	target.AgentProfile = "removed"
	app.dispatchPackageRemediation(context.Background(), target, backend.PullRequest{Number: 9, BaseRefName: "main"}, &state.HardeningPRState{FindingsCount: 2})
	if len(runner.agentCalls()) != 0 {
		t.Fatalf("agent must not run: %#v", runner.calls)
	}
}

func TestResumeBlockedIssueExecutionAppliesAgentProfile(t *testing.T) {
	app, _, claudeDir := newAgentProfileApp(t)
	runner := &agentProfileRunner{}
	app.env.Runner = runner
	if err := app.state.SaveWatchTargets([]state.WatchTarget{{Path: "/tmp/repo", Repo: "owner/repo", Provider: "claude", AgentProfile: "client-a"}}); err != nil {
		t.Fatal(err)
	}
	session := state.Session{
		RepoPath:     "/tmp/repo",
		Repo:         "owner/repo",
		IssueNumber:  7,
		Provider:     "claude",
		Branch:       "vigilante/issue-7",
		Status:       state.SessionStatusBlocked,
		BlockedStage: "agent_profile",
	}
	if err := app.resumeBlockedIssueExecution(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
	assertAgentProfileCall(t, runner.agentCalls(), claudeDir)
}
