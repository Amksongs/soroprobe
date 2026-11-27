package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// compatReport is the result of a compat run, and the shape of --json.
type compatReport struct {
	Old string `json:"old"`
	New string `json:"new"`
	// Breaking is true when any change is breaking.
	Breaking bool         `json:"breaking"`
	Changes  []abi.Change `json:"changes"`
}

func newCompatCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "compat <old: contract-id | wasm file> <new: wasm file>",
		Short: "Compare two contract interfaces and fail on a breaking change",
		Long: `Compare the interface of a deployed contract, or of an older Wasm build, with a
new Wasm build, and report every difference between them.

The first argument is a contract ID (its Wasm is read from the ledger) or the
path of a .wasm file. The second is the path of the new .wasm file. Comparing
two files needs no network.

Breaking changes: a removed function, a changed argument count or argument
type, a changed return type, a struct field that is removed, retyped or added,
a removed enum or union case, and an enum value or union payload that changed.
Not breaking: an added function, a renamed argument, an added enum or union
case.

User-defined types are compared by structure, not by name, and struct fields
are compared by name, not position.

Exits 0 when nothing breaks, 1 when any change is breaking, and 2 when
SoroProbe itself could not run. --json prints the changes as JSON.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			oldIface, err := loadInterface(cmd, opts, args[0], true)
			if err != nil {
				return fmt.Errorf("old interface: %w", err)
			}
			newIface, err := loadInterface(cmd, opts, args[1], false)
			if err != nil {
				return fmt.Errorf("new interface: %w", err)
			}

			changes := abi.Compat(oldIface, newIface)
			if changes == nil {
				changes = []abi.Change{}
			}
			report := compatReport{Old: args[0], New: args[1], Breaking: abi.HasBreaking(changes), Changes: changes}

			if opts.jsonOut {
				if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else if err := renderCompat(cmd.OutOrStdout(), report); err != nil {
				return err
			}
			if report.Breaking {
				return &checkFailedError{msg: fmt.Sprintf("breaking changes between %s and %s", args[0], args[1])}
			}
			return nil
		},
	}
}

// loadInterface reads an interface from a Wasm file or, when allowChain is
// set and the argument is a contract ID, from the ledger. A file wins: a
// contract ID is never a path that exists, so there is no ambiguity.
func loadInterface(cmd *cobra.Command, opts *options, arg string, allowChain bool) (*abi.Interface, error) {
	if info, err := os.Stat(arg); err == nil && info.Mode().IsRegular() {
		wasm, err := os.ReadFile(arg) //nolint:gosec // G304: the path is the operator's own argument
		if err != nil {
			return nil, err
		}
		iface, err := abi.ParseWasm(wasm)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", arg, err)
		}
		return iface, nil
	}

	if !allowChain {
		return nil, fmt.Errorf("%q is not a Wasm file that exists", arg)
	}
	if _, err := stellar.ParseContractID(arg); err != nil {
		return nil, fmt.Errorf("%q is neither a Wasm file that exists nor a contract ID", arg)
	}

	p, cleanup, err := opts.prober()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return p.Interface(cmd.Context(), arg)
}

func renderCompat(w io.Writer, r compatReport) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s -> %s\n\n", s.bold("compat"), r.Old, r.New)
	if len(r.Changes) == 0 {
		bw.printf("  no differences\n")
	}
	breaking := 0
	for _, c := range r.Changes {
		label := s.dim("ok      ")
		if c.Breaking {
			breaking++
			label = s.red("BREAKING")
		}
		bw.printf("  %s  %-24s %s\n", label, c.Subject, c.Detail)
	}

	bw.printf("\n%s ", s.bold("result"))
	if breaking > 0 {
		bw.printf("%s\n", s.red(fmt.Sprintf("FAIL  %d breaking, %d non-breaking", breaking, len(r.Changes)-breaking)))
	} else {
		bw.printf("%s\n", s.green(fmt.Sprintf("PASS  no breaking changes, %d non-breaking", len(r.Changes))))
	}
	return bw.err
}
