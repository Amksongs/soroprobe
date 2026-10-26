package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// Exit codes. `check` distinguishes an unhealthy contract from a tool failure
// so CI can tell "your contract has a problem" from "SoroProbe could not run".
const (
	exitOK      = 0
	exitFailed  = 1
	exitRuntime = 2
)

// proberFactory builds a Prober from resolved configuration, along with a
// cleanup func that releases the resources behind it. Tests replace it with a
// fixture-backed factory so commands run end to end without a network.
type proberFactory func(config.Config) (*probe.Prober, func(), error)

// options holds everything the persistent flags can set.
type options struct {
	cfg        config.Config
	configPath string
	jsonOut    bool
	// newProber builds the Prober commands run against. A nil factory means
	// the live one, so a zero-value options still behaves like production.
	newProber proberFactory
}

func newRootCmd() (*cobra.Command, *options) {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "soroprobe",
		Short: "Health and simulation checker for Stellar/Soroban smart contracts",
		Long: `SoroProbe dry-runs Soroban contract calls and reports on contract state health.

It simulates invocations to show whether they would succeed, what they would
return, and what they would cost, and it reads contract state to flag entries
that are close to expiring.

SoroProbe is read-only. It never submits a transaction and never needs a secret
key: simulation only requires a source account public key to build a
well-formed transaction, and that account need not exist or hold a balance.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return opts.resolve(cmd)
		},
	}

	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.configPath, "config", "", "path to a JSON config file")
	flags.String("rpc-url", "", fmt.Sprintf("Stellar RPC endpoint (env %s) (default %q)", config.EnvRPCURL, config.DefaultRPCURL))
	flags.String("network-passphrase", "", fmt.Sprintf("network passphrase (env %s)", config.EnvNetworkPassphrase))
	flags.String("source-account", "", fmt.Sprintf("public key used to build transactions for simulation (env %s)", config.EnvSourceAccount))
	flags.String("log-level", "", fmt.Sprintf("debug, info, warn or error (env %s)", config.EnvLogLevel))
	flags.Duration("timeout", 0, fmt.Sprintf("per-request RPC timeout (env %s)", config.EnvTimeout))
	flags.Uint32("warn-ledgers", 0, "flag entries with fewer than this many ledgers of TTL left")
	flags.Uint32("critical-ledgers", 0, "treat entries with fewer than this many ledgers of TTL left as critical")
	flags.String("sorovault-url", "", fmt.Sprintf("SoroVault registry used to type simulate arguments from the contract's interface (env %s)", config.EnvSoroVaultURL))
	flags.BoolVar(&opts.jsonOut, "json", false, "emit JSON instead of human-readable output")

	cmd.AddCommand(
		newSimulateCmd(opts),
		newInspectCmd(opts),
		newCheckCmd(opts),
		newEventsCmd(opts),
		newServeCmd(opts),
		newVersionCmd(),
	)
	return cmd, opts
}

// resolve loads config from file and environment, then applies any flags the
// user actually set, so that flags win over the environment.
func (o *options) resolve(cmd *cobra.Command) error {
	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}

	flags := cmd.Flags()
	if flags.Changed("rpc-url") {
		cfg.RPCURL, _ = flags.GetString("rpc-url")
	}
	if flags.Changed("network-passphrase") {
		cfg.NetworkPassphrase, _ = flags.GetString("network-passphrase")
	}
	if flags.Changed("source-account") {
		cfg.SourceAccount, _ = flags.GetString("source-account")
	}
	if flags.Changed("log-level") {
		cfg.LogLevel, _ = flags.GetString("log-level")
	}
	if flags.Changed("timeout") {
		cfg.Timeout, _ = flags.GetDuration("timeout")
	}
	if flags.Changed("warn-ledgers") {
		cfg.WarnLedgers, _ = flags.GetUint32("warn-ledgers")
	}
	if flags.Changed("critical-ledgers") {
		cfg.CriticalLedgers, _ = flags.GetUint32("critical-ledgers")
	}
	if flags.Changed("sorovault-url") {
		cfg.SoroVaultURL, _ = flags.GetString("sorovault-url")
	}

	if err := cfg.Validate(); err != nil {
		return err
	}
	o.cfg = cfg
	return nil
}

// prober builds a Prober from the resolved configuration. Tests inject a
// fixture-backed factory through options.newProber; the default is the live
// RPC-backed one.
func (o *options) prober() (*probe.Prober, func(), error) {
	if o.newProber != nil {
		return o.newProber(o.cfg)
	}
	return newLiveProber(o.cfg)
}

// newLiveProber builds a Prober over a live Stellar RPC client.
func newLiveProber(cfg config.Config) (*probe.Prober, func(), error) {
	log := cfg.Logger(os.Stderr)

	client, err := stellar.New(stellar.Options{
		URL:     cfg.RPCURL,
		Timeout: cfg.Timeout,
		Logger:  log,
	})
	if err != nil {
		return nil, nil, err
	}

	var source abi.Source
	if cfg.SoroVaultURL != "" {
		vault, err := abi.NewSoroVault(cfg.SoroVaultURL, nil, cfg.Timeout)
		if err != nil {
			_ = client.Close()
			return nil, nil, err
		}
		source = vault
	}

	p, err := probe.New(probe.Options{
		Client:        client,
		SourceAccount: cfg.SourceAccount,
		Thresholds: health.Thresholds{
			Warn:     cfg.WarnLedgers,
			Critical: cfg.CriticalLedgers,
		},
		Logger:  log,
		ABI:     source,
		Network: abi.NetworkName(cfg.NetworkPassphrase),
	})
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	// client.Close waits for in-flight requests, and a check that hit its
	// per-check timeout leaves its RPC call running until the transport's own
	// timeout (RPC_TIMEOUT, 30s by default) expires. Waiting that out would
	// undo the point of the limit, so close within a grace period and then
	// leave: the call is abandoned and its reply is never read.
	const closeGrace = 2 * time.Second
	return p, func() {
		done := make(chan struct{})
		go func() {
			_ = client.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(closeGrace):
		}
	}, nil
}
