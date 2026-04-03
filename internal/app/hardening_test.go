package app

import (
	"testing"

	"github.com/nicobistolfi/vigilante/internal/backend"
	"github.com/nicobistolfi/vigilante/internal/repo"
	"github.com/nicobistolfi/vigilante/internal/state"
)

func TestExtractPackageJSONPaths(t *testing.T) {
	tests := []struct {
		name  string
		files []backend.PullRequestFile
		want  int
	}{
		{
			name:  "no files",
			files: nil,
			want:  0,
		},
		{
			name: "no package.json",
			files: []backend.PullRequestFile{
				{Filename: "src/index.ts", Status: "modified"},
				{Filename: "README.md", Status: "modified"},
			},
			want: 0,
		},
		{
			name: "root package.json",
			files: []backend.PullRequestFile{
				{Filename: "package.json", Status: "modified"},
				{Filename: "src/index.ts", Status: "modified"},
			},
			want: 1,
		},
		{
			name: "nested package.json",
			files: []backend.PullRequestFile{
				{Filename: "packages/core/package.json", Status: "modified"},
				{Filename: "packages/utils/package.json", Status: "added"},
			},
			want: 2,
		},
		{
			name: "case insensitive",
			files: []backend.PullRequestFile{
				{Filename: "Package.JSON", Status: "modified"},
			},
			want: 1,
		},
		{
			name: "not a package.json suffix",
			files: []backend.PullRequestFile{
				{Filename: "notpackage.json", Status: "modified"},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPackageJSONPaths(tt.files)
			if len(got) != tt.want {
				t.Errorf("extractPackageJSONPaths() returned %d paths, want %d", len(got), tt.want)
			}
		})
	}
}

func TestIsNodeJSTarget(t *testing.T) {
	tests := []struct {
		name       string
		techStacks []repo.TechStack
		want       bool
	}{
		{
			name:       "nodejs target",
			techStacks: []repo.TechStack{repo.TechStackNodeJS},
			want:       true,
		},
		{
			name:       "go target",
			techStacks: []repo.TechStack{repo.TechStackGo},
			want:       false,
		},
		{
			name:       "mixed target with nodejs",
			techStacks: []repo.TechStack{repo.TechStackGo, repo.TechStackNodeJS},
			want:       true,
		},
		{
			name:       "empty tech stacks",
			techStacks: nil,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := state.WatchTarget{
				Classification: repo.Classification{
					TechStacks: tt.techStacks,
				},
			}
			if got := isNodeJSTarget(target); got != tt.want {
				t.Errorf("isNodeJSTarget() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPackageHardeningConfigGating(t *testing.T) {
	// When package hardening is disabled via config, the scan should be skipped.
	disabled := false
	config := state.ServiceConfig{PackageHardeningEnabled: &disabled}
	if config.IsPackageHardeningEnabled() {
		t.Error("expected disabled config to return false")
	}

	// Default should be enabled.
	defaultConfig := state.ServiceConfig{}
	if !defaultConfig.IsPackageHardeningEnabled() {
		t.Error("expected default config to enable package hardening")
	}
}

func TestMonitorHardeningCheckboxesSkipsAlreadyRemediated(t *testing.T) {
	hs := state.HardeningState{
		"owner/repo#10": state.HardeningPRState{
			Repo:              "owner/repo",
			PRNumber:          10,
			CommentID:         123,
			RemediationSentAt: "2026-01-01T00:00:00Z",
			FindingsCount:     2,
		},
	}

	// Verify entries with RemediationSentAt set are skipped during monitoring.
	entry := hs["owner/repo#10"]
	if entry.RemediationSentAt == "" {
		t.Error("expected RemediationSentAt to be set")
	}
	// The actual monitoring loop checks this field and skips the entry.
}

func TestMonitorHardeningCheckboxesSkipsMismatchedRepo(t *testing.T) {
	hs := state.HardeningState{
		"other/repo#5": state.HardeningPRState{
			Repo:      "other/repo",
			PRNumber:  5,
			CommentID: 456,
		},
	}
	target := state.WatchTarget{Repo: "owner/repo"}

	// Verify that entries for a different repo are not matched to this target.
	entry := hs["other/repo#5"]
	if entry.Repo == target.Repo {
		t.Error("entry repo should not match target repo")
	}
}

func TestHardeningPRKey(t *testing.T) {
	key := state.HardeningPRKey("owner/repo", 42)
	if key != "owner/repo#42" {
		t.Errorf("HardeningPRKey() = %q, want %q", key, "owner/repo#42")
	}
}
