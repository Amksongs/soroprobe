package abi

import "github.com/stellar/go-stellar-sdk/xdr"

// UDT kinds.
const (
	UDTStruct    = "struct"
	UDTUnion     = "union"
	UDTEnum      = "enum"
	UDTErrorEnum = "error_enum"
)

// UDT is a user-defined type a contract declares in its spec.
type UDT struct {
	// Kind is "struct", "union", "enum" or "error_enum".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Fields are the members of a struct. A tuple struct's fields are named
	// "0", "1", and so on.
	Fields []Param `json:"fields,omitempty"`
	// Cases are the variants of a union, enum or error enum.
	Cases []Case `json:"cases,omitempty"`
}

// Case is one variant of a union, enum or error enum.
type Case struct {
	Name string `json:"name"`
	// Value is the integer an enum or error enum case is encoded as.
	Value uint32 `json:"value,omitempty"`
	// Payload are the types a union case carries; empty for a void case.
	Payload []Type `json:"payload,omitempty"`
}

// specUDT decodes a spec entry that declares a user-defined type.
func specUDT(entry xdr.ScSpecEntry) (UDT, bool) {
	if s, ok := entry.GetUdtStructV0(); ok {
		udt := UDT{Kind: UDTStruct, Name: s.Name}
		for _, f := range s.Fields {
			udt.Fields = append(udt.Fields, Param{Name: f.Name, Type: specType(f.Type)})
		}
		return udt, true
	}
	if u, ok := entry.GetUdtUnionV0(); ok {
		udt := UDT{Kind: UDTUnion, Name: u.Name}
		for _, c := range u.Cases {
			if v, ok := c.GetVoidCase(); ok {
				udt.Cases = append(udt.Cases, Case{Name: v.Name})
			} else if tc, ok := c.GetTupleCase(); ok {
				kase := Case{Name: tc.Name}
				for _, t := range tc.Type {
					kase.Payload = append(kase.Payload, specType(t))
				}
				udt.Cases = append(udt.Cases, kase)
			}
		}
		return udt, true
	}
	if e, ok := entry.GetUdtEnumV0(); ok {
		udt := UDT{Kind: UDTEnum, Name: e.Name}
		for _, c := range e.Cases {
			udt.Cases = append(udt.Cases, Case{Name: c.Name, Value: uint32(c.Value)})
		}
		return udt, true
	}
	if e, ok := entry.GetUdtErrorEnumV0(); ok {
		udt := UDT{Kind: UDTErrorEnum, Name: e.Name}
		for _, c := range e.Cases {
			udt.Cases = append(udt.Cases, Case{Name: c.Name, Value: uint32(c.Value)})
		}
		return udt, true
	}
	return UDT{}, false
}
