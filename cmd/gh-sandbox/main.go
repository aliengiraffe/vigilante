// Command gh-sandbox is a lightweight gh CLI mirror for sandbox containers.
//
// Instead of calling the GitHub API directly, it forwards every command to
// the Vigilante reverse proxy running on the host. The proxy validates the
// sandbox token, enforces repository scope, and executes the real gh CLI.
//
// Environment variables:
//   - VIGILANTE_PROXY_URL: the host reverse proxy endpoint (e.g. http://127.0.0.1:9821)
//   - VIGILANTE_SANDBOX_TOKEN: the HMAC-signed session token
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type ghRequest struct {
	Command string `json:"command"`
	Token   string `json:"token"`
	Stdin   string `json:"stdin,omitempty"`
}

type ghResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func main() {
	os.Exit(run())
}

func run() int {
	proxyURL := os.Getenv("VIGILANTE_PROXY_URL")
	if proxyURL == "" {
		fmt.Fprintln(os.Stderr, "gh-sandbox: VIGILANTE_PROXY_URL not set")
		return 1
	}
	token := os.Getenv("VIGILANTE_SANDBOX_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "gh-sandbox: VIGILANTE_SANDBOX_TOKEN not set")
		return 1
	}

	command := strings.Join(os.Args[1:], " ")
	if command == "" {
		fmt.Fprintln(os.Stderr, "usage: gh <command>")
		return 1
	}

	// Forward stdin only when it's a pipe/file (not a TTY), so interactive
	// invocations don't block waiting for input. Required so flags like
	// `--body-file -` see the bytes the agent piped in.
	var stdinBytes []byte
	if stat, err := os.Stdin.Stat(); err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		stdinBytes, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gh-sandbox: read stdin: %s\n", err)
			return 1
		}
	}

	body, err := json.Marshal(ghRequest{
		Command: command,
		Token:   token,
		Stdin:   string(stdinBytes),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh-sandbox: marshal request: %s\n", err)
		return 1
	}

	resp, err := http.Post(proxyURL+"/api/sandbox/gh", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh-sandbox: proxy request failed: %s\n", err)
		return 1
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh-sandbox: read response: %s\n", err)
		return 1
	}

	var ghResp ghResponse
	if err := json.Unmarshal(respBody, &ghResp); err != nil {
		// Not JSON — print raw body as error.
		fmt.Fprint(os.Stderr, string(respBody))
		return 1
	}

	if ghResp.Stdout != "" {
		fmt.Fprint(os.Stdout, ghResp.Stdout)
	}
	if ghResp.Stderr != "" {
		fmt.Fprint(os.Stderr, ghResp.Stderr)
	}
	return ghResp.ExitCode
}
