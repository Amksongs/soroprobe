package abi_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestChainSourceDecodesRealWasmFixtureAndCachesByHash(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	source := abi.NewChainSource(fake)
	iface, err := source.Interface(context.Background(), "testnet", stellartest.WasmContract)
	require.NoError(t, err)
	fn, err := iface.Lookup("decimals")
	require.NoError(t, err)
	assert.Equal(t, "fn decimals() -> u32", fn.Signature())
	assert.Len(t, fake.EntryRequests, 2, "the contract instance and code are read once")

	_, err = source.Interface(context.Background(), "testnet", stellartest.WasmContract)
	require.NoError(t, err)
	assert.Len(t, fake.EntryRequests, 3, "the decoded spec is cached by Wasm hash")
}

func TestChainSourceReportsNoWasmForSAC(t *testing.T) {
	t.Parallel()

	source := abi.NewChainSource(stellartest.NewFake(t))
	_, err := source.Interface(context.Background(), "testnet", stellartest.SACContract)
	assert.ErrorIs(t, err, abi.ErrNoWasm)
}
