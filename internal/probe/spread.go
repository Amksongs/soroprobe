package probe

import "sort"

// DefaultMaxSpreadPercent is the spread, as a percentage of the minimum, above
// which a repeated simulation is reported as noisy. A call whose cost moves by
// more than this between identical runs is not a stable measurement.
const DefaultMaxSpreadPercent = 5.0

// FigureSpread is the range a single cost figure took across repeated runs.
type FigureSpread struct {
	// Name is the figure's label, such as "cpu instructions".
	Name string `json:"name"`
	// Min and Max bound the values seen across the runs.
	Min int64 `json:"min"`
	Max int64 `json:"max"`
	// Spread is Max minus Min.
	Spread int64 `json:"spread"`
	// Percent is Spread as a percentage of Min. It is nil when Min is zero,
	// because a percentage spread from zero has no meaning.
	Percent *float64 `json:"percent,omitempty"`
}

// SpreadReport describes how a call's cost varied across repeated runs.
type SpreadReport struct {
	// Runs is the number of simulations that contributed.
	Runs int `json:"runs"`
	// Figures are the per-figure ranges, in the same stable order as a cost
	// comparison.
	Figures []FigureSpread `json:"figures"`
	// Max is the cost taken as the baseline: the maximum seen for every
	// figure. A repeated run should be compared against the worst case, not
	// the luckiest.
	Max Cost `json:"max"`
	// FootprintChanges names the footprint entries that did not appear in
	// every run. A footprint that moves between identical runs usually
	// explains a cost that moves with it.
	FootprintChanges []string `json:"footprint_changes,omitempty"`
	// MaxSpreadPercent is the tolerated spread, when a threshold was given.
	MaxSpreadPercent *float64 `json:"max_spread_percent,omitempty"`
	// Noisy is true when any figure spread by more than the threshold.
	Noisy bool `json:"noisy"`
}

// SpreadCost summarizes the cost of repeated simulations.
//
// The statistics are a pure function of the costs and footprints observed, so
// they can be exercised without a network or a prober. costs must hold at
// least one entry; footprints, when given, must be the same length, with one
// footprint per cost.
//
// A nil threshold means "do not judge", leaving Noisy false while still
// reporting the ranges.
func SpreadCost(costs []Cost, footprints []Footprint, maxSpreadPercent *float64) SpreadReport {
	out := SpreadReport{Runs: len(costs), MaxSpreadPercent: maxSpreadPercent}
	if len(costs) == 0 {
		return out
	}

	figures := costFigures(costs[0])
	out.Figures = make([]FigureSpread, 0, len(figures))
	out.Max = costs[0]
	for i, f := range figures {
		spread := FigureSpread{Name: f.Name, Min: f.Value, Max: f.Value}
		for _, c := range costs[1:] {
			v := costFigures(c)[i].Value
			if v < spread.Min {
				spread.Min = v
			}
			if v > spread.Max {
				spread.Max = v
			}
		}
		spread.Spread = spread.Max - spread.Min
		if spread.Min != 0 {
			percent := float64(spread.Spread) / float64(spread.Min) * 100
			spread.Percent = &percent
		}
		out.Figures = append(out.Figures, spread)
	}

	for _, c := range costs[1:] {
		out.Max.Instructions = max(out.Max.Instructions, c.Instructions)
		out.Max.DiskReadBytes = max(out.Max.DiskReadBytes, c.DiskReadBytes)
		out.Max.WriteBytes = max(out.Max.WriteBytes, c.WriteBytes)
		out.Max.ResourceFee = max(out.Max.ResourceFee, c.ResourceFee)
		out.Max.MinResourceFee = max(out.Max.MinResourceFee, c.MinResourceFee)
	}

	if len(footprints) == len(costs) {
		out.FootprintChanges = footprintChanges(footprints)
	}

	if maxSpreadPercent == nil {
		return out
	}
	threshold := *maxSpreadPercent
	for _, f := range out.Figures {
		if f.Spread <= 0 {
			continue
		}
		if f.Percent == nil {
			// From zero to something is not a small change, exactly as in a
			// baseline comparison.
			out.Noisy = true
			continue
		}
		if *f.Percent > threshold {
			out.Noisy = true
		}
	}
	return out
}

// footprintChanges lists the read-only and read-write entries that were not
// present in every run, sorted so the report is stable.
func footprintChanges(footprints []Footprint) []string {
	seen := map[string]int{}
	for _, fp := range footprints {
		for _, k := range fp.ReadOnly {
			seen["read  "+k]++
		}
		for _, k := range fp.ReadWrite {
			seen["write "+k]++
		}
	}

	var out []string
	for k, n := range seen {
		if n != len(footprints) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
