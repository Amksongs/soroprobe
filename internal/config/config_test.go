package config_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/health"
)

// writeConfig writes a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "soroprobe.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// writeConfigFile writes a named config file into a temp dir and returns its path.
func writeConfigFile(t *testing.T, filename, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), filename)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestDefaultsTargetTestnet(t *testing.T) {
	cfg := config.Default()

	assert.Equal(t, config.DefaultRPCURL, cfg.RPCURL)
	assert.Equal(t, "Test SDF Network ; September 2015", cfg.NetworkPassphrase)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
	// A fresh checkout must be runnable with no configuration at all.
	require.NoError(t, cfg.Validate())
}

func TestDefaultSourceAccountIsValid(t *testing.T) {
	// Simulation needs a well-formed source account but never a funded one, so
	// the default placeholder still has to be a real strkey public key.
	cfg := config.Default()
	assert.Len(t, cfg.SourceAccount, 56)
	assert.NoError(t, cfg.Validate())
}

func TestDefaultHTTPAddrIsLoopback(t *testing.T) {
	// SoroProbe is read-only, but an instance reachable from the network still
	// spends the operator's RPC quota on whoever finds it, so the default
	// serves the local machine only. Serving wider than that is deliberate:
	// --addr 0.0.0.0:8080.
	cfg := config.Default()
	assert.Equal(t, "127.0.0.1:8080", cfg.HTTPAddr)

	host, port, err := net.SplitHostPort(cfg.HTTPAddr)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.Equal(t, "8080", port)
	assert.NoError(t, cfg.Validate())
}

func TestLoadWithNoFileOrEnvGivesDefaults(t *testing.T) {
	// An absent ./soroprobe.json is not an error; an explicitly named one is.
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, config.Default(), cfg)
}

func TestLoadMissingExplicitFileIsAnError(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "nope.json"))
	require.Error(t, err)
}

func TestLoadFromFile(t *testing.T) {
	path := writeConfig(t, `{
		"rpc_url": "https://rpc.example.com",
		"network_passphrase": "Custom Network",
		"http_addr": ":9999",
		"log_level": "debug",
		"log_format": "json",
		"timeout": "5s",
		"warn_ledgers": 100,
		"critical_ledgers": 10,
		"sorovault_url": "http://vault.internal:8080"
	}`)

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://rpc.example.com", cfg.RPCURL)
	assert.Equal(t, "Custom Network", cfg.NetworkPassphrase)
	assert.Equal(t, ":9999", cfg.HTTPAddr)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "json", cfg.LogFormat)
	assert.Equal(t, 5*time.Second, cfg.Timeout)
	assert.EqualValues(t, 100, cfg.WarnLedgers)
	assert.EqualValues(t, 10, cfg.CriticalLedgers)
	assert.Equal(t, "http://vault.internal:8080", cfg.SoroVaultURL)
	// Unset keys keep their defaults.
	assert.Equal(t, config.DefaultSourceAccount, cfg.SourceAccount)
}

func TestLoadPerKindThresholdOverrides(t *testing.T) {
	path := writeConfig(t, `{"thresholds":{"instance":{"warn":500,"critical":50},"code":{"warn":900}}}`)
	t.Setenv(config.EnvDataCriticalLedgers, "25")

	cfg, err := config.Load(path)
	require.NoError(t, err)
	require.NotNil(t, cfg.ThresholdOverrides.Instance)
	assert.EqualValues(t, 500, *cfg.ThresholdOverrides.Instance.Warn)
	assert.EqualValues(t, 50, *cfg.ThresholdOverrides.Instance.Critical)
	require.NotNil(t, cfg.ThresholdOverrides.Code)
	assert.EqualValues(t, 900, *cfg.ThresholdOverrides.Code.Warn)
	assert.Nil(t, cfg.ThresholdOverrides.Code.Critical, "an omitted value falls back to the global threshold")
	require.NotNil(t, cfg.ThresholdOverrides.Data)
	assert.EqualValues(t, 25, *cfg.ThresholdOverrides.Data.Critical)
}

func TestLoadFromYAMLFile(t *testing.T) {
	for _, filename := range []string{"soroprobe.yaml", "soroprobe.yml"} {
		t.Run(filename, func(t *testing.T) {
			path := writeConfigFile(t, filename, `
# Configuration for SoroProbe
rpc_url: "https://rpc.example.com"
network_passphrase: "Custom Network"
http_addr: ":9999"
log_level: "debug"
timeout: "5s"
warn_ledgers: 100
critical_ledgers: 10
sorovault_url: "http://vault.internal:8080"
`)

			cfg, err := config.Load(path)
			require.NoError(t, err)

			assert.Equal(t, "https://rpc.example.com", cfg.RPCURL)
			assert.Equal(t, "Custom Network", cfg.NetworkPassphrase)
			assert.Equal(t, ":9999", cfg.HTTPAddr)
			assert.Equal(t, "debug", cfg.LogLevel)
			assert.Equal(t, 5*time.Second, cfg.Timeout)
			assert.EqualValues(t, 100, cfg.WarnLedgers)
			assert.EqualValues(t, 10, cfg.CriticalLedgers)
			assert.Equal(t, "http://vault.internal:8080", cfg.SoroVaultURL)
			// Unset keys keep their defaults.
			assert.Equal(t, config.DefaultSourceAccount, cfg.SourceAccount)
		})
	}
}

func TestDefaultLookupOrder(t *testing.T) {
	t.Run("json before yaml and yml", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "soroprobe.json"), []byte(`{"rpc_url":"https://json.example.com"}`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "soroprobe.yaml"), []byte(`rpc_url: "https://yaml.example.com"`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "soroprobe.yml"), []byte(`rpc_url: "https://yml.example.com"`), 0o600))

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Equal(t, "https://json.example.com", cfg.RPCURL)
	})

	t.Run("yaml before yml when json absent", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "soroprobe.yaml"), []byte(`rpc_url: "https://yaml.example.com"`), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "soroprobe.yml"), []byte(`rpc_url: "https://yml.example.com"`), 0o600))

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Equal(t, "https://yaml.example.com", cfg.RPCURL)
	})

	t.Run("yml when json and yaml absent", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)

		require.NoError(t, os.WriteFile(filepath.Join(dir, "soroprobe.yml"), []byte(`rpc_url: "https://yml.example.com"`), 0o600))

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Equal(t, "https://yml.example.com", cfg.RPCURL)
	})
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Run("unknown key in json", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.json", `{"rpc_url":"https://rpc.example.com","unknown_key":"val"}`)
		_, err := config.Load(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown field")
	})

	t.Run("unknown key in yaml", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.yaml", "rpc_url: \"https://rpc.example.com\"\nunknown_key: val\n")
		_, err := config.Load(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("unknown key in yml", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.yml", "rpc_url: \"https://rpc.example.com\"\nunknown_key: val\n")
		_, err := config.Load(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestProfilePrecedenceAndResolution(t *testing.T) {
	configContent := `{
		"rpc_url": "https://top.example.com",
		"network_passphrase": "Top Network",
		"http_addr": ":8081",
		"log_level": "info",
		"timeout": "10s",
		"warn_ledgers": 100,
		"critical_ledgers": 10,
		"profiles": {
			"mainnet": {
				"rpc_url": "https://mainnet.example.com",
				"network_passphrase": "Public Global Stellar Network ; September 2015",
				"warn_ledgers": 200,
				"critical_ledgers": 20
			},
			"testnet": {
				"rpc_url": "https://testnet.example.com"
			}
		}
	}`

	t.Run("profile overrides top-level file keys and defaults", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.json", configContent)
		cfg, err := config.LoadWithProfile(path, "mainnet")
		require.NoError(t, err)

		// Profile overrides
		assert.Equal(t, "https://mainnet.example.com", cfg.RPCURL)
		assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
		assert.EqualValues(t, 200, cfg.WarnLedgers)
		assert.EqualValues(t, 20, cfg.CriticalLedgers)

		// Top-level file keys inherited when not overridden by profile
		assert.Equal(t, ":8081", cfg.HTTPAddr)
		assert.Equal(t, "info", cfg.LogLevel)
		assert.Equal(t, 10*time.Second, cfg.Timeout)

		// Defaults inherited when absent from both top-level and profile
		assert.Equal(t, config.DefaultSourceAccount, cfg.SourceAccount)
	})

	t.Run("env overrides profile", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.json", configContent)
		t.Setenv(config.EnvRPCURL, "https://env.example.com")
		t.Setenv(config.EnvWarnLedgers, "300")

		cfg, err := config.LoadWithProfile(path, "mainnet")
		require.NoError(t, err)

		// Environment wins over profile
		assert.Equal(t, "https://env.example.com", cfg.RPCURL)
		assert.EqualValues(t, 300, cfg.WarnLedgers)

		// Profile keys not set in env remain
		assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
		assert.EqualValues(t, 20, cfg.CriticalLedgers)
	})

	t.Run("profile selected via SOROPROBE_PROFILE", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.json", configContent)
		t.Setenv(config.EnvProfile, "mainnet")

		cfg, err := config.Load(path)
		require.NoError(t, err)
		assert.Equal(t, "https://mainnet.example.com", cfg.RPCURL)
		assert.Equal(t, "Public Global Stellar Network ; September 2015", cfg.NetworkPassphrase)
	})

	t.Run("YAML profile support", func(t *testing.T) {
		yamlContent := `
rpc_url: https://top-yaml.example.com
warn_ledgers: 100
critical_ledgers: 10
profiles:
  custom:
    rpc_url: https://custom-yaml.example.com
    warn_ledgers: 500
`
		path := writeConfigFile(t, "soroprobe.yaml", yamlContent)
		cfg, err := config.LoadWithProfile(path, "custom")
		require.NoError(t, err)
		assert.Equal(t, "https://custom-yaml.example.com", cfg.RPCURL)
		assert.EqualValues(t, 500, cfg.WarnLedgers)
		assert.EqualValues(t, 10, cfg.CriticalLedgers)
	})

	t.Run("unknown profile lists defined profiles", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.json", configContent)
		_, err := config.LoadWithProfile(path, "unknown")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown profile "unknown"`)
		assert.Contains(t, err.Error(), "mainnet, testnet")
	})

	t.Run("unknown profile when no profiles defined", func(t *testing.T) {
		path := writeConfigFile(t, "soroprobe.json", `{"rpc_url":"https://example.com"}`)
		_, err := config.LoadWithProfile(path, "missing")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown profile "missing"`)
		assert.Contains(t, err.Error(), "no profiles defined")
	})
}

func TestLogFileCannotSetTheAPIToken(t *testing.T) {
	// The token is a secret, so the config file cannot carry one: an
	// "api_token" key is ignored rather than honored, and the operator is
	// pointed at the environment instead.
	path := writeConfig(t, `{"api_token": "file-issued-token-abcdef"}`)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Empty(t, cfg.APIToken)
}

func TestLoadFileFoundViaEnvVar(t *testing.T) {
	path := writeConfig(t, `{"rpc_url":"https://from-env-file.example.com"}`)
	t.Setenv(config.EnvConfigFile, path)

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, "https://from-env-file.example.com", cfg.RPCURL)
}

func TestEnvOverridesFile(t *testing.T) {
	path := writeConfig(t, `{"rpc_url":"https://from-file.example.com","log_level":"debug"}`)
	t.Setenv(config.EnvRPCURL, "https://from-env.example.com")

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "https://from-env.example.com", cfg.RPCURL, "env must win over the file")
	assert.Equal(t, "debug", cfg.LogLevel, "keys not set in env keep the file value")
}

func TestEnvVarsAreRead(t *testing.T) {
	t.Setenv(config.EnvRPCURL, "https://rpc.example.com")
	t.Setenv(config.EnvNetworkPassphrase, "Public Global Stellar Network ; September 2015")
	t.Setenv(config.EnvSourceAccount, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	t.Setenv(config.EnvHTTPAddr, ":7000")
	t.Setenv(config.EnvCORSOrigins, "https://dashboard.example, https://admin.example")
	t.Setenv(config.EnvLogLevel, "warn")
	t.Setenv(config.EnvLogFormat, "json")
	t.Setenv(config.EnvAPIToken, "0123456789abcdef")
	t.Setenv(config.EnvTimeout, "90s")
	t.Setenv(config.EnvWarnLedgers, "500")
	t.Setenv(config.EnvCriticalLedgers, "50")
	t.Setenv(config.EnvSoroVaultURL, "https://vault.example.com")
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)

	assert.Equal(t, "https://rpc.example.com", cfg.RPCURL)
	assert.Equal(t, ":7000", cfg.HTTPAddr)
	assert.Equal(t, []string{"https://dashboard.example", "https://admin.example"}, cfg.CORSOrigins)
	assert.Equal(t, "warn", cfg.LogLevel)
	assert.Equal(t, "json", cfg.LogFormat)
	assert.Equal(t, "0123456789abcdef", cfg.APIToken)
	assert.Equal(t, 90*time.Second, cfg.Timeout)
	assert.EqualValues(t, 500, cfg.WarnLedgers)
	assert.EqualValues(t, 50, cfg.CriticalLedgers)
	assert.Equal(t, "https://vault.example.com", cfg.SoroVaultURL)
	require.NoError(t, cfg.Validate())
}

func TestEmptyEnvVarIsIgnored(t *testing.T) {
	// An exported-but-empty variable, as a shell can easily produce, must not
	// wipe out the default.
	t.Setenv(config.EnvRPCURL, "")
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, config.DefaultRPCURL, cfg.RPCURL)
}

func TestLoadRejectsMalformedInput(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		_, err := config.Load(writeConfig(t, `{not json`))
		require.Error(t, err)
	})

	t.Run("bad duration in file", func(t *testing.T) {
		_, err := config.Load(writeConfig(t, `{"timeout":"soon"}`))
		require.Error(t, err)
	})

	t.Run("bad duration in env", func(t *testing.T) {
		t.Setenv(config.EnvTimeout, "soon")
		t.Chdir(t.TempDir())
		_, err := config.Load("")
		require.Error(t, err)
	})

	t.Run("non-numeric ledger threshold", func(t *testing.T) {
		t.Setenv(config.EnvWarnLedgers, "lots")
		t.Chdir(t.TempDir())
		_, err := config.Load("")
		require.Error(t, err)
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"empty rpc url", func(c *config.Config) { c.RPCURL = "" }, "rpc url is required"},
		{"rpc url without scheme", func(c *config.Config) { c.RPCURL = "rpc.example.com" }, "http://"},
		{"empty passphrase", func(c *config.Config) { c.NetworkPassphrase = "" }, "passphrase is required"},
		{"source account not a key", func(c *config.Config) { c.SourceAccount = "nope" }, "not a valid ed25519 public key"},
		{
			// A secret key would work as a string but must be refused: SoroProbe
			// never needs one and accepting it invites users to paste one in.
			"secret key rejected as source account",
			func(c *config.Config) {
				c.SourceAccount = "SAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
			},
			"not a valid ed25519 public key",
		},
		{"contract address rejected as source account", func(c *config.Config) {
			c.SourceAccount = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
		}, "not a valid ed25519 public key"},
		{"zero timeout", func(c *config.Config) { c.Timeout = 0 }, "timeout must be positive"},
		{"negative timeout", func(c *config.Config) { c.Timeout = -time.Second }, "timeout must be positive"},
		{"critical above warn", func(c *config.Config) { c.WarnLedgers = 10; c.CriticalLedgers = 100 }, "must not exceed"},
		{"per-kind critical above warn", func(c *config.Config) {
			warn, critical := uint32(10), uint32(100)
			c.ThresholdOverrides.Code = &health.ThresholdOverride{Warn: &warn, Critical: &critical}
		}, "code critical threshold"},
		{"unknown log level", func(c *config.Config) { c.LogLevel = "chatty" }, "unknown log level"},
		{"unknown log format", func(c *config.Config) { c.LogFormat = "xml" }, "unknown log format"},
		{
			// A token short enough to guess is no better than none, so the
			// server must refuse to start with one.
			"short api token",
			func(c *config.Config) { c.APIToken = "0123456789abcdef"[:config.MinAPITokenLength-1] },
			"use at least",
		},
		{"sorovault url without scheme", func(c *config.Config) { c.SoroVaultURL = "localhost:8080" }, "sorovault url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutate(&cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateAcceptsALongEnoughAPIToken(t *testing.T) {
	cfg := config.Default()
	cfg.APIToken = strings.Repeat("a", config.MinAPITokenLength)
	assert.NoError(t, cfg.Validate())

	cfg.APIToken = ""
	assert.NoError(t, cfg.Validate(), "no token configured keeps the API open, which is valid")
}

func TestValidateAllowsEqualThresholds(t *testing.T) {
	cfg := config.Default()
	cfg.WarnLedgers = 100
	cfg.CriticalLedgers = 100
	assert.NoError(t, cfg.Validate())
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{" info ", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := config.ParseLogLevel(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := config.ParseLogLevel("chatty")
	assert.Error(t, err)
}

func TestLoadInspectCacheTTL(t *testing.T) {
	path := writeConfig(t, `{"inspect_cache_ttl":"5s"}`)

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.InspectCacheTTL)
	assert.NoError(t, cfg.Validate())
}

func TestEnvInspectCacheTTL(t *testing.T) {
	t.Setenv(config.EnvInspectCacheTTL, "3s")
	t.Chdir(t.TempDir())

	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, cfg.InspectCacheTTL)
	assert.NoError(t, cfg.Validate())
}

func TestLoadRejectsMalformedInspectCacheTTL(t *testing.T) {
	_, err := config.Load(writeConfig(t, `{"inspect_cache_ttl":"soon"}`))
	require.Error(t, err)
}

func TestValidateRejectsNegativeInspectCacheTTL(t *testing.T) {
	cfg := config.Default()
	cfg.InspectCacheTTL = -time.Second

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inspect cache ttl")
}

func TestParseLogFormat(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"text", "text"},
		{"TEXT", "text"},
		{"json", "json"},
		{" JSON ", "json"},
		{"", "text"},
	}

	for _, tt := range tests {
		t.Run("["+tt.in+"]", func(t *testing.T) {
			got, err := config.ParseLogFormat(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	// An unknown format is rejected, and the error names the accepted values
	// so the operator does not have to go looking for them.
	_, err := config.ParseLogFormat("yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "text")
	assert.Contains(t, err.Error(), "json")
}

func TestLoggerJSONEmitsParseableLines(t *testing.T) {
	// The point of JSON mode is that a CI log aggregator can filter on
	// fields — contract, status — instead of regex-parsing the text handler's
	// output, so every line has to decode as a JSON object on its own.
	cfg := config.Default()
	cfg.LogFormat = "json"

	var buf bytes.Buffer
	log := cfg.Logger(&buf)
	log.Info("simulated", "contract", "CDLZ", "status", "success")
	log.Error("rpc unreachable", "url", "https://rpc.example.com")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2, "one object per line, no wrapping")

	var first, second map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first), "line was: %s", lines[0])
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second), "line was: %s", lines[1])

	assert.Equal(t, "simulated", first["msg"])
	assert.Equal(t, "CDLZ", first["contract"])
	assert.Equal(t, "success", first["status"])
	assert.Equal(t, "INFO", first["level"])
	assert.NotEmpty(t, first["time"])

	assert.Equal(t, "rpc unreachable", second["msg"])
	assert.Equal(t, "ERROR", second["level"])
}

func TestLoggerDefaultsToText(t *testing.T) {
	cfg := config.Default()

	var buf bytes.Buffer
	log := cfg.Logger(&buf)
	log.Info("inspected", "contract", "CDLZ")

	// The text handler's key=value shape, not a JSON object.
	assert.Contains(t, buf.String(), "msg=inspected")
	assert.Contains(t, buf.String(), "contract=CDLZ")
	assert.NotContains(t, buf.String(), `"msg"`)
}

func TestLoggerHonoursLogLevel(t *testing.T) {
	cfg := config.Default()
	cfg.LogFormat = "json"
	cfg.LogLevel = "error"

	var buf bytes.Buffer
	log := cfg.Logger(&buf)
	log.Info("suppressed")
	log.Error("kept")

	assert.NotContains(t, buf.String(), "suppressed")
	assert.Contains(t, buf.String(), "kept")
}
