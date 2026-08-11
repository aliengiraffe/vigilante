package ghcli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeBodyFlagFormsAndFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "body.md")
	if err := os.WriteFile(path, []byte("Ready\n\nGenerated with Codex"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		tool string
		args []string
		want string
	}{
		{"gh", []string{"pr", "create", "--body=Ready\nGenerated with Gemini"}, "pr create --body=Ready"},
		{"gh", []string{"pr", "edit", "--body-file=" + path}, "pr edit --body-file -"},
		{"git", []string{"-C", "/tmp", "commit", "--message=Fix\nAuthored by Claude"}, "-C /tmp commit --message=Fix"},
		{"git", []string{"commit", "--file", path}, "commit --file -"},
	} {
		args, stdin, err := SanitizeProxyInvocation(tc.tool, tc.args, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(args, " "); got != tc.want {
			t.Errorf("args=%q want %q", got, tc.want)
		}
		if strings.Contains(tc.want, "-file -") || strings.Contains(tc.want, "--file -") {
			body, _ := io.ReadAll(stdin)
			if string(body) != "Ready" {
				t.Errorf("body=%q", body)
			}
		}
	}
	if _, _, err := SanitizeProxyInvocation("gh", []string{"issue", "comment", "1", "--body-file", filepath.Join(dir, "missing")}, nil); err == nil {
		t.Fatal("expected missing file error")
	}
	_, _, err := sanitizeFileOrStdinValue("-", errorReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error=%v", err)
	}
	reader, _, err := sanitizeFileOrStdinValue("-", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(reader)
	if len(body) != 0 {
		t.Fatalf("body=%q", body)
	}
}

func TestGitHubCommandRecognitionBoundaries(t *testing.T) {
	for _, args := range [][]string{{"issue", "comment"}, {"--repo", "owner/repo", "pr", "create"}, {"pr", "edit"}} {
		if !isGitHubBodyCommand(args) {
			t.Errorf("not body command: %#v", args)
		}
	}
	for _, args := range [][]string{nil, {"issue", "view"}, {"pr", "list"}} {
		if isGitHubBodyCommand(args) {
			t.Errorf("body command: %#v", args)
		}
	}
	if !isGitHubAPIBodyCommand([]string{"api", "repos/owner/repo/pulls/1"}) || isGitHubAPIBodyCommand([]string{"api", "user"}) || isGitHubAPIBodyCommand(nil) {
		t.Fatal("API recognition boundaries")
	}
	for _, tc := range []struct {
		tool, flag string
		want       bool
	}{{"gh", "--repo", true}, {"gh", "--repo=x", false}, {"git", "--git-dir", true}, {"docker", "--config", false}} {
		if got := proxyFlagNeedsValue(tc.tool, tc.flag); got != tc.want {
			t.Errorf("flag %s/%s=%v", tc.tool, tc.flag, got)
		}
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestSanitizeGitHubVisibleTextRemovesAgentAttribution(t *testing.T) {
	input := strings.Join([]string{
		"Summary line",
		"",
		"Generated with Codex",
		"Co-authored by: Claude Code <bot@example.com>",
		"",
		"Validation: `go test ./...`",
	}, "\n")

	got := SanitizeGitHubVisibleText(input)
	want := "Summary line\n\nValidation: `go test ./...`"
	if got != want {
		t.Fatalf("SanitizeGitHubVisibleText() = %q, want %q", got, want)
	}
}

func TestSanitizeGitHubVisibleTextPreservesOperationalProviderMentions(t *testing.T) {
	input := "## 🕹️ Coding Agent Launched: Codex\n\n- Provider routing selected `Codex` for this issue."
	if got := SanitizeGitHubVisibleText(input); got != input {
		t.Fatalf("expected operational provider mention to remain intact, got %q", got)
	}
}

func TestSanitizeProxyInvocationRewritesIssueCommentBodyFileFromStdin(t *testing.T) {
	args, stdin, err := SanitizeProxyInvocation("gh", []string{"issue", "comment", "7", "--body-file", "-"}, strings.NewReader("Done\n\nGenerated with Gemini CLI"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(args, " "), "issue comment 7 --body-file -"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), "Done"; got != want {
		t.Fatalf("stdin = %q, want %q", got, want)
	}
}

func TestSanitizeProxyInvocationRewritesCommitMessageFlags(t *testing.T) {
	args, _, err := SanitizeProxyInvocation("git", []string{"commit", "-m", "Fix bug\n\nGenerated with Codex", "--amend"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(args, " "), "commit -m Fix bug --amend"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}

func TestSanitizeProxyInvocationSanitizesGitHubAPIBodyField(t *testing.T) {
	args, _, err := SanitizeProxyInvocation("gh", []string{"api", "--method", "POST", "repos/owner/repo/issues/7/comments", "-f", "body=Ready\n\nGenerated with Claude Code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(args, " "), "api --method POST repos/owner/repo/issues/7/comments -f body=Ready"; got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
}
