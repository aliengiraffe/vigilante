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
		Mounts: []Mount{
			{Source: "/host/codex", Target: "/root/.codex", ReadOnly: false},
			{Source: "/host/gitconfig", Target: "/root/.gitconfig", ReadOnly: true},
		},
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
		"/host/codex:/root/.codex",
		"/host/gitconfig:/root/.gitconfig:ro",
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

func TestContainerLifecycleAndInspection(t *testing.T) {
	ctx := context.Background()
	r := &fakeRunner{out: "started"}
	if err := Start(ctx, r, "c1"); err != nil || r.calls[0] != "docker start c1" {
		t.Fatalf("Start calls=%#v err=%v", r.calls, err)
	}
	r = &fakeRunner{out: "command output"}
	if got, err := Exec(ctx, r, "c1", []string{"go", "test", "./..."}); err != nil || got != "command output" || r.calls[0] != "docker exec c1 go test ./..." {
		t.Fatalf("Exec=%q calls=%#v err=%v", got, r.calls, err)
	}
	r = &fakeRunner{out: "id\n"}
	if exists, err := Exists(ctx, r, "c1"); err != nil || !exists {
		t.Fatalf("Exists=%v err=%v", exists, err)
	}
	r = &fakeRunner{err: fmt.Errorf("No such object")}
	if exists, err := Exists(ctx, r, "missing"); err != nil || exists {
		t.Fatalf("missing Exists=%v err=%v", exists, err)
	}
	r = &fakeRunner{out: "vigilante-sandbox-a\nvigilante-sandbox-b\n"}
	if got, err := ListSandboxContainers(ctx, r); err != nil || len(got) != 2 || got[1] != "vigilante-sandbox-b" {
		t.Fatalf("containers=%#v err=%v", got, err)
	}
	r = &fakeRunner{out: "  "}
	if got, err := ListSandboxContainers(ctx, r); err != nil || got != nil {
		t.Fatalf("empty containers=%#v err=%v", got, err)
	}
	r = &fakeRunner{out: `[{"State":{"Status":"running"},"Name":"c1"}]`}
	if got, err := InspectStatus(ctx, r, "c1"); err != nil || got["Name"] != "c1" {
		t.Fatalf("inspect=%#v err=%v", got, err)
	}
	for _, output := range []string{"not-json", "[]"} {
		if _, err := InspectStatus(ctx, &fakeRunner{out: output}, "c1"); err == nil {
			t.Fatalf("expected inspect error for %q", output)
		}
	}
}

func TestContainerErrorPaths(t *testing.T) {
	ctx := context.Background()
	boom := fmt.Errorf("docker unavailable")
	for name, fn := range map[string]func(*fakeRunner) error{
		"create": func(r *fakeRunner) error {
			_, err := Create(ctx, r, Config{Name: "c", WorktreePath: "/tmp/repo", Image: "image"})
			return err
		},
		"start":   func(r *fakeRunner) error { return Start(ctx, r, "c") },
		"stop":    func(r *fakeRunner) error { return Stop(ctx, r, "c", 5) },
		"remove":  func(r *fakeRunner) error { return Remove(ctx, r, "c") },
		"list":    func(r *fakeRunner) error { _, err := ListSandboxContainers(ctx, r); return err },
		"inspect": func(r *fakeRunner) error { _, err := InspectStatus(ctx, r, "c"); return err },
	} {
		if err := fn(&fakeRunner{out: "details", err: boom}); err == nil || !strings.Contains(err.Error(), "docker") {
			t.Errorf("%s error=%v", name, err)
		}
	}
	if running, err := IsRunning(ctx, &fakeRunner{err: boom}, "c"); err == nil || running {
		t.Fatalf("IsRunning=%v err=%v", running, err)
	}
	if exists, err := Exists(ctx, &fakeRunner{err: boom}, "c"); err == nil || exists {
		t.Fatalf("Exists=%v err=%v", exists, err)
	}
}

func TestExtractArtifactsIncludesSuccessAndFailure(t *testing.T) {
	r := &sequenceRunner{outputs: []string{"abc commit", "", "main"}, errors: []error{nil, fmt.Errorf("diff failed"), nil}}
	got, err := ExtractArtifacts(context.Background(), r, "c1")
	if err != nil || !strings.Contains(got, "abc commit") || !strings.Contains(got, "git failed: diff failed") || !strings.Contains(got, "main") {
		t.Fatalf("artifacts=%q err=%v", got, err)
	}
}

type sequenceRunner struct {
	outputs []string
	errors  []error
	call    int
}

func (r *sequenceRunner) Run(_ context.Context, _ string, _ string, _ ...string) (string, error) {
	i := r.call
	r.call++
	return r.outputs[i], r.errors[i]
}
func (*sequenceRunner) LookPath(file string) (string, error) { return file, nil }
