// Command record captures live Stellar RPC responses into the fixture files
// that SoroProbe's tests replay.
//
// The test suite never touches the network; this tool is the only thing that
// does, and it is run by hand when fixtures need refreshing:
//
//	make fixtures
//
// Fixtures are written as the JSON encoding of the SDK's protocol types, which
// is the same shape as the JSON-RPC "result" object they were decoded from.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/stellar/go-stellar-sdk/clients/rpcclient"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/soroworks/soroprobe/internal/stellar"
)

func main() {
	rpcURL := flag.String("rpc-url", "https://soroban-testnet.stellar.org", "Stellar RPC endpoint")
	outDir := flag.String("out", "internal/stellar/stellartest/testdata", "directory to write fixtures into")
	sac := flag.String("sac", "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC", "a Stellar Asset Contract to record")
	wasm := flag.String("wasm", "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4", "a Wasm contract to record")
	undeployed := flag.String("undeployed", "CCV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XK5LVOV2XMCW", "a valid but undeployed contract address")
	source := flag.String("source", "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", "source account for simulation")
	flag.Parse()

	if err := run(*rpcURL, *outDir, *sac, *wasm, *undeployed, *source); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(rpcURL, outDir, sac, wasm, undeployed, source string) error {
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return err
	}
	ctx := context.Background()
	client := rpcclient.NewClient(rpcURL, &http.Client{Timeout: 30 * time.Second})
	defer func() { _ = client.Close() }()

	latest, err := client.GetLatestLedger(ctx)
	if err != nil {
		return fmt.Errorf("getLatestLedger: %w", err)
	}
	// The raw header and metadata blobs run to hundreds of kilobytes and
	// SoroProbe reads neither, so they are dropped rather than committed.
	latest.LedgerHeader = ""
	latest.LedgerMetadata = ""
	if err := write(outDir, "latest_ledger.json", latest); err != nil {
		return err
	}
	fmt.Printf("recorded latest ledger %d (protocol %d)\n", latest.Sequence, latest.ProtocolVersion)

	// Instance entries.
	for _, c := range []struct {
		name     string
		contract string
	}{
		{"ledger_entries_sac_instance.json", sac},
		{"ledger_entries_wasm_instance.json", wasm},
		{"ledger_entries_undeployed.json", undeployed},
	} {
		resp, err := instanceEntries(ctx, client, c.contract)
		if err != nil {
			return err
		}
		if err := write(outDir, c.name, resp); err != nil {
			return err
		}
		fmt.Printf("recorded %s (%d entries)\n", c.name, len(resp.Entries))
	}

	// The Wasm code entry, whose key comes from the instance.
	codeResp, err := codeEntry(ctx, client, wasm)
	if err != nil {
		return err
	}
	if err := write(outDir, "ledger_entries_wasm_code.json", codeResp); err != nil {
		return err
	}
	fmt.Printf("recorded ledger_entries_wasm_code.json (%d entries)\n", len(codeResp.Entries))

	// Simulations: one that succeeds, one that fails.
	for _, c := range []struct {
		name     string
		contract string
		fn       string
	}{
		{"simulate_success.json", sac, "decimals"},
		{"simulate_failure.json", sac, "no_such_fn"},
	} {
		resp, err := simulate(ctx, client, source, c.contract, c.fn)
		if err != nil {
			return err
		}
		if err := write(outDir, c.name, resp); err != nil {
			return err
		}
		fmt.Printf("recorded %s (error=%q)\n", c.name, truncate(resp.Error, 40))
	}

	// The extension simulation: what extending an entry's TTL would cost.
	extendResp, err := simulateExtend(ctx, client, source, sac)
	if err != nil {
		return err
	}
	if err := write(outDir, "simulate_extend.json", extendResp); err != nil {
		return err
	}
	fmt.Printf("recorded simulate_extend.json (error=%q)\n", truncate(extendResp.Error, 40))

	// Events: get recent events for the SAC contract.
	eventsResp, err := client.GetEvents(ctx, protocol.GetEventsRequest{
		StartLedger: latest.Sequence - 100,
		Filters: []protocol.EventFilter{
			{ContractIDs: []string{sac}},
		},
		Pagination: &protocol.PaginationOptions{Limit: 10},
	})
	if err != nil {
		// Non-fatal if testnet has no events or RPC doesn't support retention
		fmt.Printf("recording events skipped: %v\n", err)
	} else {
		if err := write(outDir, "events.json", eventsResp); err != nil {
			return err
		}
		fmt.Printf("recorded events.json (%d events)\n", len(eventsResp.Events))
	}

	return nil
}

func instanceEntries(ctx context.Context, c *rpcclient.Client, contractID string) (protocol.GetLedgerEntriesResponse, error) {
	addr, err := stellar.ParseContractID(contractID)
	if err != nil {
		return protocol.GetLedgerEntriesResponse{}, err
	}
	key, err := stellar.EncodeKey(stellar.InstanceKey(addr))
	if err != nil {
		return protocol.GetLedgerEntriesResponse{}, err
	}
	return c.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
}

func codeEntry(ctx context.Context, c *rpcclient.Client, contractID string) (protocol.GetLedgerEntriesResponse, error) {
	resp, err := instanceEntries(ctx, c, contractID)
	if err != nil {
		return resp, err
	}
	if len(resp.Entries) == 0 {
		return resp, fmt.Errorf("contract %s is not deployed", contractID)
	}

	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	if err != nil {
		return resp, err
	}
	instance, ok := data.ContractData.Val.GetInstance()
	if !ok || instance.Executable.WasmHash == nil {
		return resp, fmt.Errorf("contract %s has no wasm hash", contractID)
	}

	key, err := stellar.EncodeKey(stellar.CodeKey(*instance.Executable.WasmHash))
	if err != nil {
		return resp, err
	}
	return c.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
}

func simulate(ctx context.Context, c *rpcclient.Client, source, contractID, fn string) (protocol.SimulateTransactionResponse, error) {
	addr, err := stellar.ParseContractID(contractID)
	if err != nil {
		return protocol.SimulateTransactionResponse{}, err
	}
	envelope, err := stellar.BuildInvocationTx(stellar.InvocationParams{
		SourceAccount: source,
		Contract:      addr,
		Function:      fn,
	})
	if err != nil {
		return protocol.SimulateTransactionResponse{}, err
	}
	return c.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: envelope})
}

// simulateExtend records the cost of extending a contract's instance entry,
// which is the transaction SoroProbe simulates to price a TTL extension.
func simulateExtend(ctx context.Context, c *rpcclient.Client, source, contractID string) (protocol.SimulateTransactionResponse, error) {
	addr, err := stellar.ParseContractID(contractID)
	if err != nil {
		return protocol.SimulateTransactionResponse{}, err
	}

	latest, err := c.GetLatestLedger(ctx)
	if err != nil {
		return protocol.SimulateTransactionResponse{}, err
	}

	envelope, err := stellar.BuildExtendFootprintTx(stellar.ExtendParams{
		SourceAccount: source,
		Key:           stellar.InstanceKey(addr),
		ExtendTo:      latest.Sequence + 1_000_000,
	})
	if err != nil {
		return protocol.SimulateTransactionResponse{}, err
	}
	return c.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: envelope})
}

func write(dir, name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", name, err)
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, name), raw, 0o600)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
