package probe

import (
	"strings"
	"sync"
	"time"

	"github.com/soroworks/soroprobe/internal/health"
)

// inspectCacheMaxEntries bounds the cache, so a long-running serve process
// cannot grow it without limit. A dashboard polling a few hundred contracts
// fits comfortably; beyond that, entries are evicted.
const inspectCacheMaxEntries = 256

// inspectCache memoises Inspect results for a short window.
//
// TTLs change at most once per ledger (about five seconds), so a report a few
// seconds old is still honest as long as it keeps the ledger it was computed at
// — InspectResult.LatestLedger does exactly that. The cache is opt-in: a zero
// TTL means disabled, and simulations are never cached.
type inspectCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	now     func() time.Time
	entries map[string]inspectCacheEntry
}

type inspectCacheEntry struct {
	result  *InspectResult
	expires time.Time
}

func newInspectCache(ttl time.Duration, max int) *inspectCache {
	if max <= 0 {
		max = inspectCacheMaxEntries
	}
	return &inspectCache{
		ttl:     ttl,
		max:     max,
		now:     time.Now,
		entries: make(map[string]inspectCacheEntry),
	}
}

// inspectCacheKey identifies a request by contract, durability and the exact
// ordered key set. DataKeys order shows up in the result, so a different order
// is a different request rather than a hit.
func inspectCacheKey(req InspectRequest) string {
	durability := req.DataDurability
	if durability == "" {
		durability = health.DurabilityPersistent
	}

	var b strings.Builder
	b.WriteString(req.ContractID)
	b.WriteByte(0)
	b.WriteString(string(durability))
	for _, key := range req.DataKeys {
		b.WriteByte(0)
		b.WriteString(key)
	}
	return b.String()
}

// get returns a copy of the cached result, or false on a miss or expiry.
func (c *inspectCache) get(key string) (*InspectResult, bool) {
	if c == nil {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return cloneInspectResult(entry.result), true
}

// put stores a copy, making room first if the cache is full.
func (c *inspectCache) put(key string, result *InspectResult) {
	if c == nil || result == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.evictLocked(now)
	c.entries[key] = inspectCacheEntry{
		result:  cloneInspectResult(result),
		expires: now.Add(c.ttl),
	}
}

// evictLocked makes room for one entry: expired entries first, then the one
// closest to expiring. It is called with the lock held.
func (c *inspectCache) evictLocked(now time.Time) {
	if len(c.entries) < c.max {
		return
	}
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) < c.max {
		return
	}

	var oldestKey string
	var oldest time.Time
	for key, entry := range c.entries {
		if oldestKey == "" || entry.expires.Before(oldest) {
			oldestKey, oldest = key, entry.expires
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// cloneInspectResult copies the parts a caller could mutate. InstanceStorage is
// decoded once and treated as immutable, so it is shared rather than deep-copied
// on every hit.
func cloneInspectResult(r *InspectResult) *InspectResult {
	if r == nil {
		return nil
	}
	clone := *r
	clone.Instance = cloneEntryReport(r.Instance)
	clone.Data = make([]EntryReport, len(r.Data))
	for i, entry := range r.Data {
		clone.Data[i] = cloneEntryReport(entry)
	}
	if r.Code != nil {
		code := cloneEntryReport(*r.Code)
		clone.Code = &code
	}
	return &clone
}

// cloneEntryReport copies the embedded TTL's pointer, so a caller mutating a
// returned report cannot change the cached one.
func cloneEntryReport(e EntryReport) EntryReport {
	clone := e
	if e.LiveUntilLedger != nil {
		ledger := *e.LiveUntilLedger
		clone.LiveUntilLedger = &ledger
	}
	return clone
}
