package probe

import (
	"context"
	"fmt"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
)

// readAccount reads an account's ledger entry. It returns nil, nil when the
// account does not exist, because that is an answer, not a failure.
func (p *Prober) readAccount(ctx context.Context, address string) (*xdr.AccountEntry, error) {
	id, err := xdr.AddressToAccountId(address)
	if err != nil {
		return nil, fmt.Errorf("invalid account address %q: %w", address, err)
	}
	key, err := stellar.EncodeKey(xdr.LedgerKey{
		Type:    xdr.LedgerEntryTypeAccount,
		Account: &xdr.LedgerKeyAccount{AccountId: id},
	})
	if err != nil {
		return nil, err
	}
	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
	if err != nil {
		return nil, err
	}
	if len(resp.Entries) == 0 {
		return nil, nil
	}
	data, err := stellar.DecodeEntryData(resp.Entries[0].DataXDR)
	if err != nil {
		return nil, err
	}
	if data.Account == nil {
		return nil, fmt.Errorf("entry for %s is not an account (got %s)", address, data.Type)
	}
	return data.Account, nil
}
