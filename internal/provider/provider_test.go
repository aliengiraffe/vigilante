package provider

import "testing"

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
