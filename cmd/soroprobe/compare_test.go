package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

const (
	testnetPassphrase = "Test SDF Network ; September 2015"
	mainnetPassphrase = "Public Global Stellar Network ; September 2015"
)

// compareConfigFile writes the two-network profile configuration a compare run
// reads its endpoints from.
func compareConfigFile(t *testing.T, passphraseTestnet, passphraseMainnet string) string {
	t.Helper()

	return writeTempFile(t, "soroprobe.json", `{
		"rpc_url": "https://top.example.com",
		"network_passphrase": "Top Network",
		"profiles": {
			"testnet": {
				"rpc_url": "https://testnet.example.com",
				"network_passphrase": "`+passphraseTestnet+`"
			},
			"mainnet": {
				"rpc_url": "https://mainnet.example.com",
				"network_passphrase": "`+passphraseMainnet+`"
			}
		}
	}`)
}

// runCompareWithFakes executes compare with one fixture-backed prober per
// passphrase, so each side of the comparison talks to its own network.
func runCompareWithFakes(t *testing.T, testnetFake, mainnetFake *stellartest.Fake, args ...string) (string, error) {
	t.Helper()

	t.Chdir(t.TempDir())

	cmd, opts := newRootCmd()
	opts.newProber = func(cfg config.Config) (*probe.Prober, func(), error) {
		fake := testnetFake
		if cfg.NetworkPassphrase == mainnetPassphrase {
			fake = mainnetFake
		}
		p, err := probe.New(probe.Options{
			Client:        fake,
			SourceAccount: cfg.SourceAccount,
			Thresholds:    health.Thresholds{Warn: cfg.WarnLedgers, Critical: cfg.CriticalLedgers},
		})
		return p, func() {}, err
	}

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	err := cmd.Execute()
	return out.String(), err
}

// deployForCompare deploys a Wasm build to both fakes at the same address.
func deployForCompare(t *testing.T, testnetWasm, mainnetWasm []byte) (*stellartest.Fake, *stellartest.Fake) {
	t.Helper()

	testnetFake := stellartest.NewFake(t)
	mainnetFake := stellartest.NewFake(t)
	testnetFake.DeployWasm(t, stellartest.WasmContract, testnetWasm)
	mainnetFake.DeployWasm(t, stellartest.WasmContract, mainnetWasm)
	return testnetFake, mainnetFake
}

func TestCompareReportsMatchingDeployments(t *testing.T) {
	wasm := []byte("the same build")
	testnetFake, mainnetFake := deployForCompare(t, wasm, wasm)
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
		"--fn", "decimals",
	)
	require.NoError(t, err)

	assert.Contains(t, out, "compare")
	assert.Contains(t, out, "wasm")
	assert.Contains(t, out, "state entries")
	assert.Contains(t, out, "read-only calls")
	assert.Contains(t, out, "the deployed Wasm is the same on both networks")
	assert.Contains(t, out, "result MATCH")

	// The call ran on both networks, which the fake only records when the
	// real transaction-building code ran.
	assert.Equal(t, []string{"decimals"}, testnetFake.Functions)
	assert.Equal(t, []string{"decimals"}, mainnetFake.Functions)
}

func TestCompareFailsWhenTheWasmDiffers(t *testing.T) {
	testnetFake, mainnetFake := deployForCompare(t, []byte("build one"), []byte("build two"))
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
	)
	require.Error(t, err)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "the deployed Wasm differs")
	assert.Contains(t, out, "result DIFFERENT")
}

func TestCompareJSONOutput(t *testing.T) {
	wasm := []byte("the same build")
	testnetFake, mainnetFake := deployForCompare(t, wasm, wasm)
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"--json",
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
		"--fn", "decimals",
	)
	require.NoError(t, err)

	var result probe.CompareResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.True(t, result.WasmMatch)
	assert.True(t, result.Match)
	assert.Equal(t, "testnet", result.Testnet.Network)
	assert.Equal(t, "mainnet", result.Mainnet.Network)
	require.Len(t, result.Calls, 1)
	assert.False(t, result.Calls[0].Differ)
}

func TestCompareHighlightsTTLDifferencesWithoutFailingTheWasmVerdict(t *testing.T) {
	wasm := []byte("same build")
	testnetFake, mainnetFake := deployForCompare(t, wasm, wasm)

	instanceKey := stellartest.InstanceKeyOf(t, stellartest.WasmContract)
	testnetFake.SetTTL(t, instanceKey, testnetFake.LatestLedger+50_000)
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
	)
	require.NoError(t, err, "a TTL difference is reported, not a failed comparison")

	assert.Contains(t, out, "warning")
	assert.Contains(t, out, "DIFFERENT")
	assert.Contains(t, out, "result MATCH")
	assert.Contains(t, out, "other difference")
}

func TestCompareCallsFileRunsEveryCall(t *testing.T) {
	wasm := []byte("same build")
	testnetFake, mainnetFake := deployForCompare(t, wasm, wasm)
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)
	callsPath := writeTempFile(t, "calls.json", `{"calls": [
		{"name": "decimals", "function": "decimals"},
		{"name": "transfer", "function": "transfer"}
	]}`)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
		"--calls", callsPath,
	)
	require.NoError(t, err)

	assert.Contains(t, out, "decimals")
	assert.Contains(t, out, "transfer")
	assert.Equal(t, []string{"decimals", "transfer"}, testnetFake.Functions)
	assert.Equal(t, []string{"decimals", "transfer"}, mainnetFake.Functions)
}

func TestCompareUsesPerNetworkContractIDs(t *testing.T) {
	wasm := []byte("same build")
	testnetFake := stellartest.NewFake(t)
	mainnetFake := stellartest.NewFake(t)
	testnetFake.DeployWasm(t, stellartest.WasmContract, wasm)
	mainnetFake.DeployWasm(t, stellartest.SACContract, wasm)
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
		"--testnet-contract", stellartest.WasmContract,
		"--mainnet-contract", stellartest.SACContract,
	)
	require.NoError(t, err)
	assert.Contains(t, out, stellartest.WasmContract)
	assert.Contains(t, out, stellartest.SACContract)
}

func TestCompareFailsWhenOneSideIsNotDeployed(t *testing.T) {
	wasm := []byte("same build")
	testnetFake := stellartest.NewFake(t)
	mainnetFake := stellartest.NewFake(t)
	testnetFake.DeployWasm(t, stellartest.UndeployedContract, wasm)
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	out, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.UndeployedContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
	)
	require.Error(t, err)
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "not deployed")
	assert.Contains(t, out, "result DIFFERENT")
}

func TestCompareReportIsWrittenEvenWhenTheWasmDiffers(t *testing.T) {
	testnetFake, mainnetFake := deployForCompare(t, []byte("build one"), []byte("build two"))
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)
	report := filepath.Join(t.TempDir(), "compare.json")

	_, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
		"--report", report,
	)
	require.Error(t, err)
	assertExitCodeOne(t, err)

	raw, readErr := os.ReadFile(report) //nolint:gosec // the path is a report this test asked the command to write in t.TempDir.
	require.NoError(t, readErr)
	var result probe.CompareResult
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.False(t, result.WasmMatch)
}

func TestCompareValidatesItsInputBeforeAnyRequest(t *testing.T) {
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)
	contract := stellartest.WasmContract

	tests := map[string]struct {
		args []string
		want string
	}{
		"missing testnet": {
			[]string{"compare", contract, "--config", cfgPath, "--mainnet", "mainnet"},
			"--testnet is required",
		},
		"missing mainnet": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet"},
			"--mainnet is required",
		},
		"same profile on both sides": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "testnet"},
			"same profile",
		},
		"single-network rpc flag": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet", "--rpc-url", "https://one.example.com"},
			"--rpc-url names one network",
		},
		"single-network profile flag": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet", "--profile", "mainnet"},
			"not --profile",
		},
		"fn and calls together": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet", "--fn", "decimals", "--calls", "calls.json"},
			"not both",
		},
		"arg without fn": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet", "--arg", "u32:1"},
			"--arg needs --fn",
		},
		"bad contract id": {
			[]string{"compare", "not-a-contract", "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet"},
			"contract",
		},
		"unsupported format": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet", "--format", "yaml"},
			"unsupported output format",
		},
		"markdown is not offered": {
			[]string{"compare", contract, "--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet", "--format", "markdown"},
			"not markdown",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := runWith(t, tt.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCompareRejectsASingleNetworkPosedAsTwoProfiles(t *testing.T) {
	wasm := []byte("same build")
	testnetFake, mainnetFake := deployForCompare(t, wasm, wasm)
	// Both profiles identify the same network; comparing it with itself would
	// always pass and tell the operator nothing.
	cfgPath := compareConfigFile(t, testnetPassphrase, testnetPassphrase)

	_, err := runCompareWithFakes(t, testnetFake, mainnetFake,
		"compare", stellartest.WasmContract,
		"--config", cfgPath,
		"--testnet", "testnet", "--mainnet", "mainnet",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "same network passphrase")
}

func TestCompareRejectsSameRPCEndpoint(t *testing.T) {
	cfgPath := writeTempFile(t, "soroprobe.json", `{
		"profiles": {
			"testnet": {"rpc_url": "https://one.example.com", "network_passphrase": "`+testnetPassphrase+`"},
			"mainnet": {"rpc_url": "https://one.example.com", "network_passphrase": "`+mainnetPassphrase+`"}
		}
	}`)

	err := runWith(t, "compare", stellartest.WasmContract,
		"--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both use RPC endpoint")
}

func TestCompareRejectsABadCallsFile(t *testing.T) {
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	for name, body := range map[string]string{
		"no calls":         `{"calls": []}`,
		"unknown field":    `{"calls": [{"name": "x", "function": "decimals", "typo": true}]}`,
		"missing function": `{"calls": [{"name": "x"}]}`,
		"duplicate names":  `{"calls": [{"name": "x", "function": "decimals"}, {"name": "x", "function": "decimals"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			callsPath := writeTempFile(t, "calls.json", body)
			err := runWith(t, "compare", stellartest.WasmContract,
				"--config", cfgPath, "--testnet", "testnet", "--mainnet", "mainnet",
				"--calls", callsPath)
			require.Error(t, err)
		})
	}
}

func TestCompareRejectsAMissingProfile(t *testing.T) {
	cfgPath := compareConfigFile(t, testnetPassphrase, mainnetPassphrase)

	err := runWith(t, "compare", stellartest.WasmContract,
		"--config", cfgPath, "--testnet", "testnet", "--mainnet", "futurenet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown profile "futurenet"`)
}
