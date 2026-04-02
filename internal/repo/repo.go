package repo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

type Shape string

const (
	ShapeTraditional        Shape = "traditional"
	ShapeMonorepo           Shape = "monorepo"
	ShapeGradleMultiProject Shape = "gradle_multi_project"
)

type MonorepoStack string

const (
	MonorepoStackUnknown   MonorepoStack = "unknown"
	MonorepoStackTurborepo MonorepoStack = "turborepo"
	MonorepoStackNx        MonorepoStack = "nx"
	MonorepoStackRush      MonorepoStack = "rush"
	MonorepoStackBazel     MonorepoStack = "bazel"
	MonorepoStackGradle    MonorepoStack = "gradle"
)

type TechStack string

const (
	TechStackNodeJS        TechStack = "nodejs"
	TechStackGo            TechStack = "go"
	TechStackGitHubActions TechStack = "github-actions"
	TechStackDocker        TechStack = "docker"
	TechStackKubernetes    TechStack = "kubernetes"
	TechStackPython        TechStack = "python"
	TechStackDotNet        TechStack = "dotnet"
	TechStackJVM           TechStack = "java_kotlin"
)

type ProcessHints struct {
	WorkspaceConfigFiles   []string `json:"workspace_config_files,omitempty"`
	WorkspaceManifestFiles []string `json:"workspace_manifest_files,omitempty"`
	MultiPackageRoots      []string `json:"multi_package_roots,omitempty"`
	GradleSettingsFiles    []string `json:"gradle_settings_files,omitempty"`
	GradleRootBuildFiles   []string `json:"gradle_root_build_files,omitempty"`
	BazelRepoMarkers       []string `json:"bazel_repo_markers,omitempty"`
	BazelPackageRoots      []string `json:"bazel_package_roots,omitempty"`
	NodePackageManagers    []string `json:"node_package_managers,omitempty"`
	NodeLockFiles          []string `json:"node_lock_files,omitempty"`
	TypeScriptConfigs      []string `json:"typescript_configs,omitempty"`
	DockerFiles            []string `json:"docker_files,omitempty"`
	PythonSignals          []string `json:"python_signals,omitempty"`
}

type Classification struct {
	Shape         Shape         `json:"shape"`
	MonorepoStack MonorepoStack `json:"monorepo_stack,omitempty"`
	TechStacks    []TechStack   `json:"tech_stacks,omitempty"`
	ProcessHints  ProcessHints  `json:"process_hints,omitempty"`
}

type Info struct {
	Path           string
	Repo           string
	Branch         string
	Classification Classification
}

func Discover(ctx context.Context, runner environment.Runner, path string) (Info, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Info{}, err
	}
	if _, err := runner.Run(ctx, absPath, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return Info{}, fmt.Errorf("%s is not a git repository: %w", absPath, err)
	}

	remoteURL, err := runner.Run(ctx, absPath, "git", "remote", "get-url", "origin")
	if err != nil {
		return Info{}, fmt.Errorf("origin remote not found: %w", err)
	}
	repo, err := ParseGitHubRepo(strings.TrimSpace(remoteURL))
	if err != nil {
		return Info{}, err
	}

	branch, err := ResolveDefaultBranch(ctx, runner, absPath, "")
	if err != nil {
		return Info{}, err
	}

	return Info{
		Path:           absPath,
		Repo:           repo,
		Branch:         branch,
		Classification: Classify(absPath),
	}, nil
}

func ResolveBranch(ctx context.Context, runner environment.Runner, repoPath string, branchMode string, branch string) (string, error) {
	switch strings.TrimSpace(branchMode) {
	case "", "pinned":
		branch := strings.TrimSpace(branch)
		if branch == "" {
			return "", errors.New("pinned branch is not configured")
		}
		exists, err := remoteBranchExists(ctx, runner, repoPath, branch)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("pinned base branch %q was not found on origin", branch)
		}
		return branch, nil
	case "auto":
		return ResolveDefaultBranch(ctx, runner, repoPath, branch)
	default:
		return "", fmt.Errorf("unsupported branch mode %q", branchMode)
	}
}

func ResolveDefaultBranch(ctx context.Context, runner environment.Runner, repoPath string, fallback string) (string, error) {
	if branch, err := remoteHEADBranch(ctx, runner, repoPath); err == nil && branch != "" {
		return branch, nil
	}
	if branch, err := localRemoteHEADBranch(ctx, runner, repoPath); err == nil && branch != "" {
		return branch, nil
	}
	fallback = strings.TrimSpace(fallback)
	if fallback != "" {
		return fallback, nil
	}
	if current, err := runner.Run(ctx, repoPath, "git", "branch", "--show-current"); err == nil && strings.TrimSpace(current) != "" {
		return strings.TrimSpace(current), nil
	}
	return "", errors.New("could not resolve repository default branch")
}

func remoteHEADBranch(ctx context.Context, runner environment.Runner, repoPath string) (string, error) {
	output, err := runner.Run(ctx, repoPath, "git", "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", errors.New("origin HEAD did not report a branch")
}

func localRemoteHEADBranch(ctx context.Context, runner environment.Runner, repoPath string) (string, error) {
	output, err := runner.Run(ctx, repoPath, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(output), "origin/"), nil
}

func remoteBranchExists(ctx context.Context, runner environment.Runner, repoPath string, branch string) (bool, error) {
	_, err := runner.Run(ctx, repoPath, "git", "ls-remote", "--exit-code", "--heads", "origin", branch)
	if err == nil {
		return true, nil
	}
	if strings.Contains(err.Error(), "exit status 1") || strings.Contains(err.Error(), "exit status 2") {
		return false, nil
	}
	return false, err
}

func Classify(path string) Classification {
	classification := Classification{Shape: ShapeTraditional}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	for _, name := range []string{"settings.gradle", "settings.gradle.kts"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.ProcessHints.GradleSettingsFiles = append(classification.ProcessHints.GradleSettingsFiles, name)
		}
	}
	for _, name := range []string{"build.gradle", "build.gradle.kts"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.ProcessHints.GradleRootBuildFiles = append(classification.ProcessHints.GradleRootBuildFiles, name)
		}
	}
	for _, name := range []string{"pnpm-workspace.yaml", "turbo.json", "nx.json", "lerna.json", "rush.json", "go.work"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.ProcessHints.WorkspaceConfigFiles = append(classification.ProcessHints.WorkspaceConfigFiles, name)
		}
	}
	if packageJSONHasWorkspaces(filepath.Join(absPath, "package.json")) {
		classification.ProcessHints.WorkspaceManifestFiles = append(classification.ProcessHints.WorkspaceManifestFiles, "package.json")
	}
	if cargoTomlHasWorkspace(filepath.Join(absPath, "Cargo.toml")) {
		classification.ProcessHints.WorkspaceManifestFiles = append(classification.ProcessHints.WorkspaceManifestFiles, "Cargo.toml")
	}
	for _, root := range []string{"apps", "packages", "services", "libs", "modules"} {
		if hasChildDirectories(filepath.Join(absPath, root)) {
			classification.ProcessHints.MultiPackageRoots = append(classification.ProcessHints.MultiPackageRoots, root)
		}
	}
	for _, name := range []string{"MODULE.bazel", "WORKSPACE", "WORKSPACE.bazel"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.ProcessHints.BazelRepoMarkers = append(classification.ProcessHints.BazelRepoMarkers, name)
		}
	}
	for _, root := range []string{"apps", "packages", "services", "libs", "modules"} {
		if hasBazelPackageChildren(filepath.Join(absPath, root)) {
			classification.ProcessHints.BazelPackageRoots = append(classification.ProcessHints.BazelPackageRoots, root)
		}
	}
	if len(classification.ProcessHints.WorkspaceConfigFiles) > 0 ||
		len(classification.ProcessHints.WorkspaceManifestFiles) > 0 ||
		len(classification.ProcessHints.MultiPackageRoots) >= 2 ||
		(len(classification.ProcessHints.BazelRepoMarkers) > 0 && len(classification.ProcessHints.BazelPackageRoots) > 0) {
		classification.Shape = ShapeMonorepo
		classification.MonorepoStack = detectMonorepoStack(absPath)
	}
	if isGradleMultiProject(absPath, classification.ProcessHints.GradleSettingsFiles) {
		classification.Shape = ShapeGradleMultiProject
	}
	detectNodeJSTechStack(absPath, &classification)
	detectGoTechStack(absPath, &classification)
	detectGitHubActionsTechStack(absPath, &classification)
	detectDockerTechStack(absPath, &classification)
	detectKubernetesTechStack(absPath, &classification)
	detectPythonTechStack(absPath, &classification)
	detectDotNetTechStack(absPath, &classification)
	detectJVMTechStack(absPath, &classification)

	slices.Sort(classification.ProcessHints.GradleSettingsFiles)
	slices.Sort(classification.ProcessHints.GradleRootBuildFiles)
	slices.Sort(classification.ProcessHints.WorkspaceConfigFiles)
	slices.Sort(classification.ProcessHints.WorkspaceManifestFiles)
	slices.Sort(classification.ProcessHints.MultiPackageRoots)
	slices.Sort(classification.ProcessHints.BazelRepoMarkers)
	slices.Sort(classification.ProcessHints.BazelPackageRoots)
	slices.Sort(classification.ProcessHints.NodePackageManagers)
	slices.Sort(classification.ProcessHints.NodeLockFiles)
	slices.Sort(classification.ProcessHints.TypeScriptConfigs)
	slices.Sort(classification.ProcessHints.DockerFiles)
	slices.Sort(classification.ProcessHints.PythonSignals)
	return classification
}

func detectMonorepoStack(absPath string) MonorepoStack {
	switch {
	case fileExists(filepath.Join(absPath, "turbo.json")):
		return MonorepoStackTurborepo
	case fileExists(filepath.Join(absPath, "nx.json")):
		return MonorepoStackNx
	case fileExists(filepath.Join(absPath, "rush.json")):
		return MonorepoStackRush
	case fileExists(filepath.Join(absPath, "WORKSPACE")) ||
		fileExists(filepath.Join(absPath, "WORKSPACE.bazel")) ||
		fileExists(filepath.Join(absPath, "MODULE.bazel")):
		return MonorepoStackBazel
	case fileExists(filepath.Join(absPath, "settings.gradle")) ||
		fileExists(filepath.Join(absPath, "settings.gradle.kts")):
		return MonorepoStackGradle
	default:
		return MonorepoStackUnknown
	}
}

func ParseGitHubRepo(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", errors.New("empty remote URL")
	}

	if strings.HasPrefix(remote, "git@github.com:") {
		path := strings.TrimPrefix(remote, "git@github.com:")
		return normalizeGitHubPath(path)
	}

	if strings.HasPrefix(remote, "ssh://") || strings.HasPrefix(remote, "https://") || strings.HasPrefix(remote, "http://") {
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(parsed.Host, "github.com") {
			return "", fmt.Errorf("unsupported remote host %q", parsed.Host)
		}
		return normalizeGitHubPath(strings.TrimPrefix(parsed.Path, "/"))
	}

	return "", fmt.Errorf("unsupported remote format %q", remote)
}

func RewriteGitHubRemote(remote string, repoSlug string) (string, error) {
	repoSlug = strings.TrimSpace(repoSlug)
	if repoSlug == "" {
		return "", errors.New("empty GitHub repo slug")
	}
	if _, err := normalizeGitHubPath(repoSlug); err != nil {
		return "", err
	}

	remote = strings.TrimSpace(remote)
	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		return "git@github.com:" + repoSlug + ".git", nil
	case strings.HasPrefix(remote, "ssh://"), strings.HasPrefix(remote, "https://"), strings.HasPrefix(remote, "http://"):
		parsed, err := url.Parse(remote)
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(parsed.Host, "github.com") {
			return "", fmt.Errorf("unsupported remote host %q", parsed.Host)
		}
		parsed.Path = "/" + repoSlug + ".git"
		return parsed.String(), nil
	default:
		return "", fmt.Errorf("unsupported remote format %q", remote)
	}
}

func normalizeGitHubPath(path string) (string, error) {
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid GitHub repo path %q", path)
	}
	return parts[0] + "/" + parts[1], nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func hasChildDirectories(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true
		}
	}
	return false
}

func hasBazelPackageChildren(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		child := filepath.Join(path, entry.Name())
		if fileExists(filepath.Join(child, "BUILD")) || fileExists(filepath.Join(child, "BUILD.bazel")) {
			return true
		}
	}
	return false
}

func packageJSONHasWorkspaces(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), `"workspaces"`)
}

func cargoTomlHasWorkspace(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "[workspace]")
}

func detectNodeJSTechStack(absPath string, classification *Classification) {
	if !fileExists(filepath.Join(absPath, "package.json")) {
		return
	}

	for _, lockFile := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		if fileExists(filepath.Join(absPath, lockFile)) {
			classification.ProcessHints.NodeLockFiles = append(classification.ProcessHints.NodeLockFiles, lockFile)
			if !slices.Contains(classification.ProcessHints.NodePackageManagers, "npm") {
				classification.ProcessHints.NodePackageManagers = append(classification.ProcessHints.NodePackageManagers, "npm")
			}
		}
	}
	if fileExists(filepath.Join(absPath, "yarn.lock")) {
		classification.ProcessHints.NodeLockFiles = append(classification.ProcessHints.NodeLockFiles, "yarn.lock")
		classification.ProcessHints.NodePackageManagers = append(classification.ProcessHints.NodePackageManagers, "yarn")
	}
	if fileExists(filepath.Join(absPath, "pnpm-lock.yaml")) {
		classification.ProcessHints.NodeLockFiles = append(classification.ProcessHints.NodeLockFiles, "pnpm-lock.yaml")
		classification.ProcessHints.NodePackageManagers = append(classification.ProcessHints.NodePackageManagers, "pnpm")
	}
	if len(classification.ProcessHints.NodePackageManagers) == 0 {
		classification.ProcessHints.NodePackageManagers = append(classification.ProcessHints.NodePackageManagers, "npm")
	}

	for _, tsConfig := range []string{"tsconfig.json", "tsconfig.build.json"} {
		if fileExists(filepath.Join(absPath, tsConfig)) {
			classification.ProcessHints.TypeScriptConfigs = append(classification.ProcessHints.TypeScriptConfigs, tsConfig)
		}
	}

	classification.TechStacks = append(classification.TechStacks, TechStackNodeJS)
}

func detectGoTechStack(absPath string, classification *Classification) {
	if !fileExists(filepath.Join(absPath, "go.mod")) {
		return
	}
	classification.TechStacks = append(classification.TechStacks, TechStackGo)
}

func detectGitHubActionsTechStack(absPath string, classification *Classification) {
	workflowsDir := filepath.Join(absPath, ".github", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
			classification.TechStacks = append(classification.TechStacks, TechStackGitHubActions)
			return
		}
	}
}

func detectDockerTechStack(absPath string, classification *Classification) {
	for _, name := range []string{"Dockerfile", "Dockerfile.dev", "Dockerfile.prod"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.ProcessHints.DockerFiles = append(classification.ProcessHints.DockerFiles, name)
		}
	}
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.ProcessHints.DockerFiles = append(classification.ProcessHints.DockerFiles, name)
		}
	}
	if fileExists(filepath.Join(absPath, ".dockerignore")) {
		classification.ProcessHints.DockerFiles = append(classification.ProcessHints.DockerFiles, ".dockerignore")
	}
	if len(classification.ProcessHints.DockerFiles) == 0 {
		return
	}
	classification.TechStacks = append(classification.TechStacks, TechStackDocker)
}

func detectKubernetesTechStack(absPath string, classification *Classification) {
	// Direct Kubernetes manifest signals at root level.
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "skaffold.yaml"} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.TechStacks = append(classification.TechStacks, TechStackKubernetes)
			return
		}
	}
	// Helm chart signal.
	if fileExists(filepath.Join(absPath, "Chart.yaml")) {
		classification.TechStacks = append(classification.TechStacks, TechStackKubernetes)
		return
	}
	// Common manifest directories containing Kubernetes resource YAML.
	for _, dir := range []string{"k8s", "kubernetes", "manifests", "deploy", "base", "overlays", "helm"} {
		dirPath := filepath.Join(absPath, dir)
		if hasKubernetesYAML(dirPath) {
			classification.TechStacks = append(classification.TechStacks, TechStackKubernetes)
			return
		}
	}
}

func detectJVMTechStack(absPath string, classification *Classification) {
	for _, name := range []string{
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"settings.gradle",
		"settings.gradle.kts",
	} {
		if fileExists(filepath.Join(absPath, name)) {
			classification.TechStacks = append(classification.TechStacks, TechStackJVM)
			return
		}
	}

	for _, dir := range []string{
		"src/main/java",
		"src/test/java",
		"src/main/kotlin",
		"src/test/kotlin",
	} {
		if hasChildDirectories(filepath.Join(absPath, dir)) {
			classification.TechStacks = append(classification.TechStacks, TechStackJVM)
			return
		}
	}
}

func hasKubernetesYAML(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			content := string(data)
			if strings.Contains(content, "apiVersion:") && strings.Contains(content, "kind:") {
				return true
			}
		}
	}
	return false
}

func detectPythonTechStack(absPath string, classification *Classification) {
	for _, signal := range []string{"pyproject.toml", "requirements.txt", "requirements-dev.txt", "requirements-test.txt", "setup.py", "setup.cfg", "tox.ini", "noxfile.py"} {
		if fileExists(filepath.Join(absPath, signal)) {
			classification.ProcessHints.PythonSignals = append(classification.ProcessHints.PythonSignals, signal)
		}
	}
	if hasPythonPackageLayout(absPath) {
		classification.ProcessHints.PythonSignals = append(classification.ProcessHints.PythonSignals, "python_package_layout")
	}
	if len(classification.ProcessHints.PythonSignals) == 0 {
		return
	}
	classification.TechStacks = append(classification.TechStacks, TechStackPython)
}

func detectDotNetTechStack(absPath string, classification *Classification) {
	found := false
	err := filepath.WalkDir(absPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "bin", "obj":
				if path != absPath {
					return filepath.SkipDir
				}
			}
			return nil
		}

		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".sln"),
			strings.HasSuffix(name, ".csproj"),
			strings.HasSuffix(name, ".fsproj"),
			name == "Directory.Build.props",
			name == "Directory.Build.targets":
			found = true
			return errStopWalk
		default:
			return nil
		}
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return
	}
	if found {
		classification.TechStacks = append(classification.TechStacks, TechStackDotNet)
	}
}

var errStopWalk = errors.New("stop walk")

func isGradleMultiProject(path string, settingsFiles []string) bool {
	for _, name := range settingsFiles {
		data, err := os.ReadFile(filepath.Join(path, name))
		if err != nil {
			continue
		}
		text := string(data)
		if strings.Contains(text, "include(") || strings.Contains(text, "include ") || strings.Contains(text, "includeFlat(") || strings.Contains(text, "includeFlat ") {
			return true
		}
	}
	return false
}

func hasPythonPackageLayout(path string) bool {
	for _, root := range []string{"src", "tests"} {
		entries, err := os.ReadDir(filepath.Join(path, root))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".py") {
				return true
			}
			if entry.IsDir() && directoryContainsPythonFiles(filepath.Join(path, root, entry.Name())) {
				return true
			}
		}
	}
	return false
}

func directoryContainsPythonFiles(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if directoryContainsPythonFiles(filepath.Join(path, entry.Name())) {
				return true
			}
			continue
		}
		if strings.HasSuffix(entry.Name(), ".py") {
			return true
		}
	}
	return false
}
