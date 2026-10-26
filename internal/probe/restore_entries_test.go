package probe_test

import (
	"context"
	"fmt"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestSimulateListsArchivedFootprintEntries(t *testing.T) {
	fake := stellartest.NewFake(t)
	contract, err := stellar.ParseContractID(stellartest.SACContract)
	require.NoError(t, err)
	keyName := xdr.ScSymbol("archived")
	key := stellar.DataKey(contract, xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &keyName}, xdr.ContractDataDurabilityPersistent)
	transactionData, err := xdr.MarshalBase64(xdr.SorobanTransactionData{
		Resources: xdr.SorobanResources{Footprint: xdr.LedgerFootprint{ReadWrite: []xdr.LedgerKey{key}}},
	})
	require.NoError(t, err)
	fake.Simulations["decimals"] = protocol.SimulateTransactionResponse{
		RestorePreamble: &protocol.RestorePreamble{TransactionDataXDR: transactionData, MinResourceFee: 25},
	}
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{ContractID: stellartest.SACContract, Function: "decimals"})
	require.NoError(t, err)
	require.NotNil(t, result.RestoreRequired)
	assert.Equal(t, []string{fmt.Sprintf("contract data %s (persistent)", stellartest.SACContract)}, result.RestoreRequired.ArchivedEntries)
}
