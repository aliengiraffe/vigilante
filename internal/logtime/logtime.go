// Package logtime formats timestamps for operator-facing output.
//
// It exists so that times vigilante shows a human are rendered in the local
// timezone from one place. Timestamps are stored and logged in UTC; converting at
// the display boundary keeps the stored data unambiguous without making operators
// do timezone arithmetic.
package logtime

import "time"

// FormatLocal renders human-facing log timestamps in the user's local timezone.
func FormatLocal(t time.Time) string {
	return t.In(time.Local).Format(time.RFC3339)
}
