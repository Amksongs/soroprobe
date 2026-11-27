package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRestoreInfoMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := RestoreInfo{
		MinResourceFee:     12345,
		ArchivedEntries:    []string{"contract CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM instance (persistent)"},
		TransactionDataXDR: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded RestoreInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
