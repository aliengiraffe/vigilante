package provider

import (
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type claudeProvider struct{}

func (claudeProvider) ID() string {
	return ClaudeID
}

func (claudeProvider) DisplayName() string {
	return "Claude Code"
}

func (claudeProvider) RequiredTools() []string {
	return []string{"claude"}
}

func (claudeProvider) EnsureRuntimeInstalled(store *state.Store) error {
	return skill.EnsureInstalled(skill.RuntimeClaude, store.ClaudeHome())
}

func (claudeProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Name: "claude",
		Args: []string{
			"--print",
			"--cwd", task.Session.WorktreePath,
			"--permission-mode", "acceptEdits",
			skill.BuildIssuePreflightPrompt(task.Target, task.Issue, task.Session),
		},
	}, nil
}

func (claudeProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Name: "claude",
		Args: []string{
			"--print",
			"--cwd", task.Session.WorktreePath,
			"--permission-mode", "acceptEdits",
			skill.BuildIssuePromptForRuntime(skill.RuntimeClaude, task.Target, task.Issue, task.Session),
		},
	}, nil
}

func (claudeProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return Invocation{
		Name: "claude",
		Args: []string{
			"--print",
			"--cwd", task.Session.WorktreePath,
			"--permission-mode", "acceptEdits",
			skill.BuildConflictResolutionPromptForRuntime(skill.RuntimeClaude, task.Target, task.Session, task.PR),
		},
	}, nil
}
