package fork

import (
	"context"
	"errors"
	"testing"

	"github.com/nicobistolfi/vigilante/internal/testutil"
)

func TestDiscoverContributingGuideFindsFirstMatch(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			testutil.Key("cat", "CONTRIBUTING.md"): "# Contributing\nPlease open a PR.",
		},
		Errors: map[string]error{
			testutil.Key("cat", "CONTRIBUTING"):            errors.New("no such file"),
			testutil.Key("cat", ".github/CONTRIBUTING.md"): errors.New("no such file"),
			testutil.Key("cat", ".github/CONTRIBUTING"):    errors.New("no such file"),
			testutil.Key("cat", "docs/CONTRIBUTING.md"):    errors.New("no such file"),
		},
	}
	content, path, err := DiscoverContributingGuide(context.Background(), runner, "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if path != "CONTRIBUTING.md" {
		t.Fatalf("expected path=CONTRIBUTING.md, got %q", path)
	}
	if content != "# Contributing\nPlease open a PR." {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestDiscoverContributingGuideFindsGitHubSubdir(t *testing.T) {
	runner := testutil.FakeRunner{
		Outputs: map[string]string{
			testutil.Key("cat", ".github/CONTRIBUTING.md"): "Follow the style guide.",
		},
		Errors: map[string]error{
			testutil.Key("cat", "CONTRIBUTING.md"):      errors.New("no such file"),
			testutil.Key("cat", "CONTRIBUTING"):         errors.New("no such file"),
			testutil.Key("cat", ".github/CONTRIBUTING"): errors.New("no such file"),
			testutil.Key("cat", "docs/CONTRIBUTING.md"): errors.New("no such file"),
		},
	}
	content, path, err := DiscoverContributingGuide(context.Background(), runner, "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if path != ".github/CONTRIBUTING.md" {
		t.Fatalf("expected .github/CONTRIBUTING.md, got %q", path)
	}
	if content != "Follow the style guide." {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestDiscoverContributingGuideReturnsEmptyWhenNoneFound(t *testing.T) {
	runner := testutil.FakeRunner{
		Errors: map[string]error{
			testutil.Key("cat", "CONTRIBUTING.md"):         errors.New("no such file"),
			testutil.Key("cat", "CONTRIBUTING"):            errors.New("no such file"),
			testutil.Key("cat", ".github/CONTRIBUTING.md"): errors.New("no such file"),
			testutil.Key("cat", ".github/CONTRIBUTING"):    errors.New("no such file"),
			testutil.Key("cat", "docs/CONTRIBUTING.md"):    errors.New("no such file"),
		},
	}
	content, path, err := DiscoverContributingGuide(context.Background(), runner, "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if content != "" || path != "" {
		t.Fatalf("expected empty result when no guide found, got content=%q path=%q", content, path)
	}
}

func TestFormatContributingPromptSectionWithContent(t *testing.T) {
	result := FormatContributingPromptSection("Run `make test` before submitting.", "CONTRIBUTING.md")
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if want := "Contributor guide discovered at `CONTRIBUTING.md`"; result[:len(want)] != want {
		t.Fatalf("unexpected prefix: %q", result[:80])
	}
}

func TestFormatContributingPromptSectionEmpty(t *testing.T) {
	result := FormatContributingPromptSection("", "")
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if want := "No contributor guide"; result[:len(want)] != want {
		t.Fatalf("unexpected: %q", result[:60])
	}
}
