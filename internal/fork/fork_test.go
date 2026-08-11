package fork

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestAuthenticatedOwner(t *testing.T) {
	owner, err := AuthenticatedOwner(context.Background(), testutil.FakeRunner{Outputs: map[string]string{"gh api user": `{"login":" alice "}`}})
	if err != nil || owner != "alice" {
		t.Fatalf("owner=%q err=%v", owner, err)
	}
	for _, output := range []string{"not-json", `{"login":""}`} {
		if _, err := AuthenticatedOwner(context.Background(), testutil.FakeRunner{Outputs: map[string]string{"gh api user": output}}); err == nil {
			t.Fatalf("expected error for %q", output)
		}
	}
	want := errors.New("auth failed")
	if _, err := AuthenticatedOwner(context.Background(), testutil.FakeRunner{Errors: map[string]error{"gh api user": want}}); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

func TestEnsureForkExistingRepositoryPaths(t *testing.T) {
	base := map[string]string{"gh api repos/alice/repo": `{"full_name":"alice/repo","parent":{"full_name":"owner/repo"}}`}
	if err := EnsureFork(context.Background(), testutil.FakeRunner{Outputs: base}, "owner/repo", "alice/repo", "alice", "alice"); err != nil {
		t.Fatal(err)
	}
	wrong := map[string]string{"gh api repos/alice/repo": `{"full_name":"alice/repo","parent":{"full_name":"other/repo"}}`}
	if err := EnsureFork(context.Background(), testutil.FakeRunner{Outputs: wrong}, "owner/repo", "alice/repo", "alice", "alice"); err == nil || !strings.Contains(err.Error(), "not a fork") {
		t.Fatalf("error=%v", err)
	}
	broken := map[string]string{"gh api repos/alice/repo": `not-json`}
	if err := EnsureFork(context.Background(), testutil.FakeRunner{Outputs: broken}, "owner/repo", "alice/repo", "alice", "alice"); err == nil || !strings.Contains(err.Error(), "parse gh api repos") {
		t.Fatalf("error=%v", err)
	}
}

func TestRepositoryNotFoundClassification(t *testing.T) {
	for _, err := range []error{errors.New("HTTP 404"), errors.New("repository Not Found")} {
		if !isRepositoryNotFound(err) {
			t.Errorf("not classified: %v", err)
		}
	}
	for _, err := range []error{nil, errors.New("permission denied")} {
		if isRepositoryNotFound(err) {
			t.Errorf("misclassified: %v", err)
		}
	}
}

func TestPrepareTargetNoForkIsNoop(t *testing.T) {
	target := state.WatchTarget{Repo: "owner/repo"}
	got, err := PrepareTarget(context.Background(), testutil.FakeRunner{}, target)
	if err != nil || got.Repo != target.Repo || got.ForkMode != target.ForkMode {
		t.Fatalf("target=%#v err=%v", got, err)
	}
}

func TestEnsureForkCreatesOrganizationFork(t *testing.T) {
	r := &createForkRunner{}
	if err := EnsureFork(context.Background(), r, "owner/repo", "acme/repo", "alice", "acme"); err != nil {
		t.Fatal(err)
	}
	if !r.posted || r.lookups != 2 {
		t.Fatalf("posted=%v lookups=%d calls=%#v", r.posted, r.lookups, r.calls)
	}
	want := "gh api --method POST repos/owner/repo/forks -f organization=acme"
	if r.calls[1] != want {
		t.Fatalf("create call=%q want %q", r.calls[1], want)
	}
}

func TestConfigureWorktreeNoopAndErrors(t *testing.T) {
	if err := ConfigureWorktree(context.Background(), testutil.FakeRunner{}, state.Session{}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"git config branch.branch.remote fork",
		"git config branch.branch.merge refs/heads/branch",
		"git config branch.branch.pushRemote fork",
		"git config branch.branch.gh-merge-base main",
	} {
		r := testutil.FakeRunner{Outputs: map[string]string{
			"git config branch.branch.remote fork": "ok", "git config branch.branch.merge refs/heads/branch": "ok",
			"git config branch.branch.pushRemote fork": "ok", "git config branch.branch.gh-merge-base main": "ok",
		}, Errors: map[string]error{command: errors.New("config failed")}}
		if err := ConfigureWorktree(context.Background(), r, state.Session{WorktreePath: "/tmp", Branch: "branch", PushRemote: "fork", BaseBranch: "main"}); err == nil {
			t.Errorf("expected error at %q", command)
		}
	}
}

type createForkRunner struct {
	posted  bool
	lookups int
	calls   []string
}

func (r *createForkRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	key := testutil.Key(name, args...)
	r.calls = append(r.calls, key)
	if key == "gh api repos/acme/repo" {
		r.lookups++
		if !r.posted {
			return "HTTP 404: Not Found", errors.New("HTTP 404: Not Found")
		}
		return `{"full_name":"acme/repo","parent":{"full_name":"owner/repo"}}`, nil
	}
	if key == "gh api --method POST repos/owner/repo/forks -f organization=acme" {
		r.posted = true
		return "", nil
	}
	return "", fmt.Errorf("unexpected command: %s", key)
}
func (*createForkRunner) LookPath(file string) (string, error) { return file, nil }

func TestPrepareTargetReusesExistingForkAndConfiguresRemote(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"gh api user":                                        `{"login":"forker"}`,
			"gh api repos/forker/repo":                           `{"full_name":"forker/repo","parent":{"full_name":"owner/repo"}}`,
			"git remote get-url origin":                          "git@github.com:owner/repo.git\n",
			"git remote get-url fork":                            "",
			"git remote add fork git@github.com:forker/repo.git": "ok",
		},
		Errors: map[string]error{
			"git remote get-url fork": errors.New("exit status 2: no such remote"),
		},
		ErrorOutputs: map[string]string{
			"git remote get-url fork": "error: No such remote 'fork'\n",
		},
	}

	target, err := PrepareTarget(context.Background(), runner, state.WatchTarget{
		Path:     "/tmp/repo",
		Repo:     "owner/repo",
		ForkMode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if target.ForkOwner != "forker" {
		t.Fatalf("unexpected fork owner: %#v", target)
	}
	if target.PushRemote != RemoteName || target.PushRepo != "forker/repo" {
		t.Fatalf("unexpected push target: %#v", target)
	}
}

func TestConfigureWorktreeSetsForkPushDefaults(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			"git config branch.vigilante/issue-7.remote fork":                        "ok",
			"git config branch.vigilante/issue-7.merge refs/heads/vigilante/issue-7": "ok",
			"git config branch.vigilante/issue-7.pushRemote fork":                    "ok",
			"git config branch.vigilante/issue-7.gh-merge-base main":                 "ok",
		},
	}
	err := ConfigureWorktree(context.Background(), runner, state.Session{
		WorktreePath: "/tmp/worktree",
		Branch:       "vigilante/issue-7",
		BaseBranch:   "main",
		PushRemote:   RemoteName,
	})
	if err != nil {
		t.Fatal(err)
	}
}
