package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestSimulateWithAuth(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "transfer",
		Args:       []string{"addr:" + stellartest.SourceAccount, "i64:100"},
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	require.NotEmpty(t, result.Auth)

	entry := result.Auth[0]
	assert.Equal(t, stellartest.SourceAccount, entry.Address)
	assert.True(t, entry.IsSourceAccount)
	require.NotNil(t, entry.RootInvocation)
	assert.Equal(t, "transfer", entry.RootInvocation.Function)
	assert.Equal(t, stellartest.SACContract, entry.RootInvocation.ContractID)
	require.Len(t, entry.RootInvocation.Args, 2)
}
