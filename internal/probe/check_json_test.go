package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCheckMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := Check{
		Name:    "deployed",
		Outcome: OutcomePass,
		Detail:  "instance found, executable is wasm",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Check
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
