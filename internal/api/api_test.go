package api_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/api"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func newServer(t *testing.T, fake *stellartest.Fake) http.Handler {
	t.Helper()
	return newCachedServer(t, fake, 0)
}

// newCachedServer builds a server whose prober caches inspect results for ttl.
// A zero ttl leaves the cache off.
func newCachedServer(t *testing.T, fake *stellartest.Fake, ttl time.Duration) http.Handler {
	t.Helper()

	p, err := probe.New(probe.Options{
		Client:          fake,
		SourceAccount:   stellartest.SourceAccount,
		InspectCacheTTL: ttl,
	})
	require.NoError(t, err)

	return api.New(api.Options{Prober: p}).Handler()
}

// newReadinessServer builds a handler with explicit readiness settings, for
// the tests that pin the network passphrase and the cache window.
func newReadinessServer(t *testing.T, fake *stellartest.Fake, passphrase string, ttl time.Duration) http.Handler {
	t.Helper()

	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	return api.New(api.Options{
		Prober:            p,
		NetworkPassphrase: passphrase,
		ReadinessCacheTTL: ttl,
	}).Handler()
}

// do issues a request and decodes the JSON body into v when v is non-nil.
func do(t *testing.T, h http.Handler, method, target, body string, v any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}

	req := httptest.NewRequest(method, target, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if v != nil {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), v), "body was: %s", rec.Body.String())
	}
	return rec
}

func TestLiveness(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/healthz", "", &body)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", body["status"])
}

func TestLivenessDoesNotTouchTheNetwork(t *testing.T) {
	t.Parallel()

	// A liveness probe must not fail just because an upstream RPC endpoint is
	// briefly unavailable.
	fake := stellartest.NewFake(t)
	fake.EntriesErr = errors.New("rpc down")
	fake.SimulateErr = errors.New("rpc down")
	h := newServer(t, fake)

	rec := do(t, h, http.MethodGet, "/healthz", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, fake.EntryRequests)
	assert.Empty(t, fake.SimulateRequests)
}

func TestRateLimitWeightsBatchesAndResets(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{Client: fake, SourceAccount: stellartest.SourceAccount})
	require.NoError(t, err)
	h := api.New(api.Options{
		Prober:             p,
		RateLimitPerMinute: 6_000,
		RateLimitBurst:     1,
	}).Handler()

	first := do(t, h, http.MethodGet, "/v1/unknown", "", nil)
	assert.Equal(t, http.StatusNotFound, first.Code)

	limited := do(t, h, http.MethodPost, "/v1/checks", `{"checks":[{},{}]}`, nil)
	assert.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.Equal(t, "1", limited.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"error":"rate limit exceeded"}`, limited.Body.String())

	health := do(t, h, http.MethodGet, "/healthz", "", nil)
	assert.Equal(t, http.StatusOK, health.Code)

	time.Sleep(30 * time.Millisecond)
	reset := do(t, h, http.MethodPost, "/v1/checks", `{"checks":[{},{}]}`, nil)
	assert.Equal(t, http.StatusBadRequest, reset.Code)
}

func TestListenAndServeTLS(t *testing.T) {
	testTLS := httptest.NewTLSServer(http.NotFoundHandler())
	defer testTLS.Close()

	cert := testTLS.TLS.Certificates[0]
	certFile := filepath.Join(t.TempDir(), "server.crt")
	keyFile := filepath.Join(t.TempDir(), "server.key")
	require.NoError(t, os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: cert.Certificate[0],
	}), 0o600))
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{
		Type: "PRIVATE KEY", Bytes: key,
	}), 0o600))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{Client: fake, SourceAccount: stellartest.SourceAccount})
	require.NoError(t, err)
	server := api.New(api.Options{Prober: p, Addr: addr, TLSCert: certFile, TLSKey: keyFile})
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe(ctx) }()
	defer func() {
		cancel()
		require.NoError(t, <-serveErr)
	}()

	parsedCert, err := x509.ParseCertificate(cert.Certificate[0])
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(parsedCert)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}}
	defer client.CloseIdleConnections()

	deadline := time.Now().Add(5 * time.Second)
	var response *http.Response
	for time.Now().Before(deadline) {
		response, err = client.Get("https://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

func TestSimulateEndpoint(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.SimulateResult
	rec := do(t, h, http.MethodPost, "/v1/simulate",
		`{"contract_id":"`+stellartest.SACContract+`","function":"decimals"}`, &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, result.Success)
	assert.EqualValues(t, 7, result.ReturnValue)
	assert.Positive(t, result.Cost.Instructions)
}

func TestSimulateEndpointWithArgs(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newServer(t, fake)

	rec := do(t, h, http.MethodPost, "/v1/simulate",
		`{"contract_id":"`+stellartest.SACContract+`","function":"decimals","args":["u32:1"]}`, nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, fake.SimulateRequests, 1)
	args, err := stellartest.ArgsOf(fake.SimulateRequests[0].Transaction)
	require.NoError(t, err)
	require.Len(t, args, 1)
	assert.EqualValues(t, 1, *args[0].U32)
}

func TestSimulateEndpointReportsContractFailureAs200(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.SimulateResult
	rec := do(t, h, http.MethodPost, "/v1/simulate",
		`{"contract_id":"`+stellartest.SACContract+`","function":"no_such_fn"}`, &result)

	// The request itself succeeded; the contract rejected the call. Those are
	// different things and the status code must not conflate them.
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, result.Success)
	assert.NotEmpty(t, result.Error)
}

func TestSimulateEndpointComparesAgainstABaseline(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.SimulateResult
	rec := do(t, h, http.MethodPost, "/v1/simulate",
		`{"contract_id":"`+stellartest.SACContract+`","function":"decimals",`+
			`"baseline":{"contract_id":"`+stellartest.SACContract+`","function":"decimals","cost":{"instructions":1}}}`, &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, result.Comparison)
	require.Len(t, result.Comparison.Figures, 4)
	assert.Positive(t, result.Comparison.Figures[0].Delta)
}

func TestSimulateEndpointRejectsABaselineForAnotherCall(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	// A baseline for a different call is bad input, not an upstream failure.
	rec := do(t, h, http.MethodPost, "/v1/simulate",
		`{"contract_id":"`+stellartest.SACContract+`","function":"decimals",`+
			`"baseline":{"contract_id":"`+stellartest.WasmContract+`","function":"decimals","cost":{}}}`, nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestSimulateEndpointBadRequests(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	tests := []struct {
		name string
		body string
	}{
		{"malformed json", `{`},
		{"missing contract id", `{"function":"decimals"}`},
		{"missing function", `{"contract_id":"` + stellartest.SACContract + `"}`},
		{"invalid contract id", `{"contract_id":"nope","function":"decimals"}`},
		{"bad argument", `{"contract_id":"` + stellartest.SACContract + `","function":"decimals","args":["u32:xyz"]}`},
		{"unknown field", `{"contract_id":"x","function":"y","bogus":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, h, http.MethodPost, "/v1/simulate", tt.body, nil)
			assert.Equal(t, http.StatusBadRequest, rec.Code, "body was: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), "error")
		})
	}
}

// staticABI serves one interface for every contract.
type staticABI struct{ iface *abi.Interface }

func (s staticABI) Interface(context.Context, string, string) (*abi.Interface, error) {
	return s.iface, nil
}

func TestSimulateEndpointInterfaceErrorsAreBadRequests(t *testing.T) {
	t.Parallel()

	p, err := probe.New(probe.Options{
		Client:        stellartest.NewFake(t),
		SourceAccount: stellartest.SourceAccount,
		ABI: staticABI{&abi.Interface{Functions: []abi.Function{
			{Name: "decimals", Outputs: []abi.Type{{Kind: "u32", Display: "u32"}}},
		}}},
	})
	require.NoError(t, err)
	h := api.New(api.Options{Prober: p}).Handler()

	for name, body := range map[string]string{
		"unknown function": `{"contract_id":"` + stellartest.SACContract + `","function":"decimal"}`,
		"too many args":    `{"contract_id":"` + stellartest.SACContract + `","function":"decimals","args":["1"]}`,
	} {
		rec := do(t, h, http.MethodPost, "/v1/simulate", body, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: body was %s", name, rec.Body.String())
	}
}

func TestSimulateEndpointUpstreamFailureIsBadGateway(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.SimulateErr = errors.New("rpc unreachable")
	h := newServer(t, fake)

	rec := do(t, h, http.MethodPost, "/v1/simulate",
		`{"contract_id":"`+stellartest.SACContract+`","function":"decimals"}`, nil)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestInspectEndpoint(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.InspectResult
	rec := do(t, h, http.MethodGet, "/v1/inspect/"+stellartest.WasmContract, "", &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, result.Deployed)
	assert.Equal(t, "wasm", result.Executable)
	require.NotNil(t, result.Code)
}

func TestInspectEndpointWithDataKeys(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.InspectResult
	rec := do(t, h, http.MethodGet,
		"/v1/inspect/"+stellartest.SACContract+"?key=sym:Admin&key=sym:Balance", "", &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, result.Data, 2)
	assert.Equal(t, "sym:Admin", result.Data[0].Key)
}

func TestInspectEndpointRejectsUnknownDurability(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	rec := do(t, h, http.MethodGet,
		"/v1/inspect/"+stellartest.SACContract+"?durability=forever", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInspectEndpointEstimatesTheExtendFee(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.InspectResult
	rec := do(t, h, http.MethodGet,
		"/v1/inspect/"+stellartest.SACContract+"?estimate_extend=1000000", "", &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, result.ExtendEstimates, 1)
	assert.Equal(t, "instance", result.ExtendEstimates[0].Kind)
	assert.EqualValues(t, 1000000, result.ExtendEstimates[0].Ledgers)
	assert.Positive(t, result.ExtendEstimates[0].ResourceFee)
}

func TestInspectEndpointRejectsABadExtendCount(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	rec := do(t, h, http.MethodGet,
		"/v1/inspect/"+stellartest.SACContract+"?estimate_extend=lots", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInspectEndpointInvalidContract(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	rec := do(t, h, http.MethodGet, "/v1/inspect/nope", "", nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCheckEndpoint(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.CheckResult
	rec := do(t, h, http.MethodGet, "/v1/check/"+stellartest.SACContract+"?fn=decimals", "", &result)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, result.OK)
	require.NotNil(t, result.Simulate)
}

func TestCheckEndpointFailingCheckIsStill200(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var result probe.CheckResult
	rec := do(t, h, http.MethodGet, "/v1/check/"+stellartest.UndeployedContract, "", &result)

	// An unhealthy contract is a successful request reporting bad news.
	// Callers read "ok" rather than the status code.
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, result.OK)
}

func TestCheckEndpointPassesArgs(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newServer(t, fake)

	rec := do(t, h, http.MethodGet,
		"/v1/check/"+stellartest.SACContract+"?fn=decimals&arg=u32:1&arg=sym:x", "", nil)
	assert.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, fake.SimulateRequests, 1)
	args, err := stellartest.ArgsOf(fake.SimulateRequests[0].Transaction)
	require.NoError(t, err)
	assert.Len(t, args, 2)
}

func TestNoWriteRoutesExist(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	// SoroProbe is read-only by design. These routes must not exist.
	for _, target := range []string{"/v1/send", "/v1/submit", "/v1/deploy", "/v1/restore"} {
		rec := do(t, h, http.MethodPost, target, `{}`, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, "%s should not exist", target)
	}
}

func TestCORSAllowsConfiguredOriginAndPreflight(t *testing.T) {
	t.Parallel()
	p, err := probe.New(probe.Options{Client: stellartest.NewFake(t), SourceAccount: stellartest.SourceAccount})
	require.NoError(t, err)
	h := api.New(api.Options{Prober: p, CORSOrigins: []string{"https://dashboard.example"}}).Handler()

	req := httptest.NewRequest(http.MethodOptions, "/v1/simulate", nil)
	req.Header.Set("Origin", "https://dashboard.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "https://dashboard.example", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Authorization")

	actual := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	actual.Header.Set("Origin", "https://dashboard.example")
	actualRec := httptest.NewRecorder()
	h.ServeHTTP(actualRec, actual)
	assert.Equal(t, "https://dashboard.example", actualRec.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSRejectsUnconfiguredOrigin(t *testing.T) {
	t.Parallel()
	p, err := probe.New(probe.Options{Client: stellartest.NewFake(t), SourceAccount: stellartest.SourceAccount})
	require.NoError(t, err)
	h := api.New(api.Options{Prober: p, CORSOrigins: []string{"https://dashboard.example"}}).Handler()

	req := httptest.NewRequest(http.MethodOptions, "/v1/simulate", nil)
	req.Header.Set("Origin", "https://other.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))

	actual := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	actual.Header.Set("Origin", "https://other.example")
	actualRec := httptest.NewRecorder()
	h.ServeHTTP(actualRec, actual)
	assert.Empty(t, actualRec.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORSDisabledByDefault(t *testing.T) {
	t.Parallel()
	h := newServer(t, stellartest.NewFake(t))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Origin", "https://dashboard.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	rec := do(t, h, http.MethodGet, "/v1/simulate", "", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestChecksEndpoint(t *testing.T) {
	t.Parallel()
	h := newServer(t, stellartest.NewFake(t))

	var body probe.BatchResult
	rec := do(t, h, http.MethodPost, "/v1/checks", `{"checks": [
		{"name": "native", "contract_id": "`+stellartest.SACContract+`", "function": "decimals"},
		{"name": "gone", "contract_id": "`+stellartest.UndeployedContract+`"}
	]}`, &body)

	require.Equal(t, http.StatusOK, rec.Code, "an unhealthy contract is still a 200")
	assert.False(t, body.OK)
	assert.False(t, body.Errored)
	require.Len(t, body.Items, 2)
	assert.True(t, body.Items[0].Result.OK)
	assert.False(t, body.Items[1].Result.OK)
}

func TestChecksEndpointBadRequests(t *testing.T) {
	t.Parallel()
	h := newServer(t, stellartest.NewFake(t))

	var many strings.Builder
	many.WriteString(`{"checks": [`)
	for i := 0; i < 21; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		fmt.Fprintf(&many, `{"name": "c%d", "contract_id": "%s"}`, i, stellartest.SACContract)
	}
	many.WriteString("]}")

	for name, body := range map[string]string{
		"empty":         `{"checks": []}`,
		"unknown field": `{"checks": [{"contract_id": "C", "fn": "x"}]}`,
		"duplicate":     `{"checks": [{"name": "a", "contract_id": "C1"}, {"name": "a", "contract_id": "C2"}]}`,
		"over the cap":  many.String(),
	} {
		rec := do(t, h, http.MethodPost, "/v1/checks", body, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}
}

func TestSecurityHeadersOnEveryRoute(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	// Body-carrying routes need a valid payload to reach their handler, but the
	// headers are written by middleware before the handler runs, so any status
	// proves they are present. Every route the router serves is listed here.
	routes := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/healthz", ""},
		{http.MethodGet, "/readyz", ""},
		{http.MethodPost, "/v1/simulate",
			`{"contract_id":"` + stellartest.SACContract + `","function":"decimals"}`},
		{http.MethodGet, "/v1/inspect/" + stellartest.SACContract, ""},
		{http.MethodGet, "/v1/check/" + stellartest.SACContract + "?fn=decimals", ""},
		{http.MethodPost, "/v1/checks",
			`{"checks":[{"name":"c","contract_id":"` + stellartest.SACContract + `","function":"decimals"}]}`},
	}

	for _, rt := range routes {
		rec := do(t, h, rt.method, rt.target, rt.body, nil)
		assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"), rt.target)
	}
}

func TestSecurityHeadersOnUnknownRoute(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	// A 404 is still a response from this server, so it carries the headers too.
	rec := do(t, h, http.MethodGet, "/does-not-exist", "", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}

func TestVersionedRoutesAreNotCached(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	for _, target := range []string{
		"/v1/inspect/" + stellartest.SACContract,
		"/v1/check/" + stellartest.SACContract + "?fn=decimals",
		"/readyz",
	} {
		rec := do(t, h, http.MethodGet, target, "", nil)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), target)
	}

	// The liveness probe carries no chain state and is meant to be polled
	// cheaply, so it stays out of the no-store rule.
	rec := do(t, h, http.MethodGet, "/healthz", "", nil)
	assert.Empty(t, rec.Header().Get("Cache-Control"))
}

func TestInspectEndpointCacheHitAvoidsRPC(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newCachedServer(t, fake, time.Minute)

	var first, second probe.InspectResult
	target := "/v1/inspect/" + stellartest.SACContract
	rec := do(t, h, http.MethodGet, target, "", &first)
	require.Equal(t, http.StatusOK, rec.Code)
	rec = do(t, h, http.MethodGet, target, "", &second)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Len(t, fake.EntryRequests, 1, "a cache hit must not make an RPC call")
	assert.NotZero(t, first.LatestLedger)
	assert.Equal(t, first.LatestLedger, second.LatestLedger,
		"a cached response still states the ledger it was computed at")
}

func TestInspectEndpointCacheExpires(t *testing.T) {
	t.Parallel()

	const ttl = 25 * time.Millisecond
	fake := stellartest.NewFake(t)
	h := newCachedServer(t, fake, ttl)
	target := "/v1/inspect/" + stellartest.SACContract

	do(t, h, http.MethodGet, target, "", nil)
	do(t, h, http.MethodGet, target, "", nil)
	require.Len(t, fake.EntryRequests, 1)

	time.Sleep(2 * ttl)

	do(t, h, http.MethodGet, target, "", nil)
	assert.Len(t, fake.EntryRequests, 2, "an expired entry must be recomputed")
}

func TestSimulateEndpointIsNeverCached(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newCachedServer(t, fake, time.Minute)
	body := `{"contract_id":"` + stellartest.SACContract + `","function":"decimals"}`

	do(t, h, http.MethodPost, "/v1/simulate", body, nil)
	do(t, h, http.MethodPost, "/v1/simulate", body, nil)

	assert.Len(t, fake.SimulateRequests, 2, "simulations must never be cached")
}

func TestCheckEndpointCachesInspectButNotSimulation(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newCachedServer(t, fake, time.Minute)
	target := "/v1/check/" + stellartest.SACContract + "?fn=decimals"

	do(t, h, http.MethodGet, target, "", nil)
	do(t, h, http.MethodGet, target, "", nil)

	// Check inspects and then simulates. The inspect half is cacheable; the
	// liveness call is not.
	assert.Len(t, fake.EntryRequests, 1)
	assert.Len(t, fake.SimulateRequests, 2)
}

func TestReadinessHealthyRPC(t *testing.T) {
	t.Parallel()

	h := newServer(t, stellartest.NewFake(t))

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/readyz", "", &body)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ready", body["status"])
}

func TestReadinessUnhealthyRPC(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.HealthStatus = "unhealthy"
	h := newServer(t, fake)

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/readyz", "", &body)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "unready", body["status"])
	assert.Contains(t, body["reason"], "unhealthy")
}

func TestReadinessUnreachableRPC(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.HealthErr = errors.New("connection refused")
	h := newServer(t, fake)

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/readyz", "", &body)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "unready", body["status"])
	assert.Contains(t, body["reason"], "connection refused")
}

func TestReadinessWrongNetworkRPC(t *testing.T) {
	t.Parallel()

	// The server defaults to the testnet passphrase; the fake serves the
	// public network, so a healthy endpoint is still not ready: it answers
	// questions about the wrong chain.
	fake := stellartest.NewFake(t)
	fake.NetworkPassphrase = "Public Global Stellar Network ; September 2015"
	h := newServer(t, fake)

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/readyz", "", &body)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, body["reason"], "wrong network")
	assert.Contains(t, body["reason"], "Public Global Stellar Network")
}

func TestReadinessAcceptsTheConfiguredNetwork(t *testing.T) {
	t.Parallel()

	// An operator pointing at mainnet expects the public passphrase, and a
	// public-network endpoint must then read as ready.
	fake := stellartest.NewFake(t)
	fake.NetworkPassphrase = "Public Global Stellar Network ; September 2015"
	h := newReadinessServer(t, fake, "Public Global Stellar Network ; September 2015", time.Minute)

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/readyz", "", &body)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ready", body["status"])
}

func TestReadinessCachesTheResult(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newReadinessServer(t, fake, "Test SDF Network ; September 2015", time.Minute)

	for i := 0; i < 3; i++ {
		rec := do(t, h, http.MethodGet, "/readyz", "", nil)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	// A chatty orchestrator must not multiply RPC traffic: three probes
	// inside the cache window cost one getHealth.
	assert.Equal(t, 1, fake.HealthRequests)
	assert.Equal(t, 1, fake.NetworkRequests)
}

func TestReadinessCacheExpires(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	h := newReadinessServer(t, fake, "Test SDF Network ; September 2015", 50*time.Millisecond)

	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/readyz", "", nil).Code)
	time.Sleep(150 * time.Millisecond)
	require.Equal(t, http.StatusOK, do(t, h, http.MethodGet, "/readyz", "", nil).Code)

	assert.Equal(t, 2, fake.HealthRequests, "the expired result must be re-probed")
}

func TestReadinessCachesFailuresToo(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.HealthStatus = "unhealthy"
	h := newReadinessServer(t, fake, "Test SDF Network ; September 2015", time.Minute)

	rec := do(t, h, http.MethodGet, "/readyz", "", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// The upstream recovers, but the cached verdict stands until the TTL
	// passes: probes must not hammer an endpoint that is already down.
	fake.HealthStatus = ""
	rec = do(t, h, http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, 1, fake.HealthRequests)
}

func TestLivenessStillIgnoresReadiness(t *testing.T) {
	t.Parallel()

	// The two probes stay independent: an unhealthy RPC endpoint makes the
	// instance unready but never unlive, so a restart is not triggered.
	fake := stellartest.NewFake(t)
	fake.HealthErr = errors.New("rpc down")
	h := newServer(t, fake)

	require.Equal(t, http.StatusServiceUnavailable,
		do(t, h, http.MethodGet, "/readyz", "", nil).Code)

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/healthz", "", &body)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", body["status"])
}

// testToken is long enough to satisfy config.MinAPITokenLength.
const testToken = "0123456789abcdef"

// newServerWithToken builds a handler that requires bearer auth on /v1.
func newServerWithToken(t *testing.T, token string) http.Handler {
	t.Helper()

	p, err := probe.New(probe.Options{
		Client:        stellartest.NewFake(t),
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	return api.New(api.Options{Prober: p, APIToken: token}).Handler()
}

// doAuth issues a request carrying the given Authorization header value. An
// empty header sends no Authorization header at all.
func doAuth(t *testing.T, h http.Handler, method, target, header string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, strings.NewReader(""))
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthMissingTokenIsUnauthorized(t *testing.T) {
	t.Parallel()

	h := newServerWithToken(t, testToken)

	rec := doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	// WWW-Authenticate tells the client how to authenticate, per RFC 7235.
	assert.Equal(t, `Bearer realm="soroprobe"`, rec.Header().Get("WWW-Authenticate"))
	assert.Contains(t, rec.Body.String(), "missing bearer token")
}

func TestAuthWrongTokenIsUnauthorized(t *testing.T) {
	t.Parallel()

	h := newServerWithToken(t, testToken)

	for _, header := range []string{
		"Bearer " + testToken + "x",
		"Bearer not-the-token-at-all",
		"Bearer " + testToken[:len(testToken)-1],
	} {
		rec := doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, header)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "header was: %s", header)
		assert.Contains(t, rec.Body.String(), "invalid bearer token")
	}
}

func TestAuthMalformedAuthorizationHeaderIsUnauthorized(t *testing.T) {
	t.Parallel()

	h := newServerWithToken(t, testToken)

	for _, header := range []string{
		"Basic " + testToken,
		"Bearer",
		"Bearer    ",
		testToken, // the bare token, without the scheme
	} {
		rec := doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, header)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "header was: %q", header)
	}
}

func TestAuthCorrectTokenIsAccepted(t *testing.T) {
	t.Parallel()

	h := newServerWithToken(t, testToken)

	rec := doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, "Bearer "+testToken)
	assert.Equal(t, http.StatusOK, rec.Code)

	req := httptest.NewRequest(http.MethodPost, "/v1/simulate", strings.NewReader(
		`{"contract_id":"`+stellartest.SACContract+`","function":"decimals"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthSchemeIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	// RFC 7235 treats the auth scheme as case-insensitive, so "bearer" must
	// work exactly like "Bearer".
	h := newServerWithToken(t, testToken)

	rec := doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, "bearer "+testToken)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHealthzNeedsNoTokenWhenAuthIsEnabled(t *testing.T) {
	t.Parallel()

	// A liveness probe should not need a credential to prove the process is
	// up, and it must not start failing just because the token rotated.
	h := newServerWithToken(t, testToken)

	var body map[string]string
	rec := do(t, h, http.MethodGet, "/healthz", "", &body)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", body["status"])
}

func TestAuthAppliesToEveryV1Route(t *testing.T) {
	t.Parallel()

	h := newServerWithToken(t, testToken)

	routes := []struct {
		method string
		target string
	}{
		{http.MethodPost, "/v1/simulate"},
		{http.MethodGet, "/v1/inspect/" + stellartest.SACContract},
		{http.MethodGet, "/v1/check/" + stellartest.SACContract},
		{http.MethodPost, "/v1/checks"},
	}

	for _, rt := range routes {
		req := httptest.NewRequest(rt.method, rt.target, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s", rt.method, rt.target)
	}
}

func TestAPIStaysOpenWhenNoTokenIsConfigured(t *testing.T) {
	t.Parallel()

	// Unset keeps the previous, unauthenticated behavior — no credential
	// is required and none is rejected.
	h := newServer(t, stellartest.NewFake(t))

	rec := doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, "")
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = doAuth(t, h, http.MethodGet, "/v1/inspect/"+stellartest.SACContract, "Bearer "+testToken)
	assert.Equal(t, http.StatusOK, rec.Code, "a stray Authorization header is ignored when auth is off")
}
