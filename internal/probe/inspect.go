package probe

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/scval"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// InspectRequest describes a state-health read.
type InspectRequest struct {
	// ContractID is the strkey contract address ("C...").
	ContractID string `json:"contract_id"`
	// DataKeys are optional argument specifications, in the scval package's
	// "type:value" form, naming contract data entries to inspect alongside the
	// instance and code.
	//
	// Stellar RPC reads ledger entries by key and offers no way to enumerate a
	// contract's data entries, so callers that want specific entries checked
	// must name them. Enumerating all of them requires an indexer, which is out
	// of scope for a stateless tool.
	DataKeys []string `json:"data_keys,omitempty"`
	// DataDurability selects which durability the DataKeys are read at.
	// Defaults to persistent.
	DataDurability health.Durability `json:"data_durability,omitempty"`
	// EstimateExtend, when set, asks for the fee of extending each reported
	// entry's TTL by that many ledgers. The extension is simulated, never
	// submitted.
	EstimateExtend uint32 `json:"estimate_extend,omitempty"`
}

// ExtendEstimate is the cost of extending one entry's TTL.
type ExtendEstimate struct {
	// Kind is "instance", "code" or "data".
	Kind string `json:"kind"`
	// Key describes which entry this is, for data entries.
	Key string `json:"key,omitempty"`
	// Ledgers is the number of ledgers the extension covers.
	Ledgers uint32 `json:"ledgers"`
	// ExtendTo is the ledger the entry's TTL would be set to.
	ExtendTo uint32 `json:"extend_to"`
	// ResourceFee is the fee for the extension, in stroops.
	ResourceFee int64 `json:"resource_fee_stroops"`
	// MinResourceFee is the minimum fee the RPC recommends.
	MinResourceFee int64 `json:"min_resource_fee_stroops"`
	// Note explains why no estimate was produced.
	Note string `json:"note,omitempty"`
}

// EntryReport is the health of a single ledger entry.
type EntryReport struct {
	// Kind is "instance", "code" or "data".
	Kind string `json:"kind"`
	// Key describes which entry this is, for data entries.
	Key string `json:"key,omitempty"`
	// TTL is the interpreted expiration state.
	health.TTL
	// Summary is a human-readable one-liner.
	Summary string `json:"summary"`
	// Thresholds are the warning and critical thresholds used for this entry.
	Thresholds health.Thresholds `json:"thresholds"`
}

// InspectResult reports a contract's on-ledger state health.
type InspectResult struct {
	ContractID string `json:"contract_id"`
	// Deployed is true when the contract's instance entry exists.
	Deployed bool `json:"deployed"`
	// Executable is "wasm" or "stellar_asset".
	Executable string `json:"executable,omitempty"`
	// WasmHash is the hex code hash, for Wasm contracts.
	WasmHash string `json:"wasm_hash,omitempty"`

	// Instance is the contract instance entry's health.
	Instance EntryReport `json:"instance"`
	// Code is the Wasm code entry's health. Nil for Stellar Asset Contracts,
	// which are built into the host and have no code entry.
	Code *EntryReport `json:"code,omitempty"`
	// Data holds any explicitly requested data entries.
	Data []EntryReport `json:"data,omitempty"`

	// InstanceStorage is the decoded instance storage map. These values live
	// and die with the instance entry: they share its single TTL.
	InstanceStorage any `json:"instance_storage,omitempty"`

	// ExtendEstimates holds the cost of extending each reported entry's TTL,
	// when an extension was asked for.
	ExtendEstimates []ExtendEstimate `json:"extend_estimates,omitempty"`

	// Status is the worst status across all inspected entries.
	Status health.Status `json:"status"`
	// LatestLedger is the ledger the assessment was made against.
	LatestLedger uint32 `json:"latest_ledger"`
	// CloseTimeNote explains the measured duration estimate or its fallback.
	CloseTimeNote string `json:"close_time_note"`
}

type inspectPlan struct {
	contract    xdr.ScAddress
	instanceKey string
	dataKeys    []encodedDataKey
	keys        []string
	durability  health.Durability
}

// Inspect reads a contract's instance, code and any named data entries, and
// reports how close each is to expiration.
//
// When the Prober has an inspect cache, a repeated request within its window is
// served from it. A cached report is still honest: LatestLedger records the
// ledger it was computed at, and simulations are never cached.
func (p *Prober) Inspect(ctx context.Context, req InspectRequest) (*InspectResult, error) {
	if p.inspectCache == nil {
		return p.inspectUncached(ctx, req)
	}

	key := inspectCacheKey(req)
	if result, ok := p.inspectCache.get(key); ok {
		return result, nil
	}

	result, err := p.inspectUncached(ctx, req)
	if err != nil {
		return nil, err
	}
	p.inspectCache.put(key, result)
	return result, nil
}

// inspectUncached is Inspect without the cache.
func (p *Prober) inspectUncached(ctx context.Context, req InspectRequest) (*InspectResult, error) {
	plan, err := p.planInspect(req)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: plan.keys})
	if err != nil {
		return nil, err
	}
	closeTime, closeTimeNote, err := p.ledgerCloseTime(ctx)
	if err != nil {
		return nil, err
	}
	found := indexEntries(resp.Entries)

	result := &InspectResult{
		ContractID:    req.ContractID,
		LatestLedger:  resp.LatestLedger,
		CloseTimeNote: closeTimeNote,
	}

	instance, ok := found[plan.instanceKey]
	if !ok {
		thresholds := p.thresholdsFor(health.EntryKindInstance)
		result.Instance = EntryReport{
			Kind:       "instance",
			TTL:        health.Absent(resp.LatestLedger),
			Summary:    entrySummary("contract is not deployed on this network", thresholds),
			Thresholds: thresholds,
		}
		result.Status = health.StatusMissing
		return result, nil
	}

	result.Deployed = true
	instanceThresholds := p.thresholdsFor(health.EntryKindInstance)
	result.Instance = EntryReport{
		Kind:       "instance",
		TTL:        health.EvaluateWithCloseTime(instance.LiveUntilLedgerSeq, resp.LatestLedger, health.DurabilityPersistent, instanceThresholds, closeTime),
		Thresholds: instanceThresholds,
	}
	result.Instance.Summary = entrySummary(result.Instance.Describe(), instanceThresholds)

	wasmHash, err := p.readInstanceEntry(instance, result)
	if err != nil {
		return nil, err
	}

	dataThresholds := p.thresholdsFor(health.EntryKindData)
	for _, dk := range plan.dataKeys {
		report := EntryReport{Kind: "data", Key: dk.spec, Thresholds: dataThresholds}
		if entry, ok := found[dk.encoded]; ok {
			report.TTL = health.EvaluateWithCloseTime(entry.LiveUntilLedgerSeq, resp.LatestLedger, plan.durability, dataThresholds, closeTime)
		} else {
			report.TTL = health.Absent(resp.LatestLedger)
			report.Durability = plan.durability
		}
		report.Summary = entrySummary(report.Describe(), dataThresholds)
		result.Data = append(result.Data, report)
	}

	// Round two: the code entry, now that the Wasm hash is known.
	if wasmHash != nil {
		codeReport, err := p.inspectCode(ctx, *wasmHash, closeTime)
		if err != nil {
			return nil, err
		}
		result.Code = codeReport
	}

	// The cost of keeping each entry alive, when it was asked for. It comes
	// last so that a contract that is not deployed is reported as such
	// rather than as a failed estimate.
	if req.EstimateExtend > 0 {
		estimates, err := p.estimateExtends(ctx, plan.contract, wasmHash, plan.dataKeys, result, req.EstimateExtend)
		if err != nil {
			return nil, err
		}
		result.ExtendEstimates = estimates
	}

	result.Status = result.worstStatus()
	return result, nil
}

// extendTarget is one entry that can be extended, with the ledger key that
// addresses it.
type extendTarget struct {
	// kind is "instance", "code" or "data".
	kind string
	// spec is the key spec, for data entries.
	spec string
	// key addresses the entry on the ledger.
	key xdr.LedgerKey
	// ttl is the entry's expiration state.
	ttl health.TTL
}

// estimateExtends prices the extension of every reported entry.
//
// Knowing an entry expires in two days is half the answer; the other half is
// what keeping it alive costs. Each entry is priced on its own, with a
// footprint holding that entry alone, so the fee is the fee for the entry the
// operator is looking at.
func (p *Prober) estimateExtends(ctx context.Context, contract xdr.ScAddress, wasmHash *xdr.Hash, dataKeys []encodedDataKey, result *InspectResult, ledgers uint32) ([]ExtendEstimate, error) {
	targets := []extendTarget{
		{kind: "instance", key: stellar.InstanceKey(contract), ttl: result.Instance.TTL},
	}
	if wasmHash != nil && result.Code != nil {
		targets = append(targets, extendTarget{kind: "code", key: stellar.CodeKey(*wasmHash), ttl: result.Code.TTL})
	}
	for i, dk := range dataKeys {
		if i >= len(result.Data) {
			break
		}
		targets = append(targets, extendTarget{
			kind: "data",
			spec: dk.spec,
			key:  dk.key,
			ttl:  result.Data[i].TTL,
		})
	}

	out := make([]ExtendEstimate, 0, len(targets))
	for _, t := range targets {
		estimate := ExtendEstimate{
			Kind:     t.kind,
			Key:      t.spec,
			Ledgers:  ledgers,
			ExtendTo: result.LatestLedger + ledgers,
		}

		switch {
		case !t.ttl.Present:
			estimate.Note = "the entry is not on the ledger, so there is nothing to extend"
		case t.ttl.Status == health.StatusExpired:
			estimate.Note = "the entry has expired; it must be restored before it can be extended"
		case t.ttl.LiveUntilLedger == nil:
			estimate.Note = "the entry reported no TTL, so there is nothing to extend"
		default:
			fee, minFee, err := p.estimateExtendFee(ctx, t.key, estimate.ExtendTo)
			if err != nil {
				return nil, err
			}
			estimate.ResourceFee = fee
			estimate.MinResourceFee = minFee
		}

		out = append(out, estimate)
	}
	return out, nil
}

// estimateExtendFee simulates the extension of one entry and reports what it
// would cost.
//
// Nothing is submitted: the transaction is only ever simulated, exactly like
// the invocation SoroProbe builds for a simulation.
func (p *Prober) estimateExtendFee(ctx context.Context, key xdr.LedgerKey, extendTo uint32) (int64, int64, error) {
	envelope, err := stellar.BuildExtendFootprintTx(stellar.ExtendParams{
		SourceAccount: p.source,
		Key:           key,
		ExtendTo:      extendTo,
	})
	if err != nil {
		return 0, 0, err
	}

	resp, err := p.client.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{
		Transaction: envelope,
	})
	if err != nil {
		return 0, 0, err
	}
	if resp.Error != "" {
		return 0, 0, fmt.Errorf("the extension simulation failed: %s", resp.Error)
	}
	if resp.TransactionDataXDR == "" {
		return 0, 0, fmt.Errorf("the extension simulation returned no transaction data")
	}

	var data xdr.SorobanTransactionData
	if err := xdr.SafeUnmarshalBase64(resp.TransactionDataXDR, &data); err != nil {
		return 0, 0, fmt.Errorf("decode transaction data: %w", err)
	}
	return int64(data.ResourceFee), resp.MinResourceFee, nil
}

// readInstanceEntry decodes the instance entry, filling in the executable,
// Wasm hash and instance storage. It returns the Wasm hash when there is one.
func (p *Prober) readInstanceEntry(entry protocol.LedgerEntryResult, result *InspectResult) (*xdr.Hash, error) {
	data, err := stellar.DecodeEntryData(entry.DataXDR)
	if err != nil {
		return nil, err
	}
	if data.ContractData == nil {
		return nil, fmt.Errorf("instance entry is not contract data (got %s)", data.Type)
	}

	instance, ok := data.ContractData.Val.GetInstance()
	if !ok {
		return nil, fmt.Errorf("instance entry does not hold a contract instance")
	}

	var wasmHash *xdr.Hash
	switch instance.Executable.Type {
	case xdr.ContractExecutableTypeContractExecutableWasm:
		result.Executable = "wasm"
		if instance.Executable.WasmHash != nil {
			result.WasmHash = hex.EncodeToString(instance.Executable.WasmHash[:])
			hash := *instance.Executable.WasmHash
			wasmHash = &hash
		}
	case xdr.ContractExecutableTypeContractExecutableStellarAsset:
		// Stellar Asset Contracts are implemented by the host itself, so there
		// is no Wasm code entry to look up or to expire.
		result.Executable = "stellar_asset"
	default:
		result.Executable = instance.Executable.Type.String()
	}

	if instance.Storage != nil {
		storage, err := p.decodeStorage(instance.Storage)
		if err != nil {
			result.InstanceStorage = map[string]any{"error": err.Error()}
		} else {
			result.InstanceStorage = storage
		}
	}
	return wasmHash, nil
}

func (p *Prober) decodeStorage(storage *xdr.ScMap) (any, error) {
	decoded, err := p.codec.Decode(xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &storage})
	if err != nil {
		return nil, fmt.Errorf("decode instance storage: %w", err)
	}
	return decoded, nil
}

func (p *Prober) planInspect(req InspectRequest) (*inspectPlan, error) {
	contract, err := stellar.ParseContractID(req.ContractID)
	if err != nil {
		return nil, err
	}
	durability := req.DataDurability
	if durability == "" {
		durability = health.DurabilityPersistent
	}
	instanceKey, err := stellar.EncodeKey(stellar.InstanceKey(contract))
	if err != nil {
		return nil, err
	}
	dataKeys, err := p.encodeDataKeys(contract, req.DataKeys, durability)
	if err != nil {
		return nil, err
	}
	keys := []string{instanceKey}
	for _, key := range dataKeys {
		keys = append(keys, key.encoded)
	}
	return &inspectPlan{contract: contract, instanceKey: instanceKey, dataKeys: dataKeys, keys: keys, durability: durability}, nil
}

func (p *Prober) inspectCode(ctx context.Context, hash xdr.Hash, closeTime time.Duration) (*EntryReport, error) {
	key, err := stellar.EncodeKey(stellar.CodeKey(hash))
	if err != nil {
		return nil, err
	}

	resp, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: []string{key}})
	if err != nil {
		return nil, err
	}

	thresholds := p.thresholdsFor(health.EntryKindCode)
	report := &EntryReport{Kind: "code", Thresholds: thresholds}
	if len(resp.Entries) == 0 {
		report.TTL = health.Absent(resp.LatestLedger)
		report.Summary = entrySummary("wasm code entry not found; it may have been archived", thresholds)
		return report, nil
	}

	report.TTL = health.EvaluateWithCloseTime(resp.Entries[0].LiveUntilLedgerSeq, resp.LatestLedger, health.DurabilityPersistent, thresholds, closeTime)
	report.Summary = entrySummary(report.Describe(), thresholds)
	return report, nil
}

func (p *Prober) thresholdsFor(kind health.EntryKind) health.Thresholds {
	return health.ResolveThresholds(kind, p.thresholds, p.thresholdOverrides)
}

func entrySummary(summary string, thresholds health.Thresholds) string {
	return fmt.Sprintf("%s (thresholds: warn <= %d, critical <= %d ledgers)", summary, thresholds.Warn, thresholds.Critical)
}

type encodedDataKey struct {
	spec    string
	encoded string
	// key is the ledger key the spec addresses, kept so that the entry can
	// be put in a footprint without being decoded again.
	key xdr.LedgerKey
}

func (p *Prober) encodeDataKeys(contract xdr.ScAddress, specs []string, durability health.Durability) ([]encodedDataKey, error) {
	out := make([]encodedDataKey, 0, len(specs))
	for _, spec := range specs {
		val, err := p.codec.Encode(spec)
		if err != nil {
			return nil, err
		}
		xdrDurability := xdr.ContractDataDurabilityPersistent
		if durability == health.DurabilityTemporary {
			xdrDurability = xdr.ContractDataDurabilityTemporary
		}
		key := stellar.DataKey(contract, val, xdrDurability)
		encoded, err := stellar.EncodeKey(key)
		if err != nil {
			return nil, err
		}
		out = append(out, encodedDataKey{spec: spec, encoded: encoded, key: key})
	}
	return out, nil
}

// indexEntries maps results back to the keys that were requested. RPC omits
// entries that do not exist, so absence from this map means "not on ledger".
func indexEntries(entries []protocol.LedgerEntryResult) map[string]protocol.LedgerEntryResult {
	out := make(map[string]protocol.LedgerEntryResult, len(entries))
	for _, e := range entries {
		out[e.KeyXDR] = e
	}
	return out
}

func (r *InspectResult) worstStatus() health.Status {
	statuses := []health.Status{r.Instance.Status}
	if r.Code != nil {
		statuses = append(statuses, r.Code.Status)
	}
	for _, d := range r.Data {
		statuses = append(statuses, d.Status)
	}
	return health.Worst(statuses...)
}

// ensure the codec interface is what inspect relies on
var _ scval.Codec = (*scval.Registry)(nil)
