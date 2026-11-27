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

// Boundaries for the numeric fields the RPC responses carry. Naming them keeps
// the edge-case tables readable and makes the "just past the boundary" cases
// next to the values they exceed.
const (
	maxUint32 = 4_294_967_295
	maxInt64  = 9_223_372_036_854_775_807
	maxInt32  = 2_147_483_647
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

func TestRPCClientSimulateTransactionEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tx      string
		result  map[string]any
		want    protocol.SimulateTransactionResponse
		wantErr bool
	}{
		{
			// An empty envelope is not rejected client-side: the RPC node owns
			// envelope validation, so the client must forward it and let the
			// node's answer be what the caller sees.
			name:   "empty transaction",
			tx:     "",
			result: map[string]any{},
		},
		{
			name: "boundary fee and ledger",
			tx:   "irrelevant",
			result: map[string]any{
				"minResourceFee": "9223372036854775807",
				"latestLedger":   uint32(maxUint32),
			},
			want: protocol.SimulateTransactionResponse{
				MinResourceFee: maxInt64,
				LatestLedger:   maxUint32,
			},
		},
		{
			// Results must be an array of host-function results; a scalar is a
			// malformed response and must not decode into an empty success.
			name:    "results is not an array",
			tx:      "irrelevant",
			result:  map[string]any{"results": "not-an-array"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, stub := newStubClient(t, map[string]any{"simulateTransaction": tt.result})

			resp, err := client.SimulateTransaction(context.Background(),
				protocol.SimulateTransactionRequest{Transaction: tt.tx})
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "simulateTransaction")
				assert.Equal(t, protocol.SimulateTransactionResponse{}, resp)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, resp)
			assert.Equal(t, []string{"simulateTransaction"}, stub.methods)
		})
	}
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

func TestRPCClientGetLatestLedgerEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  map[string]any
		want    protocol.GetLatestLedgerResponse
		wantErr bool
	}{
		{
			// An empty result keeps every field at its zero value rather than
			// the client inventing a ledger number of its own.
			name:   "empty result",
			result: map[string]any{},
		},
		{
			name: "boundary sequence and protocol version",
			result: map[string]any{
				"id":              "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				"sequence":        uint32(maxUint32),
				"protocolVersion": uint32(maxUint32),
				"closeTime":       "9223372036854775807",
			},
			want: protocol.GetLatestLedgerResponse{
				Hash:            "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				Sequence:        maxUint32,
				ProtocolVersion: maxUint32,
				LedgerCloseTime: maxInt64,
			},
		},
		{
			// A ledger sequence is an unsigned number; a negative one cannot be
			// wrapped into range without silently reporting a real ledger.
			name:    "negative sequence",
			result:  map[string]any{"sequence": -1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, stub := newStubClient(t, map[string]any{"getLatestLedger": tt.result})

			resp, err := client.GetLatestLedger(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "getLatestLedger")
				assert.Equal(t, protocol.GetLatestLedgerResponse{}, resp)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, resp)
			assert.Equal(t, []string{"getLatestLedger"}, stub.methods)
		})
	}
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

func TestRPCClientGetNetworkEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  map[string]any
		want    protocol.GetNetworkResponse
		wantErr bool
	}{
		{
			// An empty result must not be confused with a wrong-network
			// answer: the fields stay zero and no error is invented.
			name:   "empty result",
			result: map[string]any{},
		},
		{
			name: "boundary protocol version",
			result: map[string]any{
				"passphrase":      "Public Global Stellar Network ; September 2015",
				"protocolVersion": int(maxInt32),
				"friendbotUrl":    "https://friendbot.stellar.org",
			},
			want: protocol.GetNetworkResponse{
				Passphrase:      "Public Global Stellar Network ; September 2015",
				ProtocolVersion: int(maxInt32),
				FriendbotURL:    "https://friendbot.stellar.org",
			},
		},
		{
			// protocolVersion is a number on the wire; a quoted value is a
			// malformed response and must not decode into a silent zero.
			name: "protocol version is a string",
			result: map[string]any{
				"passphrase":      "Test SDF Network ; September 2015",
				"protocolVersion": "27",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, stub := newStubClient(t, map[string]any{"getNetwork": tt.result})

			resp, err := client.GetNetwork(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "getNetwork")
				assert.Equal(t, protocol.GetNetworkResponse{}, resp)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, resp)
			assert.Equal(t, []string{"getNetwork"}, stub.methods)
		})
	}
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

func TestRPCClientGetHealthEdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  map[string]any
		want    protocol.GetHealthResponse
		wantErr bool
	}{
		{
			// An empty result is still a well-formed response: status and the
			// retention window keep their zero values instead of the client
			// treating an absent window as healthy.
			name:   "empty result",
			result: map[string]any{},
		},
		{
			name: "boundary ledger window",
			result: map[string]any{
				"status":                "healthy",
				"latestLedger":          uint32(maxUint32),
				"latestLedgerCloseTime": "9223372036854775807",
				"oldestLedger":          uint32(maxUint32),
				"oldestLedgerCloseTime": "9223372036854775807",
				"ledgerRetentionWindow": uint32(maxUint32),
			},
			want: protocol.GetHealthResponse{
				Status:                "healthy",
				LatestLedger:          maxUint32,
				LatestLedgerCloseTime: maxInt64,
				OldestLedger:          maxUint32,
				OldestLedgerCloseTime: maxInt64,
				LedgerRetentionWindow: maxUint32,
			},
		},
		{
			// One past the end of uint32 must fail to decode rather than wrap
			// around to ledger 0, which would read as an empty chain.
			name:    "latest ledger beyond uint32",
			result:  map[string]any{"latestLedger": uint64(maxUint32) + 1},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, stub := newStubClient(t, map[string]any{"getHealth": tt.result})

			resp, err := client.GetHealth(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "getHealth")
				assert.Equal(t, protocol.GetHealthResponse{}, resp)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, resp)
			assert.Equal(t, []string{"getHealth"}, stub.methods)
		})
	}
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
