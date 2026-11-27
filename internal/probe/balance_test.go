package probe_test

import (
	"context"
	"github.com/stellar/go-stellar-sdk/xdr"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func i128Val(n int64) xdr.ScVal {
	p := xdr.Int128Parts{Hi: 0, Lo: xdr.Uint64(uint64(n))} //nolint:gosec // G115: test values are non-negative
	return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &p}
}

func TestBalanceFormatsWithDecimalsAndSymbol(t *testing.T) {
	fake := stellartest.NewFake(t)
	base := fake.Simulations["decimals"]
	fake.Simulations["decimals"] = simResult(t, base, u32Val(7))
	fake.Simulations["symbol"] = simResult(t, base, strVal("USDC"))
	fake.Simulations["balance"] = simResult(t, base, i128Val(125_000_000))

	p := standardProber(t, fake, nil)
	res, err := p.Balance(context.Background(), probe.BalanceRequest{
		Token:   stellartest.WasmContract,
		Holders: []string{stellartest.WasmContract, "not-an-address"},
	})
	require.NoError(t, err)
	assert.Equal(t, "USDC", res.Symbol)
	assert.EqualValues(t, 7, res.Decimals)
	require.Len(t, res.Balances, 2)
	assert.Equal(t, "12.5", res.Balances[0].Balance)
	assert.Equal(t, "125000000", res.Balances[0].Raw)
	assert.Contains(t, res.Balances[1].Error, "invalid address")
}

func TestBalanceNeedsAHolder(t *testing.T) {
	p := standardProber(t, stellartest.NewFake(t), nil)
	_, err := p.Balance(context.Background(), probe.BalanceRequest{Token: stellartest.WasmContract})
	require.Error(t, err)
}

func TestFormatUnits(t *testing.T) {
	cases := []struct {
		name     string
		n        int64
		decimals uint32
		want     string
	}{
		{name: "zero", n: 0, decimals: 7, want: "0"},
		{name: "smallest fraction", n: 1, decimals: 7, want: "0.0000001"},
		{name: "whole unit", n: 10_000_000, decimals: 7, want: "1"},
		{name: "negative fraction", n: -125, decimals: 2, want: "-1.25"},
		{name: "no decimals", n: 42, decimals: 0, want: "42"},
		{name: "leading zero fraction", n: 5, decimals: 3, want: "0.005"},
		{name: "trailing zero fractional digits", n: 120, decimals: 2, want: "1.2"},
		{name: "maximum int64", n: 9_223_372_036_854_775_807, decimals: 7, want: "922337203685.4775807"},
		{name: "minimum int64", n: -9_223_372_036_854_775_808, decimals: 7, want: "-922337203685.4775808"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, probe.FormatUnits(big.NewInt(c.n), c.decimals))
		})
	}
}
