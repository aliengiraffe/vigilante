package provider

import (
	"strings"
	"testing"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestResolveDefaultsToCodex(t *testing.T) {
	selectedProvider, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if selectedProvider.ID() != DefaultID {
		t.Fatalf("unexpected provider id: %s", selectedProvider.ID())
	}
}

func TestRequiredToolsetIncludesSharedAndProviderTools(t *testing.T) {
	selectedProvider, err := Resolve(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	got := RequiredToolset(selectedProvider)
	want := []string{"codex", "gh", "git"}
	if len(got) != len(want) {
		t.Fatalf("unexpected tool count: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected toolset: %#v", got)
		}
	}
}

func TestResolveClaudeProvider(t *testing.T) {
	selectedProvider, err := Resolve(ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	if selectedProvider.DisplayName() != "Claude Code" {
		t.Fatalf("unexpected provider: %#v", selectedProvider)
	}
	got := RequiredToolset(selectedProvider)
	want := []string{"claude", "gh", "git"}
	if len(got) != len(want) {
		t.Fatalf("unexpected tool count: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected toolset: %#v", got)
		}
	}
}

func TestResolveGeminiProvider(t *testing.T) {
	selectedProvider, err := Resolve(GeminiID)
	if err != nil {
		t.Fatal(err)
	}
	if selectedProvider.DisplayName() != "Gemini CLI" {
		t.Fatalf("unexpected provider: %#v", selectedProvider)
	}
	got := RequiredToolset(selectedProvider)
	want := []string{"gemini", "gh", "git"}
	if len(got) != len(want) {
		t.Fatalf("unexpected tool count: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected toolset: %#v", got)
		}
	}
}

func TestClaudeInvocationUsesWorktreeDirForHeadlessRuns(t *testing.T) {
	selectedProvider, err := Resolve(ClaudeID)
	if err != nil {
		t.Fatal(err)
	}

	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: ClaudeID}
	pr := ghcli.PullRequest{Number: 11, URL: "https://github.com/owner/repo/pull/11"}

	preflight, err := selectedProvider.BuildIssuePreflightInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Dir != "/tmp/worktree" {
		t.Fatalf("expected preflight dir to be worktree, got %#v", preflight)
	}
	wantPreflightArgs := []string{"--print", "--permission-mode", "acceptEdits", skill.BuildIssuePreflightPrompt(target, issue, session)}
	assertInvocationArgs(t, preflight.Args, wantPreflightArgs)

	issueInvocation, err := selectedProvider.BuildIssueInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if issueInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected issue dir to be worktree, got %#v", issueInvocation)
	}
	wantIssueArgs := []string{"--print", "--permission-mode", "acceptEdits", skill.BuildIssuePromptForRuntime(skill.RuntimeClaude, target, issue, session)}
	assertInvocationArgs(t, issueInvocation.Args, wantIssueArgs)

	conflictInvocation, err := selectedProvider.BuildConflictResolutionInvocation(ConflictTask{Target: target, Session: session, PR: pr})
	if err != nil {
		t.Fatal(err)
	}
	if conflictInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected conflict dir to be worktree, got %#v", conflictInvocation)
	}
	wantConflictArgs := []string{"--print", "--permission-mode", "acceptEdits", skill.BuildConflictResolutionPromptForRuntime(skill.RuntimeClaude, target, session, pr)}
	assertInvocationArgs(t, conflictInvocation.Args, wantConflictArgs)
}

func TestResolveIssueLabelUsesRegisteredProviderIDs(t *testing.T) {
	original := registry
	registry = map[string]Provider{
		DefaultID: codexProvider{},
		"cursor":  testProvider{id: "cursor"},
	}
	t.Cleanup(func() {
		registry = original
	})

	selected, err := ResolveIssueLabel([]ghcli.Label{{Name: "cursor"}})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "cursor" {
		t.Fatalf("unexpected provider label match: %q", selected)
	}
}

func TestResolveIssueLabelRejectsConflictingProviderLabels(t *testing.T) {
	original := registry
	registry = map[string]Provider{
		DefaultID: codexProvider{},
		"cursor":  testProvider{id: "cursor"},
	}
	t.Cleanup(func() {
		registry = original
	})

	_, err := ResolveIssueLabel([]ghcli.Label{{Name: DefaultID}, {Name: "cursor"}})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := err.Error(); got != "multiple provider labels: codex, cursor" {
		t.Fatalf("unexpected conflict error: %s", got)
	}
}

func TestValidateVersionOutputAcceptsSupportedVersions(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		output   string
	}{
		{name: "codex", provider: DefaultID, output: "codex 1.2.3"},
		{name: "claude", provider: ClaudeID, output: "Claude Code v1.4.0"},
		{name: "gemini", provider: GeminiID, output: "gemini-cli 1.9.9"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selectedProvider, err := Resolve(tc.provider)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateVersionOutput(selectedProvider, tc.output); err != nil {
				t.Fatalf("expected version to be accepted, got %v", err)
			}
		})
	}
}

func TestValidateVersionOutputRejectsTooOldVersion(t *testing.T) {
	selectedProvider, err := Resolve(DefaultID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "codex 0.9.9")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	for _, want := range []string{"codex CLI version 0.9.9 is incompatible", ">=1.0.0, <2.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}
}

func TestValidateVersionOutputRejectsTooNewVersion(t *testing.T) {
	selectedProvider, err := Resolve(ClaudeID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "Claude Code 2.0.0")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	if !strings.Contains(err.Error(), "supported: >=1.0.0, <2.0.0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVersionOutputRejectsMalformedVersionOutput(t *testing.T) {
	selectedProvider, err := Resolve(GeminiID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "gemini version unknown")
	if err == nil {
		t.Fatal("expected parse error")
	}
	for _, want := range []string{"could not parse gemini CLI version", "supported: >=1.0.0, <2.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}
}

func assertInvocationArgs(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("unexpected arg count: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected args: got %#v want %#v", got, want)
		}
	}
}

type testProvider struct {
	id string
}

func (p testProvider) ID() string {
	return p.id
}

func (p testProvider) DisplayName() string {
	return p.id
}

func (p testProvider) RequiredTools() []string {
	return nil
}

func (p testProvider) EnsureRuntimeInstalled(store *state.Store) error {
	return nil
}

func (p testProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return Invocation{}, nil
}
