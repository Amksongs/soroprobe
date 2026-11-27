// Package abitest builds Wasm modules that carry only a contract spec and
// contract metadata, so tests can exercise interface decoding and comparison
// without a compiled contract or a network.
package abitest

import (
	"bytes"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Type returns a scalar type definition.
func Type(t xdr.ScSpecType) xdr.ScSpecTypeDef { return xdr.ScSpecTypeDef{Type: t} }

// U32, I128, Address, Bool and String are the scalar types tests use most.
var (
	U32     = Type(xdr.ScSpecTypeScSpecTypeU32)
	U64     = Type(xdr.ScSpecTypeScSpecTypeU64)
	I128    = Type(xdr.ScSpecTypeScSpecTypeI128)
	Address = Type(xdr.ScSpecTypeScSpecTypeAddress)
	Bool    = Type(xdr.ScSpecTypeScSpecTypeBool)
	String  = Type(xdr.ScSpecTypeScSpecTypeString)
)

// UDT refers to a user-defined type by name.
func UDT(name string) xdr.ScSpecTypeDef {
	return xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: name}}
}

// Vec is a vec of elem.
func Vec(elem xdr.ScSpecTypeDef) xdr.ScSpecTypeDef {
	return xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeVec, Vec: &xdr.ScSpecTypeVec{ElementType: elem}}
}

// Input is a named function parameter.
func Input(name string, t xdr.ScSpecTypeDef) xdr.ScSpecFunctionInputV0 {
	return xdr.ScSpecFunctionInputV0{Name: name, Type: t}
}

// Fn declares an exported function. A nil ret means it returns void.
func Fn(name string, ret *xdr.ScSpecTypeDef, inputs ...xdr.ScSpecFunctionInputV0) xdr.ScSpecEntry {
	fn := &xdr.ScSpecFunctionV0{Name: xdr.ScSymbol(name), Inputs: inputs}
	if ret != nil {
		fn.Outputs = []xdr.ScSpecTypeDef{*ret}
	}
	return xdr.ScSpecEntry{Kind: xdr.ScSpecEntryKindScSpecEntryFunctionV0, FunctionV0: fn}
}

// Returns is shorthand for taking the address of a return type.
func Returns(t xdr.ScSpecTypeDef) *xdr.ScSpecTypeDef { return &t }

// Struct declares a struct type.
func Struct(name string, fields ...xdr.ScSpecUdtStructFieldV0) xdr.ScSpecEntry {
	return xdr.ScSpecEntry{
		Kind:        xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
		UdtStructV0: &xdr.ScSpecUdtStructV0{Name: name, Fields: fields},
	}
}

// Field is a struct field.
func Field(name string, t xdr.ScSpecTypeDef) xdr.ScSpecUdtStructFieldV0 {
	return xdr.ScSpecUdtStructFieldV0{Name: name, Type: t}
}

// Enum declares an integer enum.
func Enum(name string, cases ...xdr.ScSpecUdtEnumCaseV0) xdr.ScSpecEntry {
	return xdr.ScSpecEntry{
		Kind:      xdr.ScSpecEntryKindScSpecEntryUdtEnumV0,
		UdtEnumV0: &xdr.ScSpecUdtEnumV0{Name: name, Cases: cases},
	}
}

// EnumCase is one enum case.
func EnumCase(name string, value uint32) xdr.ScSpecUdtEnumCaseV0 {
	return xdr.ScSpecUdtEnumCaseV0{Name: name, Value: xdr.Uint32(value)}
}

// Union declares a tagged union.
func Union(name string, cases ...xdr.ScSpecUdtUnionCaseV0) xdr.ScSpecEntry {
	return xdr.ScSpecEntry{
		Kind:       xdr.ScSpecEntryKindScSpecEntryUdtUnionV0,
		UdtUnionV0: &xdr.ScSpecUdtUnionV0{Name: name, Cases: cases},
	}
}

// VoidCase is a union case with no payload.
func VoidCase(name string) xdr.ScSpecUdtUnionCaseV0 {
	return xdr.ScSpecUdtUnionCaseV0{
		Kind:     xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0,
		VoidCase: &xdr.ScSpecUdtUnionCaseVoidV0{Name: name},
	}
}

// TupleCase is a union case carrying the given types.
func TupleCase(name string, types ...xdr.ScSpecTypeDef) xdr.ScSpecUdtUnionCaseV0 {
	return xdr.ScSpecUdtUnionCaseV0{
		Kind:      xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0,
		TupleCase: &xdr.ScSpecUdtUnionCaseTupleV0{Name: name, Type: types},
	}
}

// Meta is one contract metadata key and value.
type Meta struct{ Key, Val string }

// Wasm returns a module whose contractspecv0 section holds the entries.
func Wasm(t testing.TB, entries ...xdr.ScSpecEntry) []byte {
	t.Helper()
	return WasmWithMeta(t, entries, nil)
}

// WasmWithMeta returns a module with a contractspecv0 section for the entries
// and a contractmetav0 section for the metadata, when there is any.
func WasmWithMeta(t testing.TB, entries []xdr.ScSpecEntry, meta []Meta) []byte {
	t.Helper()

	var spec bytes.Buffer
	for _, e := range entries {
		b, err := e.MarshalBinary()
		if err != nil {
			t.Fatalf("encode spec entry: %v", err)
		}
		spec.Write(b)
	}

	module := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00}
	module = append(module, customSection("contractspecv0", spec.Bytes())...)

	if len(meta) > 0 {
		var buf bytes.Buffer
		for _, m := range meta {
			entry := xdr.ScMetaEntry{
				Kind: xdr.ScMetaKindScMetaV0,
				V0:   &xdr.ScMetaV0{Key: m.Key, Val: m.Val},
			}
			b, err := entry.MarshalBinary()
			if err != nil {
				t.Fatalf("encode meta entry: %v", err)
			}
			buf.Write(b)
		}
		module = append(module, customSection("contractmetav0", buf.Bytes())...)
	}
	return module
}

func customSection(name string, payload []byte) []byte {
	body := append(uleb(len(name)), name...)
	body = append(body, payload...)
	section := []byte{0x00}
	section = append(section, uleb(len(body))...)
	return append(section, body...)
}

func uleb(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}
