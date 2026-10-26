package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/soroprobe/internal/probe"
)

func newEventsCmd(opts *options) *cobra.Command {
	var topic string
	var limit uint
	var startLedger uint32
	var cursor string

	cmd := &cobra.Command{
		Use:   "events <contract-id>",
		Short: "List a contract's recent events",
		Long: `Retrieve recent contract events from Stellar RPC, listed newest first.
Decodes topics and event data via scval.

--topic filters events by the first topic (e.g. sym:transfer).
--limit limits the number of events returned.
--cursor resumes paging using RPC's event cursor.
--start-ledger sets the starting ledger sequence; if the ledger is outside
the RPC endpoint's retention window, the retention window is explained.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, cleanup, err := opts.prober()
			if err != nil {
				return err
			}
			defer cleanup()

			contractID := args[0]
			res, err := p.Events(cmd.Context(), contractID, probe.EventsOptions{
				Topic:       topic,
				Limit:       limit,
				StartLedger: startLedger,
				Cursor:      cursor,
			})
			if err != nil {
				if startLedger > 0 && strings.Contains(strings.ToLower(err.Error()), "startledger") {
					return fmt.Errorf("%w (start ledger %d is outside RPC retention window)", err, startLedger)
				}
				return err
			}

			if opts.jsonOut {
				return writeJSON(cmd.OutOrStdout(), res)
			}

			return renderEvents(cmd.OutOrStdout(), res)
		},
	}

	cmd.Flags().StringVar(&topic, "topic", "", "filter by first topic (e.g. sym:transfer)")
	cmd.Flags().UintVar(&limit, "limit", 0, "maximum number of events to list")
	cmd.Flags().Uint32Var(&startLedger, "start-ledger", 0, "start ledger sequence")
	cmd.Flags().StringVar(&cursor, "cursor", "", "paging cursor from previous request")

	return cmd
}

func renderEvents(w io.Writer, res *probe.EventsResult) error {
	s := styler{on: colorEnabled(w)}
	bw := &bufWriter{w: w}

	bw.printf("%s %s\n", s.bold("contract"), res.ContractID)
	if res.RetentionWindow > 0 {
		bw.printf("%s %d ledgers (oldest: %d, latest: %d)\n",
			s.dim("retention window:"), res.RetentionWindow, res.OldestLedger, res.LatestLedger)
	}

	if len(res.Events) == 0 {
		bw.printf("\nno events found\n")
		return bw.err
	}

	bw.printf("\n%-10s %-12s %-25s %s\n",
		s.bold("LEDGER"), s.bold("ID"), s.bold("TOPICS"), s.bold("DATA"))

	for _, ev := range res.Events {
		var topicStrs []string
		for _, t := range ev.Topics {
			topicStrs = append(topicStrs, formatValue(t))
		}
		topicsDisplay := strings.Join(topicStrs, " ")
		dataDisplay := formatValue(ev.Data)

		bw.printf("%-10d %-12s %-25s %s\n",
			ev.Ledger, ev.ID, topicsDisplay, dataDisplay)
	}

	if res.Cursor != "" {
		bw.printf("\n%s %s\n", s.dim("cursor:"), res.Cursor)
	}

	return bw.err
}

func formatValue(v any) string {
	if v == nil {
		return "void"
	}
	switch val := v.(type) {
	case string:
		return val
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case uint32:
		return strconv.FormatUint(uint64(val), 10)
	case bool:
		return strconv.FormatBool(val)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}
