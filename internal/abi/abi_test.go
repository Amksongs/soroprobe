package abi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/scval"
)

const contract = "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4"

func scalar(kind, display string) abi.Type { return abi.Type{Kind: kind, Display: display} }

var transfer = abi.Function{
	Name: "transfer",
	Inputs: []abi.Param{
		{Name: "from", Type: scalar("address", "Address")},
		{Name: "to", Type: scalar("address", "Address")},
		{Name: "amount", Type: scalar("i128", "i128")},
	},
}

var setLimit = abi.Function{
	Name: "set_limit",
	Inputs: []abi.Param{
		{Name: "count", Type: scalar("u32", "u32")},
		{Name: "label", Type: scalar("symbol", "Symbol")},
		{Name: "memo", Type: abi.Type{Kind: "option", Display: "Option<String>",
			Inner: &abi.Type{Kind: "string", Display: "String"}}},
		{Name: "hash", Type: scalar("bytes_n", "BytesN<32>")},
	},
	Outputs: []abi.Type{scalar("bool", "bool")},
}

func TestAnnotateTypesBareArguments(t *testing.T) {
	t.Parallel()
	reg := scval.NewRegistry()

	got, err := abi.Annotate(&setLimit, []string{"5", "daily", "hello world", "deadbeef"}, reg.HasType)
	require.NoError(t, err)
	assert.Equal(t, []string{"u32:5", "symbol:daily", "string:hello world", "bytes:deadbeef"}, got)

	// Inference alone would have made "5" an i128; the annotated form must
	// encode as the u32 the contract declares.
	v, err := reg.Encode(got[0])
	require.NoError(t, err)
	assert.NotNil(t, v.U32)
}

func TestAnnotateLeavesExplicitTypesAlone(t *testing.T) {
	t.Parallel()
	got, err := abi.Annotate(&setLimit, []string{"u64:5", "str:daily", "void", "bytes:00"}, scval.NewRegistry().HasType)
	require.NoError(t, err)
	assert.Equal(t, []string{"u64:5", "str:daily", "void", "bytes:00"}, got)
}

func TestAnnotateOptionNone(t *testing.T) {
	t.Parallel()
	got, err := abi.Annotate(&setLimit, []string{"1", "a", "null", "00"}, func(string) bool { return false })
	require.NoError(t, err)
	assert.Equal(t, "void:null", got[2])
}

func TestAnnotateRejectsWrongArity(t *testing.T) {
	t.Parallel()
	_, err := abi.Annotate(&transfer, []string{"GABC"}, scval.NewRegistry().HasType)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fn transfer(from: Address, to: Address, amount: i128)")
	assert.Contains(t, err.Error(), "takes 3 argument(s), got 1")
}

func TestAnnotatePassesThroughUnsupportedKinds(t *testing.T) {
	t.Parallel()
	fn := abi.Function{Name: "f", Inputs: []abi.Param{{Name: "v", Type: scalar("vec", "Vec<u32>")}}}
	got, err := abi.Annotate(&fn, []string{"whatever"}, scval.NewRegistry().HasType)
	require.NoError(t, err)
	assert.Equal(t, []string{"whatever"}, got)
}

func vecOf(el abi.Type) abi.Type {
	return abi.Type{Kind: "vec", Display: "Vec<" + el.Display + ">", Element: &el}
}

func TestAnnotateTypesCollectionElements(t *testing.T) {
	t.Parallel()
	reg := scval.NewRegistry()

	u32 := scalar("u32", "u32")
	sym := scalar("symbol", "Symbol")
	weights := abi.Type{Kind: "map", Display: "Map<Symbol, u32>", Key: &sym, Value: &u32}
	fn := abi.Function{Name: "f", Inputs: []abi.Param{
		{Name: "ids", Type: vecOf(u32)},
		{Name: "weights", Type: weights},
		{Name: "grid", Type: vecOf(vecOf(u32))},
		{Name: "mixed", Type: vecOf(u32)},
	}}

	got, err := abi.Annotate(&fn, []string{
		`[1, 2]`,
		`[["a", 10], ["b", 20]]`,
		`[[1], []]`,
		`["i64:3", 4]`,
	}, reg.HasType)
	require.NoError(t, err)

	assert.Equal(t, `vec:["u32:1","u32:2"]`, got[0])
	assert.Equal(t, `map:[["symbol:a","u32:10"],["symbol:b","u32:20"]]`, got[1])
	assert.Equal(t, `vec:["vec:[\"u32:1\"]","vec:[]"]`, got[2])
	assert.Equal(t, `vec:["i64:3","u32:4"]`, got[3], "an explicit element type still wins")

	// The annotated specs must encode, and to the declared types.
	for _, spec := range got {
		_, err := reg.Encode(spec)
		require.NoError(t, err, spec)
	}
	v, err := reg.Encode(got[0])
	require.NoError(t, err)
	assert.NotNil(t, (**v.Vec)[0].U32, "elements are u32, not inferred i128")
}

func TestAnnotateCollectionErrorsNameTheArgument(t *testing.T) {
	t.Parallel()
	u32 := scalar("u32", "u32")
	fn := abi.Function{Name: "f", Inputs: []abi.Param{
		{Name: "ids", Type: vecOf(u32)},
	}}
	_, err := abi.Annotate(&fn, []string{`[1, `}, scval.NewRegistry().HasType)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "argument ids")
}

func TestSignature(t *testing.T) {
	t.Parallel()
	assert.Equal(t,
		"fn set_limit(count: u32, label: Symbol, memo: Option<String>, hash: BytesN<32>) -> bool",
		setLimit.Signature())
}

func TestLookupListsAvailableFunctions(t *testing.T) {
	t.Parallel()
	iface := abi.Interface{Functions: []abi.Function{transfer, setLimit}}

	fn, err := iface.Lookup("transfer")
	require.NoError(t, err)
	assert.Equal(t, "transfer", fn.Name)

	_, err = iface.Lookup("tranfser")
	require.ErrorIs(t, err, abi.ErrNoSuchFunction)
	assert.Contains(t, err.Error(), "available: set_limit, transfer")
}

func TestSoroVaultInterface(t *testing.T) {
	t.Parallel()

	var gotPath, gotNetwork string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotNetwork = r.URL.Path, r.URL.Query().Get("network")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "contract_id": "` + contract + `",
		  "interface": {"functions": [{"name": "decimals", "inputs": [],
		    "outputs": [{"kind": "u32", "display": "u32"}]}]}
		}`))
	}))
	defer srv.Close()

	src, err := abi.NewSoroVault(srv.URL+"/", nil, 0)
	require.NoError(t, err)

	iface, err := src.Interface(context.Background(), "testnet", contract)
	require.NoError(t, err)
	assert.Equal(t, "/api/contracts/"+contract, gotPath)
	assert.Equal(t, "testnet", gotNetwork)

	fn, err := iface.Lookup("decimals")
	require.NoError(t, err)
	assert.Equal(t, "fn decimals() -> u32", fn.Signature())
}

func TestSoroVaultErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   int
		body     string
		wantIs   error
		contains string
	}{
		{"not registered", http.StatusNotFound, `{"error":"store: not found"}`, abi.ErrUnknownContract, contract},
		{"no interface", http.StatusUnprocessableEntity, `{"error":"no wasm: stellar asset contract"}`, abi.ErrUnknownContract, "stellar asset contract"},
		{"server error", http.StatusInternalServerError, `{"error":"internal error"}`, nil, "internal error"},
		{"non-json error", http.StatusBadGateway, `<html>bad gateway</html>`, nil, "502"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			src, err := abi.NewSoroVault(srv.URL, nil, 0)
			require.NoError(t, err)

			_, err = src.Interface(context.Background(), "testnet", contract)
			require.Error(t, err)
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			} else {
				assert.NotErrorIs(t, err, abi.ErrUnknownContract)
			}
			assert.Contains(t, err.Error(), tt.contains)
		})
	}
}

func TestNewSoroVaultValidatesURL(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "localhost:8080", "ftp://vault", "http://"} {
		_, err := abi.NewSoroVault(bad, nil, 0)
		assert.Error(t, err, bad)
	}
}

func TestNewSoroVaultEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("invalid input is rejected", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name string
			url  string
		}{
			{"whitespace only", "   "},
			{"scheme without host", "https://"},
			{"unsupported scheme", "file:///etc/passwd"},
			{"malformed url", "http://[::1"},
			{"control character", "http://vault\x7f.example"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				src, err := abi.NewSoroVault(tt.url, nil, 0)
				require.Error(t, err)
				assert.Nil(t, src)
			})
		}
	})

	t.Run("base path and trailing slashes are normalized", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name     string
			suffix   string
			wantPath string
		}{
			{"no suffix", "", "/api/contracts/" + contract},
			{"many trailing slashes", "///", "/api/contracts/" + contract},
			{"path prefix", "/vault/", "/vault/api/contracts/" + contract},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				var gotPath string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotPath = r.URL.Path
					_, _ = w.Write([]byte(`{"contract_id":"` + contract + `","interface":{"functions":[]}}`))
				}))
				defer srv.Close()

				src, err := abi.NewSoroVault(srv.URL+tt.suffix, nil, 0)
				require.NoError(t, err)
				_, err = src.Interface(context.Background(), "", contract)
				require.NoError(t, err)
				assert.Equal(t, tt.wantPath, gotPath)
			})
		}
	})
}

func TestNetworkName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "testnet", abi.NetworkName("Test SDF Network ; September 2015"))
	assert.Equal(t, "public", abi.NetworkName("Public Global Stellar Network ; September 2015"))
	assert.Equal(t, "Standalone Network ; February 2017", abi.NetworkName("Standalone Network ; February 2017"))
}

func TestNetworkNameEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		passphrase string
		want       string
	}{
		{"empty input passes through", "", ""},
		{"futurenet", "Test SDF Future Network ; October 2022", "futurenet"},
		{"match is case sensitive", "test sdf network ; september 2015", "test sdf network ; september 2015"},
		{"surrounding whitespace is not trimmed", " Test SDF Network ; September 2015 ", " Test SDF Network ; September 2015 "},
		{"prefix of a known passphrase is not a match", "Public Global Stellar Network", "Public Global Stellar Network"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, abi.NetworkName(tc.passphrase))
		})
	}
}
