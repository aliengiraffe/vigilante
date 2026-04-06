package container

import (
	"context"
	"fmt"
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

func TestCreateBuildsDockerCommand(t *testing.T) {
	r := &fakeRunner{out: "abc123\n"}
	cfg := Config{
		Image:        "vigilante-sandbox:latest",
		Name:         "vigilante-sandbox-sbx_test",
		WorktreePath: "/home/user/repo/.worktrees/vigilante/issue-1",
		SSHKeyPath:   "/tmp/ssh/id_ed25519",
		EnvVars: map[string]string{
			"VIGILANTE_SESSION_ID": "sbx_test",
		},
		MemoryLimit: "8g",
		CPUs:        "4",
		ProxyPort:   9821,
		EnableDinD:  true,
	}

	id, err := Create(context.Background(), r, cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "abc123" {
		t.Errorf("id = %q, want %q", id, "abc123")
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	cmd := r.calls[0]
	for _, want := range []string{
		"docker create",
		"--name vigilante-sandbox-sbx_test",
		"--privileged",
		"--memory 8g",
		"--cpus 4",
		"/workspace",
		"vigilante-sandbox:latest",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q:\n%s", want, cmd)
		}
	}
}

func TestStopCallsDockerStop(t *testing.T) {
	r := &fakeRunner{}
	err := Stop(context.Background(), r, "test-container", 10)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	if !strings.Contains(r.calls[0], "docker stop -t 10 test-container") {
		t.Errorf("unexpected command: %s", r.calls[0])
	}
}

func TestRemoveCallsDockerRm(t *testing.T) {
	r := &fakeRunner{}
	err := Remove(context.Background(), r, "test-container")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !strings.Contains(r.calls[0], "docker rm --volumes test-container") {
		t.Errorf("unexpected command: %s", r.calls[0])
	}
}

func TestIsRunningParsesOutput(t *testing.T) {
	r := &fakeRunner{out: "true\n"}
	running, err := IsRunning(context.Background(), r, "c1")
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if !running {
		t.Error("expected running=true")
	}
}

func TestIsRunningReturnsFalseForMissing(t *testing.T) {
	r := &fakeRunner{err: fmt.Errorf("No such object")}
	running, err := IsRunning(context.Background(), r, "missing")
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Error("expected running=false for missing container")
	}
}
