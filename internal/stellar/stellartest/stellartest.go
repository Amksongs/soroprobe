// Package stellartest provides a fixture-backed fake of stellar.Client.
//
// SoroProbe's tests never reach the network. They replay responses recorded
// from the public testnet by the sibling `record` command; see the Makefile's
// `fixtures` target.
//
// The fake resolves ledger entries by key and simulations by function name,
// decoding the transaction envelope it is handed to find that name. That means
// a test exercising the fake also exercises the real ledger-key and
// transaction-building code, rather than trusting it.
package stellartest

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
)

//go:embed testdata/*.json
var fixtures embed.FS

// Contract addresses that the recorded fixtures correspond to.
const (
	// SACContract is a Stellar Asset Contract: host-implemented, so it has an
	// instance entry but no Wasm code entry.
	SACContract = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	// WasmContract is a Wasm-backed contract with separate instance and code
	// entries, each carrying its own TTL.
	WasmContract = "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4"
	// UndeployedContract is a well-formed address with nothing deployed at it.
	UndeployedContract = "CCV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XMCW"
	// SourceAccount is the placeholder public key used to build transactions.
	SourceAccount = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"
	// ExtendFootprintTTL is the subject name of a transaction that extends
	// the TTL of a ledger entry rather than invoking a contract function.
	ExtendFootprintTTL = "extend_footprint_ttl"
	// PersistentDataKey is the SAC balance entry recorded in
	// ledger_entries_data_durability.json, stored with persistent durability.
	PersistentDataKey = `vec:["sym:Balance","addr:CBXZBQ6NW3TGPGLTR4JCG2VPVWFLCCHKDW75NJIXYXV7CJLNUO64VTFC"]`
	// TemporaryDataKey is the SAC allowance entry recorded in the same
	// fixture, stored with temporary durability.
	TemporaryDataKey = `vec:["sym:Allowance","map:[[\"sym:from\",\"addr:GB32JP5GNOQLW4NUMV6H2UBOSMB4DROL7YNDBWAHYXMVFLLJOH3LXKHK\"],[\"sym:spender\",\"addr:CA6P26K4QNNIMTYP22ILTSCXQQDEKNWJIZJPC34YEZYOLBNT7YUPX7XS\"]]"]`
)

// Load decodes a fixture file into v.
func Load(t testing.TB, name string, v any) {
	t.Helper()
	raw, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
}

// LoadLedgerEntries decodes a getLedgerEntries fixture.
func LoadLedgerEntries(t testing.TB, name string) protocol.GetLedgerEntriesResponse {
	t.Helper()
	var resp protocol.GetLedgerEntriesResponse
	Load(t, name, &resp)
	return resp
}

// LoadSimulate decodes a simulateTransaction fixture.
func LoadSimulate(t testing.TB, name string) protocol.SimulateTransactionResponse {
	t.Helper()
	var resp protocol.SimulateTransactionResponse
	Load(t, name, &resp)
	return resp
}

// LoadLatestLedger decodes the getLatestLedger fixture.
func LoadLatestLedger(t testing.TB) protocol.GetLatestLedgerResponse {
	t.Helper()
	var resp protocol.GetLatestLedgerResponse
	Load(t, "latest_ledger.json", &resp)
	return resp
}

// Fake is a stellar.Client backed by fixtures.
//
// The zero value is usable but returns nothing; prefer NewFake.
type Fake struct {
	mu sync.Mutex

	// Entries maps a base64 LedgerKey to the entry stored at it. Keys absent
	// from the map are reported as not found, exactly as RPC does.
	Entries map[string]protocol.LedgerEntryResult
	// LatestLedger is reported by every response.
	LatestLedger uint32
	// Simulations maps a contract function name to the response to return.
	Simulations map[string]protocol.SimulateTransactionResponse
	// SimulateErr, EntriesErr and LedgerErr inject transport failures.
	SimulateErr           error
	EntriesErr            error
	LedgerErr             error
	LedgersErr            error
	LedgerCloseInterval   time.Duration
	LatestLedgerCloseTime int64

	// HealthErr and NetworkErr inject getHealth and getNetwork failures.
	HealthErr  error
	NetworkErr error
	// HealthStatus and NetworkPassphrase override what getHealth and
	// getNetwork report. Empty keeps the default answer, so a Fake that
	// leaves them alone behaves exactly as it always has.
	HealthStatus      string
	NetworkPassphrase string

	// SimulateRequests and EntryRequests record what the code under test asked
	// for, so tests can assert on the requests themselves.
	SimulateRequests []protocol.SimulateTransactionRequest
	EntryRequests    [][]string
	// HealthRequests and NetworkRequests count readiness probes, so a test
	// can assert that a cache kept them from multiplying.
	HealthRequests  int
	NetworkRequests int
	// Functions records the decoded function name of each simulated call.
	Functions []string
}

var _ stellar.Client = (*Fake)(nil)

// NewFake builds a Fake wired to the standard recorded fixtures: the Stellar
// Asset Contract and Wasm contract instances, the Wasm code entry, and both a
// succeeding and a failing simulation.
func NewFake(t testing.TB) *Fake {
	t.Helper()

	f := &Fake{
		Entries:     map[string]protocol.LedgerEntryResult{},
		Simulations: map[string]protocol.SimulateTransactionResponse{},
	}

	latest := LoadLatestLedger(t)
	f.LatestLedger = latest.Sequence
	f.LatestLedgerCloseTime = latest.LedgerCloseTime
	f.LedgerCloseInterval = 5 * time.Second

	for _, name := range []string{
		"ledger_entries_sac_instance.json",
		"ledger_entries_wasm_instance.json",
		"ledger_entries_wasm_code.json",
		"ledger_entries_data_durability.json",
	} {
		resp := LoadLedgerEntries(t, name)
		for _, entry := range resp.Entries {
			f.Entries[entry.KeyXDR] = entry
		}
		// Keep the fake's ledger consistent with the recorded entries.
		if resp.LatestLedger > f.LatestLedger {
			f.LatestLedger = resp.LatestLedger
		}
	}

	f.Simulations["decimals"] = LoadSimulate(t, "simulate_success.json")
	f.Simulations["no_such_fn"] = LoadSimulate(t, "simulate_failure.json")
	f.Simulations["transfer"] = LoadSimulate(t, "simulate_auth.json")
	f.Simulations[ExtendFootprintTTL] = LoadSimulate(t, "simulate_extend.json")
	return f
}

// SimulateTransaction implements stellar.Client.
func (f *Fake) SimulateTransaction(_ context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
	f.mu.Lock()
	f.SimulateRequests = append(f.SimulateRequests, req)
	f.mu.Unlock()
	if f.SimulateErr != nil {
		return protocol.SimulateTransactionResponse{}, f.SimulateErr
	}

	fn, err := SubjectOf(req.Transaction)
	if err != nil {
		return protocol.SimulateTransactionResponse{}, err
	}
	f.mu.Lock()
	f.Functions = append(f.Functions, fn)
	f.mu.Unlock()

	resp, ok := f.Simulations[fn]
	if !ok {
		return protocol.SimulateTransactionResponse{
			Error:        fmt.Sprintf("no fixture recorded for function %q", fn),
			LatestLedger: f.LatestLedger,
		}, nil
	}
	return resp, nil
}

// GetLedgerEntries implements stellar.Client.
func (f *Fake) GetLedgerEntries(_ context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	f.mu.Lock()
	f.EntryRequests = append(f.EntryRequests, req.Keys)
	f.mu.Unlock()
	if f.EntriesErr != nil {
		return protocol.GetLedgerEntriesResponse{}, f.EntriesErr
	}

	resp := protocol.GetLedgerEntriesResponse{LatestLedger: f.LatestLedger}
	for _, key := range req.Keys {
		if entry, ok := f.Entries[key]; ok {
			resp.Entries = append(resp.Entries, entry)
		}
	}
	return resp, nil
}

// GetLatestLedger implements stellar.Client.
func (f *Fake) GetLatestLedger(context.Context) (protocol.GetLatestLedgerResponse, error) {
	if f.LedgerErr != nil {
		return protocol.GetLatestLedgerResponse{}, f.LedgerErr
	}
	return protocol.GetLatestLedgerResponse{Sequence: f.LatestLedger, ProtocolVersion: 27, LedgerCloseTime: f.LatestLedgerCloseTime}, nil
}

// GetLedgers implements stellar.Client.
func (f *Fake) GetLedgers(_ context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error) {
	if f.LedgersErr != nil {
		return protocol.GetLedgersResponse{}, f.LedgersErr
	}
	limit := uint(20)
	if req.Pagination != nil && req.Pagination.Limit > 0 {
		limit = req.Pagination.Limit
	}
	interval := int64(f.LedgerCloseInterval / time.Second)
	if interval <= 0 {
		interval = 5
	}
	start := req.StartLedger
	ledgers := make([]protocol.LedgerInfo, 0, limit)
	for sequence := start; sequence <= f.LatestLedger && uint(len(ledgers)) < limit; sequence++ {
		age := int64(f.LatestLedger - sequence)
		ledgers = append(ledgers, protocol.LedgerInfo{
			Sequence:        sequence,
			LedgerCloseTime: f.LatestLedgerCloseTime - age*interval,
		})
	}
	return protocol.GetLedgersResponse{Ledgers: ledgers, LatestLedger: f.LatestLedger}, nil
}

// GetNetwork implements stellar.Client.
func (f *Fake) GetNetwork(context.Context) (protocol.GetNetworkResponse, error) {
	f.mu.Lock()
	f.NetworkRequests++
	f.mu.Unlock()
	if f.NetworkErr != nil {
		return protocol.GetNetworkResponse{}, f.NetworkErr
	}
	passphrase := f.NetworkPassphrase
	if passphrase == "" {
		passphrase = "Test SDF Network ; September 2015"
	}
	return protocol.GetNetworkResponse{Passphrase: passphrase}, nil
}

// GetHealth implements stellar.Client.
func (f *Fake) GetHealth(context.Context) (protocol.GetHealthResponse, error) {
	f.mu.Lock()
	f.HealthRequests++
	f.mu.Unlock()
	if f.HealthErr != nil {
		return protocol.GetHealthResponse{}, f.HealthErr
	}
	status := f.HealthStatus
	if status == "" {
		status = "healthy"
	}
	return protocol.GetHealthResponse{Status: status, LatestLedger: f.LatestLedger}, nil
}

// SetTTL overrides the liveUntilLedgerSeq of every stored entry belonging to a
// contract, so tests can drive the health thresholds without new fixtures.
func (f *Fake) SetTTL(t testing.TB, key string, liveUntil uint32) {
	t.Helper()
	entry, ok := f.Entries[key]
	if !ok {
		t.Fatalf("no fixture entry for key %s", key)
	}
	entry.LiveUntilLedgerSeq = &liveUntil
	f.Entries[key] = entry
}

// InstanceKeyOf returns the base64 instance ledger key for a contract, which is
// how tests address entries in the fake.
func InstanceKeyOf(t testing.TB, contractID string) string {
	t.Helper()
	addr, err := stellar.ParseContractID(contractID)
	if err != nil {
		t.Fatalf("parse contract id: %v", err)
	}
	key, err := stellar.EncodeKey(stellar.InstanceKey(addr))
	if err != nil {
		t.Fatalf("encode instance key: %v", err)
	}
	return key
}

// SubjectOf decodes a base64 transaction envelope and returns the name of
// what it does: the contract function it invokes, or ExtendFootprintTTL for
// an extension.
func SubjectOf(envelope string) (string, error) {
	var env xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(envelope, &env); err != nil {
		return "", fmt.Errorf("decode envelope: %w", err)
	}

	ops := env.Operations()
	if len(ops) != 1 {
		return "", fmt.Errorf("expected exactly 1 operation, got %d", len(ops))
	}

	if _, ok := ops[0].Body.GetExtendFootprintTtlOp(); ok {
		return ExtendFootprintTTL, nil
	}

	invoke, ok := ops[0].Body.GetInvokeHostFunctionOp()
	if !ok {
		return "", fmt.Errorf("operation is neither invokeHostFunction nor extendFootprintTtl")
	}
	args, ok := invoke.HostFunction.GetInvokeContract()
	if !ok {
		return "", fmt.Errorf("host function is not invokeContract")
	}
	return string(args.FunctionName), nil
}

// ArgsOf decodes a base64 transaction envelope and returns the ScVal arguments
// it passes to the contract.
func ArgsOf(envelope string) ([]xdr.ScVal, error) {
	var env xdr.TransactionEnvelope
	if err := xdr.SafeUnmarshalBase64(envelope, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	ops := env.Operations()
	if len(ops) != 1 {
		return nil, fmt.Errorf("expected exactly 1 operation, got %d", len(ops))
	}
	invoke, ok := ops[0].Body.GetInvokeHostFunctionOp()
	if !ok {
		return nil, fmt.Errorf("operation is not invokeHostFunction")
	}
	args, ok := invoke.HostFunction.GetInvokeContract()
	if !ok {
		return nil, fmt.Errorf("host function is not invokeContract")
	}
	return args.Args, nil
}
