package provider

import (
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type opencodeProvider struct{}

func (opencodeProvider) ID() string {
	return OpenCodeID
}

func (opencodeProvider) DisplayName() string {
	return "OpenCode"
}

func (opencodeProvider) RequiredTools() []string {
	return []string{"opencode"}
}

func (opencodeProvider) EnsureRuntimeInstalled(store *state.Store) error {
	return skill.EnsureInstalled(skill.RuntimeOpenCode, store.OpenCodeHome())
}

func (opencodeProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "opencode",
		Args: []string{
			"run",
			"--dangerously-skip-permissions",
			skill.BuildIssuePreflightPrompt(task.Target, task.Issue, task.Session),
		},
	}, nil
}

func (opencodeProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "opencode",
		Args: []string{
			"run",
			"--dangerously-skip-permissions",
			skill.BuildIssuePromptForRuntime(skill.RuntimeOpenCode, task.Target, task.Issue, task.Session),
		},
	}, nil
}

func (opencodeProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "opencode",
		Args: []string{
			"run",
			"--dangerously-skip-permissions",
			skill.BuildConflictResolutionPromptForRuntime(skill.RuntimeOpenCode, task.Target, task.Session, task.PR),
		},
	}, nil
}

func (opencodeProvider) BuildCIRemediationInvocation(task CIRemediationTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "opencode",
		Args: []string{
			"run",
			"--dangerously-skip-permissions",
			skill.BuildCIRemediationPromptForRuntime(skill.RuntimeOpenCode, task.Target, task.Session, task.PR, task.FailingChecks),
		},
	}, nil
}

func (opencodeProvider) BuildIssueCreateInvocation(task IssueCreateTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Target.Path,
		Name: "opencode",
		Args: []string{
			"run",
			"--dangerously-skip-permissions",
			skill.BuildIssueCreatePrompt(skill.RuntimeOpenCode, task.Target, task.Prompt),
		},
	}, nil
}

func (opencodeProvider) BuildPackageRemediationInvocation(task PackageRemediationTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Target.Path,
		Name: "opencode",
		Args: []string{
			"run",
			"--dangerously-skip-permissions",
			skill.BuildPackageRemediationPrompt(task.Target, task.PRNumber, task.PRBranch, task.FindingsCount),
		},
	}, nil
}
