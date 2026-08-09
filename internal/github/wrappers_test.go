package ghcli

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func wrapperLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// These functions are thin wrappers over `gh`. The behavior worth pinning is the
// exact invocation each one builds, because a wrong flag or endpoint is invisible
// until it fails against the real API. A fixture keyed on the expected command
// makes the call succeed only when the wrapper built that command.
func TestGhWrapperInvocations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		output  string
		invoke  func(*testing.T, testutil.FakeRunner) error
	}{
		{
			name:    "CreateIssue posts title, body, labels, and assignees",
			command: "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues -f title=T -f body=B -f labels[]=bug -f assignees[]=me",
			output:  `{"number":9,"html_url":"https://github.com/owner/repo/issues/9"}`,
			invoke: func(t *testing.T, r testutil.FakeRunner) error {
				created, err := CreateIssue(context.Background(), r, "owner/repo", "T", "B", []string{"bug"}, []string{"me"})
				if err == nil {
					if created.Number != 9 {
						t.Errorf("Number = %d, want 9", created.Number)
					}
					if created.URL != "https://github.com/owner/repo/issues/9" {
						t.Errorf("URL = %q", created.URL)
					}
				}
				return err
			},
		},
		{
			name:    "CloseIssueNotPlanned sets the not_planned state reason",
			command: "gh api --method PATCH -H Accept: application/vnd.github+json repos/owner/repo/issues/4 -f state=closed -f state_reason=not_planned",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return CloseIssueNotPlanned(context.Background(), r, "owner/repo", 4)
			},
		},
		{
			name:    "AddIssueCommentReaction targets the issue-comment reactions endpoint",
			command: "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/55/reactions -f content=eyes",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return AddIssueCommentReaction(context.Background(), r, "owner/repo", 55, "eyes")
			},
		},
		{
			name:    "AddPullRequestCommentReaction shares the issue-comment endpoint",
			command: "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues/comments/55/reactions -f content=+1",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return AddPullRequestCommentReaction(context.Background(), r, "owner/repo", 55, "+1")
			},
		},
		{
			name:    "ClosePullRequest closes by number",
			command: "gh pr close --repo owner/repo 4",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return ClosePullRequest(context.Background(), r, "owner/repo", 4)
			},
		},
		{
			name:    "CommentOnPullRequest uses the issue comment command",
			command: "gh issue comment --repo owner/repo 4 --body hello",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return CommentOnPullRequest(context.Background(), r, "owner/repo", 4, "hello")
			},
		},
		{
			name:    "AddPullRequestLabel edits the pull request",
			command: "gh pr edit --repo owner/repo 4 --add-label ready",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return AddPullRequestLabel(context.Background(), r, "owner/repo", 4, "ready")
			},
		},
		{
			name:    "RemoveDeployKey deletes by key id",
			command: "gh api --method DELETE -H Accept: application/vnd.github+json repos/owner/repo/keys/77",
			invoke: func(_ *testing.T, r testutil.FakeRunner) error {
				return RemoveDeployKey(context.Background(), r, "owner/repo", 77)
			},
		},
		{
			name:    "ListIssueComments reads the issue comments endpoint",
			command: "gh api repos/owner/repo/issues/4/comments",
			output:  `[{"id":1,"body":"a","user":{"login":"u"},"created_at":"2026-03-10T12:00:00Z"}]`,
			invoke: func(t *testing.T, r testutil.FakeRunner) error {
				comments, err := ListIssueComments(context.Background(), r, "owner/repo", 4)
				if err == nil && len(comments) != 1 {
					t.Errorf("got %d comments, want 1", len(comments))
				}
				return err
			},
		},
		{
			name:    "ListPullRequestComments reads the issue comments endpoint",
			command: "gh api repos/owner/repo/issues/4/comments",
			output:  `[{"id":1,"body":"a","user":{"login":"u"}}]`,
			invoke: func(t *testing.T, r testutil.FakeRunner) error {
				_, err := ListPullRequestComments(context.Background(), r, "owner/repo", 4)
				return err
			},
		},
		{
			name:    "ListPullRequestFiles paginates the pulls files endpoint",
			command: "gh api repos/owner/repo/pulls/4/files --paginate",
			output:  `[{"filename":"a.go","status":"modified"}]`,
			invoke: func(t *testing.T, r testutil.FakeRunner) error {
				files, err := ListPullRequestFiles(context.Background(), r, "owner/repo", 4)
				if err == nil && (len(files) != 1 || files[0].Filename != "a.go") {
					t.Errorf("files = %#v", files)
				}
				return err
			},
		},
		{
			name:    "ListOpenPullRequests requests the fields the maintenance loop needs",
			command: "gh pr list --repo owner/repo --state open --json number,title,url,labels,baseRefName",
			output:  `[{"number":2,"title":"t","url":"u","baseRefName":"main"}]`,
			invoke: func(t *testing.T, r testutil.FakeRunner) error {
				prs, err := ListOpenPullRequests(context.Background(), r, "owner/repo")
				if err == nil && len(prs) != 1 {
					t.Errorf("got %d prs, want 1", len(prs))
				}
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			runner := testutil.FakeRunner{Outputs: map[string]string{tt.command: tt.output}}
			if err := tt.invoke(t, runner); err != nil {
				t.Fatalf("wrapper did not build %q: %v", tt.command, err)
			}
		})
	}
}

// AddDeployKey turns the boolean into GitHub's -F read_only field. Sending the
// wrong value would silently grant a write-capable key to a sandboxed session.
func TestAddDeployKeyReadOnlyFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		readOnly bool
		command  string
	}{
		{
			readOnly: true,
			command:  "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/keys -f title=t -f key=ssh-ed25519 AAAA -F read_only=true",
		},
		{
			readOnly: false,
			command:  "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/keys -f title=t -f key=ssh-ed25519 AAAA -F read_only=false",
		},
	}

	for _, tt := range tests {
		name := "read-only"
		if !tt.readOnly {
			name = "writable"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runner := testutil.FakeRunner{Outputs: map[string]string{tt.command: `{"id":123}`}}
			id, err := AddDeployKey(context.Background(), runner, "owner/repo", "t", "ssh-ed25519 AAAA", tt.readOnly)
			if err != nil {
				t.Fatalf("wrapper did not build %q: %v", tt.command, err)
			}
			if id != 123 {
				t.Fatalf("id = %d, want 123", id)
			}
		})
	}
}

func TestAddDeployKeyErrors(t *testing.T) {
	t.Parallel()

	command := "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/keys -f title=t -f key=k -F read_only=true"

	t.Run("includes gh stderr in the error when present", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors:       map[string]error{command: errors.New("exit status 1")},
			ErrorOutputs: map[string]string{command: "key is already in use"},
		}
		_, err := AddDeployKey(context.Background(), runner, "owner/repo", "t", "k", true)
		if err == nil {
			t.Fatal("expected an error")
		}
		// The gh output is the actionable part; dropping it leaves an operator
		// with a bare exit status.
		if !strings.Contains(err.Error(), "key is already in use") {
			t.Fatalf("error should include gh output, got %v", err)
		}
		if !strings.Contains(err.Error(), "owner/repo") {
			t.Fatalf("error should name the repo, got %v", err)
		}
	})

	t.Run("bare error when gh printed nothing", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{command: errors.New("exit status 1")},
		}
		_, err := AddDeployKey(context.Background(), runner, "owner/repo", "t", "k", true)
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "add deploy key") {
			t.Fatalf("error should describe the operation, got %v", err)
		}
	})

	t.Run("malformed response", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{command: "not json"}}
		if _, err := AddDeployKey(context.Background(), runner, "owner/repo", "t", "k", true); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

// The lookup is case-insensitive and exact-match: a substring match would let
// "owner/repo-staging" satisfy a request for "owner/repo".
func TestFindAuthenticatedUserRepository(t *testing.T) {
	t.Parallel()

	const command = "gh api --paginate -H Accept: application/vnd.github+json user/repos?per_page=100&affiliation=owner,collaborator,organization_member -q .[].full_name"

	t.Run("empty slug short-circuits without calling gh", func(t *testing.T) {
		t.Parallel()

		// An empty FakeRunner errors on any command, so a nil error proves no call.
		got, err := FindAuthenticatedUserRepository(context.Background(), testutil.FakeRunner{}, "   ")
		if err != nil {
			t.Fatalf("expected no call and no error, got %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("matches case-insensitively and returns the canonical name", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{command: "other/thing\nOwner/Repo\n\nmore/stuff\n"},
		}
		got, err := FindAuthenticatedUserRepository(context.Background(), runner, "owner/repo")
		if err != nil {
			t.Fatal(err)
		}
		if got != "Owner/Repo" {
			t.Fatalf("got %q, want the canonical %q", got, "Owner/Repo")
		}
	})

	t.Run("no match returns empty without an error", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{command: "someone/else\n"},
		}
		got, err := FindAuthenticatedUserRepository(context.Background(), runner, "owner/repo")
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty for no match", got)
		}
	})

	t.Run("partial names do not match", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{command: "owner/repo-staging\n"},
		}
		got, err := FindAuthenticatedUserRepository(context.Background(), runner, "owner/repo")
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty: a prefix must not match", got)
		}
	})

	t.Run("error includes gh output when present", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors:       map[string]error{command: errors.New("exit status 1")},
			ErrorOutputs: map[string]string{command: "Bad credentials"},
		}
		if _, err := FindAuthenticatedUserRepository(context.Background(), runner, "owner/repo"); err == nil {
			t.Fatal("expected an error")
		} else if !strings.Contains(err.Error(), "Bad credentials") {
			t.Fatalf("error should include gh output, got %v", err)
		}
	})

	t.Run("bare error when gh printed nothing", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{command: errors.New("exit status 1")},
		}
		if _, err := FindAuthenticatedUserRepository(context.Background(), runner, "owner/repo"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// DeleteRemoteBranch defaults the remote so callers that have not resolved a fork
// remote still push to origin rather than to an empty remote name.
func TestDeleteRemoteBranchDefaultsRemote(t *testing.T) {
	t.Parallel()

	t.Run("explicit remote", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{"git push fork --delete b": ""}}
		if err := DeleteRemoteBranch(context.Background(), runner, "/repo", "fork", "b"); err != nil {
			t.Fatal(err)
		}
	})

	for _, remote := range []string{"", "   "} {
		t.Run("blank remote defaults to origin", func(t *testing.T) {
			t.Parallel()

			runner := testutil.FakeRunner{Outputs: map[string]string{"git push origin --delete b": ""}}
			if err := DeleteRemoteBranch(context.Background(), runner, "/repo", remote, "b"); err != nil {
				t.Fatalf("blank remote %q should default to origin: %v", remote, err)
			}
		})
	}
}

func TestListIssueCommentsForPolling(t *testing.T) {
	t.Parallel()

	const command = "gh api repos/owner/repo/issues/4/comments"

	t.Run("returns comments", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{command: `[{"id":1,"body":"a","user":{"login":"u"}}]`},
		}
		comments, err := ListIssueCommentsForPolling(context.Background(), runner, "owner/repo", 4, "resume", wrapperLogger())
		if err != nil {
			t.Fatal(err)
		}
		if len(comments) != 1 {
			t.Fatalf("got %d comments, want 1", len(comments))
		}
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
		if _, err := ListIssueCommentsForPolling(context.Background(), runner, "owner/repo", 4, "resume", wrapperLogger()); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("tolerates a nil logger", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{command: `[]`}}
		if _, err := ListIssueCommentsForPolling(context.Background(), runner, "owner/repo", 4, "resume", nil); err != nil {
			t.Fatalf("a nil logger must not fail the call: %v", err)
		}
	})
}

func TestListPullRequestReviewCommentsForPolling(t *testing.T) {
	t.Parallel()

	const command = "gh api repos/owner/repo/pulls/4/comments --paginate"

	t.Run("returns review comments", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{command: `[{"id":2,"body":"inline","user":{"login":"u"}}]`},
		}
		comments, err := ListPullRequestReviewCommentsForPolling(context.Background(), runner, "owner/repo", 4, "review", wrapperLogger())
		if err != nil {
			t.Fatal(err)
		}
		if len(comments) != 1 {
			t.Fatalf("got %d comments, want 1", len(comments))
		}
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
		if _, err := ListPullRequestReviewCommentsForPolling(context.Background(), runner, "owner/repo", 4, "review", wrapperLogger()); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestListPullRequestCommentsForPollingWrapper(t *testing.T) {
	t.Parallel()

	const command = "gh api repos/owner/repo/issues/4/comments"

	runner := testutil.FakeRunner{
		Outputs: map[string]string{command: `[{"id":1,"body":"a","user":{"login":"u"}}]`},
	}
	comments, err := ListPullRequestCommentsForPolling(context.Background(), runner, "owner/repo", 4, "iterate", wrapperLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 {
		t.Fatalf("got %d comments, want 1", len(comments))
	}

	failing := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
	if _, err := ListPullRequestCommentsForPolling(context.Background(), failing, "owner/repo", 4, "iterate", wrapperLogger()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestHasAnyLabel(t *testing.T) {
	t.Parallel()

	labels := []Label{{Name: "bug"}, {Name: "vigilante"}}

	if !HasAnyLabel(labels, "vigilante") {
		t.Error("should match a present label")
	}
	if !HasAnyLabel(labels, "missing", "bug") {
		t.Error("should match when any candidate is present")
	}
	if HasAnyLabel(labels, "missing") {
		t.Error("must not match an absent label")
	}
	if HasAnyLabel(labels) {
		t.Error("no candidates must not match")
	}
	if HasAnyLabel(nil, "bug") {
		t.Error("no labels must not match")
	}
	// Label matching is exact: GitHub labels are case-sensitive, and treating
	// them otherwise would sync the wrong label.
	if HasAnyLabel(labels, "BUG") {
		t.Error("matching must be case-sensitive")
	}
}

func TestListPullRequestFilesErrors(t *testing.T) {
	t.Parallel()

	const command = "gh api repos/owner/repo/pulls/4/files --paginate"

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
		if _, err := ListPullRequestFiles(context.Background(), runner, "owner/repo", 4); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{command: "{"}}
		if _, err := ListPullRequestFiles(context.Background(), runner, "owner/repo", 4); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestListOpenPullRequestsErrors(t *testing.T) {
	t.Parallel()

	const command = "gh pr list --repo owner/repo --state open --json number,title,url,labels,baseRefName"

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
		if _, err := ListOpenPullRequests(context.Background(), runner, "owner/repo"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{command: "nope"}}
		if _, err := ListOpenPullRequests(context.Background(), runner, "owner/repo"); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestListIssueCommentsErrors(t *testing.T) {
	t.Parallel()

	const command = "gh api repos/owner/repo/issues/4/comments"

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
		if _, err := ListIssueComments(context.Background(), runner, "owner/repo", 4); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{command: "["}}
		if _, err := ListIssueComments(context.Background(), runner, "owner/repo", 4); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}

func TestCreateIssueErrors(t *testing.T) {
	t.Parallel()

	const command = "gh api --method POST -H Accept: application/vnd.github+json repos/owner/repo/issues -f title=T -f body=B"

	t.Run("runner failure", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Errors: map[string]error{command: errors.New("boom")}}
		if _, err := CreateIssue(context.Background(), runner, "owner/repo", "T", "B", nil, nil); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{Outputs: map[string]string{command: "nope"}}
		if _, err := CreateIssue(context.Background(), runner, "owner/repo", "T", "B", nil, nil); err == nil {
			t.Fatal("expected a parse error")
		}
	})

	t.Run("omits empty label and assignee flags", func(t *testing.T) {
		t.Parallel()

		// The command above has no labels[]/assignees[] flags, so this only
		// succeeds if the wrapper omits them rather than sending empty values.
		runner := testutil.FakeRunner{Outputs: map[string]string{command: `{"number":1}`}}
		if _, err := CreateIssue(context.Background(), runner, "owner/repo", "T", "B", nil, nil); err != nil {
			t.Fatalf("empty labels and assignees must not add flags: %v", err)
		}
	})
}
