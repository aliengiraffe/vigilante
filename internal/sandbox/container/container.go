// Package container manages Docker container lifecycle for sandbox sessions.
//
// It shells out to the Docker CLI rather than importing the Docker Go SDK,
// keeping the dependency footprint consistent with the rest of Vigilante.
package container

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

// Config describes the desired sandbox container.
type Config struct {
	// Image is the Docker image to use for the sandbox container.
	Image string

	// Name is the container name. Should be unique per session.
	Name string

	// WorktreePath is the host path to bind-mount as the repo checkout.
	WorktreePath string

	// ContainerRepoPath is the mount destination inside the container.
	ContainerRepoPath string

	// SSHKeyPath is the host path to the ephemeral SSH private key.
	SSHKeyPath string

	// EnvVars are environment variables injected into the container.
	EnvVars map[string]string

	// MemoryLimit (e.g. "8g") constrains container memory.
	MemoryLimit string

	// CPUs limits the number of CPUs available to the container.
	CPUs string

	// ProxyPort is the host port the reverse proxy listens on.
	ProxyPort int

	// EnableDinD enables Docker-in-Docker capability via --privileged.
	EnableDinD bool

	// Mounts are additional bind mounts to add to the container.
	Mounts []Mount
}

// Mount describes an additional bind mount for the sandbox container.
type Mount struct {
	Source   string
	Target   string
	ReadOnly bool
}

// ContainerRepoPathDefault is the default mount point for the repo inside the container.
const ContainerRepoPathDefault = "/workspace"

// Create provisions a new container without starting it.
func Create(ctx context.Context, runner environment.Runner, cfg Config) (string, error) {
	repoPath := cfg.ContainerRepoPath
	if repoPath == "" {
		repoPath = ContainerRepoPathDefault
	}

	args := []string{
		"create",
		"--name", cfg.Name,
		"-v", cfg.WorktreePath + ":" + repoPath,
		"-w", repoPath,
	}

	if cfg.SSHKeyPath != "" {
		args = append(args, "-v", cfg.SSHKeyPath+":/etc/vigilante/ssh/id_ed25519:ro")
	}

	for _, mount := range cfg.Mounts {
		if strings.TrimSpace(mount.Source) == "" || strings.TrimSpace(mount.Target) == "" {
			continue
		}
		spec := mount.Source + ":" + mount.Target
		if mount.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}

	for k, v := range cfg.EnvVars {
		args = append(args, "-e", k+"="+v)
	}

	if cfg.MemoryLimit != "" {
		args = append(args, "--memory", cfg.MemoryLimit)
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}

	if cfg.ProxyPort > 0 {
		// Map the proxy port from host to container so the gh mirror
		// binary can reach the reverse proxy.
		mapping := fmt.Sprintf("127.0.0.1:%d:%d", cfg.ProxyPort, cfg.ProxyPort)
		args = append(args, "-p", mapping)
	}

	if cfg.EnableDinD {
		args = append(args, "--privileged")
	}

	args = append(args, cfg.Image)

	out, err := runner.Run(ctx, "", "docker", args...)
	if err != nil {
		return "", fmt.Errorf("docker create: %w\n%s", err, out)
	}
	return strings.TrimSpace(out), nil
}

// Start starts a previously created container.
func Start(ctx context.Context, runner environment.Runner, nameOrID string) error {
	out, err := runner.Run(ctx, "", "docker", "start", nameOrID)
	if err != nil {
		return fmt.Errorf("docker start %s: %w\n%s", nameOrID, err, out)
	}
	return nil
}

// Stop gracefully stops a running container with a timeout in seconds.
func Stop(ctx context.Context, runner environment.Runner, nameOrID string, timeoutSec int) error {
	out, err := runner.Run(ctx, "", "docker", "stop", "-t", fmt.Sprintf("%d", timeoutSec), nameOrID)
	if err != nil {
		return fmt.Errorf("docker stop %s: %w\n%s", nameOrID, err, out)
	}
	return nil
}

// Remove removes a container and its anonymous volumes.
func Remove(ctx context.Context, runner environment.Runner, nameOrID string) error {
	out, err := runner.Run(ctx, "", "docker", "rm", "--volumes", nameOrID)
	if err != nil {
		return fmt.Errorf("docker rm %s: %w\n%s", nameOrID, err, out)
	}
	return nil
}

// Exec runs a command inside a running container and returns its output.
func Exec(ctx context.Context, runner environment.Runner, nameOrID string, cmd []string) (string, error) {
	args := append([]string{"exec", nameOrID}, cmd...)
	return runner.Run(ctx, "", "docker", args...)
}

// IsRunning checks whether a container is currently running.
func IsRunning(ctx context.Context, runner environment.Runner, nameOrID string) (bool, error) {
	out, err := runner.Run(ctx, "", "docker", "inspect", "--format", "{{.State.Running}}", nameOrID)
	if err != nil {
		if strings.Contains(err.Error(), "No such object") || strings.Contains(out, "No such object") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// Exists checks whether a container exists (running or stopped).
func Exists(ctx context.Context, runner environment.Runner, nameOrID string) (bool, error) {
	out, err := runner.Run(ctx, "", "docker", "inspect", "--format", "{{.Id}}", nameOrID)
	if err != nil {
		if strings.Contains(err.Error(), "No such object") || strings.Contains(out, "No such object") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// ExtractArtifacts runs git commands inside the container to capture branch
// state and returns the output as a combined summary string.
func ExtractArtifacts(ctx context.Context, runner environment.Runner, nameOrID string) (string, error) {
	var parts []string
	for _, cmd := range [][]string{
		{"git", "log", "--oneline", "-20"},
		{"git", "diff", "--stat"},
		{"git", "branch", "--show-current"},
	} {
		out, err := Exec(ctx, runner, nameOrID, cmd)
		if err != nil {
			parts = append(parts, fmt.Sprintf("[%s failed: %v]", cmd[0], err))
			continue
		}
		parts = append(parts, strings.TrimSpace(out))
	}
	return strings.Join(parts, "\n---\n"), nil
}

// ListSandboxContainers returns container names that match the Vigilante
// sandbox naming convention.
func ListSandboxContainers(ctx context.Context, runner environment.Runner) ([]string, error) {
	out, err := runner.Run(ctx, "", "docker", "ps", "-a", "--filter", "name=vigilante-sandbox-", "--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("list sandbox containers: %w", err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// InspectStatus returns the container state as a JSON-decoded map.
func InspectStatus(ctx context.Context, runner environment.Runner, nameOrID string) (map[string]any, error) {
	out, err := runner.Run(ctx, "", "docker", "inspect", nameOrID)
	if err != nil {
		return nil, fmt.Errorf("docker inspect %s: %w", nameOrID, err)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		return nil, fmt.Errorf("parse docker inspect output: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("docker inspect returned empty result for %s", nameOrID)
	}
	return results[0], nil
}
