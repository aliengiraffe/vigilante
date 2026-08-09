package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// These functions delete worktrees and force-delete branches, so the guards are
// the important part: an over-eager cleanup destroys an operator's in-flight work.
// Every test drives a fake runner, so no real git command runs.

func TestPrune(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{Outputs: map[string]string{"git worktree prune": ""}}
	if err := Prune(context.Background(), runner, "/repo"); err != nil {
		t.Fatal(err)
	}

	failing := testutil.FakeRunner{Errors: map[string]error{"git worktree prune": errors.New("boom")}}
	if err := Prune(context.Background(), failing, "/repo"); err == nil {
		t.Fatal("expected the prune error to propagate")
	}
}

func TestRemove(t *testing.T) {
	t.Parallel()

	// --force is required: a worktree with local modifications otherwise refuses
	// to be removed and cleanup stalls forever.
	runner := testutil.FakeRunner{
		Outputs: map[string]string{"git worktree remove --force /repo/.worktrees/x": ""},
	}
	if err := Remove(context.Background(), runner, "/repo", "/repo/.worktrees/x"); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupIssueArtifactsDeletesAnUnattachedBranch(t *testing.T) {
	t.Parallel()

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                               "",
			"git worktree remove --force " + worktreePath:      "",
			"git worktree list --porcelain":                    "worktree /repo\nHEAD abc\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/feature": "",
			"git branch -D feature":                            "",
		},
	}

	if err := CleanupIssueArtifacts(context.Background(), runner, "/repo", worktreePath, "feature"); err != nil {
		t.Fatal(err)
	}
}

// A branch still checked out in another worktree must be left alone. Deleting it
// would break that worktree, and git would refuse anyway.
func TestCleanupIssueArtifactsSkipsAttachedBranch(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune": "",
			// No `git branch -D feature` fixture: if cleanup tried it, the fake
			// runner would error and this test would fail.
			"git worktree list --porcelain": "worktree /repo/.worktrees/other\nHEAD abc\nbranch refs/heads/feature\n",
		},
	}

	missingPath := filepath.Join(t.TempDir(), "absent")
	if err := CleanupIssueArtifacts(context.Background(), runner, "/repo", missingPath, "feature"); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupIssueArtifactsSkipsMissingBranch(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":            "",
			"git worktree list --porcelain": "worktree /repo\nHEAD abc\nbranch refs/heads/main\n",
		},
		// show-ref exiting 1 means "no such branch", which is not an error.
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/gone": errors.New("exit status 1"),
		},
	}

	missingPath := filepath.Join(t.TempDir(), "absent")
	if err := CleanupIssueArtifacts(context.Background(), runner, "/repo", missingPath, "gone"); err != nil {
		t.Fatalf("a missing branch is not an error: %v", err)
	}
}

// A missing worktree directory is the normal case after a crash, so it must not
// be treated as a failure.
func TestCleanupIssueArtifactsToleratesMissingWorktreeDirectory(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":            "",
			"git worktree list --porcelain": "",
		},
	}

	missingPath := filepath.Join(t.TempDir(), "never-created")
	if err := CleanupIssueArtifacts(context.Background(), runner, "/repo", missingPath, ""); err != nil {
		t.Fatalf("a missing worktree directory must not fail cleanup: %v", err)
	}
}

func TestCleanupIssueArtifactsForBranchesDeduplicatesAndSkipsBlanks(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                           "",
			"git worktree list --porcelain":                "worktree /repo\nHEAD abc\nbranch refs/heads/main\n",
			"git show-ref --verify --quiet refs/heads/dup": "",
			"git branch -D dup":                            "",
		},
	}

	missingPath := filepath.Join(t.TempDir(), "absent")
	// "dup" appears twice and there are blank entries; the second delete would
	// fail against real git, so dedup has to happen before the delete.
	branches := []string{"dup", "", "   ", "dup"}
	if err := CleanupIssueArtifactsForBranches(context.Background(), runner, "/repo", missingPath, branches); err != nil {
		t.Fatal(err)
	}
}

func TestCleanupIssueArtifactsPropagatesFailures(t *testing.T) {
	t.Parallel()

	missingPath := filepath.Join(t.TempDir(), "absent")

	tests := []struct {
		name    string
		runner  testutil.FakeRunner
		wtPath  string
		branch  string
		wantErr string
	}{
		{
			name: "prune failure aborts before touching anything",
			runner: testutil.FakeRunner{
				Errors: map[string]error{"git worktree prune": errors.New("prune broke")},
			},
			wtPath: missingPath,
			branch: "b",
		},
		{
			name: "worktree list failure aborts before deleting a branch",
			runner: testutil.FakeRunner{
				Outputs: map[string]string{"git worktree prune": ""},
				Errors: map[string]error{
					"git worktree list --porcelain": errors.New("list broke"),
				},
			},
			wtPath: missingPath,
			branch: "b",
		},
		{
			name: "unexpected show-ref failure aborts",
			runner: testutil.FakeRunner{
				Outputs: map[string]string{
					"git worktree prune":            "",
					"git worktree list --porcelain": "",
				},
				Errors: map[string]error{
					// Not "exit status 1", so this is a real failure rather than
					// a missing branch.
					"git show-ref --verify --quiet refs/heads/b": errors.New("fatal: not a git repository"),
				},
			},
			wtPath: missingPath,
			branch: "b",
		},
		{
			name: "branch delete failure propagates",
			runner: testutil.FakeRunner{
				Outputs: map[string]string{
					"git worktree prune":                         "",
					"git worktree list --porcelain":              "",
					"git show-ref --verify --quiet refs/heads/b": "",
				},
				Errors: map[string]error{
					"git branch -D b": errors.New("delete broke"),
				},
			},
			wtPath: missingPath,
			branch: "b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CleanupIssueArtifacts(context.Background(), tt.runner, "/repo", tt.wtPath, tt.branch)
			if err == nil {
				t.Fatal("expected the failure to propagate")
			}
		})
	}
}

func TestCleanupIssueArtifactsPropagatesWorktreeRemoveFailure(t *testing.T) {
	t.Parallel()

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := testutil.FakeRunner{
		Outputs: map[string]string{"git worktree prune": ""},
		Errors: map[string]error{
			"git worktree remove --force " + worktreePath: errors.New("remove broke"),
		},
	}

	if err := CleanupIssueArtifacts(context.Background(), runner, "/repo", worktreePath, "b"); err == nil {
		t.Fatal("expected the remove failure to propagate")
	}
}

func TestRecreateBranchWorktreeFromRemote(t *testing.T) {
	t.Parallel()

	worktreePath := filepath.Join(t.TempDir(), "wt")

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune": "",
			"git ls-remote --exit-code --heads origin feature": "abc refs/heads/feature",
			"git fetch origin feature:feature":                 "",
			"git worktree list --porcelain":                    "",
			"git worktree add " + worktreePath + " feature":    "",
		},
	}

	if err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "feature"); err != nil {
		t.Fatal(err)
	}
}

// With no remote branch, a local branch is enough. This is the path for work that
// was never pushed.
func TestRecreateBranchWorktreeFromLocalBranch(t *testing.T) {
	t.Parallel()

	worktreePath := filepath.Join(t.TempDir(), "wt")

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                             "",
			"git show-ref --verify --quiet refs/heads/local": "",
			"git worktree list --porcelain":                  "",
			"git worktree add " + worktreePath + " local":    "",
		},
		Errors: map[string]error{
			"git ls-remote --exit-code --heads origin local": errors.New("exit status 2"),
		},
	}

	if err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "local"); err != nil {
		t.Fatal(err)
	}
}

func TestRecreateBranchWorktreeErrors(t *testing.T) {
	t.Parallel()

	worktreePath := filepath.Join(t.TempDir(), "wt")

	t.Run("branch missing locally and on origin", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git worktree prune": ""},
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin nope": errors.New("exit status 2"),
				"git show-ref --verify --quiet refs/heads/nope": errors.New("exit status 1"),
			},
		}

		err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "nope")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "not found locally or on origin") {
			t.Fatalf("error should say the branch was not found, got %v", err)
		}
	})

	// Recreating onto a branch another worktree already holds would corrupt that
	// worktree, so it has to fail loudly rather than proceed.
	t.Run("branch already attached to another worktree", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git worktree prune": "",
				"git ls-remote --exit-code --heads origin feature": "abc refs/heads/feature",
				"git fetch origin feature:feature":                 "",
				"git worktree list --porcelain":                    "worktree /elsewhere\nHEAD abc\nbranch refs/heads/feature\n",
			},
		}

		err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "feature")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "already attached") {
			t.Fatalf("error should mention the attachment, got %v", err)
		}
	})

	t.Run("fetch failure is wrapped with the branch name", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git worktree prune": "",
				"git ls-remote --exit-code --heads origin feature": "abc refs/heads/feature",
			},
			Errors: map[string]error{
				"git fetch origin feature:feature": errors.New("network down"),
			},
		}

		err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "feature")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "feature") {
			t.Fatalf("error should name the branch, got %v", err)
		}
	})

	t.Run("ls-remote hard failure aborts", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git worktree prune": ""},
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin feature": errors.New("fatal: could not read from remote"),
			},
		}

		if err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "feature"); err == nil {
			t.Fatal("expected an error for an unexpected ls-remote failure")
		}
	})

	t.Run("prune failure aborts", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{"git worktree prune": errors.New("prune broke")},
		}
		if err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "feature"); err == nil {
			t.Fatal("expected an error")
		}
	})

	t.Run("worktree add failure propagates", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git worktree prune":                             "",
				"git show-ref --verify --quiet refs/heads/local": "",
				"git worktree list --porcelain":                  "",
			},
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin local": errors.New("exit status 2"),
				"git worktree add " + worktreePath + " local":    errors.New("add broke"),
			},
		}

		if err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "local"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestRecreateBranchWorktreeRemovesExistingDirectory(t *testing.T) {
	t.Parallel()

	worktreePath := filepath.Join(t.TempDir(), "wt")
	if err := os.Mkdir(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree prune":                             "",
			"git worktree remove --force " + worktreePath:    "",
			"git show-ref --verify --quiet refs/heads/local": "",
			"git worktree list --porcelain":                  "",
			"git worktree add " + worktreePath + " local":    "",
		},
		Errors: map[string]error{
			"git ls-remote --exit-code --heads origin local": errors.New("exit status 2"),
		},
	}

	if err := RecreateBranchWorktree(context.Background(), runner, "/repo", worktreePath, "local"); err != nil {
		t.Fatal(err)
	}
}

// show-ref exits 1 for "no such branch" and something else for real trouble.
// Conflating the two would either delete nothing or abort cleanup entirely.
func TestBranchExistsWithError(t *testing.T) {
	t.Parallel()

	t.Run("exists", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git show-ref --verify --quiet refs/heads/b": ""},
		}
		exists, err := branchExistsWithError(context.Background(), runner, "/repo", "b")
		if err != nil || !exists {
			t.Fatalf("exists=%v err=%v, want true/nil", exists, err)
		}
	})

	t.Run("exit status 1 means absent, not an error", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git show-ref --verify --quiet refs/heads/b": errors.New("exit status 1"),
			},
		}
		exists, err := branchExistsWithError(context.Background(), runner, "/repo", "b")
		if err != nil || exists {
			t.Fatalf("exists=%v err=%v, want false/nil", exists, err)
		}
	})

	t.Run("other failures are errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git show-ref --verify --quiet refs/heads/b": errors.New("fatal: not a git repository"),
			},
		}
		if _, err := branchExistsWithError(context.Background(), runner, "/repo", "b"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestBranchExists(t *testing.T) {
	t.Parallel()

	present := testutil.FakeRunner{
		Outputs: map[string]string{"git show-ref --verify --quiet refs/heads/b": ""},
	}
	if !branchExists(context.Background(), present, "/repo", "b") {
		t.Error("expected the branch to be reported present")
	}

	absent := testutil.FakeRunner{
		Errors: map[string]error{
			"git show-ref --verify --quiet refs/heads/b": errors.New("exit status 1"),
		},
	}
	if branchExists(context.Background(), absent, "/repo", "b") {
		t.Error("expected the branch to be reported absent")
	}
}

// ls-remote uses exit 2 for "no such ref" and 1 in some versions, so both count
// as absent rather than as failures.
func TestRemoteBranchExistsWithError(t *testing.T) {
	t.Parallel()

	t.Run("exists", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git ls-remote --exit-code --heads origin b": "abc refs/heads/b",
			},
		}
		exists, err := remoteBranchExistsWithError(context.Background(), runner, "/repo", "origin", "b")
		if err != nil || !exists {
			t.Fatalf("exists=%v err=%v", exists, err)
		}
	})

	for _, status := range []string{"exit status 1", "exit status 2"} {
		t.Run(status+" means absent", func(t *testing.T) {
			t.Parallel()

			runner := testutil.FakeRunner{
				Errors: map[string]error{
					"git ls-remote --exit-code --heads origin b": errors.New(status),
				},
			}
			exists, err := remoteBranchExistsWithError(context.Background(), runner, "/repo", "origin", "b")
			if err != nil || exists {
				t.Fatalf("exists=%v err=%v, want false/nil", exists, err)
			}
		})
	}

	t.Run("other failures are errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin b": errors.New("fatal: could not read from remote"),
			},
		}
		if _, err := remoteBranchExistsWithError(context.Background(), runner, "/repo", "origin", "b"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestWorktreePathForBranch(t *testing.T) {
	t.Parallel()

	const listing = "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
		"worktree /repo/.worktrees/vigilante/issue-1\nHEAD bbb\nbranch refs/heads/vigilante/issue-1\n"

	t.Run("finds the attached path", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git worktree list --porcelain": listing},
		}
		path, err := worktreePathForBranch(context.Background(), runner, "/repo", "vigilante/issue-1")
		if err != nil {
			t.Fatal(err)
		}
		if path != "/repo/.worktrees/vigilante/issue-1" {
			t.Fatalf("path = %q", path)
		}
	})

	t.Run("returns empty for an unattached branch", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git worktree list --porcelain": listing},
		}
		path, err := worktreePathForBranch(context.Background(), runner, "/repo", "other")
		if err != nil {
			t.Fatal(err)
		}
		if path != "" {
			t.Fatalf("path = %q, want empty", path)
		}
	})

	t.Run("propagates errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{"git worktree list --porcelain": errors.New("boom")},
		}
		if _, err := worktreePathForBranch(context.Background(), runner, "/repo", "b"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestBranchAttachedToWorktree(t *testing.T) {
	t.Parallel()

	attachedRunner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git worktree list --porcelain": "worktree /x\nHEAD a\nbranch refs/heads/b\n",
		},
	}
	attached, err := branchAttachedToWorktree(context.Background(), attachedRunner, "/repo", "b")
	if err != nil || !attached {
		t.Fatalf("attached=%v err=%v, want true/nil", attached, err)
	}

	freeRunner := testutil.FakeRunner{
		Outputs: map[string]string{"git worktree list --porcelain": ""},
	}
	attached, err = branchAttachedToWorktree(context.Background(), freeRunner, "/repo", "b")
	if err != nil || attached {
		t.Fatalf("attached=%v err=%v, want false/nil", attached, err)
	}

	failing := testutil.FakeRunner{
		Errors: map[string]error{"git worktree list --porcelain": errors.New("boom")},
	}
	if _, err := branchAttachedToWorktree(context.Background(), failing, "/repo", "b"); err == nil {
		t.Fatal("expected an error")
	}
}

// Branch naming is what links an issue to its worktree across daemon restarts, so
// the slug rules are a compatibility surface.
func TestIssueBranchNaming(t *testing.T) {
	t.Parallel()

	t.Run("title becomes a slug suffix", func(t *testing.T) {
		t.Parallel()

		got := IssueBranchName(42, "Fix the Thing!")
		if got != "vigilante/issue-42-fix-the-thing" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a title with no usable characters falls back to the legacy name", func(t *testing.T) {
		t.Parallel()

		for _, title := range []string{"", "   ", "!!!", "---"} {
			got := IssueBranchName(42, title)
			if got != "vigilante/issue-42" {
				t.Errorf("IssueBranchName(42, %q) = %q, want the legacy name", title, got)
			}
		}
	})

	t.Run("candidates include both names when they differ", func(t *testing.T) {
		t.Parallel()

		got := IssueBranchCandidates(42, "Some Title")
		if len(got) != 2 || got[0] != "vigilante/issue-42-some-title" || got[1] != "vigilante/issue-42" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("candidates collapse to one when the slug is empty", func(t *testing.T) {
		t.Parallel()

		got := IssueBranchCandidates(42, "")
		if len(got) != 1 || got[0] != "vigilante/issue-42" {
			t.Fatalf("got %#v", got)
		}
	})

	t.Run("worktree path is namespaced under the repo", func(t *testing.T) {
		t.Parallel()

		got := IssueWorktreePath("/repo", 7)
		want := filepath.Join("/repo", ".worktrees", "vigilante", "issue-7")
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestIssueTitleSlugTableCases(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Simple Title":            "simple-title",
		"UPPER case":              "upper-case",
		"  leading and trailing ": "leading-and-trailing",
		"multiple   spaces":       "multiple-spaces",
		"punctuation!@#$":         "punctuation",
		"already-hyphenated":      "already-hyphenated",
		"":                        "",
		"!!!":                     "",
		"digits 123":              "digits-123",
	}
	for title, want := range tests {
		if got := IssueTitleSlug(title); got != want {
			t.Errorf("IssueTitleSlug(%q) = %q, want %q", title, got, want)
		}
	}
}
