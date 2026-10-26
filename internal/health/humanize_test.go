package health

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Rounding rule: seconds and minutes truncate toward zero; hours and days
// round to one decimal place. Negative durations render as their magnitude.
func TestHumanizeDuration(t *testing.T) {
	t.Parallel()

	const day = 24 * time.Hour

	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "0s"},
		{"sub-second truncates to zero", 999 * time.Millisecond, "0s"},
		{"sub-minute", 42 * time.Second, "42s"},
		{"just under a minute truncates", time.Minute - time.Millisecond, "59s"},
		{"exactly one minute", time.Minute, "1m"},
		{"minutes truncate", 90 * time.Second, "1m"},
		{"just under an hour truncates", time.Hour - time.Second, "59m"},
		{"exactly one hour", time.Hour, "1.0h"},
		{"just over an hour", time.Hour + time.Minute, "1.0h"},
		{"hours round down below .05", time.Hour + 2*time.Minute, "1.0h"},
		{"hours round up at .05 and above", time.Hour + 4*time.Minute, "1.1h"},
		{"just under a day rounds to 24.0h", day - time.Minute, "24.0h"},
		{"exactly one day", day, "1.0 days"},
		{"fractional days", day + 12*time.Hour, "1.5 days"},
		{"days round to one decimal", 3*day + 10*time.Hour, "3.4 days"},
		{"multi-week", 3 * 7 * day, "21.0 days"},
		{"negative sub-minute uses magnitude", -42 * time.Second, "42s"},
		{"negative hours use magnitude", -(time.Hour + 30*time.Minute), "1.5h"},
		{"negative days use magnitude", -2 * day, "2.0 days"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, humanizeDuration(tt.in))
		})
	}
}
