package provider

import (
	"testing"

	ghcli "github.com/nicobistolfi/vigilante/internal/github"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestResolveDefaultsToCodex(t *testing.T) {
	selectedProvider, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if selectedProvider.ID() != DefaultID {
		t.Fatalf("unexpected provider id: %s", selectedProvider.ID())
	}
}

func TestRequiredToolsetIncludesSharedAndProviderTools(t *testing.T) {
	selectedProvider, err := Resolve(DefaultID)
	if err != nil {
		t.Fatal(err)
	}
	got := RequiredToolset(selectedProvider)
	want := []string{"codex", "gh", "git"}
	if len(got) != len(want) {
		t.Fatalf("unexpected tool count: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected toolset: %#v", got)
		}
	}
}

func TestResolveIssueLabelUsesRegisteredProviderIDs(t *testing.T) {
	original := registry
	registry = map[string]Provider{
		DefaultID: codexProvider{},
		"cursor":  testProvider{id: "cursor"},
	}
	t.Cleanup(func() {
		registry = original
	})

	selected, err := ResolveIssueLabel([]ghcli.Label{{Name: "cursor"}})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "cursor" {
		t.Fatalf("unexpected provider label match: %q", selected)
	}
}

func TestResolveIssueLabelRejectsConflictingProviderLabels(t *testing.T) {
	original := registry
	registry = map[string]Provider{
		DefaultID: codexProvider{},
		"cursor":  testProvider{id: "cursor"},
	}
	t.Cleanup(func() {
		registry = original
	})

	_, err := ResolveIssueLabel([]ghcli.Label{{Name: DefaultID}, {Name: "cursor"}})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := err.Error(); got != "multiple provider labels: codex, cursor" {
		t.Fatalf("unexpected conflict error: %s", got)
	}
}

type testProvider struct {
	id string
}

func (p testProvider) ID() string {
	return p.id
}

func (p testProvider) DisplayName() string {
	return p.id
}

func (p testProvider) RequiredTools() []string {
	return nil
}

func (p testProvider) EnsureRuntimeInstalled(store *state.Store) error {
	return nil
}

func (p testProvider) BuildIssuePreflightInvocation(task IssueTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildIssueInvocation(task IssueTask) (Invocation, error) {
	return Invocation{}, nil
}

func (p testProvider) BuildConflictResolutionInvocation(task ConflictTask) (Invocation, error) {
	return Invocation{}, nil
}
