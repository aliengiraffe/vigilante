package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerSessionLifecycleHelpers(t *testing.T) {
	r := &fakeRunner{}
	m, err := NewManager(r, slog.Default(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if m.ProxyAddr() != "" {
		t.Fatalf("unstarted proxy=%q", m.ProxyAddr())
	}
	if err := m.StartProxy("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if m.ProxyAddr() == "" {
		t.Fatal("started proxy has no address")
	}
	defer m.StopProxy(context.Background())
	sess := &Session{ID: "s1", ContainerName: "container-1", Status: "provisioned"}
	m.sessions[sess.ID] = sess
	if got, ok := m.GetSession("s1"); !ok || got != sess {
		t.Fatalf("session=%#v ok=%v", got, ok)
	}
	if ids := m.ActiveSessions(); len(ids) != 1 || ids[0] != "s1" {
		t.Fatalf("active=%#v", ids)
	}
	if err := m.Start(context.Background(), "s1"); err != nil || sess.Status != "running" {
		t.Fatalf("status=%q err=%v calls=%#v", sess.Status, err, r.calls)
	}
	if err := m.StopContainer(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background(), "missing"); err == nil {
		t.Fatal("expected missing start error")
	}
	if err := m.StopContainer(context.Background(), "missing"); err == nil {
		t.Fatal("expected missing stop error")
	}
}

func TestManagerTeardownAndReconcile(t *testing.T) {
	r := &fakeRunner{}
	m, err := NewManager(r, slog.Default(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(t.TempDir(), "ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sess := &Session{ID: "s1", ContainerName: "vigilante-sandbox-s1", SSHKeyDir: sshDir, Status: "running"}
	m.sessions[sess.ID] = sess
	if err := m.Teardown(context.Background(), "s1", "test complete"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.GetSession("s1"); ok || sess.Status != "terminated" {
		t.Fatalf("session retained status=%q", sess.Status)
	}
	if _, err := os.Stat(sshDir); !os.IsNotExist(err) {
		t.Fatalf("ssh dir still exists: %v", err)
	}
	if err := m.Teardown(context.Background(), "missing", "test"); err == nil {
		t.Fatal("expected missing teardown error")
	}
	r.out = "vigilante-sandbox-orphan\n"
	if err := m.ReconcileStale(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.calls, "\n")
	if !strings.Contains(joined, "docker stop") || !strings.Contains(joined, "docker rm") {
		t.Fatalf("calls=%s", joined)
	}
}

func TestManagerRunnerErrorPaths(t *testing.T) {
	r := &fakeRunner{err: fmt.Errorf("docker failed")}
	m, err := NewManager(r, slog.Default(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.sessions["s1"] = &Session{ID: "s1", ContainerName: "c1"}
	if err := m.Start(context.Background(), "s1"); err == nil {
		t.Fatal("expected start error")
	}
	if err := m.StopContainer(context.Background(), "s1"); err == nil {
		t.Fatal("expected stop error")
	}
	if err := m.ReconcileStale(context.Background()); err == nil || !strings.Contains(err.Error(), "reconcile stale") {
		t.Fatalf("error=%v", err)
	}
}

func TestProxyPortBoundaries(t *testing.T) {
	for input, want := range map[string]int{"": 0, "invalid": 0, "127.0.0.1:notaport": 0, "127.0.0.1:9821": 9821} {
		if got := proxyPort(input); got != want {
			t.Errorf("proxyPort(%q)=%d want %d", input, got, want)
		}
	}
}

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
