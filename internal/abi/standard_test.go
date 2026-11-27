package abi_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/soroprobe/internal/abi"
)

func ty(kind string) abi.Type { return abi.Type{Kind: kind, Display: kind} }

// conformingToken builds an interface that exactly matches the standard.
func conformingToken(t *testing.T) *abi.Interface {
	t.Helper()
	std, err := abi.LookupStandard("sep41")
	require.NoError(t, err)

	iface := &abi.Interface{}
	for _, fn := range std.Functions {
		f := abi.Function{Name: fn.Name}
		for i, kind := range fn.Inputs {
			f.Inputs = append(f.Inputs, abi.Param{Name: string(rune('a' + i)), Type: ty(kind)})
		}
		if fn.Output != "void" {
			f.Outputs = []abi.Type{ty(fn.Output)}
		}
		iface.Functions = append(iface.Functions, f)
	}
	return iface
}

func TestStandardNames(t *testing.T) {
	t.Parallel()

	t.Run("returns the supported names sorted", func(t *testing.T) {
		t.Parallel()

		names := abi.StandardNames()
		require.NotEmpty(t, names)
		assert.Contains(t, names, "sep41")
		assert.True(t, sort.StringsAreSorted(names), "names are sorted: %v", names)
		assert.Equal(t, names, abi.StandardNames(), "the result is stable")
	})

	t.Run("the error path lists every supported name", func(t *testing.T) {
		t.Parallel()

		// A typo must be a one-step fix, so the error quotes every name
		// StandardNames returns.
		_, err := abi.LookupStandard("not-a-standard")
		require.Error(t, err)
		assert.Contains(t, err.Error(), strings.Join(abi.StandardNames(), ", "))
	})
}

func TestLookupStandard(t *testing.T) {
	t.Parallel()

	std, err := abi.LookupStandard(" SEP41 ")
	require.NoError(t, err)
	assert.Equal(t, "sep41", std.Name)

	_, err = abi.LookupStandard("erc20")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"erc20"`)
	assert.Contains(t, err.Error(), "sep41")
}

func TestVerifyAcceptsAConformingToken(t *testing.T) {
	t.Parallel()

	std, _ := abi.LookupStandard("sep41")
	assert.Empty(t, std.Verify(conformingToken(t)))
}

func TestVerifyIgnoresArgumentNames(t *testing.T) {
	t.Parallel()

	iface := conformingToken(t)
	for i := range iface.Functions {
		for j := range iface.Functions[i].Inputs {
			iface.Functions[i].Inputs[j].Name = "renamed"
		}
	}
	std, _ := abi.LookupStandard("sep41")
	assert.Empty(t, std.Verify(iface))
}

func TestVerifyReportsEachProblem(t *testing.T) {
	t.Parallel()

	iface := conformingToken(t)
	var kept []abi.Function
	for _, fn := range iface.Functions {
		switch fn.Name {
		case "burn_from":
			continue // missing
		case "balance":
			fn.Inputs = nil // wrong count
		case "transfer":
			fn.Inputs[2].Type = ty("u64") // wrong argument type
		case "decimals":
			fn.Outputs = []abi.Type{ty("u64")} // wrong return type
		case "name":
			fn.Outputs = nil // returns void
		}
		kept = append(kept, fn)
	}
	iface.Functions = kept

	std, _ := abi.LookupStandard("sep41")
	got := map[string]string{}
	for _, d := range std.Verify(iface) {
		got[d.Function] = d.Problem
	}

	assert.Len(t, got, 5)
	assert.Contains(t, got["burn_from"], "not exported")
	assert.Contains(t, got["balance"], "takes 0 argument(s)")
	assert.Contains(t, got["transfer"], "argument 3")
	assert.Contains(t, got["decimals"], "returns u64")
	assert.Contains(t, got["name"], "returns void")
}

func TestVerifyEmitsOneDeviationPerProblem(t *testing.T) {
	t.Parallel()

	iface := conformingToken(t)
	for i := range iface.Functions {
		if iface.Functions[i].Name == "transfer" {
			iface.Functions[i].Inputs[0].Type = ty("u32")
			iface.Functions[i].Inputs[1].Type = ty("u32")
			iface.Functions[i].Outputs = []abi.Type{ty("bool")}
		}
	}
	std, _ := abi.LookupStandard("sep41")
	assert.Len(t, std.Verify(iface), 3)
}
