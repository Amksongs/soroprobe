package stellar_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

const (
	testContract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	testAccount  = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
)

func TestParseContractID(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)
	assert.Equal(t, xdr.ScAddressTypeScAddressTypeContract, addr.Type)
	require.NotNil(t, addr.ContractId)

	// The address must survive a round trip back to strkey.
	back, err := addr.String()
	require.NoError(t, err)
	assert.Equal(t, testContract, back)
}

func TestParseContractIDRejectsBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"not strkey", "hello"},
		{"account address", testAccount},
		{"bad checksum", "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"truncated", "CDLZFC3SYJYDZT7K"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := stellar.ParseContractID(tt.in)
			require.Error(t, err)
		})
	}
}

func TestInstanceKeyShape(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	key := stellar.InstanceKey(addr)
	assert.Equal(t, xdr.LedgerEntryTypeContractData, key.Type)
	require.NotNil(t, key.ContractData)

	// The instance is addressed by the special instance ScVal at persistent
	// durability; getting either wrong silently reads the wrong entry.
	assert.Equal(t, xdr.ScValTypeScvLedgerKeyContractInstance, key.ContractData.Key.Type)
	assert.Equal(t, xdr.ContractDataDurabilityPersistent, key.ContractData.Durability)
}

func TestInstanceKeyMatchesRecordedFixture(t *testing.T) {
	t.Parallel()

	// The strongest possible check that the key is right: the key SoroProbe
	// builds must equal the one the live network echoed back in the fixture.
	resp := stellartest.LoadLedgerEntries(t, "ledger_entries_sac_instance.json")
	require.Len(t, resp.Entries, 1)

	built := stellartest.InstanceKeyOf(t, stellartest.SACContract)
	assert.Equal(t, resp.Entries[0].KeyXDR, built)
}

func TestDataKeyDurabilityChangesTheKey(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	sym := xdr.ScSymbol("Balance")
	val := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}

	persistent, err := stellar.EncodeKey(stellar.DataKey(addr, val, xdr.ContractDataDurabilityPersistent))
	require.NoError(t, err)
	temporary, err := stellar.EncodeKey(stellar.DataKey(addr, val, xdr.ContractDataDurabilityTemporary))
	require.NoError(t, err)

	assert.NotEqual(t, persistent, temporary)
}

// benchmarkKey keeps the key built by BenchmarkDataKey observable, so the
// compiler cannot optimize the call away.
var benchmarkKey xdr.LedgerKey

func BenchmarkDataKey(b *testing.B) {
	addr, err := stellar.ParseContractID(testContract)
	if err != nil {
		b.Fatalf("parse contract id: %v", err)
	}

	sym := xdr.ScSymbol("Balance")
	val := xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}

	b.ReportAllocs()
	for b.Loop() {
		benchmarkKey = stellar.DataKey(addr, val, xdr.ContractDataDurabilityPersistent)
	}
}

func TestCodeKey(t *testing.T) {
	t.Parallel()

	var hash xdr.Hash
	copy(hash[:], []byte("0123456789abcdef0123456789abcdef"))

	key := stellar.CodeKey(hash)
	assert.Equal(t, xdr.LedgerEntryTypeContractCode, key.Type)
	require.NotNil(t, key.ContractCode)
	assert.Equal(t, hash, key.ContractCode.Hash)

	encoded, err := stellar.EncodeKey(key)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)
}

func TestEncodeKeyEdgeCases(t *testing.T) {
	t.Parallel()

	var maxHash xdr.Hash
	for i := range maxHash {
		maxHash[i] = 0xff
	}

	tests := []struct {
		name    string
		key     xdr.LedgerKey
		wantErr bool
	}{
		{name: "empty ledger key", key: xdr.LedgerKey{}, wantErr: true},
		{name: "unknown ledger entry type", key: xdr.LedgerKey{Type: xdr.LedgerEntryType(-1)}, wantErr: true},
		{name: "maximum contract code hash", key: stellar.CodeKey(maxHash)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := stellar.EncodeKey(tt.key)
			if tt.wantErr {
				require.Error(t, err)
				assert.Empty(t, encoded)
				return
			}

			require.NoError(t, err)
			assert.NotEmpty(t, encoded)

			var decoded xdr.LedgerKey
			require.NoError(t, xdr.SafeUnmarshalBase64(encoded, &decoded))
			assert.Equal(t, tt.key, decoded)
		})
	}
}

func TestDecodeEntryDataFromFixture(t *testing.T) {
	t.Parallel()

	resp := stellartest.LoadLedgerEntries(t, "ledger_entries_wasm_instance.json")
	require.Len(t, resp.Entries, 1)

	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	require.NoError(t, err)
	assert.Equal(t, xdr.LedgerEntryTypeContractData, data.Type)
	require.NotNil(t, data.ContractData)

	instance, ok := data.ContractData.Val.GetInstance()
	require.True(t, ok)
	assert.Equal(t, xdr.ContractExecutableTypeContractExecutableWasm, instance.Executable.Type)
	require.NotNil(t, instance.Executable.WasmHash)
}

func TestDecodeEntryDataRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty input", input: ""},
		{name: "malformed base64", input: "not base64 xdr!!"},
		{name: "invalid entry type", input: "/////w=="},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data, err := stellar.DecodeEntryData(tt.input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "decode ledger entry data")
			assert.Equal(t, xdr.LedgerEntryData{}, data)
		})
	}
}

func TestBuildInvocationTx(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	sym := xdr.ScSymbol("world")
	envelope, err := stellar.BuildInvocationTx(stellar.InvocationParams{
		SourceAccount: testAccount,
		Contract:      addr,
		Function:      "hello",
		Args:          []xdr.ScVal{{Type: xdr.ScValTypeScvSymbol, Sym: &sym}},
	})
	require.NoError(t, err)

	fn, err := stellartest.SubjectOf(envelope)
	require.NoError(t, err)
	assert.Equal(t, "hello", fn)

	args, err := stellartest.ArgsOf(envelope)
	require.NoError(t, err)
	require.Len(t, args, 1)
	assert.Equal(t, "world", string(*args[0].Sym))
}

func TestBuildExtendFootprintTx(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	key := stellar.InstanceKey(addr)
	envelope, err := stellar.BuildExtendFootprintTx(stellar.ExtendParams{
		SourceAccount: testAccount,
		Key:           key,
		ExtendTo:      4_014_125 + 1_000_000,
	})
	require.NoError(t, err)

	subject, err := stellartest.SubjectOf(envelope)
	require.NoError(t, err)
	assert.Equal(t, stellartest.ExtendFootprintTTL, subject)

	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(envelope, &env))

	// SoroProbe never signs anything, here either.
	assert.Empty(t, env.Signatures())

	ops := env.Operations()
	require.Len(t, ops, 1)
	extend, ok := ops[0].Body.GetExtendFootprintTtlOp()
	require.True(t, ok)
	assert.EqualValues(t, 4_014_125+1_000_000, extend.ExtendTo)

	// The footprint must hold the entry being estimated and nothing else,
	// so the fee is the fee for that entry.
	require.NotNil(t, env.V1)
	require.NotNil(t, env.V1.Tx.Ext.SorobanData)
	footprint := env.V1.Tx.Ext.SorobanData.Resources.Footprint
	require.Len(t, footprint.ReadOnly, 1)
	assert.Equal(t, key, footprint.ReadOnly[0])
	assert.Empty(t, footprint.ReadWrite)
}

func TestBuildExtendFootprintTxNeedsASourceAccount(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	_, err = stellar.BuildExtendFootprintTx(stellar.ExtendParams{
		Key: stellar.InstanceKey(addr),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source account")
}

func TestBuildInvocationTxIsUnsigned(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	envelope, err := stellar.BuildInvocationTx(stellar.InvocationParams{
		SourceAccount: testAccount,
		Contract:      addr,
		Function:      "hello",
	})
	require.NoError(t, err)

	// SoroProbe must never produce a signed transaction: it has no secret key
	// and no reason to hold one.
	var env xdr.TransactionEnvelope
	require.NoError(t, xdr.SafeUnmarshalBase64(envelope, &env))
	assert.Empty(t, env.Signatures())
}

func TestBuildInvocationTxWithNoArgs(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	envelope, err := stellar.BuildInvocationTx(stellar.InvocationParams{
		SourceAccount: testAccount,
		Contract:      addr,
		Function:      "decimals",
	})
	require.NoError(t, err)

	args, err := stellartest.ArgsOf(envelope)
	require.NoError(t, err)
	assert.Empty(t, args)
}

func TestBuildInvocationTxValidation(t *testing.T) {
	t.Parallel()

	addr, err := stellar.ParseContractID(testContract)
	require.NoError(t, err)

	t.Run("requires a source account", func(t *testing.T) {
		t.Parallel()
		_, err := stellar.BuildInvocationTx(stellar.InvocationParams{Contract: addr, Function: "hello"})
		require.Error(t, err)
	})

	t.Run("requires a function name", func(t *testing.T) {
		t.Parallel()
		_, err := stellar.BuildInvocationTx(stellar.InvocationParams{SourceAccount: testAccount, Contract: addr})
		require.Error(t, err)
	})

	t.Run("rejects a malformed source account", func(t *testing.T) {
		t.Parallel()
		_, err := stellar.BuildInvocationTx(stellar.InvocationParams{
			SourceAccount: "not-an-account", Contract: addr, Function: "hello",
		})
		require.Error(t, err)
	})
}

func TestNewClientValidation(t *testing.T) {
	t.Parallel()

	_, err := stellar.New(stellar.Options{})
	require.Error(t, err, "a url is required")

	client, err := stellar.New(stellar.Options{URL: "https://rpc.example.com"})
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.NoError(t, client.Close())
}
