package probe_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// newProber wires a Prober onto the fixture-backed fake.
func newProber(t *testing.T, fake *stellartest.Fake, thresholds health.Thresholds) *probe.Prober {
	t.Helper()

	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
		Thresholds:    thresholds,
	})
	require.NoError(t, err)
	return p
}

func TestNewValidatesOptions(t *testing.T) {
	t.Parallel()

	t.Run("requires a client", func(t *testing.T) {
		t.Parallel()
		_, err := probe.New(probe.Options{SourceAccount: stellartest.SourceAccount})
		require.Error(t, err)
	})

	t.Run("requires a source account", func(t *testing.T) {
		t.Parallel()
		_, err := probe.New(probe.Options{Client: &stellartest.Fake{}})
		require.Error(t, err)
	})

	t.Run("defaults the codec and thresholds", func(t *testing.T) {
		t.Parallel()
		p, err := probe.New(probe.Options{Client: &stellartest.Fake{}, SourceAccount: stellartest.SourceAccount})
		require.NoError(t, err)
		require.NotNil(t, p)
	})
}

// --- simulate ---------------------------------------------------------------

func TestSimulateSuccess(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
	})
	require.NoError(t, err)

	assert.True(t, result.Success)
	assert.Empty(t, result.Error)
	// The recorded contract is the native asset, which has 7 decimals.
	assert.Equal(t, uint32(7), result.ReturnValue)
	assert.NotEmpty(t, result.ReturnValueXDR)

	// Costs come from the returned transaction data, since simulateTransaction
	// no longer carries a top-level cost object.
	assert.Positive(t, result.Cost.Instructions)
	assert.Positive(t, result.Cost.MinResourceFee)
	assert.Equal(t, result.Cost.MinResourceFee, result.Cost.ResourceFee)

	assert.Len(t, result.Footprint.ReadOnly, 1)
	assert.Contains(t, result.Footprint.ReadOnly[0], stellartest.SACContract)
	assert.Empty(t, result.Footprint.ReadWrite)

	assert.Nil(t, result.RestoreRequired)
	assert.NotEmpty(t, result.Events, "diagnostic events should be decoded")
}

func TestSimulateBuildsTheRequestedCall(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Args:       []string{"u32:1", "sym:hello"},
	})
	require.NoError(t, err)

	require.Len(t, fake.SimulateRequests, 1)
	assert.Equal(t, []string{"decimals"}, fake.Functions)

	// Arguments must survive encoding into the envelope in order.
	args, err := stellartest.ArgsOf(fake.SimulateRequests[0].Transaction)
	require.NoError(t, err)
	require.Len(t, args, 2)
	assert.EqualValues(t, 1, *args[0].U32)
	assert.EqualValues(t, "hello", string(*args[1].Sym))
}

func TestSimulateFailureIsAResultNotAnError(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	// A contract rejecting a call is a valid answer, so Simulate must return a
	// result rather than a Go error.
	result, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "no_such_fn",
	})
	require.NoError(t, err)

	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "HostError")
	assert.Nil(t, result.ReturnValue)
}

func TestSimulateRejectsBadInput(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)
	ctx := context.Background()

	t.Run("invalid contract id", func(t *testing.T) {
		t.Parallel()
		_, err := p.Simulate(ctx, probe.SimulateRequest{ContractID: "nope", Function: "decimals"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid contract id")
	})

	t.Run("missing function", func(t *testing.T) {
		t.Parallel()
		_, err := p.Simulate(ctx, probe.SimulateRequest{ContractID: stellartest.SACContract})
		require.Error(t, err)
	})

	t.Run("bad argument", func(t *testing.T) {
		t.Parallel()
		_, err := p.Simulate(ctx, probe.SimulateRequest{
			ContractID: stellartest.SACContract,
			Function:   "decimals",
			Args:       []string{"u32:not-a-number"},
		})
		require.Error(t, err)
	})
}

func TestSimulatePropagatesTransportErrors(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.SimulateErr = errors.New("rpc unreachable")
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Simulate(context.Background(), probe.SimulateRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc unreachable")
}

// --- inspect ----------------------------------------------------------------

func TestInspectStellarAssetContract(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.SACContract,
	})
	require.NoError(t, err)

	assert.True(t, result.Deployed)
	assert.Equal(t, "stellar_asset", result.Executable)
	assert.Empty(t, result.WasmHash)

	// A Stellar Asset Contract is implemented by the host, so there is no code
	// entry to report or to expire.
	assert.Nil(t, result.Code)

	assert.Equal(t, health.StatusHealthy, result.Instance.Status)
	assert.Equal(t, health.StatusHealthy, result.Status)
	assert.NotNil(t, result.InstanceStorage)

	// Only one round trip is needed when there is no code entry to chase.
	assert.Len(t, fake.EntryRequests, 1)
}

func TestInspectWasmContractReportsBothEntries(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.WasmContract,
	})
	require.NoError(t, err)

	assert.True(t, result.Deployed)
	assert.Equal(t, "wasm", result.Executable)
	assert.Len(t, result.WasmHash, 64, "wasm hash should be hex-encoded sha256")

	// Instance and code carry independent TTLs, which is the whole reason both
	// are reported separately.
	require.NotNil(t, result.Code)
	assert.True(t, result.Instance.Present)
	assert.True(t, result.Code.Present)
	assert.NotEqual(t, result.Instance.LedgersRemaining, result.Code.LedgersRemaining)

	// The code key is only discoverable from the instance, hence two round trips.
	assert.Len(t, fake.EntryRequests, 2)
}

func TestInspectUndeployedContract(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.UndeployedContract,
	})
	require.NoError(t, err)

	assert.False(t, result.Deployed)
	assert.Equal(t, health.StatusMissing, result.Status)
	assert.Equal(t, health.StatusMissing, result.Instance.Status)
	assert.Nil(t, result.Code)
	// It must not go looking for code when there is no instance.
	assert.Len(t, fake.EntryRequests, 1)
}

func TestInspectAppliesThresholds(t *testing.T) {
	t.Parallel()

	instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)

	tests := []struct {
		name       string
		liveUntil  func(current uint32) uint32
		thresholds health.Thresholds
		want       health.Status
	}{
		{
			name:       "comfortably live",
			liveUntil:  func(c uint32) uint32 { return c + 1_000_000 },
			thresholds: health.DefaultThresholds,
			want:       health.StatusHealthy,
		},
		{
			name:       "inside the warning band",
			liveUntil:  func(c uint32) uint32 { return c + 50_000 },
			thresholds: health.DefaultThresholds,
			want:       health.StatusWarning,
		},
		{
			name:       "inside the critical band",
			liveUntil:  func(c uint32) uint32 { return c + 100 },
			thresholds: health.DefaultThresholds,
			want:       health.StatusCritical,
		},
		{
			name:       "already lapsed",
			liveUntil:  func(c uint32) uint32 { return c - 1 },
			thresholds: health.DefaultThresholds,
			want:       health.StatusExpired,
		},
		{
			name:       "custom thresholds can widen the warning band",
			liveUntil:  func(c uint32) uint32 { return c + 500_000 },
			thresholds: health.Thresholds{Warn: 1_000_000, Critical: 10},
			want:       health.StatusWarning,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := stellartest.NewFake(t)
			fake.SetTTL(t, instanceKey, tt.liveUntil(fake.LatestLedger))
			p := newProber(t, fake, tt.thresholds)

			result, err := p.Inspect(context.Background(), probe.InspectRequest{
				ContractID: stellartest.SACContract,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.Instance.Status)
			assert.Equal(t, tt.want, result.Status)
		})
	}
}

func TestInspectNamedDataKeys(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	// No fixture holds these data entries, so they must be reported missing
	// rather than silently dropped.
	result, err := p.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.SACContract,
		DataKeys:   []string{"sym:Balance", "sym:Admin"},
	})
	require.NoError(t, err)

	require.Len(t, result.Data, 2)
	assert.Equal(t, "sym:Balance", result.Data[0].Key)
	assert.Equal(t, health.StatusMissing, result.Data[0].Status)
	assert.Equal(t, "sym:Admin", result.Data[1].Key)

	// A missing data entry drags the overall status down.
	assert.Equal(t, health.StatusMissing, result.Status)
}

func TestInspectDataKeyDurabilityChangesTheKey(t *testing.T) {
	t.Parallel()

	persistent := stellartest.NewFake(t)
	pp := newProber(t, persistent, health.DefaultThresholds)
	_, err := pp.Inspect(context.Background(), probe.InspectRequest{
		ContractID: stellartest.SACContract,
		DataKeys:   []string{"sym:Balance"},
	})
	require.NoError(t, err)

	temporary := stellartest.NewFake(t)
	tp := newProber(t, temporary, health.DefaultThresholds)
	_, err = tp.Inspect(context.Background(), probe.InspectRequest{
		ContractID:     stellartest.SACContract,
		DataKeys:       []string{"sym:Balance"},
		DataDurability: health.DurabilityTemporary,
	})
	require.NoError(t, err)

	// Durability is part of the ledger key, so the two lookups must differ.
	assert.NotEqual(t, persistent.EntryRequests[0], temporary.EntryRequests[0])
}

func TestInspectRejectsBadContractID(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: "nope"})
	require.Error(t, err)
}

func TestInspectPropagatesTransportErrors(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.EntriesErr = errors.New("rpc unreachable")
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Inspect(context.Background(), probe.InspectRequest{ContractID: stellartest.SACContract})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc unreachable")
}

// --- check ------------------------------------------------------------------

// outcomes indexes a check result by check name.
func outcomes(r *probe.CheckResult) map[string]probe.CheckOutcome {
	out := make(map[string]probe.CheckOutcome, len(r.Checks))
	for _, c := range r.Checks {
		out[c.Name] = c.Outcome
	}
	return out
}

func TestCheckHealthyContractWithoutFunction(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.SACContract,
	})
	require.NoError(t, err)

	assert.True(t, result.OK)
	got := outcomes(result)
	assert.Equal(t, probe.OutcomePass, got["deployed"])
	assert.Equal(t, probe.OutcomePass, got["instance_ttl"])
	// Neither of these can be asserted, so both must be skipped rather than
	// guessed at.
	assert.Equal(t, probe.OutcomeSkip, got["code_ttl"])
	assert.Equal(t, probe.OutcomeSkip, got["simulate"])
	assert.Nil(t, result.Simulate)
}

func TestCheckWithFunctionSimulates(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
	})
	require.NoError(t, err)

	assert.True(t, result.OK)
	assert.Equal(t, probe.OutcomePass, outcomes(result)["simulate"])
	require.NotNil(t, result.Simulate)
	assert.True(t, result.Simulate.Success)
}

func TestCheckFailsWhenSimulationFails(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.SACContract,
		Function:   "no_such_fn",
	})
	require.NoError(t, err)

	assert.False(t, result.OK)
	assert.Equal(t, probe.OutcomeFail, outcomes(result)["simulate"])
}

func TestCheckFailsWhenNotDeployed(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.UndeployedContract,
		Function:   "decimals",
	})
	require.NoError(t, err)

	assert.False(t, result.OK)
	assert.Equal(t, probe.OutcomeFail, outcomes(result)["deployed"])

	// Once deployment fails there is nothing worth simulating.
	assert.Len(t, result.Checks, 1)
	assert.Nil(t, result.Simulate)
	assert.Empty(t, fake.SimulateRequests)
}

func TestCheckTTLOutcomes(t *testing.T) {
	t.Parallel()

	instanceKey := stellartest.InstanceKeyOf(t, stellartest.SACContract)

	tests := []struct {
		name      string
		liveUntil func(current uint32) uint32
		want      probe.CheckOutcome
		wantOK    bool
	}{
		{
			name:      "healthy passes",
			liveUntil: func(c uint32) uint32 { return c + 1_000_000 },
			want:      probe.OutcomePass,
			wantOK:    true,
		},
		{
			// A warning is a nudge to extend the TTL, not a reason to break a
			// pipeline, so the overall check still passes.
			name:      "warning warns but does not fail",
			liveUntil: func(c uint32) uint32 { return c + 50_000 },
			want:      probe.OutcomeWarn,
			wantOK:    true,
		},
		{
			name:      "critical fails",
			liveUntil: func(c uint32) uint32 { return c + 100 },
			want:      probe.OutcomeFail,
			wantOK:    false,
		},
		{
			name:      "expired fails",
			liveUntil: func(c uint32) uint32 { return c - 1 },
			want:      probe.OutcomeFail,
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := stellartest.NewFake(t)
			fake.SetTTL(t, instanceKey, tt.liveUntil(fake.LatestLedger))
			p := newProber(t, fake, health.DefaultThresholds)

			result, err := p.Check(context.Background(), probe.CheckRequest{
				ContractID: stellartest.SACContract,
			})
			require.NoError(t, err)

			assert.Equal(t, tt.want, outcomes(result)["instance_ttl"])
			assert.Equal(t, tt.wantOK, result.OK)
		})
	}
}

func TestCheckIncludesInspectReport(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.DefaultThresholds)

	result, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.WasmContract,
	})
	require.NoError(t, err)

	require.NotNil(t, result.Inspect)
	assert.Equal(t, "wasm", result.Inspect.Executable)
	// A Wasm contract has a real code entry, so that check is not skipped.
	assert.NotEqual(t, probe.OutcomeSkip, outcomes(result)["code_ttl"])
	assert.Equal(t, result.Inspect.LatestLedger, result.LatestLedger)
}

func TestCheckPropagatesTransportErrors(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.EntriesErr = errors.New("rpc unreachable")
	p := newProber(t, fake, health.DefaultThresholds)

	_, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.SACContract})
	require.Error(t, err)
}

func TestCheckCostBudget(t *testing.T) {
	t.Parallel()

	uint32Ptr := func(v uint32) *uint32 { return &v }
	int64Ptr := func(v int64) *int64 { return &v }

	t.Run("no cost check when limits are not set", func(t *testing.T) {
		t.Parallel()
		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			Function:   "decimals",
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		_, hasCost := outcomes(res)["cost"]
		assert.False(t, hasCost)
	})

	t.Run("cost check passes within limits", func(t *testing.T) {
		t.Parallel()
		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:      stellartest.SACContract,
			Function:        "decimals",
			MaxInstructions: uint32Ptr(150000),
			MaxFeeStroops:   int64Ptr(20000),
			MaxWriteBytes:   uint32Ptr(100),
		})
		require.NoError(t, err)
		assert.True(t, res.OK)
		assert.Equal(t, probe.OutcomePass, outcomes(res)["cost"])
	})

	t.Run("cost check fails when instructions exceeded", func(t *testing.T) {
		t.Parallel()
		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:      stellartest.SACContract,
			Function:        "decimals",
			MaxInstructions: uint32Ptr(100000), // actual is 127569
		})
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, probe.OutcomeFail, outcomes(res)["cost"])
	})

	t.Run("cost check fails when fee exceeded", func(t *testing.T) {
		t.Parallel()
		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:    stellartest.SACContract,
			Function:      "decimals",
			MaxFeeStroops: int64Ptr(10000), // actual is 12279
		})
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, probe.OutcomeFail, outcomes(res)["cost"])
	})

	t.Run("failed simulation reports simulation failure rather than cost failure", func(t *testing.T) {
		t.Parallel()
		fake := stellartest.NewFake(t)
		p := newProber(t, fake, health.DefaultThresholds)

		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID:      stellartest.SACContract,
			Function:        "no_such_fn",
			MaxInstructions: uint32Ptr(10),
		})
		require.NoError(t, err)
		assert.False(t, res.OK)
		assert.Equal(t, probe.OutcomeFail, outcomes(res)["simulate"])
		_, hasCost := outcomes(res)["cost"]
		assert.False(t, hasCost, "cost check must not run on failed simulation")
	})
}
