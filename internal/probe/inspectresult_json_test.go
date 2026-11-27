package probe

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestInspectResultMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	liveUntil := uint32(200000)
	issuerFound := true

	original := InspectResult{
		ContractID: "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
		Deployed:   true,
		Executable: "wasm",
		WasmHash:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
		Instance: EntryReport{
			Kind: "instance",
			TTL: health.TTL{
				Present:          true,
				LiveUntilLedger:  &liveUntil,
				CurrentLedger:    100000,
				LedgersRemaining: 100000,
				Durability:       health.DurabilityPersistent,
				Status:           health.StatusHealthy,
			},
			Summary: "instance is healthy",
			Thresholds: health.Thresholds{
				Warn:     120960,
				Critical: 17280,
			},
		},
		Code: &EntryReport{
			Kind: "code",
			TTL: health.TTL{
				Present:          true,
				LiveUntilLedger:  &liveUntil,
				CurrentLedger:    100000,
				LedgersRemaining: 100000,
				Durability:       health.DurabilityPersistent,
				Status:           health.StatusHealthy,
			},
			Summary: "code is healthy",
			Thresholds: health.Thresholds{
				Warn:     120960,
				Critical: 17280,
			},
		},
		Data: []EntryReport{
			{
				Kind: "data",
				Key:  "sym:Balance",
				TTL: health.TTL{
					Present:          true,
					LiveUntilLedger:  &liveUntil,
					CurrentLedger:    100000,
					LedgersRemaining: 100000,
					Durability:       health.DurabilityPersistent,
					Status:           health.StatusHealthy,
				},
				Summary: "data entry is healthy",
				Thresholds: health.Thresholds{
					Warn:     120960,
					Critical: 17280,
				},
			},
		},
		Asset: &AssetDetails{
			Type:        "credit_alphanum4",
			Code:        "USDC",
			Issuer:      "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
			Admin:       "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
			IssuerFound: &issuerFound,
			IssuerFlags: []string{"auth_required", "auth_revocable"},
			Note:        "asset details note",
		},
		InstanceStorage: map[string]any{
			"admin": "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		},
		ExtendEstimates: []ExtendEstimate{
			{
				Kind:           "instance",
				Key:            "",
				Ledgers:        120960,
				ExtendTo:       220960,
				ResourceFee:    5000,
				MinResourceFee: 4000,
				Note:           "extended",
			},
		},
		Status:        health.StatusHealthy,
		LatestLedger:  100000,
		CloseTimeNote: "target 5s close time",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded InspectResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
