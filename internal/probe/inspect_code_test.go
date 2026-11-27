package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// codeEntryKeys returns the base64 keys of the code entries the fake holds.
// Looking them up by type rather than by call order keeps these tests honest
// about which request they are changing.
func codeEntryKeys(t *testing.T, fake *stellartest.Fake) []string {
	t.Helper()

	var keys []string
	for encoded := range fake.Entries {
		var key xdr.LedgerKey
		require.NoError(t, xdr.SafeUnmarshalBase64(encoded, &key))
		if key.Type == xdr.LedgerEntryTypeContractCode {
			keys = append(keys, encoded)
		}
	}
	require.NotEmpty(t, keys, "the fake should hold a wasm code entry")
	return keys
}

// failingCodeClient is the fake with one addition: a getLedgerEntries request
// for a code entry fails, as an RPC endpoint would. The instance lookup still
// succeeds, which is what makes this the second round rather than the first.
type failingCodeClient struct {
	*stellartest.Fake
	err error
}

func (c *failingCodeClient) GetLedgerEntries(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	isCodeLookup := false
	for _, encoded := range req.Keys {
		var key xdr.LedgerKey
		if err := xdr.SafeUnmarshalBase64(encoded, &key); err != nil {
			return protocol.GetLedgerEntriesResponse{}, err
		}
		if key.Type == xdr.LedgerEntryTypeContractCode {
			isCodeLookup = true
			break
		}
	}
	if !isCodeLookup {
		return c.Fake.GetLedgerEntries(ctx, req)
	}

	// Record the request before failing, so the test can prove the code lookup
	// was attempted at all.
	if _, err := c.Fake.GetLedgerEntries(ctx, req); err != nil {
		return protocol.GetLedgerEntriesResponse{}, err
	}
	return protocol.GetLedgerEntriesResponse{}, c.err
}

func TestInspectPropagatesCodeLookupError(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	sentinel := errors.New("rpc unreachable while reading the code entry")
	client := &failingCodeClient{Fake: fake, err: sentinel}

	p, err := probe.New(probe.Options{
		Client:        client,
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    health.DefaultThresholds,
	})
	require.NoError(t, err)

	_, err = p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.WasmContract,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)

	// The instance is read first and the code entry second, so the second
	// round must have happened before the error could be reported.
	require.Len(t, fake.EntryRequests, 2)
	var second xdr.LedgerKey
	require.NoError(t, xdr.SafeUnmarshalBase64(fake.EntryRequests[1][0], &second))
	assert.Equal(t, xdr.LedgerEntryTypeContractCode, second.Type)
}

func TestInspectMissingCodeEntry(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	for _, key := range codeEntryKeys(t, fake) {
		delete(fake.Entries, key)
	}
	// A healthy instance isolates the missing code entry: the only thing that
	// can drag the overall status down is the code lookup.
	fake.SetTTL(t, stellartest.InstanceKeyOf(t, stellartest.WasmContract), fake.LatestLedger+1_000_000)

	p := newProber(t, fake, health.DefaultThresholds)
	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.WasmContract,
	})
	require.NoError(t, err)

	assert.True(t, result.Deployed)
	assert.Equal(t, "wasm", result.Executable)

	require.NotNil(t, result.Code, "a wasm instance names a code entry, even when it is gone")
	assert.False(t, result.Code.Present)
	assert.Equal(t, health.StatusMissing, result.Code.Status)
	assert.Contains(t, result.Code.Summary, "not found")
	assert.Equal(t, health.StatusMissing, result.Status)

	// The code entry is only discoverable from the instance, so both rounds
	// had to run before the absence could be reported.
	assert.Len(t, fake.EntryRequests, 2)
}

func TestInspectCodeEntryWithoutTTLIsUnknown(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	for _, key := range codeEntryKeys(t, fake) {
		entry := fake.Entries[key]
		entry.LiveUntilLedgerSeq = nil
		fake.Entries[key] = entry
	}
	// Keep the instance healthy so the overall status can only come from the
	// code entry's unknown TTL.
	fake.SetTTL(t, stellartest.InstanceKeyOf(t, stellartest.WasmContract), fake.LatestLedger+1_000_000)

	p := newProber(t, fake, health.DefaultThresholds)
	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.WasmContract,
	})
	require.NoError(t, err)

	require.NotNil(t, result.Code)
	assert.True(t, result.Code.Present)
	assert.Nil(t, result.Code.LiveUntilLedger)
	assert.Equal(t, health.StatusUnknown, result.Code.Status)
	// The summary also states the thresholds it was judged against.
	assert.Contains(t, result.Code.Summary, "present, but no TTL was reported")
	assert.Equal(t, health.StatusUnknown, result.Status)
	assert.Len(t, fake.EntryRequests, 2)
}
