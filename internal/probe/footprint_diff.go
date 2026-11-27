package probe

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/scval"
)

// Footprint access modes.
const (
	AccessReadOnly  = "read_only"
	AccessReadWrite = "read_write"
)

// selfContract stands for "the contract being called" in a footprint entry,
// so two contracts' footprints line up entry for entry.
const selfContract = "self"

// FootprintEntry is one ledger entry a call touches, described in human
// terms.
type FootprintEntry struct {
	// Access is AccessReadOnly or AccessReadWrite.
	Access string `json:"access"`
	// Kind is "instance", "data", "code", "account", "trustline" or the
	// ledger entry type.
	Kind string `json:"kind"`
	// Contract owns an instance or data entry. It is "self" for the
	// contract the call was made to.
	Contract string `json:"contract,omitempty"`
	// Key is the decoded key of a data entry.
	Key any `json:"key,omitempty"`
	// Durability is "persistent" or "temporary" for contract data.
	Durability string `json:"durability,omitempty"`
	// Hash is the Wasm hash of a code entry, in hex.
	Hash string `json:"hash,omitempty"`
	// Account is the account of an account or trustline entry.
	Account string `json:"account,omitempty"`
	// Label is the entry as one line of text.
	Label string `json:"label"`
}

// identity is what two entries must share to be the same entry.
func (e FootprintEntry) identity() string {
	key, _ := json.Marshal(e.Key)
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", e.Kind, e.Contract, key, e.Durability, e.Hash, e.Account)
}

// describeFootprintEntries describes every footprint entry, naming the
// called contract "self".
func describeFootprintEntries(fp xdr.LedgerFootprint, self string) []FootprintEntry {
	out := make([]FootprintEntry, 0, len(fp.ReadOnly)+len(fp.ReadWrite))
	for _, key := range fp.ReadOnly {
		out = append(out, describeEntry(key, AccessReadOnly, self))
	}
	for _, key := range fp.ReadWrite {
		out = append(out, describeEntry(key, AccessReadWrite, self))
	}
	return out
}

func describeEntry(key xdr.LedgerKey, access, self string) FootprintEntry {
	e := FootprintEntry{Access: access}
	switch key.Type {
	case xdr.LedgerEntryTypeContractData:
		cd := key.ContractData
		addr, err := cd.Contract.String()
		if err != nil {
			addr = "<unknown contract>"
		}
		if addr == self {
			addr = selfContract
		}
		e.Contract = addr
		e.Durability = durabilityName(cd.Durability)
		if cd.Key.Type == xdr.ScValTypeScvLedgerKeyContractInstance {
			e.Kind = "instance"
			e.Label = fmt.Sprintf("instance of %s", addr)
			return e
		}
		e.Kind = "data"
		decoded, err := scval.Decode(cd.Key)
		if err != nil {
			decoded = cd.Key.Type.String()
		}
		e.Key = decoded
		keyText, _ := json.Marshal(decoded)
		e.Label = fmt.Sprintf("%s data %s of %s", e.Durability, keyText, addr)
	case xdr.LedgerEntryTypeContractCode:
		e.Kind = "code"
		e.Hash = hex.EncodeToString(key.ContractCode.Hash[:])
		e.Label = "code " + e.Hash[:16]
	case xdr.LedgerEntryTypeAccount:
		e.Kind = "account"
		e.Account = key.Account.AccountId.Address()
		e.Label = "account " + e.Account
	case xdr.LedgerEntryTypeTrustline:
		e.Kind = "trustline"
		e.Account = key.TrustLine.AccountId.Address()
		e.Label = "trustline of " + e.Account
	default:
		e.Kind = key.Type.String()
		e.Label = e.Kind
	}
	return e
}

// EntryMove is an entry both calls touch with different access.
type EntryMove struct {
	Entry FootprintEntry `json:"entry"`
	From  string         `json:"from"`
	To    string         `json:"to"`
}

// SimulationDiff compares two simulations' footprints and resources.
type SimulationDiff struct {
	// A and B name the two sides: a contract ID or a report file.
	A string `json:"a"`
	B string `json:"b"`
	// OnlyInA and OnlyInB are entries only one side touches.
	OnlyInA []FootprintEntry `json:"only_in_a"`
	OnlyInB []FootprintEntry `json:"only_in_b"`
	// Moved are entries read-only on one side and read-write on the other.
	Moved []EntryMove `json:"moved"`
	// Resources compares each cost figure, B against A.
	Resources []CostChange `json:"resources"`
	// Same is true when the footprints match and no cost figure changed.
	Same bool `json:"same"`
}

// CompareSimulations compares b's footprint and cost with a's. Both must
// carry FootprintEntries, which every simulation since they were added
// does; the output is sorted so it is stable for CI.
func CompareSimulations(aName string, a *SimulateResult, bName string, b *SimulateResult) (*SimulationDiff, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("two simulations are needed to compare")
	}
	for name, r := range map[string]*SimulateResult{aName: a, bName: b} {
		if r.FootprintEntries == nil && len(r.Footprint.ReadOnly)+len(r.Footprint.ReadWrite) > 0 {
			return nil, fmt.Errorf("%s has no footprint_entries; save the report with a version of SoroProbe that records them", name)
		}
	}
	diff := &SimulationDiff{A: aName, B: bName, OnlyInA: []FootprintEntry{}, OnlyInB: []FootprintEntry{}, Moved: []EntryMove{}}

	index := func(entries []FootprintEntry) map[string]FootprintEntry {
		out := make(map[string]FootprintEntry, len(entries))
		for _, e := range entries {
			out[e.identity()] = e
		}
		return out
	}
	inA, inB := index(a.FootprintEntries), index(b.FootprintEntries)
	for id, ea := range inA {
		eb, ok := inB[id]
		switch {
		case !ok:
			diff.OnlyInA = append(diff.OnlyInA, ea)
		case ea.Access != eb.Access:
			diff.Moved = append(diff.Moved, EntryMove{Entry: eb, From: ea.Access, To: eb.Access})
		}
	}
	for id, eb := range inB {
		if _, ok := inA[id]; !ok {
			diff.OnlyInB = append(diff.OnlyInB, eb)
		}
	}
	byLabel := func(s []FootprintEntry) {
		sort.Slice(s, func(i, j int) bool { return s[i].Label < s[j].Label })
	}
	byLabel(diff.OnlyInA)
	byLabel(diff.OnlyInB)
	sort.Slice(diff.Moved, func(i, j int) bool { return diff.Moved[i].Entry.Label < diff.Moved[j].Entry.Label })

	diff.Resources = CompareCost(a.Cost, b.Cost, nil).Figures
	diff.Same = len(diff.OnlyInA) == 0 && len(diff.OnlyInB) == 0 && len(diff.Moved) == 0
	for _, f := range diff.Resources {
		if f.Delta != 0 {
			diff.Same = false
		}
	}
	return diff, nil
}
