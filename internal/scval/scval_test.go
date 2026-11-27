package scval_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/scval"
)

const (
	testAccount  = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	testContract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
)

func TestEncodeExplicitTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     string
		wantType xdr.ScValType
		// wantDecoded is what Decode should give back, which doubles as a
		// round-trip assertion.
		wantDecoded any
	}{
		{"bool true", "bool:true", xdr.ScValTypeScvBool, true},
		{"bool false", "bool:false", xdr.ScValTypeScvBool, false},
		{"void", "void:", xdr.ScValTypeScvVoid, nil},
		{"null", "null:", xdr.ScValTypeScvVoid, nil},

		{"u32", "u32:42", xdr.ScValTypeScvU32, uint32(42)},
		{"u32 max", "u32:4294967295", xdr.ScValTypeScvU32, uint32(math.MaxUint32)},
		{"i32 negative", "i32:-42", xdr.ScValTypeScvI32, int32(-42)},
		{"u64", "u64:18446744073709551615", xdr.ScValTypeScvU64, "18446744073709551615"},
		{"i64 negative", "i64:-9223372036854775808", xdr.ScValTypeScvI64, "-9223372036854775808"},

		{"u128", "u128:340282366920938463463374607431768211455", xdr.ScValTypeScvU128, "340282366920938463463374607431768211455"},
		{"u128 zero", "u128:0", xdr.ScValTypeScvU128, "0"},
		{"i128 positive", "i128:170141183460469231731687303715884105727", xdr.ScValTypeScvI128, "170141183460469231731687303715884105727"},
		{"i128 negative", "i128:-170141183460469231731687303715884105728", xdr.ScValTypeScvI128, "-170141183460469231731687303715884105728"},
		{"i128 minus one", "i128:-1", xdr.ScValTypeScvI128, "-1"},
		{"i128 decimal amount", "i128:10.5@7", xdr.ScValTypeScvI128, "105000000"},
		{"u128 decimal amount", "u128:1.5@2", xdr.ScValTypeScvU128, "150"},

		{"u256", "u256:115792089237316195423570985008687907853269984665640564039457584007913129639935",
			xdr.ScValTypeScvU256, "115792089237316195423570985008687907853269984665640564039457584007913129639935"},
		{"i256 negative", "i256:-57896044618658097711785492504343953926634992332820282019728792003956564819968",
			xdr.ScValTypeScvI256, "-57896044618658097711785492504343953926634992332820282019728792003956564819968"},
		{"i256 minus one", "i256:-1", xdr.ScValTypeScvI256, "-1"},

		{"timepoint", "timepoint:1700000000", xdr.ScValTypeScvTimepoint, "1700000000"},
		{"duration", "duration:3600", xdr.ScValTypeScvDuration, "3600"},

		{"symbol", "sym:transfer", xdr.ScValTypeScvSymbol, "transfer"},
		{"symbol long form", "symbol:transfer", xdr.ScValTypeScvSymbol, "transfer"},
		{"string", "str:hello world", xdr.ScValTypeScvString, "hello world"},
		{"string quoted keeps spaces", `str:"  padded  "`, xdr.ScValTypeScvString, "  padded  "},
		{"string with colon", "str:https://example.com", xdr.ScValTypeScvString, "https://example.com"},

		{"bytes", "bytes:deadbeef", xdr.ScValTypeScvBytes, "deadbeef"},
		{"bytes with 0x", "bytes:0xdeadbeef", xdr.ScValTypeScvBytes, "deadbeef"},
		{"bytes empty", "bytes:", xdr.ScValTypeScvBytes, ""},

		{"account address", "addr:" + testAccount, xdr.ScValTypeScvAddress, testAccount},
		{"contract address", "addr:" + testContract, xdr.ScValTypeScvAddress, testContract},
		{"address long form", "address:" + testContract, xdr.ScValTypeScvAddress, testContract},
	}

	codec := scval.NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.Encode(tt.spec)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got.Type)

			decoded, err := codec.Decode(got)
			require.NoError(t, err)
			assert.Equal(t, tt.wantDecoded, decoded, "value should survive a round trip")
		})
	}
}

func TestEncodeInference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     string
		wantType xdr.ScValType
	}{
		{"true infers bool", "true", xdr.ScValTypeScvBool},
		{"false infers bool", "false", xdr.ScValTypeScvBool},
		{"void infers void", "void", xdr.ScValTypeScvVoid},
		{"null infers void", "null", xdr.ScValTypeScvVoid},
		{"account infers address", testAccount, xdr.ScValTypeScvAddress},
		{"contract infers address", testContract, xdr.ScValTypeScvAddress},
		{"digits infer i128", "1000", xdr.ScValTypeScvI128},
		{"negative digits infer i128", "-1000", xdr.ScValTypeScvI128},
		{"word infers symbol", "transfer", xdr.ScValTypeScvSymbol},
		{"underscored word infers symbol", "get_balance", xdr.ScValTypeScvSymbol},
		{"spaced text infers string", "hello world", xdr.ScValTypeScvString},
		{"long text infers string", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", xdr.ScValTypeScvString},
		{"unknown prefix falls through to inference", "notatype:value", xdr.ScValTypeScvString},
	}

	codec := scval.NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.Encode(tt.spec)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got.Type)
		})
	}
}

func TestHasType(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()
	for spec, want := range map[string]bool{
		"u32:5":          true,
		"SYM:transfer":   true,
		"void":           true,
		"NULL":           true,
		"5":              false,
		"transfer":       false,
		"notatype:value": false,
		"https://x.y":    false,
		testAccount:      false,
	} {
		assert.Equal(t, want, codec.HasType(spec), spec)
	}
}

func TestEncodeDecimalAmounts(t *testing.T) {
	t.Parallel()

	// A token amount is written as a decimal plus the token's decimal count:
	// "10.5@7" is 10.5 tokens of a 7-decimal token, which is 105000000 base
	// units. The conversion must be exact — a rounding error here sends the
	// wrong amount to a simulation — so every case below is spelled out in
	// full.
	tests := []struct {
		name string
		spec string
		// want is the decoded value, as a decimal string.
		want string
		// wantErr, when set, is a fragment the error must contain.
		wantErr string
	}{
		{name: "whole decimal amount", spec: "i128:10.5@7", want: "105000000"},
		{name: "whole number scales too", spec: "i128:10@7", want: "100000000"},
		{name: "zero", spec: "i128:0@7", want: "0"},
		{name: "zero with a fraction", spec: "i128:0.0@7", want: "0"},
		{name: "negative amount", spec: "i128:-10.5@7", want: "-105000000"},
		{name: "negative whole number", spec: "i128:-10@7", want: "-100000000"},
		{name: "leading plus", spec: "i128:+10.5@7", want: "105000000"},
		{name: "smallest unit", spec: "i128:0.0000001@7", want: "1"},
		{name: "exactly as many digits as decimals", spec: "i128:10.5000000@7", want: "105000000"},
		{name: "trailing zeros are kept", spec: "i128:10.50@7", want: "105000000"},
		{name: "no integer part", spec: "i128:.5@7", want: "5000000"},
		{name: "two decimals", spec: "u128:1.5@2", want: "150"},
		{name: "one decimal", spec: "i256:1.5@1", want: "15"},
		{name: "narrow type still scales", spec: "u32:10.5@2", want: "1050"},
		{name: "zero decimals", spec: "i128:10.5@0", wantErr: "fractional digits"},

		{name: "more digits than decimals", spec: "i128:10.50000000@7", wantErr: "fractional digits"},
		{name: "fraction without a decimal count", spec: "i128:10.5", wantErr: "expected an integer"},
		{name: "not a number", spec: "i128:abc@7", wantErr: "expected a number"},
		{name: "empty amount", spec: "i128:@7", wantErr: "expected a number"},
		{name: "decimal count is not a number", spec: "i128:10.5@x", wantErr: "expected a decimal count"},
		{name: "negative decimal count", spec: "i128:10.5@-1", wantErr: "expected a decimal count"},
		{name: "fractional decimal count", spec: "i128:10.5@7.5", wantErr: "expected a decimal count"},
		{name: "decimal count out of range", spec: "i128:10.5@77", wantErr: "out of range"},
		{name: "scaled past the type", spec: "i128:1@39", wantErr: "out of range for i128"},
	}

	codec := scval.NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.Encode(tt.spec)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Contains(t, err.Error(), tt.spec, "error should quote the offending argument")
				return
			}

			require.NoError(t, err)
			decoded, err := codec.Decode(got)
			require.NoError(t, err)
			// Integers wider than 32 bits decode to a decimal string, the
			// narrower ones to a number; the printed form is the same either
			// way.
			assert.Equal(t, tt.want, fmt.Sprint(decoded))
		})
	}
}

func TestEncodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec string
	}{
		{"u32 overflow", "u32:4294967296"},
		{"u32 negative", "u32:-1"},
		{"i32 overflow", "i32:2147483648"},
		{"u64 negative", "u64:-1"},
		{"u128 negative", "u128:-1"},
		{"u128 overflow", "u128:340282366920938463463374607431768211456"},
		{"i128 overflow", "i128:170141183460469231731687303715884105728"},
		{"i128 underflow", "i128:-170141183460469231731687303715884105729"},
		{"u256 overflow", "u256:115792089237316195423570985008687907853269984665640564039457584007913129639936"},
		{"not a number", "u32:abc"},
		{"bad bool", "bool:maybe"},
		{"bad hex", "bytes:zzzz"},
		{"bad address", "addr:notanaddress"},
		{"symbol too long", "sym:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"symbol with spaces", "sym:hello world"},
	}

	codec := scval.NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := codec.Encode(tt.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.spec, "error should quote the offending argument")
		})
	}
}

func TestEncodeSpecEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     string
		wantType xdr.ScValType
	}{
		// Empty input is not a valid symbol (symbols need at least one
		// character), so it is encoded as an empty string.
		{"empty spec is an empty string", "", xdr.ScValTypeScvString},
		// A leading colon is not a type prefix, so inference takes over.
		{"leading colon falls through to string", ":5", xdr.ScValTypeScvString},
		// Prefixes containing non-name characters are not specs (URLs, say).
		{"url falls through to string", "https://example.com", xdr.ScValTypeScvString},
		// An unregistered prefix is not an error; the whole spec is inferred.
		{"unknown prefix falls through to string", "nosuchtype:5", xdr.ScValTypeScvString},
		// Type names are matched case-insensitively.
		{"uppercase prefix", "U32:5", xdr.ScValTypeScvU32},
		// Boundary: the widest symbol is 32 characters, one more becomes a string.
		{"32 char symbol", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", xdr.ScValTypeScvSymbol},
		{"33 char bare value is a string", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", xdr.ScValTypeScvString},
	}

	codec := scval.NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.Encode(tt.spec)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got.Type)
		})
	}
}

func TestEncodeEmptyLiteralErrors(t *testing.T) {
	t.Parallel()

	// A recognized prefix with nothing after it must fail rather than encode
	// a zero value, and the error must quote the argument.
	tests := []struct {
		name string
		spec string
	}{
		{"u32", "u32:"},
		{"i128", "i128:"},
		{"bool", "bool:"},
		{"addr", "addr:"},
	}

	codec := scval.NewRegistry()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := codec.Encode(tt.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.spec)
		})
	}
}

func TestEncodeAllReportsArgIndex(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()
	_, err := codec.EncodeAll([]string{"u32:1", "u32:2", "u32:bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "arg 2")
}

func TestEncodeAllEmpty(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()
	args, err := codec.EncodeAll(nil)
	require.NoError(t, err)
	assert.Empty(t, args)
}

func TestRegisterCustomType(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()

	// A contributor adding a type only needs to register an EncodeFunc.
	codec.Register("answer", func(string) (xdr.ScVal, error) {
		v := xdr.Uint32(42)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}, nil
	})

	got, err := codec.Encode("answer:anything")
	require.NoError(t, err)
	assert.Equal(t, xdr.ScValTypeScvU32, got.Type)
	assert.EqualValues(t, 42, *got.U32)
	assert.Contains(t, codec.TypeNames(), "answer")
}

func TestRegisterOverridesBuiltin(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()
	codec.Register("u32", func(string) (xdr.ScVal, error) {
		v := xdr.Uint32(7)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}, nil
	})

	got, err := codec.Encode("u32:999")
	require.NoError(t, err)
	assert.EqualValues(t, 7, *got.U32)
}

func TestDecodeCollections(t *testing.T) {
	t.Parallel()

	u32 := func(n uint32) xdr.ScVal {
		v := xdr.Uint32(n)
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}
	}
	sym := func(s string) xdr.ScVal {
		v := xdr.ScSymbol(s)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &v}
	}

	t.Run("vec", func(t *testing.T) {
		t.Parallel()

		vec := xdr.ScVec{u32(1), u32(2), u32(3)}
		vecPtr := &vec
		got, err := scval.Decode(xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr})
		require.NoError(t, err)
		assert.Equal(t, []any{uint32(1), uint32(2), uint32(3)}, got)
	})

	t.Run("empty vec", func(t *testing.T) {
		t.Parallel()

		got, err := scval.Decode(xdr.ScVal{Type: xdr.ScValTypeScvVec})
		require.NoError(t, err)
		assert.Equal(t, []any{}, got)
	})

	t.Run("map with string keys becomes an object", func(t *testing.T) {
		t.Parallel()

		m := xdr.ScMap{
			{Key: sym("a"), Val: u32(1)},
			{Key: sym("b"), Val: u32(2)},
		}
		mPtr := &m
		got, err := scval.Decode(xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mPtr})
		require.NoError(t, err)
		assert.Equal(t, map[string]any{"a": uint32(1), "b": uint32(2)}, got)
	})

	t.Run("map with non-string keys becomes pairs", func(t *testing.T) {
		t.Parallel()

		// A u32 key cannot be a JSON object key without lying about the type,
		// so the decoder falls back to an explicit list of pairs.
		m := xdr.ScMap{{Key: u32(1), Val: sym("one")}}
		mPtr := &m
		got, err := scval.Decode(xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mPtr})
		require.NoError(t, err)
		assert.Equal(t, []any{map[string]any{"key": uint32(1), "value": "one"}}, got)
	})

	t.Run("nested vec of maps", func(t *testing.T) {
		t.Parallel()

		m := xdr.ScMap{{Key: sym("k"), Val: u32(9)}}
		mPtr := &m
		inner := xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mPtr}
		vec := xdr.ScVec{inner}
		vecPtr := &vec

		got, err := scval.Decode(xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr})
		require.NoError(t, err)
		assert.Equal(t, []any{map[string]any{"k": uint32(9)}}, got)
	})
}

func TestDecodeRejectsMalformedScVal(t *testing.T) {
	t.Parallel()

	// A u32 ScVal with no payload is structurally invalid; decoding must fail
	// loudly rather than silently yielding a zero.
	_, err := scval.Decode(xdr.ScVal{Type: xdr.ScValTypeScvU32})
	require.Error(t, err)
}

func TestDecodeUnsupportedTypeNamesItself(t *testing.T) {
	t.Parallel()

	_, err := scval.Decode(xdr.ScVal{Type: xdr.ScValType(9999)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scval.Decode", "error should point a contributor at the fix")
}
