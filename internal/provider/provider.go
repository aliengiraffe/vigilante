package provider

import (
	"fmt"
	"sort"
	"strings"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const DefaultID = "codex"

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

type Provider interface {
	ID() string
	DisplayName() string
	RequiredTools() []string
	EnsureRuntimeInstalled(store *state.Store) error
	BuildIssueInvocation(task IssueTask) (Invocation, error)
	BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error)
}

var registry = map[string]Provider{
	DefaultID: codexProvider{},
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
