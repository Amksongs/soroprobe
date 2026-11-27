package probe

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
)

// Asset types AssetDetails.Type takes.
const (
	AssetNative     = "native"
	AssetAlphaNum4  = "credit_alphanum4"
	AssetAlphaNum12 = "credit_alphanum12"
)

const issuerNotFound = "the issuer account was not found on the ledger, so its authorization flags could not be read"

// AssetDetails describes the classic asset behind a Stellar Asset Contract.
//
// A SAC's behavior is decided by its issuer: an issuer with AUTH_REQUIRED or
// AUTH_REVOCABLE can freeze a holder, and one with CLAWBACK can take balances
// back. Those are the facts an integrator needs before trusting the token, and
// they live in the issuer's account entry, not in the contract.
type AssetDetails struct {
	// Type is "native", "credit_alphanum4" or "credit_alphanum12".
	Type string `json:"type"`
	// Code is the asset code. Empty for the native asset.
	Code string `json:"code,omitempty"`
	// Issuer is the issuing account ("G..."). Empty for the native asset.
	Issuer string `json:"issuer,omitempty"`
	// Admin is the contract's admin address, when the contract has one set.
	Admin string `json:"admin,omitempty"`
	// IssuerFound is false when the issuer account does not exist.
	IssuerFound *bool `json:"issuer_found,omitempty"`
	// IssuerFlags lists the issuer account's AUTH_REQUIRED, AUTH_REVOCABLE
	// and CLAWBACK flags that are set.
	IssuerFlags []string `json:"issuer_flags,omitempty"`
	// Note explains anything that could not be determined.
	Note string `json:"note,omitempty"`
}

// inspectAsset decodes the SAC's instance storage and looks up its issuer.
// The raw storage was kept by readInstanceEntry, so only the issuer account
// costs a round trip, and only for an issued asset.
func (p *Prober) inspectAsset(ctx context.Context, storage *xdr.ScMap) (*AssetDetails, error) {
	details, issuerID, err := decodeAssetStorage(storage)
	if err != nil {
		return &AssetDetails{Note: err.Error()}, nil
	}
	if issuerID == nil {
		return details, nil
	}

	key, err := stellar.EncodeKey(xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: *issuerID},
	})
	if err != nil {
		return nil, err
	}
	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
	if err != nil {
		return nil, err
	}
	found := len(resp.Entries) > 0
	details.IssuerFound = &found
	if !found {
		details.Note = issuerNotFound
		return details, nil
	}
	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	if err != nil {
		return nil, fmt.Errorf("decode issuer account: %w", err)
	}
	if data.Account == nil {
		return nil, fmt.Errorf("issuer entry is not an account (got %s)", data.Type)
	}
	details.IssuerFlags = issuerFlags(uint32(data.Account.Flags))
	return details, nil
}

// Account flag bits, from the Stellar protocol's AccountFlags.
const (
	flagAuthRequired  uint32 = 1
	flagAuthRevocable uint32 = 2
	flagClawback      uint32 = 8
)

// issuerFlags names the account flags that matter to a token holder.
func issuerFlags(flags uint32) []string {
	var out []string
	for _, f := range []struct {
		bit  uint32
		name string
	}{
		{flagAuthRequired, "AUTH_REQUIRED"},
		{flagAuthRevocable, "AUTH_REVOCABLE"},
		{flagClawback, "CLAWBACK"},
	} {
		if flags&f.bit != 0 {
			out = append(out, f.name)
		}
	}
	return out
}

// decodeAssetStorage reads the Admin and AssetInfo entries of SAC instance
// storage. The returned account ID is nil for the native asset.
func decodeAssetStorage(storage *xdr.ScMap) (*AssetDetails, *xdr.AccountId, error) {
	details := &AssetDetails{}
	if storage == nil {
		return nil, nil, fmt.Errorf("the contract has no instance storage, so its asset could not be read")
	}

	if admin, ok := sacValue(storage, "Admin"); ok && admin.Type == xdr.ScValTypeScvAddress && admin.Address != nil {
		if s, err := admin.Address.String(); err == nil {
			details.Admin = s
		}
	}

	info, ok := sacValue(storage, "AssetInfo")
	if !ok || info.Type != xdr.ScValTypeScvVec || info.Vec == nil || len(**info.Vec) == 0 {
		return nil, nil, fmt.Errorf("the contract's instance storage has no AssetInfo entry")
	}
	parts := **info.Vec
	tag, ok := parts[0].GetSym()
	if !ok {
		return nil, nil, fmt.Errorf("the AssetInfo entry is malformed")
	}

	switch string(tag) {
	case "Native":
		details.Type = AssetNative
		return details, nil, nil
	case "AlphaNum4":
		details.Type = AssetAlphaNum4
	case "AlphaNum12":
		details.Type = AssetAlphaNum12
	default:
		return nil, nil, fmt.Errorf("the AssetInfo entry has an unknown asset type %q", string(tag))
	}

	if len(parts) != 2 || parts[1].Type != xdr.ScValTypeScvMap || parts[1].Map == nil {
		return nil, nil, fmt.Errorf("the AssetInfo entry has no asset body")
	}
	for _, entry := range **parts[1].Map {
		name, ok := entry.Key.GetSym()
		if !ok {
			continue
		}
		switch string(name) {
		case "asset_code":
			if s, ok := entry.Val.GetStr(); ok {
				details.Code = string(s)
			}
		case "issuer":
			if b, ok := entry.Val.GetBytes(); ok {
				if len(b) != 32 {
					return nil, nil, fmt.Errorf("the asset issuer is %d bytes, not 32", len(b))
				}
				details.Issuer = strkey.MustEncode(strkey.VersionByteAccountID, b)
			}
		}
	}
	if details.Code == "" || details.Issuer == "" {
		return nil, nil, fmt.Errorf("the AssetInfo entry is missing its asset code or issuer")
	}

	var id xdr.AccountId
	if err := id.SetAddress(details.Issuer); err != nil {
		return nil, nil, fmt.Errorf("the asset issuer is not a valid account: %w", err)
	}
	return details, &id, nil
}

// sacValue finds the instance storage entry keyed by vec[Sym name], which is
// how the host stores SAC metadata.
func sacValue(storage *xdr.ScMap, name string) (xdr.ScVal, bool) {
	for _, entry := range *storage {
		if entry.Key.Type != xdr.ScValTypeScvVec || entry.Key.Vec == nil {
			continue
		}
		items := **entry.Key.Vec
		if len(items) != 1 {
			continue
		}
		if sym, ok := items[0].GetSym(); ok && string(sym) == name {
			return entry.Val, true
		}
	}
	return xdr.ScVal{}, false
}
