package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/health"
	"github.com/soroworks/soroprobe/internal/probe"
)

func TestRenderSimulateSuccess(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		ContractID:   "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
		Function:     "decimals",
		Success:      true,
		ReturnValue:  uint32(7),
		Cost:         probe.Cost{Instructions: 127569, MinResourceFee: 12279, ResourceFee: 12279, EstimatedTotalFee: 12379},
		Footprint:    probe.Footprint{ReadOnly: []string{"contract instance CDLZ... (persistent)"}},
		LatestLedger: 4014125,
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "SUCCESS")
	assert.Contains(t, out, "decimals")
	assert.Contains(t, out, "7")
	assert.Contains(t, out, "127569")
	assert.Contains(t, out, "4014125")
	// Fees are shown in both stroops and XLM, since neither alone is obvious.
	assert.Contains(t, out, "12279 stroops")
	assert.Contains(t, out, "0.0012279 XLM")
	assert.Contains(t, out, "estimated total fee")
	assert.Contains(t, out, "12379 stroops")
	assert.Contains(t, out, "0.0012379 XLM")
}

func TestRenderSimulateShowsAuth(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		ContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
		Function:   "transfer",
		Success:    true,
		Auth: []probe.AuthEntry{
			{
				Address:         "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF",
				IsSourceAccount: true,
				RootInvocation: &probe.AuthorizedInvocation{
					ContractID: "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
					Function:   "transfer",
					Args:       []any{"GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", "GB...", "100"},
				},
			},
			{
				RawXDR: "AAAA...rawXDR",
			},
		},
		Cost:         probe.Cost{EstimatedTotalFee: 12379},
		LatestLedger: 4014125,
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "authorizations")
	assert.Contains(t, out, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	assert.Contains(t, out, "(source account)")
	assert.Contains(t, out, "transfer")
	assert.Contains(t, out, "raw xdr:")
	assert.Contains(t, out, "AAAA...rawXDR")
}

func TestRenderSimulateShowsInterfaceTyping(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		ContractID:  "CDLZ",
		Function:    "set_limit",
		Args:        []string{"5"},
		Signature:   "fn set_limit(count: u32)",
		EncodedArgs: []string{"u32:5"},
		Success:     true,
	})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "fn set_limit(count: u32)")
	assert.Contains(t, out, "u32:5")

	buf.Reset()
	err = renderSimulate(&buf, &probe.SimulateResult{
		ContractID: "CDLZ",
		Function:   "decimals",
		ABINote:    "contract not found in the interface registry; argument types were inferred",
		Success:    true,
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "note: contract not found")
}

func TestRenderSimulateFailureIndentsMultilineError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		ContractID: "CDLZ",
		Function:   "no_such_fn",
		Success:    false,
		Error:      "HostError: Error(Value, InvalidInput)\n\nEvent log:\n  0: something",
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "FAILED")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "HostError") || strings.Contains(line, "Event log") {
			assert.True(t, strings.HasPrefix(line, "  "), "error lines should be indented: %q", line)
		}
	}
	// A failed call has no return value to show.
	assert.NotContains(t, out, "return value")
}

func TestRenderSimulateRestoreRequired(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		ContractID:      "CDLZ",
		Function:        "balance",
		Success:         true,
		RestoreRequired: &probe.RestoreInfo{MinResourceFee: 5000},
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "RESTORE REQUIRED")
	assert.Contains(t, out, "archived")
	assert.Contains(t, out, "5000 stroops")
}

func TestRenderSimulateVoidReturn(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		ContractID: "CDLZ", Function: "noop", Success: true, ReturnValue: nil,
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "(void)")
}

func TestRenderInspectWasmContract(t *testing.T) {
	t.Parallel()

	liveUntil := uint32(4073187)
	entry := func(status health.Status, summary string) probe.EntryReport {
		return probe.EntryReport{
			Kind: "instance",
			TTL: health.TTL{
				Present: true, Status: status, LiveUntilLedger: &liveUntil, LedgersRemaining: 59100,
			},
			Summary: summary,
		}
	}
	code := entry(health.StatusWarning, "live for 24456 more ledgers")

	var buf bytes.Buffer
	err := renderInspect(&buf, &probe.InspectResult{
		ContractID:   "CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4",
		Deployed:     true,
		Executable:   "wasm",
		WasmHash:     "8608bb30bc798a05aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Instance:     entry(health.StatusWarning, "live for 59100 more ledgers"),
		Code:         &code,
		Status:       health.StatusWarning,
		LatestLedger: 4014125,
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "wasm")
	assert.Contains(t, out, "instance")
	assert.Contains(t, out, "code")
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "59100")
	assert.Contains(t, out, "24456")
}

func TestRenderInspectStellarAssetContractExplainsMissingCode(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderInspect(&buf, &probe.InspectResult{
		ContractID: "CDLZ",
		Deployed:   true,
		Executable: "stellar_asset",
		Instance: probe.EntryReport{
			TTL: health.TTL{Present: true, Status: health.StatusHealthy}, Summary: "live",
		},
		Code:   nil,
		Status: health.StatusHealthy,
	})
	require.NoError(t, err)

	// A missing code entry must be explained, not left as a blank.
	assert.Contains(t, buf.String(), "built into the host")
}

func TestRenderInspectNotDeployed(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderInspect(&buf, &probe.InspectResult{
		ContractID: "CCV2",
		Deployed:   false,
		Instance:   probe.EntryReport{TTL: health.TTL{Status: health.StatusMissing}},
		Status:     health.StatusMissing,
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "NOT DEPLOYED")
	assert.NotContains(t, out, "instance storage")
}

func TestRenderCheck(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderCheck(&buf, &probe.CheckResult{
		ContractID: "CDLZ",
		OK:         false,
		Checks: []probe.Check{
			{Name: "deployed", Outcome: probe.OutcomePass, Detail: "instance found"},
			{Name: "instance_ttl", Outcome: probe.OutcomeWarn, Detail: "expiring soon"},
			{Name: "code_ttl", Outcome: probe.OutcomeSkip, Detail: "no code entry"},
			{Name: "simulate", Outcome: probe.OutcomeFail, Detail: "call failed"},
		},
		LatestLedger: 4014125,
	})
	require.NoError(t, err)

	out := buf.String()
	for _, want := range []string{"PASS", "WARN", "SKIP", "FAIL", "deployed", "instance_ttl", "4014125"} {
		assert.Contains(t, out, want)
	}
}

func TestJSONOutputIsMachineReadable(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	require.NoError(t, writeJSON(&buf, &probe.CheckResult{ContractID: "CDLZ", OK: true}))

	var round map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &round))
	assert.Equal(t, "CDLZ", round["contract_id"])
	assert.Equal(t, true, round["ok"])
}

func TestColorIsDisabledForNonTerminals(t *testing.T) {
	t.Parallel()

	// Output redirected to a file or a pipe, as in CI, must stay free of
	// escape codes.
	var buf bytes.Buffer
	err := renderCheck(&buf, &probe.CheckResult{
		ContractID: "CDLZ",
		OK:         true,
		Checks:     []probe.Check{{Name: "deployed", Outcome: probe.OutcomePass, Detail: "ok"}},
	})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "\033[")
}

func TestStroopsConversion(t *testing.T) {
	t.Parallel()

	assert.Contains(t, stroops(10_000_000), "1.0000000 XLM")
	assert.Contains(t, stroops(1), "0.0000001 XLM")
	assert.Contains(t, stroops(0), "0 stroops")
}

func TestIndent(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "  a\n  b", indent("a\nb", "  "))
	assert.Equal(t, "  a", indent("a\n", "  "))
}

func TestParseDurability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    health.Durability
		wantErr bool
	}{
		{"", health.DurabilityPersistent, false},
		{"persistent", health.DurabilityPersistent, false},
		{"PERSISTENT", health.DurabilityPersistent, false},
		{"temporary", health.DurabilityTemporary, false},
		{"temp", health.DurabilityTemporary, false},
		{"forever", "", true},
	}

	for _, tt := range tests {
		got, err := parseDurability(tt.in)
		if tt.wantErr {
			require.Error(t, err, "input %q", tt.in)
			continue
		}
		require.NoError(t, err, "input %q", tt.in)
		assert.Equal(t, tt.want, got)
	}
}

func TestResolveVersionAlwaysReturnsSomething(t *testing.T) {
	assert.NotEmpty(t, resolveVersion())
}

func TestReadBatch(t *testing.T) {
	t.Parallel()

	req, err := readBatch(strings.NewReader(`{"checks": [
		{"name": "token", "contract_id": "CDLZ", "function": "decimals", "args": ["u32:1"], "timeout": "15s"},
		{"contract_id": "CDEF", "data_keys": ["sym:Admin"], "data_durability": "temporary"}
	]}`), "-")
	require.NoError(t, err)
	require.Len(t, req.Checks, 2)
	assert.Equal(t, "token", req.Checks[0].Name)
	assert.Equal(t, "decimals", req.Checks[0].Function)
	assert.Equal(t, []string{"u32:1"}, req.Checks[0].Args)
	assert.Equal(t, "15s", req.Checks[0].Timeout)
	assert.Empty(t, req.Checks[1].Timeout, "no timeout means no per-check limit")
	assert.Equal(t, []string{"sym:Admin"}, req.Checks[1].DataKeys)
	assert.EqualValues(t, "temporary", req.Checks[1].DataDurability)

	// "fn" is the CLI flag name, not the file key; a typo must not silently
	// drop the simulation step.
	_, err = readBatch(strings.NewReader(`{"checks": [{"contract_id": "CDLZ", "fn": "decimals"}]}`), "-")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown field "fn"`)
}

func TestReadYAMLBatchMatchesJSONAndRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	path := t.TempDir() + "/checks.yaml"
	yaml := []byte("checks:\n  - name: token\n    contract_id: CDLZ\n    function: decimals\n    args: [u32:1]\n    data_keys: [sym:Admin]\n")
	require.NoError(t, os.WriteFile(path, yaml, 0600))
	req, err := readBatch(strings.NewReader(""), path)
	require.NoError(t, err)
	require.Len(t, req.Checks, 1)
	assert.Equal(t, "token", req.Checks[0].Name)
	assert.Equal(t, "CDLZ", req.Checks[0].ContractID)
	assert.Equal(t, "decimals", req.Checks[0].Function)
	assert.Equal(t, []string{"u32:1"}, req.Checks[0].Args)
	assert.Equal(t, []string{"sym:Admin"}, req.Checks[0].DataKeys)

	path = t.TempDir() + "/checks.yml"
	require.NoError(t, os.WriteFile(path, []byte("checks:\n  - contract_id: CDLZ\n    fn: decimals\n"), 0600))
	_, err = readBatch(strings.NewReader(""), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field fn not found")
}

func TestApplyDefaultTimeout(t *testing.T) {
	t.Parallel()

	req := probe.BatchRequest{Checks: []probe.NamedCheck{
		{Name: "keeps", Timeout: "2s"},
		{Name: "fills"},
	}}
	applyDefaultTimeout(&req, 30*time.Second)
	assert.Equal(t, "2s", req.Checks[0].Timeout, "a per-entry timeout wins over the flag")
	assert.Equal(t, "30s", req.Checks[1].Timeout)

	// Without the flag there is no default, and entries stay unbounded.
	req = probe.BatchRequest{Checks: []probe.NamedCheck{{Name: "unbounded"}}}
	applyDefaultTimeout(&req, 0)
	assert.Empty(t, req.Checks[0].Timeout)
}

func TestRenderBatch(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderBatch(&buf, &probe.BatchResult{
		OK:      false,
		Errored: true,
		Items: []probe.BatchItem{
			{Name: "token", Result: &probe.CheckResult{OK: true, Checks: []probe.Check{
				{Name: "instance_ttl", Outcome: probe.OutcomeWarn, Detail: "live for 100 more ledgers"},
			}}},
			{Name: "vault", Result: &probe.CheckResult{OK: false, Checks: []probe.Check{
				{Name: "deployed", Outcome: probe.OutcomePass, Detail: "instance found"},
				{Name: "code_ttl", Outcome: probe.OutcomeFail, Detail: "expired"},
			}}},
			{Name: "broken", Error: "invalid contract id"},
		},
	})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "PASS   token   instance_ttl: live for 100 more ledgers")
	assert.Contains(t, out, "FAIL   vault   code_ttl: expired")
	assert.Contains(t, out, "ERROR  broken  invalid contract id")
	assert.Contains(t, out, "1 passed, 1 failed, 1 could not run")
}

func TestRenderMarkdownCheckGolden(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	err := renderMarkdownCheck(&buf, &probe.CheckResult{
		ContractID: "C|`token",
		OK:         false,
		Checks: []probe.Check{
			{Name: "instance_ttl", Outcome: probe.OutcomeFail, Detail: "expired | restore `now`\nsecond line"},
		},
	})
	require.NoError(t, err)

	want, err := os.ReadFile("testdata/check_markdown.golden")
	require.NoError(t, err)
	assert.Equal(t, string(want)+"\n", buf.String())
}

func TestRenderMarkdownBatchEscapesContractAndError(t *testing.T) {
	var buf bytes.Buffer
	err := renderMarkdownBatch(&buf, &probe.BatchResult{Items: []probe.BatchItem{{
		Name:       "broken",
		ContractID: "C|`one",
		Error:      "bad | `value`\nsecond line",
	}}})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "| broken | C\\|\\`one | ERROR | bad \\| \\`value\\`<br>second line |")
}

func TestRenderMarkdownInspect(t *testing.T) {
	var buf bytes.Buffer
	err := renderMarkdownInspect(&buf, &probe.InspectResult{
		ContractID: "C|`one",
		Deployed:   true,
		Instance:   probe.EntryReport{TTL: health.TTL{Status: health.StatusHealthy}, Summary: "instance found"},
		Data:       []probe.EntryReport{{Key: "key|one", TTL: health.TTL{Status: health.StatusWarning}, Summary: "expires soon"}},
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Contract: C\\|\\`one")
	assert.Contains(t, buf.String(), "| Data key\\|one | warning | expires soon |")
}

func TestRenderSimulateRestoreRequiredListsArchivedEntries(t *testing.T) {
	var buf bytes.Buffer
	err := renderSimulate(&buf, &probe.SimulateResult{
		RestoreRequired: &probe.RestoreInfo{
			MinResourceFee:  2,
			ArchivedEntries: []string{"contract data C... (persistent)"},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "archived: contract data C... (persistent)")
}
