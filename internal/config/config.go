// Package config resolves SoroProbe's runtime settings from defaults, an
// optional config file, and the environment.
//
// Precedence, lowest to highest: defaults, config file, environment. Command
// line flags sit above all of these and are applied by the caller (the CLI
// binds them directly onto the returned Config).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/stellar/go-stellar-sdk/network"
	"github.com/stellar/go-stellar-sdk/strkey"
	"go.yaml.in/yaml/v3"
)

// Defaults. These target the public testnet so that a fresh checkout can run
// the quickstart without any configuration at all.
const (
	DefaultRPCURL            = "https://soroban-testnet.stellar.org"
	DefaultNetworkPassphrase = network.TestNetworkPassphrase
	// DefaultHTTPAddr is loopback on purpose. SoroProbe is read-only, but an
	// instance reachable from the network still spends the operator's RPC
	// quota on whoever finds it, so serving wider than the local machine is an
	// opt-in rather than the default. The Docker image widens it, because
	// loopback inside a container would reach nothing.
	DefaultHTTPAddr                  = "127.0.0.1:8080"
	DefaultRateLimitPerMinute uint32 = 60
	DefaultRateLimitBurst     uint32 = 10
	DefaultLogLevel                  = "info"
	// DefaultLogFormat keeps logs human-readable; json is the opt-in for CI
	// and log aggregators, which filter on fields rather than regex.
	DefaultLogFormat = "text"
	DefaultTimeout   = 30 * time.Second

	// MinAPITokenLength is the shortest API token the HTTP API will accept.
	// An authenticated caller can spend the operator's RPC quota on demand, so
	// a token short enough to guess is not meaningfully different from no token
	// at all. The limit is a floor, not advice: use a long random value.
	MinAPITokenLength = 16

	// DefaultSourceAccount is the all-zero ed25519 account ID. Simulation reads
	// the source account but never needs it to exist, be funded, or sign
	// anything, so a placeholder is a safe default. Override it when a contract
	// authorizes on the invoker's address.
	DefaultSourceAccount = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

	// DefaultWarnLedgers is ~7 days at 5s per ledger.
	DefaultWarnLedgers = 120_960
	// DefaultCriticalLedgers is ~1 day at 5s per ledger.
	DefaultCriticalLedgers = 17_280
	// DefaultBaseFee is the network minimum base fee in stroops.
	DefaultBaseFee = int64(100)
)

// Environment variable names.
const (
	EnvRPCURL                  = "RPC_URL"
	EnvNetworkPassphrase       = "NETWORK_PASSPHRASE"
	EnvSourceAccount           = "SOURCE_ACCOUNT"
	EnvHTTPAddr                = "HTTP_ADDR"
	EnvCORSOrigins             = "CORS_ORIGINS"
	EnvLogLevel                = "LOG_LEVEL"
	EnvLogFormat               = "LOG_FORMAT"
	EnvTimeout                 = "RPC_TIMEOUT"
	EnvWarnLedgers             = "WARN_LEDGERS"
	EnvCriticalLedgers         = "CRITICAL_LEDGERS"
	EnvConfigFile              = "SOROPROBE_CONFIG"
	EnvSoroVaultURL            = "SOROVAULT_URL"
	EnvBaseFee                 = "BASE_FEE"
	EnvInspectCacheTTL         = "INSPECT_CACHE_TTL"
	EnvAPIToken                = "API_TOKEN"
	EnvInstanceWarnLedgers     = "INSTANCE_WARN_LEDGERS"
	EnvInstanceCriticalLedgers = "INSTANCE_CRITICAL_LEDGERS"
	EnvCodeWarnLedgers         = "CODE_WARN_LEDGERS"
	EnvCodeCriticalLedgers     = "CODE_CRITICAL_LEDGERS"
	EnvDataWarnLedgers         = "DATA_WARN_LEDGERS"
	EnvDataCriticalLedgers     = "DATA_CRITICAL_LEDGERS"
	EnvProfile                 = "SOROPROBE_PROFILE"
	EnvHTTPRateLimitPerMinute  = "HTTP_RATE_LIMIT_PER_MINUTE"
	EnvHTTPRateLimitBurst      = "HTTP_RATE_LIMIT_BURST"
	EnvHTTPTrustForwardedFor   = "HTTP_TRUST_FORWARDED_FOR"
	EnvHTTPTLSCert             = "HTTP_TLS_CERT"
	EnvHTTPTLSKey              = "HTTP_TLS_KEY"
)

// Config holds every setting SoroProbe needs.
type Config struct {
	// RPCURL is the Stellar RPC endpoint to query.
	RPCURL string `json:"rpc_url"`
	// NetworkPassphrase identifies the network transactions are built for.
	NetworkPassphrase string `json:"network_passphrase"`
	// SourceAccount is the public key used as the transaction source when
	// building an invocation to simulate. A secret key is never required.
	SourceAccount string `json:"source_account"`
	// HTTPAddr is the listen address for the HTTP API.
	HTTPAddr string `json:"http_addr"`
	// CORSOrigins is the exact-origin allow-list for the HTTP API.
	CORSOrigins            []string `json:"-"`
	HTTPRateLimitPerMinute uint32   `json:"http_rate_limit_per_minute"`
	HTTPRateLimitBurst     uint32   `json:"http_rate_limit_burst"`
	HTTPTrustForwardedFor  bool     `json:"http_trust_forwarded_for"`
	HTTPTLSCert            string   `json:"http_tls_cert"`
	HTTPTLSKey             string   `json:"http_tls_key"`
	// LogLevel is one of debug, info, warn, error.
	LogLevel string `json:"log_level"`
	// LogFormat selects the log handler: "text" for humans, "json" for log
	// aggregators that filter on fields instead of regex-parsing lines.
	LogFormat string `json:"log_format"`
	// Timeout bounds a single RPC request.
	Timeout time.Duration `json:"timeout"`
	// WarnLedgers is the remaining-TTL threshold, in ledgers, below which an
	// entry is reported as a warning.
	WarnLedgers uint32 `json:"warn_ledgers"`
	// CriticalLedgers is the remaining-TTL threshold, in ledgers, below which
	// an entry is reported as critical.
	CriticalLedgers uint32 `json:"critical_ledgers"`
	// ThresholdOverrides optionally adjusts TTL thresholds by entry kind.
	ThresholdOverrides health.ThresholdOverrides `json:"thresholds,omitempty"`
	// SoroVaultURL, when set, is a SoroVault registry whose recorded contract
	// interfaces type simulate arguments. Empty disables the lookup.
	SoroVaultURL string `json:"sorovault_url"`
	// InspectCacheTTL is how long `serve` may reuse an inspect result for the
	// same contract and key set. Zero, the default, disables the cache; a few
	// seconds suits dashboards, because TTLs change at most once per ledger.
	InspectCacheTTL time.Duration `json:"inspect_cache_ttl"`

	// APIToken, when set, requires "Authorization: Bearer <token>" on the
	// HTTP API's /v1 routes; /healthz stays open so a probe needs no
	// credential. Empty keeps the API unauthenticated. It is a secret, so it
	// is deliberately absent from the config file and the environment is the
	// place to set it (or the --api-token flag).
	APIToken string `json:"-"`

	// BaseFee is the base inclusion fee per operation in stroops.
	BaseFee int64 `json:"base_fee"`
}

// Default returns a Config populated entirely from the package defaults.
func Default() Config {
	return Config{
		RPCURL:                 DefaultRPCURL,
		NetworkPassphrase:      DefaultNetworkPassphrase,
		SourceAccount:          DefaultSourceAccount,
		HTTPAddr:               DefaultHTTPAddr,
		LogLevel:               DefaultLogLevel,
		LogFormat:              DefaultLogFormat,
		Timeout:                DefaultTimeout,
		WarnLedgers:            DefaultWarnLedgers,
		CriticalLedgers:        DefaultCriticalLedgers,
		BaseFee:                DefaultBaseFee,
		HTTPRateLimitPerMinute: DefaultRateLimitPerMinute,
		HTTPRateLimitBurst:     DefaultRateLimitBurst,
	}
}

// fileConfig mirrors Config but with pointers, so that "absent" is
// distinguishable from "set to the zero value". It can also contain named profiles.
type fileConfig struct {
	RPCURL                 *string                    `json:"rpc_url" yaml:"rpc_url"`
	NetworkPassphrase      *string                    `json:"network_passphrase" yaml:"network_passphrase"`
	SourceAccount          *string                    `json:"source_account" yaml:"source_account"`
	HTTPAddr               *string                    `json:"http_addr" yaml:"http_addr"`
	LogLevel               *string                    `json:"log_level" yaml:"log_level"`
	LogFormat              *string                    `json:"log_format" yaml:"log_format"`
	Timeout                *string                    `json:"timeout" yaml:"timeout"`
	WarnLedgers            *uint32                    `json:"warn_ledgers" yaml:"warn_ledgers"`
	CriticalLedgers        *uint32                    `json:"critical_ledgers" yaml:"critical_ledgers"`
	ThresholdOverrides     *health.ThresholdOverrides `json:"thresholds" yaml:"thresholds"`
	SoroVaultURL           *string                    `json:"sorovault_url" yaml:"sorovault_url"`
	BaseFee                *int64                     `json:"base_fee" yaml:"base_fee"`
	InspectCacheTTL        *string                    `json:"inspect_cache_ttl" yaml:"inspect_cache_ttl"`
	HTTPRateLimitPerMinute *uint32                    `json:"http_rate_limit_per_minute" yaml:"http_rate_limit_per_minute"`
	HTTPRateLimitBurst     *uint32                    `json:"http_rate_limit_burst" yaml:"http_rate_limit_burst"`
	HTTPTrustForwardedFor  *bool                      `json:"http_trust_forwarded_for" yaml:"http_trust_forwarded_for"`
	HTTPTLSCert            *string                    `json:"http_tls_cert" yaml:"http_tls_cert"`
	HTTPTLSKey             *string                    `json:"http_tls_key" yaml:"http_tls_key"`
	Profiles               map[string]*fileConfig     `json:"profiles" yaml:"profiles"`
	// APIToken is accepted so that a file carrying the key still loads, and
	// is never applied: the token is a secret and comes from API_TOKEN only.
	APIToken *string `json:"api_token" yaml:"api_token"`
}

// defaultCandidateFiles lists config filenames checked in order when no path is specified.
var defaultCandidateFiles = []string{
	"soroprobe.json",
	"soroprobe.yaml",
	"soroprobe.yml",
}

// Load builds a Config from defaults, then the config file at path (if
// non-empty, or if SOROPROBE_CONFIG names one), then the selected profile (if any),
// then the environment.
//
// A path given explicitly must exist; a path discovered via SOROPROBE_CONFIG
// must also exist. Only the implicit lookup of ./soroprobe.json, ./soroprobe.yaml,
// or ./soroprobe.yml is allowed to come up empty.
func Load(path string) (Config, error) {
	return LoadWithProfile(path, "")
}

// LoadWithProfile builds a Config from defaults, then the config file at path,
// then the profile named by profileName (or SOROPROBE_PROFILE if empty),
// then the environment.
func LoadWithProfile(path string, profileName string) (Config, error) {
	cfg := Default()

	if profileName == "" {
		profileName = os.Getenv(EnvProfile)
	}

	explicit := path != ""
	if !explicit {
		path = os.Getenv(EnvConfigFile)
		explicit = path != ""
	}
	if !explicit {
		for _, candidate := range defaultCandidateFiles {
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
				break
			}
		}
		if path == "" {
			// None found; if a profile was requested, report an error.
			if profileName != "" {
				return cfg, fmt.Errorf("profile %q requested but no config file found", profileName)
			}
			if err := applyEnv(&cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
	}

	if err := applyFileWithProfile(&cfg, path, explicit, profileName); err != nil {
		return cfg, err
	}
	if err := applyEnv(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func unmarshalConfigFile(path string, raw []byte, fc *fileConfig) error {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(fc); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(fc); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func applyValues(cfg *Config, path string, fc *fileConfig) error {
	setString(&cfg.RPCURL, fc.RPCURL)
	setString(&cfg.NetworkPassphrase, fc.NetworkPassphrase)
	setString(&cfg.SourceAccount, fc.SourceAccount)
	setString(&cfg.HTTPAddr, fc.HTTPAddr)
	if fc.HTTPRateLimitPerMinute != nil {
		cfg.HTTPRateLimitPerMinute = *fc.HTTPRateLimitPerMinute
	}
	if fc.HTTPRateLimitBurst != nil {
		cfg.HTTPRateLimitBurst = *fc.HTTPRateLimitBurst
	}
	if fc.HTTPTrustForwardedFor != nil {
		cfg.HTTPTrustForwardedFor = *fc.HTTPTrustForwardedFor
	}
	setString(&cfg.HTTPTLSCert, fc.HTTPTLSCert)
	setString(&cfg.HTTPTLSKey, fc.HTTPTLSKey)
	setString(&cfg.LogLevel, fc.LogLevel)
	setString(&cfg.LogFormat, fc.LogFormat)
	setString(&cfg.SoroVaultURL, fc.SoroVaultURL)
	if fc.WarnLedgers != nil {
		cfg.WarnLedgers = *fc.WarnLedgers
	}
	if fc.CriticalLedgers != nil {
		cfg.CriticalLedgers = *fc.CriticalLedgers
	}
	if fc.ThresholdOverrides != nil {
		cfg.ThresholdOverrides = *fc.ThresholdOverrides
	}
	if fc.BaseFee != nil {
		cfg.BaseFee = *fc.BaseFee
	}
	if fc.Timeout != nil {
		d, err := time.ParseDuration(*fc.Timeout)
		if err != nil {
			return fmt.Errorf("parse config file %s: timeout: %w", path, err)
		}
		cfg.Timeout = d
	}
	if fc.InspectCacheTTL != nil {
		d, err := time.ParseDuration(*fc.InspectCacheTTL)
		if err != nil {
			return fmt.Errorf("parse config file %s: inspect_cache_ttl: %w", path, err)
		}
		cfg.InspectCacheTTL = d
	}
	return nil
}

func applyFileWithProfile(cfg *Config, path string, required bool, profileName string) error {
	raw, err := os.ReadFile(path) //nolint:gosec // the config path comes from --config or SOROPROBE_CONFIG; reading it is the feature.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			if profileName != "" {
				return fmt.Errorf("profile %q requested but config file %s not found: %w", profileName, path, err)
			}
			return nil
		}
		return fmt.Errorf("read config file %s: %w", path, err)
	}

	var fc fileConfig
	if err := unmarshalConfigFile(path, raw, &fc); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}

	// 1. Apply top-level file keys
	if err := applyValues(cfg, path, &fc); err != nil {
		return err
	}

	// 2. Apply profile if specified
	if profileName != "" {
		pfc, ok := fc.Profiles[profileName]
		if !ok || pfc == nil {
			names := make([]string, 0, len(fc.Profiles))
			for name := range fc.Profiles {
				names = append(names, name)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return fmt.Errorf("unknown profile %q (no profiles defined in %s)", profileName, path)
			}
			return fmt.Errorf("unknown profile %q in %s (defined profiles: %s)", profileName, path, strings.Join(names, ", "))
		}
		if err := applyValues(cfg, path, pfc); err != nil {
			return err
		}
	}

	return nil
}

func applyEnv(cfg *Config) error {
	setString(&cfg.RPCURL, lookup(EnvRPCURL))
	setString(&cfg.NetworkPassphrase, lookup(EnvNetworkPassphrase))
	setString(&cfg.SourceAccount, lookup(EnvSourceAccount))
	setString(&cfg.HTTPAddr, lookup(EnvHTTPAddr))
	if err := setUint32FromEnv(&cfg.HTTPRateLimitPerMinute, EnvHTTPRateLimitPerMinute); err != nil {
		return err
	}
	if err := setUint32FromEnv(&cfg.HTTPRateLimitBurst, EnvHTTPRateLimitBurst); err != nil {
		return err
	}
	if v := lookup(EnvHTTPTrustForwardedFor); v != nil {
		trusted, err := strconv.ParseBool(*v)
		if err != nil {
			return fmt.Errorf("parse %s: %w", EnvHTTPTrustForwardedFor, err)
		}
		cfg.HTTPTrustForwardedFor = trusted
	}
	setString(&cfg.HTTPTLSCert, lookup(EnvHTTPTLSCert))
	setString(&cfg.HTTPTLSKey, lookup(EnvHTTPTLSKey))
	if origins := lookup(EnvCORSOrigins); origins != nil {
		cfg.CORSOrigins = nil
		for _, origin := range strings.Split(*origins, ",") {
			if origin = strings.TrimSpace(origin); origin != "" {
				cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
			}
		}
	}
	setString(&cfg.LogLevel, lookup(EnvLogLevel))
	setString(&cfg.LogFormat, lookup(EnvLogFormat))
	setString(&cfg.SoroVaultURL, lookup(EnvSoroVaultURL))
	setString(&cfg.APIToken, lookup(EnvAPIToken))

	if v := lookup(EnvTimeout); v != nil {
		d, err := time.ParseDuration(*v)
		if err != nil {
			return fmt.Errorf("parse %s: %w", EnvTimeout, err)
		}
		cfg.Timeout = d
	}
	if v := lookup(EnvInspectCacheTTL); v != nil {
		d, err := time.ParseDuration(*v)
		if err != nil {
			return fmt.Errorf("parse %s: %w", EnvInspectCacheTTL, err)
		}
		cfg.InspectCacheTTL = d
	}
	if err := setUint32FromEnv(&cfg.WarnLedgers, EnvWarnLedgers); err != nil {
		return err
	}
	if err := setUint32FromEnv(&cfg.CriticalLedgers, EnvCriticalLedgers); err != nil {
		return err
	}
	if err := setInt64FromEnv(&cfg.BaseFee, EnvBaseFee); err != nil {
		return err
	}
	return applyThresholdOverrideEnv(&cfg.ThresholdOverrides)
}

func setInt64FromEnv(dst *int64, key string) error {
	v := lookup(key)
	if v == nil {
		return nil
	}
	n, err := strconv.ParseInt(*v, 10, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*dst = n
	return nil
}

func applyThresholdOverrideEnv(overrides *health.ThresholdOverrides) error {
	for _, item := range []struct {
		kind     health.EntryKind
		warn     string
		critical string
	}{
		{health.EntryKindInstance, EnvInstanceWarnLedgers, EnvInstanceCriticalLedgers},
		{health.EntryKindCode, EnvCodeWarnLedgers, EnvCodeCriticalLedgers},
		{health.EntryKindData, EnvDataWarnLedgers, EnvDataCriticalLedgers},
	} {
		for _, field := range []struct {
			key string
			set func(*health.ThresholdOverride, *uint32)
		}{
			{item.warn, func(o *health.ThresholdOverride, n *uint32) { o.Warn = n }},
			{item.critical, func(o *health.ThresholdOverride, n *uint32) { o.Critical = n }},
		} {
			v := lookup(field.key)
			if v == nil {
				continue
			}
			n, err := strconv.ParseUint(*v, 10, 32)
			if err != nil {
				return fmt.Errorf("parse %s: %w", field.key, err)
			}
			value := uint32(n)
			field.set(thresholdOverride(overrides, item.kind), &value)
		}
	}
	return nil
}

func thresholdOverride(overrides *health.ThresholdOverrides, kind health.EntryKind) *health.ThresholdOverride {
	switch kind {
	case health.EntryKindInstance:
		if overrides.Instance == nil {
			overrides.Instance = &health.ThresholdOverride{}
		}
		return overrides.Instance
	case health.EntryKindCode:
		if overrides.Code == nil {
			overrides.Code = &health.ThresholdOverride{}
		}
		return overrides.Code
	default:
		if overrides.Data == nil {
			overrides.Data = &health.ThresholdOverride{}
		}
		return overrides.Data
	}
}

func setUint32FromEnv(dst *uint32, key string) error {
	v := lookup(key)
	if v == nil {
		return nil
	}
	n, err := strconv.ParseUint(*v, 10, 32)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*dst = uint32(n)
	return nil
}

func lookup(key string) *string {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return nil
	}
	return &v
}

func setString(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}

// Validate reports whether the configuration is usable.
func (c Config) Validate() error {
	if (c.HTTPTLSCert == "") != (c.HTTPTLSKey == "") {
		return errors.New("HTTP TLS certificate and key must be configured together")
	}
	if c.HTTPRateLimitPerMinute == 0 || c.HTTPRateLimitBurst == 0 {
		return errors.New("HTTP rate limit and burst must be positive")
	}
	if c.RPCURL == "" {
		return errors.New("rpc url is required")
	}
	if !strings.HasPrefix(c.RPCURL, "http://") && !strings.HasPrefix(c.RPCURL, "https://") {
		return fmt.Errorf("rpc url %q must start with http:// or https://", c.RPCURL)
	}
	if c.NetworkPassphrase == "" {
		return errors.New("network passphrase is required")
	}
	if !strkey.IsValidEd25519PublicKey(c.SourceAccount) {
		return fmt.Errorf("source account %q is not a valid ed25519 public key (G...)", c.SourceAccount)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive, got %s", c.Timeout)
	}
	if c.CriticalLedgers > c.WarnLedgers {
		return fmt.Errorf("critical threshold (%d) must not exceed warning threshold (%d)",
			c.CriticalLedgers, c.WarnLedgers)
	}
	if c.InspectCacheTTL < 0 {
		return fmt.Errorf("inspect cache ttl must not be negative, got %s", c.InspectCacheTTL)
	}
	global := health.Thresholds{Warn: c.WarnLedgers, Critical: c.CriticalLedgers}
	for _, kind := range []health.EntryKind{health.EntryKindInstance, health.EntryKindCode, health.EntryKindData} {
		thresholds := health.ResolveThresholds(kind, global, c.ThresholdOverrides)
		if thresholds.Critical > thresholds.Warn {
			return fmt.Errorf("%s critical threshold (%d) must not exceed warning threshold (%d)", kind, thresholds.Critical, thresholds.Warn)
		}
	}
	if _, err := ParseLogLevel(c.LogLevel); err != nil {
		return err
	}
	if _, err := ParseLogFormat(c.LogFormat); err != nil {
		return err
	}
	if c.APIToken != "" && len(c.APIToken) < MinAPITokenLength {
		return fmt.Errorf("api token is %d characters; use at least %d (e.g. \"openssl rand -hex 32\")",
			len(c.APIToken), MinAPITokenLength)
	}
	if c.SoroVaultURL != "" &&
		!strings.HasPrefix(c.SoroVaultURL, "http://") && !strings.HasPrefix(c.SoroVaultURL, "https://") {
		return fmt.Errorf("sorovault url %q must start with http:// or https://", c.SoroVaultURL)
	}
	return nil
}

// ParseLogLevel maps a log level name onto an slog.Level.
func ParseLogLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", name)
	}
}

// ParseLogFormat maps a log format name onto its canonical form, "text" or
// "json". The empty name means the default, text.
func ParseLogFormat(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "text", "":
		return "text", nil
	case "json":
		return "json", nil
	default:
		return "", fmt.Errorf("unknown log format %q (want text or json)", name)
	}
}

// Logger builds the process logger described by this config. Both formats
// write to w; json emits one parseable object per line so a CI log aggregator
// can filter on fields such as contract or status instead of regex-parsing
// the text handler's output.
func (c Config) Logger(w io.Writer) *slog.Logger {
	level, err := ParseLogLevel(c.LogLevel)
	if err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	// Validate rejects an unknown format before any command runs, so here a
	// parse failure falls back to the readable default rather than failing
	// a logging call.
	if format, err := ParseLogFormat(c.LogFormat); err == nil && format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
