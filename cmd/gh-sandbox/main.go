// Command gh-sandbox is a gh CLI mirror binary that runs inside sandbox
// containers. It forwards all gh commands to the Vigilante reverse proxy
// on the host, which enforces repository-scoped access control.
//
// Inside the container, this binary is installed as "gh" so coding agents
// use it transparently.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type ghRequest struct {
	Command string `json:"command"`
	Token   string `json:"token"`
}

type ghResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func main() {
	proxyURL := os.Getenv("VIGILANTE_PROXY_URL")
	token := os.Getenv("VIGILANTE_SANDBOX_TOKEN")

	if proxyURL == "" || token == "" {
		fmt.Fprintln(os.Stderr, "gh-sandbox: VIGILANTE_PROXY_URL and VIGILANTE_SANDBOX_TOKEN must be set")
		os.Exit(1)
	}

	command := strings.Join(os.Args[1:], " ")
	if command == "" {
		fmt.Fprintln(os.Stderr, "usage: gh <command>")
		os.Exit(1)
	}

	req := ghRequest{
		Command: command,
		Token:   token,
	}
	body, err := json.Marshal(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh-sandbox: marshal request: %v\n", err)
		os.Exit(1)
	}

	endpoint := strings.TrimRight(proxyURL, "/") + "/api/sandbox/gh"
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh-sandbox: proxy request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gh-sandbox: read response: %v\n", err)
		os.Exit(1)
	}

	var result ghResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Non-JSON response — treat as error.
		fmt.Fprintln(os.Stderr, string(respBody))
		os.Exit(1)
	}

	if result.Stdout != "" {
		fmt.Fprint(os.Stdout, result.Stdout)
	}
	if result.Stderr != "" {
		fmt.Fprint(os.Stderr, result.Stderr)
	}

	os.Exit(result.ExitCode)
}
