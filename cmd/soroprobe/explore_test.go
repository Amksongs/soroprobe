package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestSimulateCompareJSON(t *testing.T) {
	out, err := runWithFake(t, stellartest.NewFake(t), "--json", "simulate", "--compare", stellartest.WasmContract,
		stellartest.SACContract, "decimals")
	require.NoError(t, err)
	var d probe.SimulationDiff
	require.NoError(t, json.Unmarshal([]byte(out), &d))
	assert.Equal(t, stellartest.SACContract, d.A)
	assert.Equal(t, stellartest.WasmContract, d.B)
	// The fixture's footprint is the SAC's, so it is "self" for a and a
	// foreign contract for b.
	assert.NotEmpty(t, d.OnlyInA)
	assert.NotEmpty(t, d.OnlyInB)
	assert.NotEmpty(t, d.Resources)
}

func TestDiffCommandComparesSavedReports(t *testing.T) {
	fake := stellartest.NewFake(t)
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	_, err := runWithFake(t, fake, "simulate", "--report", a, stellartest.SACContract, "decimals")
	require.NoError(t, err)
	_, err = runWithFake(t, fake, "simulate", "--report", b, stellartest.SACContract, "decimals")
	require.NoError(t, err)

	out, err := runWithFake(t, fake, "diff", a, b)
	require.NoError(t, err)
	assert.Contains(t, out, "same footprint and cost")

	out, err = runWithFake(t, fake, "--json", "diff", a, b)
	require.NoError(t, err)
	var d probe.SimulationDiff
	require.NoError(t, json.Unmarshal([]byte(out), &d))
	assert.True(t, d.Same)

	require.NoError(t, os.WriteFile(b, []byte("not json"), 0o600))
	_, err = runWithFake(t, fake, "diff", a, b)
	require.ErrorContains(t, err, "not a simulate --json report")
}

func TestSimulateEnvelopeFlag(t *testing.T) {
	addr, err := stellar.ParseContractID(stellartest.SACContract)
	require.NoError(t, err)
	b64, err := stellar.BuildInvocationTx(stellar.InvocationParams{SourceAccount: stellartest.SourceAccount, Contract: addr, Function: "decimals"})
	require.NoError(t, err)

	out, err := runWithFake(t, stellartest.NewFake(t), "simulate", "--envelope", b64, "--trace")
	require.NoError(t, err)
	assert.Contains(t, out, "SUCCESS")
	assert.Contains(t, out, "source")
	assert.Contains(t, out, "call trace")

	path := filepath.Join(t.TempDir(), "tx.b64")
	require.NoError(t, os.WriteFile(path, []byte(b64+"\n"), 0o600))
	out, err = runWithFake(t, stellartest.NewFake(t), "--json", "simulate", "--envelope-file", path)
	require.NoError(t, err)
	var res probe.SimulateResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.True(t, res.FromEnvelope)
	assert.Equal(t, "decimals", res.Function)

	_, err = runWithFake(t, stellartest.NewFake(t), "simulate", "--envelope", b64, stellartest.SACContract, "decimals")
	require.ErrorContains(t, err, "envelope already names its call")
}

func TestSweepCommandWithTemplate(t *testing.T) {
	out, err := runWithFake(t, stellartest.NewFake(t), "sweep", stellartest.SACContract, "decimals",
		"--template", "u32:{n}", "--sizes", "1..3")
	require.NoError(t, err)
	for _, want := range []string{"n=1", "n=3", "instructions", "network reported no resource limits"} {
		assert.Contains(t, out, want)
	}
}

func TestSweepCommandWithArgsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "samples.json")
	require.NoError(t, os.WriteFile(path, []byte(`[["u32:1"], {"label": "ten", "args": ["u32:10"]}]`), 0o600))
	out, err := runWithFake(t, stellartest.NewFake(t), "--json", "sweep", stellartest.SACContract, "decimals", "--args-file", path)
	require.NoError(t, err)
	var res probe.SweepResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Len(t, res.Rows, 2)
	assert.Equal(t, "ten", res.Rows[1].Label)

	_, err = runWithFake(t, stellartest.NewFake(t), "sweep", stellartest.SACContract, "decimals")
	require.ErrorContains(t, err, "--args-file")
}
