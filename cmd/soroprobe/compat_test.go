package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi/abitest"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func writeWasm(t *testing.T, name string, entries ...xdr.ScSpecEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, abitest.Wasm(t, entries...), 0o600))
	return path
}

func TestCompatBetweenTwoFilesNeedsNoNetwork(t *testing.T) {
	oldPath := writeWasm(t, "old.wasm", abitest.Fn("get", abitest.Returns(abitest.U32)))
	newPath := writeWasm(t, "new.wasm", abitest.Fn("get", abitest.Returns(abitest.U32)), abitest.Fn("set", nil))

	out, err := runWithFake(t, nil, "compat", oldPath, newPath)
	require.NoError(t, err)
	assert.Contains(t, out, "function added")
	assert.Contains(t, out, "PASS")
}

func TestCompatBreakingChangeExitsOne(t *testing.T) {
	oldPath := writeWasm(t, "old.wasm", abitest.Fn("get", abitest.Returns(abitest.U32)))
	newPath := writeWasm(t, "new.wasm", abitest.Fn("get", abitest.Returns(abitest.U64)))

	out, err := runWithFake(t, nil, "compat", oldPath, newPath)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "BREAKING")
	assert.Contains(t, out, "return type changed from u32 to u64")
	assert.Contains(t, out, "FAIL")
}

func TestCompatJSONOutput(t *testing.T) {
	oldPath := writeWasm(t, "old.wasm", abitest.Fn("get", nil))
	newPath := writeWasm(t, "new.wasm")

	out, err := runWithFake(t, nil, "compat", "--json", oldPath, newPath)
	assertExitCodeOne(t, err)

	var report compatReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.True(t, report.Breaking)
	require.Len(t, report.Changes, 1)
	assert.Equal(t, "fn get", report.Changes[0].Subject)
	assert.True(t, report.Changes[0].Breaking)
}

func TestCompatNoDifferencesEmitsAnEmptyChangeList(t *testing.T) {
	path := writeWasm(t, "same.wasm", abitest.Fn("get", nil))

	out, err := runWithFake(t, nil, "compat", "--json", path, path)
	require.NoError(t, err)
	assert.Contains(t, out, `"changes": []`)
	assert.Contains(t, out, `"breaking": false`)
}

func TestCompatReadsTheOldInterfaceFromTheChain(t *testing.T) {
	// The recorded contract exports decimals() -> u32 among other functions,
	// so a file that keeps only decimals drops the rest.
	same := writeWasm(t, "same.wasm", abitest.Fn("decimals", abitest.Returns(abitest.U32)))
	changed := writeWasm(t, "changed.wasm", abitest.Fn("decimals", abitest.Returns(abitest.U64)))

	out, err := runWithFake(t, stellartest.NewFake(t), "compat", stellartest.WasmContract, same)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "function removed")
	assert.NotContains(t, out, "fn decimals")

	out, err = runWithFake(t, stellartest.NewFake(t), "compat", stellartest.WasmContract, changed)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "return type changed from u32 to u64")
}

func TestCompatRejectsBadArguments(t *testing.T) {
	good := writeWasm(t, "good.wasm", abitest.Fn("get", nil))

	err := runWith(t, "compat", "not-a-contract-or-file", good)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither a Wasm file")

	err = runWith(t, "compat", good, stellartest.WasmContract)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new interface")

	notWasm := filepath.Join(t.TempDir(), "x.wasm")
	require.NoError(t, os.WriteFile(notWasm, []byte("nope"), 0o600))
	err = runWith(t, "compat", good, notWasm)
	require.Error(t, err)

	require.Error(t, runWith(t, "compat", good))
}
