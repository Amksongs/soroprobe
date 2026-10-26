package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func findCheck(checks []probe.Check, name string) *probe.Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func TestCheckExpectMatch(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	// SAC decimals returns uint32(7). Numeric comparison means u32:7, u64:7, or bare 7 match.
	for _, spec := range []string{"u32:7", "u64:7", "7"} {
		res, err := p.Check(context.Background(), probe.CheckRequest{
			ContractID: stellartest.SACContract,
			Function:   "decimals",
			Expect:     spec,
		})
		require.NoError(t, err)
		assert.True(t, res.OK)

		chk := findCheck(res.Checks, "expect")
		require.NotNil(t, chk)
		assert.Equal(t, probe.OutcomePass, chk.Outcome)
		assert.Contains(t, chk.Detail, "expected")
		assert.Contains(t, chk.Detail, "got")
		assert.Contains(t, chk.Detail, "7")
	}
}

func TestCheckExpectMismatch(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	res, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Expect:     "u32:8",
	})
	require.NoError(t, err)
	assert.False(t, res.OK)

	chk := findCheck(res.Checks, "expect")
	require.NotNil(t, chk)
	assert.Equal(t, probe.OutcomeFail, chk.Outcome)
	assert.Contains(t, chk.Detail, "expected 8, got 7")
}

func TestCheckExpectTypeMismatch(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p, err := probe.New(probe.Options{
		Client:        fake,
		SourceAccount: stellartest.SourceAccount,
	})
	require.NoError(t, err)

	res, err := p.Check(context.Background(), probe.CheckRequest{
		ContractID: stellartest.SACContract,
		Function:   "decimals",
		Expect:     "bool:true",
	})
	require.NoError(t, err)
	assert.False(t, res.OK)

	chk := findCheck(res.Checks, "expect")
	require.NotNil(t, chk)
	assert.Equal(t, probe.OutcomeFail, chk.Outcome)
	assert.Contains(t, chk.Detail, "type mismatch")
	assert.Contains(t, chk.Detail, "expected true, got 7")
}
