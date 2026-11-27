package main

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestCheckAdminKeyReportsPosture(t *testing.T) {
	// The recorded Wasm contract keeps its admin in instance storage.
	out, err := runWithFake(t, stellartest.NewFake(t), "check", "--admin-key", "sym:admin", stellartest.WasmContract)
	require.NoError(t, err, out)
	assert.Contains(t, out, "admin")
	assert.Contains(t, out, "instance storage sym:admin")
}

func TestCheckUpgradeabilityIsLabelledBestEffort(t *testing.T) {
	out, err := runWithFake(t, stellartest.NewFake(t), "check", "--upgradeability", stellartest.WasmContract)
	require.NoError(t, err, out)
	assert.Contains(t, out, "upgradeability:interface")
	assert.Contains(t, out, "best effort")
}

func TestCheckRentPayerShowsTheCalculation(t *testing.T) {
	fake := stellartest.NewFake(t)
	kp := keypair.MustRandom()
	id := xdr.MustAddress(kp.Address())
	fake.StoreAccount(t, xdr.AccountEntry{AccountId: id, Balance: 1e12})

	out, err := runWithFake(t, fake, "check", "--rent-payer", kp.Address(), stellartest.WasmContract)
	require.NoError(t, err, out)
	assert.Contains(t, out, "rent_payer")
	assert.Contains(t, out, "spendable")
}

func TestCheckPostureFlagsConflicts(t *testing.T) {
	fake := stellartest.NewFake(t)
	_, err := runWithFake(t, fake, "check", "--admin-fn", "admin", "--admin-key", "sym:Admin", stellartest.WasmContract)
	require.Error(t, err)
	_, err = runWithFake(t, fake, "check", "--upgradeability", "--file", "x.json")
	require.Error(t, err)
}
