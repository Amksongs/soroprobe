package abi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/abi/abitest"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// ExampleParseWasm decodes the interface a contract carries in the
// contractspecv0 section of its Wasm.
func ExampleParseWasm() {
	// A minimal module whose contractspecv0 section declares
	// fn decimals() -> u32.
	const module = "0061736d01000000002f0e636f6e7472616374737065637630000000000000000000000008646563696d616c73000000000000000100000004"
	wasm, err := hex.DecodeString(module)
	if err != nil {
		panic(err)
	}

	iface, err := abi.ParseWasm(wasm)
	if err != nil {
		panic(err)
	}
	for _, fn := range iface.Functions {
		fmt.Println(fn.Signature())
	}
	// Output: fn decimals() -> u32
}

// ExampleNewChainSource builds an interface source over a Stellar RPC client.
// The client is only consulted for a well-formed contract ID, so a malformed
// one is reported before any request is made.
func ExampleNewChainSource() {
	source := abi.NewChainSource(struct{ stellar.Client }{})
	_, err := source.Interface(context.Background(), "testnet", "not-a-contract")
	fmt.Println(strings.HasPrefix(err.Error(), "invalid contract id"))
	// Output: true
}

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

func TestChainSourceCodeReturnsTheLiveWasmAndItsHash(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	wasm := abitest.Wasm(t, abitest.Fn("get", nil))
	hash := fake.DeployWasm(t, stellartest.UndeployedContract, wasm)

	got, gotHash, err := abi.NewChainSource(fake).Code(context.Background(), stellartest.UndeployedContract)
	require.NoError(t, err)
	assert.Equal(t, wasm, got)
	assert.Equal(t, hash, gotHash)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(wasm)), gotHash)
}

func TestChainSourceCodeErrors(t *testing.T) {
	t.Parallel()

	source := abi.NewChainSource(stellartest.NewFake(t))

	_, _, err := source.Code(context.Background(), stellartest.SACContract)
	assert.ErrorIs(t, err, abi.ErrNoWasm)

	_, _, err = source.Code(context.Background(), stellartest.UndeployedContract)
	assert.ErrorIs(t, err, abi.ErrUnknownContract)

	_, _, err = source.Code(context.Background(), "not-a-contract")
	require.Error(t, err)
}

func TestParseWasmEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wasm    []byte
		wantErr string
	}{
		{
			name:    "empty input",
			wasm:    nil,
			wantErr: "invalid Wasm module",
		},
		{
			name:    "module without custom sections",
			wasm:    []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00},
			wantErr: "no such custom section",
		},
		{
			name: "section length exceeds module",
			wasm: []byte{
				0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00,
				0x00, 0x02, 'x',
			},
			wantErr: "invalid Wasm section length",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iface, err := abi.ParseWasm(tt.wasm)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, iface)
				return
			}

			require.NoError(t, err)
			assert.Empty(t, iface.Functions)
			assert.Empty(t, iface.Types)
		})
	}
}

func TestParseMeta(t *testing.T) {
	t.Parallel()

	wasm := abitest.WasmWithMeta(t, nil, []abitest.Meta{
		{Key: "rsver", Val: "1.91.0"},
		{Key: "source_repo", Val: "https://github.com/example/token"},
		{Key: "commit_hash", Val: "abc123"},
		{Key: "bldimg", Val: "example/build@sha256:00"},
		{Key: "commit_hash", Val: "def456"},
	})
	meta, err := abi.ParseMeta(wasm)
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/example/token", meta["source_repo"])
	assert.Equal(t, "example/build@sha256:00", meta["bldimg"])
	assert.Equal(t, "def456", meta["commit_hash"], "a repeated key keeps its last value")
}

func TestParseMetaWithoutASectionIsEmptyNotAnError(t *testing.T) {
	t.Parallel()

	meta, err := abi.ParseMeta(abitest.Wasm(t, abitest.Fn("get", nil)))
	require.NoError(t, err)
	assert.Empty(t, meta)
}

func TestParseMetaEdgeCases(t *testing.T) {
	t.Parallel()

	truncatedMetaWasm := append(
		[]byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00, 0x00, 19, 14},
		[]byte("contractmetav0")...,
	)
	truncatedMetaWasm = append(truncatedMetaWasm, []byte{0, 0, 0, 0}...)

	tests := []struct {
		name    string
		wasm    []byte
		want    map[string]string
		wantErr string
	}{
		{
			name:    "empty input",
			wasm:    nil,
			wantErr: "invalid Wasm module",
		},
		{
			name: "empty key and value",
			wasm: abitest.WasmWithMeta(t, nil, []abitest.Meta{{Key: "", Val: ""}}),
			want: map[string]string{"": ""},
		},
		{
			name:    "truncated metadata entry",
			wasm:    truncatedMetaWasm,
			wantErr: "decode contract metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			meta, err := abi.ParseMeta(tt.wasm)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, meta)
		})
	}
}

func ExampleParseMeta() {
	// A Wasm module carrying a contractmetav0 custom section.
	wasm := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x00, 0x6b, 0x0e,
		'c', 'o', 'n', 't', 'r', 'a', 'c', 't', 'm', 'e', 't', 'a', 'v', '0',
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x05, 'r', 's', 'v', 'e', 'r', 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x06, '1', '.', '9', '1', '.', '0', 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x0b, 's', 'o', 'u', 'r', 'c', 'e', '_', 'r', 'e', 'p', 'o', 0x00,
		0x00, 0x00, 0x00, 0x26,
		'h', 't', 't', 'p', 's', ':', '/', '/', 'g', 'i', 't', 'h', 'u', 'b', '.', 'c', 'o', 'm', '/', 's', 'o', 'r', 'o', 'w', 'o', 'r', 'k', 's', '/', 's', 'o', 'r', 'o', 'p', 'r', 'o', 'b', 'e', 0x00, 0x00,
	}

	meta, err := abi.ParseMeta(wasm)
	if err != nil {
		fmt.Printf("parse error: %v\n", err)
		return
	}

	fmt.Println("rsver:", meta["rsver"])
	fmt.Println("source_repo:", meta["source_repo"])

	// Output:
	// rsver: 1.91.0
	// source_repo: https://github.com/soroworks/soroprobe
}
