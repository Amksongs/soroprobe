package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// compareCallsFile is the shape of `compare --calls`.
//
// It is deliberately smaller than a simulation batch: the contract is the same
// one being compared, and the point is which calls to run, so a call names only
// a function and its arguments.
type compareCallsFile struct {
	Calls []probe.CompareCall `json:"calls"`
}

func newCompareCmd(opts *options) *cobra.Command {
	var testnetProfile, mainnetProfile string
	var testnetContract, mainnetContract string
	var function string
	var callArgs []string
	var callsPath string
	var dataKeys []string
	var keyFile string
	var durability string
	var reportPath string
	var format string

	cmd := &cobra.Command{
		Use:   "compare <contract-id> --testnet <profile> --mainnet <profile>",
		Short: "Compare a contract's Wasm, TTLs and read-only calls across two networks",
		Long: `Compare the same contract on two configured networks, so a testnet deployment
can be checked against mainnet before it ships.

Two things decide whether the deployments agree, and the report shows both:

  wasm          the deployed executable. Equal Wasm hashes mean the same code
                is running; a Stellar Asset Contract on both sides counts as a
                match because the host implements it.
  state entries the health of the instance, code and any named data entries.
                Remaining ledger counts always differ between chains, so the
                comparison highlights a difference in health status, not in the
                raw count.

Read-only calls named with --fn (or listed in --calls) are simulated on both
networks and their success and decoded return values are compared.

Each side's network comes from a config profile, not from --rpc-url or
--network-passphrase: those name exactly one network and would point both sides
at the same endpoint. The profile supplies rpc_url and network_passphrase;
everything else (source account, thresholds, timeout) still follows the usual
precedence, with flags applied to both sides.

The contract address is taken to be the same on both networks, which is the
usual case for a deterministic deployment. Use --testnet-contract or
--mainnet-contract when the two addresses differ.

  soroprobe compare C... --testnet testnet --mainnet mainnet
  soroprobe compare C... --testnet testnet --mainnet mainnet --fn decimals
  soroprobe compare C... --testnet testnet --mainnet mainnet --calls calls.json

--key and --key-file work as they do for inspect, naming data entries whose TTL
should be compared. --calls reads {"calls": [{"name", "function", "args"}]}
("-" for stdin); unknown fields are rejected.

Exits 0 when the Wasm hashes match, 1 when they do not (including when one side
is not deployed), and 2 when SoroProbe itself could not run. TTL and call
differences are reported and highlighted but do not change the exit code: the
verdict is about the code being the same.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if testnetProfile == "" {
				return fmt.Errorf("--testnet is required: name the config profile for the first network")
			}
			if mainnetProfile == "" {
				return fmt.Errorf("--mainnet is required: name the config profile for the second network")
			}
			if testnetProfile == mainnetProfile {
				return fmt.Errorf("--testnet and --mainnet name the same profile %q; comparing a network with itself shows nothing", testnetProfile)
			}
			if cmd.Flags().Changed("profile") {
				return fmt.Errorf("give networks with --testnet and --mainnet, not --profile; compare needs two")
			}
			for _, flag := range []string{"rpc-url", "network-passphrase"} {
				if cmd.Flags().Changed(flag) {
					return fmt.Errorf("--%s names one network, so it cannot be used with compare; set it in each profile", flag)
				}
			}
			if function != "" && callsPath != "" {
				return fmt.Errorf("give --fn or --calls, not both")
			}
			if function == "" && len(callArgs) > 0 {
				return fmt.Errorf("--arg needs --fn, which is what it is an argument for")
			}

			outputFormat, err := selectOutputFormat(cmd, opts, format)
			if err != nil {
				return err
			}
			if outputFormat == "markdown" {
				return fmt.Errorf("compare supports text or json output, not markdown")
			}
			d, err := parseDurability(durability)
			if err != nil {
				return err
			}

			testnetID, mainnetID := args[0], args[0]
			if testnetContract != "" {
				testnetID = testnetContract
			}
			if mainnetContract != "" {
				mainnetID = mainnetContract
			}
			// Reject a malformed address before loading any network, so a typo
			// costs no requests and produces one clear message.
			for _, id := range []struct {
				side string
				id   string
			}{{"--testnet", testnetID}, {"--mainnet", mainnetID}} {
				if _, err := stellar.ParseContractID(id.id); err != nil {
					return fmt.Errorf("%s contract: %w", id.side, err)
				}
			}

			calls, err := readCompareCalls(cmd.InOrStdin(), callsPath, function, callArgs)
			if err != nil {
				return err
			}

			keys, err := mergeKeys(keyFile, dataKeys)
			if err != nil {
				return err
			}

			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, report.close()) }()

			testnetCfg, err := compareConfig(opts, cmd, testnetProfile)
			if err != nil {
				return fmt.Errorf("testnet profile %q: %w", testnetProfile, err)
			}
			mainnetCfg, err := compareConfig(opts, cmd, mainnetProfile)
			if err != nil {
				return fmt.Errorf("mainnet profile %q: %w", mainnetProfile, err)
			}
			if testnetCfg.RPCURL == mainnetCfg.RPCURL {
				return fmt.Errorf("profiles %q and %q both use RPC endpoint %s; two different networks are required",
					testnetProfile, mainnetProfile, testnetCfg.RPCURL)
			}
			if testnetCfg.NetworkPassphrase == mainnetCfg.NetworkPassphrase {
				return fmt.Errorf("profiles %q and %q use the same network passphrase; two different networks are required",
					testnetProfile, mainnetProfile)
			}

			testnetProber, testnetCleanup, err := opts.proberFor(testnetCfg)
			if err != nil {
				return err
			}
			defer testnetCleanup()

			mainnetProber, mainnetCleanup, err := opts.proberFor(mainnetCfg)
			if err != nil {
				return err
			}
			defer mainnetCleanup()

			result, err := testnetProber.Compare(cmd.Context(), mainnetProber, probe.CompareRequest{
				ContractID:      testnetID,
				OtherContractID: mainnetID,
				TestnetLabel:    testnetProfile,
				MainnetLabel:    mainnetProfile,
				DataKeys:        keys,
				DataDurability:  d,
				Calls:           calls,
			})
			if err != nil {
				return err
			}

			if err := report.write(result); err != nil {
				return err
			}

			if outputFormat == "json" {
				if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
					return err
				}
			} else if err := renderCompare(cmd.OutOrStdout(), result); err != nil {
				return err
			}

			if !result.WasmMatch {
				return &checkFailedError{msg: fmt.Sprintf(
					"the Wasm on %s does not match %s", mainnetProfile, testnetProfile)}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&testnetProfile, "testnet", "", "config profile for the first network")
	cmd.Flags().StringVar(&mainnetProfile, "mainnet", "", "config profile for the second network")
	cmd.Flags().StringVar(&testnetContract, "testnet-contract", "", "contract id on the first network (default: the positional id)")
	cmd.Flags().StringVar(&mainnetContract, "mainnet-contract", "", "contract id on the second network (default: the positional id)")
	cmd.Flags().StringVar(&function, "fn", "", "read-only function to simulate on both networks")
	cmd.Flags().StringArrayVar(&callArgs, "arg", nil, "argument for --fn, in type:value form (repeatable)")
	cmd.Flags().StringVar(&callsPath, "calls", "", `JSON file of read-only calls to run on both networks ("-" for stdin)`)
	cmd.Flags().StringArrayVar(&dataKeys, "key", nil, "a contract data key whose TTL should be compared (repeatable)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "a file listing data keys to compare, one spec per line (blank lines and # comments ignored)")
	cmd.Flags().StringVar(&durability, "durability", "persistent", "durability for --key lookups: persistent or temporary")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	return cmd
}

// compareConfig builds one side's configuration from its profile, then applies
// the persistent flags the user set so both sides behave the same way.
func compareConfig(opts *options, cmd *cobra.Command, profile string) (config.Config, error) {
	cfg, err := config.LoadNetworkProfile(opts.configPath, profile)
	if err != nil {
		return config.Config{}, err
	}
	if err := opts.applyFlagOverrides(cmd, &cfg); err != nil {
		return config.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

// readCompareCalls resolves the calls to run from either a single --fn or a
// --calls file. No calls is a valid comparison: the Wasm and TTL report still
// stands on its own.
func readCompareCalls(stdin io.Reader, path, function string, args []string) ([]probe.CompareCall, error) {
	if path == "" {
		if function == "" {
			return nil, nil
		}
		return []probe.CompareCall{{Name: function, Function: function, Args: args}}, nil
	}

	r := stdin
	if path != "-" {
		f, err := os.Open(path) //nolint:gosec // --calls is an operator-supplied path; reading it is the feature.
		if err != nil {
			return nil, fmt.Errorf("read calls file %s: %w", path, err)
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	var file compareCallsFile
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("read calls file %s: %w", path, err)
	}

	if len(file.Calls) == 0 {
		return nil, fmt.Errorf("calls file %s contains no calls", path)
	}

	names := make(map[string]struct{}, len(file.Calls))
	for i, call := range file.Calls {
		if strings.TrimSpace(call.Function) == "" {
			return nil, fmt.Errorf("calls file %s: call %d names no function", path, i+1)
		}
		name := call.Name
		if name == "" {
			name = call.Function
		}
		if _, ok := names[name]; ok {
			if call.Name == "" {
				return nil, fmt.Errorf("calls file %s: function %q is called more than once; give each of those calls a name", path, call.Function)
			}
			return nil, fmt.Errorf("calls file %s: call name %q is used more than once", path, name)
		}
		names[name] = struct{}{}
	}
	return file.Calls, nil
}
