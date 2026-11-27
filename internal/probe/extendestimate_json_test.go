package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExtendEstimateMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := ExtendEstimate{
		Kind:           "data",
		Key:            "sym:Balance",
		Ledgers:        120960,
		ExtendTo:       200000,
		ResourceFee:    12345,
		MinResourceFee: 10000,
		Note:           "estimated",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ExtendEstimate
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
