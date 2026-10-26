package scval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stellar/go-stellar-sdk/xdr"

	"github.com/soroworks/soroprobe/internal/scval"
)

func TestIsIntegerLiteralAndEncode(t *testing.T) {
	t.Parallel()

	codec := scval.NewRegistry()

	tests := []struct {
		name     string
		spec     string
		wantType xdr.ScValType
	}{
		// Leading + and - should infer i128
		{"leading plus", "+123", xdr.ScValTypeScvI128},
		{"leading minus", "-123", xdr.ScValTypeScvI128},

		// A lone sign should NOT be an integer literal; fall through to string
		{"lone plus sign", "+", xdr.ScValTypeScvString},
		{"lone minus sign", "-", xdr.ScValTypeScvString},

		// Empty string should NOT be an integer literal; fall through to string
		{"empty string", "", xdr.ScValTypeScvString},

		// Leading zeros should still be integer literals
		{"leading zeros", "007", xdr.ScValTypeScvI128},

		// Embedded spaces should NOT be integer literals; fall through to string
		{"embedded space", "1 23", xdr.ScValTypeScvString},

		// Non-ASCII digits (Arabic-Indic numerals) should NOT be integer literals
		{"Arabic-Indic digits", "١٢٣", xdr.ScValTypeScvString}, // Arabic-Indic 123

		// Regular integer literals
		{"positive integer", "1000", xdr.ScValTypeScvI128},
		{"negative integer", "-1000", xdr.ScValTypeScvI128},

		// Zero
		{"zero", "0", xdr.ScValTypeScvI128},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := codec.Encode(tt.spec)
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, got.Type,
				"Encode(%q) = %v, want %v", tt.spec, got.Type, tt.wantType)
		})
	}
}
