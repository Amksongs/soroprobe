package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCostMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := Cost{
		Instructions:      127569,
		DiskReadBytes:     4096,
		WriteBytes:        512,
		ResourceFee:       12279,
		MinResourceFee:    12000,
		EstimatedTotalFee: 12379,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Cost
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
