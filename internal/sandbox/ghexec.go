package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// execGHCommand runs a gh CLI command on the host and returns its stdout.
func execGHCommand(ctx context.Context, command string) (string, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty command")
	}

	args := parts
	cmd := exec.CommandContext(ctx, "gh", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s", errMsg)
	}

	return stdout.String(), nil
}
