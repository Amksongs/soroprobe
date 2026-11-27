// Command gen-man generates man pages from the soroprobe CLI help text.
// It is a development helper, not a released binary. The Makefile calls it
// through `make man`, and `.goreleaser.yaml` bundles the generated pages with
// each release archive. Generated pages are not committed to the repository;
// regenerate them whenever the CLI help text changes and include the new files
// in the next release.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"
)

func main() {
	usage := `usage: gen-man
`
	if len(os.Args) > 1 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	root := &cobra.Command{
		Use:   "soroprobe",
		Short: "Health and simulation checker for Stellar/Soroban smart contracts",
		Long: `SoroProbe dry-runs Soroban contract calls and reports on contract state health.

It simulates invocations to show whether they would succeed, what they would
return, and what they would cost, and it reads contract state to flag entries
that are close to expiring.

SoroProbe is read-only. It never submits a transaction and never needs a secret
key: simulation only requires a source account public key to build a
well-formed transaction, and that account need not exist or hold a balance.`,
	}

	out := &strings.Builder{}
	if err := doc.GenManTreeFromOpts(root, doc.GenManTreeOptions{Path: out.String()}); err != nil {
		fmt.Fprintln(os.Stderr, "gen-man:", err)
		os.Exit(1)
	}
	fmt.Println("generated man pages in docs/man")
}
