package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// resolveAdmin finds the contract's admin address, from the zero-argument
// function fn when given, else from the storage entry named by key: instance
// storage first, then a persistent data entry. The second result says where
// the address came from.
func (p *Prober) resolveAdmin(ctx context.Context, contractID, fn, key string, inspect *InspectResult) (string, string, error) {
	if fn != "" {
		val, err := p.simulateValue(ctx, contractID, fn)
		if err != nil {
			var simErr *simulationError
			if errors.As(err, &simErr) {
				return "", "", &unresolvedError{err}
			}
			return "", "", err
		}
		addr, err := addressString(val)
		if err != nil {
			return "", "", unresolved("%s: %w", fn, err)
		}
		return addr, "function " + fn, nil
	}

	want, err := p.codec.Encode(key)
	if err != nil {
		return "", "", err
	}
	wantBytes, err := want.MarshalBinary()
	if err != nil {
		return "", "", err
	}
	if inspect.instanceStorage != nil {
		for _, e := range *inspect.instanceStorage {
			got, err := e.Key.MarshalBinary()
			if err != nil || !bytes.Equal(got, wantBytes) {
				continue
			}
			addr, err := addressString(e.Val)
			if err != nil {
				return "", "", unresolved("instance storage %s: %w", key, err)
			}
			return addr, "instance storage " + key, nil
		}
	}

	contract, err := stellar.ParseContractID(contractID)
	if err != nil {
		return "", "", err
	}
	encoded, err := stellar.EncodeKey(stellar.DataKey(contract, want, xdr.ContractDataDurabilityPersistent))
	if err != nil {
		return "", "", err
	}
	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{encoded}})
	if err != nil {
		return "", "", err
	}
	if len(resp.Entries) == 0 {
		return "", "", unresolved("no storage entry %s in the contract's instance storage or persistent data", key)
	}
	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	if err != nil {
		return "", "", err
	}
	if data.ContractData == nil {
		return "", "", unresolved("entry %s is not contract data", key)
	}
	addr, err := addressString(data.ContractData.Val)
	if err != nil {
		return "", "", unresolved("data entry %s: %w", key, err)
	}
	return addr, "data entry " + key, nil
}

// unresolvedError marks an admin that could not be determined from what the
// contract holds, as opposed to a failure to reach the network.
type unresolvedError struct{ error }

func (e *unresolvedError) Unwrap() error { return e.error }

func unresolved(format string, a ...any) error {
	return &unresolvedError{fmt.Errorf(format, a...)}
}

func addressString(v xdr.ScVal) (string, error) {
	if v.Type != xdr.ScValTypeScvAddress || v.Address == nil {
		return "", fmt.Errorf("holds %s, not an address", v.Type)
	}
	return v.Address.String()
}

// adminStep reports the security posture of the contract's admin: for an
// account its signers and thresholds, for a contract its executable. Without
// --admin-fn or --admin-key it contributes nothing.
type adminStep struct{ p *Prober }

func (s *adminStep) Name() string { return "admin" }

func (s *adminStep) Run(ctx context.Context, req CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if req.AdminFn == "" && req.AdminKey == "" {
		return nil, nil
	}
	addr, source, err := s.p.resolveAdmin(ctx, req.ContractID, req.AdminFn, req.AdminKey, inspect)
	if err != nil {
		var un *unresolvedError
		if errors.As(err, &un) {
			return []Check{{Name: "admin", Outcome: OutcomeFail, Detail: fmt.Sprintf("could not resolve the admin: %v", err)}}, nil
		}
		return nil, err
	}
	detail, outcome, err := s.p.adminPosture(ctx, addr)
	if err != nil {
		return nil, err
	}
	return []Check{{Name: "admin", Outcome: outcome, Detail: fmt.Sprintf("admin %s (from %s): %s", addr, source, detail)}}, nil
}

// adminPosture describes an admin address and judges it. A single key with
// master weight is the weakest posture, so it warns, and on the public
// network the warning says so first: that is where a lost or stolen key costs
// real money.
func (p *Prober) adminPosture(ctx context.Context, addr string) (string, CheckOutcome, error) {
	if strings.HasPrefix(addr, "C") {
		exec, err := p.executableOf(ctx, addr)
		if err != nil {
			return "", "", err
		}
		return "a contract, " + exec, OutcomePass, nil
	}

	acc, err := p.readAccount(ctx, addr)
	if err != nil {
		return "", "", err
	}
	if acc == nil {
		return "an account that does not exist on this network", OutcomeWarn, nil
	}

	th := acc.Thresholds
	master := int(th[0])
	live := 0
	var signers []string
	if master > 0 {
		live++
	}
	for _, sg := range acc.Signers {
		signers = append(signers, fmt.Sprintf("%s weight %d", sg.Key.Address(), sg.Weight))
		if sg.Weight > 0 {
			live++
		}
	}
	summary := fmt.Sprintf("an account with master weight %d, thresholds low %d / medium %d / high %d, %d additional signer(s)",
		master, th[1], th[2], th[3], len(acc.Signers))
	if len(signers) > 0 {
		summary += " (" + strings.Join(signers, ", ") + ")"
	}
	if live <= 1 && master > 0 {
		warning := "single key controls the admin"
		if p.network == "public" {
			warning = "MAINNET: " + warning
		}
		return warning + "; " + summary, OutcomeWarn, nil
	}
	return summary, OutcomePass, nil
}

// executableOf reads a contract's instance and names what it runs.
func (p *Prober) executableOf(ctx context.Context, contractID string) (string, error) {
	contract, err := stellar.ParseContractID(contractID)
	if err != nil {
		return "", err
	}
	key, err := stellar.EncodeKey(stellar.InstanceKey(contract))
	if err != nil {
		return "", err
	}
	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
	if err != nil {
		return "", err
	}
	if len(resp.Entries) == 0 {
		return "not deployed on this network", nil
	}
	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	if err != nil {
		return "", err
	}
	if data.ContractData == nil {
		return "", fmt.Errorf("instance entry of %s is not contract data", contractID)
	}
	instance, ok := data.ContractData.Val.GetInstance()
	if !ok {
		return "", fmt.Errorf("instance entry of %s holds no contract instance", contractID)
	}
	if instance.Executable.Type == xdr.ContractExecutableTypeContractExecutableWasm && instance.Executable.WasmHash != nil {
		return fmt.Sprintf("wasm hash %x", instance.Executable.WasmHash[:]), nil
	}
	return "executable " + instance.Executable.Type.String(), nil
}

// contractInterface reads the contract's interface the way the standard check
// does: from the configured ABI source, else from the Wasm on chain.
func (p *Prober) contractInterface(ctx context.Context, contractID string) (*abi.Interface, error) {
	if p.abi != nil {
		return p.abi.Interface(ctx, p.network, contractID)
	}
	return p.Interface(ctx, contractID)
}
