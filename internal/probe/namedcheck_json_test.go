package probe

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestNamedCheckMarshalsAndUnmarshals(t *testing.T) {
	t.Parallel()

	original := NamedCheck{
		Name:    "balance-check",
		Timeout: "45s",
		CheckRequest: CheckRequest{
			ContractID:       "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAD2KM",
			Function:         "balance",
			Args:             []string{"u128:1"},
			Expect:           "u128:100",
			DataKeys:         []string{"sym:Balance", "sym:Admin"},
			DataDurability:   health.DurabilityPersistent,
			ExpectedWASMHash: "abcdef0123456789",
			Standard:         "fungible",
			AdminFn:          "get_admin",
			AdminKey:         "sym:Admin",
			Upgradeability:   true,
			RentPayer:        "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAACH3X",
			ExtendLedgers:    120960,
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded NamedCheck
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip mismatch:\noriginal: %#v\ndecoded:  %#v", original, decoded)
	}
}
