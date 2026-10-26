package probe_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestInspectInstanceStorageEmptyMap(t *testing.T) {
	fake := stellartest.NewFake(t)
	setInstanceStorage(t, fake, stellartest.SACContract, xdr.ScMap{})
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.NoError(t, err)
	encoded, err := json.Marshal(result.InstanceStorage)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(encoded))
}

func TestInspectInstanceStorageNestedCollections(t *testing.T) {
	leafKey := xdr.ScSymbol("leaf")
	number := xdr.Uint32(7)
	leaf := xdr.ScMap{{
		Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &leafKey},
		Val: xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &number},
	}}
	nestedKey := xdr.ScSymbol("nested")
	itemsKey := xdr.ScSymbol("items")
	item := xdr.Uint32(9)
	items := xdr.ScVec{xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &item}}
	leafPtr := &leaf
	itemsPtr := &items
	storage := xdr.ScMap{
		{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &nestedKey}, Val: xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &leafPtr}},
		{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &itemsKey}, Val: xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &itemsPtr}},
	}
	fake := stellartest.NewFake(t)
	setInstanceStorage(t, fake, stellartest.SACContract, storage)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.NoError(t, err)
	encoded, err := json.Marshal(result.InstanceStorage)
	require.NoError(t, err)
	assert.JSONEq(t, `{"nested":{"leaf":7},"items":[9]}`, string(encoded))
}

func TestInspectInstanceStorageDecodeFailureIsReported(t *testing.T) {
	fake := stellartest.NewFake(t)
	setInstanceStorage(t, fake, stellartest.SACContract, xdr.ScMap{})
	p, err := probe.New(probe.Options{
		Client:        fake,
		Codec:         failingStorageCodec{Codec: scval.NewRegistry()},
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.NoError(t, err, "a storage decode problem must not fail the whole inspect")
	assert.Equal(t, map[string]any{"error": "decode instance storage: malformed storage"}, result.InstanceStorage)
}

type failingStorageCodec struct{ scval.Codec }

func (failingStorageCodec) Decode(xdr.ScVal) (any, error) {
	return nil, errors.New("malformed storage")
}

func setInstanceStorage(t *testing.T, fake *stellartest.Fake, contract string, storage xdr.ScMap) {
	t.Helper()
	key := stellartest.InstanceKeyOf(t, contract)
	entry, ok := fake.Entries[key]
	require.True(t, ok)
	data, err := stellar.DecodeEntryData(entry.DataXDR)
	require.NoError(t, err)
	instance, ok := data.ContractData.Val.GetInstance()
	require.True(t, ok)
	instance.Storage = &storage
	data.ContractData.Val.Instance = &instance
	entry.DataXDR, err = xdr.MarshalBase64(data)
	require.NoError(t, err)
	fake.Entries[key] = entry
}
