package probe

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestCheckResultMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	liveUntil := uint32(4073187)
	original := CheckResult{
		ContractID: "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4",
		OK:         false,
		Status:     health.StatusWarning,
		Checks: []Check{
			{Name: "deployed", Outcome: OutcomePass, Detail: "instance found, executable is wasm"},
			{Name: "instance_ttl", Outcome: OutcomeWarn, Detail: "live for 58912 more ledgers"},
			{Name: "code_ttl", Outcome: OutcomeFail, Detail: "expired at ledger 4038543"},
			{Name: "simulate", Outcome: OutcomeSkip, Detail: "no function given; pass one to probe a read-only call"},
		},
		Inspect: &InspectResult{
			ContractID: "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4",
			Deployed:   true,
			Executable: "wasm",
			WasmHash:   "8608bb30",
			Instance: EntryReport{
				Kind: "instance",
				TTL: health.TTL{
					Present:          true,
					LiveUntilLedger:  &liveUntil,
					CurrentLedger:    4014275,
					LedgersRemaining: 58912,
					Durability:       health.DurabilityPersistent,
					Status:           health.StatusWarning,
				},
				Summary:    "live for 58912 more ledgers (~3.4 days)",
				Thresholds: health.Thresholds{Warn: 120960, Critical: 17280},
			},
			Data: []EntryReport{{
				Kind:       "data",
				Key:        "sym:Admin",
				Summary:    "live for 30000 more ledgers",
				Thresholds: health.Thresholds{Warn: 120960, Critical: 17280},
			}},
			InstanceStorage: map[string]any{"Admin": "GA5Z"},
			ExtendEstimates: []ExtendEstimate{{
				Kind:           "instance",
				Ledgers:        1209600,
				ExtendTo:       5223725,
				ResourceFee:    6128,
				MinResourceFee: 6000,
			}},
			Status:        health.StatusWarning,
			LatestLedger:  4014275,
			CloseTimeNote: "measured over the 20 most recent ledgers",
		},
		Simulate: &SimulateResult{
			ContractID:  "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4",
			Function:    "decimals",
			Success:     true,
			ReturnValue: float64(7),
			Cost: Cost{
				Instructions:      127569,
				DiskReadBytes:     0,
				WriteBytes:        0,
				ResourceFee:       12279,
				MinResourceFee:    12279,
				EstimatedTotalFee: 12379,
			},
			Footprint: Footprint{
				ReadOnly: []string{"contract instance CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4 (persistent)"},
			},
			LatestLedger: 4014077,
		},
		LatestLedger:  4014275,
		CloseTimeNote: "measured over the 20 most recent ledgers",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded CheckResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
