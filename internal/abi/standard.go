package abi

import (
	"fmt"
	"sort"
	"strings"
)

// Standard is a published contract interface that a contract can be checked
// against, written as data so a new standard is a table entry, not new code.
type Standard struct {
	// Name is the identifier callers pass, such as "sep41".
	Name string
	// Functions are the entry points the standard requires.
	Functions []StandardFunction
}

// StandardFunction is one required entry point.
type StandardFunction struct {
	Name string
	// Inputs are the kinds of the parameters, in order. Parameter names are
	// not part of a standard: renaming one does not change the Wasm ABI.
	Inputs []string
	// Output is the kind of the return value; "void" for none.
	Output string
	// ReadOnly marks a function that only reads state, so it is safe to
	// simulate without arguments being meaningful.
	ReadOnly bool
}

// Deviation is one way a contract departs from a standard.
type Deviation struct {
	// Function is the standard function the problem concerns.
	Function string
	// Problem says what is wrong, quoting what was found.
	Problem string
}

// Standards lists the supported standards by name.
//
// "address" covers both Address and MuxedAddress: a contract may accept a
// muxed address where the standard says Address, and the ABI kind is the same.
var Standards = map[string]Standard{
	"sep41": {
		Name: "sep41",
		Functions: []StandardFunction{
			{Name: "allowance", Inputs: []string{"address", "address"}, Output: "i128", ReadOnly: true},
			{Name: "approve", Inputs: []string{"address", "address", "i128", "u32"}, Output: "void"},
			{Name: "balance", Inputs: []string{"address"}, Output: "i128", ReadOnly: true},
			{Name: "transfer", Inputs: []string{"address", "address", "i128"}, Output: "void"},
			{Name: "transfer_from", Inputs: []string{"address", "address", "address", "i128"}, Output: "void"},
			{Name: "burn", Inputs: []string{"address", "i128"}, Output: "void"},
			{Name: "burn_from", Inputs: []string{"address", "address", "i128"}, Output: "void"},
			{Name: "decimals", Output: "u32", ReadOnly: true},
			{Name: "name", Output: "string", ReadOnly: true},
			{Name: "symbol", Output: "string", ReadOnly: true},
		},
	},
}

// StandardNames returns the supported standard names, sorted.
func StandardNames() []string {
	names := make([]string, 0, len(Standards))
	for name := range Standards {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupStandard finds a standard by name, ignoring case. The error lists the
// supported names so a typo is a one-step fix.
func LookupStandard(name string) (Standard, error) {
	if s, ok := Standards[strings.ToLower(strings.TrimSpace(name))]; ok {
		return s, nil
	}
	return Standard{}, fmt.Errorf("unknown standard %q (supported: %s)", name, strings.Join(StandardNames(), ", "))
}

// Verify compares iface with the standard and returns one Deviation per
// problem: a missing function, a wrong argument count, a wrong argument type
// or a wrong return type. An empty result means the contract conforms.
func (s Standard) Verify(iface *Interface) []Deviation {
	var out []Deviation
	for _, want := range s.Functions {
		got, err := iface.Lookup(want.Name)
		if err != nil {
			out = append(out, Deviation{Function: want.Name, Problem: "the function is not exported"})
			continue
		}
		if len(got.Inputs) != len(want.Inputs) {
			out = append(out, Deviation{
				Function: want.Name,
				Problem:  fmt.Sprintf("takes %d argument(s), the standard requires %d: %s", len(got.Inputs), len(want.Inputs), got.Signature()),
			})
		} else {
			for i, input := range got.Inputs {
				if input.Type.Kind != want.Inputs[i] {
					out = append(out, Deviation{
						Function: want.Name,
						Problem:  fmt.Sprintf("argument %d (%s) is %s, the standard requires %s", i+1, input.Name, input.Type.Display, want.Inputs[i]),
					})
				}
			}
		}
		gotOut := "void"
		display := "void"
		if len(got.Outputs) > 0 {
			gotOut, display = got.Outputs[0].Kind, got.Outputs[0].Display
		}
		if gotOut != want.Output {
			out = append(out, Deviation{
				Function: want.Name,
				Problem:  fmt.Sprintf("returns %s, the standard requires %s", display, want.Output),
			})
		}
	}
	return out
}
