package probe

import (
	"context"
	"errors"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/abi"
)

// maxTokenDecimals is the largest decimals value SEP-41 tokens may report:
// balances are i128, which holds 38 decimal digits.
const maxTokenDecimals = 38

// standardStep verifies the contract's exported interface against a published
// standard. Without a standard in the request it contributes nothing.
//
// The interface is read from the configured ABI source, or from the contract's
// Wasm on chain when none is configured, so the check works with no setup.
type standardStep struct{ p *Prober }

func (s *standardStep) Name() string { return "standard" }

func (s *standardStep) Run(ctx context.Context, req CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if req.Standard == "" {
		return nil, nil
	}
	std, err := abi.LookupStandard(req.Standard)
	if err != nil {
		return nil, err
	}
	prefix := "standard:" + std.Name

	// A Stellar Asset Contract is implemented by the host and has no Wasm
	// interface to read. Its token interface is SEP-41 by construction.
	if inspect.Executable == "stellar_asset" {
		return []Check{{
			Name:    prefix,
			Outcome: OutcomeSkip,
			Detail:  "stellar asset contracts are implemented by the host and have no Wasm interface to verify",
		}}, nil
	}

	var iface *abi.Interface
	if s.p.abi != nil {
		iface, err = s.p.abi.Interface(ctx, s.p.network, req.ContractID)
	} else {
		iface, err = s.p.Interface(ctx, req.ContractID)
	}
	switch {
	case errors.Is(err, abi.ErrUnknownContract), errors.Is(err, abi.ErrNoWasm):
		return []Check{{
			Name:    prefix,
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("the contract's interface could not be read: %v", err),
		}}, nil
	case err != nil:
		return nil, err
	}

	deviations := std.Verify(iface)
	failed := map[string]bool{}
	var checks []Check
	for _, d := range deviations {
		failed[d.Function] = true
		checks = append(checks, Check{
			Name:    prefix + ":" + d.Function,
			Outcome: OutcomeFail,
			Detail:  d.Problem,
		})
	}
	if len(deviations) == 0 {
		checks = append(checks, Check{
			Name:    prefix,
			Outcome: OutcomePass,
			Detail:  fmt.Sprintf("all %d %s functions are exported with the standard signatures", len(std.Functions), std.Name),
		})
	}

	// Signatures can match while behavior does not, so the zero-argument
	// metadata calls are also run. A function that already deviates is not
	// simulated: its failure would only repeat the same problem.
	for _, fn := range std.Functions {
		validate, ok := metadataValidators[fn.Name]
		if !ok || failed[fn.Name] || len(fn.Inputs) != 0 {
			continue
		}
		check, err := s.simulateMetadata(ctx, req.ContractID, prefix+":"+fn.Name+":simulate", fn.Name, validate)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (s *standardStep) simulateMetadata(ctx context.Context, contractID, name, fn string, validate func(xdr.ScVal) error) (Check, error) {
	sim, err := s.p.Simulate(ctx, SimulateRequest{ContractID: contractID, Function: fn})
	if err != nil {
		return Check{}, err
	}
	if !sim.Success {
		return Check{Name: name, Outcome: OutcomeFail, Detail: fmt.Sprintf("calling %s failed: %s", fn, sim.Error)}, nil
	}
	var val xdr.ScVal
	if sim.ReturnValueXDR == "" {
		val = xdr.ScVal{Type: xdr.ScValTypeScvVoid}
	} else if err := xdr.SafeUnmarshalBase64(sim.ReturnValueXDR, &val); err != nil {
		return Check{Name: name, Outcome: OutcomeFail, Detail: fmt.Sprintf("decode %s return value: %v", fn, err)}, nil
	}
	if err := validate(val); err != nil {
		return Check{Name: name, Outcome: OutcomeFail, Detail: fmt.Sprintf("%s: %v", fn, err)}, nil
	}
	return Check{Name: name, Outcome: OutcomePass, Detail: fmt.Sprintf("calling %s returned %s", fn, formatVal(sim.ReturnValue))}, nil
}

// metadataValidators check the values the SEP-41 metadata functions return.
var metadataValidators = map[string]func(xdr.ScVal) error{
	"decimals": func(v xdr.ScVal) error {
		if v.Type != xdr.ScValTypeScvU32 || v.U32 == nil {
			return fmt.Errorf("returned %s, expected a u32", v.Type)
		}
		if *v.U32 > maxTokenDecimals {
			return fmt.Errorf("returned %d, which exceeds the maximum of %d", *v.U32, maxTokenDecimals)
		}
		return nil
	},
	"name":   nonEmptyString,
	"symbol": nonEmptyString,
}

func nonEmptyString(v xdr.ScVal) error {
	if v.Type != xdr.ScValTypeScvString || v.Str == nil {
		return fmt.Errorf("returned %s, expected a string", v.Type)
	}
	if len(*v.Str) == 0 {
		return errors.New("returned an empty string")
	}
	return nil
}
