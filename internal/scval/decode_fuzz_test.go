package scval_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/scval"
)

// Decode renders whatever a contract call returned, and the value it is handed
// comes from an RPC endpoint that may be buggy or hostile. An ScVal tree is
// recursive, so a crafted response could ask Decode to walk a structure no
// contract would ever produce. The fuzz target below pins down the two
// properties that keep `soroprobe serve` alive: decoding arbitrary XDR either
// fails with an error or produces a value that JSON can render, and it never
// panics on the way there.
//
// A panic fails the fuzz target by itself, because the engine treats it as a
// crash. The JSON check needs an assertion, since an unmarshalable value would
// only surface later, in the HTTP handler that writes the response.

// fixtureReturnValues is the recorder's output directory. The seeds are the
// return values recorded from the live testnet, so the fuzzer starts from the
// shapes real contracts actually send rather than from random bytes.
const fixtureReturnValues = "../stellar/stellartest/testdata"

// The decoder is recursive, so a deeply nested value is the interesting case
// for stack growth. These depths are below the XDR decoder's own nesting limit
// but far deeper than any real contract result, and the seeds are checked to
// survive XDR decoding in addDeepSeed: a seed that cannot be unmarshaled would
// never reach Decode and so would test nothing.
const (
	deepVecDepth = 700
	deepMapDepth = 450
)

// FuzzDecode feeds arbitrary bytes to the XDR decoder and, when they form an
// ScVal, to Decode.
//
// Bytes that are not valid XDR are not interesting: the RPC client rejects them
// before Decode is reached, so the target returns rather than asserting on the
// decoder's error. What matters is that a valid tree always comes back as
// either an error or a JSON-marshalable value.
func FuzzDecode(f *testing.F) {
	addFixtureReturnValues(f)
	addDeepSeeds(f)

	f.Fuzz(func(t *testing.T, raw []byte) {
		var value xdr.ScVal
		if err := xdr.SafeUnmarshal(raw, &value); err != nil {
			return
		}

		decoded, err := scval.Decode(value)
		if err != nil {
			// An unsupported type or a malformed union arm is a clean
			// failure. It still has to say what went wrong: an empty error
			// would reach the API as a blank reason.
			require.NotEmpty(t, err.Error())
			return
		}

		_, err = json.Marshal(decoded)
		require.NoError(t, err, "Decode(%s) produced a value JSON cannot render", value.Type)
	})
}

// addFixtureReturnValues seeds the corpus with every contract return value the
// recorder has captured. The fixtures are simulateTransaction responses; other
// recordings simply have no results to contribute.
func addFixtureReturnValues(f *testing.F) {
	paths, err := filepath.Glob(filepath.Join(fixtureReturnValues, "*.json"))
	require.NoError(f, err)
	require.NotEmpty(f, paths, "no fixtures in %s", fixtureReturnValues)

	added := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // path comes from filepath.Glob over the fixed fixture directory
		require.NoError(f, err)

		var response struct {
			Results []struct {
				ReturnValueXDR *string `json:"xdr"`
			} `json:"results"`
		}
		require.NoError(f, json.Unmarshal(raw, &response), path)

		for _, result := range response.Results {
			if result.ReturnValueXDR == nil || *result.ReturnValueXDR == "" {
				continue
			}
			value, err := base64.StdEncoding.DecodeString(*result.ReturnValueXDR)
			require.NoError(f, err, path)
			f.Add(value)
			added++
		}
	}

	// A fixture directory with no return values would leave the target with
	// only synthetic seeds, which is not what this test is meant to cover.
	require.Positive(f, added, "no return values found in %s", fixtureReturnValues)
}

// addDeepSeeds seeds the two recursive shapes Decode walks: a vec of vecs and a
// map whose value is another map.
func addDeepSeeds(f *testing.F) {
	addDeepSeed(f, nestedVec(deepVecDepth))
	addDeepSeed(f, nestedMap(deepMapDepth))
}

// addDeepSeed marshals one deeply nested value, checks that it survives XDR
// decoding, and adds it to the corpus.
func addDeepSeed(f *testing.F, value xdr.ScVal) {
	var buf bytes.Buffer
	_, err := xdr.Marshal(&buf, value)
	require.NoError(f, err)

	raw := buf.Bytes()
	var decoded xdr.ScVal
	require.NoError(f, xdr.SafeUnmarshal(raw, &decoded), "deep seed does not survive XDR decoding")
	f.Add(raw)
}

// nestedVec builds [ [ [ ... [0] ... ] ] ] with depth levels of vec.
func nestedVec(depth int) xdr.ScVal {
	value := scvU32(0)
	for i := 0; i < depth; i++ {
		inner := xdr.ScVec{value}
		pointer := &inner
		value = xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &pointer}
	}
	return value
}

// nestedMap builds {"k": {"k": ... {"k": 0} ...}} with depth levels of map.
func nestedMap(depth int) xdr.ScVal {
	key := scvSymbol("k")
	value := scvU32(0)
	for i := 0; i < depth; i++ {
		entries := xdr.ScMap{{Key: key, Val: value}}
		pointer := &entries
		value = xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &pointer}
	}
	return value
}

func scvU32(n uint32) xdr.ScVal {
	value := xdr.Uint32(n)
	return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &value}
}

func scvSymbol(s string) xdr.ScVal {
	value := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &value}
}
