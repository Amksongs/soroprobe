package probe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
)

func TestInspectCacheKeyDistinguishesRequests(t *testing.T) {
	t.Parallel()

	base := InspectRequest{
		ContractID: "CABC",
		DataKeys:   []string{"sym:Balance", "sym:Admin"},
	}

	// An empty durability is persistent, so the two spellings share an entry.
	same := InspectRequest{
		ContractID:     "CABC",
		DataKeys:       []string{"sym:Balance", "sym:Admin"},
		DataDurability: health.DurabilityPersistent,
	}
	assert.Equal(t, inspectCacheKey(base), inspectCacheKey(same))

	reversed := base
	reversed.DataKeys = []string{"sym:Admin", "sym:Balance"}
	assert.NotEqual(t, inspectCacheKey(base), inspectCacheKey(reversed),
		"key order is part of the result, so it must be part of the key")

	temporary := base
	temporary.DataDurability = health.DurabilityTemporary
	assert.NotEqual(t, inspectCacheKey(base), inspectCacheKey(temporary))

	otherContract := base
	otherContract.ContractID = "CXYZ"
	assert.NotEqual(t, inspectCacheKey(base), inspectCacheKey(otherContract))

	fewerKeys := base
	fewerKeys.DataKeys = []string{"sym:Balance"}
	assert.NotEqual(t, inspectCacheKey(base), inspectCacheKey(fewerKeys))
}

func TestInspectCacheExpires(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_000, 0)
	c := newInspectCache(time.Second, 4)
	c.now = func() time.Time { return now }

	c.put("key", &InspectResult{ContractID: "CABC", LatestLedger: 42})

	got, ok := c.get("key")
	require.True(t, ok)
	assert.EqualValues(t, 42, got.LatestLedger)

	// Exactly at the deadline the entry is no longer fresh.
	now = now.Add(time.Second)
	_, ok = c.get("key")
	assert.False(t, ok, "an entry must expire when its window elapses")
}

func TestInspectCacheStaysBounded(t *testing.T) {
	t.Parallel()

	c := newInspectCache(time.Minute, 2)
	for _, key := range []string{"a", "b", "c"} {
		c.put(key, &InspectResult{ContractID: key})
	}

	assert.Len(t, c.entries, 2, "the cache must never exceed its bound")

	present := 0
	for _, key := range []string{"a", "b", "c"} {
		if _, ok := c.get(key); ok {
			present++
		}
	}
	assert.Equal(t, 2, present)
}

func TestInspectCacheReturnsCopies(t *testing.T) {
	t.Parallel()

	ledger := uint32(100)
	c := newInspectCache(time.Minute, 4)
	source := &InspectResult{
		ContractID: "CABC",
		Instance:   EntryReport{TTL: health.TTL{LiveUntilLedger: &ledger}},
		Data:       []EntryReport{{Key: "sym:Balance"}},
		Code:       &EntryReport{Kind: "code"},
	}
	c.put("key", source)

	// Mutating the value that was stored must not affect the cache.
	source.Data[0].Key = "mutated-source"

	first, ok := c.get("key")
	require.True(t, ok)
	assert.Equal(t, "sym:Balance", first.Data[0].Key)

	// Mutating a returned value must not affect later hits either.
	first.Data[0].Key = "mutated-hit"
	first.Code.Kind = "mutated-code"
	*first.Instance.LiveUntilLedger = 999

	second, ok := c.get("key")
	require.True(t, ok)
	assert.Equal(t, "sym:Balance", second.Data[0].Key)
	assert.Equal(t, "code", second.Code.Kind)
	assert.EqualValues(t, 100, *second.Instance.LiveUntilLedger)
}

func TestInspectCacheNilIsSafe(t *testing.T) {
	t.Parallel()

	var c *inspectCache
	_, ok := c.get("key")
	assert.False(t, ok)
	c.put("key", &InspectResult{})
	assert.NotPanics(t, func() { c.put("key", nil) })
}
