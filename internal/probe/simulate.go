package probe

import (
	"context"
	"errors"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// SimulateRequest describes an invocation to simulate.
type SimulateRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id"`
	// Function is the contract function to call.
	Function string `json:"function"`
	// Args are argument specifications in the scval package's "type:value" form.
	Args []string `json:"args,omitempty"`
	// Baseline, when set, is a saved simulation report whose cost this
	// simulation is compared against. It must name the same contract and
	// function; see ErrBaselineMismatch.
	Baseline *Baseline `json:"baseline,omitempty"`
	// MaxIncreasePercent, when set, is the largest percentage increase in
	// any cost figure that is tolerated. It only has an effect alongside a
	// Baseline.
	MaxIncreasePercent *float64 `json:"max_increase_percent,omitempty"`
	// Repeat, when greater than one, simulates the call that many times and
	// reports the spread between the runs. It is how a noisy estimate is
	// told apart from a stable one.
	Repeat int `json:"repeat,omitempty"`
	// MaxSpreadPercent, when set alongside Repeat, is the largest spread in
	// any cost figure, as a percentage of its minimum, that is tolerated
	// before the call is reported as noisy. A nil value reports the spread
	// without judging it.
	MaxSpreadPercent *float64 `json:"max_spread_percent,omitempty"`
}

// ErrBaselineMismatch is returned when a baseline names a different contract
// or function than the call being simulated. Its costs say nothing about a
// different call, so comparing them would report a change that never
// happened.
var ErrBaselineMismatch = errors.New("the baseline does not match the call")

// Baseline is a saved simulation report used as a cost reference.
//
// It has the shape a `simulate --json` report has, so a report saved from an
// earlier run can be fed back in unchanged.
type Baseline struct {
	// ContractID is the contract the baseline was taken against.
	ContractID string `json:"contract_id"`
	// Function is the function the baseline was taken against.
	Function string `json:"function"`
	// Cost is the cost the baseline recorded.
	Cost Cost `json:"cost"`
}

// CostChange is the change in one cost figure between a baseline and a
// simulation.
type CostChange struct {
	// Name is the figure's label, such as "cpu instructions".
	Name string `json:"name"`
	// Baseline is the figure's value in the saved report.
	Baseline int64 `json:"baseline"`
	// Current is the figure's value in this simulation.
	Current int64 `json:"current"`
	// Delta is Current minus Baseline.
	Delta int64 `json:"delta"`
	// Percent is the percentage change. It is nil when the baseline was
	// zero, because a percentage change from zero has no meaning.
	Percent *float64 `json:"percent,omitempty"`
}

// CostComparison reports how a simulation's cost differs from a baseline.
type CostComparison struct {
	// Figures are the per-figure changes, in a stable order.
	Figures []CostChange `json:"figures"`
	// MaxIncreasePercent is the tolerated percentage increase, when a
	// threshold was given.
	MaxIncreasePercent *float64 `json:"max_increase_percent,omitempty"`
	// Exceeded is true when a figure grew by more than the tolerance.
	Exceeded bool `json:"exceeded"`
}

// costFigures lists the figures a comparison looks at, in a stable order.
//
// The minimum resource fee is deliberately left out: it is the network's
// recommendation rather than a measurement, and it moves with the resource
// fee anyway.
func costFigures(c Cost) []CostFigure {
	return []CostFigure{
		{Name: "cpu instructions", Value: int64(c.Instructions)},
		{Name: "disk read bytes", Value: int64(c.DiskReadBytes)},
		{Name: "write bytes", Value: int64(c.WriteBytes)},
		{Name: "resource fee (stroops)", Value: c.ResourceFee},
	}
}

// CostFigure is one measurable figure of a simulation's cost.
type CostFigure struct {
	// Name is the figure's label.
	Name string
	// Value is the figure's value.
	Value int64
}

// CompareCost compares a simulation's cost against a saved baseline.
//
// The comparison is a pure function of the two costs and the threshold, so it
// can be exercised without a network or a prober.
//
// A figure that grew is compared in percentage terms. A baseline of zero has
// no percentage to compare against, so any growth from it counts as an
// increase without limit: from nothing to something is not a small change.
func CompareCost(baseline, current Cost, maxIncreasePercent *float64) CostComparison {
	base := costFigures(baseline)
	cur := costFigures(current)

	out := CostComparison{
		Figures:            make([]CostChange, 0, len(base)),
		MaxIncreasePercent: maxIncreasePercent,
	}

	for i := range base {
		change := CostChange{
			Name:     base[i].Name,
			Baseline: base[i].Value,
			Current:  cur[i].Value,
		}
		change.Delta = change.Current - change.Baseline
		if change.Baseline != 0 {
			percent := float64(change.Delta) / float64(change.Baseline) * 100
			change.Percent = &percent
		}
		out.Figures = append(out.Figures, change)
	}

	if maxIncreasePercent == nil {
		return out
	}

	threshold := *maxIncreasePercent
	for _, f := range out.Figures {
		if f.Delta <= 0 {
			continue
		}
		if f.Percent == nil {
			out.Exceeded = true
			continue
		}
		if *f.Percent > threshold {
			out.Exceeded = true
		}
	}
	return out
}

// NamedSimulation is one named invocation in a simulation batch.
type NamedSimulation struct {
	Name string `json:"name,omitempty"`
	SimulateRequest
}

// SimulateBatchRequest is a list of invocations to simulate sequentially.
type SimulateBatchRequest struct {
	Calls []NamedSimulation `json:"calls"`
}

// SimulateBatchItem is the outcome of one simulation in a batch.
type SimulateBatchItem struct {
	Name   string          `json:"name"`
	Result *SimulateResult `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// SimulateBatchResult aggregates a batch of simulations.
type SimulateBatchResult struct {
	Items []SimulateBatchItem `json:"items"`
}

// SimulateBatch runs each invocation sequentially and records individual
// errors so a bad call does not prevent the remaining calls from running.
func (p *Prober) SimulateBatch(ctx context.Context, req SimulateBatchRequest) (*SimulateBatchResult, error) {
	if len(req.Calls) == 0 {
		return nil, fmt.Errorf("a simulation batch must contain at least one call")
	}

	result := &SimulateBatchResult{Items: make([]SimulateBatchItem, 0, len(req.Calls))}
	for _, call := range req.Calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := call.Name
		if name == "" {
			name = call.ContractID
		}
		item := SimulateBatchItem{Name: name}
		simulated, err := p.Simulate(ctx, call.SimulateRequest)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			item.Error = err.Error()
		} else {
			item.Result = simulated
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

// SimulateResult is the outcome of a simulated invocation.
type SimulateResult struct {
	ContractID string   `json:"contract_id"`
	Function   string   `json:"function"`
	Args       []string `json:"args,omitempty"`

	// Signature is the function's declared signature, present when the
	// arguments were typed from the contract's interface.
	Signature string `json:"signature,omitempty"`
	// EncodedArgs are the arguments as actually encoded, after typing from
	// the interface. Present only when typing changed at least one.
	EncodedArgs []string `json:"encoded_args,omitempty"`
	// ABINote explains why an interface source was configured but not
	// used, so a fallback to inference is visible rather than silent.
	ABINote string `json:"abi_note,omitempty"`

	// Success is true when the call would succeed.
	Success bool `json:"success"`
	// Error carries the simulation failure message when Success is false.
	Error string `json:"error,omitempty"`

	// ReturnValue is the decoded return value, JSON-marshalable.
	ReturnValue any `json:"return_value"`
	// ReturnValueXDR is the raw base64 ScVal, for exact round-tripping.
	ReturnValueXDR string `json:"return_value_xdr,omitempty"`

	Cost      Cost      `json:"cost"`
	Footprint Footprint `json:"footprint"`

	// Comparison is the cost compared against a baseline, when a baseline
	// was supplied.
	Comparison *CostComparison `json:"comparison,omitempty"`

	// Spread describes how the cost varied across repeated runs, when
	// --repeat asked for more than one.
	Spread *SpreadReport `json:"spread,omitempty"`

	// RestoreRequired is set when archived entries block the call.
	RestoreRequired *RestoreInfo `json:"restore_required,omitempty"`

	// Events are decoded diagnostic events from the simulation.
	Events []Event `json:"events,omitempty"`

	// Auth lists the authorization entries the call requires.
	Auth []AuthEntry `json:"auth,omitempty"`

	// LatestLedger is the ledger the simulation ran against.
	LatestLedger uint32 `json:"latest_ledger"`
}

// AuthorizedInvocation is a node in an authorized invocation tree.
type AuthorizedInvocation struct {
	ContractID     string                 `json:"contract_id,omitempty"`
	Function       string                 `json:"function,omitempty"`
	Args           []any                  `json:"args,omitempty"`
	SubInvocations []AuthorizedInvocation `json:"sub_invocations,omitempty"`
}

// AuthEntry represents an authorization required by an invocation.
type AuthEntry struct {
	Address         string                `json:"address,omitempty"`
	IsSourceAccount bool                  `json:"is_source_account"`
	RootInvocation  *AuthorizedInvocation `json:"root_invocation,omitempty"`
	RawXDR          string                `json:"raw_xdr,omitempty"`
}

// DefaultBaseFee is the network minimum base fee for a single transaction operation (100 stroops).
const DefaultBaseFee int64 = 100

// EstimateTotalFee calculates the estimated total fee for a simulated call:
// resource fee plus the base inclusion fee (base fee × 1 operation).
func EstimateTotalFee(resourceFee, baseFee int64) int64 {
	if baseFee <= 0 {
		baseFee = DefaultBaseFee
	}
	return resourceFee + baseFee
}

// Simulate builds an invocation, simulates it, and reports the decoded result
// alongside its resource cost.
//
// A simulation that the network rejects is not a Go error: it is a result with
// Success false and Error set. Go errors are reserved for problems reaching the
// network or encoding the request.
func (p *Prober) Simulate(ctx context.Context, req SimulateRequest) (*SimulateResult, error) {
	contract, err := stellar.ParseContractID(req.ContractID)
	if err != nil {
		return nil, err
	}
	if req.Function == "" {
		return nil, fmt.Errorf("a function name is required")
	}
	// A baseline for a different call is rejected before anything is sent:
	// its costs would describe a call that was never made.
	if err := checkBaseline(req); err != nil {
		return nil, err
	}

	typed, err := p.typeArgs(ctx, req)
	if err != nil {
		return nil, err
	}

	args, err := p.codec.EncodeAll(typed.args)
	if err != nil {
		return nil, err
	}

	if req.Repeat < 1 {
		req.Repeat = 1
	}

	envelope, err := stellar.BuildInvocationTx(stellar.InvocationParams{
		SourceAccount: p.source,
		Contract:      contract,
		Function:      req.Function,
		Args:          args,
	})
	if err != nil {
		return nil, err
	}

	costs := make([]Cost, 0, req.Repeat)
	footprints := make([]Footprint, 0, req.Repeat)
	var result *SimulateResult
	for i := 0; i < req.Repeat; i++ {
		resp, err := p.client.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{
			Transaction: envelope,
		})
		if err != nil {
			return nil, err
		}

		built, err := p.buildSimulateResult(req, resp)
		if err != nil {
			return nil, err
		}
		// The last run is the one reported in full; the earlier runs only
		// contribute to the spread. The call is deterministic in the
		// success case, so they agree on everything the report shows.
		result = built
		costs = append(costs, built.Cost)
		footprints = append(footprints, built.Footprint)
	}

	result.Signature = typed.signature
	result.ABINote = typed.note
	if typed.changed {
		result.EncodedArgs = typed.args
	}

	// A repeated run is judged by its worst case: the maximum of every
	// figure cannot make a flaky cost check pass by luck.
	if req.Repeat > 1 {
		spread := SpreadCost(costs, footprints, req.MaxSpreadPercent)
		result.Spread = &spread
	}

	if req.Baseline != nil {
		// With repeated runs the comparison is against the worst case
		// observed, not the last sample: a flaky estimate must not make a
		// cost check pass by luck.
		current := result.Cost
		if result.Spread != nil {
			current = result.Spread.Max
		}
		comparison := CompareCost(req.Baseline.Cost, current, req.MaxIncreasePercent)
		result.Comparison = &comparison
	}
	return result, nil
}

// checkBaseline rejects a baseline that does not describe the call being
// simulated.
//
// A baseline is a report of one contract's one function. Its costs say
// nothing about another call, so comparing them would report a change that
// never happened — which is worse than no comparison at all, because it looks
// like a measurement.
func checkBaseline(req SimulateRequest) error {
	if req.Baseline == nil {
		return nil
	}
	if req.Baseline.ContractID == "" || req.Baseline.Function == "" {
		return fmt.Errorf("%w: the baseline must name the contract and function it was taken against",
			ErrBaselineMismatch)
	}
	if req.Baseline.ContractID != req.ContractID {
		return fmt.Errorf("%w: the baseline is for contract %s, not %s",
			ErrBaselineMismatch, req.Baseline.ContractID, req.ContractID)
	}
	if req.Baseline.Function != req.Function {
		return fmt.Errorf("%w: the baseline is for function %s, not %s",
			ErrBaselineMismatch, req.Baseline.Function, req.Function)
	}
	return nil
}

// typedArgs is the outcome of typing a request's arguments.
type typedArgs struct {
	args      []string
	signature string
	note      string
	changed   bool
}

// typeArgs types req.Args from the contract's interface when an ABI source
// is configured.
//
// A contract the source does not know, or a source that cannot be reached,
// falls back to inference with a note, because the probe is still worth
// running. A function the interface does not export, or the wrong number of
// arguments, is a definite error: the simulation could only fail, and less
// helpfully.
func (p *Prober) typeArgs(ctx context.Context, req SimulateRequest) (typedArgs, error) {
	out := typedArgs{args: req.Args}
	if p.abi == nil {
		return out, nil
	}
	typer, ok := p.codec.(interface{ HasType(string) bool })
	if !ok {
		// A custom codec with its own syntax: SoroProbe cannot tell which
		// arguments already carry a type, so it does not rewrite any.
		return out, nil
	}

	iface, err := p.abi.Interface(ctx, p.network, req.ContractID)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if errors.Is(err, abi.ErrUnknownContract) {
			out.note = "contract not found in the interface registry; argument types were inferred"
		} else if errors.Is(err, abi.ErrNoWasm) {
			out.note = "stellar asset contract has no WASM interface; argument types were inferred"
		} else {
			out.note = "interface registry unavailable; argument types were inferred: " + err.Error()
		}
		p.log.Debug("abi lookup failed", "contract", req.ContractID, "err", err)
		return out, nil
	}

	fn, err := iface.Lookup(req.Function)
	if err != nil {
		return out, err
	}

	args, err := abi.Annotate(fn, req.Args, typer.HasType)
	if err != nil {
		return out, err
	}

	out.args = args
	out.signature = fn.Signature()
	for i := range args {
		if args[i] != req.Args[i] {
			out.changed = true
			break
		}
	}
	return out, nil
}

func (p *Prober) buildSimulateResult(req SimulateRequest, resp protocol.SimulateTransactionResponse) (*SimulateResult, error) {
	result := &SimulateResult{
		ContractID:   req.ContractID,
		Function:     req.Function,
		Args:         req.Args,
		Success:      resp.Error == "",
		Error:        resp.Error,
		LatestLedger: resp.LatestLedger,
		Cost:         Cost{MinResourceFee: resp.MinResourceFee},
	}

	if resp.RestorePreamble != nil {
		result.RestoreRequired = &RestoreInfo{
			MinResourceFee:     resp.RestorePreamble.MinResourceFee,
			TransactionDataXDR: resp.RestorePreamble.TransactionDataXDR,
		}
		var data xdr.SorobanTransactionData
		if err := xdr.SafeUnmarshalBase64(resp.RestorePreamble.TransactionDataXDR, &data); err != nil {
			return nil, fmt.Errorf("decode restore transaction data: %w", err)
		}
		for _, key := range data.Resources.Footprint.ReadWrite {
			result.RestoreRequired.ArchivedEntries = append(result.RestoreRequired.ArchivedEntries, describeLedgerKey(key))
		}
	}

	if resp.TransactionDataXDR != "" {
		var data xdr.SorobanTransactionData
		if err := xdr.SafeUnmarshalBase64(resp.TransactionDataXDR, &data); err != nil {
			return nil, fmt.Errorf("decode transaction data: %w", err)
		}
		result.Cost.Instructions = uint32(data.Resources.Instructions)
		result.Cost.DiskReadBytes = uint32(data.Resources.DiskReadBytes)
		result.Cost.WriteBytes = uint32(data.Resources.WriteBytes)
		result.Cost.ResourceFee = int64(data.ResourceFee)
		result.Footprint = describeFootprint(data.Resources.Footprint)
	}

	if len(resp.Results) > 0 && resp.Results[0].ReturnValueXDR != nil {
		raw := *resp.Results[0].ReturnValueXDR
		result.ReturnValueXDR = raw

		var val xdr.ScVal
		if err := xdr.SafeUnmarshalBase64(raw, &val); err != nil {
			return nil, fmt.Errorf("decode return value: %w", err)
		}
		decoded, err := p.codec.Decode(val)
		if err != nil {
			return nil, fmt.Errorf("decode return value: %w", err)
		}
		result.ReturnValue = decoded
	}

	if len(resp.Results) > 0 && resp.Results[0].AuthXDR != nil && len(*resp.Results[0].AuthXDR) > 0 {
		result.Auth = p.decodeAuthEntries(*resp.Results[0].AuthXDR)
	}

	result.Cost.EstimatedTotalFee = EstimateTotalFee(result.Cost.ResourceFee, p.baseFee)

	events, err := p.decodeEvents(resp.EventsXDR)
	if err != nil {
		// Diagnostic events are advisory. Losing them must not mask an
		// otherwise usable simulation result.
		p.log.Debug("could not decode diagnostic events", "err", err)
	} else {
		result.Events = events
	}

	return result, nil
}

func (p *Prober) decodeAuthEntries(raw []string) []AuthEntry {
	if len(raw) == 0 {
		return nil
	}
	out := make([]AuthEntry, 0, len(raw))
	for _, b64 := range raw {
		var entry xdr.SorobanAuthorizationEntry
		if err := xdr.SafeUnmarshalBase64(b64, &entry); err != nil {
			out = append(out, AuthEntry{RawXDR: b64})
			continue
		}

		ae := AuthEntry{}
		switch entry.Credentials.Type {
		case xdr.SorobanCredentialsTypeSorobanCredentialsSourceAccount:
			ae.Address = p.source
			ae.IsSourceAccount = true
		case xdr.SorobanCredentialsTypeSorobanCredentialsAddress:
			if entry.Credentials.Address != nil {
				if s, err := entry.Credentials.Address.Address.String(); err == nil {
					ae.Address = s
				}
			}
			ae.IsSourceAccount = (ae.Address != "" && ae.Address == p.source)
		case xdr.SorobanCredentialsTypeSorobanCredentialsAddressV2:
			if entry.Credentials.AddressV2 != nil {
				if s, err := entry.Credentials.AddressV2.Address.String(); err == nil {
					ae.Address = s
				}
			}
			ae.IsSourceAccount = (ae.Address != "" && ae.Address == p.source)
		case xdr.SorobanCredentialsTypeSorobanCredentialsAddressWithDelegates:
			if entry.Credentials.AddressWithDelegates != nil {
				if s, err := entry.Credentials.AddressWithDelegates.AddressCredentials.Address.String(); err == nil {
					ae.Address = s
				}
			}
			ae.IsSourceAccount = (ae.Address != "" && ae.Address == p.source)
		default:
			ae.IsSourceAccount = false
		}

		inv, err := p.decodeAuthorizedInvocation(entry.RootInvocation)
		if err != nil {
			out = append(out, AuthEntry{RawXDR: b64})
			continue
		}
		ae.RootInvocation = &inv
		out = append(out, ae)
	}
	return out
}

func (p *Prober) decodeAuthorizedInvocation(inv xdr.SorobanAuthorizedInvocation) (AuthorizedInvocation, error) {
	out := AuthorizedInvocation{}
	switch inv.Function.Type {
	case xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeContractFn:
		fn := inv.Function.ContractFn
		if fn != nil {
			if s, err := fn.ContractAddress.String(); err == nil {
				out.ContractID = s
			}
			out.Function = string(fn.FunctionName)
			for _, arg := range fn.Args {
				decoded, err := p.codec.Decode(arg)
				if err != nil {
					return out, err
				}
				out.Args = append(out.Args, decoded)
			}
		}
	case xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeCreateContractHostFn:
		out.Function = "create_contract"
	case xdr.SorobanAuthorizedFunctionTypeSorobanAuthorizedFunctionTypeCreateContractV2HostFn:
		out.Function = "create_contract_v2"
	default:
		return out, fmt.Errorf("unknown authorized function type: %v", inv.Function.Type)
	}

	for _, sub := range inv.SubInvocations {
		subInv, err := p.decodeAuthorizedInvocation(sub)
		if err != nil {
			return out, err
		}
		out.SubInvocations = append(out.SubInvocations, subInv)
	}
	return out, nil
}

func (p *Prober) decodeEvents(raw []string) ([]Event, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	events := make([]Event, 0, len(raw))

	for i, b64 := range raw {
		var de xdr.DiagnosticEvent
		if err := xdr.SafeUnmarshalBase64(b64, &de); err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}

		event := Event{
			Type:                     de.Event.Type.String(),
			InSuccessfulContractCall: de.InSuccessfulContractCall,
		}
		if de.Event.ContractId != nil {
			addr := xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: de.Event.ContractId,
			}
			if s, err := addr.String(); err == nil {
				event.ContractID = s
			}
		}

		for _, topic := range de.Event.Body.V0.Topics {
			decoded, err := p.codec.Decode(topic)
			if err != nil {
				return nil, fmt.Errorf("event %d topic: %w", i, err)
			}
			event.Topics = append(event.Topics, decoded)
		}
		data, err := p.codec.Decode(de.Event.Body.V0.Data)
		if err != nil {
			return nil, fmt.Errorf("event %d data: %w", i, err)
		}
		event.Data = data

		events = append(events, event)
	}
	return events, nil
}

func describeFootprint(fp xdr.LedgerFootprint) Footprint {
	out := Footprint{ReadOnly: []string{}, ReadWrite: []string{}}
	for _, key := range fp.ReadOnly {
		out.ReadOnly = append(out.ReadOnly, describeLedgerKey(key))
	}
	for _, key := range fp.ReadWrite {
		out.ReadWrite = append(out.ReadWrite, describeLedgerKey(key))
	}
	return out
}

// describeLedgerKey renders a ledger key as a short human-readable label.
func describeLedgerKey(key xdr.LedgerKey) string {
	switch key.Type {
	case xdr.LedgerEntryTypeContractData:
		cd := key.ContractData
		addr, err := cd.Contract.String()
		if err != nil {
			addr = "<unknown contract>"
		}
		kind := "data"
		if cd.Key.Type == xdr.ScValTypeScvLedgerKeyContractInstance {
			kind = "instance"
		}
		return fmt.Sprintf("contract %s %s (%s)", kind, addr, durabilityName(cd.Durability))

	case xdr.LedgerEntryTypeContractCode:
		return fmt.Sprintf("contract code %x", key.ContractCode.Hash[:8])

	case xdr.LedgerEntryTypeAccount:
		return fmt.Sprintf("account %s", key.Account.AccountId.Address())

	case xdr.LedgerEntryTypeTrustline:
		return "trustline"

	case xdr.LedgerEntryTypeTtl:
		return "ttl entry"

	default:
		return key.Type.String()
	}
}

func durabilityName(d xdr.ContractDataDurability) string {
	if d == xdr.ContractDataDurabilityTemporary {
		return "temporary"
	}
	return "persistent"
}
