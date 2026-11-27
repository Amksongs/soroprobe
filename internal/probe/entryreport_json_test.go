package probe

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestEntryReportMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	ledger := uint32(12345)
	original := EntryReport{
		Kind: "data",
		Key:  "sym:Balance",
		TTL: health.TTL{
			Present:          true,
			LiveUntilLedger:  &ledger,
			CurrentLedger:    100,
			LedgersRemaining: 12245,
			Durability:       health.DurabilityPersistent,
			Status:           health.StatusHealthy,
		},
		Summary:    "live for 12245 more ledgers",
		Thresholds: health.Thresholds{Warn: 120960, Critical: 17280},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded EntryReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
