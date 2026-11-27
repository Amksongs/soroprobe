package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSimulateBatchRequestMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	maxIncrease := 15.5
	maxSpread := 10.0
	original := SimulateBatchRequest{
		Calls: []NamedSimulation{
			{
				Name: "sim1",
				SimulateRequest: SimulateRequest{
					ContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
					Function:   "balance",
					Args:       []string{"address:GABC"},
					Baseline: &Baseline{
						ContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
						Function:   "balance",
						Cost: Cost{
							Instructions:      10000,
							DiskReadBytes:     2000,
							WriteBytes:        500,
							ResourceFee:       30000,
							MinResourceFee:    25000,
							EstimatedTotalFee: 35000,
						},
					},
					MaxIncreasePercent: &maxIncrease,
					Repeat:             3,
					Trace:              true,
					MaxSpreadPercent:   &maxSpread,
				},
			},
			{
				Name: "sim2",
				SimulateRequest: SimulateRequest{
					ContractID: "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4",
					Function:   "decimals",
					Args:       []string{"symbol:TEST"},
					Repeat:     1,
				},
			},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateBatchRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}

func TestSimulateBatchRequestEmptyMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := SimulateBatchRequest{
		Calls: []NamedSimulation{},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SimulateBatchRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
