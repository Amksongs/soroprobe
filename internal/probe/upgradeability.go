package probe

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// bestEffort is appended to the details that rest on a naming heuristic.
const bestEffort = "best effort: matched by function name"

// upgradeFunctions are names that mean "replace this contract's code" by
// convention. A contract can name its upgrade function anything, and an
// ordinary function can contain "upgrade", so this is a hint, not a proof.
var upgradeFunctions = map[string]bool{
	"upgrade":                      true,
	"upgrade_wasm":                 true,
	"update_wasm":                  true,
	"set_wasm":                     true,
	"update_current_contract_wasm": true,
}

func looksLikeUpgrade(name string) bool {
	return upgradeFunctions[name] || strings.Contains(name, "upgrade")
}

// defaultAdminKeys and defaultAdminFns are tried, in this order, when the
// request names no admin but an upgradeability report still wants one.
var (
	defaultAdminKeys = []string{"sym:Admin", "sym:admin"}
	defaultAdminFns  = []string{"admin", "get_admin", "owner"}
)

// upgradeabilityStep answers "can this contract be changed under me": does its
// interface expose an upgrade-like function, who would have to call it, and
// when was its code entry last written. Without --upgradeability it
// contributes nothing.
type upgradeabilityStep struct{ p *Prober }

func (s *upgradeabilityStep) Name() string { return "upgradeability" }

func (s *upgradeabilityStep) Run(ctx context.Context, req CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if !req.Upgradeability {
		return nil, nil
	}
	const name = "upgradeability"
	if inspect.Executable == "stellar_asset" {
		return []Check{{
			Name:    name,
			Outcome: OutcomeSkip,
			Detail:  "a stellar asset contract is implemented by the host and cannot be upgraded",
		}}, nil
	}

	iface, err := s.p.contractInterface(ctx, req.ContractID)
	switch {
	case errors.Is(err, abi.ErrUnknownContract), errors.Is(err, abi.ErrNoWasm):
		return []Check{{Name: name, Outcome: OutcomeWarn, Detail: fmt.Sprintf("the contract's interface could not be read, so upgrade functions were not looked for: %v", err)}}, nil
	case err != nil:
		return nil, err
	}

	var found []string
	exported := map[string]abi.Function{}
	for _, fn := range iface.Functions {
		exported[fn.Name] = fn
		if looksLikeUpgrade(fn.Name) {
			found = append(found, fn.Name)
		}
	}
	checks := []Check{}
	if len(found) > 0 {
		checks = append(checks, Check{Name: name + ":interface", Outcome: OutcomeWarn,
			Detail: fmt.Sprintf("exposes upgrade-like function(s): %s (%s)", strings.Join(found, ", "), bestEffort)})
	} else {
		checks = append(checks, Check{Name: name + ":interface", Outcome: OutcomePass,
			Detail: fmt.Sprintf("no upgrade-like function is exported (%s; a differently named one would not be seen)", bestEffort)})
	}

	// The admin: the one the request names, else the conventional ones.
	adminFn, adminKey := req.AdminFn, req.AdminKey
	var candidates [][2]string
	if adminFn != "" || adminKey != "" {
		candidates = append(candidates, [2]string{adminFn, adminKey})
	} else {
		for _, k := range defaultAdminKeys {
			candidates = append(candidates, [2]string{"", k})
		}
		for _, f := range defaultAdminFns {
			if fn, ok := exported[f]; ok && len(fn.Inputs) == 0 {
				candidates = append(candidates, [2]string{f, ""})
			}
		}
	}
	checks = append(checks, s.adminCheck(ctx, req.ContractID, candidates, inspect, len(found) > 0))

	code, err := s.codeModified(ctx, inspect)
	if err != nil {
		return nil, err
	}
	checks = append(checks, code)
	return checks, nil
}

// adminCheck reports the first candidate that resolves. A contract with no
// upgrade function and an admin that cannot be found is not a problem, so
// this never fails; it warns only when an upgrade function is exposed and its
// caller cannot be determined.
func (s *upgradeabilityStep) adminCheck(ctx context.Context, contractID string, candidates [][2]string, inspect *InspectResult, hasUpgrade bool) Check {
	const name = "upgradeability:admin"
	for _, c := range candidates {
		addr, source, err := s.p.resolveAdmin(ctx, contractID, c[0], c[1], inspect)
		if err != nil {
			continue
		}
		return Check{Name: name, Outcome: OutcomePass, Detail: fmt.Sprintf("upgrades would need %s, read from %s (%s; pass --admin-fn or --admin-key for the real one)", addr, source, bestEffort)}
	}
	outcome := OutcomeSkip
	if hasUpgrade {
		outcome = OutcomeWarn
	}
	return Check{Name: name, Outcome: outcome, Detail: "the admin could not be determined; pass --admin-fn or --admin-key to name it"}
}

// codeModified reports when the contract's code entry was last written.
func (s *upgradeabilityStep) codeModified(ctx context.Context, inspect *InspectResult) (Check, error) {
	const name = "upgradeability:code_modified"
	hash, err := hexHash(inspect.WasmHash)
	if err != nil || inspect.WasmHash == "" {
		return Check{Name: name, Outcome: OutcomeSkip, Detail: "the contract has no Wasm code entry"}, nil
	}
	key, err := stellar.EncodeKey(stellar.CodeKey(hash))
	if err != nil {
		return Check{}, err
	}
	resp, err := s.p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
	if err != nil {
		return Check{}, err
	}
	if len(resp.Entries) == 0 {
		return Check{Name: name, Outcome: OutcomeSkip, Detail: "the code entry is not on the ledger"}, nil
	}
	modified := resp.Entries[0].LastModifiedLedger
	if modified == 0 || modified > inspect.LatestLedger {
		return Check{Name: name, Outcome: OutcomeSkip, Detail: "the code entry reported no usable last-modified ledger"}, nil
	}
	return Check{Name: name, Outcome: OutcomePass, Detail: fmt.Sprintf(
		"code entry %s last modified at ledger %d, %d ledgers before ledger %d (a deployed hash is immutable; a changed contract shows a new hash)",
		inspect.WasmHash, modified, inspect.LatestLedger-modified, inspect.LatestLedger)}, nil
}

func hexHash(s string) (xdr.Hash, error) {
	var h xdr.Hash
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != len(h) {
		return h, fmt.Errorf("not a 32-byte hex hash")
	}
	copy(h[:], raw)
	return h, nil
}
