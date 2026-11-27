package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/probe"
)

// readSimulationReport reads a saved `simulate --json` report.
func readSimulationReport(path string) (*probe.SimulateResult, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator names the report to read; reading it is the feature.
	if err != nil {
		return nil, err
	}
	var r probe.SimulateResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s is not a simulate --json report: %w", path, err)
	}
	return &r, nil
}

func newDiffCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "diff <report-a.json> <report-b.json>",
		Short: "Compare the footprints and cost of two saved simulation reports",
		Long: `Compare two saved simulation reports (simulate --json output) and print the
ledger entries only one of them touches, entries that moved between read-only
and read-write, and the change in each cost figure.

Entries owned by the contract each report called are matched as "self", so a
report for one contract lines up with a report for a new version of it.
--json output is sorted and stable for CI.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := readSimulationReport(args[0])
			if err != nil {
				return err
			}
			b, err := readSimulationReport(args[1])
			if err != nil {
				return err
			}
			diff, err := probe.CompareSimulations(args[0], a, args[1], b)
			if err != nil {
				return err
			}
			if opts.jsonOut {
				return writeJSON(cmd.OutOrStdout(), diff)
			}
			return renderDiff(cmd.OutOrStdout(), diff)
		},
	}
}

func renderDiff(w io.Writer, d *probe.SimulationDiff) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s\n%s %s\n", s.bold("a"), d.A, s.bold("b"), d.B)
	if d.Same {
		bw.printf("\n%s\n", s.green("same footprint and cost"))
	}
	section := func(title string, entries []probe.FootprintEntry, mark string) {
		if len(entries) == 0 {
			return
		}
		bw.printf("\n%s\n", s.bold(title))
		for _, e := range entries {
			bw.printf("  %s %-10s %s\n", mark, e.Access, e.Label)
		}
	}
	section("only in a", d.OnlyInA, "-")
	section("only in b", d.OnlyInB, "+")
	if len(d.Moved) > 0 {
		bw.printf("\n%s\n", s.bold("access changed"))
		for _, m := range d.Moved {
			bw.printf("  ~ %s: %s -> %s\n", m.Entry.Label, m.From, m.To)
		}
	}
	bw.printf("\n%s\n", s.bold("resources (b - a)"))
	for _, f := range d.Resources {
		pct := ""
		if f.Percent != nil {
			pct = fmt.Sprintf(" (%+.1f%%)", *f.Percent)
		}
		bw.printf("  %-22s %12d -> %-12d %+d%s\n", f.Name, f.Baseline, f.Current, f.Delta, pct)
	}
	return bw.err
}

// renderTrace prints the call tree, one indented line per call.
func renderTrace(bw *bufWriter, s styler, frames []*probe.TraceFrame, depth int) {
	for _, f := range frames {
		pad := strings.Repeat("  ", depth+1)
		args := make([]string, len(f.Args))
		for i, a := range f.Args {
			text, _ := json.Marshal(a)
			if i < len(f.ArgNames) {
				args[i] = f.ArgNames[i] + ": " + string(text)
			} else {
				args[i] = string(text)
			}
		}
		bw.printf("%s%s %s(%s)\n", pad, s.cyan(f.Contract), f.Function, strings.Join(args, ", "))
		renderTrace(bw, s, f.Calls, depth+1)
		switch {
		case f.Error != "":
			bw.printf("%s  %s %s\n", pad, s.red("error"), f.Error)
		case f.Returned:
			text, _ := json.Marshal(f.Return)
			bw.printf("%s  %s %s\n", pad, s.dim("->"), text)
		}
	}
}

func newSweepCmd(opts *options) *cobra.Command {
	var argsFile, sizes string
	var templates []string
	var concurrency int

	cmd := &cobra.Command{
		Use:   "sweep <contract-id> <function> (--args-file samples.json | --template ARG... --sizes 1..N)",
		Short: "Simulate a function across argument samples and show how its cost scales",
		Long: `Simulate one function with many argument sets and print a table of the
resources each one uses, to see how cost scales before a user hits a limit
with a large input.

--args-file reads the samples: a JSON array whose items are either an array
of argument specs or {"label": "...", "args": [...]}. "-" reads stdin.

--template generates them instead, one --template per argument, for each size
in --sizes ("1,2,4,8", "1..64" or "8..512:8"). In a template, {n} becomes the
size and {repeat:SPEC} becomes SPEC repeated n times, so
  --template 'vec:[{repeat:"u32:7"}]' --sizes 1..32
sweeps vectors of one to thirty-two sevens.

Samples are simulated at most --concurrency at a time. Each is judged against
the network's per-transaction limits, and the output names the largest sample,
in the order given, that succeeds within them. Nothing is submitted.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			samples, err := sweepSamples(cmd, argsFile, templates, sizes)
			if err != nil {
				return err
			}
			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			res, err := p.Sweep(cmd.Context(), probe.SweepRequest{
				ContractID: args[0], Function: args[1], Samples: samples, Concurrency: concurrency,
			})
			if err != nil {
				return err
			}
			if opts.jsonOut {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			return renderSweep(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVar(&argsFile, "args-file", "", `JSON file of argument samples ("-" for stdin)`)
	cmd.Flags().StringArrayVar(&templates, "template", nil, "argument template, one per argument; {n} is the size and {repeat:SPEC} repeats SPEC n times")
	cmd.Flags().StringVar(&sizes, "sizes", "", `sizes for --template: "1,2,4", "1..64" or "8..512:8"`)
	cmd.Flags().IntVar(&concurrency, "concurrency", probe.DefaultSweepConcurrency, "simulations to run at once")
	return cmd
}

func sweepSamples(cmd *cobra.Command, argsFile string, templates []string, sizes string) ([]probe.SweepSample, error) {
	switch {
	case argsFile != "" && len(templates) > 0:
		return nil, fmt.Errorf("give --args-file or --template, not both")
	case argsFile != "":
		var raw []byte
		var err error
		if argsFile == "-" {
			raw, err = io.ReadAll(cmd.InOrStdin())
		} else {
			raw, err = os.ReadFile(argsFile) //nolint:gosec // the operator names the samples file; reading it is the feature.
		}
		if err != nil {
			return nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("%s: want a JSON array of samples: %w", argsFile, err)
		}
		out := make([]probe.SweepSample, 0, len(items))
		for i, item := range items {
			var s probe.SweepSample
			if err := json.Unmarshal(item, &s.Args); err != nil {
				if err := json.Unmarshal(item, &s); err != nil {
					return nil, fmt.Errorf("%s: sample %d is neither an array of arguments nor {label, args}", argsFile, i+1)
				}
			}
			out = append(out, s)
		}
		return out, nil
	case len(templates) > 0:
		ns, err := probe.ParseSizes(sizes)
		if err != nil {
			return nil, err
		}
		out := make([]probe.SweepSample, 0, len(ns))
		for _, n := range ns {
			args, err := probe.ExpandTemplate(templates, n)
			if err != nil {
				return nil, err
			}
			out = append(out, probe.SweepSample{Label: fmt.Sprintf("n=%d", n), Args: args})
		}
		return out, nil
	default:
		return nil, fmt.Errorf("give --args-file, or --template with --sizes")
	}
}

func renderSweep(w io.Writer, r *probe.SweepResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s  %s %s\n\n", s.bold("contract"), r.ContractID, s.bold("function"), r.Function)
	bw.printf("  %-12s %-8s %14s %12s %12s %10s %14s  %s\n", "sample", "result", "instructions", "read bytes", "write bytes", "entries", "resource fee", "limits")
	for _, row := range r.Rows {
		result := s.green("ok")
		if !row.Success {
			result = s.red("failed")
		}
		limits := "unknown"
		if row.WithinLimits != nil {
			limits = "within"
			if !*row.WithinLimits {
				limits = "over " + strings.Join(row.OverLimits, ", ")
				if !row.Success {
					limits = "-"
				}
			}
		}
		bw.printf("  %-12s %-8s %14d %12d %12d %10d %14d  %s\n", row.Label, result,
			row.Cost.Instructions, row.Cost.DiskReadBytes, row.Cost.WriteBytes, row.FootprintEntries, row.Cost.ResourceFee, limits)
		if row.Error != "" && !row.Success {
			first, _, _ := strings.Cut(row.Error, "\n")
			bw.printf("  %-12s %s\n", "", s.dim(first))
		}
	}
	bw.printf("\n")
	if r.LargestWithinLimits != "" {
		bw.printf("%s %s\n", s.bold("largest sample within network limits:"), r.LargestWithinLimits)
	} else if r.Limits != nil {
		bw.printf("%s\n", s.red("no sample stays within the network limits"))
	}
	if r.Note != "" {
		bw.printf("%s\n", s.dim("note: "+r.Note))
	}
	return bw.err
}
