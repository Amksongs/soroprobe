package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
)

// writeJSON emits an indented JSON document.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- terminal styling -------------------------------------------------------

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

// colorEnabled reports whether to emit ANSI escapes. It honors the NO_COLOR
// convention and stays quiet when output is redirected.
func colorEnabled(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type styler struct{ on bool }

func (s styler) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return code + text + ansiReset
}

func (s styler) bold(t string) string  { return s.wrap(ansiBold, t) }
func (s styler) dim(t string) string   { return s.wrap(ansiDim, t) }
func (s styler) green(t string) string { return s.wrap(ansiGreen, t) }
func (s styler) red(t string) string   { return s.wrap(ansiRed, t) }
func (s styler) cyan(t string) string  { return s.wrap(ansiCyan, t) }

// status renders a health status with an appropriate glyph and color.
func (s styler) status(st health.Status) string {
	switch st {
	case health.StatusHealthy:
		return s.wrap(ansiGreen, "OK       healthy")
	case health.StatusWarning:
		return s.wrap(ansiYellow, "WARN     expiring soon")
	case health.StatusCritical:
		return s.wrap(ansiRed, "CRITICAL expiring very soon")
	case health.StatusExpired:
		return s.wrap(ansiRed, "EXPIRED  ttl has lapsed")
	case health.StatusMissing:
		return s.wrap(ansiRed, "MISSING  not on ledger")
	default:
		return s.wrap(ansiDim, "UNKNOWN  no ttl reported")
	}
}

func (s styler) outcome(o probe.CheckOutcome) string {
	switch o {
	case probe.OutcomePass:
		return s.wrap(ansiGreen, "PASS")
	case probe.OutcomeWarn:
		return s.wrap(ansiYellow, "WARN")
	case probe.OutcomeFail:
		return s.wrap(ansiRed, "FAIL")
	default:
		return s.wrap(ansiDim, "SKIP")
	}
}

// stroops renders a stroop amount with its XLM equivalent. There are 10^7
// stroops in one XLM.
func stroops(n int64) string {
	return fmt.Sprintf("%d stroops (%.7f XLM)", n, float64(n)/1e7)
}

// --- renderers --------------------------------------------------------------

func renderSimulate(w io.Writer, r *probe.SimulateResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s\n", s.bold("contract"), r.ContractID)
	if r.Signature != "" {
		bw.printf("%s %s\n", s.bold("function"), r.Signature)
	} else {
		bw.printf("%s %s\n", s.bold("function"), r.Function)
	}
	if len(r.EncodedArgs) > 0 {
		bw.printf("%s %s\n", s.bold("encoded "), strings.Join(r.EncodedArgs, " "))
	}
	if r.ABINote != "" {
		bw.printf("%s\n", s.dim("note: "+r.ABINote))
	}
	bw.printf("\n")

	if r.Success {
		bw.printf("%s\n", s.green("SUCCESS  the call would succeed"))
	} else {
		bw.printf("%s\n", s.red("FAILED   the call would fail"))
		bw.printf("%s\n", indent(r.Error, "  "))
	}

	if r.Success {
		value := "(void)"
		if r.ReturnValue != nil {
			encoded, err := json.Marshal(r.ReturnValue)
			if err != nil {
				return err
			}
			value = string(encoded)
		}
		bw.printf("\n%s\n  %s\n", s.bold("return value"), value)
	}

	bw.printf("\n%s\n", s.bold("resource cost"))
	bw.printf("  cpu instructions   %d\n", r.Cost.Instructions)
	bw.printf("  disk read bytes    %d\n", r.Cost.DiskReadBytes)
	bw.printf("  write bytes        %d\n", r.Cost.WriteBytes)
	bw.printf("  resource fee       %s\n", stroops(r.Cost.ResourceFee))
	bw.printf("  min resource fee   %s\n", stroops(r.Cost.MinResourceFee))

	if n := len(r.Footprint.ReadOnly) + len(r.Footprint.ReadWrite); n > 0 {
		bw.printf("\n%s\n", s.bold("footprint"))
		for _, k := range r.Footprint.ReadOnly {
			bw.printf("  %s %s\n", s.dim("read "), k)
		}
		for _, k := range r.Footprint.ReadWrite {
			bw.printf("  %s %s\n", s.cyan("write"), k)
		}
	}

	if r.Comparison != nil {
		renderCostComparison(bw, s, r.Comparison)
	}

	if r.Spread != nil {
		renderCostSpread(bw, s, r.Spread)
	}

	if r.RestoreRequired != nil {
		bw.printf("\n%s\n", s.red("RESTORE REQUIRED"))
		bw.printf("  This call touches archived entries. They must be restored before it\n")
		bw.printf("  can run. Restore fee: %s\n", stroops(r.RestoreRequired.MinResourceFee))
	}

	// Diagnostic events are not printed here: Stellar RPC already embeds a
	// formatted event log in the error text above, so repeating them would only
	// add noise. They remain available, decoded and structured, under --json.

	bw.printf("\n%s\n", s.dim(fmt.Sprintf("simulated against ledger %d", r.LatestLedger)))
	return bw.err
}

// renderCostComparison prints each cost figure with its change against the
// baseline, in absolute and percentage terms.
//
// The percentage is omitted rather than guessed at when the baseline figure
// was zero: there is no percentage change from nothing, and inventing one
// would make an increase look smaller than it is.
func renderCostComparison(bw *bufWriter, s styler, c *probe.CostComparison) {
	bw.printf("\n%s\n", s.bold("cost compared to the baseline"))
	for _, f := range c.Figures {
		percent := "n/a"
		if f.Percent != nil {
			percent = fmt.Sprintf("%+.1f%%", *f.Percent)
		}
		bw.printf("  %-22s %d -> %d  (%+d, %s)\n", f.Name, f.Baseline, f.Current, f.Delta, percent)
	}
	if c.Exceeded && c.MaxIncreasePercent != nil {
		bw.printf("  %s\n", s.red(fmt.Sprintf(
			"a cost figure grew by more than %s", formatPercent(*c.MaxIncreasePercent))))
	}
}

// renderCostSpread prints the range each cost figure took across repeated runs.
//
// The percentage is shown only when the minimum was non-zero, for the same
// reason a baseline comparison omits it: there is no percentage spread from
// nothing. A footprint that changed between runs is listed too, because it is
// usually what made the cost move.
func renderCostSpread(bw *bufWriter, s styler, sp *probe.SpreadReport) {
	bw.printf("\n%s\n", s.bold(fmt.Sprintf("cost across %d runs", sp.Runs)))
	for _, f := range sp.Figures {
		percent := "n/a"
		if f.Percent != nil {
			percent = fmt.Sprintf("%.1f%%", *f.Percent)
		}
		bw.printf("  %-22s %d..%d  (spread %d, %s)\n", f.Name, f.Min, f.Max, f.Spread, percent)
	}
	if len(sp.FootprintChanges) > 0 {
		bw.printf("  %s\n", s.dim("footprint changed between runs:"))
		for _, k := range sp.FootprintChanges {
			bw.printf("    %s\n", k)
		}
	}
	if sp.Noisy && sp.MaxSpreadPercent != nil {
		bw.printf("  %s\n", s.red(fmt.Sprintf(
			"the cost varied by more than %s across identical runs", formatPercent(*sp.MaxSpreadPercent))))
	}
}

func renderSimulateBatch(w io.Writer, r *probe.SimulateBatchResult) error {
	bw := &bufWriter{w: w}
	bw.printf("%-24s %-8s %-48s %-14s %s\n", "NAME", "SUCCESS", "RETURN VALUE", "INSTRUCTIONS", "FEE")
	for _, item := range r.Items {
		if item.Error != "" {
			bw.printf("%-24s %-8s %-48s %-14s %s\n", truncate(item.Name, 24), "error", truncate(item.Error, 48), "-", "-")
			continue
		}
		value := item.Result.Error
		if item.Result.ReturnValue != nil {
			encoded, err := json.Marshal(item.Result.ReturnValue)
			if err != nil {
				return err
			}
			value = string(encoded)
		}
		if value == "" {
			value = "(void)"
		}
		bw.printf("%-24s %-8t %-48s %-14d %s\n",
			truncate(item.Name, 24), item.Result.Success, truncate(value, 48),
			item.Result.Cost.Instructions, stroops(item.Result.Cost.ResourceFee))
	}
	return bw.err
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-3]) + "..."
}

// indent prefixes every line of s, so multi-line RPC errors stay aligned.
func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func renderInspect(w io.Writer, r *probe.InspectResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s\n\n", s.bold("contract"), r.ContractID)

	if !r.Deployed {
		bw.printf("%s\n", s.red("NOT DEPLOYED  no contract instance entry found on this network"))
		bw.printf("\n%s\n", s.dim(fmt.Sprintf("checked at ledger %d", r.LatestLedger)))
		return bw.err
	}

	bw.printf("%s %s", s.bold("executable"), r.Executable)
	if r.WasmHash != "" {
		bw.printf(" %s", s.dim(r.WasmHash[:16]+"..."))
	}
	bw.printf("\n\n%s\n", s.bold("state entries"))

	renderEntry(bw, s, "instance", r.Instance)
	if r.Code != nil {
		renderEntry(bw, s, "code", *r.Code)
	} else {
		bw.printf("  %-10s %s\n", "code", s.dim("n/a  built into the host, no code entry"))
	}
	for _, d := range r.Data {
		renderEntry(bw, s, "data "+d.Key, d)
	}

	if r.InstanceStorage != nil {
		encoded, err := json.MarshalIndent(r.InstanceStorage, "  ", "  ")
		if err != nil {
			return err
		}
		bw.printf("\n%s %s\n", s.bold("instance storage"), s.dim("(shares the instance entry's TTL)"))
		bw.printf("  %s\n", string(encoded))
	}

	if len(r.ExtendEstimates) > 0 {
		renderExtendEstimates(bw, s, r.ExtendEstimates)
	}

	bw.printf("\n%s %s\n", s.bold("overall"), s.status(r.Status))
	bw.printf("%s\n", s.dim(fmt.Sprintf("checked at ledger %d", r.LatestLedger)))
	return bw.err
}

// renderExtendEstimates prints what it would cost to keep each entry alive.
//
// An entry that cannot be extended says so instead of quoting a price: a
// number for an entry that is not there would be worse than no number.
func renderExtendEstimates(bw *bufWriter, s styler, estimates []probe.ExtendEstimate) {
	bw.printf("\n%s %s\n", s.bold("cost to extend"), s.dim("(simulated; nothing is submitted)"))
	for _, e := range estimates {
		label := e.Kind
		if e.Key != "" {
			label = e.Kind + " " + e.Key
		}
		if e.Note != "" {
			bw.printf("  %-22s %s\n", label, s.dim(e.Note))
			continue
		}
		bw.printf("  %-22s %s  %s\n", label,
			stroops(e.ResourceFee),
			s.dim(fmt.Sprintf("to ledger %d, %d ledgers", e.ExtendTo, e.Ledgers)))
	}
}

func renderEntry(bw *bufWriter, s styler, label string, e probe.EntryReport) {
	bw.printf("  %-10s %s\n", label, s.status(e.Status))
	bw.printf("  %-10s %s\n", "", s.dim(e.Summary))
}

func renderCheck(w io.Writer, r *probe.CheckResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s\n\n", s.bold("contract"), r.ContractID)
	for _, c := range r.Checks {
		bw.printf("  %s  %-22s %s\n", s.outcome(c.Outcome), c.Name, c.Detail)
	}

	bw.printf("\n%s ", s.bold("result"))
	if r.OK {
		bw.printf("%s\n", s.green("PASS"))
	} else {
		bw.printf("%s\n", s.red("FAIL"))
	}
	bw.printf("%s\n", s.dim(fmt.Sprintf("checked at ledger %d", r.LatestLedger)))
	return bw.err
}

// renderBatch prints one line per check — its verdict and the first step
// that did not pass — and a summary. The full per-step detail is in --json.
func renderBatch(w io.Writer, r *probe.BatchResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	width := 4
	for _, item := range r.Items {
		width = max(width, len(item.Name))
	}

	passed, failed, errored := 0, 0, 0
	for _, item := range r.Items {
		var verdict, detail string
		switch {
		case item.Error != "":
			errored++
			verdict, detail = s.red("ERROR"), item.Error
		case item.Result.OK:
			passed++
			verdict, detail = s.green("PASS "), firstNotPassing(item.Result, probe.OutcomeWarn)
		default:
			failed++
			verdict, detail = s.red("FAIL "), firstNotPassing(item.Result, probe.OutcomeFail)
		}
		bw.printf("  %s  %-*s  %s\n", verdict, width, item.Name, detail)
	}

	bw.printf("\n%s %d passed, %d failed, %d could not run\n", s.bold("result"), passed, failed, errored)
	return bw.err
}

// firstNotPassing describes the first step with the given outcome, or
// returns "" when there is none.
func firstNotPassing(r *probe.CheckResult, outcome probe.CheckOutcome) string {
	for _, c := range r.Checks {
		if c.Outcome == outcome {
			return c.Name + ": " + c.Detail
		}
	}
	return ""
}

// bufWriter records the first write error so renderers need not check each call.
type bufWriter struct {
	w   io.Writer
	err error
}

func (b *bufWriter) printf(format string, args ...any) {
	if b.err != nil {
		return
	}
	_, b.err = fmt.Fprintf(b.w, format, args...)
}

func renderNetwork(w io.Writer, r *probe.NetworkResult, configuredPassphrase string) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%-18s %s\n", s.bold("rpc url"), redactURL(r.RPCURL))

	statusStr := r.Status
	if r.Status == "healthy" {
		statusStr = s.green(r.Status)
	} else {
		statusStr = s.red(r.Status)
	}
	bw.printf("%-18s %s\n", s.bold("health"), statusStr)
	bw.printf("%-18s %d\n", s.bold("latest ledger"), r.LatestLedger)
	bw.printf("%-18s %d\n", s.bold("protocol"), r.ProtocolVersion)

	if r.PassphraseMatch {
		bw.printf("%-18s %s\n", s.bold("passphrase"), r.NetworkPassphrase)
	} else {
		bw.printf("%-18s %s %s\n", s.bold("passphrase"), s.red(r.NetworkPassphrase), s.red(fmt.Sprintf("(mismatch with configured %q)", configuredPassphrase)))
	}

	return bw.err
}
