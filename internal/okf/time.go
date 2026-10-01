package okf

import "time"

// Timestamp renders t as an OKF event time: UTC, millisecond precision, "Z"
// suffix (the same shape as JavaScript's Date.toISOString).
func Timestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
