package provider

import (
	"strings"
	"testing"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestResolveDefaultsToClaude(t *testing.T) {
	selectedProvider, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if selectedProvider.ID() != ClaudeID {
		t.Fatalf("unexpected provider id: %s", selectedProvider.ID())
	}
}

// TestDefaultIDIsNotCodexID guards the decoupling of "the provider used when
// none is specified" from "the Codex provider's identity". Re-merging the two
// silently reroutes Codex dispatch and Codex's CLI version contract.
func TestDefaultIDIsNotCodexID(t *testing.T) {
	if DefaultID == CodexID {
		t.Fatalf("DefaultID must stay distinct from CodexID, both are %q", DefaultID)
	}
	if DefaultID != ClaudeID {
		t.Fatalf("unexpected default provider id: %s", DefaultID)
	}
}

// TestRegisteredProviderIDsMatchRegistryKeys ensures no provider reports an ID
// that disagrees with the key it is registered under.
func TestRegisteredProviderIDsMatchRegistryKeys(t *testing.T) {
	for _, id := range RegisteredIDs() {
		selectedProvider, err := Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		if selectedProvider.ID() != id {
			t.Fatalf("provider registered as %q reports id %q", id, selectedProvider.ID())
		}
	}
}

func TestCompatibilityContractsStayBoundToTheirProvider(t *testing.T) {
	cases := map[string]compatibilityContract{
		CodexID:    {minInclusive: "0.114.0", maxExclusive: "2.0.0"},
		ClaudeID:   {minInclusive: "2.0.0", maxExclusive: "3.0.0"},
		GeminiID:   {minInclusive: "0.34.0", maxExclusive: "1.0.0"},
		OpenCodeID: {minInclusive: "1.0.0", maxExclusive: "2.0.0"},
	}
	for id, want := range cases {
		got, err := compatibilityFor(id)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("unexpected contract for %q: %#v", id, got)
		}
	}
}

func TestRequiredToolsetForCodexProvider(t *testing.T) {
	selectedProvider, err := Resolve(CodexID)
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

func TestRequiredToolsetIncludesSharedAndProviderTools(t *testing.T) {
	selectedProvider, err := Resolve(DefaultID)
	if err != nil {
		t.Fatal(err)
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

func TestResolveOpenCodeProvider(t *testing.T) {
	selectedProvider, err := Resolve(OpenCodeID)
	if err != nil {
		t.Fatal(err)
	}
	if selectedProvider.ID() != OpenCodeID {
		t.Fatalf("unexpected provider id: %s", selectedProvider.ID())
	}
	if selectedProvider.DisplayName() != "OpenCode" {
		t.Fatalf("unexpected provider: %#v", selectedProvider)
	}
	got := RequiredToolset(selectedProvider)
	want := []string{"gh", "git", "opencode"}
	if len(got) != len(want) {
		t.Fatalf("unexpected tool count: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected toolset: %#v", got)
		}
	}
}

func TestOpenCodeInvocationUsesWorktreeDirAndRunCommand(t *testing.T) {
	selectedProvider, err := Resolve(OpenCodeID)
	if err != nil {
		t.Fatal(err)
	}

	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 7, Title: "Demo", URL: "https://github.com/owner/repo/issues/7"}
	session := state.Session{WorktreePath: "/tmp/worktree", Branch: "vigilante/issue-7", Provider: OpenCodeID}
	pr := ghcli.PullRequest{Number: 11, URL: "https://github.com/owner/repo/pull/11"}

	preflight, err := selectedProvider.BuildIssuePreflightInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Name != "opencode" {
		t.Fatalf("expected opencode binary, got %q", preflight.Name)
	}
	if preflight.Dir != "/tmp/worktree" {
		t.Fatalf("expected preflight dir to be worktree, got %#v", preflight)
	}
	wantPreflightArgs := []string{"run", "--dangerously-skip-permissions", skill.BuildIssuePreflightPrompt(target, issue, session)}
	assertInvocationArgs(t, preflight.Args, wantPreflightArgs)

	issueInvocation, err := selectedProvider.BuildIssueInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if issueInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected issue dir to be worktree, got %#v", issueInvocation)
	}
	wantIssueArgs := []string{"run", "--dangerously-skip-permissions", skill.BuildIssuePromptForRuntime(skill.RuntimeOpenCode, target, issue, session)}
	assertInvocationArgs(t, issueInvocation.Args, wantIssueArgs)

	conflictInvocation, err := selectedProvider.BuildConflictResolutionInvocation(ConflictTask{Target: target, Session: session, PR: pr})
	if err != nil {
		t.Fatal(err)
	}
	if conflictInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected conflict dir to be worktree, got %#v", conflictInvocation)
	}
	wantConflictArgs := []string{"run", "--dangerously-skip-permissions", skill.BuildConflictResolutionPromptForRuntime(skill.RuntimeOpenCode, target, session, pr)}
	assertInvocationArgs(t, conflictInvocation.Args, wantConflictArgs)

	remediationInvocation, err := selectedProvider.BuildCIRemediationInvocation(CIRemediationTask{
		Target:        target,
		Session:       session,
		PR:            pr,
		FailingChecks: []ghcli.StatusCheckRoll{{Context: "test", Conclusion: "FAILURE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if remediationInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected remediation dir to be worktree, got %#v", remediationInvocation)
	}
	wantRemediationArgs := []string{"run", "--dangerously-skip-permissions", skill.BuildCIRemediationPromptForRuntime(skill.RuntimeOpenCode, target, session, pr, []ghcli.StatusCheckRoll{{Context: "test", Conclusion: "FAILURE"}})}
	assertInvocationArgs(t, remediationInvocation.Args, wantRemediationArgs)

	packageInvocation, err := selectedProvider.BuildPackageRemediationInvocation(PackageRemediationTask{
		Target:        target,
		PRNumber:      11,
		PRBranch:      "vigilante/issue-7",
		FindingsCount: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if packageInvocation.Dir != "/tmp/repo" {
		t.Fatalf("expected package dir to be target path, got %#v", packageInvocation)
	}
	if packageInvocation.Args[0] != "run" || packageInvocation.Args[1] != "--dangerously-skip-permissions" {
		t.Fatalf("expected opencode run flags, got %#v", packageInvocation.Args)
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
	wantPreflightArgs := []string{"--print", "--dangerously-skip-permissions", skill.BuildIssuePreflightPrompt(target, issue, session)}
	assertInvocationArgs(t, preflight.Args, wantPreflightArgs)

	issueInvocation, err := selectedProvider.BuildIssueInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if issueInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected issue dir to be worktree, got %#v", issueInvocation)
	}
	wantIssueArgs := []string{"--print", "--dangerously-skip-permissions", skill.BuildIssuePromptForRuntime(skill.RuntimeClaude, target, issue, session)}
	assertInvocationArgs(t, issueInvocation.Args, wantIssueArgs)

	conflictInvocation, err := selectedProvider.BuildConflictResolutionInvocation(ConflictTask{Target: target, Session: session, PR: pr})
	if err != nil {
		t.Fatal(err)
	}
	if conflictInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected conflict dir to be worktree, got %#v", conflictInvocation)
	}
	wantConflictArgs := []string{"--print", "--dangerously-skip-permissions", skill.BuildConflictResolutionPromptForRuntime(skill.RuntimeClaude, target, session, pr)}
	assertInvocationArgs(t, conflictInvocation.Args, wantConflictArgs)

	remediationInvocation, err := selectedProvider.BuildCIRemediationInvocation(CIRemediationTask{
		Target:        target,
		Session:       session,
		PR:            pr,
		FailingChecks: []ghcli.StatusCheckRoll{{Context: "test", Conclusion: "FAILURE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if remediationInvocation.Dir != "/tmp/worktree" {
		t.Fatalf("expected remediation dir to be worktree, got %#v", remediationInvocation)
	}
	wantRemediationArgs := []string{"--print", "--permission-mode", "acceptEdits", skill.BuildCIRemediationPromptForRuntime(skill.RuntimeClaude, target, session, pr, []ghcli.StatusCheckRoll{{Context: "test", Conclusion: "FAILURE"}})}
	assertInvocationArgs(t, remediationInvocation.Args, wantRemediationArgs)
}

func TestResolveIssueLabelUsesRegisteredProviderIDs(t *testing.T) {
	original := registry
	registry = map[string]Provider{
		CodexID:  codexProvider{},
		"cursor": testProvider{id: "cursor"},
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
		CodexID:  codexProvider{},
		"cursor": testProvider{id: "cursor"},
	}
	t.Cleanup(func() {
		registry = original
	})

	_, err := ResolveIssueLabel([]ghcli.Label{{Name: CodexID}, {Name: "cursor"}})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := err.Error(); got != "multiple provider labels: codex, cursor" {
		t.Fatalf("unexpected conflict error: %s", got)
	}
}

func TestResolveIssueLabelsSelectsClaudeModel(t *testing.T) {
	for _, model := range []string{"sonnet", "opus", "fable"} {
		providerID, gotModel, err := ResolveIssueLabels([]ghcli.Label{{Name: "claude:" + model}})
		if err != nil {
			t.Fatal(err)
		}
		if providerID != ClaudeID || gotModel != model {
			t.Fatalf("claude:%s resolved to provider=%q model=%q", model, providerID, gotModel)
		}
	}
}

func TestResolveIssueLabelsClaudeConflictRules(t *testing.T) {
	tests := []struct {
		name      string
		labels    []ghcli.Label
		provider  string
		model     string
		wantError bool
	}{
		{name: "bare and model", labels: []ghcli.Label{{Name: ClaudeID}, {Name: "claude:sonnet"}}, provider: ClaudeID, model: "sonnet"},
		{name: "two models", labels: []ghcli.Label{{Name: "claude:sonnet"}, {Name: "claude:opus"}}, wantError: true},
		{name: "other provider", labels: []ghcli.Label{{Name: "claude:opus"}, {Name: CodexID}}, wantError: true},
		{name: "unknown model", labels: []ghcli.Label{{Name: "claude:haiku"}}, provider: "", model: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providerID, model, err := ResolveIssueLabels(tt.labels)
			if (err != nil) != tt.wantError {
				t.Fatalf("unexpected error: %v", err)
			}
			if providerID != tt.provider || model != tt.model {
				t.Fatalf("resolved provider=%q model=%q", providerID, model)
			}
		})
	}
}

func TestClaudeInvocationIncludesPersistedModel(t *testing.T) {
	selectedProvider, err := Resolve(ClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	target := state.WatchTarget{Path: "/tmp/repo", Repo: "owner/repo"}
	issue := ghcli.Issue{Number: 7, Title: "Demo"}
	session := state.Session{WorktreePath: "/tmp/worktree", Model: "opus"}
	pr := ghcli.PullRequest{Number: 11}

	invocations := []Invocation{}
	preflight, _ := selectedProvider.BuildIssuePreflightInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	invocations = append(invocations, preflight)
	issueRun, _ := selectedProvider.BuildIssueInvocation(IssueTask{Target: target, Issue: issue, Session: session})
	invocations = append(invocations, issueRun)
	conflict, _ := selectedProvider.BuildConflictResolutionInvocation(ConflictTask{Target: target, Session: session, PR: pr})
	invocations = append(invocations, conflict)
	ci, _ := selectedProvider.BuildCIRemediationInvocation(CIRemediationTask{Target: target, Session: session, PR: pr})
	invocations = append(invocations, ci)
	for _, invocation := range invocations {
		if len(invocation.Args) < 2 || invocation.Args[0] != "--model" || invocation.Args[1] != "opus" {
			t.Fatalf("model missing from invocation: %#v", invocation.Args)
		}
	}
}

func TestValidateVersionOutputAcceptsSupportedVersions(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		output   string
	}{
		{name: "codex current supported 0.x", provider: CodexID, output: "codex 0.114.0"},
		{name: "codex", provider: CodexID, output: "codex 1.2.3"},
		{name: "claude 2.x", provider: ClaudeID, output: "Claude Code v2.1.3"},
		{name: "gemini current supported 0.x", provider: GeminiID, output: "gemini-cli 0.34.0"},
		{name: "opencode 1.x minimum", provider: OpenCodeID, output: "opencode 1.0.0"},
		{name: "opencode 1.x current", provider: OpenCodeID, output: "opencode 1.15.13"},
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
	selectedProvider, err := Resolve(CodexID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "codex 0.113.9")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	for _, want := range []string{"codex CLI version 0.113.9 is incompatible", ">=0.114.0, <2.0.0"} {
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

	err = ValidateVersionOutput(selectedProvider, "Claude Code 3.0.0")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	if !strings.Contains(err.Error(), "supported: >=2.0.0, <3.0.0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVersionOutputRejectsTooOldClaude2Contract(t *testing.T) {
	selectedProvider, err := Resolve(ClaudeID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "Claude Code 1.9.9")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	if !strings.Contains(err.Error(), "supported: >=2.0.0, <3.0.0") {
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
	for _, want := range []string{"could not parse gemini CLI version", "supported: >=0.34.0, <1.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}
}

func TestValidateVersionOutputRejectsTooOldOpenCodeContract(t *testing.T) {
	selectedProvider, err := Resolve(OpenCodeID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "opencode 0.99.0")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	for _, want := range []string{"opencode CLI version 0.99.0 is incompatible", "supported: >=1.0.0, <2.0.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %q", want, err.Error())
		}
	}
}

func TestValidateVersionOutputRejectsTooNewOpenCodeContract(t *testing.T) {
	selectedProvider, err := Resolve(OpenCodeID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "opencode 2.0.0")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	if !strings.Contains(err.Error(), "supported: >=1.0.0, <2.0.0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateVersionOutputRejectsTooOldGemini0Contract(t *testing.T) {
	selectedProvider, err := Resolve(GeminiID)
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateVersionOutput(selectedProvider, "gemini-cli 0.33.1")
	if err == nil {
		t.Fatal("expected compatibility error")
	}
	for _, want := range []string{"gemini CLI version 0.33.1 is incompatible", "supported: >=0.34.0, <1.0.0"} {
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

func (p testProvider) BuildCIRemediationInvocation(task CIRemediationTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildIssueCreateInvocation(task IssueCreateTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildPackageRemediationInvocation(task PackageRemediationTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildReviewInvocation(task ReviewTask) (Invocation, error) {
	return Invocation{}, nil
}

func TestParseAgentSelector(t *testing.T) {
	cases := []struct {
		selector string
		provider string
		model    string
	}{
		{selector: "claude:fable", provider: ClaudeID, model: "fable"},
		{selector: "claude:opus", provider: ClaudeID, model: "opus"},
		{selector: "claude:claude-fable-5", provider: ClaudeID, model: "claude-fable-5"},
		{selector: "codex:gpt-5-codex", provider: CodexID, model: "gpt-5-codex"},
		{selector: "  claude:fable  ", provider: ClaudeID, model: "fable"},
	}
	for _, tc := range cases {
		selected, model, err := ParseAgentSelector(tc.selector)
		if err != nil {
			t.Fatalf("ParseAgentSelector(%q) failed: %v", tc.selector, err)
		}
		if selected.ID() != tc.provider {
			t.Fatalf("ParseAgentSelector(%q) provider = %q, want %q", tc.selector, selected.ID(), tc.provider)
		}
		if model != tc.model {
			t.Fatalf("ParseAgentSelector(%q) model = %q, want %q", tc.selector, model, tc.model)
		}
	}
}

func TestParseAgentSelectorRejectsMalformedSelectors(t *testing.T) {
	for _, selector := range []string{"", "claude", "claude:", ":fable", ":", "   "} {
		_, _, err := ParseAgentSelector(selector)
		if err == nil || !strings.Contains(err.Error(), "expected {provider}:{model}") {
			t.Fatalf("ParseAgentSelector(%q) error = %v, want malformed-selector error", selector, err)
		}
	}
}

func TestParseAgentSelectorRejectsUnknownProvider(t *testing.T) {
	_, _, err := ParseAgentSelector("foo:bar")
	if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("expected unsupported provider error, got: %v", err)
	}
	for _, id := range RegisteredIDs() {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("expected error to list registered provider %q, got: %v", id, err)
		}
	}
}

func TestClaudeBuildReviewInvocationHonorsModel(t *testing.T) {
	task := ReviewTask{
		Target: state.WatchTarget{
			Repo: "owner/repo",
			Path: "/tmp/repo",
		},
		PRNumber: 42,
		Model:    "fable",
	}
	selected, err := Resolve(ClaudeID)
	if err != nil {
		t.Fatal(err)
	}

	invocation, err := selected.BuildReviewInvocation(task)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Dir != "/tmp/repo" {
		t.Fatalf("expected review invocation to run in the repo path, got: %q", invocation.Dir)
	}
	if invocation.Name != "claude" {
		t.Fatalf("unexpected invocation name: %q", invocation.Name)
	}
	if len(invocation.Args) < 2 || invocation.Args[0] != "--model" || invocation.Args[1] != "fable" {
		t.Fatalf("expected --model fable to be prepended, got: %v", invocation.Args)
	}
	prompt := invocation.Args[len(invocation.Args)-1]
	if !strings.Contains(prompt, "Pull Request: #42") {
		t.Fatalf("expected prompt to reference the PR, got: %q", prompt)
	}
	if !strings.Contains(prompt, "vigilante-adversarial-review") {
		t.Fatalf("expected prompt to use the adversarial review skill, got: %q", prompt)
	}
}

func TestBuildReviewInvocationRejectsProvidersWithoutModelOverride(t *testing.T) {
	task := ReviewTask{
		Target: state.WatchTarget{
			Repo: "owner/repo",
			Path: "/tmp/repo",
		},
		PRNumber: 42,
		Model:    "some-model",
	}
	for _, id := range []string{CodexID, GeminiID, OpenCodeID} {
		selected, err := Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = selected.BuildReviewInvocation(task)
		if err == nil || !strings.Contains(err.Error(), "does not support a model override") {
			t.Fatalf("expected %s review invocation to reject the model override, got: %v", id, err)
		}
	}
}

func TestBuildIssueCreateInvocationForAllProviders(t *testing.T) {
	task := IssueCreateTask{
		Target: state.WatchTarget{
			Repo: "owner/repo",
			Path: "/tmp/repo",
		},
		Prompt: "add dark mode",
	}

	for _, id := range RegisteredIDs() {
		t.Run(id, func(t *testing.T) {
			p, err := Resolve(id)
			if err != nil {
				t.Fatal(err)
			}
			inv, err := p.BuildIssueCreateInvocation(task)
			if err != nil {
				t.Fatal(err)
			}
			if inv.Name == "" {
				t.Fatal("expected non-empty invocation name")
			}
			// Codex uses --cd flag instead of Dir
			if id != CodexID && inv.Dir != "/tmp/repo" {
				t.Fatalf("expected dir %q, got %q", "/tmp/repo", inv.Dir)
			}
			// Verify the prompt text is somewhere in the args
			found := false
			for _, arg := range inv.Args {
				if strings.Contains(arg, "add dark mode") {
					found = true
					break
				}
			}
			if !found {
				t.Fatal("expected prompt text in invocation args")
			}
		})
	}
}
