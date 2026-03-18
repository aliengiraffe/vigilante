package skill

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	skillassets "github.com/nicobistolfi/vigilante"
	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/repo"
	"github.com/nicobistolfi/vigilante/internal/state"
)

const VigilanteIssueImplementation = "vigilante-issue-implementation"
const VigilanteIssueImplementationOnMonorepo = "vigilante-issue-implementation-on-monorepo"
const VigilanteIssueImplementationOnTurborepo = "vigilante-issue-implementation-on-turborepo"
const VigilanteIssueImplementationOnNx = "vigilante-issue-implementation-on-nx"
const VigilanteIssueImplementationOnRush = "vigilante-issue-implementation-on-rush"
const VigilanteIssueImplementationOnRushMonorepo = "vigilante-issue-implementation-on-rush-monorepo"
const VigilanteIssueImplementationOnBazel = "vigilante-issue-implementation-on-bazel"
const VigilanteIssueImplementationOnGradle = "vigilante-issue-implementation-on-gradle"
const VigilanteIssueImplementationOnGradleMultiProject = "vigilante-issue-implementation-on-gradle-multi-project"
const VigilanteIssueImplementationOnBazelMonorepo = "vigilante-issue-implementation-on-bazel-monorepo"
const VigilanteConflictResolution = "vigilante-conflict-resolution"
const VigilanteCreateIssue = "vigilante-create-issue"
const VigilanteLocalServiceDependencies = "vigilante-local-service-dependencies"
const DockerComposeLaunch = "docker-compose-launch"

const RuntimeCodex = "codex"
const RuntimeClaude = "claude"
const RuntimeGemini = "gemini"

func VigilanteSkillNames() []string {
	return []string{
		VigilanteIssueImplementation,
		VigilanteIssueImplementationOnMonorepo,
		VigilanteIssueImplementationOnTurborepo,
		VigilanteIssueImplementationOnNx,
		VigilanteIssueImplementationOnRush,
		VigilanteIssueImplementationOnRushMonorepo,
		VigilanteIssueImplementationOnBazel,
		VigilanteIssueImplementationOnGradle,
		VigilanteIssueImplementationOnGradleMultiProject,
		VigilanteIssueImplementationOnBazelMonorepo,
		VigilanteConflictResolution,
		VigilanteCreateIssue,
		VigilanteLocalServiceDependencies,
		DockerComposeLaunch,
	}
}

func EnsureInstalled(runtime string, home string) error {
	for _, name := range VigilanteSkillNames() {
		source, err := resolveSkillSource(name)
		if err != nil {
			return err
		}
		targets, err := installTargets(runtime, home, name)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if err := source.install(target); err != nil {
				return err
			}
		}
		if strings.TrimSpace(runtime) == RuntimeGemini {
			if err := installGeminiCommand(home, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func installTargets(runtime string, home string, name string) ([]string, error) {
	switch strings.TrimSpace(runtime) {
	case RuntimeCodex:
		return []string{filepath.Join(home, "skills", name)}, nil
	case RuntimeClaude:
		return []string{
			filepath.Join(home, "skills", name),
			filepath.Join(home, "commands", name),
		}, nil
	case RuntimeGemini:
		return []string{
			filepath.Join(home, "skills", name),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported skill runtime %q", runtime)
	}
}

func installGeminiCommand(home string, name string) error {
	body, err := skillBody(name)
	if err != nil {
		return err
	}
	commandDir := filepath.Join(home, "commands")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		return err
	}
	commandPath := filepath.Join(commandDir, name+".toml")
	commandBody := strings.TrimSpace(fmt.Sprintf(`
description = "Bundled Vigilante skill: %s"
prompt = '''
Follow these %q skill instructions directly for this task:
%s
'''
`, name, "`"+name+"`", body)) + "\n"
	return os.WriteFile(commandPath, []byte(commandBody), 0o644)
}

func skillBody(name string) (string, error) {
	source, err := resolveSkillSource(name)
	if err != nil {
		return "", err
	}
	switch s := source.(type) {
	case dirSkillSource:
		data, err := os.ReadFile(filepath.Join(string(s), "SKILL.md"))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	case embeddedSkillSource:
		data, err := fs.ReadFile(s.fs, pathJoin(s.root, "SKILL.md"))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	default:
		return "", fmt.Errorf("unsupported skill source %T", source)
	}
}

func InlineSkillHeader(name string) string {
	body, err := skillBody(name)
	if err != nil {
		return fmt.Sprintf("The `%s` skill was requested, but the bundled instructions could not be loaded: %v", name, err)
	}
	return strings.Join([]string{
		fmt.Sprintf("Follow these `%s` skill instructions directly for this task:", name),
		body,
		"",
	}, "\n")
}

func BuildIssuePrompt(target state.WatchTarget, issue ghcli.Issue, session state.Session) string {
	return BuildIssuePromptForRuntime(RuntimeCodex, target, issue, session)
}

func BuildIssuePromptForRuntime(runtime string, target state.WatchTarget, issue ghcli.Issue, session state.Session) string {
	selectedSkill := IssueImplementationSkill(target)
	lines := []string{}
	if runtimeUsesInlineSkillHeader(runtime) {
		lines = append(lines, InlineSkillHeader(selectedSkill))
	} else {
		lines = append(lines, fmt.Sprintf("Use the `%s` skill for this task.", selectedSkill))
	}
	lines = append(lines,
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Detected repo shape: %s", normalizedRepoShape(target)),
		fmt.Sprintf("Selected issue implementation skill: %s", selectedSkill),
		fmt.Sprintf("Repo process context JSON: %s", repoClassificationJSON(target)),
		fmt.Sprintf("Issue: #%d - %s", issue.Number, issue.Title),
		fmt.Sprintf("Issue URL: %s", issue.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		"Use `gh issue comment` to comment on the issue when you start working, post a concise implementation plan before substantial coding, add milestone progress comments as you make progress, comment again when the PR is opened, push the branch, open a pull request, and report any execution failure back to the issue.",
		fmt.Sprintf("For the coding-agent start comment, use `## 🕹️ Coding Agent Launched: %s` instead of a generic session-start title.", displayProviderName(session.Provider)),
		"Use the same GitHub comment structure for every non-terminal milestone comment: a short header with the current stage and optional emoji, a 10-cell progress bar with percentage, an `ETA: ~N minutes` line, 1-3 concise bullets covering what just happened and what is next, and an optional short playful quote or tagline.",
		"Use the issue as the source of truth for the requested behavior and keep the implementation minimal.",
	)
	if strings.TrimSpace(session.ReusedRemoteBranch) != "" {
		baseBranch := strings.TrimSpace(session.BaseBranch)
		if baseBranch == "" {
			baseBranch = "main"
		}
		lines = append(lines,
			fmt.Sprintf("Existing remote issue branch detected: origin/%s", session.ReusedRemoteBranch),
			fmt.Sprintf("Default branch for comparison: %s", baseBranch),
			fmt.Sprintf("Diff summary against `%s`: %s", baseBranch, fallbackPromptText(session.BranchDiffSummary, "Diff analysis was requested but no summary was recorded.")),
			"Continue from the reused branch state and build on top of the existing diff instead of restarting from scratch.",
		)
	}
	if normalizedRepoShape(target) == string(repo.ShapeMonorepo) {
		lines = append(lines,
			fmt.Sprintf("Detected monorepo stack: %s", normalizedMonorepoStack(target)),
			fmt.Sprintf("Monorepo execution context JSON: %s", monorepoExecutionContextJSON(target, selectedSkill)),
			fmt.Sprintf("When local services are required, use the `%s` skill instead of inventing ad hoc compose logic.", DockerComposeLaunch),
		)
	}
	return strings.Join(lines, "\n")
}

func IssueImplementationSkill(target state.WatchTarget) string {
	if normalizedRepoShape(target) == string(repo.ShapeGradleMultiProject) {
		return VigilanteIssueImplementationOnGradleMultiProject
	}
	if normalizedRepoShape(target) == string(repo.ShapeMonorepo) && hasWorkspaceConfigFile(target.Classification, "rush.json") {
		return VigilanteIssueImplementationOnRushMonorepo
	}
	if normalizedRepoShape(target) != string(repo.ShapeMonorepo) {
		return VigilanteIssueImplementation
	}
	if isBazelMonorepo(target.Classification) {
		return VigilanteIssueImplementationOnBazelMonorepo
	}
	switch normalizedMonorepoStack(target) {
	case string(repo.MonorepoStackTurborepo):
		return VigilanteIssueImplementationOnTurborepo
	case string(repo.MonorepoStackNx):
		return VigilanteIssueImplementationOnNx
	case string(repo.MonorepoStackRush):
		return VigilanteIssueImplementationOnRushMonorepo
	case string(repo.MonorepoStackBazel):
		return VigilanteIssueImplementationOnBazel
	case string(repo.MonorepoStackGradle):
		return VigilanteIssueImplementationOnGradle
	default:
		if isTurborepoTarget(target) {
			return VigilanteIssueImplementationOnTurborepo
		}
		return VigilanteIssueImplementationOnMonorepo
	}
}

func isTurborepoTarget(target state.WatchTarget) bool {
	if normalizedRepoShape(target) != string(repo.ShapeMonorepo) {
		return false
	}

	hints := target.Classification.ProcessHints
	hasTurboConfig := slicesContains(hints.WorkspaceConfigFiles, "turbo.json")
	if !hasTurboConfig {
		return false
	}
	return slicesContains(hints.WorkspaceConfigFiles, "pnpm-workspace.yaml") ||
		slicesContains(hints.WorkspaceManifestFiles, "package.json")
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func isBazelMonorepo(classification repo.Classification) bool {
	return len(classification.ProcessHints.BazelRepoMarkers) > 0 && len(classification.ProcessHints.BazelPackageRoots) > 0
}

func hasWorkspaceConfigFile(classification repo.Classification, name string) bool {
	for _, file := range classification.ProcessHints.WorkspaceConfigFiles {
		if strings.EqualFold(strings.TrimSpace(file), name) {
			return true
		}
	}
	return false
}

func normalizedRepoShape(target state.WatchTarget) string {
	shape := strings.TrimSpace(string(target.Classification.Shape))
	if shape == "" {
		return string(repo.ShapeTraditional)
	}
	return shape
}

func normalizedMonorepoStack(target state.WatchTarget) string {
	stack := strings.TrimSpace(string(target.Classification.MonorepoStack))
	if stack == "" && normalizedRepoShape(target) == string(repo.ShapeMonorepo) {
		return string(repo.MonorepoStackUnknown)
	}
	return stack
}

func repoClassificationJSON(target state.WatchTarget) string {
	classification := target.Classification
	if strings.TrimSpace(string(classification.Shape)) == "" {
		classification.Shape = repo.ShapeTraditional
	}
	payload := struct {
		Shape         repo.Shape         `json:"shape"`
		MonorepoStack repo.MonorepoStack `json:"monorepo_stack,omitempty"`
		ProcessHints  *repo.ProcessHints `json:"process_hints,omitempty"`
	}{
		Shape: classification.Shape,
	}
	if classification.Shape == repo.ShapeMonorepo {
		if strings.TrimSpace(string(classification.MonorepoStack)) == "" {
			classification.MonorepoStack = repo.MonorepoStackUnknown
		}
		payload.MonorepoStack = classification.MonorepoStack
	}
	if len(classification.ProcessHints.WorkspaceConfigFiles) > 0 ||
		len(classification.ProcessHints.WorkspaceManifestFiles) > 0 ||
		len(classification.ProcessHints.MultiPackageRoots) > 0 ||
		len(classification.ProcessHints.GradleSettingsFiles) > 0 ||
		len(classification.ProcessHints.GradleRootBuildFiles) > 0 ||
		len(classification.ProcessHints.BazelRepoMarkers) > 0 ||
		len(classification.ProcessHints.BazelPackageRoots) > 0 {
		payload.ProcessHints = &classification.ProcessHints
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"shape":"traditional"}`
	}
	return string(data)
}

func monorepoExecutionContextJSON(target state.WatchTarget, selectedSkill string) string {
	payload := struct {
		Stack               string             `json:"stack"`
		ImplementationSkill string             `json:"implementation_skill"`
		ProcessHints        *repo.ProcessHints `json:"process_hints,omitempty"`
		LocalServices       struct {
			Required              bool     `json:"required"`
			LaunchSkill           string   `json:"launch_skill"`
			Scope                 string   `json:"scope"`
			SupportedServiceTypes []string `json:"supported_service_types"`
			OutputFields          []string `json:"output_fields"`
		} `json:"local_services"`
	}{
		Stack:               normalizedMonorepoStack(target),
		ImplementationSkill: selectedSkill,
	}
	if len(target.Classification.ProcessHints.WorkspaceConfigFiles) > 0 ||
		len(target.Classification.ProcessHints.WorkspaceManifestFiles) > 0 ||
		len(target.Classification.ProcessHints.MultiPackageRoots) > 0 ||
		len(target.Classification.ProcessHints.BazelRepoMarkers) > 0 ||
		len(target.Classification.ProcessHints.BazelPackageRoots) > 0 {
		payload.ProcessHints = &target.Classification.ProcessHints
	}
	payload.LocalServices.Required = false
	payload.LocalServices.LaunchSkill = DockerComposeLaunch
	payload.LocalServices.Scope = "assigned_worktree"
	payload.LocalServices.SupportedServiceTypes = []string{"mysql", "mariadb", "postgres", "mongodb"}
	payload.LocalServices.OutputFields = []string{"status", "services", "mechanism", "commands", "connection", "cleanup", "artifacts", "notes"}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"stack":"%s","implementation_skill":"%s"}`, normalizedMonorepoStack(target), selectedSkill)
	}
	return string(data)
}

func BuildIssuePreflightPrompt(target state.WatchTarget, issue ghcli.Issue, session state.Session) string {
	baselineLine := fmt.Sprintf("Before implementing issue #%d, validate the repository baseline from the current `main`-derived worktree without making any file changes.", issue.Number)
	if strings.TrimSpace(session.ReusedRemoteBranch) != "" {
		baseBranch := strings.TrimSpace(session.BaseBranch)
		if baseBranch == "" {
			baseBranch = "main"
		}
		baselineLine = fmt.Sprintf("Before implementing issue #%d, validate the repository baseline from the current reused issue-branch worktree without making any file changes. This branch is being continued from `origin/%s` and compared against `%s`.", issue.Number, session.ReusedRemoteBranch, baseBranch)
	}
	lines := []string{
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Issue: #%d - %s", issue.Number, issue.Title),
		fmt.Sprintf("Issue URL: %s", issue.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		baselineLine,
		"Detect and run the appropriate build or equivalent verification command for this repository.",
		"Detect and run the existing test suite when tests are present; if no tests exist, state that clearly and continue.",
		"If the baseline build or tests fail, exit with a non-zero status and summarize the failing validation in the final output.",
		"If the baseline is healthy, exit successfully with a short summary of the commands you validated.",
		"Do not implement the issue, do not modify files, do not commit, and do not comment on GitHub during this preflight.",
	}
	return strings.Join(lines, "\n")
}

func fallbackPromptText(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func displayProviderName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Configured Coding Agent"
	}
	switch strings.ToLower(name) {
	case RuntimeClaude:
		return "Claude Code"
	case RuntimeCodex:
		return "Codex"
	case RuntimeGemini:
		return "Gemini CLI"
	}
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func BuildConflictResolutionPrompt(target state.WatchTarget, session state.Session, pr ghcli.PullRequest) string {
	return BuildConflictResolutionPromptForRuntime(RuntimeCodex, target, session, pr)
}

func BuildConflictResolutionPromptForRuntime(runtime string, target state.WatchTarget, session state.Session, pr ghcli.PullRequest) string {
	lines := []string{}
	if runtimeUsesInlineSkillHeader(runtime) {
		lines = append(lines, InlineSkillHeader(VigilanteConflictResolution))
	} else {
		lines = append(lines, fmt.Sprintf("Use the `%s` skill for this task.", VigilanteConflictResolution))
	}
	lines = append(lines,
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Issue: #%d - %s", session.IssueNumber, session.IssueTitle),
		fmt.Sprintf("Issue URL: %s", session.IssueURL),
		fmt.Sprintf("Pull Request: #%d", pr.Number),
		fmt.Sprintf("Pull Request URL: %s", pr.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		"Base branch: origin/main",
		"Resolve the current rebase conflicts in the assigned worktree, use `gh issue comment` for progress and failures, rerun `go test ./...` after conflict resolution if the rebase succeeds, and push the updated branch when finished.",
		"Keep the changes minimal and focused on getting the PR back to a merge-ready state.",
	)
	return strings.Join(lines, "\n")
}

func BuildCIRemediationPrompt(target state.WatchTarget, session state.Session, pr ghcli.PullRequest, checks []ghcli.StatusCheckRoll) string {
	return BuildCIRemediationPromptForRuntime(RuntimeCodex, target, session, pr, checks)
}

func BuildCIRemediationPromptForRuntime(runtime string, target state.WatchTarget, session state.Session, pr ghcli.PullRequest, checks []ghcli.StatusCheckRoll) string {
	lines := []string{}
	if runtimeUsesInlineSkillHeader(runtime) {
		lines = append(lines, InlineSkillHeader(IssueImplementationSkill(target)))
	} else {
		lines = append(lines, fmt.Sprintf("Use the `%s` skill for this task.", IssueImplementationSkill(target)))
	}
	lines = append(lines,
		fmt.Sprintf("Repository: %s", target.Repo),
		fmt.Sprintf("Local repository path: %s", target.Path),
		fmt.Sprintf("Issue: #%d - %s", session.IssueNumber, session.IssueTitle),
		fmt.Sprintf("Issue URL: %s", session.IssueURL),
		fmt.Sprintf("Pull Request: #%d", pr.Number),
		fmt.Sprintf("Pull Request URL: %s", pr.URL),
		fmt.Sprintf("Worktree path: %s", session.WorktreePath),
		fmt.Sprintf("Branch: %s", session.Branch),
		"CI remediation context: GitHub reported failing required checks for this existing PR.",
	)
	for _, check := range checks {
		name := strings.TrimSpace(check.Name)
		if name == "" {
			name = strings.TrimSpace(check.Context)
		}
		if name == "" {
			name = "unnamed-check"
		}
		lines = append(lines, fmt.Sprintf("Failing check: %s (state=%s conclusion=%s)", name, fallbackPromptText(strings.TrimSpace(check.State), "unknown"), fallbackPromptText(strings.TrimSpace(check.Conclusion), "unknown")))
	}
	lines = append(lines,
		"Investigate the failing CI checks, reproduce the problem locally when practical, and make the minimal code or configuration fix needed to get the PR green again.",
		"Use `gh issue comment` for progress updates and blockers, push any successful fix to the existing PR branch, and do not open a new pull request.",
		"If GitHub exposes a failing check summary or log URL during your investigation, use it. At minimum, work from the failing check identifiers listed above.",
		"If you cannot fix the failure safely, leave a concise GitHub comment explaining the blocker and exit with a non-zero status so Vigilante can stop and hand off to a human.",
		"Keep the changes minimal and focused on restoring CI for the existing pull request.",
	)
	return strings.Join(lines, "\n")
}

func runtimeUsesInlineSkillHeader(runtime string) bool {
	switch strings.TrimSpace(runtime) {
	case RuntimeClaude, RuntimeGemini:
		return true
	default:
		return false
	}
}

func repoSkillPath(name string) string {
	return filepath.Join(repoRoot(), "skills", name, "SKILL.md")
}

func repoSkillDir(name string) string {
	return filepath.Join(repoRoot(), "skills", name)
}

type skillSource interface {
	install(dst string) error
}

type dirSkillSource string

func (s dirSkillSource) install(dst string) error {
	return copyDir(string(s), dst)
}

type embeddedSkillSource struct {
	root string
	fs   fs.FS
}

func (s embeddedSkillSource) install(dst string) error {
	return copyFS(s.fs, s.root, dst)
}

func resolveSkillSource(name string) (skillSource, error) {
	sourceDir := repoSkillDir(name)
	if _, err := os.Stat(filepath.Join(sourceDir, "SKILL.md")); err == nil {
		return dirSkillSource(sourceDir), nil
	}

	root := filepath.ToSlash(filepath.Join("skills", name))
	if _, err := fs.Stat(skillassets.Skills, pathJoin(root, "SKILL.md")); err != nil {
		return nil, err
	}
	return embeddedSkillSource{root: root, fs: skillassets.Skills}, nil
}

func repoRoot() string {
	exe, err := os.Executable()
	if err == nil {
		if root, ok := findRepoRoot(filepath.Dir(exe)); ok {
			return root
		}
	}

	wd, err := os.Getwd()
	if err == nil {
		if root, ok := findRepoRoot(wd); ok {
			return root
		}
	}

	return "."
}

func findRepoRoot(start string) (string, bool) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "skills")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func copyDir(src string, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFS(source fs.FS, root string, dst string) error {
	return fs.WalkDir(source, root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(path))
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFSFile(source, path, target, info.Mode())
	})
}

func copyFSFile(source fs.FS, src string, dst string, mode os.FileMode) error {
	in, err := source.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func copyFile(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func pathJoin(parts ...string) string {
	return strings.Join(parts, "/")
}
