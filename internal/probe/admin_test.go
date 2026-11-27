package probe_test

import (
	"context"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func accountID(t *testing.T, seed string) xdr.AccountId {
	t.Helper()
	kp := keypair.MustRandom()
	_ = seed
	id, err := xdr.AddressToAccountId(kp.Address())
	require.NoError(t, err)
	return id
}

func accountVal(id xdr.AccountId) xdr.ScVal {
	addr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &id}
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}
}

// adminFake answers the "admin" function with the given account.
func adminFake(t *testing.T, admin xdr.AccountId) *stellartest.Fake {
	t.Helper()
	fake := stellartest.NewFake(t)
	fake.Simulations["admin"] = simResult(t, fake.Simulations["decimals"], accountVal(admin))
	return fake
}

func runCheck(t *testing.T, p *probe.Prober, req probe.CheckRequest) map[string]probe.Check {
	t.Helper()
	req.ContractID = stellartest.WasmContract
	res, err := p.Check(context.Background(), req)
	require.NoError(t, err)
	return checksByName(res)
}

func networkProber(t *testing.T, fake *stellartest.Fake, network string) *probe.Prober {
	t.Helper()
	p, err := probe.New(probe.Options{Client: fake, SourceAccount: stellartest.SourceAccount, Thresholds: health.DefaultThresholds, Network: network})
	require.NoError(t, err)
	return p
}

func TestAdminSingleKeyWarnsAndMainnetIsHighlighted(t *testing.T) {
	admin := accountID(t, "a")
	fake := adminFake(t, admin)
	fake.StoreAccount(t, xdr.AccountEntry{AccountId: admin, Balance: 1e9, Thresholds: xdr.Thresholds{1, 0, 0, 0}})

	c := runCheck(t, networkProber(t, fake, "testnet"), probe.CheckRequest{AdminFn: "admin"})["admin"]
	assert.Equal(t, probe.OutcomeWarn, c.Outcome)
	assert.Contains(t, c.Detail, "single key")
	assert.NotContains(t, c.Detail, "MAINNET")

	c = runCheck(t, networkProber(t, fake, "public"), probe.CheckRequest{AdminFn: "admin"})["admin"]
	assert.Equal(t, probe.OutcomeWarn, c.Outcome)
	assert.Contains(t, c.Detail, "MAINNET")
}

func TestAdminMultisigPasses(t *testing.T) {
	admin := accountID(t, "a")
	other := keypair.MustRandom().Address()
	sk := xdr.Signer{Key: xdr.MustSigner(other), Weight: 1}
	fake := adminFake(t, admin)
	fake.StoreAccount(t, xdr.AccountEntry{
		AccountId: admin, Balance: 1e9, Thresholds: xdr.Thresholds{1, 2, 2, 2},
		Signers: []xdr.Signer{sk},
	})
	c := runCheck(t, networkProber(t, fake, "public"), probe.CheckRequest{AdminFn: "admin"})["admin"]
	assert.Equal(t, probe.OutcomePass, c.Outcome)
	assert.Contains(t, c.Detail, other)
	assert.Contains(t, c.Detail, "medium 2")
}

func TestAdminContractReportsWasmHash(t *testing.T) {
	fake := stellartest.NewFake(t)
	addr, err := stellar.ParseContractID(stellartest.WasmContract)
	require.NoError(t, err)
	fake.Simulations["admin"] = simResult(t, fake.Simulations["decimals"], xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr})

	c := runCheck(t, networkProber(t, fake, ""), probe.CheckRequest{AdminFn: "admin"})["admin"]
	assert.Equal(t, probe.OutcomePass, c.Outcome)
	assert.Contains(t, c.Detail, "a contract, wasm hash")
}

func TestAdminKeyReadsInstanceStorage(t *testing.T) {
	admin := accountID(t, "a")
	fake := stellartest.NewFake(t)
	fake.DeployWasm(t, stellartest.WasmContract, []byte("wasm"))
	contract, _ := stellar.ParseContractID(stellartest.WasmContract)
	sym := xdr.ScSymbol("Admin")
	storage := xdr.ScMap{{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}, Val: accountVal(admin)}}
	hash := xdr.Hash{1}
	storagePtr := &storage
	inst := xdr.ScContractInstance{
		Executable: xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableWasm, WasmHash: &hash},
		Storage:    storagePtr,
	}
	fake.StoreEntry(t, stellar.InstanceKey(contract), xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract: contract, Key: xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val:        xdr.ScVal{Type: xdr.ScValTypeScvContractInstance, Instance: &inst},
		},
	})
	fake.StoreAccount(t, xdr.AccountEntry{AccountId: admin, Balance: 1e9, Thresholds: xdr.Thresholds{1, 0, 0, 0}})

	c := runCheck(t, networkProber(t, fake, ""), probe.CheckRequest{AdminKey: "sym:Admin"})["admin"]
	assert.Contains(t, c.Detail, "instance storage sym:Admin")
	assert.Equal(t, probe.OutcomeWarn, c.Outcome)
}

func TestAdminUnresolvableFails(t *testing.T) {
	fake := stellartest.NewFake(t)
	c := runCheck(t, networkProber(t, fake, ""), probe.CheckRequest{AdminKey: "sym:Nope"})["admin"]
	assert.Equal(t, probe.OutcomeFail, c.Outcome)
	assert.Contains(t, c.Detail, "could not resolve the admin")
}

func TestAdminEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T, fake *stellartest.Fake, admin xdr.AccountId)
		wantOutcome probe.CheckOutcome
		wantDetail  string
	}{
		{
			name: "account does not exist",
			setup: func(_ *testing.T, _ *stellartest.Fake, _ xdr.AccountId) {
			},
			wantOutcome: probe.OutcomeWarn,
			wantDetail:  "does not exist on this network",
		},
		{
			name: "zero master weight with no signers",
			setup: func(t *testing.T, fake *stellartest.Fake, admin xdr.AccountId) {
				fake.StoreAccount(t, xdr.AccountEntry{
					AccountId: admin,
					Balance:   1e9,
					Thresholds: xdr.Thresholds{
						0, 0, 0, 0,
					},
				})
			},
			wantOutcome: probe.OutcomePass,
			wantDetail:  "master weight 0",
		},
		{
			name: "admin function returns a non-address",
			setup: func(t *testing.T, fake *stellartest.Fake, _ xdr.AccountId) {
				fake.Simulations["admin"] = simResult(t, fake.Simulations["decimals"], strVal("not-an-address"))
			},
			wantOutcome: probe.OutcomeFail,
			wantDetail:  "could not resolve the admin",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admin := accountID(t, "edge")
			fake := adminFake(t, admin)
			tt.setup(t, fake, admin)

			c := runCheck(t, networkProber(t, fake, "testnet"), probe.CheckRequest{AdminFn: "admin"})["admin"]
			assert.Equal(t, tt.wantOutcome, c.Outcome)
			assert.Contains(t, c.Detail, tt.wantDetail)
		})
	}
}

func TestAdminAbsentWithoutFlags(t *testing.T) {
	c := runCheck(t, networkProber(t, stellartest.NewFake(t), ""), probe.CheckRequest{})
	assert.NotContains(t, c, "admin")
	assert.NotContains(t, c, "upgradeability")
	assert.NotContains(t, c, "rent_payer")
}

func TestAdminFnAndKeyTogetherRejected(t *testing.T) {
	p := networkProber(t, stellartest.NewFake(t), "")
	_, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.WasmContract, AdminFn: "a", AdminKey: "sym:A"})
	require.Error(t, err)
}
