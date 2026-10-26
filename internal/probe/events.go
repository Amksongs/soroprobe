package probe

import (
	"context"
	"fmt"
	"sort"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/stellar"
)

// EventItem represents a single contract event with decoded topics and data.
type EventItem struct {
	Ledger         int32    `json:"ledger"`
	LedgerClosedAt string   `json:"ledger_closed_at"`
	ContractID     string   `json:"contract_id"`
	ID             string   `json:"id"`
	TxHash         string   `json:"tx_hash"`
	Topics         []any    `json:"topics"`
	Data           any      `json:"data"`
	TopicXDR       []string `json:"topic_xdr,omitempty"`
	ValueXDR       string   `json:"value_xdr,omitempty"`
}

// EventsResult is the output of Events().
type EventsResult struct {
	ContractID            string      `json:"contract_id"`
	Events                []EventItem `json:"events"`
	Cursor                string      `json:"cursor,omitempty"`
	LatestLedger          uint32      `json:"latest_ledger"`
	OldestLedger          uint32      `json:"oldest_ledger"`
	LatestLedgerCloseTime int64       `json:"latest_ledger_close_time,omitempty"`
	OldestLedgerCloseTime int64       `json:"oldest_ledger_close_time,omitempty"`
	RetentionWindow       uint32      `json:"retention_window,omitempty"`
}

// EventsOptions configures the Events query.
type EventsOptions struct {
	Topic       string
	Limit       uint
	StartLedger uint32
	Cursor      string
}

// Events retrieves recent events for a contract, newest first, with topics and value decoded via Codec.
func (p *Prober) Events(ctx context.Context, contractID string, opts EventsOptions) (*EventsResult, error) {
	if _, err := stellar.ParseContractID(contractID); err != nil {
		return nil, fmt.Errorf("invalid contract id: %w", err)
	}

	filter := protocol.EventFilter{
		EventType:   protocol.EventTypeSet{protocol.EventTypeContract: struct{}{}},
		ContractIDs: []string{contractID},
	}

	if opts.Topic != "" {
		topicVal, err := p.codec.Encode(opts.Topic)
		if err != nil {
			return nil, fmt.Errorf("invalid topic filter %q: %w", opts.Topic, err)
		}
		filter.Topics = []protocol.TopicFilter{
			{
				{ScVal: &topicVal},
				{Wildcard: stringPtr("**")},
			},
		}
	}

	req := protocol.GetEventsRequest{
		Filters: []protocol.EventFilter{filter},
	}

	if opts.Cursor != "" {
		cur, err := protocol.ParseCursor(opts.Cursor)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor %q: %w", opts.Cursor, err)
		}
		req.Pagination = &protocol.PaginationOptions{
			Cursor: &cur,
		}
	} else if opts.StartLedger > 0 {
		req.StartLedger = opts.StartLedger
	} else {
		// Determine start ledger based on RPC health or latest ledger retention window
		healthResp, err := p.client.GetHealth(ctx)
		if err == nil && healthResp.OldestLedger > 0 {
			req.StartLedger = healthResp.OldestLedger
		} else {
			latest, err := p.client.GetLatestLedger(ctx)
			if err != nil {
				return nil, fmt.Errorf("getLatestLedger: %w", err)
			}
			// Default window: last 1000 ledgers or ledger 1
			if latest.Sequence > 1000 {
				req.StartLedger = latest.Sequence - 1000
			} else {
				req.StartLedger = 1
			}
		}
	}

	if opts.Limit > 0 {
		if req.Pagination == nil {
			req.Pagination = &protocol.PaginationOptions{}
		}
		req.Pagination.Limit = opts.Limit
	}

	resp, err := p.client.GetEvents(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("getEvents: %w", err)
	}

	result := &EventsResult{
		ContractID:            contractID,
		Cursor:                resp.Cursor,
		LatestLedger:          resp.LatestLedger,
		OldestLedger:          resp.OldestLedger,
		LatestLedgerCloseTime: resp.LatestLedgerCloseTime,
		OldestLedgerCloseTime: resp.OldestLedgerCloseTime,
		Events:                make([]EventItem, 0, len(resp.Events)),
	}
	if resp.LatestLedger >= resp.OldestLedger && resp.OldestLedger > 0 {
		result.RetentionWindow = resp.LatestLedger - resp.OldestLedger
	}

	for _, ev := range resp.Events {
		item := EventItem{
			Ledger:         ev.Ledger,
			LedgerClosedAt: ev.LedgerClosedAt,
			ContractID:     ev.ContractID,
			ID:             ev.ID,
			TxHash:         ev.TransactionHash,
			TopicXDR:       ev.TopicXDR,
			ValueXDR:       ev.ValueXDR,
			Topics:         make([]any, 0, len(ev.TopicXDR)),
		}

		for _, topB64 := range ev.TopicXDR {
			var scv xdr.ScVal
			if err := xdr.SafeUnmarshalBase64(topB64, &scv); err != nil {
				item.Topics = append(item.Topics, topB64)
			} else {
				decoded, err := p.codec.Decode(scv)
				if err != nil {
					item.Topics = append(item.Topics, topB64)
				} else {
					item.Topics = append(item.Topics, decoded)
				}
			}
		}

		if ev.ValueXDR != "" {
			var scv xdr.ScVal
			if err := xdr.SafeUnmarshalBase64(ev.ValueXDR, &scv); err != nil {
				item.Data = ev.ValueXDR
			} else {
				decoded, err := p.codec.Decode(scv)
				if err != nil {
					item.Data = ev.ValueXDR
				} else {
					item.Data = decoded
				}
			}
		}

		result.Events = append(result.Events, item)
	}

	// Newest first: sort by Ledger descending, then ID descending
	sort.SliceStable(result.Events, func(i, j int) bool {
		if result.Events[i].Ledger != result.Events[j].Ledger {
			return result.Events[i].Ledger > result.Events[j].Ledger
		}
		return result.Events[i].ID > result.Events[j].ID
	})

	return result, nil
}

func stringPtr(s string) *string {
	return &s
}
