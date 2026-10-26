package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func TestEventsCommand(t *testing.T) {
	fake := stellartest.NewFake(t)

	t.Run("human readable events output", func(t *testing.T) {
		out, err := runWithFake(t, fake, "events", stellartest.SACContract)
		require.NoError(t, err)

		assert.Contains(t, out, "contract "+stellartest.SACContract)
		assert.Contains(t, out, "LEDGER")
		assert.Contains(t, out, "TOPICS")
		assert.Contains(t, out, "DATA")
		assert.Contains(t, out, "voted")
		assert.Contains(t, out, "transfer")
	})

	t.Run("json events output", func(t *testing.T) {
		out, err := runWithFake(t, fake, "--json", "events", stellartest.SACContract)
		require.NoError(t, err)

		var res probe.EventsResult
		require.NoError(t, json.Unmarshal([]byte(out), &res))
		assert.Equal(t, stellartest.SACContract, res.ContractID)
		assert.Len(t, res.Events, 2)
		assert.Equal(t, int32(1002), res.Events[0].Ledger)
	})

	t.Run("filter by topic", func(t *testing.T) {
		out, err := runWithFake(t, fake, "--json", "events", stellartest.SACContract, "--topic", "sym:transfer")
		require.NoError(t, err)

		var res probe.EventsResult
		require.NoError(t, json.Unmarshal([]byte(out), &res))
		assert.Len(t, res.Events, 1)
		assert.Equal(t, "transfer", res.Events[0].Topics[0])
	})

	t.Run("filter by limit", func(t *testing.T) {
		out, err := runWithFake(t, fake, "--json", "events", stellartest.SACContract, "--limit", "1")
		require.NoError(t, err)

		var res probe.EventsResult
		require.NoError(t, json.Unmarshal([]byte(out), &res))
		assert.Len(t, res.Events, 1)
	})
}
