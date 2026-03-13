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
	ShapeTraditional Shape = "traditional"
	ShapeMonorepo    Shape = "monorepo"
)

type ProcessHints struct {
	WorkspaceConfigFiles   []string `json:"workspace_config_files,omitempty"`
	WorkspaceManifestFiles []string `json:"workspace_manifest_files,omitempty"`
	MultiPackageRoots      []string `json:"multi_package_roots,omitempty"`
}

type Classification struct {
	Shape        Shape        `json:"shape"`
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

	return Info{
		Path:           absPath,
		Repo:           repo,
		Branch:         branch,
		Classification: Classify(absPath),
	}, nil
}

func Classify(path string) Classification {
	classification := Classification{Shape: ShapeTraditional}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
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
	if len(classification.ProcessHints.WorkspaceConfigFiles) > 0 ||
		len(classification.ProcessHints.WorkspaceManifestFiles) > 0 ||
		len(classification.ProcessHints.MultiPackageRoots) >= 2 {
		classification.Shape = ShapeMonorepo
	}
	slices.Sort(classification.ProcessHints.WorkspaceConfigFiles)
	slices.Sort(classification.ProcessHints.WorkspaceManifestFiles)
	slices.Sort(classification.ProcessHints.MultiPackageRoots)
	return classification
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
