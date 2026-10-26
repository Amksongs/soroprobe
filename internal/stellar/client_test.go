package stellar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// rpcStub serves canned JSON-RPC results, so the live client can be exercised
// without a network. It records the method names it was asked for.
type rpcStub struct {
	t       *testing.T
	results map[string]any
	methods []string
	status  int
	server  *httptest.Server
}

func newRPCStub(t *testing.T, results map[string]any) *rpcStub {
	t.Helper()

	s := &rpcStub{t: t, results: results, status: http.StatusOK}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

func (s *rpcStub) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	require.NoError(s.t, json.NewDecoder(r.Body).Decode(&req))
	s.methods = append(s.methods, req.Method)

	if s.status != http.StatusOK {
		w.WriteHeader(s.status)
		return
	}

	result, ok := s.results[req.Method]
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func newStubClient(t *testing.T, results map[string]any) (*stellar.RPCClient, *rpcStub) {
	t.Helper()

	stub := newRPCStub(t, results)
	client, err := stellar.New(stellar.Options{URL: stub.server.URL, Timeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client, stub
}

func TestRPCClientSimulateTransaction(t *testing.T) {
	t.Parallel()

	// Replay a real recorded response through the real client.
	fixture := stellartest.LoadSimulate(t, "simulate_success.json")
	client, stub := newStubClient(t, map[string]any{"simulateTransaction": fixture})

	resp, err := client.SimulateTransaction(context.Background(),
		protocol.SimulateTransactionRequest{Transaction: "irrelevant"})
	require.NoError(t, err)

	assert.Empty(t, resp.Error)
	assert.Equal(t, fixture.MinResourceFee, resp.MinResourceFee)
	require.Len(t, resp.Results, 1)
	assert.Equal(t, []string{"simulateTransaction"}, stub.methods)
}

func TestRPCClientGetLedgerEntries(t *testing.T) {
	t.Parallel()

	fixture := stellartest.LoadLedgerEntries(t, "ledger_entries_sac_instance.json")
	client, stub := newStubClient(t, map[string]any{"getLedgerEntries": fixture})

	resp, err := client.GetLedgerEntries(context.Background(),
		protocol.GetLedgerEntriesRequest{Keys: []string{"key"}})
	require.NoError(t, err)

	require.Len(t, resp.Entries, 1)
	// liveUntilLedgerSeq is the field the whole health model rests on, so it
	// must survive the round trip through the live client.
	require.NotNil(t, resp.Entries[0].LiveUntilLedgerSeq)
	assert.Equal(t, *fixture.Entries[0].LiveUntilLedgerSeq, *resp.Entries[0].LiveUntilLedgerSeq)
	assert.Equal(t, []string{"getLedgerEntries"}, stub.methods)
}

func TestRPCClientGetLatestLedger(t *testing.T) {
	t.Parallel()

	client, _ := newStubClient(t, map[string]any{
		"getLatestLedger": map[string]any{"id": "abc", "sequence": 4014125, "protocolVersion": 27},
	})

	resp, err := client.GetLatestLedger(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 4014125, resp.Sequence)
	assert.EqualValues(t, 27, resp.ProtocolVersion)
}

func TestRPCClientGetNetwork(t *testing.T) {
	t.Parallel()

	client, _ := newStubClient(t, map[string]any{
		"getNetwork": map[string]any{"passphrase": "Test SDF Network ; September 2015"},
	})

	resp, err := client.GetNetwork(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Test SDF Network ; September 2015", resp.Passphrase)
}

func TestRPCClientGetHealth(t *testing.T) {
	t.Parallel()

	client, _ := newStubClient(t, map[string]any{
		"getHealth": map[string]any{"status": "healthy", "latestLedger": 4014125},
	})

	resp, err := client.GetHealth(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "healthy", resp.Status)
}

func TestRPCClientWrapsErrorsWithMethodName(t *testing.T) {
	t.Parallel()

	// No canned results, so every call comes back as a JSON-RPC error. The
	// wrapper should name the method that failed.
	client, _ := newStubClient(t, map[string]any{})
	ctx := context.Background()

	_, err := client.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "simulateTransaction")

	_, err = client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{"k"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getLedgerEntries")

	_, err = client.GetLatestLedger(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getLatestLedger")

	_, err = client.GetNetwork(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getNetwork")

	_, err = client.GetHealth(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getHealth")

	_, err = client.GetEvents(ctx, protocol.GetEventsRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getEvents")
}

func TestRPCClientHandlesTransportFailure(t *testing.T) {
	t.Parallel()

	client, stub := newStubClient(t, map[string]any{})
	stub.status = http.StatusInternalServerError

	_, err := client.GetLatestLedger(context.Background())
	require.Error(t, err)
}

func TestRPCClientRespectsCallerDeadline(t *testing.T) {
	t.Parallel()

	client, _ := newStubClient(t, map[string]any{
		"getLatestLedger": map[string]any{"id": "abc", "sequence": 1},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.GetLatestLedger(ctx)
	require.Error(t, err, "a canceled context must not be ignored")
}
