package logtime

import (
	"strings"
	"testing"
	"time"
)

// This package exists so operator-facing timestamps render in local time from one
// place. #490 fixed `vigilante status` showing UTC where a local time was meant,
// so the local-conversion behavior is worth a regression guard.
func TestFormatLocalRendersInLocalZone(t *testing.T) {
	t.Parallel()

	// A fixed instant expressed in UTC. Whatever the machine's zone, the output
	// must be that instant rendered in the local zone.
	instant := time.Date(2026, 8, 9, 15, 4, 5, 0, time.UTC)

	got := FormatLocal(instant)

	want := instant.In(time.Local).Format(time.RFC3339)
	if got != want {
		t.Fatalf("FormatLocal() = %q, want %q", got, want)
	}

	// Round-tripping must land on the same instant: the conversion may only change
	// the offset, never the point in time.
	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("output is not RFC3339: %v", err)
	}
	if !parsed.Equal(instant) {
		t.Fatalf("parsed %v, want the same instant as %v", parsed, instant)
	}
}

// An input already carrying a non-UTC offset must be converted rather than echoed.
func TestFormatLocalConvertsFromAnotherOffset(t *testing.T) {
	t.Parallel()

	tokyo := time.FixedZone("JST", 9*60*60)
	instant := time.Date(2026, 8, 9, 15, 4, 5, 0, tokyo)

	got := FormatLocal(instant)

	parsed, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("output is not RFC3339: %v", err)
	}
	if !parsed.Equal(instant) {
		t.Fatalf("parsed %v, want the same instant as %v", parsed, instant)
	}

	_, wantOffset := instant.In(time.Local).Zone()
	_, gotOffset := parsed.Zone()
	if gotOffset != wantOffset {
		t.Fatalf("offset = %d, want the local offset %d", gotOffset, wantOffset)
	}
}

func TestFormatLocalZeroTime(t *testing.T) {
	t.Parallel()

	// The zero time reaches this function for sessions that never started, so it
	// must format rather than panic.
	got := FormatLocal(time.Time{})
	if got == "" {
		t.Fatal("expected a formatted string for the zero time")
	}
	if !strings.Contains(got, "0001-01-01") && !strings.Contains(got, "0000-12-31") {
		t.Fatalf("got %q, want the zero time rendered", got)
	}
}

func TestFormatLocalIsStable(t *testing.T) {
	t.Parallel()

	instant := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if FormatLocal(instant) != FormatLocal(instant) {
		t.Fatal("FormatLocal must be deterministic for the same input")
	}
}
