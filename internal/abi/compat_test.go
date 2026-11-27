package abi_test

import (
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/abi/abitest"
)

func parse(t *testing.T, entries ...xdr.ScSpecEntry) *abi.Interface {
	t.Helper()
	iface, err := abi.ParseWasm(abitest.Wasm(t, entries...))
	require.NoError(t, err)
	return iface
}

// find returns the change whose detail contains want, failing if none does.
func find(t *testing.T, changes []abi.Change, want string) abi.Change {
	t.Helper()
	for _, c := range changes {
		if strings.Contains(c.Detail, want) {
			return c
		}
	}
	require.Failf(t, "no change matches", "wanted %q in %+v", want, changes)
	return abi.Change{}
}

func transferFn(amount xdr.ScSpecTypeDef) xdr.ScSpecEntry {
	return abitest.Fn("transfer", nil,
		abitest.Input("from", abitest.Address), abitest.Input("to", abitest.Address), abitest.Input("amount", amount))
}

func TestParseWasmDecodesUserDefinedTypes(t *testing.T) {
	t.Parallel()

	iface := parse(t,
		abitest.Struct("Config", abitest.Field("admin", abitest.Address), abitest.Field("fee", abitest.U32)),
		abitest.Enum("Level", abitest.EnumCase("Low", 1), abitest.EnumCase("High", 2)),
		abitest.Union("Key", abitest.VoidCase("Admin"), abitest.TupleCase("Balance", abitest.Address)),
		abitest.Fn("get", abitest.Returns(abitest.UDT("Config"))),
	)

	require.Len(t, iface.Types, 3)
	assert.Equal(t, abi.UDTStruct, iface.Types[0].Kind)
	assert.Equal(t, "Config", iface.Types[0].Name)
	assert.Len(t, iface.Types[0].Fields, 2)
	assert.Equal(t, abi.UDTEnum, iface.Types[1].Kind)
	assert.Equal(t, uint32(2), iface.Types[1].Cases[1].Value)
	assert.Equal(t, abi.UDTUnion, iface.Types[2].Kind)
	assert.Len(t, iface.Types[2].Cases[1].Payload, 1)
	require.Len(t, iface.Functions, 1)
	assert.Equal(t, "Config", iface.Functions[0].Outputs[0].Display)
}

func TestCompatIdenticalInterfacesHaveNoChanges(t *testing.T) {
	t.Parallel()

	entries := []xdr.ScSpecEntry{transferFn(abitest.I128), abitest.Struct("S", abitest.Field("a", abitest.U32))}
	assert.Empty(t, abi.Compat(parse(t, entries...), parse(t, entries...)))
}

func TestCompatFunctionChanges(t *testing.T) {
	t.Parallel()

	oldIface := parse(t,
		transferFn(abitest.I128),
		abitest.Fn("balance", abitest.Returns(abitest.I128), abitest.Input("id", abitest.Address)),
		abitest.Fn("retired", nil),
		abitest.Fn("arity", nil, abitest.Input("a", abitest.U32)),
		abitest.Fn("answer", abitest.Returns(abitest.U32)),
	)
	newIface := parse(t,
		transferFn(abitest.U64), // argument type changed
		abitest.Fn("balance", abitest.Returns(abitest.I128), abitest.Input("account", abitest.Address)), // renamed
		abitest.Fn("arity", nil, abitest.Input("a", abitest.U32), abitest.Input("b", abitest.U32)),
		abitest.Fn("answer", abitest.Returns(abitest.U64)), // return changed
		abitest.Fn("fresh", nil), // added
	)

	changes := abi.Compat(oldIface, newIface)

	assert.True(t, find(t, changes, "function removed").Breaking)
	assert.Equal(t, "fn retired", find(t, changes, "function removed").Subject)
	assert.True(t, find(t, changes, "argument 3 (amount) type changed from i128 to u64").Breaking)
	assert.True(t, find(t, changes, "takes 2 argument(s), was 1").Breaking)
	assert.True(t, find(t, changes, "return type changed from u32 to u64").Breaking)
	assert.False(t, find(t, changes, "argument 1 renamed from id to account").Breaking)
	added := find(t, changes, "function added")
	assert.False(t, added.Breaking)
	assert.Equal(t, "fn fresh", added.Subject)
	assert.True(t, abi.HasBreaking(changes))
}

func TestCompatOnlyNonBreakingChangesPass(t *testing.T) {
	t.Parallel()

	oldIface := parse(t, abitest.Fn("a", nil, abitest.Input("x", abitest.U32)))
	newIface := parse(t, abitest.Fn("a", nil, abitest.Input("y", abitest.U32)), abitest.Fn("b", nil))

	changes := abi.Compat(oldIface, newIface)
	assert.Len(t, changes, 2)
	assert.False(t, abi.HasBreaking(changes))
}

func TestCompatStructFieldChanges(t *testing.T) {
	t.Parallel()

	use := abitest.Fn("get", abitest.Returns(abitest.UDT("Config")))
	oldIface := parse(t, use, abitest.Struct("Config",
		abitest.Field("admin", abitest.Address), abitest.Field("fee", abitest.U32), abitest.Field("gone", abitest.Bool)))
	newIface := parse(t, use, abitest.Struct("Config",
		abitest.Field("admin", abitest.Address), abitest.Field("fee", abitest.U64), abitest.Field("extra", abitest.Bool)))

	changes := abi.Compat(oldIface, newIface)

	assert.Equal(t, "type Config", find(t, changes, "field gone removed").Subject)
	assert.True(t, find(t, changes, "field gone removed").Breaking)
	assert.True(t, find(t, changes, "field fee type changed from u32 to u64").Breaking)
	assert.True(t, find(t, changes, "field extra (bool) added").Breaking)
	// The function still names Config, so the change is reported once, on the type.
	for _, c := range changes {
		assert.NotEqual(t, "fn get", c.Subject)
	}
}

func TestCompatStructFieldOrderDoesNotMatter(t *testing.T) {
	t.Parallel()

	oldIface := parse(t, abitest.Struct("S", abitest.Field("a", abitest.U32), abitest.Field("b", abitest.Bool)))
	newIface := parse(t, abitest.Struct("S", abitest.Field("b", abitest.Bool), abitest.Field("a", abitest.U32)))
	assert.Empty(t, abi.Compat(oldIface, newIface))
}

func TestCompatTypesAreComparedByStructureNotName(t *testing.T) {
	t.Parallel()

	oldIface := parse(t,
		abitest.Fn("get", abitest.Returns(abitest.UDT("Config")), abitest.Input("c", abitest.UDT("Config"))),
		abitest.Struct("Config", abitest.Field("a", abitest.U32), abitest.Field("b", abitest.Bool)),
	)
	renamed := parse(t,
		abitest.Fn("get", abitest.Returns(abitest.UDT("Settings")), abitest.Input("c", abitest.UDT("Settings"))),
		abitest.Struct("Settings", abitest.Field("b", abitest.Bool), abitest.Field("a", abitest.U32)),
	)
	assert.Empty(t, abi.Compat(oldIface, renamed), "same structure under another name is the same type")

	different := parse(t,
		abitest.Fn("get", abitest.Returns(abitest.UDT("Settings")), abitest.Input("c", abitest.UDT("Settings"))),
		abitest.Struct("Settings", abitest.Field("a", abitest.U32)),
	)
	changes := abi.Compat(oldIface, different)
	assert.True(t, abi.HasBreaking(changes))
	assert.True(t, find(t, changes, "return type changed from Config to Settings").Breaking)
	assert.True(t, find(t, changes, "type removed").Breaking)
}

func TestCompatEnumCases(t *testing.T) {
	t.Parallel()

	oldIface := parse(t, abitest.Enum("Level",
		abitest.EnumCase("Low", 1), abitest.EnumCase("Mid", 2), abitest.EnumCase("High", 3)))
	newIface := parse(t, abitest.Enum("Level",
		abitest.EnumCase("Low", 1), abitest.EnumCase("Mid", 5), abitest.EnumCase("Max", 4)))

	changes := abi.Compat(oldIface, newIface)

	assert.True(t, find(t, changes, "case High removed").Breaking)
	assert.True(t, find(t, changes, "case Mid value changed from 2 to 5").Breaking)
	assert.False(t, find(t, changes, "case Max added").Breaking)
}

func TestCompatAddingAnEnumCaseIsNotBreakingEvenInASignature(t *testing.T) {
	t.Parallel()

	use := abitest.Fn("set", nil, abitest.Input("l", abitest.UDT("Level")))
	oldIface := parse(t, use, abitest.Enum("Level", abitest.EnumCase("Low", 1)))
	newIface := parse(t, use, abitest.Enum("Level", abitest.EnumCase("Low", 1), abitest.EnumCase("High", 2)))

	changes := abi.Compat(oldIface, newIface)
	require.Len(t, changes, 1)
	assert.False(t, changes[0].Breaking)
	assert.False(t, abi.HasBreaking(changes))
}

func TestCompatUnionCases(t *testing.T) {
	t.Parallel()

	oldIface := parse(t, abitest.Union("Key",
		abitest.VoidCase("Admin"),
		abitest.TupleCase("Balance", abitest.Address),
		abitest.TupleCase("Gone", abitest.U32)))
	newIface := parse(t, abitest.Union("Key",
		abitest.VoidCase("Admin"),
		abitest.TupleCase("Balance", abitest.Address, abitest.U32),
		abitest.VoidCase("Fresh")))

	changes := abi.Compat(oldIface, newIface)

	assert.True(t, find(t, changes, "case Gone removed").Breaking)
	assert.True(t, find(t, changes, "case Balance payload changed").Breaking)
	assert.False(t, find(t, changes, "case Fresh added").Breaking)
}

func TestCompatTypeKindChange(t *testing.T) {
	t.Parallel()

	oldIface := parse(t, abitest.Enum("T", abitest.EnumCase("A", 1)))
	newIface := parse(t, abitest.Union("T", abitest.VoidCase("A")))

	changes := abi.Compat(oldIface, newIface)
	require.Len(t, changes, 1)
	assert.True(t, changes[0].Breaking)
	assert.Contains(t, changes[0].Detail, "enum to a union")
}

func TestCompatNestedTypesAreCompared(t *testing.T) {
	t.Parallel()

	oldIface := parse(t, abitest.Fn("f", nil, abitest.Input("v", abitest.Vec(abitest.U32))))
	newIface := parse(t, abitest.Fn("f", nil, abitest.Input("v", abitest.Vec(abitest.U64))))

	changes := abi.Compat(oldIface, newIface)
	require.Len(t, changes, 1)
	assert.True(t, changes[0].Breaking)
}

func TestCompatHandlesSelfReferentialTypes(t *testing.T) {
	t.Parallel()

	node := abitest.Struct("Node", abitest.Field("next", abitest.Vec(abitest.UDT("Node"))), abitest.Field("v", abitest.U32))
	renamed := abitest.Struct("Link", abitest.Field("next", abitest.Vec(abitest.UDT("Link"))), abitest.Field("v", abitest.U32))
	assert.Empty(t, abi.Compat(parse(t, node), parse(t, node)))
	assert.NotPanics(t, func() { abi.Compat(parse(t, node), parse(t, renamed)) })
}

func TestParseWasmRejectsAModuleWithoutASpec(t *testing.T) {
	t.Parallel()

	_, err := abi.ParseWasm([]byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00})
	require.Error(t, err)
	_, err = abi.ParseWasm([]byte("not wasm"))
	require.Error(t, err)
}
