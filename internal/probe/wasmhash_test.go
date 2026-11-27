package probe_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func newWasmProber(t *testing.T) *probe.Prober {
	t.Helper()

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)
	return p
}

func deployedHash(t *testing.T, p *probe.Prober, contractID string) string {
	t.Helper()

	_, hash, err := p.Code(context.Background(), contractID)
	require.NoError(t, err)
	require.NotEmpty(t, hash)
	return hash
}

func TestCheckWasmHashMatchesTheRecordedHash(t *testing.T) {
	t.Parallel()

	p := newWasmProber(t)
	hash := deployedHash(t, p, stellartest.WasmContract)

	for _, recorded := range []string{hash, strings.ToUpper(hash), "0x" + hash} {
		result, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:       stellartest.WasmContract,
			ExpectedWASMHash: recorded,
		})
		require.NoError(t, err)
		assert.True(t, result.OK, "recorded hash %q should match", recorded)

		check := findCheck(result.Checks, "wasm_hash")
		require.NotNil(t, check)
		assert.Equal(t, probe.OutcomePass, check.Outcome)
		assert.Contains(t, check.Detail, hash)
	}
}

func TestCheckWasmHashReportsDrift(t *testing.T) {
	t.Parallel()

	p := newWasmProber(t)
	hash := deployedHash(t, p, stellartest.WasmContract)
	recorded := strings.Repeat("0", len(hash))

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID:       stellartest.WasmContract,
		ExpectedWASMHash: recorded,
	})
	require.NoError(t, err)
	assert.False(t, result.OK)

	check := findCheck(result.Checks, "wasm_hash")
	require.NotNil(t, check)
	assert.Equal(t, probe.OutcomeFail, check.Outcome)
	assert.Contains(t, check.Detail, "drift")
	assert.Contains(t, check.Detail, recorded)
	assert.Contains(t, check.Detail, hash)
}

func TestCheckWasmHashSkipsAContractWithoutACodeEntry(t *testing.T) {
	t.Parallel()

	p := newWasmProber(t)
	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID:       stellartest.SACContract,
		ExpectedWASMHash: strings.Repeat("ab", 32),
	})
	require.NoError(t, err)
	assert.True(t, result.OK, "a Stellar Asset Contract has no code entry to drift")

	check := findCheck(result.Checks, "wasm_hash")
	require.NotNil(t, check)
	assert.Equal(t, probe.OutcomeSkip, check.Outcome)
	assert.Contains(t, check.Detail, "no code entry")
}

func TestCheckWasmHashRejectsAMalformedRecordedHash(t *testing.T) {
	t.Parallel()

	p := newWasmProber(t)
	cases := []struct {
		recorded string
		want     string
	}{
		{"not-a-hash", "32-byte hex hash"},
		{strings.Repeat("zz", 32), "not hex"},
		{strings.Repeat("a", 63), "32-byte hex hash"},
	}
	for _, tc := range cases {
		result, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:       stellartest.WasmContract,
			ExpectedWASMHash: tc.recorded,
		})
		require.NoError(t, err)
		assert.False(t, result.OK)

		check := findCheck(result.Checks, "wasm_hash")
		require.NotNil(t, check)
		assert.Equal(t, probe.OutcomeFail, check.Outcome)
		assert.Contains(t, check.Detail, tc.want)
	}
}

func TestCheckWithoutARecordedHashHasNoWasmHashStep(t *testing.T) {
	t.Parallel()

	p := newWasmProber(t)
	result, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract})
	require.NoError(t, err)
	assert.Nil(t, findCheck(result.Checks, "wasm_hash"),
		"a plain check must not grow a step that has nothing to assert")
}

func TestCheckBatchCarriesTheRecordedHash(t *testing.T) {
	t.Parallel()

	p := newWasmProber(t)
	hash := deployedHash(t, p, stellartest.WasmContract)

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{
		Checks: []probe.NamedCheck{{
			Name: "token",
			CheckRequest: probe.CheckRequest{
				ContractID:       stellartest.WasmContract,
				ExpectedWASMHash: strings.Repeat("0", len(hash)),
			},
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	require.NotNil(t, result.Items[0].Result)
	assert.False(t, result.OK)

	check := findCheck(result.Items[0].Result.Checks, "wasm_hash")
	require.NotNil(t, check)
	assert.Equal(t, probe.OutcomeFail, check.Outcome)
	assert.Contains(t, check.Detail, "drift")
}
