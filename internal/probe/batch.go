package probe

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/stellar"
)

// MaxBatchChecks bounds a batch, so one API request cannot queue an
// unbounded amount of RPC work.
const MaxBatchChecks = 100

// ErrInvalidBatch reports a malformed batch: empty, too large, or with
// duplicate or invalid entries. Nothing is checked when it is returned.
var ErrInvalidBatch = errors.New("invalid check batch")

// NamedCheck is one entry in a batch: a CheckRequest with a name that
// identifies it in the results. The name defaults to the contract ID.
type NamedCheck struct {
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Timeout bounds this one check, as a Go duration string ("45s"). Empty
	// means no per-check limit beyond the context the caller passes; this is
	// what keeps one slow contract from consuming a whole batch's budget.
	Timeout      string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	CheckRequest `yaml:",inline"`
}

// BatchRequest is a list of checks to run together.
type BatchRequest struct {
	Checks      []NamedCheck `json:"checks" yaml:"checks"`
	Concurrency int          `json:"-" yaml:"-"`
}

// BatchItem is the outcome of one check in a batch. Exactly one of Result
// and Error is set: Error means the check could not be run at all.
type BatchItem struct {
	Name       string       `json:"name"`
	ContractID string       `json:"contract_id"`
	Result     *CheckResult `json:"result,omitempty"`
	Error      string       `json:"error,omitempty"`
}

// BatchResult aggregates a batch.
type BatchResult struct {
	// OK is true only when every check ran and passed.
	OK bool `json:"ok"`
	// Errored is true when at least one check could not be run. It is kept
	// separate from OK so a pipeline can still tell "a contract is
	// unhealthy" from "the tool could not tell".
	Errored bool        `json:"errored"`
	Items   []BatchItem `json:"items"`
}

// CheckBatch runs every check and reports them in input order. One check
// failing to run does not stop the others; its error is recorded and the batch
// moves on. Only a malformed batch or a canceled context returns a Go error.
//
// A plain batch prefetches every check's ledger entries in as few
// getLedgerEntries calls as the RPC allows and then runs the simulations in
// order. A batch that asks for concurrency, or gives any check its own
// Timeout, runs each check independently instead, so every check can be
// bounded and scheduled on its own.
func (p *Prober) CheckBatch(ctx context.Context, req BatchRequest) (*BatchResult, error) {
	if err := validateBatch(req); err != nil {
		return nil, err
	}
	if req.Concurrency < 0 {
		return nil, fmt.Errorf("%w: concurrency must not be negative", ErrInvalidBatch)
	}
	if req.Concurrency > 1 {
		return p.checkBatchConcurrent(ctx, req)
	}
	for _, check := range req.Checks {
		if check.Timeout != "" {
			return p.checkBatchConcurrent(ctx, req)
		}
	}
	return p.checkBatchPrefetched(ctx, req)
}

// checkBatchConcurrent runs up to req.Concurrency checks at once, each under
// its own Timeout. A check that outlives its Timeout is reported as one that
// could not be run.
func (p *Prober) checkBatchConcurrent(ctx context.Context, req BatchRequest) (*BatchResult, error) {
	concurrency := req.Concurrency
	if concurrency == 0 {
		concurrency = 1
	}
	if concurrency > len(req.Checks) {
		concurrency = len(req.Checks)
	}

	items := make([]BatchItem, len(req.Checks))
	jobs := make(chan int, len(req.Checks))
	for i := range req.Checks {
		jobs <- i
	}
	close(jobs)

	var workers sync.WaitGroup
	workers.Add(concurrency)
	for worker := 0; worker < concurrency; worker++ {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case i, ok := <-jobs:
					if !ok || ctx.Err() != nil {
						return
					}
					c := req.Checks[i]
					timeout, _ := c.checkTimeout()
					checkCtx, cancel := withCheckTimeout(ctx, timeout)
					result, err := runBounded(checkCtx, p, c.CheckRequest)
					cancel()

					item := BatchItem{Name: checkName(c), ContractID: c.ContractID, Result: result}
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						if timeout > 0 && errors.Is(err, context.DeadlineExceeded) {
							item.Error = fmt.Sprintf("check timed out after %s", timeout)
						} else {
							item.Error = err.Error()
						}
						item.Result = nil
					}
					items[i] = item
				}
			}
		}()
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out := &BatchResult{OK: true, Items: items}
	for _, item := range items {
		if item.Error != "" {
			out.Errored = true
			out.OK = false
		} else if !item.Result.OK {
			out.OK = false
		}
	}
	return out, nil
}

// Stellar RPC documents a maximum of 200 keys per getLedgerEntries request.
const maxLedgerKeysPerRequest = 200

type batchCheckState struct {
	plan    *inspectPlan
	codeKey string
	err     error
}

type batchLedgerEntries struct {
	entries map[string]protocol.LedgerEntryResult
	latest  map[string]uint32
	errors  map[string]error
}

func (p *Prober) checkBatchPrefetched(ctx context.Context, req BatchRequest) (*BatchResult, error) {
	states := make([]batchCheckState, len(req.Checks))
	initialKeys := make([]string, 0)
	seen := make(map[string]struct{})
	for i, check := range req.Checks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		plan, err := p.planInspect(InspectRequest{
			ContractID:     check.ContractID,
			DataKeys:       check.DataKeys,
			DataDurability: check.DataDurability,
		})
		if err != nil {
			states[i].err = err
			continue
		}
		states[i].plan = plan
		for _, key := range plan.keys {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				initialKeys = append(initialKeys, key)
			}
		}
	}

	initial := p.fetchBatchEntries(ctx, initialKeys)
	codeKeys := make([]string, 0, len(req.Checks))
	seen = make(map[string]struct{})
	for i := range states {
		state := &states[i]
		if state.err != nil || state.plan == nil {
			continue
		}
		for _, key := range state.plan.keys {
			if err := initial.errors[key]; err != nil {
				state.err = err
				break
			}
		}
		if state.err != nil {
			continue
		}
		instance, ok := initial.entries[state.plan.instanceKey]
		if !ok {
			continue
		}
		partial := &InspectResult{}
		wasmHash, err := p.readInstanceEntry(instance, partial)
		if err != nil {
			state.err = err
			continue
		}
		if wasmHash == nil {
			continue
		}
		state.codeKey, err = stellar.EncodeKey(stellar.CodeKey(*wasmHash))
		if err != nil {
			state.err = err
			continue
		}
		if _, ok := seen[state.codeKey]; !ok {
			seen[state.codeKey] = struct{}{}
			codeKeys = append(codeKeys, state.codeKey)
		}
	}

	code := p.fetchBatchEntries(ctx, codeKeys)
	entries := make(map[string]protocol.LedgerEntryResult, len(initial.entries)+len(code.entries))
	latest := make(map[string]uint32, len(initial.latest)+len(code.latest))
	keyErrors := make(map[string]error, len(initial.errors)+len(code.errors))
	for key, entry := range initial.entries {
		entries[key] = entry
	}
	for key, ledger := range initial.latest {
		latest[key] = ledger
	}
	for key, err := range initial.errors {
		keyErrors[key] = err
	}
	for key, entry := range code.entries {
		entries[key] = entry
	}
	for key, ledger := range code.latest {
		latest[key] = ledger
	}
	for key, err := range code.errors {
		keyErrors[key] = err
	}
	client := &cachedLedgerClient{Client: p.client, entries: entries, latest: latest, errors: keyErrors}
	batchedProber := *p
	batchedProber.client = client

	out := &BatchResult{OK: true, Items: make([]BatchItem, 0, len(req.Checks))}
	for i, check := range req.Checks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		item := BatchItem{Name: checkName(check), ContractID: check.ContractID}
		if states[i].err != nil {
			item.Error = states[i].err.Error()
			out.Errored = true
			out.OK = false
			out.Items = append(out.Items, item)
			continue
		}
		if states[i].codeKey != "" {
			if err := keyErrors[states[i].codeKey]; err != nil {
				item.Error = err.Error()
				out.Errored = true
				out.OK = false
				out.Items = append(out.Items, item)
				continue
			}
		}
		result, err := batchedProber.Check(ctx, check.CheckRequest)
		if err != nil {
			item.Error = err.Error()
			out.Errored = true
			out.OK = false
		} else {
			item.Result = result
			if !result.OK {
				out.OK = false
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (p *Prober) fetchBatchEntries(ctx context.Context, keys []string) batchLedgerEntries {
	result := batchLedgerEntries{
		entries: make(map[string]protocol.LedgerEntryResult, len(keys)),
		latest:  make(map[string]uint32, len(keys)),
		errors:  make(map[string]error),
	}
	for start := 0; start < len(keys); start += maxLedgerKeysPerRequest {
		if ctx.Err() != nil {
			for _, key := range keys[start:] {
				result.errors[key] = ctx.Err()
			}
			return result
		}
		end := min(start+maxLedgerKeysPerRequest, len(keys))
		chunk := keys[start:end]
		response, err := p.client.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: chunk})
		if err != nil {
			for _, key := range chunk {
				result.errors[key] = err
			}
			continue
		}
		for _, key := range chunk {
			result.latest[key] = response.LatestLedger
		}
		for _, entry := range response.Entries {
			result.entries[entry.KeyXDR] = entry
		}
	}
	return result
}

type cachedLedgerClient struct {
	stellar.Client
	entries map[string]protocol.LedgerEntryResult
	latest  map[string]uint32
	errors  map[string]error
}

func (c *cachedLedgerClient) GetLedgerEntries(_ context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	response := protocol.GetLedgerEntriesResponse{}
	for _, key := range req.Keys {
		if err := c.errors[key]; err != nil {
			return response, err
		}
		ledger, ok := c.latest[key]
		if !ok {
			return response, fmt.Errorf("batch ledger entry %q was not prefetched", key)
		}
		if response.LatestLedger == 0 {
			response.LatestLedger = ledger
		}
		if entry, ok := c.entries[key]; ok {
			response.Entries = append(response.Entries, entry)
		}
	}
	return response, nil
}

// checkTimeout parses the per-check time limit. Zero means no limit.
func (c NamedCheck) checkTimeout() (time.Duration, error) {
	if c.Timeout == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 0, fmt.Errorf("%w: check %q: invalid timeout %q: %w", ErrInvalidBatch, checkName(c), c.Timeout, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: check %q: timeout must not be negative, got %s", ErrInvalidBatch, checkName(c), d)
	}
	return d, nil
}

// withCheckTimeout bounds one check with its own deadline. A zero limit leaves
// the parent context as the only bound, so a shorter parent deadline still
// wins: context.WithTimeout keeps whichever comes first.
func withCheckTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// runBounded runs one check and reports its result as soon as ctx is done.
//
// The deadline is enforced here rather than by the RPC layer, and that is
// deliberate: the Stellar SDK's JSON-RPC channel posts with an http.Request
// carrying no context, so an in-flight request is only ever bounded by the
// per-request HTTP timeout, however tight the caller's context is. Selecting
// on ctx means a hung check costs its own budget and no more; the abandoned
// call still ends on the transport's own timeout, and nothing it later
// returns is read.
//
// ctx must carry a deadline, as the per-check deadline always does when there
// is a limit; with none, this is a plain call.
func runBounded(ctx context.Context, p *Prober, req CheckRequest) (*CheckResult, error) {
	type outcome struct {
		result *CheckResult
		err    error
	}

	done := make(chan outcome, 1) // buffered: the runner must not leak when its result is abandoned
	go func() {
		result, err := p.Check(ctx, req)
		done <- outcome{result, err}
	}()

	select {
	case o := <-done:
		return o.result, o.err
	case <-ctx.Done():
		select {
		case o := <-done: // finished as the deadline passed; the answer is still the answer
			return o.result, o.err
		default:
		}
		return nil, ctx.Err()
	}
}

func checkName(c NamedCheck) string {
	if c.Name != "" {
		return c.Name
	}
	return c.ContractID
}

func validateBatch(req BatchRequest) error {
	switch {
	case len(req.Checks) == 0:
		return fmt.Errorf("%w: no checks given", ErrInvalidBatch)
	case len(req.Checks) > MaxBatchChecks:
		return fmt.Errorf("%w: %d checks exceeds the limit of %d", ErrInvalidBatch, len(req.Checks), MaxBatchChecks)
	}

	seen := make(map[string]int, len(req.Checks))
	for i, c := range req.Checks {
		if c.ContractID == "" {
			return fmt.Errorf("%w: check %d has no contract_id", ErrInvalidBatch, i)
		}
		if _, err := c.checkTimeout(); err != nil {
			return err
		}
		switch c.DataDurability {
		case "", health.DurabilityPersistent, health.DurabilityTemporary:
		default:
			return fmt.Errorf("%w: check %q: unknown data_durability %q (want persistent or temporary)",
				ErrInvalidBatch, checkName(c), c.DataDurability)
		}
		name := checkName(c)
		if prev, dup := seen[name]; dup {
			return fmt.Errorf("%w: checks %d and %d are both named %q; names must be unique",
				ErrInvalidBatch, prev, i, name)
		}
		seen[name] = i
	}
	return nil
}
