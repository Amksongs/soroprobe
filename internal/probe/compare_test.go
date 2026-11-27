package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// compareFixture builds two probers over separate fakes, each with the same
// Wasm deployed at the same address unless a test says otherwise.
func compareFixture(t *testing.T, testnetWasm, mainnetWasm []byte) (*probe.Prober, *probe.Prober, *stellartest.Fake, *stellartest.Fake) {
	t.Helper()

	testnetFake := stellartest.NewFake(t)
	mainnetFake := stellartest.NewFake(t)

	testnetFake.DeployWasm(t, stellartest.WasmContract, testnetWasm)
	mainnetFake.DeployWasm(t, stellartest.WasmContract, mainnetWasm)

	return newProber(t, testnetFake, health.DefaultThresholds),
		newProber(t, mainnetFake, health.DefaultThresholds),
		testnetFake, mainnetFake
}

func compareRequest() probe.CompareRequest {
	return probe.CompareRequest{
		ContractID:   stellartest.WasmContract,
		TestnetLabel: "testnet",
		MainnetLabel: "mainnet",
	}
}

func TestCompareMatchingWasm(t *testing.T) {
	t.Parallel()

	wasm := []byte("the same build")
	testnet, mainnet, _, _ := compareFixture(t, wasm, wasm)

	result, err := testnet.Compare(context.Background(), mainnet, compareRequest())
	require.NoError(t, err)

	assert.True(t, result.WasmMatch)
	assert.True(t, result.Match)
	assert.Equal(t, "testnet", result.Testnet.Network)
	assert.Equal(t, "mainnet", result.Mainnet.Network)
	require.NotNil(t, result.Testnet.Inspect)
	require.NotNil(t, result.Mainnet.Inspect)
	assert.Equal(t, result.Testnet.Inspect.WasmHash, result.Mainnet.Inspect.WasmHash)

	// Instance and code rows are always compared when the contract is Wasm.
	kinds := make(map[string]probe.CompareEntry, len(result.Entries))
	for _, entry := range result.Entries {
		kinds[entry.Kind] = entry
	}
	require.Contains(t, kinds, "instance")
	require.Contains(t, kinds, "code")
	assert.False(t, kinds["instance"].Differ)
	assert.False(t, kinds["code"].Differ)
}

func TestCompareDifferentWasm(t *testing.T) {
	t.Parallel()

	testnet, mainnet, _, _ := compareFixture(t, []byte("build one"), []byte("build two"))

	result, err := testnet.Compare(context.Background(), mainnet, compareRequest())
	require.NoError(t, err)

	assert.False(t, result.WasmMatch)
	assert.False(t, result.Match)
	assert.NotEqual(t, result.Testnet.Inspect.WasmHash, result.Mainnet.Inspect.WasmHash)
}

func TestCompareTreatsTwoStellarAssetContractsAsMatching(t *testing.T) {
	t.Parallel()

	// Host-implemented contracts have no Wasm entry to hash; two of them are
	// the same executable by definition.
	testnet := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)
	mainnet := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	result, err := testnet.Compare(context.Background(), mainnet, probe.CompareRequest{
		ContractID:   stellartest.SACContract,
		TestnetLabel: "testnet",
		MainnetLabel: "mainnet",
	})
	require.NoError(t, err)

	assert.True(t, result.WasmMatch)
	// A Stellar Asset Contract has no code entry on either side, so there is
	// no code row to compare.
	for _, entry := range result.Entries {
		assert.NotEqual(t, "code", entry.Kind)
	}
}

func TestCompareHighlightsTTLHealthDifferences(t *testing.T) {
	t.Parallel()

	wasm := []byte("same build, different rent")
	testnet, mainnet, testnetFake, _ := compareFixture(t, wasm, wasm)

	// The same code can be near expiration on one network and comfortable on
	// the other. Raw ledger counts always differ, so the comparison is about
	// health status.
	instanceKey := stellartest.InstanceKeyOf(t, stellartest.WasmContract)
	testnetFake.SetTTL(t, instanceKey, testnetFake.LatestLedger+50_000)

	result, err := testnet.Compare(context.Background(), mainnet, compareRequest())
	require.NoError(t, err)

	// The Wasm still matches, so the exit-code verdict stays a match.
	assert.True(t, result.WasmMatch)

	var instance probe.CompareEntry
	for _, entry := range result.Entries {
		if entry.Kind == "instance" {
			instance = entry
		}
	}
	assert.Equal(t, string(health.StatusWarning), instance.TestnetStatus)
	assert.Equal(t, string(health.StatusHealthy), instance.MainnetStatus)
	assert.True(t, instance.Differ)
	assert.Greater(t, instance.MainnetLedgersRemaining, instance.TestnetLedgersRemaining)
}

func TestCompareNamedDataEntries(t *testing.T) {
	t.Parallel()

	wasm := []byte("same build")
	testnet, mainnet, _, _ := compareFixture(t, wasm, wasm)

	// No fixture stores these entries, so both sides report them missing and
	// agree. The key must still appear in the report.
	req := compareRequest()
	req.DataKeys = []string{"sym:Admin"}

	result, err := testnet.Compare(context.Background(), mainnet, req)
	require.NoError(t, err)

	require.Len(t, result.Entries, 3)
	admin := result.Entries[2]
	assert.Equal(t, "data", admin.Kind)
	assert.Equal(t, "sym:Admin", admin.Key)
	assert.Equal(t, string(health.StatusMissing), admin.TestnetStatus)
	assert.Equal(t, string(health.StatusMissing), admin.MainnetStatus)
	assert.False(t, admin.Differ)
}

func TestCompareReadOnlyCalls(t *testing.T) {
	t.Parallel()

	wasm := []byte("same build")

	t.Run("matching answers", func(t *testing.T) {
		t.Parallel()

		testnet, mainnet, _, _ := compareFixture(t, wasm, wasm)
		req := compareRequest()
		req.Calls = []probe.CompareCall{{Name: "decimals", Function: "decimals"}}

		result, err := testnet.Compare(context.Background(), mainnet, req)
		require.NoError(t, err)

		require.Len(t, result.Calls, 1)
		call := result.Calls[0]
		assert.Equal(t, "decimals", call.Name)
		require.NotNil(t, call.Testnet)
		require.NotNil(t, call.Mainnet)
		assert.True(t, call.Testnet.Success)
		assert.False(t, call.Differ)
		assert.Empty(t, call.Detail)
	})

	t.Run("different answers", func(t *testing.T) {
		t.Parallel()

		testnet, mainnet, _, mainnetFake := compareFixture(t, wasm, wasm)
		mainnetFake.Simulations["decimals"] = stellartest.LoadSimulate(t, "simulate_failure.json")

		req := compareRequest()
		req.Calls = []probe.CompareCall{{Name: "decimals", Function: "decimals"}}

		result, err := testnet.Compare(context.Background(), mainnet, req)
		require.NoError(t, err)

		require.Len(t, result.Calls, 1)
		call := result.Calls[0]
		assert.True(t, call.Differ)
		assert.Contains(t, call.Detail, "one network succeeds")
		// A differing call does not change the Wasm verdict.
		assert.True(t, result.WasmMatch)
	})
}

func TestCompareUsesADifferentContractIDPerNetwork(t *testing.T) {
	t.Parallel()

	wasm := []byte("same build, deployed separately")
	testnetFake := stellartest.NewFake(t)
	mainnetFake := stellartest.NewFake(t)
	testnetFake.DeployWasm(t, stellartest.WasmContract, wasm)
	mainnetFake.DeployWasm(t, stellartest.SACContract, wasm)

	testnet := newProber(t, testnetFake, health.DefaultThresholds)
	mainnet := newProber(t, mainnetFake, health.DefaultThresholds)

	result, err := testnet.Compare(context.Background(), mainnet, probe.CompareRequest{
		ContractID:      stellartest.WasmContract,
		OtherContractID: stellartest.SACContract,
		TestnetLabel:    "testnet",
		MainnetLabel:    "mainnet",
	})
	require.NoError(t, err)

	assert.True(t, result.WasmMatch)
	assert.Equal(t, stellartest.WasmContract, result.Testnet.Inspect.ContractID)
	assert.Equal(t, stellartest.SACContract, result.Mainnet.Inspect.ContractID)
}

func TestCompareNotDeployedCannotMatch(t *testing.T) {
	t.Parallel()

	testnet := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)
	mainnet := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	result, err := testnet.Compare(context.Background(), mainnet, probe.CompareRequest{
		ContractID:   stellartest.UndeployedContract,
		TestnetLabel: "testnet",
		MainnetLabel: "mainnet",
	})
	require.NoError(t, err, "an undeployed contract is an answer, not a tool failure")

	assert.False(t, result.WasmMatch)
	assert.False(t, result.Testnet.Inspect.Deployed)
	assert.False(t, result.Mainnet.Inspect.Deployed)
}

func TestCompareRequiresBothSides(t *testing.T) {
	t.Parallel()

	testnet := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	_, err := testnet.Compare(context.Background(), nil, compareRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "second prober")

	_, err = testnet.Compare(context.Background(), testnet, probe.CompareRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contract id")
}

func TestComparePropagatesTransportErrors(t *testing.T) {
	t.Parallel()

	wasm := []byte("same build")
	testnet, mainnet, _, mainnetFake := compareFixture(t, wasm, wasm)
	mainnetFake.EntriesErr = errors.New("rpc unreachable")

	_, err := testnet.Compare(context.Background(), mainnet, compareRequest())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc unreachable")
}
