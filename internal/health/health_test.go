package health_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/soroworks/soroprobe/internal/health"
)

func ptr(n uint32) *uint32 { return &n }

func TestEvaluateThresholds(t *testing.T) {
	t.Parallel()

	// Deliberately small thresholds keep the boundary arithmetic readable.
	thresholds := health.Thresholds{Warn: 1000, Critical: 100}
	const current = 5000

	tests := []struct {
		name      string
		liveUntil *uint32
		want      health.Status
		remaining int64
	}{
		{"far in the future is healthy", ptr(current + 100_000), health.StatusHealthy, 100_000},
		{"just above the warn threshold is healthy", ptr(current + 1001), health.StatusHealthy, 1001},
		{"exactly at the warn threshold warns", ptr(current + 1000), health.StatusWarning, 1000},
		{"inside the warn band warns", ptr(current + 500), health.StatusWarning, 500},
		{"just above the critical threshold warns", ptr(current + 101), health.StatusWarning, 101},
		{"exactly at the critical threshold is critical", ptr(current + 100), health.StatusCritical, 100},
		{"inside the critical band is critical", ptr(current + 10), health.StatusCritical, 10},
		{"expiring this ledger is critical, not expired", ptr(current), health.StatusCritical, 0},
		{"one ledger past is expired", ptr(current - 1), health.StatusExpired, -1},
		{"long past is expired", ptr(current - 4000), health.StatusExpired, -4000},
		{"no ttl reported is unknown", nil, health.StatusUnknown, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := health.Evaluate(tt.liveUntil, current, health.DurabilityPersistent, thresholds)
			assert.Equal(t, tt.want, got.Status)
			assert.Equal(t, tt.remaining, got.LedgersRemaining)
			assert.True(t, got.Present)
		})
	}
}

func TestEvaluateConvertsLedgersToTime(t *testing.T) {
	t.Parallel()

	// One day is 17,280 ledgers at the five second target close time.
	got := health.Evaluate(ptr(1_017_280), 1_000_000, health.DurabilityPersistent, health.DefaultThresholds)
	assert.Equal(t, 24*time.Hour, got.EstimatedTimeLeft)
	assert.Equal(t, int64(17_280), got.LedgersRemaining)
}

func TestEvaluateUsesMeasuredCloseTime(t *testing.T) {
	t.Parallel()

	got := health.EvaluateWithCloseTime(ptr(1_000_010), 1_000_000, health.DurabilityPersistent, health.DefaultThresholds, 4*time.Second)
	assert.Equal(t, 40*time.Second, got.EstimatedTimeLeft)
}

func TestResolveThresholdsPerKindFallsBackIndependently(t *testing.T) {
	t.Parallel()

	warn := uint32(500)
	global := health.Thresholds{Warn: 1000, Critical: 100}
	got := health.ResolveThresholds(health.EntryKindInstance, global, health.ThresholdOverrides{
		Instance: &health.ThresholdOverride{Warn: &warn},
	})
	assert.Equal(t, health.Thresholds{Warn: 500, Critical: 100}, got)
	assert.Equal(t, global, health.ResolveThresholds(health.EntryKindCode, global, health.ThresholdOverrides{}))
}

func TestEvaluateExpiredHasNegativeTime(t *testing.T) {
	t.Parallel()

	got := health.Evaluate(ptr(900_000), 1_000_000, health.DurabilityPersistent, health.DefaultThresholds)
	assert.Equal(t, health.StatusExpired, got.Status)
	assert.Negative(t, got.LedgersRemaining)
	assert.Negative(t, got.EstimatedTimeLeft)
}

func TestDefaultThresholdsMatchDocumentedDurations(t *testing.T) {
	t.Parallel()

	// The README states these as roughly seven days and one day.
	assert.Equal(t, 7*24*time.Hour, time.Duration(health.DefaultThresholds.Warn)*health.LedgerCloseTime)
	assert.Equal(t, 24*time.Hour, time.Duration(health.DefaultThresholds.Critical)*health.LedgerCloseTime)
}

func TestAbsent(t *testing.T) {
	t.Parallel()

	got := health.Absent(1234)
	assert.False(t, got.Present)
	assert.Equal(t, health.StatusMissing, got.Status)
	assert.Equal(t, uint32(1234), got.CurrentLedger)
	assert.Nil(t, got.LiveUntilLedger)
	assert.Equal(t, "not found on ledger", got.Describe())
}

func TestDurabilityExpiryConsequences(t *testing.T) {
	t.Parallel()

	// This distinction is the single most important thing SoroProbe reports:
	// a temporary entry that lapses is gone for good.
	assert.True(t, health.DurabilityPersistent.Archivable())
	assert.Contains(t, health.DurabilityPersistent.OnExpiry(), "restorable")

	assert.False(t, health.DurabilityTemporary.Archivable())
	assert.Contains(t, health.DurabilityTemporary.OnExpiry(), "deleted permanently")
}

func TestDescribeMentionsExpiryConsequence(t *testing.T) {
	t.Parallel()

	temp := health.Evaluate(ptr(100), 200, health.DurabilityTemporary, health.DefaultThresholds)
	assert.Contains(t, temp.Describe(), "deleted permanently")

	persistent := health.Evaluate(ptr(100), 200, health.DurabilityPersistent, health.DefaultThresholds)
	assert.Contains(t, persistent.Describe(), "restorable")
}

func TestDescribeLiveEntry(t *testing.T) {
	t.Parallel()

	got := health.Evaluate(ptr(1_017_280), 1_000_000, health.DurabilityPersistent, health.DefaultThresholds)
	summary := got.Describe()
	assert.Contains(t, summary, "17280 more ledgers")
	assert.Contains(t, summary, "1.0 days")
	assert.Contains(t, summary, "1017280")
}

func TestStatusSeverityOrdering(t *testing.T) {
	t.Parallel()

	ordered := []health.Status{
		health.StatusHealthy,
		health.StatusUnknown,
		health.StatusWarning,
		health.StatusCritical,
		health.StatusExpired,
		health.StatusMissing,
	}
	for i := 1; i < len(ordered); i++ {
		assert.Greater(t, ordered[i].Severity(), ordered[i-1].Severity(),
			"%s should outrank %s", ordered[i], ordered[i-1])
	}
}

func TestStatusOK(t *testing.T) {
	t.Parallel()

	assert.True(t, health.StatusHealthy.OK())
	assert.True(t, health.StatusUnknown.OK())
	assert.False(t, health.StatusWarning.OK())
	assert.False(t, health.StatusCritical.OK())
	assert.False(t, health.StatusExpired.OK())
	assert.False(t, health.StatusMissing.OK())
}

func TestWorst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []health.Status
		want health.Status
	}{
		{"no statuses is healthy", nil, health.StatusHealthy},
		{"all healthy", []health.Status{health.StatusHealthy, health.StatusHealthy}, health.StatusHealthy},
		{"warning beats healthy", []health.Status{health.StatusHealthy, health.StatusWarning}, health.StatusWarning},
		{"critical beats warning", []health.Status{health.StatusWarning, health.StatusCritical}, health.StatusCritical},
		{"missing beats everything", []health.Status{health.StatusCritical, health.StatusMissing, health.StatusExpired}, health.StatusMissing},
		{"order does not matter", []health.Status{health.StatusExpired, health.StatusHealthy}, health.StatusExpired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, health.Worst(tt.in...))
		})
	}
}
