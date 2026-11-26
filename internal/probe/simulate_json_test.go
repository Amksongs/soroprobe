package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCostChangeMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	percent := 25.0
	original := CostChange{
		Name:     "cpu instructions",
		Baseline: 1000,
		Current:  1250,
		Delta:    250,
		Percent:  &percent,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded CostChange
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}

func TestCostChangeMarshalsNilPercent(t *testing.T) {
	t.Parallel()

	original := CostChange{
		Name:     "disk read bytes",
		Baseline: 0,
		Current:  500,
		Delta:    500,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded CostChange
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}

func TestCostComparisonMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	threshold := 10.0
	percent := 25.0
	original := CostComparison{
		Figures: []CostChange{
			{
				Name:     "cpu instructions",
				Baseline: 1000,
				Current:  1250,
				Delta:    250,
				Percent:  &percent,
			},
			{
				Name:     "disk read bytes",
				Baseline: 200,
				Current:  200,
				Delta:    0,
			},
		},
		MaxIncreasePercent: &threshold,
		Exceeded:           true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded CostComparison
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}

func TestSimulateBatchResultMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := SimulateBatchResult{
		Items: []SimulateBatchItem{
			{
				Name: "transfer",
				Result: &SimulateResult{
					ContractID:  "C123",
					Function:    "transfer",
					Args:        []string{"addr:GABC", "i64:100"},
					Success:     true,
					ReturnValue: "ok",
					Cost: Cost{
						Instructions: 1234,
						ResourceFee:  56,
					},
					Footprint: Footprint{
						ReadOnly:  []string{"account:GABC"},
						ReadWrite: []string{"balance:GABC"},
					},
					LatestLedger: 987,
				},
			},
			{
				Name:  "withdraw",
				Error: "simulation failed",
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateBatchResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
