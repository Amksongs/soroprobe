package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/soroworks/soroprobe/internal/api"
	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = ""

const argHelp = `Arguments use a "type:value" form. Supported types:

  bool:true          void                 null
  u32:5              i32:-5               u64:100        i64:-100
  u128:10000000      i128:-10000000       u256:...       i256:...
  timepoint:1700000000                    duration:3600
  sym:transfer       str:"hello world"    bytes:deadbeef
  addr:GABC...       addr:CDEF...
  'vec:["u32:1","u32:2"]'                 'map:[["sym:a","u32:1"]]'

Collections are JSON; each string element is itself an argument spec, and a
JSON number, true, false or null is a bare literal. Write map keys in
ascending order: Soroban rejects an unsorted map.

A token amount can be written as a decimal with the token's decimal count
after an "@": i128:10.5@7 is 10.5 tokens of a 7-decimal token, encoded as
105000000. The conversion is exact, and an amount with more fractional digits
than the token has decimals is rejected rather than rounded.

A bare value with no prefix is inferred: "true" and "false" become bool, "void"
and "null" become void, a valid G... or C... address becomes an address, a run
of digits becomes i128, and anything else becomes a symbol if it fits Soroban's
symbol rules or a string otherwise.

Prefer explicit prefixes. A contract expecting u32 will fail confusingly if it
is handed an inferred i128.`

func newSimulateCmd(opts *options) *cobra.Command {
	var reportPath string
	var file string
	var baselinePath string
	var maxIncrease string
	var repeat int
	var maxSpread string

	cmd := &cobra.Command{
		Use:   "simulate <contract-id> <function> [args...] | simulate --file <calls.json>",
		Short: "Simulate a contract invocation and report its result and cost",
		Long: `Build a contract invocation, simulate it against the network, and report
whether it would succeed, what it would return, and what it would cost.

Nothing is submitted and no signing takes place.

--file reads named calls from a JSON file ("-" for stdin). Each call contains
name, contract_id, function and optional args. Calls run sequentially and
individual errors do not stop later calls.

--baseline compares the cost against a saved report — a previous --json
output — and prints each figure with its change in absolute and percentage
terms. The baseline must be for the same contract and function; a report for
a different call is rejected rather than compared.

--max-increase sets the largest increase in any cost figure that is
tolerated, as a percentage ("10%"). When a figure grows by more than that,
the command exits 1.

--repeat runs the same simulation N times and reports the spread between the
runs, so a noisy estimate is told apart from a stable one. --max-spread sets
the largest spread, as a percentage of the minimum, that is tolerated before
the run is flagged as noisy and the command exits 1. When --baseline is also
given, the comparison uses the maximum of the repeated runs, so a flaky
estimate cannot make a cost check pass by luck.

` + argHelp,
		Args: func(cmd *cobra.Command, args []string) error {
			if file != "" {
				if len(args) > 0 {
					return fmt.Errorf("give a contract call or --file, not both")
				}
				if baselinePath != "" {
					return fmt.Errorf("--baseline compares one call, so it cannot be used with --file")
				}
				if maxIncrease != "" {
					return fmt.Errorf("--max-increase compares one call, so it cannot be used with --file")
				}
				if repeat > 1 {
					return fmt.Errorf("--repeat simulates one call, so it cannot be used with --file")
				}
				if maxSpread != "" {
					return fmt.Errorf("--max-spread judges repeated runs, so it cannot be used with --file")
				}
				return nil
			}
			return cobra.MinimumNArgs(2)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, report.close()) }()

			if file != "" {
				return runSimulateFile(cmd, opts, file, report)
			}

			baseline, err := readBaseline(baselinePath)
			if err != nil {
				return err
			}
			threshold, err := parseMaxIncrease(maxIncrease, baseline != nil)
			if err != nil {
				return err
			}
			spread, err := parseMaxSpread(maxSpread, repeat)
			if err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Simulate(cmd.Context(), probe.SimulateRequest{
				ContractID:         args[0],
				Function:           args[1],
				Args:               args[2:],
				Baseline:           baseline,
				MaxIncreasePercent: threshold,
				Repeat:             repeat,
				MaxSpreadPercent:   spread,
			})
			if err != nil {
				return err
			}

			if err := report.write(result); err != nil {
				return err
			}

			if opts.jsonOut {
				err = writeJSON(cmd.OutOrStdout(), result)
			} else {
				err = renderSimulate(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}

			// The report is written before the verdict, so a run that trips
			// the threshold still leaves its numbers behind.
			if result.Comparison != nil && result.Comparison.Exceeded {
				return &checkFailedError{msg: fmt.Sprintf(
					"the cost grew by more than %s", formatPercent(*result.Comparison.MaxIncreasePercent))}
			}
			// A noisy estimate is a failed measurement, not a failed call:
			// its own exit keeps it apart from a check failure.
			if result.Spread != nil && result.Spread.Noisy {
				return &checkFailedError{msg: fmt.Sprintf(
					"the cost varied by more than %s across %d runs",
					formatPercent(*result.Spread.MaxSpreadPercent), result.Spread.Runs)}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	cmd.Flags().StringVarP(&file, "file", "f", "", `run calls listed in a JSON file ("-" for stdin)`)
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "compare the cost against a saved simulation report (a previous --json output)")
	cmd.Flags().StringVar(&maxIncrease, "max-increase", "", `fail when a cost figure grows by more than this percentage (e.g. "10%")`)
	cmd.Flags().IntVar(&repeat, "repeat", 1, "simulate the call this many times and report the spread between runs")
	cmd.Flags().StringVar(&maxSpread, "max-spread", "", `fail when the spread across repeated runs exceeds this percentage of the minimum (e.g. "5%")`)
	return cmd
}

// readBaseline reads a saved simulation report to compare a cost against.
//
// The file is a previous --json output, so it holds more fields than the
// baseline needs; the rest are ignored rather than rejected, because the
// report is a document SoroProbe wrote and not a form the user fills in.
func readBaseline(path string) (*probe.Baseline, error) {
	if path == "" {
		return nil, nil
	}

	raw, err := os.ReadFile(path) //nolint:gosec // --baseline is an operator-supplied path; reading it is the feature.
	if err != nil {
		return nil, fmt.Errorf("read baseline %s: %w", path, err)
	}

	var baseline probe.Baseline
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return nil, fmt.Errorf("parse baseline %s: %w", path, err)
	}
	return &baseline, nil
}

// parseMaxIncrease parses the --max-increase threshold.
//
// The percentage sign is optional, so "10" and "10%" mean the same thing, and
// the threshold is only meaningful alongside a baseline.
func parseMaxIncrease(s string, hasBaseline bool) (*float64, error) {
	if s == "" {
		return nil, nil
	}
	if !hasBaseline {
		return nil, fmt.Errorf("--max-increase needs --baseline, which is what it compares against")
	}

	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return nil, fmt.Errorf("expected a percentage, got %q", s)
	}
	if value < 0 {
		return nil, fmt.Errorf("a percentage cannot be negative, got %q", s)
	}
	return &value, nil
}

// formatPercent renders a percentage threshold.
func formatPercent(v float64) string {
	return fmt.Sprintf("%g%%", v)
}

// parseMaxSpread parses the --max-spread threshold.
//
// Like --max-increase the percentage sign is optional, but unlike it a spread
// threshold needs repeated runs: with one run there is no spread to judge.
// An empty string means "do not judge", leaving the spread reported but not
// flagged, which is what a bare --repeat asks for.
func parseMaxSpread(s string, repeat int) (*float64, error) {
	if s == "" {
		return nil, nil
	}
	if repeat < 2 {
		return nil, fmt.Errorf("--max-spread needs --repeat of at least 2, which is what it judges")
	}

	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return nil, fmt.Errorf("expected a percentage, got %q", s)
	}
	if value < 0 {
		return nil, fmt.Errorf("a percentage cannot be negative, got %q", s)
	}
	return &value, nil
}

func newInspectCmd(opts *options) *cobra.Command {
	var dataKeys []string
	var keyFile string
	var durability string
	var reportPath string
	var format string
	var estimateExtend uint32

	cmd := &cobra.Command{
		Use:   "inspect <contract-id>",
		Short: "Read a contract's state entries and report expiration health",
		Long: `Read a contract's instance and code entries, and report how close each is to
expiring.

Stellar RPC reads ledger entries by key and cannot enumerate a contract's data
entries, so data entries must be named explicitly with --key. Listing every
entry a contract owns requires an indexer, which SoroProbe deliberately does not
depend on.

--key-file reads the same specs from a file, one per line, for contracts with
too many entries for a command line. Keys from the file and from --key are
combined; an invalid line is reported with its line number before any request
is sent.

--estimate-extend prices keeping the contract's entries alive: it simulates
an extension of each reported entry and reports what it would cost. Nothing
is submitted, and an entry that cannot be extended — because it is missing or
has already expired — says so instead of quoting a price.

` + argHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			outputFormat, err := selectOutputFormat(cmd, opts, format)
			if err != nil {
				return err
			}

			d, err := parseDurability(durability)
			if err != nil {
				return err
			}

			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, report.close()) }()

			keys, err := mergeKeys(keyFile, dataKeys)
			if err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Inspect(cmd.Context(), probe.InspectRequest{
				ContractID:     args[0],
				DataKeys:       keys,
				DataDurability: d,
				EstimateExtend: estimateExtend,
			})
			if err != nil {
				return err
			}

			if err := report.write(result); err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return writeJSON(cmd.OutOrStdout(), result)
			case "markdown":
				return renderMarkdownInspect(cmd.OutOrStdout(), result)
			default:
				return renderInspect(cmd.OutOrStdout(), result)
			}
		},
	}

	cmd.Flags().StringArrayVar(&dataKeys, "key", nil, "a contract data key to inspect, in type:value form (repeatable)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "a file listing data keys to inspect, one spec per line (blank lines and # comments ignored)")
	cmd.Flags().StringVar(&durability, "durability", "persistent", "durability for --key lookups: persistent or temporary")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json or markdown")
	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	cmd.Flags().Uint32Var(&estimateExtend, "estimate-extend", 0,
		"also price extending each reported entry's TTL by this many ledgers")
	return cmd
}

func newCheckCmd(opts *options) *cobra.Command {
	var function string
	var callArgs []string
	var expect string
	var dataKeys []string
	var keyFile string
	var durability string
	var file string
	var checkTimeout time.Duration
	var concurrency int
	var reportPath string
	var format string
	var quiet bool

	cmd := &cobra.Command{
		Use:   "check <contract-id> | check --file <checks.json>",
		Short: "Run a combined health check, exiting non-zero on failure",
		Long: `Run a combined health check against a contract: confirm it is deployed, that
its instance and code entries are live and not near expiration, and optionally
that a read-only call simulates successfully.

Exits 0 when every check passes, 1 when any check fails, and 2 when SoroProbe
itself could not run. A TTL warning does not fail the check; it is a signal to
extend the entry, not a reason to break a pipeline.

--fn is optional. SoroProbe cannot discover a contract's exported functions, and
guessing one would produce a misleading failure, so the simulation step is
reported as skipped unless a function is named.

--key and --key-file name data entries for the TTL check. --key-file reads one
spec per line from a file, ignoring blank lines and lines starting with #, and
combines them with --key. It applies to the single-contract form; --file
entries carry their own data_keys.

--file runs many checks at once, from a JSON file ("-" for stdin):

  {"checks": [
    {"name": "token", "contract_id": "C...", "function": "decimals"},
    {"name": "vault", "contract_id": "C...", "data_keys": ["sym:Admin"]}
  ]}

Each entry takes name, contract_id, function, args, data_keys, data_durability
and timeout — the same body POST /v1/checks accepts. timeout bounds a single
check, as a Go duration string ("45s"); a check that outlives it is reported as
one that could not run, and the batch continues. --check-timeout supplies the
default for entries that name none. With --file, the exit code is 2 if any
check could not run, else 1 if any failed, else 0.

--report writes the JSON result to a file while the text report still goes to
the terminal, so a CI run can keep readable logs and a machine-readable
artifact. The file is written even when the check fails.

--quiet prints no report at all: success and check failure are told apart by
the exit code alone. Errors that make SoroProbe itself fail (exit 2) still
reach stderr, which is the point — redirecting stdout is what used to hide
them. --quiet cannot be combined with --json or --report.

Inside GitHub Actions (GITHUB_ACTIONS is set) each failed or warned step also
prints an ::error:: or ::warning:: line, which surfaces on the run summary
instead of hiding in the log. --json and --quiet suppress them.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if file != "" {
				if len(args) > 0 {
					return fmt.Errorf("give a contract ID or --file, not both")
				}
				if keyFile != "" {
					return fmt.Errorf("give --key-file with a contract ID, not with --file")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if err := checkQuietConflicts(quiet, opts.jsonOut, reportPath); err != nil {
				return err
			}
			outputFormat, err := selectOutputFormat(cmd, opts, format)
			if err != nil {
				return err
			}

			report, err := openReport(reportPath)
			if err != nil {
				return err
			}
			defer func() { err = errors.Join(err, report.close()) }()

			if file != "" {
				return runCheckFile(cmd, opts, file, checkTimeout, concurrency, report, quiet, outputFormat)
			}

			d, err := parseDurability(durability)
			if err != nil {
				return err
			}

			keys, err := mergeKeys(keyFile, dataKeys)
			if err != nil {
				return err
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			result, err := p.Check(cmd.Context(), probe.CheckRequest{
				ContractID:     args[0],
				Function:       function,
				Args:           callArgs,
				Expect:         expect,
				DataKeys:       keys,
				DataDurability: d,
			})
			if err != nil {
				return err
			}

			// Write the report before deciding the exit code, so a failed
			// check still leaves its report behind for the CI artifact.
			if err := report.write(result); err != nil {
				return err
			}

			if err := emitSingleCheckOutput(cmd, result, quiet, outputFormat); err != nil {
				return err
			}

			if !result.OK {
				return &checkFailedError{msg: fmt.Sprintf("check failed for %s", args[0])}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&function, "fn", "", "read-only function to simulate as a liveness probe")
	cmd.Flags().StringArrayVar(&callArgs, "arg", nil, "argument for --fn, in type:value form (repeatable)")
	cmd.Flags().StringVar(&expect, "expect", "", "expected return value specification for --fn, in type:value form")
	cmd.Flags().StringArrayVar(&dataKeys, "key", nil, "a contract data key to include in the TTL check (repeatable)")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "a file listing data keys for the TTL check, one spec per line (blank lines and # comments ignored)")
	cmd.Flags().StringVar(&durability, "durability", "persistent", "durability for --key lookups: persistent or temporary")
	cmd.Flags().StringVarP(&file, "file", "f", "", `run every check listed in this JSON file ("-" for stdin)`)
	cmd.Flags().DurationVar(&checkTimeout, "check-timeout", 0,
		`default time limit for each check in --file; a per-entry "timeout" wins (0 means no limit)`)
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json or markdown")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "maximum number of --file checks to run at once")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false,
		"print no report; the exit code is the whole answer (errors still reach stderr)")
	cmd.Flags().StringVar(&reportPath, "report", "", "write the JSON result to this file while printing text as usual")
	return cmd
}

func selectOutputFormat(cmd *cobra.Command, opts *options, format string) (string, error) {
	if opts.jsonOut && !cmd.Flags().Changed("format") {
		return "json", nil
	}
	switch format {
	case "text", "json", "markdown":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported output format %q (want text, json or markdown)", format)
	}
}

// runCheckFile runs a batch of checks read from path.
func runCheckFile(cmd *cobra.Command, opts *options, path string, checkTimeout time.Duration, concurrency int, report *reportFile, quiet bool, format string) error {
	req, err := readBatch(cmd.InOrStdin(), path)
	if err != nil {
		return err
	}
	applyDefaultTimeout(&req, checkTimeout)
	req.Concurrency = concurrency

	p, cleanup, err := opts.prober()
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := p.CheckBatch(cmd.Context(), req)
	if err != nil {
		return err
	}

	// Write the report before deciding the exit code, so a failed batch still
	// leaves its report behind for the CI artifact.
	if err := report.write(result); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if !quiet {
		switch format {
		case "json":
			err = writeJSON(out, result)
		case "markdown":
			err = renderMarkdownBatch(out, result)
		default:
			err = renderBatch(out, result)
		}
		if err != nil {
			return err
		}

		// Annotations go to stdout, the stream a step's log reads. They stay
		// off for JSON and Markdown, whose consumers parse or render the
		// document.
		if format == "text" {
			emitBatchAnnotations(out, result, inGitHubActions())
		}
	}

	switch {
	case result.Errored:
		// Returned as a plain error, so main exits with exitRuntime: the
		// batch is incomplete and its verdict cannot be trusted.
		return fmt.Errorf("one or more checks could not be run")
	case !result.OK:
		return &checkFailedError{msg: "one or more checks failed"}
	}
	return nil
}

// emitSingleCheckOutput writes the text or JSON report for a single contract,
// unless --quiet asked for none. Annotations are only emitted when a text
// report was printed: --json consumers parse the document, and --quiet asked
// for silence.
func emitSingleCheckOutput(cmd *cobra.Command, result *probe.CheckResult, quiet bool, format string) error {
	if quiet {
		return nil
	}
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		return writeJSON(out, result)
	case "markdown":
		return renderMarkdownCheck(out, result)
	}
	if err := renderCheck(out, result); err != nil {
		return err
	}
	// Annotations go to stdout, the stream a step's log reads.
	emitCheckAnnotations(out, result, inGitHubActions())
	return nil
}

// checkQuietConflicts rejects flag combinations that contradict each other,
// rather than silently letting one win. Exit-2 errors are printed by main, so
// --quiet cannot swallow them.
func checkQuietConflicts(quiet, jsonOut bool, reportPath string) error {
	if !quiet {
		return nil
	}
	switch {
	case jsonOut:
		return fmt.Errorf("--quiet prints no report, so it cannot be combined with --json")
	case reportPath != "":
		return fmt.Errorf("--report writes a JSON file while printing text as usual, so it cannot be combined with --quiet")
	}
	return nil
}

// applyDefaultTimeout fills entries that name no timeout of their own with the
// --check-timeout default. A per-entry value always wins, so a batch can give
// one slow contract a longer budget than the rest.
func applyDefaultTimeout(req *probe.BatchRequest, d time.Duration) {
	if d <= 0 {
		return
	}
	for i := range req.Checks {
		if req.Checks[i].Timeout == "" {
			req.Checks[i].Timeout = d.String()
		}
	}
}

// readBatch parses a check file. Unknown fields are rejected, so a typo such
// as "fn" for "function" fails loudly instead of silently skipping a probe.
func readBatch(stdin io.Reader, path string) (probe.BatchRequest, error) {
	r := stdin
	if path != "-" {
		f, err := os.Open(path) //nolint:gosec // --file is an operator-supplied path; reading it is the feature.
		if err != nil {
			return probe.BatchRequest{}, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	var req probe.BatchRequest
	var err error
	if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
		dec := yaml.NewDecoder(r)
		dec.KnownFields(true)
		err = dec.Decode(&req)
	} else {
		dec := json.NewDecoder(r)
		dec.DisallowUnknownFields()
		err = dec.Decode(&req)
	}
	if err != nil {
		return probe.BatchRequest{}, fmt.Errorf("read check file %s: %w", path, err)
	}
	return req, nil
}

func runSimulateFile(cmd *cobra.Command, opts *options, path string, report *reportFile) error {
	req, err := readSimulateBatch(cmd.InOrStdin(), path)
	if err != nil {
		return err
	}
	p, cleanup, err := opts.prober()
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := p.SimulateBatch(cmd.Context(), req)
	if err != nil {
		return err
	}
	if err := report.write(result); err != nil {
		return err
	}
	if opts.jsonOut {
		err = writeJSON(cmd.OutOrStdout(), result)
	} else {
		err = renderSimulateBatch(cmd.OutOrStdout(), result)
	}
	if err != nil {
		return err
	}
	for _, item := range result.Items {
		if item.Error != "" {
			return fmt.Errorf("one or more simulation calls could not be run")
		}
	}
	return nil
}

func readSimulateBatch(stdin io.Reader, path string) (probe.SimulateBatchRequest, error) {
	r := stdin
	if path != "-" {
		f, err := os.Open(path) //nolint:gosec // --file is an operator-supplied path; reading it is the feature.
		if err != nil {
			return probe.SimulateBatchRequest{}, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}

	var req probe.SimulateBatchRequest
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return probe.SimulateBatchRequest{}, fmt.Errorf("read simulation file %s: %w", path, err)
	}
	return req, nil
}

// inGitHubActions reports whether GitHub Actions sets the marker variable.
// Only its presence matters: a step may set GITHUB_ACTIONS=false and still
// expect workflow commands to work.
func inGitHubActions() bool {
	_, ok := os.LookupEnv(annotationEnv)
	return ok
}

func newServeCmd(opts *options) *cobra.Command {
	var addr string
	var tlsCert string
	var tlsKey string
	var inspectCacheTTL time.Duration
	var apiToken string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP API",
		Long: `Serve the HTTP API, which mirrors the CLI for use from CI systems and other
services. The API is read-only, exactly like the CLI.

The API is unauthenticated by default, which is fine on a private network. Set
API_TOKEN (or --api-token) to require "Authorization: Bearer <token>" on every
/v1 route; /healthz stays open so a liveness probe needs no credential. A
token shorter than 16 characters is rejected at startup.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if addr != "" {
				opts.cfg.HTTPAddr = addr
			}
			if cmd.Flags().Changed("inspect-cache-ttl") {
				opts.cfg.InspectCacheTTL = inspectCacheTTL
			}
			if tlsCert != "" {
				opts.cfg.HTTPTLSCert = tlsCert
			}
			if tlsKey != "" {
				opts.cfg.HTTPTLSKey = tlsKey
			}
			if (opts.cfg.HTTPTLSCert == "") != (opts.cfg.HTTPTLSKey == "") {
				return fmt.Errorf("TLS certificate and key must be configured together")
			}

			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			log := opts.cfg.Logger(os.Stderr)
			server := api.New(api.Options{
				Prober:             p,
				Addr:               opts.cfg.HTTPAddr,
				Logger:             log,
				CORSOrigins:        opts.cfg.CORSOrigins,
				NetworkPassphrase:  opts.cfg.NetworkPassphrase,
				RateLimitPerMinute: opts.cfg.HTTPRateLimitPerMinute,
				RateLimitBurst:     opts.cfg.HTTPRateLimitBurst,
				TrustForwardedFor:  opts.cfg.HTTPTrustForwardedFor,
				TLSCert:            opts.cfg.HTTPTLSCert,
				TLSKey:             opts.cfg.HTTPTLSKey,
				APIToken:           opts.cfg.APIToken,
			})
			return server.ListenAndServe(cmd.Context())
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "", "listen address, overriding HTTP_ADDR")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "TLS certificate file, overriding HTTP_TLS_CERT")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "TLS key file, overriding HTTP_TLS_KEY")
	cmd.Flags().DurationVar(&inspectCacheTTL, "inspect-cache-ttl", 0,
		"cache inspect results for this long; 0 disables (a few seconds suits dashboards, because TTLs change once per ledger)")
	cmd.Flags().StringVar(&apiToken, "api-token", "",
		fmt.Sprintf("require this bearer token on /v1 routes, overriding %s", config.EnvAPIToken))
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the SoroProbe version",
		Args:  cobra.NoArgs,
		// Version needs no network, so skip config resolution.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), resolveVersion())
			return err
		},
	}
}

// resolveVersion prefers a build-time version, falling back to the module
// version stamped into the binary by the Go toolchain.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "devel"
}

func parseDurability(s string) (health.Durability, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "persistent":
		return health.DurabilityPersistent, nil
	case "temporary", "temp":
		return health.DurabilityTemporary, nil
	default:
		return "", fmt.Errorf("unknown durability %q (want persistent or temporary)", s)
	}
}
