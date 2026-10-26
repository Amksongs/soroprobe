// Package probe orchestrates SoroProbe's three operations — simulate, inspect
// and check — on top of the stellar, scval and health packages, and answers
// readiness questions about the RPC endpoint itself.
//
// Every operation returns a plain result struct. The CLI and the HTTP API both
// render those structs, so the two interfaces cannot drift apart.
package probe

import (
	"errors"
	"log/slog"
	"time"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// Prober runs health and simulation probes against a network.
type Prober struct {
	client             stellar.Client
	codec              scval.Codec
	source             string
	thresholds         health.Thresholds
	thresholdOverrides health.ThresholdOverrides
	log                *slog.Logger
	ledgerTime         *ledgerTimeCache

	abi          abi.Source
	network      string
	baseFee      int64
	inspectCache *inspectCache
}

// Options configures a Prober.
type Options struct {
	// Client is the Stellar RPC client. Required.
	Client stellar.Client
	// Codec encodes arguments and decodes results. Defaults to scval.NewRegistry().
	Codec scval.Codec
	// SourceAccount is the public key used to build transactions for
	// simulation. Required. A secret key is never needed.
	SourceAccount string
	// Thresholds control TTL classification. Defaults to health.DefaultThresholds.
	Thresholds health.Thresholds
	// ThresholdOverrides optionally replace global thresholds by entry kind.
	ThresholdOverrides health.ThresholdOverrides
	// Logger receives debug tracing. Defaults to a discarding logger.
	Logger *slog.Logger

	// ABI, when set, supplies contract interfaces used to type simulate
	// arguments, so a bare "5" is encoded as whatever the function declares
	// rather than an inferred i128. Optional.
	ABI abi.Source
	// Network is the label ABI lookups are filed under ("testnet",
	// "public", ...). Only read when ABI is set.
	Network string
	// InspectCacheTTL enables a bounded cache for Inspect results. Zero, the
	// default, disables it. A few seconds suits dashboards polling the same
	// contracts: TTLs change at most once per ledger.
	InspectCacheTTL time.Duration

	// BaseFee is the base fee per operation in stroops used to estimate total fees.
	// Defaults to DefaultBaseFee (100 stroops).
	BaseFee int64
}

// New builds a Prober.
func New(opts Options) (*Prober, error) {
	if opts.Client == nil {
		return nil, errors.New("probe: a stellar client is required")
	}
	if opts.SourceAccount == "" {
		return nil, errors.New("probe: a source account public key is required")
	}
	if opts.Codec == nil {
		opts.Codec = scval.NewRegistry()
	}
	if opts.Thresholds == (health.Thresholds{}) {
		opts.Thresholds = health.DefaultThresholds
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	var cache *inspectCache
	if opts.InspectCacheTTL > 0 {
		cache = newInspectCache(opts.InspectCacheTTL, inspectCacheMaxEntries)
	}
	baseFee := opts.BaseFee
	if baseFee <= 0 {
		baseFee = DefaultBaseFee
	}
	return &Prober{
		client:             opts.Client,
		codec:              opts.Codec,
		source:             opts.SourceAccount,
		thresholds:         opts.Thresholds,
		thresholdOverrides: opts.ThresholdOverrides,
		log:                opts.Logger,
		ledgerTime:         &ledgerTimeCache{},
		abi:                opts.ABI,
		network:            opts.Network,
		inspectCache:       cache,
		baseFee:            baseFee,
	}, nil
}

// Cost reports the resources a simulated invocation would consume.
//
// Stellar RPC removed the older top-level "cost" object from
// simulateTransaction responses; these figures are decoded from the returned
// SorobanTransactionData instead. Fees are in stroops (1 XLM = 10,000,000).
type Cost struct {
	// Instructions is the CPU instruction count.
	Instructions uint32 `json:"instructions"`
	// DiskReadBytes counts bytes read from disk. Named ReadBytes before
	// protocol 23.
	DiskReadBytes uint32 `json:"disk_read_bytes"`
	// WriteBytes counts bytes written to the ledger.
	WriteBytes uint32 `json:"write_bytes"`
	// ResourceFee is the resource fee in the returned transaction data.
	ResourceFee int64 `json:"resource_fee_stroops"`
	// MinResourceFee is the minimum resource fee RPC recommends adding.
	MinResourceFee int64 `json:"min_resource_fee_stroops"`
	// EstimatedTotalFee is the estimated total fee (resource fee plus base inclusion fee) in stroops.
	EstimatedTotalFee int64 `json:"estimated_total_fee_stroops"`
}

// Footprint lists the ledger entries an invocation would touch.
type Footprint struct {
	ReadOnly  []string `json:"read_only"`
	ReadWrite []string `json:"read_write"`
}

// RestoreInfo is present when simulation found archived entries that must be
// restored before the call can run.
type RestoreInfo struct {
	// MinResourceFee is the fee for the required RestoreFootprint operation.
	MinResourceFee int64 `json:"min_resource_fee_stroops"`
	// ArchivedEntries are the ledger entries the restore operation would restore.
	ArchivedEntries []string `json:"archived_entries,omitempty"`
	// TransactionDataXDR is the base64 SorobanTransactionData for that
	// operation. SoroProbe never submits it; it is reported so an operator can.
	TransactionDataXDR string `json:"transaction_data_xdr,omitempty"`
}

// Event is a decoded diagnostic event emitted during simulation. Events are the
// most useful signal when a call fails.
type Event struct {
	// Type is the contract event type, such as "contract" or "diagnostic".
	Type string `json:"type"`
	// ContractID is the emitting contract, when the event names one.
	ContractID string `json:"contract_id,omitempty"`
	// Topics are the decoded event topics.
	Topics []any `json:"topics,omitempty"`
	// Data is the decoded event payload.
	Data any `json:"data,omitempty"`
	// InSuccessfulContractCall is false for events from a failed sub-call.
	InSuccessfulContractCall bool `json:"in_successful_contract_call"`
}
