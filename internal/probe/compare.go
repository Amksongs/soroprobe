package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/soroworks/soroprobe/internal/health"
)

// CompareCall is one read-only invocation to run on both networks.
//
// A call is judged by what the contract answers: whether it succeeds and, when
// it does, the decoded return value. Costs are not compared — two networks can
// price the same work differently without either deployment being wrong.
type CompareCall struct {
	// Name labels the call in the report. Defaults to Function.
	Name string `json:"name,omitempty"`
	// Function is the contract function to call.
	Function string `json:"function"`
	// Args are argument specifications in the scval package's "type:value" form.
	Args []string `json:"args,omitempty"`
}

// label is what the call is shown as.
func (c CompareCall) label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Function
}

// CompareRequest describes a contract to compare across two networks.
type CompareRequest struct {
	// ContractID is the contract address on the first network.
	ContractID string `json:"contract_id"`
	// OtherContractID is the contract address on the second network. Empty
	// means the same address, which is the usual case: a contract address is
	// derived from its deployer and salt, so the same deployment lands at the
	// same address on every network.
	OtherContractID string `json:"other_contract_id,omitempty"`

	// TestnetLabel and MainnetLabel name the two networks in the result.
	TestnetLabel string `json:"-"`
	MainnetLabel string `json:"-"`

	// DataKeys name contract data entries to compare alongside the instance
	// and code entries, exactly as InspectRequest.DataKeys does.
	DataKeys []string `json:"data_keys,omitempty"`
	// DataDurability selects which durability the DataKeys are read at.
	DataDurability health.Durability `json:"data_durability,omitempty"`
	// Calls are the read-only invocations to run on both networks.
	Calls []CompareCall `json:"calls,omitempty"`
}

// CompareSide is what one network reported.
type CompareSide struct {
	// Network is the profile or label the side was configured with.
	Network string `json:"network"`
	// Inspect is the contract's state health on this network.
	Inspect *InspectResult `json:"inspect"`
}

// CompareEntry links one ledger entry's TTL on both networks.
//
// Differ reports whether the health classification differs. The remaining
// ledger counts are expected to differ — the two chains are at different
// heights — so they are reported, not judged.
type CompareEntry struct {
	// Kind is "instance", "code" or "data".
	Kind string `json:"kind"`
	// Key describes which data entry this is; empty for instance and code.
	Key string `json:"key,omitempty"`

	TestnetStatus string `json:"testnet_status"`
	MainnetStatus string `json:"mainnet_status"`

	TestnetLedgersRemaining int64 `json:"testnet_ledgers_remaining"`
	MainnetLedgersRemaining int64 `json:"mainnet_ledgers_remaining"`

	// Differ is true when the two health statuses disagree.
	Differ bool `json:"differ"`
}

// CompareCallResult is one read-only call run on both networks.
type CompareCallResult struct {
	Name     string   `json:"name"`
	Function string   `json:"function"`
	Args     []string `json:"args,omitempty"`

	Testnet *SimulateResult `json:"testnet,omitempty"`
	Mainnet *SimulateResult `json:"mainnet,omitempty"`

	// Differ is true when the calls do not answer the same way.
	Differ bool `json:"differ"`
	// Detail explains a difference in one line.
	Detail string `json:"detail,omitempty"`
}

// CompareResult is the outcome of comparing one contract across two networks.
type CompareResult struct {
	Testnet CompareSide `json:"testnet"`
	Mainnet CompareSide `json:"mainnet"`

	// WasmMatch is true when both networks run the same executable: equal
	// Wasm hashes, or a host-implemented Stellar Asset Contract on both. This
	// is the verdict the exit code reflects.
	WasmMatch bool `json:"wasm_match"`

	// Entries compares the TTL health of the instance, code and any named
	// data entries.
	Entries []CompareEntry `json:"entries,omitempty"`
	// Calls compares the configured read-only invocations.
	Calls []CompareCallResult `json:"calls,omitempty"`

	// Match mirrors WasmMatch. It is named separately in JSON so a consumer
	// reading the document does not have to infer what the overall verdict
	// depends on.
	Match bool `json:"match"`
}

// Compare inspects a contract on two networks and compares the executable,
// state-entry TTLs and any configured read-only calls.
//
// p is the first network (the "testnet" side) and other is the second. A
// contract that is not deployed on either side is a result, not an error: the
// Wasm hashes cannot match, so the comparison fails with an explanation rather
// than pretending the two deployments agree. Go errors are reserved for
// problems reaching a network or encoding a call.
func (p *Prober) Compare(ctx context.Context, other *Prober, req CompareRequest) (*CompareResult, error) {
	if other == nil {
		return nil, errors.New("probe: a second prober is required to compare networks")
	}
	if req.ContractID == "" {
		return nil, errors.New("probe: a contract id is required to compare networks")
	}

	otherContractID := req.OtherContractID
	if otherContractID == "" {
		otherContractID = req.ContractID
	}

	testnetInspect, err := p.Inspect(ctx, InspectRequest{
		ContractID:     req.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, fmt.Errorf("inspect on %s: %w", req.TestnetLabel, err)
	}

	mainnetInspect, err := other.Inspect(ctx, InspectRequest{
		ContractID:     otherContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, fmt.Errorf("inspect on %s: %w", req.MainnetLabel, err)
	}

	result := &CompareResult{
		Testnet: CompareSide{Network: req.TestnetLabel, Inspect: testnetInspect},
		Mainnet: CompareSide{Network: req.MainnetLabel, Inspect: mainnetInspect},
		Entries: compareEntries(testnetInspect, mainnetInspect, req.DataKeys),
	}
	result.WasmMatch = wasmMatches(testnetInspect, mainnetInspect)

	for _, call := range req.Calls {
		compared, err := compareCall(ctx, p, other, req.ContractID, otherContractID, call)
		if err != nil {
			return nil, err
		}
		result.Calls = append(result.Calls, compared)
	}

	result.Match = result.WasmMatch
	return result, nil
}

// wasmMatches reports whether the two deployments run the same executable.
//
// A Stellar Asset Contract has no Wasm entry to hash, so two of them count as
// matching: neither deployment's behavior can drift from the other's, because
// both are implemented by the host. One Wasm contract and one asset contract
// never match.
func wasmMatches(testnet, mainnet *InspectResult) bool {
	if testnet == nil || mainnet == nil || !testnet.Deployed || !mainnet.Deployed {
		return false
	}
	if testnet.Executable != mainnet.Executable {
		return false
	}
	if testnet.Executable == "stellar_asset" {
		return true
	}
	return testnet.WasmHash != "" && testnet.WasmHash == mainnet.WasmHash
}

// compareEntries lines up the instance, code and named data entries of two
// inspections. The result keeps the order the entries were asked for, so the
// report reads the same way twice.
func compareEntries(testnet, mainnet *InspectResult, dataKeys []string) []CompareEntry {
	out := make([]CompareEntry, 0, len(dataKeys)+2)

	out = append(out, compareEntry("instance", "", &testnet.Instance, &mainnet.Instance))

	// A Stellar Asset Contract has no code entry on either side, which is a
	// meaningful "n/a" rather than a difference.
	if testnet.Code != nil || mainnet.Code != nil {
		out = append(out, compareEntry("code", "", testnet.Code, mainnet.Code))
	}

	testnetData := indexData(testnet)
	mainnetData := indexData(mainnet)
	for _, key := range dataKeys {
		out = append(out, compareEntry("data", key, testnetData[key], mainnetData[key]))
	}
	return out
}

// compareEntry builds one row, treating an absent report as "n/a".
func compareEntry(kind, key string, testnet, mainnet *EntryReport) CompareEntry {
	testnetStatus, testnetLedgers := reportStatus(testnet)
	mainnetStatus, mainnetLedgers := reportStatus(mainnet)

	return CompareEntry{
		Kind:                    kind,
		Key:                     key,
		TestnetStatus:           testnetStatus,
		MainnetStatus:           mainnetStatus,
		TestnetLedgersRemaining: testnetLedgers,
		MainnetLedgersRemaining: mainnetLedgers,
		Differ:                  testnetStatus != mainnetStatus,
	}
}

func reportStatus(report *EntryReport) (string, int64) {
	if report == nil {
		return "n/a", 0
	}
	return string(report.Status), report.LedgersRemaining
}

// indexData maps data-entry key specs to their reports.
//
// Absence from the map means the entry was not reported at all, which happens
// when the contract is not deployed. Looking the key up by spec rather than by
// position keeps the two sides aligned even if one of them returned fewer
// entries.
func indexData(result *InspectResult) map[string]*EntryReport {
	out := make(map[string]*EntryReport, len(result.Data))
	for i := range result.Data {
		out[result.Data[i].Key] = &result.Data[i]
	}
	return out
}

// compareCall runs one invocation on both networks and judges the answers.
func compareCall(ctx context.Context, testnet, mainnet *Prober, testnetContractID, mainnetContractID string, call CompareCall) (CompareCallResult, error) {
	testnetResult, err := testnet.Simulate(ctx, SimulateRequest{
		ContractID: testnetContractID,
		Function:   call.Function,
		Args:       call.Args,
	})
	if err != nil {
		return CompareCallResult{}, fmt.Errorf("call %s on the first network: %w", call.label(), err)
	}

	mainnetResult, err := mainnet.Simulate(ctx, SimulateRequest{
		ContractID: mainnetContractID,
		Function:   call.Function,
		Args:       call.Args,
	})
	if err != nil {
		return CompareCallResult{}, fmt.Errorf("call %s on the second network: %w", call.label(), err)
	}

	compared := CompareCallResult{
		Name:     call.label(),
		Function: call.Function,
		Args:     call.Args,
		Testnet:  testnetResult,
		Mainnet:  mainnetResult,
	}
	compared.Differ, compared.Detail = callsDiffer(testnetResult, mainnetResult)
	return compared, nil
}

// callsDiffer compares two simulation results.
func callsDiffer(testnet, mainnet *SimulateResult) (bool, string) {
	switch {
	case testnet.Success != mainnet.Success:
		return true, "one network succeeds where the other fails"
	case !testnet.Success && testnet.Error != mainnet.Error:
		return true, "both fail, but with different errors"
	case !testnet.Success:
		return false, ""
	}

	testnetValue := canonicalValue(testnet.ReturnValue)
	mainnetValue := canonicalValue(mainnet.ReturnValue)
	if testnetValue != mainnetValue {
		return true, fmt.Sprintf("returned %s on one network and %s on the other", testnetValue, mainnetValue)
	}
	return false, ""
}

// canonicalValue renders a decoded return value so two of them can be compared
// exactly. JSON encoding sorts map keys, so a map's field order cannot show up
// as a false difference.
func canonicalValue(value any) string {
	if value == nil {
		return "(void)"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}
