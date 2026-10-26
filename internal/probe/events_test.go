package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestEvents(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Events(context.Background(), stellartest.SACContract, probe.EventsOptions{})
	require.NoError(t, err)

	assert.Equal(t, stellartest.SACContract, res.ContractID)
	assert.Len(t, res.Events, 2)

	// Newest first
	assert.Equal(t, int32(1002), res.Events[0].Ledger)
	assert.Equal(t, "voted", res.Events[0].Topics[0])
	assert.Equal(t, uint32(10), res.Events[0].Data)

	assert.Equal(t, int32(1001), res.Events[1].Ledger)
	assert.Equal(t, "transfer", res.Events[1].Topics[0])
	assert.Equal(t, uint32(100), res.Events[1].Data)
}

func TestEventsTopicFilter(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Events(context.Background(), stellartest.SACContract, probe.EventsOptions{
		Topic: "sym:transfer",
	})
	require.NoError(t, err)

	assert.Len(t, res.Events, 1)
	assert.Equal(t, int32(1001), res.Events[0].Ledger)
	assert.Equal(t, "transfer", res.Events[0].Topics[0])
}
