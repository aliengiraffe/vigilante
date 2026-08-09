package repo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

// Branch resolution decides what a session branches off. Silently picking the
// wrong base would produce a PR against the wrong branch, so a pinned branch that
// does not exist has to fail loudly rather than fall back.
func TestResolveBranchPinned(t *testing.T) {
	t.Parallel()

	t.Run("existing pinned branch is returned", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git ls-remote --exit-code --heads origin main": "abc refs/heads/main",
			},
		}
		got, err := ResolveBranch(context.Background(), runner, "/repo", "pinned", "main")
		if err != nil {
			t.Fatal(err)
		}
		if got != "main" {
			t.Fatalf("got %q, want main", got)
		}
	})

	t.Run("empty branch mode behaves as pinned", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git ls-remote --exit-code --heads origin main": "abc refs/heads/main",
			},
		}
		got, err := ResolveBranch(context.Background(), runner, "/repo", "  ", "main")
		if err != nil {
			t.Fatal(err)
		}
		if got != "main" {
			t.Fatalf("got %q, want main", got)
		}
	})

	t.Run("missing pinned branch is an error, not a fallback", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin gone": errors.New("exit status 2"),
			},
		}
		_, err := ResolveBranch(context.Background(), runner, "/repo", "pinned", "gone")
		if err == nil {
			t.Fatal("expected an error for a missing pinned branch")
		}
		if !strings.Contains(err.Error(), "not found on origin") {
			t.Fatalf("error should say the branch is missing, got %v", err)
		}
	})

	t.Run("unconfigured pinned branch is an error", func(t *testing.T) {
		t.Parallel()

		// An empty FakeRunner errors on any command, so reaching the expected
		// message proves no git call was attempted.
		_, err := ResolveBranch(context.Background(), testutil.FakeRunner{}, "/repo", "pinned", "   ")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("error should say the branch is not configured, got %v", err)
		}
	})

	t.Run("an unexpected ls-remote failure propagates", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin main": errors.New("fatal: could not read from remote"),
			},
		}
		if _, err := ResolveBranch(context.Background(), runner, "/repo", "pinned", "main"); err == nil {
			t.Fatal("expected the failure to propagate rather than read as absent")
		}
	})
}

func TestResolveBranchAuto(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git ls-remote --symref origin HEAD": "ref: refs/heads/develop\tHEAD\nabc\tHEAD\n",
		},
	}
	got, err := ResolveBranch(context.Background(), runner, "/repo", "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "develop" {
		t.Fatalf("got %q, want develop", got)
	}
}

func TestResolveBranchUnsupportedMode(t *testing.T) {
	t.Parallel()

	_, err := ResolveBranch(context.Background(), testutil.FakeRunner{}, "/repo", "sideways", "main")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "unsupported branch mode") {
		t.Fatalf("got %v", err)
	}
}

// Default-branch resolution walks a fallback chain. Each step is covered because
// the later ones only run on hosts where the earlier git queries fail.
func TestResolveDefaultBranchFallbackChain(t *testing.T) {
	t.Parallel()

	t.Run("prefers the remote symref", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git ls-remote --symref origin HEAD": "ref: refs/heads/trunk\tHEAD\n",
				// Present but must not be consulted.
				"git symbolic-ref --short refs/remotes/origin/HEAD": "origin/wrong",
			},
		}
		got, err := ResolveDefaultBranch(context.Background(), runner, "/repo", "fb")
		if err != nil {
			t.Fatal(err)
		}
		if got != "trunk" {
			t.Fatalf("got %q, want trunk", got)
		}
	})

	t.Run("falls back to the local symbolic ref", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git symbolic-ref --short refs/remotes/origin/HEAD": "origin/main\n",
			},
			Errors: map[string]error{
				"git ls-remote --symref origin HEAD": errors.New("offline"),
			},
		}
		got, err := ResolveDefaultBranch(context.Background(), runner, "/repo", "fb")
		if err != nil {
			t.Fatal(err)
		}
		if got != "main" {
			t.Fatalf("got %q, want main with the origin/ prefix stripped", got)
		}
	})

	t.Run("falls back to the configured value", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git ls-remote --symref origin HEAD":                errors.New("offline"),
				"git symbolic-ref --short refs/remotes/origin/HEAD": errors.New("no symref"),
			},
		}
		got, err := ResolveDefaultBranch(context.Background(), runner, "/repo", "  configured  ")
		if err != nil {
			t.Fatal(err)
		}
		if got != "configured" {
			t.Fatalf("got %q, want the trimmed fallback", got)
		}
	})

	t.Run("falls back to the current branch", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git branch --show-current": "current-branch\n"},
			Errors: map[string]error{
				"git ls-remote --symref origin HEAD":                errors.New("offline"),
				"git symbolic-ref --short refs/remotes/origin/HEAD": errors.New("no symref"),
			},
		}
		got, err := ResolveDefaultBranch(context.Background(), runner, "/repo", "")
		if err != nil {
			t.Fatal(err)
		}
		if got != "current-branch" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("errors when nothing resolves", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git ls-remote --symref origin HEAD":                errors.New("offline"),
				"git symbolic-ref --short refs/remotes/origin/HEAD": errors.New("no symref"),
				"git branch --show-current":                         errors.New("detached"),
			},
		}
		if _, err := ResolveDefaultBranch(context.Background(), runner, "/repo", ""); err == nil {
			t.Fatal("expected an error when no strategy resolves a branch")
		}
	})
}

func TestRemoteHEADBranch(t *testing.T) {
	t.Parallel()

	t.Run("parses the symref line", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git ls-remote --symref origin HEAD": "ref: refs/heads/main\tHEAD\nabc123\tHEAD\n",
			},
		}
		got, err := remoteHEADBranch(context.Background(), runner, "/repo")
		if err != nil {
			t.Fatal(err)
		}
		if got != "main" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("errors when no ref line is present", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{"git ls-remote --symref origin HEAD": "abc123\tHEAD\n"},
		}
		if _, err := remoteHEADBranch(context.Background(), runner, "/repo"); err == nil {
			t.Fatal("expected an error when HEAD reports no branch")
		}
	})

	t.Run("propagates git failures", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{"git ls-remote --symref origin HEAD": errors.New("offline")},
		}
		if _, err := remoteHEADBranch(context.Background(), runner, "/repo"); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestLocalRemoteHEADBranch(t *testing.T) {
	t.Parallel()

	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git symbolic-ref --short refs/remotes/origin/HEAD": "  origin/main  \n",
		},
	}
	got, err := localRemoteHEADBranch(context.Background(), runner, "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "main" {
		t.Fatalf("got %q, want the origin/ prefix stripped", got)
	}

	failing := testutil.FakeRunner{
		Errors: map[string]error{
			"git symbolic-ref --short refs/remotes/origin/HEAD": errors.New("no symref"),
		},
	}
	if _, err := localRemoteHEADBranch(context.Background(), failing, "/repo"); err == nil {
		t.Fatal("expected an error")
	}
}

// ls-remote reports a missing ref with exit 1 or 2 depending on version. Treating
// a transport failure as "absent" would make a pinned branch look deleted.
func TestRemoteBranchExists(t *testing.T) {
	t.Parallel()

	t.Run("exists", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Outputs: map[string]string{
				"git ls-remote --exit-code --heads origin b": "abc refs/heads/b",
			},
		}
		exists, err := remoteBranchExists(context.Background(), runner, "/repo", "b")
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
			exists, err := remoteBranchExists(context.Background(), runner, "/repo", "b")
			if err != nil || exists {
				t.Fatalf("exists=%v err=%v, want false/nil", exists, err)
			}
		})
	}

	t.Run("transport failures are errors", func(t *testing.T) {
		t.Parallel()

		runner := testutil.FakeRunner{
			Errors: map[string]error{
				"git ls-remote --exit-code --heads origin b": errors.New("fatal: could not read from remote repository"),
			},
		}
		if _, err := remoteBranchExists(context.Background(), runner, "/repo", "b"); err == nil {
			t.Fatal("expected an error rather than a false negative")
		}
	})
}

// Remote parsing feeds every gh call, so accepting a non-GitHub host or a
// malformed path would send API calls at the wrong repository.
func TestParseGitHubRepoTableCases(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"git@github.com:owner/repo.git":       "owner/repo",
		"git@github.com:owner/repo":           "owner/repo",
		"https://github.com/owner/repo.git":   "owner/repo",
		"https://github.com/owner/repo":       "owner/repo",
		"http://github.com/owner/repo":        "owner/repo",
		"ssh://git@github.com/owner/repo.git": "owner/repo",
		"https://GitHub.com/owner/repo":       "owner/repo",
		"  https://github.com/owner/repo  ":   "owner/repo",
	}
	for remote, want := range valid {
		got, err := ParseGitHubRepo(remote)
		if err != nil {
			t.Errorf("ParseGitHubRepo(%q): %v", remote, err)
			continue
		}
		if got != want {
			t.Errorf("ParseGitHubRepo(%q) = %q, want %q", remote, got, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"https://gitlab.com/owner/repo",
		"git@gitlab.com:owner/repo.git",
		"https://github.com/owner",
		"https://github.com/",
		"git@github.com:owner",
		"file:///local/path",
		"owner/repo",
	}
	for _, remote := range invalid {
		if _, err := ParseGitHubRepo(remote); err == nil {
			t.Errorf("ParseGitHubRepo(%q) should have failed", remote)
		}
	}
}

// Rewriting is how fork mode points a remote at the fork. The scheme has to be
// preserved so an ssh remote does not silently become https.
func TestRewriteGitHubRemoteTableCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		remote string
		slug   string
		want   string
	}{
		{remote: "git@github.com:owner/repo.git", slug: "fork/repo", want: "git@github.com:fork/repo.git"},
		{remote: "https://github.com/owner/repo.git", slug: "fork/repo", want: "https://github.com/fork/repo.git"},
		{remote: "http://github.com/owner/repo", slug: "fork/repo", want: "http://github.com/fork/repo.git"},
		{remote: "ssh://git@github.com/owner/repo.git", slug: "fork/repo", want: "ssh://git@github.com/fork/repo.git"},
	}
	for _, tt := range tests {
		got, err := RewriteGitHubRemote(tt.remote, tt.slug)
		if err != nil {
			t.Errorf("RewriteGitHubRemote(%q, %q): %v", tt.remote, tt.slug, err)
			continue
		}
		if got != tt.want {
			t.Errorf("RewriteGitHubRemote(%q, %q) = %q, want %q", tt.remote, tt.slug, got, tt.want)
		}
	}

	invalid := []struct {
		remote string
		slug   string
	}{
		{remote: "git@github.com:owner/repo.git", slug: ""},
		{remote: "git@github.com:owner/repo.git", slug: "   "},
		{remote: "git@github.com:owner/repo.git", slug: "not-a-slug"},
		{remote: "https://gitlab.com/owner/repo", slug: "fork/repo"},
		{remote: "file:///local", slug: "fork/repo"},
		{remote: "", slug: "fork/repo"},
	}
	for _, tt := range invalid {
		if _, err := RewriteGitHubRemote(tt.remote, tt.slug); err == nil {
			t.Errorf("RewriteGitHubRemote(%q, %q) should have failed", tt.remote, tt.slug)
		}
	}
}

func TestNormalizeGitHubPath(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"owner/repo":      "owner/repo",
		"owner/repo.git":  "owner/repo",
		"/owner/repo/":    "owner/repo",
		"/owner/repo.git": "owner/repo",
	}
	for input, want := range valid {
		got, err := normalizeGitHubPath(input)
		if err != nil {
			t.Errorf("normalizeGitHubPath(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeGitHubPath(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"", "owner", "owner/repo/extra", "/", "owner/", "/repo"} {
		if _, err := normalizeGitHubPath(input); err == nil {
			t.Errorf("normalizeGitHubPath(%q) should have failed", input)
		}
	}
}
