package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/config"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

// fakeSoroForge serves a fixed contract list and records the bearer token it
// was given, so a test can prove the CLI authenticated the request.
func fakeSoroForge(t *testing.T, contracts []map[string]any) (*httptest.Server, *string) {
	t.Helper()

	var token string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("Authorization")
		assert.Equal(t, "/v1/contracts", r.URL.Path)
		assert.Equal(t, "testnet", r.URL.Query().Get("network"))
		w.Header().Set("Content-Type", "application/json")
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"contracts": contracts,
			"limit":     100,
			"offset":    0,
			"has_more":  false,
		}))
	}))
	t.Cleanup(server.Close)
	return server, &token
}

func soroForgeContract(alias, id, hash string) map[string]any {
	return map[string]any{
		"alias":             alias,
		"network":           "testnet",
		"contract_id":       id,
		"current_wasm_hash": hash,
	}
}

func liveWasmHash(t *testing.T, fake *stellartest.Fake) string {
	t.Helper()

	p, err := probe.New(probe.Options{Client: fake, SourceAccount: stellartest.SourceAccount})
	require.NoError(t, err)
	_, hash, err := p.Code(context.Background(), stellartest.WasmContract)
	require.NoError(t, err)
	return hash
}

func TestCheckFromSoroForgeChecksEveryTrackedContract(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "read-only-token")

	fake := stellartest.NewFake(t)
	hash := liveWasmHash(t, fake)
	server, token := fakeSoroForge(t, []map[string]any{
		soroForgeContract("vault", stellartest.WasmContract, hash),
	})

	out, err := runWithFake(t, fake, "check", "--from-soroforge", server.URL, "--network", "testnet")
	require.NoError(t, err)

	assert.Equal(t, "Bearer read-only-token", *token)
	assert.Contains(t, out, "vault")
	assert.Contains(t, out, "PASS")
	assert.Contains(t, out, "1 passed, 0 failed")
}

func TestCheckFromSoroForgeReportsDrift(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "read-only-token")

	fake := stellartest.NewFake(t)
	hash := liveWasmHash(t, fake)
	recorded := strings.Repeat("0", len(hash))
	server, _ := fakeSoroForge(t, []map[string]any{
		soroForgeContract("vault", stellartest.WasmContract, recorded),
	})

	out, err := runWithFake(t, fake, "check", "--from-soroforge", server.URL, "--network", "testnet")
	assertExitCodeOne(t, err)
	assert.Contains(t, out, "FAIL")
	assert.Contains(t, out, "drift")
	assert.Contains(t, out, "1 failed, 0 could not run")
}

func TestCheckFromSoroForgeAcceptsTheTokenFlag(t *testing.T) {
	fake := stellartest.NewFake(t)
	hash := liveWasmHash(t, fake)
	server, token := fakeSoroForge(t, []map[string]any{
		soroForgeContract("vault", stellartest.WasmContract, hash),
	})

	_, err := runWithFake(t, fake, "check", "--from-soroforge", server.URL, "--network", "testnet",
		"--soroforge-token", "from-the-flag")
	require.NoError(t, err)
	assert.Equal(t, "Bearer from-the-flag", *token)
}

func TestCheckFromSoroForgeJSONCarriesTheWasmHashCheck(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "read-only-token")

	fake := stellartest.NewFake(t)
	hash := liveWasmHash(t, fake)
	recorded := strings.Repeat("0", len(hash))
	server, _ := fakeSoroForge(t, []map[string]any{
		soroForgeContract("vault", stellartest.WasmContract, recorded),
	})

	out, err := runWithFake(t, fake, "check", "--from-soroforge", server.URL, "--network", "testnet", "--json")
	assertExitCodeOne(t, err)

	var result probe.BatchResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.Len(t, result.Items, 1)
	require.NotNil(t, result.Items[0].Result)

	check := findResultCheck(result.Items[0].Result.Checks, "wasm_hash")
	require.NotNil(t, check)
	assert.Equal(t, probe.OutcomeFail, check.Outcome)
	assert.Contains(t, check.Detail, "drift")
}

func findResultCheck(checks []probe.Check, name string) *probe.Check {
	for i := range checks {
		if checks[i].Name == name {
			return &checks[i]
		}
	}
	return nil
}

func TestCheckFromSoroForgeNeedsAToken(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "")

	err := runWith(t, "check", "--from-soroforge", "http://127.0.0.1:1", "--network", "testnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only token")

	var failed *checkFailedError
	assert.False(t, errors.As(err, &failed), "a missing token is a tool failure, not a failed check")
}

func TestCheckFromSoroForgeReportsAnAuthFailureAsCouldNotRun(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "expired-token")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, err := w.Write([]byte(`{"error": "invalid bearer token"}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)

	err := runWith(t, "check", "--from-soroforge", server.URL, "--network", "testnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rejected the token")
	assert.Contains(t, err.Error(), "invalid bearer token")

	var failed *checkFailedError
	assert.False(t, errors.As(err, &failed), "an auth failure is not a failed check")
}

func TestCheckFromSoroForgeRejectsAnEmptyContractList(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "read-only-token")

	server, _ := fakeSoroForge(t, []map[string]any{})
	err := runWith(t, "check", "--from-soroforge", server.URL, "--network", "testnet")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tracks no contracts")
	assert.Contains(t, err.Error(), "testnet")
}

func TestCheckFromSoroForgeRejectsConflictingFlags(t *testing.T) {
	t.Setenv(config.EnvSoroForgeToken, "read-only-token")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a contract ID",
			args: []string{"check", "--from-soroforge", "http://localhost:8080", stellartest.WasmContract},
			want: "not both",
		},
		{
			name: "a check file",
			args: []string{"check", "--from-soroforge", "http://localhost:8080", "--file", "checks.json"},
			want: "not both",
		},
		{
			name: "a function probe",
			args: []string{"check", "--from-soroforge", "http://localhost:8080", "--fn", "decimals"},
			want: "apply to a single contract",
		},
		{
			name: "a data key",
			args: []string{"check", "--from-soroforge", "http://localhost:8080", "--key", "sym:Admin"},
			want: "apply to a single contract",
		},
		{
			name: "a durability",
			args: []string{"check", "--from-soroforge", "http://localhost:8080", "--durability", "temporary"},
			want: "apply to a single contract",
		},
		{
			name: "--network without --from-soroforge",
			args: []string{"check", stellartest.WasmContract, "--network", "testnet"},
			want: "--network selects a SoroForge network",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := runWith(t, tc.args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
