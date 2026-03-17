package ghcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type labelManifest struct {
	Labels []labelSpec `json:"labels"`
}

type labelSpec struct {
	Name        string   `json:"name"`
	Color       string   `json:"color"`
	Group       string   `json:"group"`
	Behavior    string   `json:"behavior"`
	Description string   `json:"description"`
	AliasFor    string   `json:"alias_for"`
	Notes       []string `json:"notes"`
}

func TestIssueLabelManifestDefinesExpectedGroups(t *testing.T) {
	manifest := loadLabelManifest(t)

	if len(manifest.Labels) == 0 {
		t.Fatal("expected label manifest entries")
	}

	seenNames := map[string]bool{}
	groups := map[string]int{}
	for _, label := range manifest.Labels {
		if label.Name == "" {
			t.Fatal("expected every label to have a name")
		}
		if seenNames[label.Name] {
			t.Fatalf("duplicate label name %q", label.Name)
		}
		seenNames[label.Name] = true
		if label.Group == "" {
			t.Fatalf("expected label %q to declare a group", label.Name)
		}
		if label.Behavior != "informational" && label.Behavior != "control" {
			t.Fatalf("unexpected behavior for %q: %q", label.Name, label.Behavior)
		}
		if label.Description == "" {
			t.Fatalf("expected label %q to include a description", label.Name)
		}
		groups[label.Group]++
	}

	for _, group := range []string{"execution-state", "intervention", "provider-routing", "control"} {
		if groups[group] == 0 {
			t.Fatalf("expected at least one label in group %q", group)
		}
	}
}

func TestIssueLabelManifestKeepsCompatibilityControls(t *testing.T) {
	manifest := loadLabelManifest(t)

	labels := map[string]labelSpec{}
	for _, label := range manifest.Labels {
		labels[label.Name] = label
	}

	for _, provider := range []string{"codex", "claude", "gemini"} {
		label, ok := labels[provider]
		if !ok {
			t.Fatalf("expected provider label %q", provider)
		}
		if label.Group != "provider-routing" || label.Behavior != "control" {
			t.Fatalf("expected provider label %q to remain a control routing label: %#v", provider, label)
		}
	}

	resume, ok := labels["vigilante:resume"]
	if !ok {
		t.Fatal("expected vigilante:resume in label manifest")
	}
	if resume.Group != "control" || resume.Behavior != "control" {
		t.Fatalf("expected vigilante:resume to be a control label: %#v", resume)
	}

	legacy, ok := labels["resume"]
	if !ok {
		t.Fatal("expected legacy resume alias in label manifest")
	}
	if legacy.AliasFor != "vigilante:resume" {
		t.Fatalf("expected resume alias to target vigilante:resume, got %#v", legacy)
	}
}

func TestIssueLabelManifestIncludesHumanReviewStates(t *testing.T) {
	manifest := loadLabelManifest(t)

	labels := map[string]labelSpec{}
	for _, label := range manifest.Labels {
		labels[label.Name] = label
	}

	for _, name := range []string{
		"vigilante:ready-for-review",
		"vigilante:awaiting-user-validation",
		"vigilante:needs-review",
		"vigilante:needs-human-input",
	} {
		label, ok := labels[name]
		if !ok {
			t.Fatalf("expected review or human-input label %q", name)
		}
		if label.Behavior != "informational" {
			t.Fatalf("expected %q to stay informational, got %#v", name, label)
		}
	}
}

func TestLoadRepositoryLabelSpecsUsesCanonicalManifest(t *testing.T) {
	manifest := loadLabelManifest(t)

	labels, err := LoadRepositoryLabelSpecs()
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != len(manifest.Labels) {
		t.Fatalf("expected %d labels from embedded manifest, got %d", len(manifest.Labels), len(labels))
	}

	for i, label := range labels {
		if label.Name != manifest.Labels[i].Name {
			t.Fatalf("unexpected label at index %d: %#v", i, label)
		}
		if label.Color != manifest.Labels[i].Color {
			t.Fatalf("unexpected color for %q: %#v", label.Name, label)
		}
		if label.Description != manifest.Labels[i].Description {
			t.Fatalf("unexpected description for %q: %#v", label.Name, label)
		}
	}
}

func loadLabelManifest(t *testing.T) labelManifest {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", ".github", "labels.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read label manifest: %v", err)
	}

	var manifest labelManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parse label manifest: %v", err)
	}
	return manifest
}
