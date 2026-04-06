package sandbox

import "testing"

func TestParseMemoryBytes(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"8g", 8 * 1024 * 1024 * 1024},
		{"4G", 4 * 1024 * 1024 * 1024},
		{"512m", 512 * 1024 * 1024},
		{"256M", 256 * 1024 * 1024},
		{"", 0},
		{"0", 0},
	}

	for _, tt := range tests {
		got := parseMemoryBytes(tt.input)
		if got != tt.want {
			t.Errorf("parseMemoryBytes(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEffectiveBaseImage(t *testing.T) {
	if got := effectiveBaseImage(""); got != "vigilante/sandbox:latest" {
		t.Errorf("effectiveBaseImage('') = %q, want default", got)
	}
	if got := effectiveBaseImage("custom:v1"); got != "custom:v1" {
		t.Errorf("effectiveBaseImage('custom:v1') = %q", got)
	}
}
