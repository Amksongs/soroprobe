package probe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
	"github.com/soroworks/soroprobe/internal/probe"
	"github.com/soroworks/soroprobe/internal/stellar/stellartest"
)

func upgradeIface(names ...string) *abi.Interface {
	iface := &abi.Interface{}
	for _, n := range names {
		iface.Functions = append(iface.Functions, abi.Function{Name: n})
	}
	return iface
}

type fixedABI struct{ iface *abi.Interface }

func (f fixedABI) Interface(context.Context, string, string) (*abi.Interface, error) {
	return f.iface, nil
}

func TestUpgradeabilityReport(t *testing.T) {
	admin := accountID(t, "a")
	fake := adminFake(t, admin)
	// Make the code entry's last-modified ledger known.
	for k, e := range fake.Entries {
		if e.LiveUntilLedgerSeq != nil {
			e.LastModifiedLedger = fake.LatestLedger - 10
			fake.Entries[k] = e
		}
	}
	p := standardProber(t, fake, fixedABI{upgradeIface("admin", "upgrade", "get")})
	checks := runCheck(t, p, probe.CheckRequest{Upgradeability: true})

	assert.Equal(t, probe.OutcomeWarn, checks["upgradeability:interface"].Outcome)
	assert.Contains(t, checks["upgradeability:interface"].Detail, "upgrade")
	assert.Contains(t, checks["upgradeability:interface"].Detail, "best effort")
	assert.Equal(t, probe.OutcomePass, checks["upgradeability:admin"].Outcome)
	assert.Contains(t, checks["upgradeability:admin"].Detail, "instance storage sym:admin")
	assert.Contains(t, checks["upgradeability:code_modified"].Detail, "last modified at ledger")
}

// bareContract deploys a Wasm contract with no instance storage, so no admin
// can be found by convention.
func bareContract(t *testing.T) *stellartest.Fake {
	t.Helper()
	fake := stellartest.NewFake(t)
	fake.DeployWasm(t, stellartest.UndeployedContract, []byte("wasm"))
	return fake
}

func runBare(t *testing.T, p *probe.Prober, req probe.CheckRequest) map[string]probe.Check {
	t.Helper()
	req.ContractID = stellartest.UndeployedContract
	res, err := p.Check(context.Background(), req)
	require.NoError(t, err)
	return checksByName(res)
}

func TestUpgradeabilityNoUpgradeFunction(t *testing.T) {
	p := standardProber(t, bareContract(t), fixedABI{upgradeIface("get")})
	checks := runBare(t, p, probe.CheckRequest{Upgradeability: true})
	assert.Equal(t, probe.OutcomePass, checks["upgradeability:interface"].Outcome)
	assert.Equal(t, probe.OutcomeSkip, checks["upgradeability:admin"].Outcome)
}

func TestUpgradeabilityUnknownAdminWarnsWhenUpgradable(t *testing.T) {
	p := standardProber(t, bareContract(t), fixedABI{upgradeIface("update_current_contract_wasm")})
	checks := runBare(t, p, probe.CheckRequest{Upgradeability: true})
	assert.Equal(t, probe.OutcomeWarn, checks["upgradeability:admin"].Outcome)
}

func TestUpgradeabilitySkippedForSAC(t *testing.T) {
	p := networkProber(t, stellartest.NewFake(t), "")
	res, err := p.Check(context.Background(), probe.CheckRequest{ContractID: stellartest.SACContract, Upgradeability: true})
	require.NoError(t, err)
	assert.Equal(t, probe.OutcomeSkip, checksByName(res)["upgradeability"].Outcome)
}
