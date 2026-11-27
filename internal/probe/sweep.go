package probe

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
)

// DefaultSweepConcurrency bounds how many sweep simulations run at once,
// so a large sweep does not flood the RPC.
const DefaultSweepConcurrency = 4

// MaxSweepSamples caps one sweep.
const MaxSweepSamples = 500

// SweepSample is one argument set to simulate.
type SweepSample struct {
	// Label names the sample in the output; it defaults to its position.
	Label string   `json:"label,omitempty"`
	Args  []string `json:"args"`
}

// SweepRequest simulates one function with many argument sets.
type SweepRequest struct {
	ContractID string
	Function   string
	// Samples are run in order; list them from smallest to largest, so the
	// largest one that fits the limits is meaningful.
	Samples []SweepSample
	// Concurrency bounds parallel simulations; 0 means
	// DefaultSweepConcurrency.
	Concurrency int
}

// SweepRow is one sample's outcome.
type SweepRow struct {
	Label   string   `json:"label"`
	Args    []string `json:"args"`
	Success bool     `json:"success"`
	Error   string   `json:"error,omitempty"`
	Cost    Cost     `json:"cost"`
	// FootprintEntries counts the entries the call touches.
	FootprintEntries int `json:"footprint_entries"`
	// WithinLimits is false when the call failed or a resource exceeds its
	// per-transaction limit; nil when the limits are unknown and the call
	// succeeded.
	WithinLimits *bool `json:"within_limits,omitempty"`
	// OverLimits names the resources over their limit.
	OverLimits []string `json:"over_limits,omitempty"`
}

// SweepResult is a table of resources per sample.
type SweepResult struct {
	ContractID string     `json:"contract_id"`
	Function   string     `json:"function"`
	Rows       []SweepRow `json:"rows"`
	// Limits are the per-transaction limits the rows were judged against.
	Limits map[string]int64 `json:"limits,omitempty"`
	// LargestWithinLimits is the label of the last sample, in the order
	// given, that succeeded within every limit. Empty when none did.
	LargestWithinLimits string `json:"largest_within_limits,omitempty"`
	// Note explains limits that could not be read.
	Note string `json:"note,omitempty"`
}

// Sweep simulates each sample with bounded concurrency and reports how the
// cost scales with the input.
func (p *Prober) Sweep(ctx context.Context, req SweepRequest) (*SweepResult, error) {
	if len(req.Samples) == 0 {
		return nil, errors.New("a sweep needs at least one sample")
	}
	if len(req.Samples) > MaxSweepSamples {
		return nil, fmt.Errorf("a sweep takes at most %d samples, got %d", MaxSweepSamples, len(req.Samples))
	}
	workers := req.Concurrency
	if workers <= 0 {
		workers = DefaultSweepConcurrency
	}

	out := &SweepResult{ContractID: req.ContractID, Function: req.Function, Rows: make([]SweepRow, len(req.Samples))}
	limits, err := p.txLimits(ctx)
	if err != nil {
		out.Note = "network limits unavailable, so samples are only judged by whether they succeed: " + err.Error()
	} else if len(limits) > 0 {
		out.Limits = limits
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i, sample := range req.Samples {
		label := sample.Label
		if label == "" {
			label = strconv.Itoa(i + 1)
		}
		wg.Add(1)
		go func(i int, label string, args []string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				if firstErr == nil {
					firstErr = ctx.Err()
				}
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			row := SweepRow{Label: label, Args: args}
			res, err := p.Simulate(ctx, SimulateRequest{ContractID: req.ContractID, Function: req.Function, Args: args})
			if err != nil {
				// A sample that cannot even be built (a bad argument) is a
				// row, not an abort: the rest of the sweep still means
				// something.
				row.Error = err.Error()
			} else {
				row.Success, row.Error, row.Cost = res.Success, res.Error, res.Cost
				row.FootprintEntries = len(res.Footprint.ReadOnly) + len(res.Footprint.ReadWrite)
			}
			judge(&row, out.Limits)
			mu.Lock()
			out.Rows[i] = row
			mu.Unlock()
		}(i, label, sample.Args)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	for _, row := range out.Rows {
		if row.WithinLimits != nil && *row.WithinLimits {
			out.LargestWithinLimits = row.Label
		}
	}
	if out.Limits == nil && out.Note == "" {
		out.Note = "the network reported no resource limits"
	}
	return out, nil
}

func judge(row *SweepRow, limits map[string]int64) {
	if !row.Success {
		f := false
		row.WithinLimits = &f
		return
	}
	if len(limits) == 0 {
		return
	}
	check := func(name string, used int64) {
		if limit, ok := limits[name]; ok && used > limit {
			row.OverLimits = append(row.OverLimits, name)
		}
	}
	check(sweepInstructions, int64(row.Cost.Instructions))
	check(sweepReadBytes, int64(row.Cost.DiskReadBytes))
	check(sweepWriteBytes, int64(row.Cost.WriteBytes))
	check(sweepFootprint, int64(row.FootprintEntries))
	ok := len(row.OverLimits) == 0
	row.WithinLimits = &ok
}

// Names of the per-transaction limits a sweep judges rows against.
const (
	sweepInstructions = "instructions"
	sweepReadBytes    = "disk_read_bytes"
	sweepWriteBytes   = "write_bytes" //nolint:gosec // a resource name, not a credential
	sweepFootprint    = "footprint_entries"
)

// txLimits reads the per-transaction limits a sweep needs from the
// network's config settings.
func (p *Prober) txLimits(ctx context.Context) (map[string]int64, error) {
	ids := []xdr.ConfigSettingId{
		xdr.ConfigSettingIdConfigSettingContractComputeV0,
		xdr.ConfigSettingIdConfigSettingContractLedgerCostV0,
		xdr.ConfigSettingIdConfigSettingContractLedgerCostExtV0,
	}
	keys := make([]string, 0, len(ids))
	for _, id := range ids {
		key, err := stellar.EncodeKey(xdr.LedgerKey{
			Type:          xdr.LedgerEntryTypeConfigSetting,
			ConfigSetting: &xdr.LedgerKeyConfigSetting{ConfigSettingId: id},
		})
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keys})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, entry := range resp.Entries {
		var data xdr.LedgerEntryData
		if err := xdr.SafeUnmarshalBase64(entry.DataXDR, &data); err != nil {
			continue
		}
		s, ok := data.GetConfigSetting()
		if !ok {
			continue
		}
		if c, ok := s.GetContractCompute(); ok {
			out[sweepInstructions] = int64(c.TxMaxInstructions)
		}
		if c, ok := s.GetContractLedgerCost(); ok {
			out[sweepReadBytes] = int64(c.TxMaxDiskReadBytes)
			out[sweepWriteBytes] = int64(c.TxMaxWriteBytes)
		}
		if c, ok := s.GetContractLedgerCostExt(); ok {
			out[sweepFootprint] = int64(c.TxMaxFootprintEntries)
		}
	}
	return out, nil
}

// ExpandTemplate builds the arguments for sample size n from templates.
// "{n}" becomes n itself, and "{repeat:SPEC}" becomes SPEC n times as the
// elements of a vec literal, so `vec:[{repeat:"u32:7"}]` is a vector of n
// sevens.
func ExpandTemplate(templates []string, n int) ([]string, error) {
	out := make([]string, len(templates))
	for i, t := range templates {
		s := strings.ReplaceAll(t, "{n}", strconv.Itoa(n))
		for {
			start := strings.Index(s, "{repeat:")
			if start < 0 {
				break
			}
			end := strings.Index(s[start:], "}")
			if end < 0 {
				return nil, fmt.Errorf("template %q has a {repeat: placeholder without a closing brace", t)
			}
			spec := s[start+len("{repeat:") : start+end]
			items := make([]string, n)
			for j := range items {
				items[j] = spec
			}
			s = s[:start] + strings.Join(items, ",") + s[start+end+1:]
		}
		out[i] = s
	}
	return out, nil
}

// ParseSizes reads a list of sample sizes: "1,2,4,8", "1..10" or
// "10..100:10" (a range with a step).
func ParseSizes(spec string) ([]int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("no sizes given")
	}
	if from, rest, ok := strings.Cut(spec, ".."); ok {
		to, stepText, hasStep := strings.Cut(rest, ":")
		lo, err1 := strconv.Atoi(strings.TrimSpace(from))
		hi, err2 := strconv.Atoi(strings.TrimSpace(to))
		step := 1
		var err3 error
		if hasStep {
			step, err3 = strconv.Atoi(strings.TrimSpace(stepText))
		}
		if err := errors.Join(err1, err2, err3); err != nil || lo < 0 || hi < lo || step < 1 {
			return nil, fmt.Errorf("sizes %q: want FROM..TO or FROM..TO:STEP with 0 <= FROM <= TO and STEP >= 1", spec)
		}
		var out []int
		for n := lo; n <= hi; n += step {
			out = append(out, n)
			if len(out) > MaxSweepSamples {
				return nil, fmt.Errorf("sizes %q give more than %d samples", spec, MaxSweepSamples)
			}
		}
		return out, nil
	}
	var out []int
	for _, part := range strings.Split(spec, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("sizes %q: %q is not a size", spec, part)
		}
		out = append(out, n)
	}
	return out, nil
}
