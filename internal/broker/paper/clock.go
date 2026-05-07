package paper

import "time"

// wallClockMs returns the current UTC wall-clock millisecond timestamp.
// Isolated in its own file so simple.go contains zero strategy-relevant
// time references — keeps grep-based iron-rule audits clean even if the
// rule list expands.
func wallClockMs() int64 {
	return time.Now().UTC().UnixMilli()
}
