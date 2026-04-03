package hardening

import (
	"fmt"
	"strings"
)

const (
	// CommentMarker is embedded in hardening comments so Vigilante can
	// identify its own comments when scanning for checkbox state changes.
	CommentMarker = "<!-- vigilante:package-hardening -->"

	// ImplementFixesUnchecked is the unchecked checkbox text.
	ImplementFixesUnchecked = "- [ ] **implement fixes** — Vigilante will launch an automated remediation session for the findings above."

	// ImplementFixesChecked is the checked checkbox text.
	ImplementFixesChecked = "- [x] **implement fixes** — Vigilante will launch an automated remediation session for the findings above."
)

// FormatHardeningComment renders a structured PR comment from a hardening
// scan result. The comment includes the comment marker for identification,
// a summary of findings, and the implement-fixes checkbox.
func FormatHardeningComment(result Result, prNumber int) string {
	var sb strings.Builder

	sb.WriteString(CommentMarker)
	sb.WriteString("\n")
	sb.WriteString("## 🔒 Package Hardening Review\n\n")

	sb.WriteString(fmt.Sprintf("Vigilante ran a deterministic security scan on PR #%d and found **%d issue(s)**.\n\n", prNumber, len(result.Findings)))

	if result.PackageManager != "" {
		sb.WriteString(fmt.Sprintf("**Package manager:** %s", result.PackageManager))
		if result.LockfilePresent {
			sb.WriteString(" (lockfile present ✅)")
		} else {
			sb.WriteString(" (lockfile missing ⚠️)")
		}
		sb.WriteString("\n")
	}
	if result.AuditAvailable {
		if result.AuditRan {
			sb.WriteString("**Audit:** ran successfully\n")
		} else {
			sb.WriteString("**Audit:** skipped (see findings)\n")
		}
	}
	sb.WriteString("\n")

	sb.WriteString("### Findings\n\n")
	sb.WriteString("| Severity | Check | Details |\n")
	sb.WriteString("|----------|-------|---------|\n")
	for _, f := range result.Findings {
		sev := severityEmoji(f.Severity) + " " + string(f.Severity)
		msg := strings.ReplaceAll(f.Message, "\n", " ")
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %s |\n", sev, f.Check, msg))
	}
	sb.WriteString("\n")

	hasRemediation := false
	for _, f := range result.Findings {
		if f.Remediation != "" {
			hasRemediation = true
			break
		}
	}
	if hasRemediation {
		sb.WriteString("<details>\n<summary>Remediation details</summary>\n\n")
		for _, f := range result.Findings {
			if f.Remediation == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("**%s** (`%s`): %s\n\n", severityEmoji(f.Severity)+" "+string(f.Severity), f.Check, f.Remediation))
		}
		sb.WriteString("</details>\n\n")
	}

	sb.WriteString("### Automated Remediation\n\n")
	sb.WriteString(ImplementFixesUnchecked)
	sb.WriteString("\n")

	return sb.String()
}

// IsHardeningComment returns true when the comment body contains the
// package-hardening marker.
func IsHardeningComment(body string) bool {
	return strings.Contains(body, CommentMarker)
}

// IsImplementFixesChecked returns true when the implement-fixes checkbox
// in a hardening comment has been checked by a human.
func IsImplementFixesChecked(body string) bool {
	if !IsHardeningComment(body) {
		return false
	}
	return strings.Contains(body, "- [x] **implement fixes**") ||
		strings.Contains(body, "- [X] **implement fixes**")
}

// IsImplementFixesUnchecked returns true when the implement-fixes checkbox
// exists but is unchecked.
func IsImplementFixesUnchecked(body string) bool {
	if !IsHardeningComment(body) {
		return false
	}
	return strings.Contains(body, "- [ ] **implement fixes**")
}

// FormatRemediationResultComment renders a follow-up comment after the
// automated remediation session completes.
func FormatRemediationResultComment(success bool, summary string) string {
	var sb strings.Builder
	sb.WriteString(CommentMarker)
	sb.WriteString("\n")
	if success {
		sb.WriteString("## ✅ Package Remediation Complete\n\n")
		sb.WriteString("Vigilante's automated remediation session has completed.\n\n")
	} else {
		sb.WriteString("## ⚠️ Package Remediation Incomplete\n\n")
		sb.WriteString("Vigilante attempted automated remediation but could not resolve all findings safely.\n\n")
	}
	if summary != "" {
		sb.WriteString(summary)
		sb.WriteString("\n")
	}
	return sb.String()
}

func severityEmoji(s Severity) string {
	switch s {
	case SeverityHigh:
		return "🔴"
	case SeverityMedium:
		return "🟠"
	case SeverityLow:
		return "🟡"
	default:
		return "🔵"
	}
}
