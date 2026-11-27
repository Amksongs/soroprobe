package probe

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/health"
)

// DefaultExtendLedgers is how far the rent-payer check prices an extension
// when none is given: about 30 days at five seconds a ledger.
const DefaultExtendLedgers uint32 = 518_400

// rentPayerStep checks that the account designated to pay rent can afford to
// extend the entries that are running low. A warning about an expiring entry
// is only actionable if someone can pay for the extension.
//
// The arithmetic is spelled out in the detail so an operator can verify it:
// the fee of each extension SoroProbe simulated, the payer's balance, and the
// reserve held back from it.
type rentPayerStep struct{ p *Prober }

func (s *rentPayerStep) Name() string { return "rent_payer" }

func (s *rentPayerStep) Run(ctx context.Context, req CheckRequest, inspect *InspectResult, _ *SimulateResult) ([]Check, error) {
	if req.RentPayer == "" {
		return nil, nil
	}
	const name = "rent_payer"

	acc, err := s.p.readAccount(ctx, req.RentPayer)
	if err != nil {
		return []Check{{Name: name, Outcome: OutcomeFail, Detail: err.Error()}}, nil
	}
	if acc == nil {
		return []Check{{Name: name, Outcome: OutcomeFail, Detail: fmt.Sprintf("rent payer %s does not exist on this network", req.RentPayer)}}, nil
	}
	spendable, minimum := spendableBalance(acc)

	// Only entries that are running low need extending now. Expired ones
	// need a restore first, which is a different operation and fee.
	needs := map[string]bool{}
	entries := append([]EntryReport{inspect.Instance}, inspect.Data...)
	if inspect.Code != nil {
		entries = append(entries, *inspect.Code)
	}
	for _, e := range entries {
		if e.Status == health.StatusWarning || e.Status == health.StatusCritical {
			needs[estimateID(e.Kind, e.Key)] = true
		}
	}

	var parts []string
	var total int64
	for _, est := range inspect.ExtendEstimates {
		if !needs[estimateID(est.Kind, est.Key)] || est.Note != "" {
			continue
		}
		fee := EstimateTotalFee(est.ResourceFee, s.p.baseFee)
		total += fee
		label := est.Kind
		if est.Key != "" {
			label += " " + est.Key
		}
		parts = append(parts, fmt.Sprintf("%s %s", label, formatXLM(fee)))
	}

	balanceDetail := fmt.Sprintf("payer %s has %s, minus %s reserve (%d base reserves of %s) and selling liabilities, leaving %s spendable",
		req.RentPayer, formatXLM(int64(acc.Balance)), formatXLM(minimum), minimum/baseReserveStroops,
		formatXLM(baseReserveStroops), formatXLM(spendable))

	if total == 0 {
		return []Check{{Name: name, Outcome: OutcomePass, Detail: "no entry needs extending now; " + balanceDetail}}, nil
	}
	detail := fmt.Sprintf("extending by %d ledgers costs about %s (%s); %s", req.ExtendLedgers, formatXLM(total), strings.Join(parts, " + "), balanceDetail)
	switch {
	case spendable < total:
		return []Check{{Name: name, Outcome: OutcomeFail, Detail: "cannot afford the extension: " + detail}}, nil
	case spendable < 2*total:
		return []Check{{Name: name, Outcome: OutcomeWarn, Detail: "affords the extension with less than twice the cost to spare: " + detail}}, nil
	}
	return []Check{{Name: name, Outcome: OutcomePass, Detail: detail}}, nil
}

func estimateID(kind, key string) string { return kind + "\x00" + key }

// baseReserveStroops is the network base reserve, 0.5 XLM. It has been the
// value since protocol 11 but is a network setting RPC does not report, so
// the rent-payer check states it in its output rather than hiding it.
const baseReserveStroops int64 = 5_000_000

// spendableBalance is an account's balance minus its minimum balance and its
// selling liabilities, in stroops. It can be negative.
func spendableBalance(acc *xdr.AccountEntry) (spendable, minimum int64) {
	var sponsoring, sponsored, selling int64
	if v1, ok := acc.Ext.GetV1(); ok {
		selling = int64(v1.Liabilities.Selling)
		if v2, ok := v1.Ext.GetV2(); ok {
			sponsoring, sponsored = int64(v2.NumSponsoring), int64(v2.NumSponsored)
		}
	}
	minimum = (2 + int64(acc.NumSubEntries) + sponsoring - sponsored) * baseReserveStroops
	return int64(acc.Balance) - minimum - selling, minimum
}

// formatXLM renders stroops as XLM with up to seven decimals.
func formatXLM(stroops int64) string {
	return FormatUnits(bigInt(stroops), 7) + " XLM"
}

func bigInt(n int64) *big.Int { return big.NewInt(n) }
