package hardening

import (
	"strings"
	"testing"
)

func TestFormatHardeningComment(t *testing.T) {
	result := Result{
		Findings: []Finding{
			{Check: "lockfile-present", Severity: SeverityHigh, Message: "No lockfile found.", Remediation: "Run npm install."},
			{Check: "npm-audit-vulnerabilities", Severity: SeverityMedium, Message: "1 vulnerability found.", Remediation: "Run npm audit fix."},
		},
		LockfilePresent: false,
		PackageManager:  "npm",
		AuditAvailable:  true,
		AuditRan:        false,
	}

	body := FormatHardeningComment(result, 42)

	if !strings.Contains(body, CommentMarker) {
		t.Error("comment should contain the hardening marker")
	}
	if !strings.Contains(body, "2 issue(s)") {
		t.Error("comment should mention finding count")
	}
	if !strings.Contains(body, "PR #42") {
		t.Error("comment should reference PR number")
	}
	if !strings.Contains(body, ImplementFixesUnchecked) {
		t.Error("comment should contain unchecked implement-fixes checkbox")
	}
	if !strings.Contains(body, "lockfile missing") {
		t.Error("comment should indicate missing lockfile")
	}
	if !strings.Contains(body, "No lockfile found.") {
		t.Error("comment should contain finding message")
	}
	if !strings.Contains(body, "Remediation details") {
		t.Error("comment should contain remediation section")
	}
}

func TestFormatHardeningCommentNoRemediation(t *testing.T) {
	result := Result{
		Findings: []Finding{
			{Check: "test-check", Severity: SeverityInfo, Message: "Info only."},
		},
		PackageManager: "npm",
	}

	body := FormatHardeningComment(result, 10)

	if strings.Contains(body, "Remediation details") {
		t.Error("should not contain remediation section when no findings have remediation")
	}
}

func TestIsHardeningComment(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{CommentMarker + "\n## Header", true},
		{"Just a normal comment", false},
		{"", false},
		{CommentMarker, true},
	}
	for _, tt := range tests {
		if got := IsHardeningComment(tt.body); got != tt.want {
			t.Errorf("IsHardeningComment(%q) = %v, want %v", tt.body[:min(len(tt.body), 40)], got, tt.want)
		}
	}
}

func TestIsImplementFixesChecked(t *testing.T) {
	unchecked := CommentMarker + "\n" + ImplementFixesUnchecked
	checked := CommentMarker + "\n" + ImplementFixesChecked
	checkedUpper := strings.Replace(unchecked, "- [ ]", "- [X]", 1)
	noMarker := ImplementFixesChecked

	if IsImplementFixesChecked(unchecked) {
		t.Error("unchecked should not be detected as checked")
	}
	if !IsImplementFixesChecked(checked) {
		t.Error("checked (lowercase x) should be detected")
	}
	if !IsImplementFixesChecked(checkedUpper) {
		t.Error("checked (uppercase X) should be detected")
	}
	if IsImplementFixesChecked(noMarker) {
		t.Error("comment without marker should not be detected")
	}
}

func TestIsImplementFixesUnchecked(t *testing.T) {
	unchecked := CommentMarker + "\n" + ImplementFixesUnchecked
	checked := CommentMarker + "\n" + ImplementFixesChecked

	if !IsImplementFixesUnchecked(unchecked) {
		t.Error("unchecked should be detected")
	}
	if IsImplementFixesUnchecked(checked) {
		t.Error("checked should not be detected as unchecked")
	}
}

func TestFormatRemediationResultComment(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		body := FormatRemediationResultComment(true, "All fixed.")
		if !strings.Contains(body, CommentMarker) {
			t.Error("should contain marker")
		}
		if !strings.Contains(body, "Remediation Complete") {
			t.Error("should indicate success")
		}
		if !strings.Contains(body, "All fixed.") {
			t.Error("should contain summary")
		}
	})
	t.Run("failure", func(t *testing.T) {
		body := FormatRemediationResultComment(false, "Could not fix.")
		if !strings.Contains(body, "Remediation Incomplete") {
			t.Error("should indicate incomplete")
		}
		if !strings.Contains(body, "Could not fix.") {
			t.Error("should contain summary")
		}
	})
}

func TestSeverityEmoji(t *testing.T) {
	tests := []struct {
		severity Severity
		want     string
	}{
		{SeverityHigh, "🔴"},
		{SeverityMedium, "🟠"},
		{SeverityLow, "🟡"},
		{SeverityInfo, "🔵"},
	}
	for _, tt := range tests {
		if got := severityEmoji(tt.severity); got != tt.want {
			t.Errorf("severityEmoji(%s) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
