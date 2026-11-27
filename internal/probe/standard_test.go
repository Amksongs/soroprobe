package probe_test

import (
	"context"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func simResult(t *testing.T, base protocol.SimulateTransactionResponse, val xdr.ScVal) protocol.SimulateTransactionResponse {
	t.Helper()
	b64, err := xdr.MarshalBase64(val)
	require.NoError(t, err)
	resp := base
	resp.Results = []protocol.SimulateHostFunctionResult{{ReturnValueXDR: &b64}}
	return resp
}

func strVal(s string) xdr.ScVal {
	v := xdr.ScString(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &v}
}

func u32Val(n uint32) xdr.ScVal {
	v := xdr.Uint32(n)
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}
}

// tokenInterface is a conforming SEP-41 interface.
func tokenInterface(t *testing.T) *abi.Interface {
	t.Helper()
	std, err := abi.LookupStandard("sep41")
	require.NoError(t, err)
	iface := &abi.Interface{}
	for _, fn := range std.Functions {
		f := abi.Function{Name: fn.Name}
		for i, kind := range fn.Inputs {
			f.Inputs = append(f.Inputs, abi.Param{Name: string(rune('a' + i)), Type: abi.Type{Kind: kind, Display: kind}})
		}
		if fn.Output != "void" {
			f.Outputs = []abi.Type{{Kind: fn.Output, Display: fn.Output}}
		}
		iface.Functions = append(iface.Functions, f)
	}
	return iface
}

func standardProber(t *testing.T, fake *stellartest.Fake, src abi.Source) *probe.Prober {
	t.Helper()
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    health.DefaultThresholds,
		ABI:           src,
	})
	require.NoError(t, err)
	return p
}

func checksByName(r *probe.CheckResult) map[string]probe.Check {
	out := map[string]probe.Check{}
	for _, c := range r.Checks {
		out[c.Name] = c
	}
	return out
}

func tokenFake(t *testing.T, decimals uint32, name, symbol string) *stellartest.Fake {
	t.Helper()
	fake := stellartest.NewFake(t)
	base := fake.Simulations["decimals"]
	fake.Simulations["decimals"] = simResult(t, base, u32Val(decimals))
	fake.Simulations["name"] = simResult(t, base, strVal(name))
	fake.Simulations["symbol"] = simResult(t, base, strVal(symbol))
	return fake
}

func TestCheckStandardPassesAConformingToken(t *testing.T) {
	t.Parallel()

	fake := tokenFake(t, 7, "Token", "TKN")
	src := &fakeABI{ifaces: map[string]*abi.Interface{stellartest.WasmContract: tokenInterface(t)}}
	p := standardProber(t, fake, src)

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, Standard: "sep41"})
	require.NoError(t, err)

	got := checksByName(result)
	assert.Equal(t, probe.OutcomePass, got["standard:sep41"].Outcome)
	for _, fn := range []string{"decimals", "name", "symbol"} {
		assert.Equal(t, probe.OutcomePass, got["standard:sep41:"+fn+":simulate"].Outcome, fn)
	}
	assert.True(t, result.OK)
}

func TestCheckStandardFailsEachDeviationByName(t *testing.T) {
	t.Parallel()

	iface := tokenInterface(t)
	var kept []abi.Function
	for _, fn := range iface.Functions {
		switch fn.Name {
		case "burn":
			continue
		case "decimals":
			fn.Outputs = []abi.Type{{Kind: "u64", Display: "u64"}}
		}
		kept = append(kept, fn)
	}
	iface.Functions = kept

	fake := tokenFake(t, 7, "Token", "TKN")
	src := &fakeABI{ifaces: map[string]*abi.Interface{stellartest.WasmContract: iface}}
	p := standardProber(t, fake, src)

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, Standard: "sep41"})
	require.NoError(t, err)

	got := checksByName(result)
	assert.Equal(t, probe.OutcomeFail, got["standard:sep41:burn"].Outcome)
	assert.Equal(t, probe.OutcomeFail, got["standard:sep41:decimals"].Outcome)
	assert.NotContains(t, got, "standard:sep41", "a failing standard has no overall pass")
	// A function that already deviates is not simulated as well.
	assert.NotContains(t, got, "standard:sep41:decimals:simulate")
	assert.Equal(t, probe.OutcomePass, got["standard:sep41:name:simulate"].Outcome)
	assert.False(t, result.OK)
}

func TestCheckStandardRejectsBadMetadata(t *testing.T) {
	t.Parallel()

	fake := tokenFake(t, 39, "", "TKN")
	src := &fakeABI{ifaces: map[string]*abi.Interface{stellartest.WasmContract: tokenInterface(t)}}
	p := standardProber(t, fake, src)

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, Standard: "sep41"})
	require.NoError(t, err)

	got := checksByName(result)
	assert.Equal(t, probe.OutcomeFail, got["standard:sep41:decimals:simulate"].Outcome)
	assert.Contains(t, got["standard:sep41:decimals:simulate"].Detail, "38")
	assert.Equal(t, probe.OutcomeFail, got["standard:sep41:name:simulate"].Outcome)
	assert.Contains(t, got["standard:sep41:name:simulate"].Detail, "empty")
	assert.Equal(t, probe.OutcomePass, got["standard:sep41:symbol:simulate"].Outcome)
	assert.False(t, result.OK)
}

func TestCheckStandardSkipsStellarAssetContracts(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	src := &fakeABI{}
	p := standardProber(t, fake, src)

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.SACContract, Standard: "sep41"})
	require.NoError(t, err)

	got := checksByName(result)
	assert.Equal(t, probe.OutcomeSkip, got["standard:sep41"].Outcome)
	assert.Empty(t, src.networks, "no interface was read for a stellar asset contract")
	assert.True(t, result.OK)
}

func TestCheckStandardFailsWhenTheInterfaceIsUnknown(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := standardProber(t, fake, &fakeABI{ifaces: map[string]*abi.Interface{}})

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, Standard: "sep41"})
	require.NoError(t, err)

	got := checksByName(result)
	assert.Equal(t, probe.OutcomeFail, got["standard:sep41"].Outcome)
	assert.False(t, result.OK)
}

func TestCheckStandardReadsTheChainWhenNoSourceIsConfigured(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := standardProber(t, fake, nil)

	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, Standard: "sep41"})
	require.NoError(t, err)

	// The recorded contract is not a token, so deviations are expected; what
	// matters is that its interface was read from its Wasm.
	var failures int
	for _, c := range result.Checks {
		if c.Outcome == probe.OutcomeFail {
			failures++
		}
	}
	assert.Positive(t, failures)
	assert.False(t, result.OK)
}

func TestCheckRejectsAnUnknownStandard(t *testing.T) {
	t.Parallel()

	p := standardProber(t, stellartest.NewFake(t), nil)
	_, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, Standard: "erc20"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "erc20")
}

func TestCheckWithoutStandardAddsNoStandardChecks(t *testing.T) {
	t.Parallel()

	p := standardProber(t, stellartest.NewFake(t), &fakeABI{})
	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract})
	require.NoError(t, err)
	for _, c := range result.Checks {
		assert.NotContains(t, c.Name, "standard")
	}
}

func TestProberInterfaceReadsTheChain(t *testing.T) {
	t.Parallel()

	// The configured ABI source is ignored: Interface is about what is deployed.
	p := standardProber(t, stellartest.NewFake(t), &fakeABI{})

	iface, err := p.Interface(context.Background(), stellartest.WasmContract)
	require.NoError(t, err)
	fn, err := iface.Lookup("decimals")
	require.NoError(t, err)
	assert.Equal(t, "fn decimals() -> u32", fn.Signature())

	_, err = p.Interface(context.Background(), stellartest.SACContract)
	assert.ErrorIs(t, err, abi.ErrNoWasm)
}
