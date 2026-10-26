package main

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestRedactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"https://user:pass@example.com/rpc?key=secret#frag", "https://example.com/rpc"},
		{"https://user@example.com:8000/rpc", "https://example.com:8000/rpc"},
		{"https://example.com/rpc?foo=bar", "https://example.com/rpc"},
		{"https://example.com/rpc", "https://example.com/rpc"},
		{"not a url", "not a url"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, redactURL(tt.input))
		})
	}
}

func TestNetworkRunsEndToEndAgainstTheFake(t *testing.T) {
	fake := stellartest.NewFake(t)

	t.Run("healthy network passes", func(t *testing.T) {
		out, err := runWithFake(t, fake, "network", "--rpc-url", "https://user:secret@example.com/rpc?key=123")
		require.NoError(t, err)

		assert.Contains(t, out, "https://example.com/rpc")
		assert.NotContains(t, out, "user:secret")
		assert.NotContains(t, out, "key=123")
		assert.Contains(t, out, "healthy")
		assert.Contains(t, out, "Test SDF Network ; September 2015")
		assert.Contains(t, out, "27")
	})

	t.Run("healthy network json output", func(t *testing.T) {
		out, err := runWithFake(t, fake, "--json", "network", "--rpc-url", "https://user:secret@example.com/rpc?key=123")
		require.NoError(t, err)

		var res probe.NetworkResult
		require.NoError(t, json.Unmarshal([]byte(out), &res))
		assert.Equal(t, "https://example.com/rpc", res.RPCURL)
		assert.Equal(t, "healthy", res.Status)
		assert.Equal(t, 27, res.ProtocolVersion)
		assert.Equal(t, fake.LatestLedger, res.LatestLedger)
		assert.True(t, res.PassphraseMatch)
	})

	t.Run("unhealthy rpc exits 1", func(t *testing.T) {
		unhealthyFake := stellartest.NewFake(t)
		unhealthyFake.HealthStatus = "degraded"

		out, err := runWithFake(t, unhealthyFake, "network")
		require.Error(t, err)
		assertExitCodeOne(t, err)
		assert.Contains(t, out, "degraded")
	})

	t.Run("passphrase mismatch exits 1", func(t *testing.T) {
		mismatchedFake := stellartest.NewFake(t)
		mismatchedFake.NetworkPassphrase = "Public Global Stellar Network ; September 2015"

		out, err := runWithFake(t, mismatchedFake, "network")
		require.Error(t, err)
		assertExitCodeOne(t, err)
		assert.Contains(t, out, "Public Global Stellar Network")
		assert.Contains(t, out, "mismatch")
	})

	t.Run("unreachable rpc exits 2", func(t *testing.T) {
		unreachableFake := stellartest.NewFake(t)
		unreachableFake.HealthErr = errors.New("connection refused")

		_, err := runWithFake(t, unreachableFake, "network")
		require.Error(t, err)

		var failed *checkFailedError
		assert.False(t, errors.As(err, &failed))
		assert.Contains(t, err.Error(), "connection refused")
	})
}
