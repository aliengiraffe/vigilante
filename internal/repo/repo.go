package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

const (
	ShapeTraditional = "traditional"
	ShapeMonorepo    = "monorepo"
)

type ProcessHints struct {
	WorkspaceFiles []string `json:"workspace_files,omitempty"`
	WorkspaceGlobs []string `json:"workspace_globs,omitempty"`
	ProjectRoots   []string `json:"project_roots,omitempty"`
}

type Classification struct {
	Shape        string       `json:"shape"`
	ProcessHints ProcessHints `json:"process_hints,omitempty"`
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

	branch := "main"
	if remoteHead, err := runner.Run(ctx, absPath, "git", "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		branch = strings.TrimPrefix(strings.TrimSpace(remoteHead), "origin/")
	} else if current, err := runner.Run(ctx, absPath, "git", "branch", "--show-current"); err == nil && strings.TrimSpace(current) != "" {
		branch = strings.TrimSpace(current)
	}

	classification, err := Classify(absPath)
	if err != nil {
		return Info{}, err
	}

	return Info{
		Path:           absPath,
		Repo:           repo,
		Branch:         branch,
		Classification: classification,
	}, nil
}

func Classify(root string) (Classification, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Classification{}, err
	}

	classification := Classification{
		Shape:        ShapeTraditional,
		ProcessHints: ProcessHints{},
	}
	classification.ProcessHints.WorkspaceFiles = detectWorkspaceFiles(absRoot)
	classification.ProcessHints.WorkspaceGlobs = detectWorkspaceGlobs(absRoot)
	classification.ProcessHints.ProjectRoots = detectProjectRoots(absRoot)

	if len(classification.ProcessHints.WorkspaceFiles) > 0 || len(classification.ProcessHints.WorkspaceGlobs) > 0 || len(classification.ProcessHints.ProjectRoots) >= 2 {
		classification.Shape = ShapeMonorepo
	}
	return classification, nil
}

func detectWorkspaceFiles(root string) []string {
	candidates := []string{
		"pnpm-workspace.yaml",
		"turbo.json",
		"nx.json",
		"lerna.json",
		"rush.json",
		"go.work",
	}
	found := make([]string, 0, len(candidates)+1)
	for _, name := range candidates {
		if fileExists(filepath.Join(root, name)) {
			found = append(found, name)
		}
	}
	if packageJSONDeclaresWorkspaces(filepath.Join(root, "package.json")) {
		found = append(found, "package.json#workspaces")
	}
	sort.Strings(found)
	return found
}

func detectWorkspaceGlobs(root string) []string {
	return readPackageJSONWorkspaces(filepath.Join(root, "package.json"))
}

func detectProjectRoots(root string) []string {
	candidates := []string{"apps", "packages", "services"}
	projects := []string{}
	for _, dir := range candidates {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			projects = append(projects, filepath.ToSlash(filepath.Join(dir, entry.Name())))
		}
	}
	return compactSorted(projects)
}

type packageJSONWorkspaces struct {
	Workspaces json.RawMessage `json:"workspaces"`
}

func packageJSONDeclaresWorkspaces(path string) bool {
	return len(readPackageJSONWorkspaces(path)) > 0
}

func readPackageJSONWorkspaces(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg packageJSONWorkspaces
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}
	if len(pkg.Workspaces) == 0 {
		return nil
	}

	var globs []string
	if err := json.Unmarshal(pkg.Workspaces, &globs); err == nil {
		return compactSorted(globs)
	}

	var object struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(pkg.Workspaces, &object); err == nil {
		return compactSorted(object.Packages)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func compactSorted(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
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

func normalizeGitHubPath(path string) (string, error) {
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid GitHub repo path %q", path)
	}
	return parts[0] + "/" + parts[1], nil
}
