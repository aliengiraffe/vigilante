package sandbox

import (
	"context"
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
