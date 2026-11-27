package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSimulateBatchItemMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := SimulateBatchItem{
		Name: "test-simulation",
		Result: &SimulateResult{
			ContractID:     "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
			Function:       "transfer",
			Args:           []string{"address:GABC", "u64:100"},
			Signature:      "fn transfer(to: address, amount: u64)",
			EncodedArgs:    []string{"address:GABC", "u64:100"},
			ABINote:        "inferred",
			Success:        true,
			Error:          "",
			ReturnValue:    map[string]any{"status": "ok", "value": float64(42)},
			ReturnValueXDR: "AAAAAQ==",
			Cost: Cost{
				Instructions:      12345,
				DiskReadBytes:     6789,
				WriteBytes:        1000,
				ResourceFee:       50000,
				MinResourceFee:    40000,
				EstimatedTotalFee: 55000,
			},
			Footprint: Footprint{
				ReadOnly:  []string{"key1", "key2"},
				ReadWrite: []string{"key3"},
			},
			FootprintEntries: []FootprintEntry{
				{
					Access:     "read_only",
					Kind:       "data",
					Contract:   "self",
					Key:        "Balance",
					Durability: "persistent",
					Hash:       "abc",
					Account:    "GXYZ",
					Label:      "data entry Balance",
				},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateBatchItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}

func TestSimulateBatchItemWithErrorMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := SimulateBatchItem{
		Name:  "failed-simulation",
		Error: "simulation failed: host error",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateBatchItem
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
