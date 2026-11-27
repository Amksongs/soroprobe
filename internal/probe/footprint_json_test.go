package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFootprintMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := Footprint{
		ReadOnly: []string{
			"CCBM...1",
			"CCBM...2",
		},
		ReadWrite: []string{
			"CCBM...3",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Footprint
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
