package probe

import (
	"context"
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/scval"
)

// TraceFrame is one contract call in a simulated call tree.
type TraceFrame struct {
	// Contract is the called contract ("C...").
	Contract string `json:"contract"`
	// Function is the called function.
	Function string `json:"function"`
	// Args are the decoded arguments.
	Args []any `json:"args"`
	// ArgNames are the parameter names from the contract's interface, when
	// it could be read; they line up with Args.
	ArgNames []string `json:"arg_names,omitempty"`
	// Returned is true when the call returned; Return is its value.
	Returned bool `json:"returned"`
	Return   any  `json:"return,omitempty"`
	// Error is the error raised in this frame, such as
	// "Error(Contract, #3)", with any message the host attached.
	Error string `json:"error,omitempty"`
	// Calls are the calls this frame made, in order.
	Calls []*TraceFrame `json:"calls,omitempty"`
}

// buildCallTrace rebuilds the call tree from a simulation's diagnostic
// events: every fn_call opens a frame under the innermost open one, the
// matching fn_return closes it, and an error event is recorded on the frame
// that was running when it was raised.
func buildCallTrace(raw []string) ([]*TraceFrame, error) {
	var roots []*TraceFrame
	var stack []*TraceFrame
	for _, b64 := range raw {
		var ev xdr.DiagnosticEvent
		if err := xdr.SafeUnmarshalBase64(b64, &ev); err != nil {
			return nil, fmt.Errorf("decode diagnostic event: %w", err)
		}
		body, ok := ev.Event.Body.GetV0()
		if !ok || len(body.Topics) == 0 {
			continue
		}
		kind, ok := body.Topics[0].GetSym()
		if !ok {
			continue
		}
		switch string(kind) {
		case "fn_call":
			if len(body.Topics) < 3 {
				continue
			}
			fn, _ := body.Topics[2].GetSym()
			frame := &TraceFrame{
				Contract: contractOf(body.Topics[1]),
				Function: string(fn),
				Args:     callArgs(body.Data),
			}
			if n := len(stack); n > 0 {
				stack[n-1].Calls = append(stack[n-1].Calls, frame)
			} else {
				roots = append(roots, frame)
			}
			stack = append(stack, frame)
		case "fn_return":
			if len(stack) == 0 {
				continue
			}
			fn := ""
			if len(body.Topics) > 1 {
				if s, ok := body.Topics[1].GetSym(); ok {
					fn = string(s)
				}
			}
			// Close up to the frame this return belongs to; frames above it
			// trapped without returning.
			for i := len(stack) - 1; i >= 0; i-- {
				if fn == "" || stack[i].Function == fn {
					stack[i].Returned = true
					stack[i].Return = decodeOrRaw(body.Data)
					stack = stack[:i]
					break
				}
			}
		case "error":
			if len(stack) == 0 {
				continue
			}
			frame := stack[len(stack)-1]
			if frame.Error != "" {
				// The host repeats an error as it unwinds; the innermost
				// frame keeps the first report.
				continue
			}
			frame.Error = errorText(body)
		}
	}
	return roots, nil
}

// contractOf renders a fn_call's contract topic, which the host writes as
// the contract's 32-byte hash, or as an address.
func contractOf(v xdr.ScVal) string {
	if b, ok := v.GetBytes(); ok && len(b) == 32 {
		if s, err := strkey.Encode(strkey.VersionByteContract, b); err == nil {
			return s
		}
	}
	if a, ok := v.GetAddress(); ok {
		if s, err := a.String(); err == nil {
			return s
		}
	}
	return fmt.Sprint(decodeOrRaw(v))
}

// callArgs decodes a fn_call's data: the arguments as a vec, or the only
// argument itself.
func callArgs(v xdr.ScVal) []any {
	if v.Type == xdr.ScValTypeScvVoid {
		return []any{}
	}
	if vec, ok := v.GetVec(); ok && vec != nil {
		out := make([]any, 0, len(*vec))
		for _, item := range *vec {
			out = append(out, decodeOrRaw(item))
		}
		return out
	}
	return []any{decodeOrRaw(v)}
}

func decodeOrRaw(v xdr.ScVal) any {
	decoded, err := scval.Decode(v)
	if err != nil {
		return v.Type.String()
	}
	return decoded
}

// errorText renders an error event: its Error(Type, Code) topic and the
// host's message, when there is one.
func errorText(body xdr.ContractEventV0) string {
	text := ""
	if len(body.Topics) > 1 {
		if e, ok := body.Topics[1].GetError(); ok {
			text = formatScError(e)
		}
	}
	msg := decodeOrRaw(body.Data)
	if items, ok := msg.([]any); ok && len(items) > 0 {
		msg = items[0]
	}
	if s, ok := msg.(string); ok && s != "" {
		if text == "" {
			return s
		}
		return text + ": " + s
	}
	return text
}

// formatScError writes an ScError the way the host prints it.
func formatScError(e xdr.ScError) string {
	typ := scErrorTypeNames[e.Type]
	if typ == "" {
		typ = e.Type.String()
	}
	if e.Type == xdr.ScErrorTypeSceContract && e.ContractCode != nil {
		return fmt.Sprintf("Error(%s, #%d)", typ, *e.ContractCode)
	}
	code := ""
	if e.Code != nil {
		code = scErrorCodeNames[*e.Code]
		if code == "" {
			code = e.Code.String()
		}
	}
	return fmt.Sprintf("Error(%s, %s)", typ, code)
}

var scErrorTypeNames = map[xdr.ScErrorType]string{
	xdr.ScErrorTypeSceContract: "Contract", xdr.ScErrorTypeSceWasmVm: "WasmVm",
	xdr.ScErrorTypeSceContext: "Context", xdr.ScErrorTypeSceStorage: "Storage",
	xdr.ScErrorTypeSceObject: "Object", xdr.ScErrorTypeSceCrypto: "Crypto",
	xdr.ScErrorTypeSceEvents: "Events", xdr.ScErrorTypeSceBudget: "Budget",
	xdr.ScErrorTypeSceValue: "Value", xdr.ScErrorTypeSceAuth: "Auth",
}

var scErrorCodeNames = map[xdr.ScErrorCode]string{
	xdr.ScErrorCodeScecArithDomain: "ArithDomain", xdr.ScErrorCodeScecIndexBounds: "IndexBounds",
	xdr.ScErrorCodeScecInvalidInput: "InvalidInput", xdr.ScErrorCodeScecMissingValue: "MissingValue",
	xdr.ScErrorCodeScecExistingValue: "ExistingValue", xdr.ScErrorCodeScecExceededLimit: "ExceededLimit",
	xdr.ScErrorCodeScecInvalidAction: "InvalidAction", xdr.ScErrorCodeScecInternalError: "InternalError",
	xdr.ScErrorCodeScecUnexpectedType: "UnexpectedType", xdr.ScErrorCodeScecUnexpectedSize: "UnexpectedSize",
}

// nameTraceArgs adds parameter names to every frame whose contract's
// interface can be read. A contract without one (a Stellar Asset Contract,
// say) keeps unnamed arguments.
func (p *Prober) nameTraceArgs(ctx context.Context, frames []*TraceFrame) {
	for _, f := range frames {
		if iface, err := p.chain.Interface(ctx, p.network, f.Contract); err == nil {
			if fn, err := iface.Lookup(f.Function); err == nil && len(fn.Inputs) == len(f.Args) {
				for _, in := range fn.Inputs {
					f.ArgNames = append(f.ArgNames, in.Name)
				}
			}
		}
		p.nameTraceArgs(ctx, f.Calls)
	}
}
