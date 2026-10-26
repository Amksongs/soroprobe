package probe

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/soroworks/soroprobe/internal/health"
)

const (
	ledgerTimeWindow   = 20
	ledgerTimeCacheTTL = 5 * time.Minute
)

type ledgerTimeCache struct {
	mu         sync.Mutex
	duration   time.Duration
	measuredAt time.Time
}

func (p *Prober) ledgerCloseTime(ctx context.Context) (time.Duration, string, error) {
	fallback := func() (time.Duration, string, error) {
		return health.LedgerCloseTime, "recent ledger close time unavailable; duration estimates use the 5-second target", nil
	}
	if p.ledgerTime == nil {
		return fallback()
	}

	cache := p.ledgerTime
	cache.mu.Lock()
	if cache.duration > 0 && time.Since(cache.measuredAt) < ledgerTimeCacheTTL {
		duration := cache.duration
		cache.mu.Unlock()
		return duration, fmt.Sprintf("duration estimates use measured average ledger close time of %s", duration), nil
	}
	cache.mu.Unlock()

	latest, err := p.client.GetLatestLedger(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		p.log.Debug("could not get latest ledger for close-time estimate", "err", err)
		return fallback()
	}
	start := uint32(1)
	if latest.Sequence >= ledgerTimeWindow {
		start = latest.Sequence - (ledgerTimeWindow - 1)
	}
	resp, err := p.client.GetLedgers(ctx, protocol.GetLedgersRequest{
		StartLedger: start,
		Pagination:  &protocol.LedgerPaginationOptions{Limit: ledgerTimeWindow},
	})
	if err != nil {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		p.log.Debug("could not get recent ledgers for close-time estimate", "err", err)
		return fallback()
	}
	if len(resp.Ledgers) < 2 {
		return fallback()
	}
	ledgers := append([]protocol.LedgerInfo(nil), resp.Ledgers...)
	sort.Slice(ledgers, func(i, j int) bool { return ledgers[i].Sequence < ledgers[j].Sequence })
	first, last := ledgers[0], ledgers[len(ledgers)-1]
	sequenceDelta := int64(last.Sequence) - int64(first.Sequence)
	closeTimeDelta := last.LedgerCloseTime - first.LedgerCloseTime
	if sequenceDelta <= 0 || closeTimeDelta <= 0 {
		return fallback()
	}
	measured := time.Duration(closeTimeDelta) * time.Second / time.Duration(sequenceDelta)
	if measured <= 0 {
		return fallback()
	}
	cache.mu.Lock()
	cache.duration = measured
	cache.measuredAt = time.Now()
	cache.mu.Unlock()
	return measured, fmt.Sprintf("duration estimates use measured average ledger close time of %s", measured), nil
}
