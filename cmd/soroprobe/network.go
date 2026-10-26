package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

// redactURL removes userinfo (username/password) and query strings from a URL.
// If the URL cannot be parsed or has no scheme/host, it is returned as-is.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func newNetworkCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "network",
		Short: "Print RPC endpoint health and network details",
		Long: `Query the configured Stellar RPC endpoint for health status and network
details (latest ledger, protocol version, and network passphrase), and verify
that the passphrase matches the configured network.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			res, err := p.Network(cmd.Context(), opts.cfg.RPCURL, opts.cfg.NetworkPassphrase)
			if err != nil {
				return err
			}

			// Redact URL for display/JSON
			displayRes := *res
			displayRes.RPCURL = redactURL(res.RPCURL)

			if opts.jsonOut {
				if err := writeJSON(cmd.OutOrStdout(), displayRes); err != nil {
					return err
				}
			} else {
				if err := renderNetwork(cmd.OutOrStdout(), res, opts.cfg.NetworkPassphrase); err != nil {
					return err
				}
			}

			if res.Status != "healthy" {
				return &checkFailedError{msg: fmt.Sprintf("rpc is unhealthy: status %q", res.Status)}
			}
			if !res.PassphraseMatch {
				return &checkFailedError{msg: fmt.Sprintf("network mismatch: rpc serves %q, configured %q", res.NetworkPassphrase, opts.cfg.NetworkPassphrase)}
			}

			return nil
		},
	}
}
