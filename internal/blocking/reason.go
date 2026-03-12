package blocking

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/nicobistolfi/vigilante/internal/state"
)

var providerRetryHintPattern = regexp.MustCompile(`(?i)(try again at[^.\n]*|retry(?: after| at)?[^.\n]*|resets? at[^.\n]*)`)

func Classify(stage string, operation string, text string, fallbackSummary string) state.BlockedReason {
	normalized := strings.TrimSpace(text)
	lower := strings.ToLower(normalized)
	reason := state.BlockedReason{
		Kind:      "unknown_operator_action_required",
		Operation: operation,
		Summary:   summarize(fallbackSummary),
		Detail:    summarize(normalized),
	}
	switch {
	case strings.Contains(lower, "permission denied (publickey)") || strings.Contains(lower, "sign_and_send_pubkey") || strings.Contains(lower, "could not read from remote repository"):
		reason.Kind = "git_auth"
	case strings.Contains(lower, "gh auth") || strings.Contains(lower, "not logged into") || strings.Contains(lower, "authentication failed"):
		reason.Kind = "gh_auth"
	case strings.Contains(lower, "session expired") || strings.Contains(lower, "re-auth") || strings.Contains(lower, "login required") || strings.Contains(lower, "unauthorized"):
		reason.Kind = "provider_auth"
	case isProviderQuotaFailure(lower):
		reason.Kind = "provider_quota"
		reason.Summary = summarize(providerQuotaSummary(normalized))
	case strings.Contains(lower, "executable file not found") || strings.Contains(lower, "no such file or directory"):
		reason.Kind = "provider_missing"
	case strings.Contains(lower, "worktree is not clean"):
		reason.Kind = "dirty_worktree"
	case strings.Contains(lower, "go test") || strings.Contains(lower, "validation") || strings.Contains(lower, "build failed") || strings.Contains(lower, "tests failed"):
		reason.Kind = "validation_failed"
	case strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "timed out"):
		reason.Kind = "network_unreachable"
	case stage == "issue_execution" || stage == "conflict_resolution" || stage == "baseline_preflight":
		reason.Kind = "provider_runtime_error"
	}
	if reason.Summary == "" {
		reason.Summary = reason.Detail
	}
	if reason.Detail == "" {
		reason.Detail = reason.Summary
	}
	return reason
}

func StateLabel(kind string) string {
	switch kind {
	case "git_auth":
		return "blocked_waiting_for_credentials"
	case "gh_auth":
		return "blocked_waiting_for_github_auth"
	case "provider_auth":
		return "blocked_waiting_for_provider_auth"
	case "provider_quota":
		return "blocked_waiting_for_provider_quota"
	case "provider_missing":
		return "blocked_waiting_for_provider_binary"
	default:
		return "blocked_waiting_for_operator"
	}
}

func CauseLine(reason state.BlockedReason) string {
	line := fmt.Sprintf("Cause class: `%s`.", fallback(reason.Kind, "unknown_operator_action_required"))
	if strings.TrimSpace(reason.Summary) == "" || reason.Kind != "provider_quota" {
		return line
	}
	return fmt.Sprintf("%s Provider detail: `%s`.", line, reason.Summary)
}

func isProviderQuotaFailure(lower string) bool {
	if strings.Contains(lower, "usage limit") || strings.Contains(lower, "rate limit reached") || strings.Contains(lower, "quota exceeded") {
		return true
	}
	return (strings.Contains(lower, "upgrade to") || strings.Contains(lower, "purchase more credits") || strings.Contains(lower, "buy more credits")) &&
		(strings.Contains(lower, "credits") || strings.Contains(lower, "quota") || strings.Contains(lower, "usage"))
}

func providerQuotaSummary(text string) string {
	compact := compactWhitespace(text)
	lower := strings.ToLower(compact)
	parts := []string{"Coding-agent account hit a usage or subscription limit."}
	if match := strings.TrimSpace(providerRetryHintPattern.FindString(compact)); match != "" {
		parts = append(parts, sentence(match))
	}
	if strings.Contains(lower, "upgrade to") {
		parts = append(parts, "Provider suggests upgrading the subscription.")
	}
	if strings.Contains(lower, "purchase more credits") || strings.Contains(lower, "buy more credits") {
		parts = append(parts, "Provider suggests purchasing more credits.")
	}
	return strings.Join(unique(parts), " ")
}

func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func sentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	last := text[len(text)-1]
	if last == '.' || last == '!' || last == '?' {
		return text
	}
	return text + "."
}

func summarize(text string) string {
	text = compactWhitespace(text)
	if len(text) > 400 {
		return text[:400]
	}
	return text
}

func fallback(value string, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func unique(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
