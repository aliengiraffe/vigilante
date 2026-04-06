package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// DockerClient abstracts the Docker Engine API operations needed by sandbox.
type DockerClient interface {
	CreateContainer(ctx context.Context, cfg ContainerConfig) (containerID string, err error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RemoveContainer(ctx context.Context, containerID string) error
	ExecInContainer(ctx context.Context, containerID string, cmd []string) (string, error)
	ListVigilanteContainers(ctx context.Context) ([]string, error)
}

// ContainerConfig describes how to create a sandbox container.
type ContainerConfig struct {
	Image      string
	SessionID  string
	Repository string
	Provider   string
	Token      string
	ProxyURL   string
	SSHDir     string
	Worktree   string
	Limits     ResourceLimits
}

// EngineClient talks to the Docker Engine API over a Unix socket.
type EngineClient struct {
	socketPath string
	client     *http.Client
}

// NewEngineClient creates a Docker client using the given socket path.
// Pass an empty string to use the default /var/run/docker.sock.
func NewEngineClient(socketPath string) *EngineClient {
	if socketPath == "" {
		socketPath = "/var/run/docker.sock"
	}
	return &EngineClient{
		socketPath: socketPath,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		},
	}
}

// CreateContainer creates a new container via the Docker Engine API.
func (c *EngineClient) CreateContainer(ctx context.Context, cfg ContainerConfig) (string, error) {
	env := []string{
		"VIGILANTE_SESSION_ID=" + cfg.SessionID,
		"VIGILANTE_SANDBOX_TOKEN=" + cfg.Token,
		"VIGILANTE_PROXY_URL=" + cfg.ProxyURL,
	}

	binds := []string{
		cfg.SSHDir + ":/etc/vigilante/ssh:ro",
	}
	if cfg.Worktree != "" {
		binds = append(binds, cfg.Worktree+":/workspace")
	}

	body := map[string]any{
		"Image": cfg.Image,
		"Env":   env,
		"Labels": map[string]string{
			"vigilante.sandbox":    "true",
			"vigilante.session-id": cfg.SessionID,
		},
		"HostConfig": map[string]any{
			"Binds":      binds,
			"Privileged": true, // for DinD
			"Memory":     parseMemoryBytes(cfg.Limits.Memory),
			"NanoCpus":   int64(cfg.Limits.CPUs) * 1_000_000_000,
		},
		"WorkingDir": "/workspace",
	}

	data, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal container config: %w", err)
	}

	resp, err := c.doRequest(ctx, "POST", "/containers/create?name=vigilante-"+cfg.SessionID, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", readAPIError(resp)
	}

	var result struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode create response: %w", err)
	}
	return result.ID, nil
}

// StartContainer starts a previously created container.
func (c *EngineClient) StartContainer(ctx context.Context, containerID string) error {
	resp, err := c.doRequest(ctx, "POST", "/containers/"+containerID+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return readAPIError(resp)
	}
	return nil
}

// StopContainer sends SIGTERM to the container with a 10s timeout.
func (c *EngineClient) StopContainer(ctx context.Context, containerID string) error {
	resp, err := c.doRequest(ctx, "POST", "/containers/"+containerID+"/stop?t=10", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return readAPIError(resp)
	}
	return nil
}

// RemoveContainer removes a container and its anonymous volumes.
func (c *EngineClient) RemoveContainer(ctx context.Context, containerID string) error {
	resp, err := c.doRequest(ctx, "DELETE", "/containers/"+containerID+"?v=true&force=true", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return readAPIError(resp)
	}
	return nil
}

// ExecInContainer runs a command inside a running container and returns the output.
func (c *EngineClient) ExecInContainer(ctx context.Context, containerID string, cmd []string) (string, error) {
	execBody := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
	}
	data, err := json.Marshal(execBody)
	if err != nil {
		return "", err
	}

	resp, err := c.doRequest(ctx, "POST", "/containers/"+containerID+"/exec", data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return "", readAPIError(resp)
	}

	var execResult struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&execResult); err != nil {
		return "", err
	}

	startBody, _ := json.Marshal(map[string]any{"Detach": false})
	startResp, err := c.doRequest(ctx, "POST", "/exec/"+execResult.ID+"/start", startBody)
	if err != nil {
		return "", err
	}
	defer startResp.Body.Close()

	output, err := io.ReadAll(startResp.Body)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// ListVigilanteContainers returns IDs of containers with the vigilante.sandbox label.
func (c *EngineClient) ListVigilanteContainers(ctx context.Context) ([]string, error) {
	filterJSON, _ := json.Marshal(map[string][]string{
		"label": {"vigilante.sandbox=true"},
	})
	resp, err := c.doRequest(ctx, "GET", "/containers/json?all=true&filters="+string(filterJSON), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var containers []struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}

	ids := make([]string, len(containers))
	for i, c := range containers {
		ids[i] = c.ID
	}
	return ids, nil
}

func (c *EngineClient) doRequest(ctx context.Context, method string, path string, body []byte) (*http.Response, error) {
	url := "http://localhost/v1.43" + path
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("docker api: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.client.Do(req)
}

func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("docker api %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func parseMemoryBytes(mem string) int64 {
	mem = strings.TrimSpace(strings.ToLower(mem))
	if mem == "" {
		return 0
	}
	var multiplier int64 = 1
	if strings.HasSuffix(mem, "g") {
		multiplier = 1024 * 1024 * 1024
		mem = strings.TrimSuffix(mem, "g")
	} else if strings.HasSuffix(mem, "m") {
		multiplier = 1024 * 1024
		mem = strings.TrimSuffix(mem, "m")
	}
	var value int64
	fmt.Sscanf(mem, "%d", &value)
	return value * multiplier
}
