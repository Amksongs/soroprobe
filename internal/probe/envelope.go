package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ErrNotSorobanEnvelope reports an envelope SoroProbe cannot simulate.
var ErrNotSorobanEnvelope = errors.New("not a single-operation Soroban invocation")

// EnvelopeRequest simulates a transaction envelope exactly as given, such as
// one a wallet or dApp built.
type EnvelopeRequest struct {
	// EnvelopeXDR is the base64 TransactionEnvelope.
	EnvelopeXDR string
	// Trace builds the call tree from the simulation's diagnostic events.
	Trace bool
}

// SimulateEnvelope simulates a prebuilt envelope as given and decodes the
// result, cost, footprint and authorizations the same way Simulate does.
//
// simulateTransaction accepts one operation, and only an InvokeHostFunction
// operation runs a contract, so anything else is rejected with an
// explanation rather than sent. A contract call's arguments are decoded,
// and named from the target contract's interface when it can be read.
func (p *Prober) SimulateEnvelope(ctx context.Context, req EnvelopeRequest) (*SimulateResult, error) {
	b64 := strings.TrimSpace(req.EnvelopeXDR)
	var env xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(b64, &env); err != nil {
		return nil, fmt.Errorf("%w: the input is not a base64 transaction envelope (%w)", ErrNotSorobanEnvelope, err)
	}
	ops := env.Operations()
	if len(ops) != 1 {
		return nil, fmt.Errorf("%w: the envelope has %d operations; Soroban transactions carry exactly one", ErrNotSorobanEnvelope, len(ops))
	}
	invoke, ok := ops[0].Body.GetInvokeHostFunctionOp()
	if !ok {
		return nil, fmt.Errorf("%w: its operation is %s, which runs no contract", ErrNotSorobanEnvelope, ops[0].Body.Type)
	}

	call := SimulateRequest{}
	var decoded []any
	switch invoke.HostFunction.Type {
	case xdr.HostFunctionTypeHostFunctionTypeInvokeContract:
		args := invoke.HostFunction.MustInvokeContract()
		id, err := args.ContractAddress.String()
		if err != nil {
			return nil, fmt.Errorf("decode contract address: %w", err)
		}
		call.ContractID, call.Function = id, string(args.FunctionName)
		decoded = make([]any, 0, len(args.Args))
		for _, a := range args.Args {
			v := decodeOrRaw(a)
			decoded = append(decoded, v)
			text, _ := json.Marshal(v)
			call.Args = append(call.Args, string(text))
		}
	case xdr.HostFunctionTypeHostFunctionTypeUploadContractWasm:
		call.Function = "upload_contract_wasm"
	default:
		call.Function = "create_contract"
	}

	resp, err := p.client.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: b64})
	if err != nil {
		return nil, err
	}
	result, err := p.buildSimulateResult(call, resp)
	if err != nil {
		return nil, err
	}
	result.FromEnvelope = true
	result.DecodedArgs = decoded
	result.Source = env.SourceAccount().ToAccountId().Address()

	if call.ContractID != "" {
		if iface, err := p.chain.Interface(ctx, p.network, call.ContractID); err == nil {
			if fn, err := iface.Lookup(call.Function); err == nil {
				result.Signature = fn.Signature()
			}
		} else {
			result.ABINote = "the contract interface could not be read, so arguments are shown without names: " + err.Error()
		}
	}
	if req.Trace {
		if err := p.addTrace(ctx, result, resp.EventsXDR); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// addTrace builds the call tree from a simulation's events and names each
// frame's arguments.
func (p *Prober) addTrace(ctx context.Context, result *SimulateResult, events []string) error {
	frames, err := buildCallTrace(events)
	if err != nil {
		return err
	}
	p.nameTraceArgs(ctx, frames)
	if frames == nil {
		frames = []*TraceFrame{}
	}
	result.Trace = frames
	return nil
}
