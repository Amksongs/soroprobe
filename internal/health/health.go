// Package health interprets Soroban state-expiration data.
//
// # The model
//
// Every contract instance, code and data entry carries a TTL, expressed as a
// ledger sequence number in the entry's associated TTL entry. Stellar RPC
// surfaces it as liveUntilLedgerSeq on each getLedgerEntries result.
//
// An entry is live while:
//
//	liveUntilLedgerSeq >= currentLedgerSeq
//
// so the remaining lifetime in ledgers is liveUntilLedgerSeq - currentLedgerSeq.
// A value of zero means the entry expires with the current ledger; a negative
// value means it has already expired.
//
// What expiry costs depends on durability:
//
//   - temporary entries are deleted permanently and cannot be recovered
//   - persistent entries, contract instances and contract code are archived
//     rather than deleted, and can be restored
//
// SoroProbe converts remaining ledgers into wall-clock time using the network's
// target close time of five seconds per ledger. That is an estimate: close times
// drift slightly with network conditions, so treat the ledger count as the
// authoritative figure and the duration as a convenience.
package health

import (
	"fmt"
	"time"
)

// LedgerCloseTime is Stellar's target ledger close time, used to convert a
// count of ledgers into an approximate duration.
const LedgerCloseTime = 5 * time.Second

// Status classifies an entry's expiration risk.
type Status string

const (
	// StatusHealthy means the entry is live and not near expiration.
	StatusHealthy Status = "healthy"
	// StatusWarning means the entry expires within the warning threshold.
	StatusWarning Status = "warning"
	// StatusCritical means the entry expires within the critical threshold.
	StatusCritical Status = "critical"
	// StatusExpired means the entry's TTL has already lapsed; it is archived
	// or, for temporary durability, gone.
	StatusExpired Status = "expired"
	// StatusMissing means no such entry exists on the ledger.
	StatusMissing Status = "missing"
	// StatusUnknown means the entry exists but reported no TTL.
	StatusUnknown Status = "unknown"
)

// Severity orders statuses so that the worst one in a set can be found.
func (s Status) Severity() int {
	switch s {
	case StatusHealthy:
		return 0
	case StatusUnknown:
		return 1
	case StatusWarning:
		return 2
	case StatusCritical:
		return 3
	case StatusExpired:
		return 4
	case StatusMissing:
		return 5
	default:
		return 6
	}
}

// OK reports whether a status should be treated as a passing result.
func (s Status) OK() bool { return s == StatusHealthy || s == StatusUnknown }

// Worst returns the highest-severity status in the set. With no arguments it
// returns StatusHealthy.
func Worst(statuses ...Status) Status {
	worst := StatusHealthy
	for _, s := range statuses {
		if s.Severity() > worst.Severity() {
			worst = s
		}
	}
	return worst
}

// Durability distinguishes the two Soroban storage durabilities. Contract code
// and contract instance entries behave like persistent storage on expiry.
type Durability string

const (
	DurabilityPersistent Durability = "persistent"
	DurabilityTemporary  Durability = "temporary"
)

// Archivable reports whether an expired entry of this durability can be
// restored. Temporary entries cannot: they are deleted outright.
func (d Durability) Archivable() bool { return d != DurabilityTemporary }

// OnExpiry describes, in plain words, what expiration does to this entry.
func (d Durability) OnExpiry() string {
	if d.Archivable() {
		return "archived and restorable"
	}
	return "deleted permanently and not restorable (gone permanently)"
}

// Thresholds define how much remaining TTL triggers a warning or a critical
// result, measured in ledgers.
type Thresholds struct {
	// Warn is the remaining-ledger count at or below which an entry is a warning.
	Warn uint32 `json:"warn"`
	// Critical is the remaining-ledger count at or below which an entry is critical.
	Critical uint32 `json:"critical"`
}

// ThresholdOverride replaces either global threshold for one entry kind.
type ThresholdOverride struct {
	Warn     *uint32 `json:"warn,omitempty"`
	Critical *uint32 `json:"critical,omitempty"`
}

// ThresholdOverrides applies optional per-kind threshold values.
type ThresholdOverrides struct {
	Instance *ThresholdOverride `json:"instance,omitempty"`
	Code     *ThresholdOverride `json:"code,omitempty"`
	Data     *ThresholdOverride `json:"data,omitempty"`
}

// EntryKind identifies the ledger entry whose thresholds are resolved.
type EntryKind string

const (
	EntryKindInstance EntryKind = "instance"
	EntryKindCode     EntryKind = "code"
	EntryKindData     EntryKind = "data"
)

// ResolveThresholds applies the per-kind values and falls back independently
// to the corresponding global warning or critical threshold.
func ResolveThresholds(kind EntryKind, global Thresholds, overrides ThresholdOverrides) Thresholds {
	var override *ThresholdOverride
	switch kind {
	case EntryKindInstance:
		override = overrides.Instance
	case EntryKindCode:
		override = overrides.Code
	case EntryKindData:
		override = overrides.Data
	}
	if override == nil {
		return global
	}
	if override.Warn != nil {
		global.Warn = *override.Warn
	}
	if override.Critical != nil {
		global.Critical = *override.Critical
	}
	return global
}

// DefaultThresholds is roughly seven days for a warning and one day for
// critical, at five seconds per ledger.
var DefaultThresholds = Thresholds{Warn: 120_960, Critical: 17_280}

// TTL is the interpreted expiration state of a single ledger entry.
type TTL struct {
	// Present is false when the entry does not exist on the ledger.
	Present bool `json:"present"`
	// LiveUntilLedger is the last ledger at which the entry is still live.
	// Nil when the entry is absent or carries no TTL.
	LiveUntilLedger *uint32 `json:"live_until_ledger,omitempty"`
	// CurrentLedger is the ledger the assessment was made against.
	CurrentLedger uint32 `json:"current_ledger"`
	// LedgersRemaining is LiveUntilLedger - CurrentLedger. It is negative for
	// an entry whose TTL has already lapsed.
	LedgersRemaining int64 `json:"ledgers_remaining"`
	// EstimatedTimeLeft converts LedgersRemaining at the target close time.
	EstimatedTimeLeft time.Duration `json:"-"`
	// Durability is the storage durability of the entry.
	Durability Durability `json:"durability,omitempty"`
	// Status is the resulting classification.
	Status Status `json:"status"`
}

// Evaluate classifies an entry that exists on the ledger.
//
// liveUntil may be nil, which yields StatusUnknown: the entry is present but
// reported no TTL, so SoroProbe declines to guess.
func Evaluate(liveUntil *uint32, currentLedger uint32, durability Durability, t Thresholds) TTL {
	return EvaluateWithCloseTime(liveUntil, currentLedger, durability, t, LedgerCloseTime)
}

// EvaluateWithCloseTime classifies an entry and estimates its remaining time
// using the measured average duration between ledgers.
func EvaluateWithCloseTime(liveUntil *uint32, currentLedger uint32, durability Durability, t Thresholds, closeTime time.Duration) TTL {
	if closeTime <= 0 {
		closeTime = LedgerCloseTime
	}
	ttl := TTL{
		Present:         true,
		LiveUntilLedger: liveUntil,
		CurrentLedger:   currentLedger,
		Durability:      durability,
	}

	if liveUntil == nil {
		ttl.Status = StatusUnknown
		return ttl
	}

	remaining := int64(*liveUntil) - int64(currentLedger)
	ttl.LedgersRemaining = remaining
	ttl.EstimatedTimeLeft = time.Duration(remaining) * closeTime

	switch {
	case remaining < 0:
		ttl.Status = StatusExpired
	case remaining <= int64(t.Critical):
		ttl.Status = StatusCritical
	case remaining <= int64(t.Warn):
		ttl.Status = StatusWarning
	default:
		ttl.Status = StatusHealthy
	}
	return ttl
}

// Absent describes an entry that does not exist on the ledger.
func Absent(currentLedger uint32) TTL {
	return TTL{Present: false, CurrentLedger: currentLedger, Status: StatusMissing}
}

// Describe renders a one-line, human-readable summary of the TTL.
func (t TTL) Describe() string {
	switch t.Status {
	case StatusMissing:
		return "not found on ledger"
	case StatusUnknown:
		return "present, but no TTL was reported"
	case StatusExpired:
		return fmt.Sprintf("expired %s ago (at ledger %d); %s",
			humanizeDuration(-t.EstimatedTimeLeft), *t.LiveUntilLedger, t.Durability.OnExpiry())
	default:
		return fmt.Sprintf("live for %d more ledgers (~%s, until ledger %d)",
			t.LedgersRemaining, humanizeDuration(t.EstimatedTimeLeft), *t.LiveUntilLedger)
	}
}

// humanizeDuration renders a duration in the largest sensible unit.
func humanizeDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	default:
		return fmt.Sprintf("%.1f days", d.Hours()/24)
	}
}
