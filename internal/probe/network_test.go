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

func TestNetworkSuccess(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Network(context.Background(), "https://example.com/rpc", testnetPassphrase)
	require.NoError(t, err)

	assert.Equal(t, "https://example.com/rpc", res.RPCURL)
	assert.Equal(t, "healthy", res.Status)
	assert.Equal(t, fake.LatestLedger, res.LatestLedger)
	assert.Equal(t, 27, res.ProtocolVersion)
	assert.Equal(t, testnetPassphrase, res.NetworkPassphrase)
	assert.True(t, res.PassphraseMatch)
}

func TestNetworkPassphraseMismatch(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.NetworkPassphrase = "Public Global Stellar Network ; September 2015"
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Network(context.Background(), "https://example.com/rpc", testnetPassphrase)
	require.NoError(t, err)

	assert.Equal(t, "healthy", res.Status)
	assert.Equal(t, "Public Global Stellar Network ; September 2015", res.NetworkPassphrase)
	assert.False(t, res.PassphraseMatch)
}

func TestNetworkUnreachableHealth(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.HealthErr = errors.New("connection refused")
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Network(context.Background(), "https://example.com/rpc", testnetPassphrase)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, probe.ErrRPCUnreachable)
}

func TestNetworkUnreachableNetwork(t *testing.T) {
	t.Parallel()

	fake := stellartest.NewFake(t)
	fake.NetworkErr = errors.New("timeout")
	p := newProber(t, fake, health.Thresholds{})

	res, err := p.Network(context.Background(), "https://example.com/rpc", testnetPassphrase)
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, probe.ErrRPCUnreachable)
}
