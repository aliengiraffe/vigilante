package ghcli

import (
	"io"
	"os"
	"strings"
)

var prohibitedAttributionPhrases = []string{
	"generated with",
	"created with",
	"written by",
	"authored by",
	"co-authored by",
}

var prohibitedAgentNames = []string{
	"claude code",
	"claude",
	"codex",
	"gemini cli",
	"gemini",
}

func SanitizeGitHubVisibleText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return strings.TrimSpace(text)
	}

	lines := strings.Split(text, "\n")
	sanitized := make([]string, 0, len(lines))
	blankRun := 0
	for _, line := range lines {
		if isProhibitedAttributionLine(line) {
			continue
		}
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > 1 {
				continue
			}
		} else {
			blankRun = 0
		}
		sanitized = append(sanitized, line)
	}
	return strings.TrimSpace(strings.Join(sanitized, "\n"))
}

func SanitizeProxyInvocation(tool string, args []string, stdin io.Reader) ([]string, io.Reader, error) {
	sanitizedArgs := append([]string(nil), args...)
	currentStdin := stdin

	switch tool {
	case "gh":
		if isGitHubBodyCommand(sanitizedArgs) {
			return sanitizeBodyFlags(sanitizedArgs, currentStdin, []string{"--body"}, []string{"--body-file"})
		}
		if isGitHubAPIBodyCommand(sanitizedArgs) {
			return sanitizeAPIBodyFields(sanitizedArgs), currentStdin, nil
		}
	case "git":
		if isGitCommitCommand(sanitizedArgs) {
			return sanitizeBodyFlags(sanitizedArgs, currentStdin, []string{"-m", "--message"}, []string{"-F", "--file"})
		}
	}

	return sanitizedArgs, currentStdin, nil
}

func isProhibitedAttributionLine(line string) bool {
	normalized := normalizeAttributionLine(line)
	if normalized == "" {
		return false
	}
	hasPhrase := false
	for _, phrase := range prohibitedAttributionPhrases {
		if strings.Contains(normalized, phrase) {
			hasPhrase = true
			break
		}
	}
	if !hasPhrase {
		return false
	}
	for _, agent := range prohibitedAgentNames {
		if strings.Contains(normalized, agent) {
			return true
		}
	}
	return false
}

func normalizeAttributionLine(line string) string {
	line = strings.TrimSpace(strings.ToLower(line))
	replacer := strings.NewReplacer("`", "", "*", "", "_", "", "#", "", ">", "", "-", " ", ":", " ", "\t", " ")
	line = replacer.Replace(line)
	return strings.Join(strings.Fields(line), " ")
}

func sanitizeBodyFlags(args []string, stdin io.Reader, inlineFlags []string, fileFlags []string) ([]string, io.Reader, error) {
	sanitizedArgs := make([]string, 0, len(args))
	currentStdin := stdin

	for i := 0; i < len(args); i++ {
		token := args[i]

		if flag, value, ok := splitInlineFlag(token, inlineFlags); ok {
			sanitizedArgs = append(sanitizedArgs, flag+"="+SanitizeGitHubVisibleText(value))
			continue
		}
		if matchesFlag(token, inlineFlags) && i+1 < len(args) {
			sanitizedArgs = append(sanitizedArgs, token, SanitizeGitHubVisibleText(args[i+1]))
			i++
			continue
		}

		if flag, value, ok := splitInlineFlag(token, fileFlags); ok {
			content, nextStdin, err := sanitizeFileOrStdinValue(value, currentStdin)
			if err != nil {
				return nil, nil, err
			}
			currentStdin = nextStdin
			sanitizedArgs = append(sanitizedArgs, flag, "-")
			if content != nil {
				currentStdin = content
			}
			continue
		}
		if matchesFlag(token, fileFlags) && i+1 < len(args) {
			content, nextStdin, err := sanitizeFileOrStdinValue(args[i+1], currentStdin)
			if err != nil {
				return nil, nil, err
			}
			currentStdin = nextStdin
			sanitizedArgs = append(sanitizedArgs, token, "-")
			if content != nil {
				currentStdin = content
			}
			i++
			continue
		}

		sanitizedArgs = append(sanitizedArgs, token)
	}

	return sanitizedArgs, currentStdin, nil
}

func sanitizeFileOrStdinValue(value string, currentStdin io.Reader) (io.Reader, io.Reader, error) {
	if strings.TrimSpace(value) == "-" {
		if currentStdin == nil {
			return strings.NewReader(""), strings.NewReader(""), nil
		}
		body, err := io.ReadAll(currentStdin)
		if err != nil {
			return nil, nil, err
		}
		sanitized := SanitizeGitHubVisibleText(string(body))
		reader := strings.NewReader(sanitized)
		return reader, reader, nil
	}

	body, err := os.ReadFile(value)
	if err != nil {
		return nil, nil, err
	}
	return strings.NewReader(SanitizeGitHubVisibleText(string(body))), currentStdin, nil
}

func sanitizeAPIBodyFields(args []string) []string {
	sanitized := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		token := args[i]
		sanitized = append(sanitized, token)
		if !matchesFlag(token, []string{"-f", "--field", "-F", "--raw-field"}) || i+1 >= len(args) {
			continue
		}
		value := args[i+1]
		if strings.HasPrefix(value, "body=") {
			value = "body=" + SanitizeGitHubVisibleText(strings.TrimPrefix(value, "body="))
		}
		sanitized = append(sanitized, value)
		i++
	}
	return sanitized
}

func isGitHubBodyCommand(args []string) bool {
	tokens := proxyCommandTokens("gh", args)
	if len(tokens) < 2 {
		return false
	}
	return (tokens[0] == "issue" && tokens[1] == "comment") ||
		(tokens[0] == "pr" && (tokens[1] == "create" || tokens[1] == "edit"))
}

func isGitHubAPIBodyCommand(args []string) bool {
	tokens := proxyCommandTokens("gh", args)
	if len(tokens) == 0 || tokens[0] != "api" {
		return false
	}
	for _, token := range args {
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "repos/") && (strings.Contains(token, "/issues") || strings.Contains(token, "/pulls")) {
			return true
		}
	}
	return false
}

func isGitCommitCommand(args []string) bool {
	tokens := proxyCommandTokens("git", args)
	return len(tokens) > 0 && tokens[0] == "commit"
}

func proxyCommandTokens(tool string, args []string) []string {
	tokens := make([]string, 0, 3)
	for i := 0; i < len(args); i++ {
		token := strings.TrimSpace(args[i])
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "-") {
			if proxyFlagNeedsValue(tool, token) && i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
				i++
			}
			continue
		}
		tokens = append(tokens, token)
		if len(tokens) == 3 {
			return tokens
		}
	}
	return tokens
}

func proxyFlagNeedsValue(tool string, flag string) bool {
	if strings.Contains(flag, "=") {
		return false
	}
	switch tool {
	case "gh":
		return flag == "-R" || flag == "--repo" || flag == "--hostname"
	case "git":
		switch flag {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--super-prefix", "--exec-path", "--config-env":
			return true
		}
	}
	return false
}

func matchesFlag(token string, flags []string) bool {
	for _, flag := range flags {
		if token == flag {
			return true
		}
	}
	return false
}

func splitInlineFlag(token string, flags []string) (string, string, bool) {
	for _, flag := range flags {
		prefix := flag + "="
		if strings.HasPrefix(token, prefix) {
			return flag, strings.TrimPrefix(token, prefix), true
		}
	}
	return "", "", false
}
