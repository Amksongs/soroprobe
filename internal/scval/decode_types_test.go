package scval

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func TestDecodeSupportedTypes(t *testing.T) {
	codec := NewRegistry()
	accountAddress, err := strkey.Encode(strkey.VersionByteAccountID, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	contractAddress, err := strkey.Encode(strkey.VersionByteContract, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}

	encode := func(spec string) xdr.ScVal {
		t.Helper()
		value, err := codec.Encode(spec)
		if err != nil {
			t.Fatalf("Encode(%q): %v", spec, err)
		}
		return value
	}
	vector := xdr.ScVec{encode("i32:-7")}
	vectorPointer := &vector
	emptyVector := xdr.ScVec{}
	emptyVectorPointer := &emptyVector
	mapEntries := xdr.ScMap{{Key: encode("sym:key"), Val: encode("str:value")}}
	mapPointer := &mapEntries

	tests := []struct {
		name  string
		value xdr.ScVal
		want  any
	}{
		{name: "bool", value: encode("bool:true"), want: true},
		{name: "void", value: encode("void"), want: nil},
		{name: "u32 zero", value: encode("u32:0"), want: uint32(0)},
		{name: "u32", value: encode("u32:4294967295"), want: uint32(4294967295)},
		{name: "i32", value: encode("i32:-2147483648"), want: int32(-2147483648)},
		{name: "u64", value: encode("u64:18446744073709551615"), want: "18446744073709551615"},
		{name: "i64", value: encode("i64:-9223372036854775808"), want: "-9223372036854775808"},
		{name: "timepoint", value: encode("timepoint:18446744073709551615"), want: "18446744073709551615"},
		{name: "duration", value: encode("duration:18446744073709551615"), want: "18446744073709551615"},
		{name: "u128", value: encode("u128:340282366920938463463374607431768211455"), want: "340282366920938463463374607431768211455"},
		{name: "i128", value: encode("i128:-170141183460469231731687303715884105728"), want: "-170141183460469231731687303715884105728"},
		{name: "u256", value: encode("u256:115792089237316195423570985008687907853269984665640564039457584007913129639935"), want: "115792089237316195423570985008687907853269984665640564039457584007913129639935"},
		{name: "i256", value: encode("i256:-57896044618658097711785492504343953926634992332820282019728792003956564819968"), want: "-57896044618658097711785492504343953926634992332820282019728792003956564819968"},
		{name: "bytes", value: encode("bytes:deadbeef"), want: "deadbeef"},
		{name: "bytes empty", value: encode("bytes:"), want: ""},
		{name: "string", value: encode("str:hello"), want: "hello"},
		{name: "symbol", value: encode("sym:transfer"), want: "transfer"},
		{name: "vector", value: xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vectorPointer}, want: []any{int32(-7)}},
		{name: "empty vector", value: xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &emptyVectorPointer}, want: []any{}},
		{name: "map", value: xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mapPointer}, want: map[string]any{"key": "value"}},
		{name: "account address", value: encode("addr:" + accountAddress), want: accountAddress},
		{name: "contract address", value: encode("addr:" + contractAddress), want: contractAddress},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := codec.Decode(test.value)
			if err != nil {
				t.Fatalf("Decode(): %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("Decode() = (%T) %#v, want (%T) %#v", got, got, test.want, test.want)
			}
		})
	}
}

func TestDecodeUnsupportedTypeNamesIt(t *testing.T) {
	_, err := NewRegistry().Decode(xdr.ScVal{Type: xdr.ScValTypeScvExecutableTag})
	if err == nil {
		t.Fatal("Decode() succeeded for an unsupported type")
	}
	if !strings.Contains(err.Error(), xdr.ScValTypeScvExecutableTag.String()) {
		t.Fatalf("Decode() error = %q, want it to name %s", err, xdr.ScValTypeScvExecutableTag)
	}
}
