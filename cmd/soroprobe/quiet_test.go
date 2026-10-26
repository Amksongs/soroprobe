package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
)

func TestCheckQuietRejectsContradictoryFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		jsonOut    bool
		reportPath string
		wantErr    string
	}{
		{"quiet alone is fine", false, "", ""},
		{"the defaults are unaffected", false, "", ""},
		{"quiet with json is contradictory", true, "", "--json"},
		{"quiet with report is contradictory", false, "out.json", "--report"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkQuietConflicts(true, tt.jsonOut, tt.reportPath)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestCheckQuietConflictsIgnoresNonQuietRuns(t *testing.T) {
	t.Parallel()

	assert.NoError(t, checkQuietConflicts(false, true, "out.json"))
}

func TestQuietSuppressesTheSingleCheckReport(t *testing.T) {
	t.Parallel()

	result := &probe.CheckResult{ContractID: "CDLZ", OK: false, Checks: []probe.Check{
		{Name: "code_ttl", Outcome: probe.OutcomeFail, Detail: "expired"},
	}}

	cmd, _ := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, emitSingleCheckOutput(cmd, result, true, "text"))
	assert.Empty(t, out.String(), "--quiet must print no report at all")
}

func TestSingleCheckOutputPrintsWhenNotQuiet(t *testing.T) {
	t.Parallel()

	result := &probe.CheckResult{ContractID: "CDLZ", OK: true, Checks: []probe.Check{
		{Name: "deployed", Outcome: probe.OutcomePass, Detail: "found"},
	}}

	cmd, _ := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, emitSingleCheckOutput(cmd, result, false, "text"))
	assert.Contains(t, out.String(), "CDLZ")
}

func TestQuietCanBeSetOnTheCheckCommand(t *testing.T) {
	cmd, _ := newRootCmd()
	sub, rest, err := cmd.Find([]string{"check"})
	require.NoError(t, err)
	require.NoError(t, sub.ParseFlags(rest))

	flag := sub.Flags().Lookup("quiet")
	require.NotNil(t, flag, "check must accept --quiet")
	assert.Equal(t, "q", flag.Shorthand)

	// The contradictory pairs must fail before any network call.
	for _, args := range [][]string{
		{"check", "CDLZ", "--quiet", "--json"},
		{"check", "CDLZ", "--quiet", "--report", "out.json"},
		{"check", "--file", "-", "--quiet", "--json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			err := runWith(t, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--quiet")
		})
	}
}
