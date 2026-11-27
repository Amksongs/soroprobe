package probe

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestCheckRequestMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := CheckRequest{
		ContractID:       "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4",
		Function:         "balance",
		Args:             []string{"addr:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN", "i128:10.5@7"},
		Expect:           "i128:0",
		DataKeys:         []string{"sym:Admin", "u32:7"},
		DataDurability:   health.DurabilityTemporary,
		ExpectedWASMHash: "9c1e5f2b8d4a7c3e6f0b1d2a4c5e7f8091a2b3c4d5e6f708192a3b4c5d6e7f80",
		Standard:         "sep41",
		AdminFn:          "admin",
		AdminKey:         "sym:Admin",
		Upgradeability:   true,
		RentPayer:        "GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN",
		ExtendLedgers:    518400,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded CheckRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
