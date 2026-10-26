package scval

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Decode converts an ScVal into a JSON-marshalable Go value.
//
// Integers wider than 32 bits are rendered as decimal strings rather than JSON
// numbers, because a u64 or i128 cannot round-trip through a float64 and
// silently losing precision on a token balance would be worse than a string.
// Bytes become lowercase hex. Addresses become strkey.
func Decode(v xdr.ScVal) (any, error) {
	switch v.Type {
	case xdr.ScValTypeScvVoid:
		return nil, nil

	case xdr.ScValTypeScvBool:
		if v.B == nil {
			return nil, fmt.Errorf("bool ScVal has no value")
		}
		return *v.B, nil

	case xdr.ScValTypeScvU32:
		if v.U32 == nil {
			return nil, fmt.Errorf("u32 ScVal has no value")
		}
		return uint32(*v.U32), nil

	case xdr.ScValTypeScvI32:
		if v.I32 == nil {
			return nil, fmt.Errorf("i32 ScVal has no value")
		}
		return int32(*v.I32), nil

	case xdr.ScValTypeScvU64:
		if v.U64 == nil {
			return nil, fmt.Errorf("u64 ScVal has no value")
		}
		return strconv.FormatUint(uint64(*v.U64), 10), nil

	case xdr.ScValTypeScvI64:
		if v.I64 == nil {
			return nil, fmt.Errorf("i64 ScVal has no value")
		}
		return strconv.FormatInt(int64(*v.I64), 10), nil

	case xdr.ScValTypeScvTimepoint:
		if v.Timepoint == nil {
			return nil, fmt.Errorf("timepoint ScVal has no value")
		}
		return strconv.FormatUint(uint64(*v.Timepoint), 10), nil

	case xdr.ScValTypeScvDuration:
		if v.Duration == nil {
			return nil, fmt.Errorf("duration ScVal has no value")
		}
		return strconv.FormatUint(uint64(*v.Duration), 10), nil

	case xdr.ScValTypeScvU128:
		if v.U128 == nil {
			return nil, fmt.Errorf("u128 ScVal has no value")
		}
		return wordsToBigInt([]uint64{uint64(v.U128.Hi), uint64(v.U128.Lo)}, false).String(), nil

	case xdr.ScValTypeScvI128:
		if v.I128 == nil {
			return nil, fmt.Errorf("i128 ScVal has no value")
		}
		return wordsToBigInt([]uint64{uint64(v.I128.Hi), uint64(v.I128.Lo)}, true).String(), nil //nolint:gosec // reinterpreting a signed word as an unsigned bit pattern is exact and intended.

	case xdr.ScValTypeScvU256:
		if v.U256 == nil {
			return nil, fmt.Errorf("u256 ScVal has no value")
		}
		words := []uint64{uint64(v.U256.HiHi), uint64(v.U256.HiLo), uint64(v.U256.LoHi), uint64(v.U256.LoLo)}
		return wordsToBigInt(words, false).String(), nil

	case xdr.ScValTypeScvI256:
		if v.I256 == nil {
			return nil, fmt.Errorf("i256 ScVal has no value")
		}
		words := []uint64{uint64(v.I256.HiHi), uint64(v.I256.HiLo), uint64(v.I256.LoHi), uint64(v.I256.LoLo)} //nolint:gosec // reinterpreting signed words as unsigned bit patterns is exact and intended.
		return wordsToBigInt(words, true).String(), nil

	case xdr.ScValTypeScvBytes:
		if v.Bytes == nil {
			return nil, fmt.Errorf("bytes ScVal has no value")
		}
		return hex.EncodeToString(*v.Bytes), nil

	case xdr.ScValTypeScvString:
		if v.Str == nil {
			return nil, fmt.Errorf("string ScVal has no value")
		}
		return string(*v.Str), nil

	case xdr.ScValTypeScvSymbol:
		if v.Sym == nil {
			return nil, fmt.Errorf("symbol ScVal has no value")
		}
		return string(*v.Sym), nil

	case xdr.ScValTypeScvAddress:
		if v.Address == nil {
			return nil, fmt.Errorf("address ScVal has no value")
		}
		s, err := v.Address.String()
		if err != nil {
			return nil, fmt.Errorf("decode address: %w", err)
		}
		return s, nil

	case xdr.ScValTypeScvVec:
		if v.Vec == nil || *v.Vec == nil {
			return []any{}, nil
		}
		out := make([]any, 0, len(**v.Vec))
		for i, item := range **v.Vec {
			decoded, err := Decode(item)
			if err != nil {
				return nil, fmt.Errorf("vec[%d]: %w", i, err)
			}
			out = append(out, decoded)
		}
		return out, nil

	case xdr.ScValTypeScvMap:
		return decodeMap(v)

	case xdr.ScValTypeScvError:
		return decodeError(v)

	case xdr.ScValTypeScvLedgerKeyContractInstance:
		return "contract_instance", nil

	case xdr.ScValTypeScvLedgerKeyNonce:
		if v.NonceKey == nil {
			return nil, fmt.Errorf("nonce ScVal has no value")
		}
		return map[string]any{"nonce": strconv.FormatInt(int64(v.NonceKey.Nonce), 10)}, nil

	case xdr.ScValTypeScvContractInstance:
		return decodeContractInstance(v)

	default:
		return nil, fmt.Errorf("unsupported ScVal type %s: add a case to scval.Decode", v.Type)
	}
}

// decodeMap renders an ScMap as a JSON object when every key decodes to a
// string, and as a list of key/value pairs otherwise, so that maps with
// non-string keys are not silently mangled.
func decodeMap(v xdr.ScVal) (any, error) {
	if v.Map == nil || *v.Map == nil {
		return map[string]any{}, nil
	}
	entries := **v.Map

	type pair struct {
		key   any
		value any
	}
	pairs := make([]pair, 0, len(entries))
	allStringKeys := true

	for i, e := range entries {
		k, err := Decode(e.Key)
		if err != nil {
			return nil, fmt.Errorf("map[%d].key: %w", i, err)
		}
		val, err := Decode(e.Val)
		if err != nil {
			return nil, fmt.Errorf("map[%d].value: %w", i, err)
		}
		if _, ok := k.(string); !ok {
			allStringKeys = false
		}
		pairs = append(pairs, pair{key: k, value: val})
	}

	if allStringKeys {
		out := make(map[string]any, len(pairs))
		for _, p := range pairs {
			out[p.key.(string)] = p.value
		}
		return out, nil
	}

	out := make([]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, map[string]any{"key": p.key, "value": p.value})
	}
	return out, nil
}

func decodeError(v xdr.ScVal) (any, error) {
	if v.Error == nil {
		return nil, fmt.Errorf("error ScVal has no value")
	}
	out := map[string]any{"type": v.Error.Type.String()}
	if code, ok := v.Error.GetCode(); ok {
		out["code"] = code.String()
	}
	if c, ok := v.Error.GetContractCode(); ok {
		out["contract_code"] = uint32(c)
	}
	return out, nil
}

func decodeContractInstance(v xdr.ScVal) (any, error) {
	if v.Instance == nil {
		return nil, fmt.Errorf("contract instance ScVal has no value")
	}
	out := map[string]any{"executable": v.Instance.Executable.Type.String()}
	if hash := v.Instance.Executable.WasmHash; hash != nil {
		out["wasm_hash"] = hex.EncodeToString(hash[:])
	}
	if v.Instance.Storage != nil {
		storage, err := decodeMap(xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &v.Instance.Storage})
		if err != nil {
			return nil, fmt.Errorf("instance storage: %w", err)
		}
		out["storage"] = storage
	}
	return out, nil
}

// wordsToBigInt reassembles 64-bit words, most significant first, into a
// big.Int, interpreting the value as two's complement when signed.
func wordsToBigInt(words []uint64, signed bool) *big.Int {
	result := new(big.Int)
	part := new(big.Int)
	for _, w := range words {
		result.Lsh(result, 64)
		result.Or(result, part.SetUint64(w))
	}
	if signed && len(words) > 0 && words[0]&(1<<63) != 0 {
		result.Sub(result, new(big.Int).Lsh(big.NewInt(1), uint(len(words)*64)))
	}
	return result
}

// ToBigInt extracts the numeric value of any integer-like ScVal.
func ToBigInt(v xdr.ScVal) (*big.Int, bool) {
	switch v.Type {
	case xdr.ScValTypeScvU32:
		if v.U32 == nil {
			return nil, false
		}
		return new(big.Int).SetUint64(uint64(*v.U32)), true
	case xdr.ScValTypeScvI32:
		if v.I32 == nil {
			return nil, false
		}
		return big.NewInt(int64(*v.I32)), true
	case xdr.ScValTypeScvU64:
		if v.U64 == nil {
			return nil, false
		}
		return new(big.Int).SetUint64(uint64(*v.U64)), true
	case xdr.ScValTypeScvI64:
		if v.I64 == nil {
			return nil, false
		}
		return big.NewInt(int64(*v.I64)), true
	case xdr.ScValTypeScvTimepoint:
		if v.Timepoint == nil {
			return nil, false
		}
		return new(big.Int).SetUint64(uint64(*v.Timepoint)), true
	case xdr.ScValTypeScvDuration:
		if v.Duration == nil {
			return nil, false
		}
		return new(big.Int).SetUint64(uint64(*v.Duration)), true
	case xdr.ScValTypeScvU128:
		if v.U128 == nil {
			return nil, false
		}
		return wordsToBigInt([]uint64{uint64(v.U128.Hi), uint64(v.U128.Lo)}, false), true
	case xdr.ScValTypeScvI128:
		if v.I128 == nil {
			return nil, false
		}
		return wordsToBigInt([]uint64{uint64(v.I128.Hi), uint64(v.I128.Lo)}, true), true //nolint:gosec // two's-complement words: the bit pattern is reinterpreted on purpose
	case xdr.ScValTypeScvU256:
		if v.U256 == nil {
			return nil, false
		}
		return wordsToBigInt([]uint64{uint64(v.U256.HiHi), uint64(v.U256.HiLo), uint64(v.U256.LoHi), uint64(v.U256.LoLo)}, false), true
	case xdr.ScValTypeScvI256:
		if v.I256 == nil {
			return nil, false
		}
		return wordsToBigInt([]uint64{uint64(v.I256.HiHi), uint64(v.I256.HiLo), uint64(v.I256.LoHi), uint64(v.I256.LoLo)}, true), true //nolint:gosec // two's-complement words: the bit pattern is reinterpreted on purpose
	default:
		return nil, false
	}
}
