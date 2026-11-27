package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/integration/soroforge"
	"github.com/soroworks/soroprobe/internal/probe"
)

// runFromSoroForge reads the contracts a SoroForge registry tracks and checks
// each one, comparing the recorded Wasm hash with the live one so a contract
// that was replaced outside SoroForge is reported as drift rather than passing
// quietly.
//
// Everything up to the check itself is a tool failure (exit 2): an unreachable
// registry, a rejected token, a broken page of results or an empty contract list
// all mean no check ran, and a pipeline must be able to tell that apart from a
// contract that failed its checks.
func runFromSoroForge(cmd *cobra.Command, opts *options, rawURL, token, network string,
	checkTimeout time.Duration, concurrency int, report *reportFile, quiet bool, format string) error {
	if token == "" {
		token = opts.cfg.SoroForgeToken
	}
	client, err := soroforge.New(soroforge.Options{
		URL:     rawURL,
		Token:   token,
		Timeout: opts.cfg.Timeout,
	})
	if err != nil {
		return err
	}

	if network == "" {
		network = abi.NetworkName(opts.cfg.NetworkPassphrase)
	}
	contracts, err := client.Contracts(cmd.Context(), network)
	if err != nil {
		return err
	}
	if len(contracts) == 0 {
		return fmt.Errorf("SoroForge tracks no contracts on %q; check --network or the registry contents", network)
	}

	req := probe.BatchRequest{Concurrency: concurrency}
	req.Checks = make([]probe.NamedCheck, 0, len(contracts))
	for _, contract := range contracts {
		name := contract.Alias
		if name == "" {
			name = contract.ContractID
		}
		req.Checks = append(req.Checks, probe.NamedCheck{
			Name: name,
			CheckRequest: probe.CheckRequest{
				ContractID:       contract.ContractID,
				ExpectedWASMHash: contract.WASMHash,
			},
		})
	}
	applyDefaultTimeout(&req, checkTimeout)
	return runCheckBatch(cmd, opts, req, report, quiet, format)
}
