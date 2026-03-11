package ghcli

import "testing"

func TestFormatProgressComment(t *testing.T) {
	comment := FormatProgressComment(ProgressComment{
		Stage:      "Validation Passed",
		Emoji:      "✅",
		Percent:    90,
		ETAMinutes: 5,
		Items: []string{
			"Ran `go test ./...`.",
			"Pushed `vigilante/issue-12`.",
		},
		Tagline: "Success is where preparation and opportunity meet.",
	})

	expected := "## ✅ Validation Passed\n`█████████░ 90%`\n`ETA: ~5 minutes`\n- Ran `go test ./...`.\n- Pushed `vigilante/issue-12`.\n> \"Success is where preparation and opportunity meet.\""
	if comment != expected {
		t.Fatalf("unexpected comment:\n%s", comment)
	}
}
