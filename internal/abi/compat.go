package abi

import (
	"fmt"
	"sort"
	"strings"
)

// Change is one difference between two versions of a contract interface.
type Change struct {
	// Breaking is true when a caller written against the old interface can
	// fail against the new one.
	Breaking bool `json:"breaking"`
	// Subject is what changed: "fn transfer" or "type Config".
	Subject string `json:"subject"`
	// Detail says how, quoting the old and new forms.
	Detail string `json:"detail"`
}

// Compat compares an old interface with its replacement and lists every
// difference, breaking or not, in a stable order: functions in the order the
// old interface declares them, then added functions, then user-defined types.
//
// Breaking: a removed function, a changed argument count or argument type, a
// changed return type, a removed or retyped struct field, a struct field that
// is added (old callers cannot build the new struct), a removed enum or union
// case, and a case whose value or payload changed. Not breaking: an added
// function, a renamed argument (argument names are not part of the Wasm ABI)
// and an added enum or union case.
//
// User-defined types are compared by structure, not by name. A struct's fields
// are sorted by name before comparing, because Soroban stores a struct as a map
// keyed by field name, so declaration order is not part of its identity. Two
// types with the same structure under different names are therefore equal,
// which keeps a pure rename from being reported as breaking.
func Compat(oldIface, newIface *Interface) []Change {
	c := comparer{old: indexTypes(oldIface), new: indexTypes(newIface)}

	var changes []Change
	newFns := make(map[string]*Function, len(newIface.Functions))
	for i := range newIface.Functions {
		newFns[newIface.Functions[i].Name] = &newIface.Functions[i]
	}
	oldFns := make(map[string]bool, len(oldIface.Functions))
	for i := range oldIface.Functions {
		oldFn := &oldIface.Functions[i]
		oldFns[oldFn.Name] = true
		newFn, ok := newFns[oldFn.Name]
		if !ok {
			changes = append(changes, Change{Breaking: true, Subject: "fn " + oldFn.Name, Detail: "function removed: " + oldFn.Signature()})
			continue
		}
		changes = append(changes, c.diffFunction(oldFn, newFn)...)
	}
	for i := range newIface.Functions {
		if fn := &newIface.Functions[i]; !oldFns[fn.Name] {
			changes = append(changes, Change{Subject: "fn " + fn.Name, Detail: "function added: " + fn.Signature()})
		}
	}

	for _, oldType := range oldIface.Types {
		changes = append(changes, c.diffType(oldType)...)
	}
	return changes
}

// HasBreaking reports whether any change is breaking.
func HasBreaking(changes []Change) bool {
	for _, c := range changes {
		if c.Breaking {
			return true
		}
	}
	return false
}

type comparer struct {
	old, new map[string]UDT
}

func indexTypes(iface *Interface) map[string]UDT {
	out := make(map[string]UDT, len(iface.Types))
	for _, t := range iface.Types {
		out[t.Name] = t
	}
	return out
}

func (c comparer) diffFunction(oldFn, newFn *Function) []Change {
	subject := "fn " + oldFn.Name
	var changes []Change

	if len(oldFn.Inputs) != len(newFn.Inputs) {
		changes = append(changes, Change{
			Breaking: true, Subject: subject,
			Detail: fmt.Sprintf("takes %d argument(s), was %d: %s", len(newFn.Inputs), len(oldFn.Inputs), newFn.Signature()),
		})
	} else {
		for i, oldIn := range oldFn.Inputs {
			newIn := newFn.Inputs[i]
			switch {
			case !c.sameType(oldIn.Type, newIn.Type):
				changes = append(changes, Change{
					Breaking: true, Subject: subject,
					Detail: fmt.Sprintf("argument %d (%s) type changed from %s to %s", i+1, oldIn.Name, oldIn.Type.Display, newIn.Type.Display),
				})
			case oldIn.Name != newIn.Name:
				changes = append(changes, Change{
					Subject: subject,
					Detail:  fmt.Sprintf("argument %d renamed from %s to %s", i+1, oldIn.Name, newIn.Name),
				})
			}
		}
	}

	oldOut, newOut := returnType(oldFn), returnType(newFn)
	if !c.sameType(oldOut, newOut) {
		changes = append(changes, Change{
			Breaking: true, Subject: subject,
			Detail: fmt.Sprintf("return type changed from %s to %s", oldOut.Display, newOut.Display),
		})
	}
	return changes
}

func returnType(fn *Function) Type {
	if len(fn.Outputs) == 0 {
		return Type{Kind: "void", Display: "void"}
	}
	return fn.Outputs[0]
}

func (c comparer) diffType(oldType UDT) []Change {
	subject := "type " + oldType.Name
	newType, ok := c.new[oldType.Name]
	if !ok {
		// A pure rename keeps the structure, so a type with an identical
		// shape under another name means nothing was lost.
		want := canonUDT(oldType, c.old, map[string]bool{})
		for _, candidate := range c.new {
			if canonUDT(candidate, c.new, map[string]bool{}) == want {
				return nil
			}
		}
		return []Change{{Breaking: true, Subject: subject, Detail: "type removed"}}
	}
	if oldType.Kind != newType.Kind {
		return []Change{{Breaking: true, Subject: subject, Detail: fmt.Sprintf("changed from a %s to a %s", oldType.Kind, newType.Kind)}}
	}
	if oldType.Kind == UDTStruct {
		return c.diffStruct(subject, oldType, newType)
	}
	return c.diffCases(subject, oldType, newType)
}

func (c comparer) diffStruct(subject string, oldType, newType UDT) []Change {
	var changes []Change
	newFields := make(map[string]Param, len(newType.Fields))
	for _, f := range newType.Fields {
		newFields[f.Name] = f
	}
	oldFields := make(map[string]bool, len(oldType.Fields))
	for _, f := range sortedParams(oldType.Fields) {
		oldFields[f.Name] = true
		nf, ok := newFields[f.Name]
		switch {
		case !ok:
			changes = append(changes, Change{Breaking: true, Subject: subject, Detail: fmt.Sprintf("field %s removed", f.Name)})
		case !c.sameType(f.Type, nf.Type):
			changes = append(changes, Change{
				Breaking: true, Subject: subject,
				Detail: fmt.Sprintf("field %s type changed from %s to %s", f.Name, f.Type.Display, nf.Type.Display),
			})
		}
	}
	for _, f := range sortedParams(newType.Fields) {
		if !oldFields[f.Name] {
			changes = append(changes, Change{Breaking: true, Subject: subject, Detail: fmt.Sprintf("field %s (%s) added", f.Name, f.Type.Display)})
		}
	}
	return changes
}

func (c comparer) diffCases(subject string, oldType, newType UDT) []Change {
	var changes []Change
	newCases := make(map[string]Case, len(newType.Cases))
	for _, k := range newType.Cases {
		newCases[k.Name] = k
	}
	oldCases := make(map[string]bool, len(oldType.Cases))
	for _, k := range oldType.Cases {
		oldCases[k.Name] = true
		nk, ok := newCases[k.Name]
		switch {
		case !ok:
			changes = append(changes, Change{Breaking: true, Subject: subject, Detail: fmt.Sprintf("case %s removed", k.Name)})
		case oldType.Kind != UDTUnion && k.Value != nk.Value:
			changes = append(changes, Change{
				Breaking: true, Subject: subject,
				Detail: fmt.Sprintf("case %s value changed from %d to %d", k.Name, k.Value, nk.Value),
			})
		case oldType.Kind == UDTUnion && !c.samePayload(k.Payload, nk.Payload):
			changes = append(changes, Change{
				Breaking: true, Subject: subject,
				Detail: fmt.Sprintf("case %s payload changed from (%s) to (%s)", k.Name, displays(k.Payload), displays(nk.Payload)),
			})
		}
	}
	for _, k := range newType.Cases {
		if !oldCases[k.Name] {
			changes = append(changes, Change{Subject: subject, Detail: fmt.Sprintf("case %s added", k.Name)})
		}
	}
	return changes
}

func displays(types []Type) string {
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = t.Display
	}
	return strings.Join(names, ", ")
}

func sortedParams(params []Param) []Param {
	out := append([]Param(nil), params...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c comparer) samePayload(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !c.sameType(a[i], b[i]) {
			return false
		}
	}
	return true
}

// sameType reports whether two types are interchangeable on the wire.
//
// A user-defined type reached by the same name in both versions is treated as
// the same type here: how its definition changed is reported once, against the
// type, rather than again at every function that mentions it. Under different
// names, the definitions must match structurally.
func (c comparer) sameType(a, b Type) bool {
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case "option":
		return c.samePtr(a.Inner, b.Inner)
	case "vec":
		return c.samePtr(a.Element, b.Element)
	case "map":
		return c.samePtr(a.Key, b.Key) && c.samePtr(a.Value, b.Value)
	case "result":
		return c.samePtr(a.Ok, b.Ok) && c.samePtr(a.Err, b.Err)
	case "tuple":
		return c.samePayload(a.Elements, b.Elements)
	case "bytes_n":
		return a.Length == b.Length
	case "udt":
		if a.Display == b.Display {
			return true
		}
		oldType, okOld := c.old[a.Display]
		newType, okNew := c.new[b.Display]
		if !okOld || !okNew {
			return false
		}
		return canonUDT(oldType, c.old, map[string]bool{}) == canonUDT(newType, c.new, map[string]bool{})
	}
	return true
}

func (c comparer) samePtr(a, b *Type) bool {
	if a == nil || b == nil {
		return a == b
	}
	return c.sameType(*a, *b)
}

// canonType renders a type so that two structurally equal types render alike.
func canonType(t Type, types map[string]UDT, visiting map[string]bool) string {
	ptr := func(p *Type) string {
		if p == nil {
			return "?"
		}
		return canonType(*p, types, visiting)
	}
	switch t.Kind {
	case "option":
		return "option<" + ptr(t.Inner) + ">"
	case "vec":
		return "vec<" + ptr(t.Element) + ">"
	case "map":
		return "map<" + ptr(t.Key) + "," + ptr(t.Value) + ">"
	case "result":
		return "result<" + ptr(t.Ok) + "," + ptr(t.Err) + ">"
	case "tuple":
		parts := make([]string, len(t.Elements))
		for i, e := range t.Elements {
			parts[i] = canonType(e, types, visiting)
		}
		return "tuple(" + strings.Join(parts, ",") + ")"
	case "bytes_n":
		return fmt.Sprintf("bytes_n(%d)", t.Length)
	case "udt":
		def, ok := types[t.Display]
		if !ok {
			return "udt:" + t.Display
		}
		return canonUDT(def, types, visiting)
	}
	return t.Kind
}

// canonUDT renders a definition with fields and cases sorted by name. A type
// that refers back to itself renders the inner reference by name, which ends
// the recursion.
func canonUDT(u UDT, types map[string]UDT, visiting map[string]bool) string {
	if visiting[u.Name] {
		return "recursive:" + u.Name
	}
	visiting[u.Name] = true
	defer delete(visiting, u.Name)

	var parts []string
	switch u.Kind {
	case UDTStruct:
		for _, f := range sortedParams(u.Fields) {
			parts = append(parts, f.Name+":"+canonType(f.Type, types, visiting))
		}
	case UDTUnion:
		for _, k := range sortedCases(u.Cases) {
			payload := make([]string, len(k.Payload))
			for i, p := range k.Payload {
				payload[i] = canonType(p, types, visiting)
			}
			parts = append(parts, k.Name+"("+strings.Join(payload, ",")+")")
		}
	default:
		for _, k := range sortedCases(u.Cases) {
			parts = append(parts, fmt.Sprintf("%s=%d", k.Name, k.Value))
		}
	}
	return u.Kind + "{" + strings.Join(parts, ";") + "}"
}

func sortedCases(cases []Case) []Case {
	out := append([]Case(nil), cases...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
