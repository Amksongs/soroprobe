package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/scval"
)

// CheckRequest describes a combined health check.
type CheckRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id" yaml:"contract_id"`
	// Function, when set, names a read-only function to simulate as a
	// liveness probe.
	//
	// It is optional on purpose. SoroProbe cannot discover a contract's
	// exported functions from the ledger, and inventing a name would produce a
	// misleading failure, so the simulation step is reported as skipped rather
	// than guessed at.
	Function string `json:"function,omitempty" yaml:"function,omitempty"`
	// Args are argument specifications for Function.
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
	// Expect is an optional argument specification for the expected return value.
	Expect string `json:"expect,omitempty" yaml:"expect,omitempty"`
	// DataKeys are optional data entries to include in the TTL assessment.
	DataKeys []string `json:"data_keys,omitempty" yaml:"data_keys,omitempty"`
	// DataDurability selects the durability DataKeys are read at.
	DataDurability health.Durability `json:"data_durability,omitempty" yaml:"data_durability,omitempty"`
	// MaxInstructions bounds CPU instructions consumed by the simulation.
	MaxInstructions *uint32 `json:"max_instructions,omitempty" yaml:"max_instructions,omitempty"`
	// MaxFeeStroops bounds the resource fee in stroops consumed by the simulation.
	MaxFeeStroops *int64 `json:"max_fee,omitempty" yaml:"max_fee,omitempty"`
	// MaxWriteBytes bounds write bytes consumed by the simulation.
	MaxWriteBytes *uint32 `json:"max_write_bytes,omitempty" yaml:"max_write_bytes,omitempty"`
}

// CheckOutcome is the result of one individual check.
type CheckOutcome string

const (
	// OutcomePass means the check succeeded.
	OutcomePass CheckOutcome = "pass"
	// OutcomeWarn means the check succeeded but something needs attention.
	OutcomeWarn CheckOutcome = "warn"
	// OutcomeFail means the check failed.
	OutcomeFail CheckOutcome = "fail"
	// OutcomeSkip means the check was not run.
	OutcomeSkip CheckOutcome = "skip"
)

// Check is one named assertion about a contract.
type Check struct {
	Name    string       `json:"name"`
	Outcome CheckOutcome `json:"outcome"`
	Detail  string       `json:"detail"`
}

// CheckResult aggregates the individual checks.
type CheckResult struct {
	ContractID string `json:"contract_id"`
	// OK is false when any check failed. The CLI exits non-zero on false.
	OK bool `json:"ok"`
	// Status is the worst state-health status observed.
	Status health.Status `json:"status"`
	// Checks lists each assertion in the order it was evaluated.
	Checks []Check `json:"checks"`
	// Inspect is the full state-health report the checks were derived from.
	Inspect *InspectResult `json:"inspect,omitempty"`
	// Simulate is the invocation result, when a function was given.
	Simulate *SimulateResult `json:"simulate,omitempty"`
	// LatestLedger is the ledger the assessment was made against.
	LatestLedger uint32 `json:"latest_ledger"`
	// CloseTimeNote explains the measured duration estimate or its fallback.
	CloseTimeNote string `json:"close_time_note"`
}

// CheckStep is one pluggable step in the health check pipeline.
//
// Run receives the request and the state gathered once by Check — the inspect
// report and, when one was needed, the simulation — and returns the checks it
// contributes, in the order they should be reported. A step that has nothing to
// say returns no checks; a step that does not apply usually returns a single
// OutcomeSkip check so the report still names it.
//
// Returning errStop ends the pipeline after the step's checks are recorded. Any
// other error aborts Check.
type CheckStep interface {
	Name() string
	Run(ctx context.Context, req CheckRequest, inspect *InspectResult, simulate *SimulateResult) ([]Check, error)
}

// errStop ends the pipeline once a step's checks are recorded. Only the
// deployment step uses it: with no instance there is nothing further worth
// probing, which is the one early stop the checks have always had. Other
// failures are still reported in full.
var errStop = errors.New("stop checking")

// checkSteps is the ordered list Check runs. To add a check, implement
// CheckStep and append it here at the position its result should appear; a step
// reads the inspect and simulation state Check gathered once, so it cannot add
// an RPC call by itself. It is built per Prober because some steps, such as the
// return-value assertion, need the prober's argument codec.
func (p *Prober) checkSteps() []CheckStep {
	return []CheckStep{
		&deployedStep{},
		&instanceTTLStep{},
		&codeTTLStep{},
		&dataTTLStep{},
		&simulateStep{},
		&expectStep{p: p},
	}
}

// expectStep asserts the simulated call's return value when the request names
// an expected value. Without one it contributes nothing.
type expectStep struct{ p *Prober }

func (s *expectStep) Name() string { return "expect" }

func (s *expectStep) Run(_ context.Context, req CheckRequest, _ *InspectResult, simulate *SimulateResult) ([]Check, error) {
	if req.Expect == "" {
		return nil, nil
	}
	return []Check{s.p.expectCheck(req, &CheckResult{Simulate: simulate})}, nil
}

// Check runs the combined health check: is the contract deployed, are its
// instance, code and named data entries live and not near expiration, and — if
// a function was given — does a read-only call simulate successfully.
//
// The work is done by the ordered steps in checkSteps. Check gathers the
// inspect report and the simulation once and hands both to every step, so
// adding a step cannot accidentally add an RPC call.
func (p *Prober) Check(ctx context.Context, req CheckRequest) (*CheckResult, error) {
	inspect, err := p.Inspect(ctx, InspectRequest{
		ContractID:     req.ContractID,
		DataKeys:       req.DataKeys,
		DataDurability: req.DataDurability,
	})
	if err != nil {
		return nil, err
	}

	// Gather the simulation once, and only when a step can use it: an
	// undeployed contract fails the first step, and a check without a function
	// never reports a simulation, so in both cases calling Simulate would spend
	// an RPC call whose result no step reads.
	var sim *SimulateResult
	if inspect.Deployed && req.Function != "" {
		sim, err = p.Simulate(ctx, SimulateRequest{
			ContractID: req.ContractID,
			Function:   req.Function,
			Args:       req.Args,
		})
		if err != nil {
			return nil, err
		}
	}

	result := &CheckResult{
		ContractID:    req.ContractID,
		Inspect:       inspect,
		Status:        inspect.Status,
		Simulate:      sim,
		LatestLedger:  inspect.LatestLedger,
		CloseTimeNote: inspect.CloseTimeNote,
	}

	for _, step := range p.checkSteps() {
		checks, err := step.Run(ctx, req, inspect, sim)
		result.Checks = append(result.Checks, checks...)
		if err != nil {
			if errors.Is(err, errStop) {
				break
			}
			return nil, err
		}
	}

	result.OK = true
	for _, c := range result.Checks {
		if c.Outcome == OutcomeFail {
			result.OK = false
			break
		}
	}
	return result, nil
}

// ttlCheck converts a TTL assessment into a pass/warn/fail outcome. A warning
// threshold breach is deliberately not a failure: it is a signal to extend the
// entry's TTL, not a reason to fail a CI pipeline.
func ttlCheck(name string, entry EntryReport) Check {
	outcome := OutcomeFail
	switch entry.Status {
	case health.StatusHealthy, health.StatusUnknown:
		outcome = OutcomePass
	case health.StatusWarning:
		outcome = OutcomeWarn
	}
	return Check{Name: name, Outcome: outcome, Detail: entry.Summary}
}

// --- step implementations ---

// deployedStep reports whether the contract has an instance on this network.
// It is the pipeline's precondition, so a missing instance stops the run.
type deployedStep struct{}

func (s *deployedStep) Name() string { return "deployed" }

func (s *deployedStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if !inspect.Deployed {
		return []Check{{
			Name:    "deployed",
			Outcome: OutcomeFail,
			Detail:  "no contract instance entry found on this network",
		}}, errStop
	}
	return []Check{{
		Name:    "deployed",
		Outcome: OutcomePass,
		Detail:  fmt.Sprintf("instance found, executable is %s", inspect.Executable),
	}}, nil
}

// instanceTTLStep reports the contract instance's expiration health.
type instanceTTLStep struct{}

func (s *instanceTTLStep) Name() string { return "instance_ttl" }

func (s *instanceTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	return []Check{ttlCheck("instance_ttl", inspect.Instance)}, nil
}

// codeTTLStep reports the Wasm code entry's expiration health. Stellar Asset
// Contracts have no code entry, so the step is reported as skipped for them.
type codeTTLStep struct{}

func (s *codeTTLStep) Name() string { return "code_ttl" }

func (s *codeTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if inspect.Code != nil {
		return []Check{ttlCheck("code_ttl", *inspect.Code)}, nil
	}
	return []Check{{
		Name:    "code_ttl",
		Outcome: OutcomeSkip,
		Detail:  "stellar asset contracts are built into the host and have no code entry",
	}}, nil
}

// dataTTLStep reports one check per named data entry. With no keys it
// contributes nothing, matching the report's previous shape.
type dataTTLStep struct{}

func (s *dataTTLStep) Name() string { return "data_ttl" }

func (s *dataTTLStep) Run(_ context.Context, _ CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	checks := make([]Check, 0, len(inspect.Data))
	for _, entry := range inspect.Data {
		checks = append(checks, ttlCheck("data_ttl:"+entry.Key, entry))
	}
	return checks, nil
}

// simulateStep reports the read-only call's outcome. Without a function there
// is nothing to assert, so it is reported as skipped.
type simulateStep struct{}

func (s *simulateStep) Name() string { return "simulate" }

func (s *simulateStep) Run(_ context.Context, req CheckRequest, _ *InspectResult, simulate *SimulateResult) ([]Check, error) {
	if req.Function == "" {
		return []Check{{
			Name:    "simulate",
			Outcome: OutcomeSkip,
			Detail:  "no function given; pass one to probe a read-only call",
		}}, nil
	}
	if simulate == nil {
		return nil, errors.New("simulate step has no simulation result")
	}
	if !simulate.Success {
		return []Check{{
			Name:    "simulate",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("calling %s failed: %s", req.Function, simulate.Error),
		}}, nil
	}
	return []Check{{
		Name:    "simulate",
		Outcome: OutcomePass,
		Detail: fmt.Sprintf("calling %s succeeded, %d instructions, min resource fee %d stroops",
			req.Function, simulate.Cost.Instructions, simulate.Cost.MinResourceFee),
	}}, nil
}

func (p *Prober) expectCheck(req CheckRequest, result *CheckResult) Check {
	if req.Function == "" {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  "an expected value was given but no function was specified",
		}
	}
	if result.Simulate == nil || !result.Simulate.Success {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  "cannot check return value because simulation failed or did not run",
		}
	}

	expectedVal, err := p.codec.Encode(req.Expect)
	if err != nil {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("invalid expected value %q: %v", req.Expect, err),
		}
	}

	var actualVal xdr.ScVal
	if result.Simulate.ReturnValueXDR != "" {
		if err := xdr.SafeUnmarshalBase64(result.Simulate.ReturnValueXDR, &actualVal); err != nil {
			return Check{
				Name:    "expect",
				Outcome: OutcomeFail,
				Detail:  fmt.Sprintf("decode actual return value: %v", err),
			}
		}
	} else {
		actualVal = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	}

	match, typeMismatch := compareScVals(expectedVal, actualVal)

	expDecoded, _ := p.codec.Decode(expectedVal)
	actDecoded := result.Simulate.ReturnValue
	expDisp := formatVal(expDecoded)
	actDisp := formatVal(actDecoded)

	if match {
		return Check{
			Name:    "expect",
			Outcome: OutcomePass,
			Detail:  fmt.Sprintf("expected %s, got %s", expDisp, actDisp),
		}
	}

	if typeMismatch {
		return Check{
			Name:    "expect",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("type mismatch: expected %s, got %s", expDisp, actDisp),
		}
	}

	return Check{
		Name:    "expect",
		Outcome: OutcomeFail,
		Detail:  fmt.Sprintf("expected %s, got %s", expDisp, actDisp),
	}
}

func compareScVals(expected, actual xdr.ScVal) (match bool, typeMismatch bool) {
	expInt, expIsInt := scval.ToBigInt(expected)
	actInt, actIsInt := scval.ToBigInt(actual)

	if expIsInt || actIsInt {
		if expIsInt && actIsInt {
			return expInt.Cmp(actInt) == 0, false
		}
		return false, true
	}

	if expected.Type != actual.Type {
		return false, true
	}

	expBytes, err1 := expected.MarshalBinary()
	actBytes, err2 := actual.MarshalBinary()
	if err1 == nil && err2 == nil {
		return bytes.Equal(expBytes, actBytes), false
	}

	return reflect.DeepEqual(expected, actual), false
}

func formatVal(v any) string {
	if v == nil {
		return "(void)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// costCheck evaluates resource costs of a simulation against configured limits.
// If no limits are set, or if simulation was not run or failed, costCheck returns false.
func costCheck(req CheckRequest, sim *SimulateResult) (Check, bool) {
	if req.MaxInstructions == nil && req.MaxFeeStroops == nil && req.MaxWriteBytes == nil {
		return Check{}, false
	}
	if sim == nil || !sim.Success {
		return Check{}, false
	}

	var breaches []string
	var details []string

	if req.MaxInstructions != nil {
		details = append(details, fmt.Sprintf("instructions %d (limit %d)", sim.Cost.Instructions, *req.MaxInstructions))
		if sim.Cost.Instructions > *req.MaxInstructions {
			breaches = append(breaches, fmt.Sprintf("instructions %d exceeds limit %d", sim.Cost.Instructions, *req.MaxInstructions))
		}
	}
	if req.MaxFeeStroops != nil {
		details = append(details, fmt.Sprintf("fee %d stroops (limit %d)", sim.Cost.MinResourceFee, *req.MaxFeeStroops))
		if sim.Cost.MinResourceFee > *req.MaxFeeStroops {
			breaches = append(breaches, fmt.Sprintf("fee %d stroops exceeds limit %d", sim.Cost.MinResourceFee, *req.MaxFeeStroops))
		}
	}
	if req.MaxWriteBytes != nil {
		details = append(details, fmt.Sprintf("write bytes %d (limit %d)", sim.Cost.WriteBytes, *req.MaxWriteBytes))
		if sim.Cost.WriteBytes > *req.MaxWriteBytes {
			breaches = append(breaches, fmt.Sprintf("write bytes %d exceeds limit %d", sim.Cost.WriteBytes, *req.MaxWriteBytes))
		}
	}

	if len(breaches) > 0 {
		return Check{
			Name:    "cost",
			Outcome: OutcomeFail,
			Detail:  strings.Join(breaches, ", "),
		}, true
	}

	return Check{
		Name:    "cost",
		Outcome: OutcomePass,
		Detail:  strings.Join(details, ", "),
	}, true
}
