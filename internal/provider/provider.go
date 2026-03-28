package provider

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const DefaultID = "codex"
const ClaudeID = "claude"
const GeminiID = "gemini"

type Invocation struct {
	Dir  string
	Name string
	Args []string
}

type IssueTask struct {
	Target  state.WatchTarget
	Issue   backend.WorkItem
	Session state.Session
}

type ConflictTask struct {
	Target  state.WatchTarget
	Session state.Session
	PR      backend.PullRequest
}

type CIRemediationTask struct {
	Target        state.WatchTarget
	Session       state.Session
	PR            backend.PullRequest
	FailingChecks []backend.StatusCheck
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
}

var registry = map[string]Provider{
	DefaultID: codexProvider{},
	ClaudeID:  claudeProvider{},
	GeminiID:  geminiProvider{},
}

func RegisteredIDs() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func ResolveIssueLabel(labels []string) (string, error) {
	return backend.ResolveIssueProviderLabel(labels, RegisteredIDs())
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
