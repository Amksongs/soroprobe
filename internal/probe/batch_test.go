package probe_test

import (
	"context"
	"sync"
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func named(name, contract, fn string) probe.NamedCheck {
	return probe.NamedCheck{Name: name, CheckRequest: probe.CheckRequest{ContractID: contract, Function: fn}}
}

func TestCheckBatchAllPass(t *testing.T) {
	t.Parallel()
	p := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		named("native", stellartest.SACContract, "decimals"),
		named("", stellartest.WasmContract, ""),
	}})
	require.NoError(t, err)

	assert.True(t, result.OK)
	assert.False(t, result.Errored)
	require.Len(t, result.Items, 2)
	assert.Equal(t, "native", result.Items[0].Name)
	assert.Equal(t, stellartest.WasmContract, result.Items[1].Name, "the name defaults to the contract ID")
	assert.True(t, result.Items[0].Result.OK)
}

func TestCheckBatchWithCostBudget(t *testing.T) {
	t.Parallel()
	p := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	maxInstPass := uint32(200000)
	maxInstFail := uint32(50000)

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		{
			Name: "passes_budget",
			CheckRequest: probe.CheckRequest{
				ContractID:      stellartest.SACContract,
				Function:        "decimals",
				MaxInstructions: &maxInstPass,
			},
		},
		{
			Name: "fails_budget",
			CheckRequest: probe.CheckRequest{
				ContractID:      stellartest.SACContract,
				Function:        "decimals",
				MaxInstructions: &maxInstFail,
			},
		},
	}})
	require.NoError(t, err)

	assert.False(t, result.OK)
	require.Len(t, result.Items, 2)
	assert.True(t, result.Items[0].Result.OK)
	assert.False(t, result.Items[1].Result.OK)
}

func TestCheckBatchConcurrentKeepsInputOrder(t *testing.T) {
	t.Parallel()
	p := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)
	checks := []probe.NamedCheck{
		named("first", stellartest.WasmContract, ""),
		named("second", stellartest.SACContract, "decimals"),
		named("third", stellartest.WasmContract, "decimals"),
	}

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: checks, Concurrency: 3})
	require.NoError(t, err)
	require.Len(t, result.Items, len(checks))
	for i, item := range result.Items {
		assert.Equal(t, checks[i].Name, item.Name)
		assert.NotNil(t, item.Result)
	}
}

func TestCheckBatchUnhealthyIsNotAnError(t *testing.T) {
	t.Parallel()
	p := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		named("ok", stellartest.SACContract, "decimals"),
		named("gone", stellartest.UndeployedContract, ""),
	}})
	require.NoError(t, err)

	assert.False(t, result.OK)
	assert.False(t, result.Errored, "an unhealthy contract is an answer, not a tool failure")
	assert.False(t, result.Items[1].Result.OK)
	assert.Empty(t, result.Items[1].Error)
}

func TestCheckBatchContinuesPastAnErroredCheck(t *testing.T) {
	t.Parallel()
	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		named("bad", "not-a-contract", ""),
		named("good", stellartest.SACContract, "decimals"),
	}})
	require.NoError(t, err)

	assert.False(t, result.OK)
	assert.True(t, result.Errored)
	assert.NotEmpty(t, result.Items[0].Error)
	assert.NotContains(t, result.Items[0].Error, "timed out", "an ordinary failure is not a timeout")
	assert.Nil(t, result.Items[0].Result)
	require.NotNil(t, result.Items[1].Result, "the next check still runs")
	assert.True(t, result.Items[1].Result.OK)
}

func TestCheckBatchRejectsMalformedBatches(t *testing.T) {
	t.Parallel()
	p := newProber(t, stellartest.NewFake(t), health.DefaultThresholds)

	tooMany := make([]probe.NamedCheck, probe.MaxBatchChecks+1)
	for i := range tooMany {
		tooMany[i] = named("", stellartest.SACContract, "")
		tooMany[i].Name = string(rune('a'+i%26)) + string(rune('a'+i/26))
	}

	durability := named("x", stellartest.SACContract, "")
	durability.DataDurability = "temp"

	badTimeout := named("t", stellartest.SACContract, "")
	badTimeout.Timeout = "soon"

	negativeTimeout := named("n", stellartest.SACContract, "")
	negativeTimeout.Timeout = "-1s"

	tests := map[string]probe.BatchRequest{
		"empty":            {},
		"too many":         {Checks: tooMany},
		"no contract":      {Checks: []probe.NamedCheck{named("x", "", "")}},
		"duplicate names":  {Checks: []probe.NamedCheck{named("x", stellartest.SACContract, ""), named("x", stellartest.WasmContract, "")}},
		"default dup name": {Checks: []probe.NamedCheck{named("", stellartest.SACContract, ""), named("", stellartest.SACContract, "decimals")}},
		"bad durability":   {Checks: []probe.NamedCheck{durability}},
		"bad timeout":      {Checks: []probe.NamedCheck{badTimeout}},
		"negative timeout": {Checks: []probe.NamedCheck{negativeTimeout}},
	}
	for name, req := range tests {
		_, err := p.CheckBatch(context.Background(), req)
		assert.ErrorIs(t, err, probe.ErrInvalidBatch, name)
	}
}

func TestCheckBatchStopsOnCancel(t *testing.T) {
	t.Parallel()
	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.CheckBatch(ctx, probe.BatchRequest{Checks: []probe.NamedCheck{named("a", stellartest.SACContract, "")}})
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, fake.EntryRequests, "a canceled batch sends nothing")
}

// hungClient makes the first GetLedgerEntries call block until its context is
// done, modeling an upstream that never answers. Later calls reach the
// fixtures, so the batch can be shown to keep going after a check times out.
type hungClient struct {
	*stellartest.Fake
	hangOnce sync.Once
}

func (c *hungClient) GetLedgerEntries(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	var hang bool
	c.hangOnce.Do(func() { hang = true })
	if !hang {
		return c.Fake.GetLedgerEntries(ctx, req)
	}
	<-ctx.Done()
	return protocol.GetLedgerEntriesResponse{}, ctx.Err()
}

func TestCheckBatchPerCheckTimeoutContinuesPastTheHungCheck(t *testing.T) {
	t.Parallel()

	p, err := probe.New(probe.Options{
		Client:        &hungClient{Fake: stellartest.NewFake(t)},
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    health.DefaultThresholds,
	})
	require.NoError(t, err)

	hung := named("hung", stellartest.WasmContract, "decimals")
	hung.Timeout = "50ms"

	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{
		hung,
		named("next", stellartest.SACContract, "decimals"),
	}})
	require.NoError(t, err, "a check that times out is a result, not a batch failure")

	assert.False(t, result.OK)
	assert.True(t, result.Errored)
	require.Len(t, result.Items, 2)
	assert.Nil(t, result.Items[0].Result)
	assert.Contains(t, result.Items[0].Error, "timed out after 50ms")
	require.NotNil(t, result.Items[1].Result, "the batch continues past a timeout")
	assert.True(t, result.Items[1].Result.OK)
}

// deadClient never answers and ignores the context it is given, standing in for
// the live JSON-RPC transport, which posts with an uncontexted request and so
// cannot be interrupted once a call is in flight.
type deadClient struct {
	*stellartest.Fake
}

func (c *deadClient) GetLedgerEntries(context.Context, protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	select {} // stays blocked; the abandoned goroutine ends with the test binary
}

func TestCheckBatchPerCheckTimeoutBoundsAnUninterruptibleCall(t *testing.T) {
	t.Parallel()

	p, err := probe.New(probe.Options{
		Client:        &deadClient{Fake: stellartest.NewFake(t)},
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    health.DefaultThresholds,
	})
	require.NoError(t, err)

	slow := named("slow", stellartest.WasmContract, "")
	slow.Timeout = "50ms"

	start := time.Now()
	result, err := p.CheckBatch(context.Background(), probe.BatchRequest{Checks: []probe.NamedCheck{slow}})
	require.NoError(t, err)

	assert.Less(t, time.Since(start), 5*time.Second,
		"the batch returns at the per-check deadline, not after the RPC timeout")
	assert.True(t, result.Errored)
	require.Len(t, result.Items, 1)
	assert.Nil(t, result.Items[0].Result)
	assert.Contains(t, result.Items[0].Error, "timed out after 50ms")
}
