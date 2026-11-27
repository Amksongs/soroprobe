package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSimulateRequestMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	maxIncreasePercent := 15.0
	maxSpreadPercent := 2.5
	original := SimulateRequest{
		ContractID: "CABC",
		Function:   "transfer",
		Args:       []string{"address:GABC", "i128:10"},
		Baseline: &Baseline{
			ContractID: "CABC",
			Function:   "transfer",
			Cost:       Cost{Instructions: 100},
		},
		MaxIncreasePercent: &maxIncreasePercent,
		Repeat:             3,
		Trace:              true,
		MaxSpreadPercent:   &maxSpreadPercent,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
