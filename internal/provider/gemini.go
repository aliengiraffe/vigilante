package provider

import (
	"github.com/nicobistolfi/vigilante/internal/skill"
	"github.com/nicobistolfi/vigilante/internal/state"
)

type geminiProvider struct{}

func (geminiProvider) ID() string {
	return GeminiID
}

func (geminiProvider) DisplayName() string {
	return "Gemini CLI"
}

func (geminiProvider) RequiredTools() []string {
	return []string{"gemini"}
}

func (geminiProvider) EnsureRuntimeInstalled(store *state.Store) error {
	return skill.EnsureInstalled(skill.RuntimeGemini, store.GeminiHome())
}

func (geminiProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "gemini",
		Args: []string{
			"--prompt",
			skill.BuildIssuePreflightPrompt(task.Target, task.Issue, task.Session),
			"--yolo",
		},
	}, nil
}

func (geminiProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "gemini",
		Args: []string{
			"--prompt",
			skill.BuildIssuePromptForRuntime(skill.RuntimeGemini, task.Target, task.Issue, task.Session),
			"--yolo",
		},
	}, nil
}

func (geminiProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "gemini",
		Args: []string{
			"--prompt",
			skill.BuildConflictResolutionPromptForRuntime(skill.RuntimeGemini, task.Target, task.Session, task.PR),
			"--yolo",
		},
	}, nil
}

func (geminiProvider) BuildCIRemediationInvocation(task CIRemediationTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Session.WorktreePath,
		Name: "gemini",
		Args: []string{
			"--prompt",
			skill.BuildCIRemediationPromptForRuntime(skill.RuntimeGemini, task.Target, task.Session, task.PR, task.FailingChecks),
			"--yolo",
		},
	}, nil
}

func (geminiProvider) BuildIssueCreateInvocation(task IssueCreateTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Target.Path,
		Name: "gemini",
		Args: []string{
			"--prompt",
			skill.BuildIssueCreatePrompt(skill.RuntimeGemini, task.Target, task.Prompt),
			"--yolo",
		},
	}, nil
}

func (geminiProvider) BuildPackageRemediationInvocation(task PackageRemediationTask) (Invocation, error) {
	return Invocation{
		Dir:  task.Target.Path,
		Name: "gemini",
		Args: []string{
			"--prompt",
			skill.BuildPackageRemediationPrompt(task.Target, task.PRNumber, task.PRBranch, task.FindingsCount),
			"--yolo",
		},
	}, nil
}
