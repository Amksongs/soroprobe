package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNamedSimulationMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	maxIncrease := 12.5
	maxSpread := 3.5
	original := NamedSimulation{
		Name: "balance-simulation",
		SimulateRequest: SimulateRequest{
			ContractID:         "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			Function:           "balance",
			Args:               []string{"u128:1"},
			Baseline:           &Baseline{ContractID: "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM", Function: "balance", Cost: Cost{Instructions: 1000, ResourceFee: 500}},
			MaxIncreasePercent: &maxIncrease,
			Repeat:             3,
			Trace:              true,
			MaxSpreadPercent:   &maxSpread,
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded NamedSimulation
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
