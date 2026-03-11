package logtime

import "time"

// FormatLocal renders human-facing log timestamps in the user's local timezone.
func FormatLocal(t time.Time) string {
	return t.In(time.Local).Format(time.RFC3339)
}
