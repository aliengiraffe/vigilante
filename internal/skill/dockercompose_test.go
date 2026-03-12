package skill

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestSelectComposeCommandPrefersDockerPlugin(t *testing.T) {
	cmd, err := SelectComposeCommand(testutil.FakeRunner{
		LookPaths: map[string]string{
			"docker":         "/usr/bin/docker",
			"docker-compose": "/usr/local/bin/docker-compose",
		},
	}.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd.Slice(), " "); got != "docker compose" {
		t.Fatalf("unexpected compose command: %s", got)
	}
}

func TestSelectComposeCommandFallsBackToLegacyBinary(t *testing.T) {
	cmd, err := SelectComposeCommand(testutil.FakeRunner{
		LookPaths: map[string]string{
			"docker-compose": "/usr/local/bin/docker-compose",
		},
	}.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmd.Slice(), " "); got != "docker-compose" {
		t.Fatalf("unexpected compose command: %s", got)
	}
}

func TestSelectComposeCommandFailsWithoutDocker(t *testing.T) {
	_, err := SelectComposeCommand(testutil.FakeRunner{}.LookPath)
	if err == nil || !strings.Contains(err.Error(), "neither docker nor docker-compose") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFindRepositoryComposeAssetPrefersKnownComposeNames(t *testing.T) {
	worktree := "/tmp/worktree"
	asset, ok := FindRepositoryComposeAsset(worktree, []string{
		filepath.Join(worktree, "ops", "docker-compose.yml"),
		filepath.Join(worktree, "compose.yaml"),
	})
	if !ok {
		t.Fatal("expected compose asset to be found")
	}
	if asset.FilePath != filepath.Join(worktree, "compose.yaml") {
		t.Fatalf("unexpected compose file: %s", asset.FilePath)
	}
	if asset.WorkingDir != worktree {
		t.Fatalf("unexpected working dir: %s", asset.WorkingDir)
	}
}

func TestBuildGeneratedComposePlanIncludesConnectionsAndCleanup(t *testing.T) {
	plan, err := BuildGeneratedComposePlan("/tmp/issue-66", []DatabaseService{DatabasePostgres, DatabaseMongoDB})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ComposeFile != "/tmp/issue-66/.vigilante/docker-compose.launch.yml" {
		t.Fatalf("unexpected compose file: %s", plan.ComposeFile)
	}
	if len(plan.Services) != 2 {
		t.Fatalf("unexpected service count: %d", len(plan.Services))
	}
	if !strings.Contains(plan.ComposeYAML, "image: postgres:16") {
		t.Fatalf("compose yaml missing postgres image: %s", plan.ComposeYAML)
	}
	if !strings.Contains(plan.ComposeYAML, "image: mongo:7") {
		t.Fatalf("compose yaml missing mongo image: %s", plan.ComposeYAML)
	}
	if !strings.Contains(plan.CleanupExpectation, "down -v") {
		t.Fatalf("cleanup expectation missing down command: %s", plan.CleanupExpectation)
	}
	if plan.Connections[0].ConnectionURI == "" || plan.Connections[1].ConnectionURI == "" {
		t.Fatalf("expected connection URIs: %#v", plan.Connections)
	}
	if !strings.Contains(plan.ProjectName, "issue-66-") {
		t.Fatalf("unexpected project name: %s", plan.ProjectName)
	}
}

func TestBuildGeneratedComposePlanRejectsUnsupportedServices(t *testing.T) {
	_, err := BuildGeneratedComposePlan("/tmp/issue-66", []DatabaseService{"redis"})
	if err == nil {
		t.Fatal("expected unsupported service error")
	}
}

func TestBuildGeneratedComposePlanRequiresAtLeastOneService(t *testing.T) {
	_, err := BuildGeneratedComposePlan("/tmp/issue-66", nil)
	if err == nil || !strings.Contains(err.Error(), "at least one database service") {
		t.Fatalf("unexpected error: %v", err)
	}
}
