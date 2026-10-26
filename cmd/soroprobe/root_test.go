package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// resolveConfig parses args and runs config resolution, without executing the
// command. It exists because --help short-circuits cobra before the persistent
// pre-run hook, so flag precedence cannot be observed through Execute.
func resolveConfig(t *testing.T, args ...string) config.Config {
	t.Helper()

	// Isolate from any soroprobe.json in the working tree.
	t.Chdir(t.TempDir())

	cmd, opts := newRootCmd()
	sub, rest, err := cmd.Find(args)
	require.NoError(t, err)
	require.NoError(t, sub.ParseFlags(rest))
	require.NoError(t, opts.resolve(sub))

	return opts.cfg
}

// runWith executes a command and returns its error. Every case here fails
// before any network call is attempted.
func runWith(t *testing.T, args ...string) error {
	t.Helper()

	t.Chdir(t.TempDir())

	cmd, _ := newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)

	return cmd.Execute()
}

// runWithFake executes args with the prober factory swapped for one backed by
// the fixture fake, and returns stdout and the error. This drives commands end
// end to end — flag parsing, config resolution, probing, rendering and the
// exit-code mapping — without a network.
func runWithFake(t *testing.T, fake *stellartest.Fake, args ...string) (string, error) {
	t.Helper()

	t.Chdir(t.TempDir())

	cmd, opts := newRootCmd()
	opts.newProber = func(cfg config.Config) (*probe.Prober, func(), error) {
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

// failingWriter rejects every write, standing in for stdout that has gone
// away — a closed pipe, for example.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// TestCheckReportsAWriteFailure guards the error path that used to be ignored:
// a check whose report cannot be written must fail rather than exit 0 as if
// the report had been printed.
func TestCheckReportsAWriteFailure(t *testing.T) {
	fake := stellartest.NewFake(t)

	t.Chdir(t.TempDir())
	cmd, opts := newRootCmd()
	opts.newProber = func(cfg config.Config) (*probe.Prober, func(), error) {
		p, err := probe.New(probe.Options{
			Client:        fake,
			SourceAccount: cfg.SourceAccount,
			Thresholds:    health.Thresholds{Warn: cfg.WarnLedgers, Critical: cfg.CriticalLedgers},
		})
		return p, func() {}, err
	}

	cmd.SetOut(failingWriter{})
	cmd.SetErr(failingWriter{})
	cmd.SetArgs([]string{"check", stellartest.SACContract})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write failed")
}

// writeTempFile writes content to a file in a fresh temp dir and returns its
// path, so a test can hand the CLI a --file, --baseline or --report argument.
func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// assertExitCodeOne asserts the error maps to exit code 1 — a probe that ran
// and found the contract unhealthy — without calling os.Exit.
func assertExitCodeOne(t *testing.T, err error) {
	t.Helper()

	var failed *checkFailedError
	require.True(t, errors.As(err, &failed), "expected a check-failed error (exit 1), got %v", err)
}

func TestFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv(config.EnvRPCURL, "https://from-env.example.com")
	t.Setenv(config.EnvLogLevel, "error")

	cfg := resolveConfig(t, "inspect", "--rpc-url", "https://from-flag.example.com")

	assert.Equal(t, "https://from-flag.example.com", cfg.RPCURL, "a flag must beat the environment")
	assert.Equal(t, "error", cfg.LogLevel, "unset flags leave the environment value alone")
}

func TestEnvironmentUsedWhenNoFlags(t *testing.T) {
	t.Setenv(config.EnvRPCURL, "https://from-env.example.com")

	cfg := resolveConfig(t, "inspect")
	assert.Equal(t, "https://from-env.example.com", cfg.RPCURL)
}

func TestAllPersistentFlagsAreApplied(t *testing.T) {
	cfg := resolveConfig(t, "inspect",
		"--rpc-url", "https://rpc.example.com",
		"--network-passphrase", "Custom Network",
		"--source-account", "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
		"--log-level", "debug",
		"--log-format", "json",
		"--timeout", "12s",
		"--warn-ledgers", "999",
		"--critical-ledgers", "99",
	)

	assert.Equal(t, "https://rpc.example.com", cfg.RPCURL)
	assert.Equal(t, "Custom Network", cfg.NetworkPassphrase)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "json", cfg.LogFormat)
	assert.Equal(t, 12*time.Second, cfg.Timeout)
	assert.EqualValues(t, 999, cfg.WarnLedgers)
	assert.EqualValues(t, 99, cfg.CriticalLedgers)
}

func TestPerKindThresholdAndChainABIFlags(t *testing.T) {
	cfg := resolveConfig(t, "simulate", "--data-warn-ledgers", "500", "--data-critical-ledgers", "25", "--abi-from-chain")
	require.NotNil(t, cfg.ThresholdOverrides.Data)
	assert.EqualValues(t, 500, *cfg.ThresholdOverrides.Data.Warn)
	assert.EqualValues(t, 25, *cfg.ThresholdOverrides.Data.Critical)

	cmd, opts := newRootCmd()
	sub, rest, err := cmd.Find([]string{"simulate", "--abi-from-chain"})
	require.NoError(t, err)
	require.NoError(t, sub.ParseFlags(rest))
	require.NoError(t, opts.resolve(sub))
	assert.True(t, opts.abiFromChain)
}

func TestLogFormatDefaultsToText(t *testing.T) {
	cfg := resolveConfig(t, "inspect")
	assert.Equal(t, "text", cfg.LogFormat)
}

func TestAPITokenFlagOverridesEnvironment(t *testing.T) {
	t.Setenv(config.EnvAPIToken, "env-token-0123456789")

	cfg := resolveConfig(t, "serve", "--api-token", "flag-token-0123456789")
	assert.Equal(t, "flag-token-0123456789", cfg.APIToken, "a flag must beat the environment")
}

func TestAPITokenFromEnvironmentIsResolvedForServe(t *testing.T) {
	t.Setenv(config.EnvAPIToken, "env-token-0123456789")

	cfg := resolveConfig(t, "serve")
	assert.Equal(t, "env-token-0123456789", cfg.APIToken)
}

func TestInvalidConfigIsRejectedBeforeAnyNetworkCall(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"bad source account", []string{"inspect", "CDLZ", "--source-account", "not-a-key"}},
		{"secret key as source account", []string{
			"inspect", "CDLZ",
			"--source-account", "SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		}},
		{"bad log level", []string{"inspect", "CDLZ", "--log-level", "chatty"}},
		{
			// The error must name the accepted values, so the operator does
			// not have to guess them.
			"bad log format",
			[]string{"inspect", "CDLZ", "--log-format", "yaml"},
		},
		{"rpc url without scheme", []string{"inspect", "CDLZ", "--rpc-url", "rpc.example.com"}},
		{"critical above warn", []string{"inspect", "CDLZ", "--warn-ledgers", "10", "--critical-ledgers", "100"}},
		{
			// A token too short to be safe must stop serve before it binds.
			"short api token",
			[]string{"serve", "--api-token", "tooshort"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, runWith(t, tt.args...))
		})
	}
}

func TestVersionCommandNeedsNoConfig(t *testing.T) {
	// `version` must work even when the configuration is unusable, so that it
	// stays useful in a bug report.
	t.Setenv(config.EnvRPCURL, "not-a-url")
	t.Chdir(t.TempDir())

	cmd, _ := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	require.NoError(t, cmd.Execute())
	assert.NotEmpty(t, out.String())
}

func TestProfileSelection(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "soroprobe.json")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`{
		"rpc_url": "https://default.example.com",
		"network_passphrase": "Default Network",
		"warn_ledgers": 100,
		"critical_ledgers": 10,
		"profiles": {
			"mainnet": {
				"rpc_url": "https://mainnet.example.com",
				"network_passphrase": "Public Global Stellar Network ; September 2015",
				"warn_ledgers": 200,
				"critical_ledgers": 20
			},
			"testnet": {
				"rpc_url": "https://testnet.example.com"
			}
		}
	}`), 0o600))

	t.Run("profile selected via flag", func(t *testing.T) {
		cfg := resolveConfig(t, "inspect", "--config", cfgPath, "--profile", "mainnet")
		assert.Equal(t, "https://mainnet.example.com", cfg.RPCURL)
		assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
		assert.EqualValues(t, 200, cfg.WarnLedgers)
	})

	t.Run("profile selected via env", func(t *testing.T) {
		t.Setenv(config.EnvProfile, "mainnet")
		cfg := resolveConfig(t, "inspect", "--config", cfgPath)
		assert.Equal(t, "https://mainnet.example.com", cfg.RPCURL)
		assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
		assert.EqualValues(t, 200, cfg.WarnLedgers)
	})

	t.Run("flag profile overrides env profile", func(t *testing.T) {
		t.Setenv(config.EnvProfile, "mainnet")
		cfg := resolveConfig(t, "inspect", "--config", cfgPath, "--profile", "testnet")
		assert.Equal(t, "https://testnet.example.com", cfg.RPCURL)
		assert.Equal(t, "Default Network", cfg.NetworkPassphrase)
	})

	t.Run("unknown profile returns error with available profiles", func(t *testing.T) {
		err := runWith(t, "inspect", "CDLZ", "--config", cfgPath, "--profile", "futurenet")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown profile "futurenet"`)
		assert.Contains(t, err.Error(), "mainnet, testnet")
	})
}

func TestCommandArgumentValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"simulate needs a function", []string{"simulate", "CDLZ"}},
		{"simulate needs a contract", []string{"simulate"}},
		{"inspect needs a contract", []string{"inspect"}},
		{"inspect takes only one contract", []string{"inspect", "CDLZ", "CDEF"}},
		{"check needs a contract", []string{"check"}},
		{"check takes a contract or a file, not both", []string{"check", "CDLZ", "--file", "checks.json"}},
		{"check --file must exist", []string{"check", "--file", "no-such-file.json"}},
		{"serve takes no arguments", []string{"serve", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Error(t, runWith(t, tt.args...))
		})
	}
}

func TestUnknownDurabilityIsRejected(t *testing.T) {
	err := runWith(t, "inspect", "CDLZ", "--durability", "forever")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "durability")
}

func TestProberRequiresValidConfig(t *testing.T) {
	opts := &options{cfg: config.Default()}
	p, cleanup, err := opts.prober()
	require.NoError(t, err)
	require.NotNil(t, p)
	cleanup()
}

func TestProberFactoryErrorsSurface(t *testing.T) {
	// Whatever the factory fails with is what the command reports, so a
	// test can pin the failure path without a network.
	sentinel := errors.New("prober factory failed")

	run := func(t *testing.T, args ...string) error {
		t.Helper()
		t.Chdir(t.TempDir())

		cmd, opts := newRootCmd()
		opts.newProber = func(config.Config) (*probe.Prober, func(), error) {
			return nil, nil, sentinel
		}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		return cmd.Execute()
	}

	checks := writeTempFile(t, "checks.json", `{"checks": []}`)
	calls := writeTempFile(t, "calls.json", `{"calls": []}`)

	for _, args := range [][]string{
		{"simulate", stellartest.SACContract, "decimals"},
		{"simulate", "--file", calls},
		{"inspect", stellartest.SACContract},
		{"check", stellartest.SACContract},
		{"check", "--file", checks},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			require.ErrorIs(t, run(t, args...), sentinel)
		})
	}
}

func TestSimulateRunsEndToEndAgainstTheFake(t *testing.T) {
	fake := stellartest.NewFake(t)

	out, err := runWithFake(t, fake, "simulate", stellartest.SACContract, "decimals")
	require.NoError(t, err)

	assert.Contains(t, out, stellartest.SACContract)
	assert.Contains(t, out, "SUCCESS")
	assert.Contains(t, out, "return value")
	// The fake decodes the envelope it is handed, so a recorded function
	// name proves the real transaction-building code ran.
	assert.Equal(t, []string{"decimals"}, fake.Functions)
}

func TestSimulateJSONOutputEndToEnd(t *testing.T) {
	fake := stellartest.NewFake(t)

	out, err := runWithFake(t, fake, "--json", "simulate", stellartest.SACContract, "decimals")
	require.NoError(t, err)

	var result probe.SimulateResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.True(t, result.Success)
	assert.EqualValues(t, 7, result.ReturnValue)
	assert.Positive(t, result.Cost.Instructions)
}

func TestSimulateReportsARejectedCallWithoutFailing(t *testing.T) {
	fake := stellartest.NewFake(t)

	out, err := runWithFake(t, fake, "simulate", stellartest.SACContract, "no_such_fn")
	require.NoError(t, err, "a rejected call is an answer, not a tool failure")
	assert.Contains(t, out, "FAILED")
}

func TestSimulateFileRunsEveryCallEndToEnd(t *testing.T) {
	fake := stellartest.NewFake(t)
	path := writeTempFile(t, "calls.json", `{"calls": [
		{"name": "ok", "contract_id": "`+stellartest.SACContract+`", "function": "decimals"},
		{"name": "rejected", "contract_id": "`+stellartest.SACContract+`", "function": "no_such_fn"}
	]}`)

	out, err := runWithFake(t, fake, "simulate", "--file", path)
	require.NoError(t, err, "a rejected call does not stop the batch")
	assert.Contains(t, out, "ok")
	assert.Contains(t, out, "rejected")
	assert.Equal(t, []string{"decimals", "no_such_fn"}, fake.Functions)
}

func TestSimulateFileReportsACallThatCouldNotRun(t *testing.T) {
	fake := stellartest.NewFake(t)
	path := writeTempFile(t, "calls.json", `{"calls": [
		{"name": "bad", "contract_id": "not-a-contract", "function": "decimals"}
	]}`)

	_, err := runWithFake(t, fake, "simulate", "--file", path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not be run")

	// A batch that could not be completed is a runtime failure (exit 2),
	// not a check failure (exit 1).
	var failed *checkFailedError
	assert.False(t, errors.As(err, &failed))
}

func TestSimulateFileRejectsConflictingFlags(t *testing.T) {
	fake := stellartest.NewFake(t)

	for name, args := range map[string][]string{
		"contract and file": {"simulate", "--file", "calls.json", stellartest.SACContract, "decimals"},
		"file and baseline": {"simulate", "--file", "calls.json", "--baseline", "b.json"},
		"file and max":      {"simulate", "--file", "calls.json", "--max-increase", "10%"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := runWithFake(t, fake, args...)
			require.Error(t, err)
		})
	}
}

func TestSimulateBaselineErrors(t *testing.T) {
	fake := stellartest.NewFake(t)
	baseline := writeBaseline(t, map[string]any{
		"contract_id": stellartest.SACContract,
		"function":    "decimals",
		"cost":        recordedCost(),
	})

	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"missing file":            {[]string{"--baseline", filepath.Join(t.TempDir(), "nope.json")}, "read baseline"},
		"no baseline":             {[]string{"--max-increase", "10%"}, "--max-increase needs --baseline"},
		"not a percentage":        {[]string{"--baseline", baseline, "--max-increase", "lots"}, "expected a percentage"},
		"negative percent":        {[]string{"--baseline", baseline, "--max-increase=-5%"}, "cannot be negative"},
		"baseline is a directory": {[]string{"--baseline", t.TempDir()}, "read baseline"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"simulate", stellartest.SACContract, "decimals"}, tt.args...)
			_, err := runWithFake(t, fake, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestSimulateBaselineVerdictsEndToEnd(t *testing.T) {
	fake := stellartest.NewFake(t)

	t.Run("within the threshold passes", func(t *testing.T) {
		path := writeBaseline(t, map[string]any{
			"contract_id": stellartest.SACContract,
			"function":    "decimals",
			"cost":        recordedCost(),
		})

		out, err := runWithFake(t, fake,
			"simulate", stellartest.SACContract, "decimals", "--baseline", path, "--max-increase", "10%")
		require.NoError(t, err)
		assert.Contains(t, out, "cost compared to the baseline")
	})

	t.Run("beyond the threshold exits 1", func(t *testing.T) {
		path := writeBaseline(t, map[string]any{
			"contract_id": stellartest.SACContract,
			"function":    "decimals",
			"cost":        map[string]any{"instructions": 1000, "resource_fee_stroops": 1000},
		})

		out, err := runWithFake(t, fake,
			"simulate", stellartest.SACContract, "decimals", "--baseline", path, "--max-increase", "10%")
		require.Error(t, err)
		assertExitCodeOne(t, err)
		assert.Contains(t, out, "more than 10%")
	})
}

func TestSimulateReportFlagWritesJSONWhilePrintingText(t *testing.T) {
	fake := stellartest.NewFake(t)
	report := filepath.Join(t.TempDir(), "report.json")

	out, err := runWithFake(t, fake,
		"simulate", stellartest.SACContract, "decimals", "--report", report)
	require.NoError(t, err)
	assert.Contains(t, out, "SUCCESS", "the text report still goes to the terminal")

	raw, err := os.ReadFile(report) //nolint:gosec // the path is a report this test asked the command to write in t.TempDir.
	require.NoError(t, err)
	var result probe.SimulateResult
	require.NoError(t, json.Unmarshal(raw, &result))
	assert.True(t, result.Success)
}

func TestSimulateFailsFastOnAnUnwritableReport(t *testing.T) {
	fake := stellartest.NewFake(t)

	_, err := runWithFake(t, fake,
		"simulate", stellartest.SACContract, "decimals",
		"--report", filepath.Join(t.TempDir(), "missing", "report.json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create report file")
}

func TestInspectRunsEndToEndAgainstTheFake(t *testing.T) {
	fake := stellartest.NewFake(t)

	out, err := runWithFake(t, fake, "inspect", stellartest.WasmContract)
	require.NoError(t, err)

	assert.Contains(t, out, stellartest.WasmContract)
	assert.Contains(t, out, "state entries")
	assert.Contains(t, out, "wasm")
	assert.Contains(t, out, "overall")
}

func TestInspectJSONOutputEndToEnd(t *testing.T) {
	fake := stellartest.NewFake(t)

	out, err := runWithFake(t, fake, "--json", "inspect", stellartest.WasmContract)
	require.NoError(t, err)

	var result probe.InspectResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.True(t, result.Deployed)
	assert.Equal(t, "wasm", result.Executable)
}

func TestCheckRunsEndToEndAgainstTheFake(t *testing.T) {
	fake := stellartest.NewFake(t)

	t.Run("healthy contract passes", func(t *testing.T) {
		out, err := runWithFake(t, fake, "check", "--fn", "decimals", stellartest.SACContract)
		require.NoError(t, err)
		assert.Contains(t, out, "PASS")
	})

	t.Run("unhealthy contract exits 1", func(t *testing.T) {
		out, err := runWithFake(t, fake, "check", stellartest.UndeployedContract)
		require.Error(t, err)
		assertExitCodeOne(t, err)
		assert.Contains(t, out, "FAIL")
	})

	t.Run("a tool failure exits 2", func(t *testing.T) {
		_, err := runWithFake(t, fake, "check", "CDLZ")
		require.Error(t, err)

		// Invalid input is a runtime failure, not a verdict on the
		// contract, so it must not read as a check failure.
		var failed *checkFailedError
		assert.False(t, errors.As(err, &failed))
		assert.Contains(t, err.Error(), "invalid contract id")
	})

	t.Run("cost budget check passes within limit", func(t *testing.T) {
		out, err := runWithFake(t, fake, "check", "--fn", "decimals", "--max-instructions", "150000", "--max-fee", "20000", "--max-write-bytes", "100", stellartest.SACContract)
		require.NoError(t, err)
		assert.Contains(t, out, "PASS")
		assert.Contains(t, out, "cost")
	})

	t.Run("cost budget check fails when limit exceeded", func(t *testing.T) {
		out, err := runWithFake(t, fake, "check", "--fn", "decimals", "--max-instructions", "100000", stellartest.SACContract)
		require.Error(t, err)
		assertExitCodeOne(t, err)
		assert.Contains(t, out, "FAIL")
		assert.Contains(t, out, "cost")
		assert.Contains(t, out, "instructions 127569 exceeds limit 100000")
	})
}

func TestCheckFileRunsEndToEndAgainstTheFake(t *testing.T) {
	fake := stellartest.NewFake(t)

	passing := writeTempFile(t, "passing.json", `{"checks": [
		{"name": "token", "contract_id": "`+stellartest.SACContract+`", "function": "decimals"}
	]}`)
	failing := writeTempFile(t, "failing.json", `{"checks": [
		{"name": "token", "contract_id": "`+stellartest.SACContract+`", "function": "decimals"},
		{"name": "gone", "contract_id": "`+stellartest.UndeployedContract+`"}
	]}`)
	broken := writeTempFile(t, "broken.json", `{"checks": [
		{"name": "bad", "contract_id": "not-a-contract"}
	]}`)
	duplicate := writeTempFile(t, "duplicate.json", `{"checks": [
		{"name": "same", "contract_id": "`+stellartest.SACContract+`"},
		{"name": "same", "contract_id": "`+stellartest.WasmContract+`"}
	]}`)
	malformed := writeTempFile(t, "malformed.json", `{`)

	t.Run("all checks pass", func(t *testing.T) {
		out, err := runWithFake(t, fake, "check", "--file", passing)
		require.NoError(t, err)
		assert.Contains(t, out, "token")
		assert.Contains(t, out, "1 passed, 0 failed")
	})

	t.Run("a failed check exits 1", func(t *testing.T) {
		out, err := runWithFake(t, fake, "check", "--file", failing)
		require.Error(t, err)
		assertExitCodeOne(t, err)
		assert.Contains(t, out, "1 passed, 1 failed")
	})

	t.Run("a check that could not run exits 2", func(t *testing.T) {
		_, err := runWithFake(t, fake, "check", "--file", broken)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not be run")

		var failed *checkFailedError
		assert.False(t, errors.As(err, &failed))
	})

	t.Run("json output", func(t *testing.T) {
		out, err := runWithFake(t, fake, "--json", "check", "--file", passing)
		require.NoError(t, err)

		var result probe.BatchResult
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		assert.True(t, result.OK)
		require.Len(t, result.Items, 1)
		assert.Equal(t, "token", result.Items[0].Name)
	})

	t.Run("report is written even when the batch fails", func(t *testing.T) {
		report := filepath.Join(t.TempDir(), "report.json")

		_, err := runWithFake(t, fake, "check", "--file", failing, "--report", report)
		require.Error(t, err)
		assertExitCodeOne(t, err)

		raw, readErr := os.ReadFile(report) //nolint:gosec // the path is a report this test asked the command to write in t.TempDir.
		require.NoError(t, readErr)
		var result probe.BatchResult
		require.NoError(t, json.Unmarshal(raw, &result))
		assert.False(t, result.OK)
	})

	t.Run("concurrency and a default timeout are accepted", func(t *testing.T) {
		_, err := runWithFake(t, fake, "check", "--file", passing,
			"--concurrency", "2", "--check-timeout", "5s")
		require.NoError(t, err)
	})

	t.Run("duplicate names are rejected", func(t *testing.T) {
		_, err := runWithFake(t, fake, "check", "--file", duplicate)
		require.Error(t, err)
		assert.ErrorIs(t, err, probe.ErrInvalidBatch)
	})

	t.Run("a malformed file is rejected", func(t *testing.T) {
		_, err := runWithFake(t, fake, "check", "--file", malformed)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read check file")
	})
}
