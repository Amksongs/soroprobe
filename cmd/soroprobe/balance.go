package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/probe"
)

func newBalanceCmd(opts *options) *cobra.Command {
	var file string

	cmd := &cobra.Command{
		Use:   "balance <token> [address...]",
		Short: "Read a token's balances for one or many holders",
		Long: `Read the balances of one or many holders of a token, formatted with the
token's decimals and symbol.

<token> is a SEP-41 token or a Stellar Asset Contract. Each address is an
account ("G...") or a contract ("C..."). --file reads more addresses from a
file, one per line, ignoring blank lines and lines starting with # ("-" for
stdin); they are read in addition to any given as arguments.

The balances are read by simulating the token's own decimals, symbol and
balance functions. Nothing is submitted, and no key is needed.

An address that cannot be read is reported next to the ones that can, and
makes the command exit 1. --json prints the balances as JSON, with the raw
integer alongside the formatted figure.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			holders := args[1:]
			if file != "" {
				fromFile, err := readHolders(cmd, file)
				if err != nil {
					return err
				}
				holders = append(holders, fromFile...)
			}
			if len(holders) == 0 {
				return fmt.Errorf("give at least one address, or --file")
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Balance(cmd.Context(), probe.BalanceRequest{Token: args[0], Holders: holders})
			if err != nil {
				return err
			}

			if opts.jsonOut {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = renderBalance(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			for _, b := range result.Balances {
				if b.Error != "" {
					return &checkFailedError{msg: "one or more balances could not be read"}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", `read holder addresses from this file, one per line ("-" for stdin)`)
	return cmd
}

// readHolders reads one address per line.
func readHolders(cmd *cobra.Command, path string) ([]string, error) {
	r := cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path) //nolint:gosec // --file is an operator-supplied path; reading it is the feature.
		if err != nil {
			return nil, err
		}
		defer f.Close() //nolint:errcheck // read-only input; a close error cannot lose data.
		r = f
	}
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read holders file %s: %w", path, err)
	}
	return out, nil
}

func renderBalance(w io.Writer, r *probe.BalanceResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	label := r.Symbol
	if label == "" {
		label = "(no symbol)"
	}
	bw.printf("%s %s  %s, %d decimals\n\n", s.bold("token"), r.Token, label, r.Decimals)
	for _, b := range r.Balances {
		if b.Error != "" {
			bw.printf("  %s  %s\n", s.red("ERROR"), b.Address+": "+b.Error)
			continue
		}
		bw.printf("  %s %s\n", b.Address, strings.TrimSpace(b.Balance+" "+r.Symbol))
	}
	return bw.err
}
