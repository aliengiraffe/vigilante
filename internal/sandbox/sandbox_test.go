package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls []string
	out   string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, _ string, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.out, f.err
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	return "/usr/bin/" + file, nil
}

func TestGenerateSSHKeyPairCallsSSHKeygen(t *testing.T) {
	r := &fakeRunner{out: ""}
	dir := t.TempDir()

	// ssh-keygen won't actually create the file with our fake runner,
	// so the read will fail. We verify the runner was called correctly.
	_, err := generateSSHKeyPair(context.Background(), r, dir)
	if err == nil {
		t.Fatal("expected error because fake runner does not create files")
	}
	if len(r.calls) == 0 {
		t.Fatal("expected ssh-keygen call")
	}
	if !strings.Contains(r.calls[0], "ssh-keygen") {
		t.Errorf("expected ssh-keygen command, got: %s", r.calls[0])
	}
	if !strings.Contains(r.calls[0], "ed25519") {
		t.Errorf("expected ed25519 key type, got: %s", r.calls[0])
	}
}

func TestWorktreeGitdirMountReturnsParentGitForSeparateWorktree(t *testing.T) {
	repoPath := t.TempDir()
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(repoPath, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mount, ok := worktreeGitdirMount(repoPath, worktreePath)
	if !ok {
		t.Fatal("expected mount when worktree is a separate path")
	}
	if mount.Source != gitDir || mount.Target != gitDir {
		t.Errorf("expected mount source/target = %s, got src=%s tgt=%s", gitDir, mount.Source, mount.Target)
	}
}

func TestWorktreeGitdirMountSkipsWhenRepoEqualsWorktree(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := worktreeGitdirMount(repoPath, repoPath); ok {
		t.Error("expected no mount when worktree is the repo itself")
	}
}

func TestWorktreeGitdirMountSkipsWhenGitDirMissing(t *testing.T) {
	repoPath := t.TempDir()
	worktreePath := filepath.Join(repoPath, ".worktrees", "vigilante", "issue-1")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := worktreeGitdirMount(repoPath, worktreePath); ok {
		t.Error("expected no mount when parent .git directory is missing")
	}
}
