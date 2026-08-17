// Package provider describes the coding-agent CLIs vigilante can launch — Claude
// Code, Codex, Gemini, and OpenCode — and how to invoke each one for a given task.
//
// A Provider owns the command line, required toolset, and prompt shape for its
// agent, so adding support for another agent means adding a Provider here rather
// than threading conditionals through the runner.
package provider

import (
	"fmt"
	"sort"
	"strings"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const CodexID = "codex"
const ClaudeID = "claude"
const GeminiID = "gemini"
const OpenCodeID = "opencode"

var claudeModelLabels = map[string]string{
	"claude:sonnet": "sonnet",
	"claude:opus":   "opus",
	"claude:fable":  "fable",
}

// DefaultID is the provider selected when no provider is specified by a flag,
// an issue label, or a persisted watch target. It is deliberately an alias of
// another provider's ID rather than its own literal, so "the default" and
// "which provider" stay separate concepts and changing the default stays a
// one-line edit.
const DefaultID = ClaudeID

type Invocation struct {
	Dir  string
	Name string
	Args []string
}

type IssueTask struct {
	Target  state.WatchTarget
	Issue   ghcli.Issue
	Session state.Session
}

type ConflictTask struct {
	Target  state.WatchTarget
	Session state.Session
	PR      ghcli.PullRequest
}

type CIRemediationTask struct {
	Target        state.WatchTarget
	Session       state.Session
	PR            ghcli.PullRequest
	FailingChecks []ghcli.StatusCheckRoll
}

type IssueCreateTask struct {
	Target state.WatchTarget
	Prompt string
}

// PackageRemediationTask describes a package-hardening remediation session
// dispatched when a human checks the "implement fixes" checkbox on a PR
// hardening comment.
type PackageRemediationTask struct {
	Target        state.WatchTarget
	PRNumber      int
	PRBranch      string
	FindingsCount int
}

// ReviewTask describes a solicited adversarial review session for an existing
// Vigilante-opened pull request. The session must not modify the PR branch;
// its only write is the review findings posted back to the PR.
type ReviewTask struct {
	Target   state.WatchTarget
	PRNumber int
	Model    string
}

type Provider interface {
	ID() string
	DisplayName() string
	RequiredTools() []string
	EnsureRuntimeInstalled(store *state.Store) error
	BuildIssuePreflightInvocation(task IssueTask) (Invocation, error)
	BuildIssueInvocation(task IssueTask) (Invocation, error)
	BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error)
	BuildCIRemediationInvocation(task CIRemediationTask) (Invocation, error)
	BuildIssueCreateInvocation(task IssueCreateTask) (Invocation, error)
	BuildPackageRemediationInvocation(task PackageRemediationTask) (Invocation, error)
	BuildReviewInvocation(task ReviewTask) (Invocation, error)
}

var registry = map[string]Provider{
	CodexID:    codexProvider{},
	ClaudeID:   claudeProvider{},
	GeminiID:   geminiProvider{},
	OpenCodeID: opencodeProvider{},
}

func RegisteredIDs() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func ResolveIssueLabel(labels []ghcli.Label) (string, error) {
	providerID, _, err := ResolveIssueLabels(labels)
	return providerID, err
}

// ResolveIssueLabels resolves provider and model routing labels attached to an issue.
func ResolveIssueLabels(labels []ghcli.Label) (string, string, error) {
	matches := make([]string, 0, len(registry))
	for _, providerID := range RegisteredIDs() {
		if ghcli.HasAnyLabel(labels, providerID) {
			matches = append(matches, providerID)
		}
	}
	models := make([]string, 0, 1)
	for label, model := range claudeModelLabels {
		if ghcli.HasAnyLabel(labels, label) {
			models = append(models, model)
		}
	}
	sort.Strings(models)
	if len(models) > 1 {
		return "", "", fmt.Errorf("multiple model labels: claude:%s", strings.Join(models, ", claude:"))
	}
	if len(models) == 1 && !containsString(matches, ClaudeID) {
		matches = append(matches, ClaudeID)
		sort.Strings(matches)
	}
	switch len(matches) {
	case 0:
		return "", "", nil
	case 1:
		if len(models) == 1 {
			return matches[0], models[0], nil
		}
		return matches[0], "", nil
	default:
		return "", "", fmt.Errorf("multiple provider labels: %s", strings.Join(matches, ", "))
	}
}

// modelOverrideUnsupportedError explains that a provider's CLI cannot honor
// the model requested by a "{provider}:{model}" selector, instead of silently
// ignoring it.
func modelOverrideUnsupportedError(providerID string, model string) error {
	return fmt.Errorf("the %s CLI does not support a model override, so the requested model %q cannot be honored; use a %s:{model} selector instead", providerID, model, ClaudeID)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ParseAgentSelector parses a "{provider}:{model}" coding-agent selector such
// as "claude:fable" or "claude:claude-fable-5". The provider segment must be a
// registered provider ID; the model segment may be a model family alias or a
// specific model identifier and is passed through verbatim.
func ParseAgentSelector(selector string) (Provider, string, error) {
	trimmed := strings.TrimSpace(selector)
	providerID, model, found := strings.Cut(trimmed, ":")
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if !found || providerID == "" || model == "" {
		return nil, "", fmt.Errorf("invalid agent selector %q: expected {provider}:{model}, for example claude:fable", trimmed)
	}
	selected, ok := registry[providerID]
	if !ok {
		return nil, "", fmt.Errorf("unsupported provider %q in agent selector (registered providers: %s)", providerID, strings.Join(RegisteredIDs(), ", "))
	}
	return selected, model, nil
}

func Resolve(id string) (Provider, error) {
	resolved := strings.TrimSpace(id)
	if resolved == "" {
		resolved = DefaultID
	}
	provider, ok := registry[resolved]
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q", resolved)
	}
	return provider, nil
}

func RequiredToolset(p Provider) []string {
	seen := map[string]bool{}
	tools := make([]string, 0, 2+len(p.RequiredTools()))
	for _, tool := range append([]string{"git", "gh"}, p.RequiredTools()...) {
		tool = strings.TrimSpace(tool)
		if tool == "" || seen[tool] {
			continue
		}
		seen[tool] = true
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	return tools
}
