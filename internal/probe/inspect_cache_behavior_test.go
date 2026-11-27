package probe_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func newCachedProber(t *testing.T, fake *stellartest.Fake, ttl time.Duration) *probe.Prober {
	t.Helper()

	p, err := probe.New(probe.Options{
		Client:          fake,
		SourceAccount:   stellartest.SourceAccount,
		Thresholds:      health.DefaultThresholds,
		InspectCacheTTL: ttl,
	})
	require.NoError(t, err)
	return p
}

func TestInspectCacheHitAvoidsRPC(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newCachedProber(t, fake, time.Minute)
	req := probe.InspectRequest{ContractID: stellartest.SACContract}

	first, err := p.Inspect(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, fake.EntryRequests, 1)

	second, err := p.Inspect(context.Background(), req)
	require.NoError(t, err)

	assert.Len(t, fake.EntryRequests, 1, "a cache hit must not make an RPC call")
	assert.Equal(t, first.LatestLedger, second.LatestLedger,
		"a cached report keeps the ledger it was computed at")
	assert.Equal(t, first.Status, second.Status)
}

func TestInspectCacheIsScopedToTheKeySet(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newCachedProber(t, fake, time.Minute)
	ctx := context.Background()

	balance := probe.InspectRequest{
		ContractID: stellartest.SACContract,
		DataKeys:   []string{"sym:Balance"},
	}
	_, err := p.Inspect(ctx, balance)
	require.NoError(t, err)
	require.Len(t, fake.EntryRequests, 1)

	// Same contract and keys: a hit.
	_, err = p.Inspect(ctx, balance)
	require.NoError(t, err)
	assert.Len(t, fake.EntryRequests, 1)

	// A different key set is a different entry, so it must reach the network.
	_, err = p.Inspect(ctx, probe.InspectRequest{
		ContractID: stellartest.SACContract,
		DataKeys:   []string{"sym:Admin"},
	})
	require.NoError(t, err)
	assert.Len(t, fake.EntryRequests, 2)

	// The first entry is still cached.
	_, err = p.Inspect(ctx, balance)
	require.NoError(t, err)
	assert.Len(t, fake.EntryRequests, 2)
}

func TestInspectCacheExpires(t *testing.T) {
	t.Parallel()

	const ttl = 25 * time.Millisecond
	fake := stellartest.NewFake(t)
	p := newCachedProber(t, fake, ttl)
	req := probe.InspectRequest{ContractID: stellartest.SACContract}

	_, err := p.Inspect(context.Background(), req)
	require.NoError(t, err)
	_, err = p.Inspect(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, fake.EntryRequests, 1)

	time.Sleep(2 * ttl)

	_, err = p.Inspect(context.Background(), req)
	require.NoError(t, err)
	assert.Len(t, fake.EntryRequests, 2, "an expired entry must be recomputed")
}

func TestInspectCacheDisabledByDefault(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newCachedProber(t, fake, 0)
	req := probe.InspectRequest{ContractID: stellartest.SACContract}

	_, err := p.Inspect(context.Background(), req)
	require.NoError(t, err)
	_, err = p.Inspect(context.Background(), req)
	require.NoError(t, err)

	assert.Len(t, fake.EntryRequests, 2, "with no TTL every request reaches the network")
}
