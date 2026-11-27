package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestBalanceCommandReadsHoldersFromArgsAndFile(t *testing.T) {
	// The fixture's decimals simulation stands in for every call, so the
	// balance reads back as the same u32; what is under test is the plumbing.
	fake := stellartest.NewFake(t)
	fake.Simulations["balance"] = fake.Simulations["decimals"]
	list := filepath.Join(t.TempDir(), "holders.txt")
	require.NoError(t, os.WriteFile(list, []byte("# holders\n\n"+stellartest.SACContract+"\n"), 0o600))

	out, err := runWithFake(t, fake, "balance", "--json", "--file", list, stellartest.WasmContract, stellartest.WasmContract)
	require.NoError(t, err)

	var res probe.BalanceResult
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	require.Len(t, res.Balances, 2)
	assert.Equal(t, stellartest.WasmContract, res.Balances[0].Address)
	assert.Equal(t, stellartest.SACContract, res.Balances[1].Address)
}

func TestBalanceCommandBadAddressExitsOne(t *testing.T) {
	out, err := runWithFake(t, stellartest.NewFake(t), "balance", stellartest.WasmContract, "nope")
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "invalid address")
}

func TestBalanceCommandNeedsAHolder(t *testing.T) {
	_, err := runWithFake(t, stellartest.NewFake(t), "balance", stellartest.WasmContract)
	require.Error(t, err)
}
