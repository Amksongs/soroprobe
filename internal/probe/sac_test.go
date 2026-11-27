package probe_test

import (
	"context"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func scSym(s string) xdr.ScVal {
	sym := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

func scVec(items ...xdr.ScVal) xdr.ScVal {
	v := xdr.ScVec(items)
	vp := &v
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vp}
}

func scMap(entries ...xdr.ScMapEntry) xdr.ScVal {
	m := xdr.ScMap(entries)
	mp := &m
	return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mp}
}

// sacEntry hand-builds the instance ledger entry of a Stellar Asset Contract
// holding the given storage.
func sacEntry(t *testing.T, contract string, storage xdr.ScMap) (string, protocol.LedgerEntryResult) {
	t.Helper()
	addr, err := stellar.ParseContractID(contract)
	require.NoError(t, err)
	key, err := stellar.EncodeKey(stellar.InstanceKey(addr))
	require.NoError(t, err)

	instance := xdr.ScContractInstance{
		Executable: xdr.ContractExecutable{Type: xdr.ContractExecutableTypeContractExecutableStellarAsset},
		Storage:    &storage,
	}
	data := xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract:   addr,
			Key:        xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Durability: xdr.ContractDataDurabilityPersistent,
			Val:        xdr.ScVal{Type: xdr.ScValTypeScvContractInstance, Instance: &instance},
		},
	}
	b64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	live := uint32(10_000_000)
	return key, protocol.LedgerEntryResult{KeyXDR: key, DataXDR: b64, LiveUntilLedgerSeq: &live}
}

func accountEntry(t *testing.T, id xdr.AccountId, flags uint32) (string, protocol.LedgerEntryResult) {
	t.Helper()
	key, err := stellar.EncodeKey(xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: id},
	})
	require.NoError(t, err)
	data := xdr.LedgerEntryData{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.AccountEntry{AccountId: id, Flags: xdr.Uint32(flags)},
	}
	b64, err := xdr.MarshalBase64(data)
	require.NoError(t, err)
	return key, protocol.LedgerEntryResult{KeyXDR: key, DataXDR: b64}
}

func issuedAssetStorage(t *testing.T, kind, code string, issuer []byte, admin xdr.ScAddress) xdr.ScMap {
	t.Helper()
	assetBody := scMap(
		xdr.ScMapEntry{Key: scSym("asset_code"), Val: xdr.ScVal{Type: xdr.ScValTypeScvString, Str: ptrTo(xdr.ScString(code))}},
		xdr.ScMapEntry{Key: scSym("issuer"), Val: xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: ptrTo(xdr.ScBytes(issuer))}},
	)
	return xdr.ScMap{
		{Key: scVec(scSym("Admin")), Val: xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &admin}},
		{Key: scVec(scSym("AssetInfo")), Val: scVec(scSym(kind), assetBody)},
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestInspectNativeAsset(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.NoError(t, err)

	require.NotNil(t, result.Asset)
	assert.Equal(t, probe.AssetNative, result.Asset.Type)
	assert.Empty(t, result.Asset.Code)
	assert.Empty(t, result.Asset.Issuer)
	assert.Nil(t, result.Asset.IssuerFound)
	assert.Empty(t, result.Asset.IssuerFlags)
	assert.Len(t, fake.EntryRequests, 1, "the native asset has no issuer account to fetch")
}

func TestInspectIssuedAssetReportsIssuerFlags(t *testing.T) {
	t.Parallel()

	issuer := keypair.MustRandom()
	var issuerID xdr.AccountId
	require.NoError(t, issuerID.SetAddress(issuer.Address()))
	raw := issuerID.MustEd25519()
	adminAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &issuerID}

	fake := stellartest.NewFake(t)
	key, entry := sacEntry(t, stellartest.SACContract, issuedAssetStorage(t, "AlphaNum4", "USDC", raw[:], adminAddr))
	fake.Entries[key] = entry
	// AUTH_REQUIRED (1), AUTH_IMMUTABLE (4) and CLAWBACK (8); immutable is not reported.
	accKey, accEntry := accountEntry(t, issuerID, 1|4|8)
	fake.Entries[accKey] = accEntry

	p := newProber(t, fake, health.DefaultThresholds)
	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.NoError(t, err)

	require.NotNil(t, result.Asset)
	assert.Equal(t, probe.AssetAlphaNum4, result.Asset.Type)
	assert.Equal(t, "USDC", result.Asset.Code)
	assert.Equal(t, issuer.Address(), result.Asset.Issuer)
	assert.Equal(t, issuer.Address(), result.Asset.Admin)
	require.NotNil(t, result.Asset.IssuerFound)
	assert.True(t, *result.Asset.IssuerFound)
	assert.Equal(t, []string{"AUTH_REQUIRED", "CLAWBACK"}, result.Asset.IssuerFlags)
}

func TestInspectIssuedAssetWithMissingIssuer(t *testing.T) {
	t.Parallel()

	issuer := keypair.MustRandom()
	var issuerID xdr.AccountId
	require.NoError(t, issuerID.SetAddress(issuer.Address()))
	raw := issuerID.MustEd25519()
	adminAddr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeAccount, AccountId: &issuerID}

	fake := stellartest.NewFake(t)
	key, entry := sacEntry(t, stellartest.SACContract, issuedAssetStorage(t, "AlphaNum12", "LONGERCODE", raw[:], adminAddr))
	fake.Entries[key] = entry

	p := newProber(t, fake, health.DefaultThresholds)
	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.NoError(t, err)

	require.NotNil(t, result.Asset)
	assert.Equal(t, probe.AssetAlphaNum12, result.Asset.Type)
	assert.Equal(t, "LONGERCODE", result.Asset.Code)
	require.NotNil(t, result.Asset.IssuerFound)
	assert.False(t, *result.Asset.IssuerFound)
	assert.Contains(t, result.Asset.Note, "issuer account was not found")
}

func TestInspectWasmContractHasNoAsset(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)
	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.WasmContract})
	require.NoError(t, err)
	assert.Nil(t, result.Asset)
}
