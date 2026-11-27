package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
)

// twoNetworkConfig defines the profiles a cross-network comparison needs.
const twoNetworkConfig = `{
	"rpc_url": "https://top.example.com",
	"network_passphrase": "Top Network",
	"timeout": "10s",
	"profiles": {
		"testnet": {
			"rpc_url": "https://testnet.example.com",
			"network_passphrase": "Test SDF Network ; September 2015"
		},
		"mainnet": {
			"rpc_url": "https://mainnet.example.com",
			"network_passphrase": "Public Global Stellar Network ; September 2015"
		}
	}
}`

func TestLoadNetworkProfileKeepsEachProfilesNetwork(t *testing.T) {
	path := writeConfigFile(t, "soroprobe.json", twoNetworkConfig)

	// A process-wide RPC_URL and NETWORK_PASSPHRASE name exactly one network.
	// In a comparison they must not overwrite either side, or both sides would
	// silently probe the same endpoint.
	t.Setenv(config.EnvRPCURL, "https://env.example.com")
	t.Setenv(config.EnvNetworkPassphrase, "Env Network")
	t.Setenv(config.EnvTimeout, "7s")

	testnet, err := config.LoadNetworkProfile(path, "testnet")
	require.NoError(t, err)
	assert.Equal(t, "https://testnet.example.com", testnet.RPCURL)
	assert.Equal(t, "Test SDF Network ; September 2015", testnet.NetworkPassphrase)

	mainnet, err := config.LoadNetworkProfile(path, "mainnet")
	require.NoError(t, err)
	assert.Equal(t, "https://mainnet.example.com", mainnet.RPCURL)
	assert.Equal(t, "Public Global Stellar Network ; September 2015", mainnet.NetworkPassphrase)

	// Everything that is not network-specific still follows the environment.
	assert.Equal(t, 7*time.Second, testnet.Timeout)
	assert.Equal(t, 7*time.Second, mainnet.Timeout)

	require.NoError(t, testnet.Validate())
	require.NoError(t, mainnet.Validate())
}

func TestLoadNetworkProfileRequiresAProfileName(t *testing.T) {
	path := writeConfigFile(t, "soroprobe.json", twoNetworkConfig)

	_, err := config.LoadNetworkProfile(path, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profile name is required")
}

func TestLoadNetworkProfileReportsUnknownProfiles(t *testing.T) {
	path := writeConfigFile(t, "soroprobe.json", twoNetworkConfig)

	_, err := config.LoadNetworkProfile(path, "futurenet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown profile "futurenet"`)
	assert.Contains(t, err.Error(), "mainnet, testnet")
}

func TestLoadNetworkProfileWithoutAConfigFileIsAnError(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := config.LoadNetworkProfile("", "testnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no config file found")
}
