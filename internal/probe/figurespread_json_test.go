package probe

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFigureSpreadMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	pct := 5.0
	original := FigureSpread{
		Name:    "cpu instructions",
		Min:     100,
		Max:     110,
		Spread:  10,
		Percent: &pct,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded FigureSpread
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
