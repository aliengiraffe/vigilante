package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	githubbackend "github.com/nicobistolfi/vigilante/internal/backend/github"
	"github.com/nicobistolfi/vigilante/internal/environment"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/provider"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type profileCall struct {
	method string
	dir    string
	name   string
	args   []string
	env    map[string]string
}

// profileRunner accepts every command and records how it was run, so tests can
// assert which binary, args, and env reached the runner.
type profileRunner struct {
	calls []profileCall
}

func (r *profileRunner) record(method string, dir string, env map[string]string, name string, args []string) string {
	r.calls = append(r.calls, profileCall{method: method, dir: dir, name: name, args: args, env: env})
	switch {
	case len(args) == 1 && args[0] == "--version":
		if strings.Contains(name, "codex") {
			return "codex 0.114.0"
		}
		return "2.1.3 (Claude Code)"
	case name == "git" && len(args) > 0 && args[0] == "rev-list":
		return "1\n"
	}
	return "ok"
}

func (r *profileRunner) Run(_ context.Context, dir string, name string, args ...string) (string, error) {
	return r.record("Run", dir, nil, name, args), nil
}

func (r *profileRunner) RunStreaming(_ context.Context, dir string, _ io.Writer, name string, args ...string) (string, error) {
	return r.record("RunStreaming", dir, nil, name, args), nil
}

func (r *profileRunner) RunStreamingWithEnv(_ context.Context, dir string, env map[string]string, _ io.Writer, name string, args ...string) (string, error) {
	return r.record("RunStreamingWithEnv", dir, env, name, args), nil
}

func (r *profileRunner) LookPath(file string) (string, error) { return "/usr/bin/" + file, nil }

// agentCalls returns the calls that launched a coding agent (directly or via
// docker exec), skipping version probes, git, and gh.
func (r *profileRunner) agentCalls() []profileCall {
	var out []profileCall
	for _, call := range r.calls {
		if call.name == "git" || call.name == "gh" || (len(call.args) == 1 && call.args[0] == "--version") {
			continue
		}
		out = append(out, call)
	}
	return out
}

const profileSecret = "s3cr3t-token-value"

type profileFixture struct {
	store     *state.Store
	env       *environment.Environment
	runner    *profileRunner
	target    state.WatchTarget
	issue     ghcli.Issue
	session   state.Session
	claudeDir string
	claudeEnv map[string]string
	codexEnv  map[string]string
}

func newProfileFixture(t *testing.T) *profileFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("VIGILANTE_HOME", filepath.Join(home, ".vigilante"))
	store := state.NewStore()
	if err := store.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f := &profileFixture{
		store:     store,
		runner:    &profileRunner{},
		claudeDir: filepath.Join(home, "client-a", ".claude"),
		target:    state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo", AgentProfile: "client-a"},
		issue:     ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"},
		session: state.Session{
			RepoPath:          "/tmp/repo",
			Repo:              "owner/repo",
			IssueNumber:       7,
			WorktreePath:      "/tmp/worktree",
			Provider:          "claude",
			Branch:            "vigilante/issue-7",
			Status:            state.SessionStatusRunning,
			PullRequestNumber: 42,
		},
	}
	f.claudeEnv = map[string]string{"CLAUDE_CONFIG_DIR": f.claudeDir, "CLIENT_TOKEN": profileSecret}
	f.codexEnv = map[string]string{"CODEX_HOME": filepath.Join(home, "client-a", ".codex")}
	if err := store.SaveServiceConfig(state.ServiceConfig{AgentProfiles: map[string]state.AgentProfile{
		"client-a": {Providers: map[string]state.AgentInvocation{
			"claude": {Command: "/opt/claude-v2/bin/claude", Args: []string{"--settings", "/srv/client-a/settings.json"}, Env: f.claudeEnv},
			"codex":  {Env: f.codexEnv},
		}},
	}}); err != nil {
		t.Fatal(err)
	}
	f.env = &environment.Environment{OS: "darwin", Runner: f.runner}
	return f
}

func (f *profileFixture) sessionLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.store.SessionLogPath("owner/repo", 7))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertProfiledClaudeCall(t *testing.T, f *profileFixture, call profileCall) {
	t.Helper()
	if call.method != "RunStreamingWithEnv" || call.name != "/opt/claude-v2/bin/claude" {
		t.Fatalf("expected profile command through the env-aware runner, got %s %s", call.method, call.name)
	}
	if !reflect.DeepEqual(call.env, f.claudeEnv) {
		t.Fatalf("env = %v, want %v", call.env, f.claudeEnv)
	}
	n := len(call.args)
	if n < 3 || call.args[n-3] != "--settings" || call.args[n-2] != "/srv/client-a/settings.json" {
		t.Fatalf("profile args should sit directly before the prompt: %v", call.args[:n-1])
	}
	if call.args[0] != "--print" {
		t.Fatalf("Vigilante's own flags should stay first: %v", call.args[:n-1])
	}
}

func TestRunIssueSessionAppliesAgentProfile(t *testing.T) {
	f := newProfileFixture(t)
	got := RunIssueSession(context.Background(), f.env, f.store, githubbackend.NewBackend(&f.env.Runner), f.target, f.issue, f.session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
	if got.AgentProfile != "client-a" {
		t.Fatalf("session should record its agent profile, got %q", got.AgentProfile)
	}
	agents := f.runner.agentCalls()
	if len(agents) != 2 {
		t.Fatalf("expected preflight and implementation agent calls, got %#v", agents)
	}
	for _, call := range agents {
		assertProfiledClaudeCall(t, f, call)
	}
	// The version probe targets the profile's binary, not the default one.
	probed := false
	for _, call := range f.runner.calls {
		if len(call.args) == 1 && call.args[0] == "--version" {
			probed = true
			if call.name != "/opt/claude-v2/bin/claude" {
				t.Fatalf("version probe ran %q", call.name)
			}
		}
	}
	if !probed {
		t.Fatal("expected a version probe")
	}
	if _, err := os.Stat(filepath.Join(f.claudeDir, "skills", skill.VigilanteIssueImplementation, "SKILL.md")); err != nil {
		t.Fatalf("skills must be installed under the profile config dir before the session starts: %v", err)
	}

	log := f.sessionLog(t)
	for _, want := range []string{"agent_profile=client-a", "cmd=/opt/claude-v2/bin/claude", "env=CLAUDE_CONFIG_DIR,CLIENT_TOKEN"} {
		if !strings.Contains(log, want) {
			t.Errorf("session log missing %q:\n%s", want, log)
		}
	}
	for _, leaked := range []string{profileSecret, f.claudeDir} {
		if strings.Contains(log, leaked) {
			t.Errorf("env value %q leaked into the session log", leaked)
		}
	}
}

// A target without a profile must reach the runner exactly as before: default
// binary, provider-built args, and no env overrides.
func TestRunIssueSessionWithoutAgentProfileIsUnchanged(t *testing.T) {
	f := newProfileFixture(t)
	f.target.AgentProfile = ""
	got := RunIssueSession(context.Background(), f.env, f.store, githubbackend.NewBackend(&f.env.Runner), f.target, f.issue, f.session)
	if got.Status != state.SessionStatusSuccess || got.AgentProfile != "" {
		t.Fatalf("unexpected session: %#v", got)
	}
	base, _ := provider.Resolve("claude")
	task := provider.IssueTask{Target: f.target, Issue: f.issue, Session: got}
	task.Session.Status = state.SessionStatusRunning
	agents := f.runner.agentCalls()
	if len(agents) != 2 {
		t.Fatalf("expected two agent calls, got %#v", agents)
	}
	wantIssue, _ := base.BuildIssueInvocation(task)
	issueCall := agents[1]
	if issueCall.method != "RunStreaming" || issueCall.env != nil {
		t.Fatalf("no-profile call must use the plain streaming path without env, got %s env=%v", issueCall.method, issueCall.env)
	}
	if issueCall.name != "claude" || issueCall.dir != wantIssue.Dir || len(issueCall.args) != len(wantIssue.Args) {
		t.Fatalf("no-profile invocation changed: %s %v (dir %s)", issueCall.name, issueCall.args[:len(issueCall.args)-1], issueCall.dir)
	}
	for i, arg := range wantIssue.Args[:len(wantIssue.Args)-1] {
		if issueCall.args[i] != arg {
			t.Fatalf("arg[%d] = %q, want %q", i, issueCall.args[i], arg)
		}
	}
	log := f.sessionLog(t)
	if strings.Contains(log, "agent_profile") || strings.Contains(log, "\nenv=") {
		t.Fatalf("no-profile session log should not mention profiles or env:\n%s", log)
	}
}

// In sandbox mode the invocation keeps its docker exec form and carries no
// env: the profile env reaches the container through ExtraEnv at provision.
func TestRunIssueSessionSandboxKeepsDockerExecWithAgentProfile(t *testing.T) {
	f := newProfileFixture(t)
	f.session.SandboxMode = true
	f.session.SandboxContainerName = "vigilante-sandbox-sbx_test"
	got := RunIssueSession(context.Background(), f.env, f.store, githubbackend.NewBackend(&f.env.Runner), f.target, f.issue, f.session)
	if got.Status != state.SessionStatusSuccess {
		t.Fatalf("unexpected status: %#v", got)
	}
	agents := f.runner.agentCalls()
	if len(agents) != 2 {
		t.Fatalf("expected two agent calls, got %#v", agents)
	}
	for _, call := range agents {
		wantPrefix := []string{"exec", "-w", "/workspace", "vigilante-sandbox-sbx_test", "/opt/claude-v2/bin/claude", "--print"}
		if call.name != "docker" || !reflect.DeepEqual(call.args[:len(wantPrefix)], wantPrefix) {
			t.Fatalf("expected docker exec of the profile command, got %s %v", call.name, call.args[:len(call.args)-1])
		}
		if call.env != nil || call.method != "RunStreaming" {
			t.Fatalf("sandbox invocation must not carry env on the host docker client: %s %v", call.method, call.env)
		}
		if indexOfArg(call.args, "--settings") < 0 {
			t.Fatalf("profile args missing in sandbox invocation: %v", call.args[:len(call.args)-1])
		}
	}
	if log := f.sessionLog(t); strings.Contains(log, profileSecret) {
		t.Fatal("env value leaked into the sandbox session log")
	}
}

func indexOfArg(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func TestMaintenanceSessionsApplyAgentProfile(t *testing.T) {
	pr := ghcli.PullRequest{Number: 42, URL: "https://github.com/owner/repo/pull/42"}
	runs := map[string]func(f *profileFixture) error{
		"conflict resolution": func(f *profileFixture) error {
			return RunConflictResolutionSession(context.Background(), f.env, f.store, githubbackend.NewBackend(&f.env.Runner), f.target, f.session, pr)
		},
		"ci remediation": func(f *profileFixture) error {
			return RunCIRemediationSession(context.Background(), f.env, f.store, githubbackend.NewBackend(&f.env.Runner), f.target, f.session, pr, nil)
		},
	}
	for name, run := range runs {
		t.Run(name, func(t *testing.T) {
			f := newProfileFixture(t)
			if err := run(f); err != nil {
				t.Fatal(err)
			}
			agents := f.runner.agentCalls()
			if len(agents) != 1 {
				t.Fatalf("expected one agent call, got %#v", agents)
			}
			assertProfiledClaudeCall(t, f, agents[0])
			if log := f.sessionLog(t); !strings.Contains(log, "agent_profile=client-a") || strings.Contains(log, profileSecret) {
				t.Fatalf("log should name the profile and never show env values:\n%s", log)
			}
		})
		t.Run(name+" codex env-only profile", func(t *testing.T) {
			f := newProfileFixture(t)
			f.session.Provider = "codex"
			if err := run(f); err != nil {
				t.Fatal(err)
			}
			agents := f.runner.agentCalls()
			if len(agents) != 1 || agents[0].name != "codex" || !reflect.DeepEqual(agents[0].env, f.codexEnv) {
				t.Fatalf("expected default codex binary with profile env, got %#v", agents)
			}
		})
		t.Run(name+" removed profile", func(t *testing.T) {
			f := newProfileFixture(t)
			f.target.AgentProfile = "removed"
			err := run(f)
			if err == nil || !strings.Contains(err.Error(), `agent profile "removed" is not defined`) {
				t.Fatalf("expected error naming the removed profile, got %v", err)
			}
			if agents := f.runner.agentCalls(); len(agents) != 0 {
				t.Fatalf("agent must not run under the default configuration: %#v", agents)
			}
		})
	}
}

// A target whose profile was removed from config.json must not panic and must
// not run the agent under the default account: the session is blocked.
func TestRunIssueSessionBlocksWhenAgentProfileWasRemoved(t *testing.T) {
	f := newProfileFixture(t)
	f.target.AgentProfile = "removed"
	got := RunIssueSession(context.Background(), f.env, f.store, githubbackend.NewBackend(&f.env.Runner), f.target, f.issue, f.session)
	if got.Status != state.SessionStatusBlocked || got.BlockedStage != "agent_profile" || got.BlockedReason.Kind != "agent_profile_unavailable" {
		t.Fatalf("expected session blocked on the agent profile, got %#v", got)
	}
	if !strings.Contains(got.LastError, `agent profile "removed" is not defined`) || !got.ResumeRequired || got.ResumeHint == "" {
		t.Fatalf("blocked session should explain itself and be resumable: %#v", got)
	}
	if agents := f.runner.agentCalls(); len(agents) != 0 {
		t.Fatalf("agent must not run under the default configuration: %#v", agents)
	}
	commented := false
	for _, call := range f.runner.calls {
		if call.name == "gh" && indexOfArg(call.args, "comment") >= 0 {
			commented = true
			body := call.args[len(call.args)-1]
			if !strings.Contains(body, "Blocked") || !strings.Contains(body, "`removed`") {
				t.Fatalf("unexpected blocked comment: %s", body)
			}
		}
	}
	if !commented {
		t.Fatal("expected a blocked comment on the issue")
	}
	if log := f.sessionLog(t); !strings.Contains(log, "agent profile unavailable") {
		t.Fatalf("session log should record the blocked reason:\n%s", log)
	}
	daemonLog, err := os.ReadFile(f.store.DaemonLogPath())
	if err != nil || !strings.Contains(string(daemonLog), "warning: owner/repo#7 blocked") {
		t.Fatalf("expected a daemon log warning, got %q (%v)", daemonLog, err)
	}
}

func TestFormatInvocationDebugShowsEnvNamesOnly(t *testing.T) {
	plain := formatInvocationDebug(provider.Invocation{Dir: "/w", Name: "claude", Args: []string{"--print"}})
	if want := "dir=/w\ncmd=claude\narg[0]=--print"; plain != want {
		t.Fatalf("no-env debug output changed: %q, want %q", plain, want)
	}
	got := formatInvocationDebug(provider.Invocation{Dir: "/w", Name: "claude", Args: []string{"--print"}, Env: map[string]string{"B_KEY": "b-secret", "A_KEY": "a-secret"}})
	if want := "dir=/w\ncmd=claude\narg[0]=--print\nenv=A_KEY,B_KEY"; got != want {
		t.Fatalf("debug output = %q, want %q", got, want)
	}
}

func TestAutoAgentProfileLaunches(t *testing.T) {
	for _, stage := range []string{"issue", "conflict", "ci", "sandbox", "invalid"} {
		t.Run(stage, func(t *testing.T) {
			f := newProfileFixture(t)
			cfg, err := f.store.LoadServiceConfig()
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.AgentProfiles["client-a"]
			p.Match = "^owner/"
			if stage == "invalid" {
				p.Providers["unknown"] = state.AgentInvocation{}
			}
			cfg.AgentProfiles["client-a"] = p
			if err := f.store.SaveServiceConfig(cfg); err != nil {
				t.Fatal(err)
			}
			f.target.AgentProfile = ""
			tracker := githubbackend.NewBackend(&f.env.Runner)
			switch stage {
			case "conflict":
				err = RunConflictResolutionSession(context.Background(), f.env, f.store, tracker, f.target, f.session, ghcli.PullRequest{Number: 42})
			case "ci":
				err = RunCIRemediationSession(context.Background(), f.env, f.store, tracker, f.target, f.session, ghcli.PullRequest{Number: 42}, nil)
			default:
				if stage == "sandbox" {
					f.session.SandboxMode = true
					f.session.SandboxContainerName = "sandbox-test"
				}
				got := RunIssueSession(context.Background(), f.env, f.store, tracker, f.target, f.issue, f.session)
				if got.AgentProfile != "client-a" || !got.AgentProfileAuto {
					t.Fatalf("selection not recorded: %#v", got)
				}
				if stage == "invalid" {
					if got.Status != state.SessionStatusBlocked || got.BlockedReason.Kind != "agent_profile_unavailable" || len(f.runner.agentCalls()) != 0 {
						t.Fatalf("did not block: %#v", got)
					}
					if !strings.Contains(got.BlockedReason.Operation, "client-a (auto)") {
						t.Fatal(got.BlockedReason.Operation)
					}
					return
				}
				if got.Status != state.SessionStatusSuccess {
					t.Fatalf("session failed: %#v", got)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := f.runner.agentCalls()
			if len(calls) == 0 {
				t.Fatal("no agent calls")
			}
			for _, call := range calls {
				if stage == "sandbox" {
					if call.name != "docker" || indexOfArg(call.args, "/opt/claude-v2/bin/claude") < 0 || call.env != nil {
						t.Fatalf("sandbox call: %#v", call)
					}
				} else {
					assertProfiledClaudeCall(t, f, call)
				}
			}
			log := f.sessionLog(t)
			if !strings.Contains(log, "agent_profile=client-a agent_profile_source=auto") || strings.Contains(log, profileSecret) {
				t.Fatal("missing selection or leaked env")
			}
		})
	}
}
