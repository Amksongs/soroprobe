package probe_test

import (
	"context"
	"errors"
	"testing"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// testnetPassphrase is the network the fixture fake serves, and the default
// the rest of the suite configures its Prober for.
const testnetPassphrase = "Test SDF Network ; September 2015"

func TestReadyHealthyEndpoint(t *testing.T) {
	fake := stellartest.NewFake(t)
	p := newProber(t, fake, health.Thresholds{})

	require.NoError(t, p.Ready(context.Background(), testnetPassphrase))
	assert.Equal(t, 1, fake.HealthRequests)
	assert.Equal(t, 1, fake.NetworkRequests)
}

func TestReadyUnhealthyRPC(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.HealthStatus = "unhealthy"
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrRPCUnhealthy)
	assert.Contains(t, err.Error(), "unhealthy")

	// A failed health check must not spend a second call asking which
	// network the endpoint serves.
	assert.Equal(t, 0, fake.NetworkRequests)
}

func TestReadyUnreachableRPC(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.HealthErr = errors.New("connection refused")
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrRPCUnreachable)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestReadyUnreachableOnGetNetwork(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.NetworkErr = errors.New("connection refused")
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrRPCUnreachable)
}

// stubClient answers getHealth and getNetwork with exactly the values a test
// asks for, including the empty string. The fixture Fake treats an empty
// override as "use the default answer", which would hide the empty-response
// boundary these cases exist to pin down.
type stubClient struct {
	*stellartest.Fake
	status     string
	passphrase string
}

func (c stubClient) GetHealth(context.Context) (protocol.GetHealthResponse, error) {
	c.HealthRequests++
	return protocol.GetHealthResponse{Status: c.status, LatestLedger: c.LatestLedger}, nil
}

func (c stubClient) GetNetwork(context.Context) (protocol.GetNetworkResponse, error) {
	c.NetworkRequests++
	return protocol.GetNetworkResponse{Passphrase: c.passphrase}, nil
}

// TestReadyEmptyInputs covers the empty values Ready must reject rather than
// treat as a match: an empty wanted passphrase, an empty health status, and an
// endpoint that reports no passphrase at all. An empty string comparing equal
// to an empty string is the boundary where a naive equality check would report
// a misconfigured endpoint as ready.
func TestReadyEmptyInputs(t *testing.T) {
	tests := []struct {
		name             string
		wantPassphrase   string
		healthStatus     string
		servedPassphrase string
		wantErr          error
		wantNetworkCalls int
	}{
		{
			name:             "empty wanted passphrase does not match a healthy endpoint",
			wantPassphrase:   "",
			healthStatus:     "healthy",
			servedPassphrase: testnetPassphrase,
			wantErr:          probe.ErrWrongNetwork,
			wantNetworkCalls: 1,
		},
		{
			name:             "empty health status is not healthy",
			wantPassphrase:   testnetPassphrase,
			healthStatus:     "",
			servedPassphrase: testnetPassphrase,
			wantErr:          probe.ErrRPCUnhealthy,
			wantNetworkCalls: 0,
		},
		{
			name:             "empty served passphrase does not match a configured network",
			wantPassphrase:   testnetPassphrase,
			healthStatus:     "healthy",
			servedPassphrase: "",
			wantErr:          probe.ErrWrongNetwork,
			wantNetworkCalls: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := stellartest.NewFake(t)
			client := stubClient{Fake: fake, status: tc.healthStatus, passphrase: tc.servedPassphrase}
			p, err := probe.New(probe.Options{
				Client:        client,
				SourceAccount: stellartest.SourceAccount,
				Thresholds:    health.Thresholds{},
			})
			require.NoError(t, err)

			err = p.Ready(context.Background(), tc.wantPassphrase)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.wantErr)
			assert.Equal(t, 1, fake.HealthRequests)
			assert.Equal(t, tc.wantNetworkCalls, fake.NetworkRequests)
		})
	}
}

func TestReadyWrongNetwork(t *testing.T) {
	fake := stellartest.NewFake(t)
	fake.NetworkPassphrase = "Public Global Stellar Network ; September 2015"
	p := newProber(t, fake, health.Thresholds{})

	err := p.Ready(context.Background(), testnetPassphrase)
	require.Error(t, err)
	assert.ErrorIs(t, err, probe.ErrWrongNetwork)
	// The reason must quote both networks: an operator has to be able to
	// see what the endpoint serves and what was expected.
	assert.Contains(t, err.Error(), "Public Global Stellar Network")
	assert.Contains(t, err.Error(), testnetPassphrase)
}
