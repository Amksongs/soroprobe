package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestHolderBalanceMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := HolderBalance{
		Address: "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		Raw:     "10000000",
		Balance: "1.0000000",
		Error:   "simulated error",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded HolderBalance
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
