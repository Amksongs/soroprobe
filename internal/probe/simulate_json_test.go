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

func TestSimulateResultMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	percent := 2.5
	maxIncreasePercent := 10.0
	maxSpreadPercent := 5.0
	original := SimulateResult{
		ContractID:     "C123",
		Function:       "transfer",
		Args:           []string{"G123", "100"},
		Signature:      "transfer(Address, i128)",
		EncodedArgs:    []string{"AAAA", "AAAA"},
		ABINote:        "typed from interface",
		Success:        true,
		ReturnValue:    map[string]any{"amount": float64(100)},
		ReturnValueXDR: "AAAA",
		Cost: Cost{
			Instructions:      1000,
			DiskReadBytes:     200,
			WriteBytes:        50,
			ResourceFee:       300,
			MinResourceFee:    250,
			EstimatedTotalFee: 400,
		},
		Footprint: Footprint{
			ReadOnly:  []string{"contract-data"},
			ReadWrite: []string{"contract-instance"},
		},
		FootprintEntries: []FootprintEntry{{
			Access:     "read_only",
			Kind:       "data",
			Contract:   "self",
			Key:        "Balance",
			Durability: "persistent",
			Label:      "data self Balance",
		}},
		Trace: []*TraceFrame{{
			Contract: "C123",
			Function: "transfer",
			Args:     []any{"G123", float64(100)},
			ArgNames: []string{"to", "amount"},
			Returned: true,
			Return:   true,
			Calls: []*TraceFrame{{
				Contract: "C456",
				Function: "authorize",
				Args:     []any{},
			}},
		}},
		FromEnvelope: true,
		DecodedArgs:  []any{"G123", float64(100)},
		Source:       "G456",
		Comparison: &CostComparison{
			Figures: []CostChange{{
				Name:     "cpu instructions",
				Baseline: 900,
				Current:  1000,
				Delta:    100,
				Percent:  &percent,
			}},
			MaxIncreasePercent: &maxIncreasePercent,
			Exceeded:           false,
		},
		Spread: &SpreadReport{
			Runs: 2,
			Figures: []FigureSpread{{
				Name:    "cpu instructions",
				Min:     950,
				Max:     1000,
				Spread:  50,
				Percent: &percent,
			}},
			Max:              Cost{Instructions: 1000},
			FootprintChanges: []string{"contract-data"},
			MaxSpreadPercent: &maxSpreadPercent,
			Noisy:            false,
		},
		RestoreRequired: &RestoreInfo{
			MinResourceFee:     500,
			ArchivedEntries:    []string{"archived-entry"},
			TransactionDataXDR: "BBBB",
		},
		Events: []Event{{
			Type:                     "contract",
			ContractID:               "C123",
			Topics:                   []any{"transfer", "G123"},
			Data:                     map[string]any{"amount": float64(100)},
			InSuccessfulContractCall: true,
		}},
		Auth: []AuthEntry{{
			Address:         "G123",
			IsSourceAccount: false,
			RootInvocation: &AuthorizedInvocation{
				ContractID: "C123",
				Function:   "transfer",
				Args:       []any{"G123", float64(100)},
				SubInvocations: []AuthorizedInvocation{{
					ContractID: "C456",
					Function:   "authorize",
				}},
			},
			RawXDR: "CCCC",
		}},
		LatestLedger: 12345,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
