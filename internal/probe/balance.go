package probe

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/scval"
)

// BalanceRequest asks for the balances of several holders of one token.
type BalanceRequest struct {
	// Token is the strkey contract address of a SEP-41 token or a Stellar
	// Asset Contract.
	Token string `json:"token"`
	// Holders are account ("G...") or contract ("C...") addresses.
	Holders []string `json:"holders"`
}

// HolderBalance is one holder's balance.
type HolderBalance struct {
	Address string `json:"address"`
	// Raw is the balance in the token's smallest unit, as the contract
	// reports it. It is a string because i128 does not fit a JSON number.
	Raw string `json:"raw,omitempty"`
	// Balance is Raw divided by 10^decimals.
	Balance string `json:"balance,omitempty"`
	// Error explains why this holder's balance could not be read. One bad
	// address does not stop the others.
	Error string `json:"error,omitempty"`
}

// BalanceResult is the outcome of a balance read.
type BalanceResult struct {
	Token    string          `json:"token"`
	Symbol   string          `json:"symbol,omitempty"`
	Decimals uint32          `json:"decimals"`
	Balances []HolderBalance `json:"balances"`
}

// Balance reads token balances by simulating the token's own read-only
// decimals, symbol and balance functions, so it works for any SEP-41 token and
// for Stellar Asset Contracts alike, and never submits anything.
func (p *Prober) Balance(ctx context.Context, req BalanceRequest) (*BalanceResult, error) {
	if len(req.Holders) == 0 {
		return nil, errors.New("at least one holder address is required")
	}

	dec, err := p.simulateValue(ctx, req.Token, "decimals")
	if err != nil {
		return nil, err
	}
	if dec.Type != xdr.ScValTypeScvU32 || dec.U32 == nil || uint32(*dec.U32) > maxTokenDecimals {
		return nil, fmt.Errorf("decimals returned %s, expected a u32 of at most %d", dec.Type, maxTokenDecimals)
	}
	decimals := uint32(*dec.U32)

	result := &BalanceResult{Token: req.Token, Decimals: decimals, Balances: make([]HolderBalance, 0, len(req.Holders))}

	// The symbol is only a label; a token that cannot report one is still
	// worth reading balances from.
	if sym, err := p.simulateValue(ctx, req.Token, "symbol"); err == nil && sym.Type == xdr.ScValTypeScvString && sym.Str != nil {
		result.Symbol = string(*sym.Str)
	}

	for _, holder := range req.Holders {
		hb := HolderBalance{Address: holder}
		spec := "addr:" + holder
		if _, err := p.codec.Encode(spec); err != nil {
			hb.Error = fmt.Sprintf("invalid address %q", holder)
			result.Balances = append(result.Balances, hb)
			continue
		}
		val, err := p.simulateValue(ctx, req.Token, "balance", spec)
		if err != nil {
			var simErr *simulationError
			if !errors.As(err, &simErr) {
				return nil, err
			}
			hb.Error = err.Error()
			result.Balances = append(result.Balances, hb)
			continue
		}
		n, ok := scval.ToBigInt(val)
		if !ok {
			hb.Error = fmt.Sprintf("balance returned %s, expected an integer", val.Type)
		} else {
			hb.Raw = n.String()
			hb.Balance = FormatUnits(n, decimals)
		}
		result.Balances = append(result.Balances, hb)
	}
	return result, nil
}

// simulationError marks a call the network rejected, as opposed to a failure
// to reach it.
type simulationError struct{ msg string }

func (e *simulationError) Error() string { return e.msg }

// simulateValue simulates a read-only call and returns its decoded result.
func (p *Prober) simulateValue(ctx context.Context, contractID, fn string, args ...string) (xdr.ScVal, error) {
	sim, err := p.Simulate(ctx, SimulateRequest{ContractID: contractID, Function: fn, Args: args})
	if err != nil {
		return xdr.ScVal{}, err
	}
	if !sim.Success {
		return xdr.ScVal{}, &simulationError{msg: fmt.Sprintf("calling %s failed: %s", fn, sim.Error)}
	}
	var val xdr.ScVal
	if sim.ReturnValueXDR == "" {
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
	}
	if err := xdr.SafeUnmarshalBase64(sim.ReturnValueXDR, &val); err != nil {
		return xdr.ScVal{}, fmt.Errorf("decode %s return value: %w", fn, err)
	}
	return val, nil
}

// FormatUnits renders n as a decimal number with the given number of
// fractional digits, trimming trailing zeros ("12.5", not "12.5000000").
func FormatUnits(n *big.Int, decimals uint32) string {
	neg := n.Sign() < 0
	digits := new(big.Int).Abs(n).String()
	if decimals > 0 {
		places := int(decimals)
		if len(digits) <= places {
			digits = strings.Repeat("0", places-len(digits)+1) + digits
		}
		cut := len(digits) - places
		whole, frac := digits[:cut], strings.TrimRight(digits[cut:], "0")
		if frac != "" {
			digits = whole + "." + frac
		} else {
			digits = whole
		}
	}
	if neg {
		return "-" + digits
	}
	return digits
}
