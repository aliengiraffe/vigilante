package provider

import (
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type codexProvider struct{}

func (codexProvider) ID() string {
	return DefaultID
}

func (codexProvider) DisplayName() string {
	return "Codex"
}

func (codexProvider) RequiredTools() []string {
	return []string{"codex"}
}

func (codexProvider) EnsureRuntimeInstalled(store *state.Store) error {
	return skill.EnsureInstalled(skill.RuntimeCodex, store.CodexHome())
}

func (codexProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Name: "codex",
		Args: []string{
			"exec",
			"--cd", task.Session.WorktreePath,
			"--dangerously-bypass-approvals-and-sandbox",
			skill.BuildIssuePreflightPrompt(task.Target, task.Issue, task.Session),
		},
	}, nil
}

func (codexProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Name: "codex",
		Args: []string{
			"exec",
			"--cd", task.Session.WorktreePath,
			"--dangerously-bypass-approvals-and-sandbox",
			skill.BuildIssuePrompt(task.Target, task.Issue, task.Session),
		},
	}, nil
}

func (codexProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return Invocation{
		Name: "codex",
		Args: []string{
			"exec",
			"--cd", task.Session.WorktreePath,
			"--dangerously-bypass-approvals-and-sandbox",
			skill.BuildConflictResolutionPrompt(task.Target, task.Session, task.PR),
		},
	}, nil
}
