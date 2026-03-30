package fork

import (
	"context"
	"errors"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/state"
	"github.com/nicobistolfi/vigilante/internal/testutil"
)

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
