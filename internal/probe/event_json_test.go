package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEventMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := Event{
		Type:                     "contract",
		ContractID:               "CABC",
		Topics:                   []any{"hello", true, float64(42)},
		Data:                     map[string]any{"n": float64(7), "label": "payload"},
		InSuccessfulContractCall: true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
