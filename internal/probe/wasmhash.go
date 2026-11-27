package probe

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
)

// wasmHashStep compares the live code hash with the one a deployment registry
// recorded. It contributes nothing unless the request names an expected hash,
// so a plain check never grows a step that has nothing to assert.
type wasmHashStep struct{}

func (s *wasmHashStep) Name() string { return "wasm_hash" }

func (s *wasmHashStep) Run(_ context.Context, req CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if req.ExpectedWASMHash == "" {
		return nil, nil
	}

	want, err := normalizeWASMHash(req.ExpectedWASMHash)
	if err != nil {
		return []Check{{
			Name:    "wasm_hash",
			Outcome: OutcomeFail,
			Detail:  err.Error(),
		}}, nil
	}

	// A Stellar Asset Contract is implemented by the host and has no uploaded
	// bytecode, so there is no resource a recorded hash could describe.
	if inspect.WasmHash == "" {
		return []Check{{
			Name:    "wasm_hash",
			Outcome: OutcomeSkip,
			Detail:  "the contract has no code entry to compare, so there is nothing to drift",
		}}, nil
	}

	got, err := normalizeWASMHash(inspect.WasmHash)
	if err != nil {
		return []Check{{
			Name:    "wasm_hash",
			Outcome: OutcomeFail,
			Detail:  fmt.Sprintf("the deployed code hash %q is not a 32-byte hex hash: %v", inspect.WasmHash, err),
		}}, nil
	}

	if got == want {
		return []Check{{
			Name:    "wasm_hash",
			Outcome: OutcomePass,
			Detail:  fmt.Sprintf("the deployed code matches the recorded hash %s", got),
		}}, nil
	}

	return []Check{{
		Name:    "wasm_hash",
		Outcome: OutcomeFail,
		Detail: fmt.Sprintf("drift: the registry recorded %s but the deployed code is %s",
			want, got),
	}}, nil
}

// normalizeWASMHash lowercases a hash and drops an optional 0x prefix, because
// a registry and the chain can spell the same hash differently. The value must
// still be exactly 32 bytes of hex, so a truncated or malformed hash is
// rejected rather than compared as a string that can never match.
func normalizeWASMHash(hash string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(hash))
	value = strings.TrimPrefix(value, "0x")
	if len(value) != 64 {
		return "", fmt.Errorf("hash %q is not a 32-byte hex hash: want 64 hex characters, got %d", hash, len(value))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("hash %q is not hex: %w", hash, err)
	}
	return value, nil
}
