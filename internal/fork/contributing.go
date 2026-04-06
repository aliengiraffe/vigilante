package fork

import (
	"context"
	"fmt"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/environment"
)

// contributingGuidePaths lists the standard locations for contribution
// guidelines in a GitHub repository, checked in priority order.
var contributingGuidePaths = []string{
	"CONTRIBUTING.md",
	"CONTRIBUTING",
	".github/CONTRIBUTING.md",
	".github/CONTRIBUTING",
	"docs/CONTRIBUTING.md",
}

// DiscoverContributingGuide reads the first contributor guide found at a
// standard location in the repository rooted at repoPath.  Returns the
// file contents and the relative path where it was found, or empty strings
// when no guide is present.
func DiscoverContributingGuide(ctx context.Context, runner environment.Runner, repoPath string) (content string, path string, err error) {
	for _, candidate := range contributingGuidePaths {
		out, readErr := runner.Run(ctx, repoPath, "cat", candidate)
		if readErr != nil {
			continue
		}
		trimmed := strings.TrimSpace(out)
		if trimmed == "" {
			continue
		}
		return trimmed, candidate, nil
	}
	return "", "", nil
}

// FormatContributingPromptSection returns a prompt block that includes the
// contributor guide contents, or an explicit note that no guide was found.
func FormatContributingPromptSection(content string, path string) string {
	if content == "" {
		return "No contributor guide (CONTRIBUTING.md or similar) was found in this repository. Follow general open-source contribution best practices."
	}
	return fmt.Sprintf("Contributor guide discovered at `%s`. Follow these contribution guidelines on a best-effort basis while keeping Vigilante's operational safety rules authoritative:\n\n%s", path, content)
}
