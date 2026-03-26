package ghcli

import (
	"io"
	"strings"
	"testing"
)

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
