package probe_test

import (
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// onlyInstanceExpiring makes every stored entry healthy except the Wasm
// contract's instance, so exactly one extension is priced.
func onlyInstanceExpiring(t *testing.T, fake *stellartest.Fake) {
	t.Helper()
	far := fake.LatestLedger + 10_000_000
	for k, e := range fake.Entries {
		if e.LiveUntilLedgerSeq != nil {
			e.LiveUntilLedgerSeq = &far
			fake.Entries[k] = e
		}
	}
	fake.SetTTL(t, stellartest.InstanceKeyOf(t, stellartest.WasmContract), fake.LatestLedger+100)
}

func TestRentPayerAffordability(t *testing.T) {
	payer := accountID(t, "p")
	addr := payer.Address()

	// The fee the fake reports for one extension, plus the base fee, is what
	// the payer has to cover beyond its two-reserve minimum balance.
	sim := stellartest.LoadSimulate(t, "simulate_extend.json")
	var data xdr.SorobanTransactionData
	require.NoError(t, xdr.SafeUnmarshalBase64(sim.TransactionDataXDR, &data))
	cost := probe.EstimateTotalFee(int64(data.ResourceFee), probe.DefaultBaseFee)
	const minimum = 2 * 5_000_000

	cases := []struct {
		name    string
		balance int64
		want    probe.CheckOutcome
		detail  string
	}{
		{"short", minimum + cost - 1, probe.OutcomeFail, "cannot afford"},
		{"thin", minimum + cost + cost/2, probe.OutcomeWarn, "less than twice"},
		{"plenty", minimum + 3*cost, probe.OutcomePass, "spendable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := stellartest.NewFake(t)
			onlyInstanceExpiring(t, fake)
			fake.StoreAccount(t, xdr.AccountEntry{AccountId: payer, Balance: xdr.Int64(tc.balance)})
			c := runCheck(t, networkProber(t, fake, ""), probe.CheckRequest{RentPayer: addr})["rent_payer"]
			assert.Equal(t, tc.want, c.Outcome, c.Detail)
			assert.Contains(t, c.Detail, tc.detail)
			assert.Contains(t, c.Detail, "instance")
		})
	}
}

func TestRentPayerNothingToExtend(t *testing.T) {
	payer := accountID(t, "p")
	addr := payer.Address()
	fake := stellartest.NewFake(t)
	fake.SetTTL(t, stellartest.InstanceKeyOf(t, stellartest.WasmContract), fake.LatestLedger+10_000_000)
	for k, e := range fake.Entries {
		if e.LiveUntilLedgerSeq != nil {
			l := fake.LatestLedger + 10_000_000
			e.LiveUntilLedgerSeq = &l
			fake.Entries[k] = e
		}
	}
	fake.StoreAccount(t, xdr.AccountEntry{AccountId: payer, Balance: 1e9})
	c := runCheck(t, networkProber(t, fake, ""), probe.CheckRequest{RentPayer: addr})["rent_payer"]
	assert.Equal(t, probe.OutcomePass, c.Outcome, c.Detail)
	assert.Contains(t, c.Detail, "no entry needs extending")
}

func TestRentPayerMissingAccountFails(t *testing.T) {
	addr := accountID(t, "p").Address()
	c := runCheck(t, networkProber(t, stellartest.NewFake(t), ""), probe.CheckRequest{RentPayer: addr})["rent_payer"]
	assert.Equal(t, probe.OutcomeFail, c.Outcome)
}
