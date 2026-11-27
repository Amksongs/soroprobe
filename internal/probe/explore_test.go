package probe_test

import (
	"context"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi/abitest"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// --- call trace (#149) ---

func sym(s string) xdr.ScVal {
	v := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &v}
}

func u32(n uint32) xdr.ScVal {
	v := xdr.Uint32(n)
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}
}

func contractBytes(t *testing.T, id string) xdr.ScVal {
	t.Helper()
	addr, err := stellar.ParseContractID(id)
	require.NoError(t, err)
	b := xdr.ScBytes(addr.ContractId[:])
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b}
}

func diagnostic(t *testing.T, topics []xdr.ScVal, data xdr.ScVal) string {
	t.Helper()
	b64, err := xdr.MarshalBase64(xdr.DiagnosticEvent{Event: xdr.ContractEvent{
		Type: xdr.ContractEventTypeDiagnostic,
		Body: xdr.ContractEventBody{V: 0, V0: &xdr.ContractEventV0{Topics: topics, Data: data}},
	}})
	require.NoError(t, err)
	return b64
}

func vecOf(items ...xdr.ScVal) xdr.ScVal {
	v := xdr.ScVec(items)
	pv := &v
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &pv}
}

func TestSimulateTraceBuildsTheCallTree(t *testing.T) {
	fake := stellartest.NewFake(t)
	// The Wasm contract declares swap(amount, min_out); the SAC has no
	// interface, so its arguments stay unnamed.
	fake.DeployWasm(t, stellartest.WasmContract, abitest.Wasm(t,
		abitest.Fn("swap", nil, abitest.Input("amount", abitest.U32), abitest.Input("min_out", abitest.U32))))
	sim := fake.Simulations["decimals"]
	sim.EventsXDR = []string{
		diagnostic(t, []xdr.ScVal{sym("fn_call"), contractBytes(t, stellartest.WasmContract), sym("swap")}, vecOf(u32(10), u32(9))),
		diagnostic(t, []xdr.ScVal{sym("fn_call"), contractBytes(t, stellartest.SACContract), sym("transfer")}, u32(10)),
		diagnostic(t, []xdr.ScVal{sym("fn_return"), sym("transfer")}, xdr.ScVal{Type: xdr.ScValTypeScvVoid}),
		diagnostic(t, []xdr.ScVal{sym("fn_return"), sym("swap")}, u32(9)),
	}
	fake.Simulations["swap"] = sim
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.WasmContract, Function: "swap", Args: []string{"u32:10", "u32:9"}, Trace: true,
	})
	require.NoError(t, err)
	require.Len(t, res.Trace, 1)
	root := res.Trace[0]
	assert.Equal(t, stellartest.WasmContract, root.Contract)
	assert.Equal(t, "swap", root.Function)
	assert.Equal(t, []any{uint32(10), uint32(9)}, root.Args)
	assert.Equal(t, []string{"amount", "min_out"}, root.ArgNames)
	assert.True(t, root.Returned)
	assert.Equal(t, uint32(9), root.Return)
	require.Len(t, root.Calls, 1)
	inner := root.Calls[0]
	assert.Equal(t, stellartest.SACContract, inner.Contract)
	assert.Equal(t, "transfer", inner.Function)
	assert.Equal(t, []any{uint32(10)}, inner.Args)
	assert.Empty(t, inner.ArgNames, "a SAC has no interface to name arguments from")
	assert.True(t, inner.Returned)
}

func TestSimulateTraceShowsErrorsAtTheirFrame(t *testing.T) {
	fake := stellartest.NewFake(t)
	code := xdr.Uint32(3)
	errVal := xdr.ScVal{Type: xdr.ScValTypeScvError, Error: &xdr.ScError{Type: xdr.ScErrorTypeSceContract, ContractCode: &code}}
	msg := xdr.ScString("balance too low")
	resp := protocol.SimulateTransactionResponse{
		Error: "HostError: Error(Contract, #3)",
		EventsXDR: []string{
			diagnostic(t, []xdr.ScVal{sym("fn_call"), contractBytes(t, stellartest.WasmContract), sym("swap")}, xdr.ScVal{Type: xdr.ScValTypeScvVoid}),
			diagnostic(t, []xdr.ScVal{sym("fn_call"), contractBytes(t, stellartest.SACContract), sym("transfer")}, u32(1)),
			diagnostic(t, []xdr.ScVal{sym("error"), errVal}, xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &msg}),
			// The host repeats the error as it unwinds.
			diagnostic(t, []xdr.ScVal{sym("error"), errVal}, xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &msg}),
		},
	}
	fake.Simulations["swap"] = resp
	res, err := newProber(t, fake, health.Thresholds{}).Simulate(context.Background(),
		probe.SimulateRequest{ContractID: stellartest.WasmContract, Function: "swap", Trace: true})
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Len(t, res.Trace, 1)
	assert.Empty(t, res.Trace[0].Error, "the outer frame did not raise it")
	assert.False(t, res.Trace[0].Returned)
	require.Len(t, res.Trace[0].Calls, 1)
	assert.Equal(t, "Error(Contract, #3): balance too low", res.Trace[0].Calls[0].Error)
	assert.Empty(t, res.Trace[0].Args)
}

func TestSimulateWithoutTraceHasNone(t *testing.T) {
	res, err := newProber(t, stellartest.NewFake(t), health.Thresholds{}).Simulate(context.Background(),
		probe.SimulateRequest{ContractID: stellartest.SACContract, Function: "decimals"})
	require.NoError(t, err)
	assert.Nil(t, res.Trace)
	assert.NotEmpty(t, res.FootprintEntries)
}

// --- prebuilt envelopes (#148) ---

func envelopeFor(t *testing.T, contract, fn string, args ...xdr.ScVal) string {
	t.Helper()
	addr, err := stellar.ParseContractID(contract)
	require.NoError(t, err)
	b64, err := stellar.BuildInvocationTx(stellar.InvocationParams{
		SourceAccount: stellartest.SourceAccount, Contract: addr, Function: fn, Args: args,
	})
	require.NoError(t, err)
	return b64
}

func TestSimulateEnvelopeDecodesTheCall(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.DeployWasm(t, stellartest.WasmContract, abitest.Wasm(t,
		abitest.Fn("decimals", nil, abitest.Input("scale", abitest.U32))))
	b64 := envelopeFor(t, stellartest.WasmContract, "decimals", u32(7))
	res, err := newProber(t, fake, health.Thresholds{}).SimulateEnvelope(context.Background(), probe.EnvelopeRequest{EnvelopeXDR: "  " + b64 + "\n"})
	require.NoError(t, err)

	assert.True(t, res.FromEnvelope)
	assert.True(t, res.Success)
	assert.Equal(t, stellartest.WasmContract, res.ContractID)
	assert.Equal(t, "decimals", res.Function)
	assert.Equal(t, []any{uint32(7)}, res.DecodedArgs)
	assert.Equal(t, "fn decimals(scale: u32)", res.Signature)
	assert.Equal(t, stellartest.SourceAccount, res.Source)
	// Simulated exactly as given.
	require.Len(t, fake.SimulateRequests, 1)
	assert.Equal(t, b64, fake.SimulateRequests[0].Transaction)
}

func TestSimulateEnvelopeRejectsWhatItCannotRun(t *testing.T) {
	p := newProber(t, stellartest.NewFake(t), health.Thresholds{})
	ctx := context.Background()

	_, err := p.SimulateEnvelope(ctx, probe.EnvelopeRequest{EnvelopeXDR: "not base64 xdr"})
	require.ErrorIs(t, err, probe.ErrNotSorobanEnvelope)

	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(envelopeFor(t, stellartest.SACContract, "decimals"), &env))
	two := env
	two.V1.Tx.Operations = append(append([]xdr.Operation{}, env.V1.Tx.Operations...), env.V1.Tx.Operations[0])
	b64, err := xdr.MarshalBase64(two)
	require.NoError(t, err)
	_, err = p.SimulateEnvelope(ctx, probe.EnvelopeRequest{EnvelopeXDR: b64})
	require.ErrorIs(t, err, probe.ErrNotSorobanEnvelope)
	assert.Contains(t, err.Error(), "2 operations")

	classic := env
	classic.V1.Tx.Operations = []xdr.Operation{{Body: xdr.OperationBody{
		Type: xdr.OperationTypeBumpSequence, BumpSequenceOp: &xdr.BumpSequenceOp{BumpTo: 5},
	}}}
	b64, err = xdr.MarshalBase64(classic)
	require.NoError(t, err)
	_, err = p.SimulateEnvelope(ctx, probe.EnvelopeRequest{EnvelopeXDR: b64})
	require.ErrorIs(t, err, probe.ErrNotSorobanEnvelope)
	assert.Contains(t, err.Error(), "runs no contract")
}

// --- footprint comparison (#145) ---

func TestCompareSimulations(t *testing.T) {
	entry := func(access, kind, contract string, key any) probe.FootprintEntry {
		return probe.FootprintEntry{Access: access, Kind: kind, Contract: contract, Key: key, Durability: "persistent", Label: kind + " " + contract}
	}
	a := &probe.SimulateResult{
		Cost: probe.Cost{Instructions: 1000, DiskReadBytes: 100},
		FootprintEntries: []probe.FootprintEntry{
			entry(probe.AccessReadOnly, "instance", "self", nil),
			entry(probe.AccessReadOnly, "data", "self", "Config"),
			entry(probe.AccessReadWrite, "data", "self", "Old"),
		},
	}
	b := &probe.SimulateResult{
		Cost: probe.Cost{Instructions: 1500, DiskReadBytes: 100},
		FootprintEntries: []probe.FootprintEntry{
			entry(probe.AccessReadOnly, "instance", "self", nil),
			entry(probe.AccessReadWrite, "data", "self", "Config"),
			entry(probe.AccessReadOnly, "data", "self", "New"),
		},
	}
	d, err := probe.CompareSimulations("v1", a, "v2", b)
	require.NoError(t, err)
	assert.False(t, d.Same)
	require.Len(t, d.OnlyInA, 1)
	assert.Equal(t, "Old", d.OnlyInA[0].Key)
	require.Len(t, d.OnlyInB, 1)
	assert.Equal(t, "New", d.OnlyInB[0].Key)
	require.Len(t, d.Moved, 1)
	assert.Equal(t, probe.AccessReadOnly, d.Moved[0].From)
	assert.Equal(t, probe.AccessReadWrite, d.Moved[0].To)
	for _, f := range d.Resources {
		if f.Name == "cpu instructions" {
			assert.Equal(t, int64(500), f.Delta)
		}
	}

	same, err := probe.CompareSimulations("a", a, "a again", a)
	require.NoError(t, err)
	assert.True(t, same.Same)
	assert.Empty(t, same.OnlyInA)

	_, err = probe.CompareSimulations("old", &probe.SimulateResult{Footprint: probe.Footprint{ReadOnly: []string{"x"}}}, "b", b)
	require.ErrorContains(t, err, "no footprint_entries")
}

func TestFootprintEntriesNameTheCalledContractSelf(t *testing.T) {
	res, err := newProber(t, stellartest.NewFake(t), health.Thresholds{}).Simulate(context.Background(),
		probe.SimulateRequest{ContractID: stellartest.SACContract, Function: "decimals"})
	require.NoError(t, err)
	require.NotEmpty(t, res.FootprintEntries)
	found := false
	for _, e := range res.FootprintEntries {
		if e.Kind == "instance" && e.Contract == "self" {
			found = true
		}
		assert.NotEmpty(t, e.Label)
	}
	assert.True(t, found, "%+v", res.FootprintEntries)
}

// --- sweep (#146) ---

func storeTxInstructionLimit(t *testing.T, fake *stellartest.Fake, limit int64) {
	t.Helper()
	fake.StoreEntry(t, xdr.LedgerKey{
		Type:          xdr.LedgerEntryTypeConfigSetting,
		ConfigSetting: &xdr.LedgerKeyConfigSetting{ConfigSettingId: xdr.ConfigSettingIdConfigSettingContractComputeV0},
	}, xdr.LedgerEntryData{Type: xdr.LedgerEntryTypeConfigSetting, ConfigSetting: &xdr.ConfigSettingEntry{
		ConfigSettingId: xdr.ConfigSettingIdConfigSettingContractComputeV0,
		ContractCompute: &xdr.ConfigSettingContractComputeV0{TxMaxInstructions: xdr.Int64(limit)},
	}})
}

func TestSweepTablesEachSample(t *testing.T) {
	fake := stellartest.NewFake(t)
	storeTxInstructionLimit(t, fake, 200_000)
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Sweep(context.Background(), probe.SweepRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Samples: []probe.SweepSample{
			{Args: nil},
			{Label: "big", Args: nil},
			{Label: "broken", Args: []string{"u32:not-a-number"}},
		},
		Concurrency: 2,
	})
	require.NoError(t, err)
	require.Len(t, res.Rows, 3)
	assert.Equal(t, "1", res.Rows[0].Label)
	assert.True(t, res.Rows[0].Success)
	assert.Equal(t, uint32(127_569), res.Rows[0].Cost.Instructions)
	require.NotNil(t, res.Rows[0].WithinLimits)
	assert.True(t, *res.Rows[0].WithinLimits)
	assert.False(t, res.Rows[2].Success)
	assert.NotEmpty(t, res.Rows[2].Error)
	assert.Equal(t, "big", res.LargestWithinLimits)
	assert.Equal(t, int64(200_000), res.Limits["instructions"])
}

func TestSweepFlagsSamplesOverTheLimit(t *testing.T) {
	fake := stellartest.NewFake(t)
	storeTxInstructionLimit(t, fake, 100_000)
	res, err := newProber(t, fake, health.Thresholds{}).Sweep(context.Background(), probe.SweepRequest{
		ContractID: stellartest.SACContract, Function: "decimals", Samples: []probe.SweepSample{{}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"instructions"}, res.Rows[0].OverLimits)
	assert.Empty(t, res.LargestWithinLimits)
}

func TestSweepWithoutLimits(t *testing.T) {
	res, err := newProber(t, stellartest.NewFake(t), health.Thresholds{}).Sweep(context.Background(), probe.SweepRequest{
		ContractID: stellartest.SACContract, Function: "decimals", Samples: []probe.SweepSample{{}},
	})
	require.NoError(t, err)
	assert.Nil(t, res.Rows[0].WithinLimits)
	assert.Contains(t, res.Note, "no resource limits")

	_, err = newProber(t, stellartest.NewFake(t), health.Thresholds{}).Sweep(context.Background(), probe.SweepRequest{})
	require.Error(t, err)
}

func TestExpandTemplateAndParseSizes(t *testing.T) {
	args, err := probe.ExpandTemplate([]string{`vec:[{repeat:"u32:7"}]`, "u32:{n}"}, 3)
	require.NoError(t, err)
	assert.Equal(t, []string{`vec:["u32:7","u32:7","u32:7"]`, "u32:3"}, args)

	_, err = probe.ExpandTemplate([]string{"vec:[{repeat:x"}, 2)
	require.Error(t, err)

	for spec, want := range map[string][]int{
		"1,2,4":   {1, 2, 4},
		"1..4":    {1, 2, 3, 4},
		"0..10:5": {0, 5, 10},
	} {
		got, err := probe.ParseSizes(spec)
		require.NoError(t, err, spec)
		assert.Equal(t, want, got, spec)
	}
	for _, bad := range []string{"", "a", "5..1", "1..3:0", "1..100000"} {
		_, err := probe.ParseSizes(bad)
		assert.Error(t, err, bad)
	}
}
