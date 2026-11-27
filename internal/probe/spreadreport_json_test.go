package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSpreadReportMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	percent := 5.0
	maxSpreadPercent := 10.0
	original := SpreadReport{
		Runs: 3,
		Figures: []FigureSpread{
			{
				Name:    "cpu instructions",
				Min:     100,
				Max:     110,
				Spread:  10,
				Percent: &percent,
			},
		},
		Max: Cost{
			Instructions:      110,
			DiskReadBytes:     20,
			WriteBytes:        5,
			ResourceFee:       1000,
			MinResourceFee:    900,
			EstimatedTotalFee: 1100,
		},
		FootprintChanges: []string{"read  account"},
		MaxSpreadPercent: &maxSpreadPercent,
		Noisy:            true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SpreadReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
