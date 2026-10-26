// Package abi types simulate arguments from a contract's published interface.
//
// SoroProbe cannot read a contract's interface from the ledger cheaply, so on
// its own it infers argument types from their spelling, and a bare "5" becomes
// an i128. A contract that expects u32 then rejects the call with a host error
// that says nothing about the argument. When an interface source such as a
// SoroVault registry is configured, Annotate replaces that guess with the type
// the contract actually declares, and catches a wrong argument count or a
// misspelled function name before anything is simulated.
package abi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUnknownContract reports that the interface source has no record of the
// contract. Callers fall back to inference rather than failing, because an
// unregistered contract is still a contract worth probing.
var ErrUnknownContract = errors.New("abi: contract is not in the interface registry")

// ErrNoWasm reports that a contract has no Wasm interface to decode.
var ErrNoWasm = errors.New("abi: contract has no wasm interface")

// ErrNoSuchFunction reports that the contract's interface does not export the
// requested function. Unlike ErrUnknownContract this is a definite answer: a
// simulation could only fail.
var ErrNoSuchFunction = errors.New("abi: contract does not export that function")

// ErrArgumentCount reports a call with more or fewer arguments than the
// function declares.
var ErrArgumentCount = errors.New("abi: wrong number of arguments")

// Type is a value type in a contract interface. It mirrors the subset of
// SoroVault's ABI JSON that argument typing needs.
type Type struct {
	Kind    string `json:"kind"`
	Display string `json:"display"`
	// Inner is the wrapped type of an option.
	Inner *Type `json:"inner,omitempty"`
	// Element is the item type of a vec.
	Element *Type `json:"element,omitempty"`
	// Key and Value are the halves of a map.
	Key   *Type `json:"key,omitempty"`
	Value *Type `json:"value,omitempty"`
}

// Param is a named function input.
type Param struct {
	Name string `json:"name"`
	Type Type   `json:"type"`
}

// Function is a contract entry point.
type Function struct {
	Name    string  `json:"name"`
	Doc     string  `json:"doc,omitempty"`
	Inputs  []Param `json:"inputs"`
	Outputs []Type  `json:"outputs"`
}

// Interface is the part of a contract interface Annotate needs.
type Interface struct {
	Functions []Function `json:"functions"`
}

// Lookup returns the named function, or an ErrNoSuchFunction error listing
// the functions that do exist, so a typo is a one-step fix.
func (i *Interface) Lookup(name string) (*Function, error) {
	names := make([]string, 0, len(i.Functions))
	for idx := range i.Functions {
		if i.Functions[idx].Name == name {
			return &i.Functions[idx], nil
		}
		names = append(names, i.Functions[idx].Name)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("%w: %q (available: %s)", ErrNoSuchFunction, name, strings.Join(names, ", "))
}

// Signature renders the function in Rust-like notation, as SoroVault does:
// "fn transfer(from: Address, to: Address, amount: i128)".
func (f *Function) Signature() string {
	var b strings.Builder
	b.WriteString("fn ")
	b.WriteString(f.Name)
	b.WriteByte('(')
	for i, p := range f.Inputs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(p.Name)
		b.WriteString(": ")
		b.WriteString(p.Type.Display)
	}
	b.WriteByte(')')
	if len(f.Outputs) > 0 {
		b.WriteString(" -> ")
		b.WriteString(f.Outputs[0].Display)
	}
	return b.String()
}

// Source supplies contract interfaces.
type Source interface {
	// Interface returns the current interface of contractID on the given
	// network. It returns an error wrapping ErrUnknownContract when the
	// source has no record of the contract.
	Interface(ctx context.Context, network, contractID string) (*Interface, error)
}

// prefixes maps a scalar interface kind onto the scval argument prefix that
// encodes it. Vec and map are typed element by element instead (see
// annotator); tuples and user-defined types have no literal form, so their
// arguments are passed through and must be written explicitly.
var prefixes = map[string]string{
	"bool":      "bool",
	"void":      "void",
	"u32":       "u32",
	"i32":       "i32",
	"u64":       "u64",
	"i64":       "i64",
	"u128":      "u128",
	"i128":      "i128",
	"u256":      "u256",
	"i256":      "i256",
	"timepoint": "timepoint",
	"duration":  "duration",
	"bytes":     "bytes",
	"bytes_n":   "bytes",
	"string":    "string",
	"symbol":    "symbol",
	"address":   "address",
}

// Annotate checks args against fn's declared inputs and gives each untyped
// argument the prefix of its declared type.
//
// hasType reports whether an argument already names its type; those are left
// exactly as written, so an explicit prefix always wins over the interface.
// An Option<T> parameter takes "void"/"null" as None and otherwise types the
// literal as T. A Vec or Map parameter given as a JSON array has each element
// typed in turn, recursively, so [1, 2] for a Vec<u32> becomes
// vec:["u32:1","u32:2"].
func Annotate(fn *Function, args []string, hasType func(string) bool) ([]string, error) {
	if len(args) != len(fn.Inputs) {
		return nil, fmt.Errorf("%w: %s takes %d argument(s), got %d",
			ErrArgumentCount, fn.Signature(), len(fn.Inputs), len(args))
	}

	a := annotator{hasType: hasType}
	out := make([]string, len(args))
	for i, arg := range args {
		spec, err := a.spec(fn.Inputs[i].Type, arg)
		if err != nil {
			return nil, fmt.Errorf("argument %s: %w", fn.Inputs[i].Name, err)
		}
		out[i] = spec
	}
	return out, nil
}

type annotator struct {
	hasType func(string) bool
}

// spec types one literal as t. A literal that already names its type, or
// whose declared type has no literal form, is returned unchanged.
func (a annotator) spec(t Type, literal string) (string, error) {
	if a.hasType(literal) {
		return literal, nil
	}

	switch t.Kind {
	case "option":
		switch strings.ToLower(literal) {
		case "void", "null":
			return "void:" + literal, nil
		}
		if t.Inner == nil {
			return literal, nil
		}
		return a.spec(*t.Inner, literal)

	case "vec":
		if !strings.HasPrefix(strings.TrimSpace(literal), "[") {
			return literal, nil
		}
		return a.vec(t, literal)

	case "map":
		if !strings.HasPrefix(strings.TrimSpace(literal), "[") {
			return literal, nil
		}
		return a.mapSpec(t, literal)
	}

	if p, ok := prefixes[t.Kind]; ok {
		return p + ":" + literal, nil
	}
	return literal, nil
}

// vec types each element of a JSON array literal as the vec's element type
// and re-emits it in scval's vec:[...] form, with every element a spec.
func (a annotator) vec(t Type, literal string) (string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(literal), &items); err != nil {
		return "", fmt.Errorf("%s is not a valid JSON array: %w", t.Display, err)
	}
	specs := make([]string, len(items))
	for i, item := range items {
		s, err := a.element(t.Element, item)
		if err != nil {
			return "", fmt.Errorf("[%d]: %w", i, err)
		}
		specs[i] = s
	}
	return encodeCollection("vec", specs)
}

// mapSpec types the keys and values of a JSON [[key, value], ...] literal.
func (a annotator) mapSpec(t Type, literal string) (string, error) {
	var items [][]json.RawMessage
	if err := json.Unmarshal([]byte(literal), &items); err != nil {
		return "", fmt.Errorf("%s must be a JSON array of [key, value] pairs: %w", t.Display, err)
	}
	pairs := make([][2]string, len(items))
	for i, pair := range items {
		if len(pair) != 2 {
			return "", fmt.Errorf("[%d]: each entry must be a [key, value] pair", i)
		}
		k, err := a.element(t.Key, pair[0])
		if err != nil {
			return "", fmt.Errorf("[%d] key: %w", i, err)
		}
		v, err := a.element(t.Value, pair[1])
		if err != nil {
			return "", fmt.Errorf("[%d] value: %w", i, err)
		}
		pairs[i] = [2]string{k, v}
	}
	body, err := json.Marshal(pairs)
	if err != nil {
		return "", err
	}
	return "map:" + string(body), nil
}

// element turns one JSON element into a typed spec string. A nested array is
// passed through as a JSON-array literal so that a vec or map element type
// can type it in turn.
func (a annotator) element(t *Type, raw json.RawMessage) (string, error) {
	literal := strings.TrimSpace(string(raw))
	if strings.HasPrefix(literal, `"`) {
		if err := json.Unmarshal(raw, &literal); err != nil {
			return "", err
		}
	}
	if t == nil {
		if strings.HasPrefix(literal, "[") {
			// Untyped nesting keeps its meaning: a nested vec.
			return "vec:" + literal, nil
		}
		return literal, nil
	}
	s, err := a.spec(*t, literal)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(s, "[") {
		// The element type had no literal form; keep the array a vec.
		return "vec:" + s, nil
	}
	return s, nil
}

func encodeCollection(kind string, specs []string) (string, error) {
	body, err := json.Marshal(specs)
	if err != nil {
		return "", err
	}
	return kind + ":" + string(body), nil
}
