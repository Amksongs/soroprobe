package probe_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/soroworks/soroprobe/internal/probe"
)

func TestEstimateTotalFee(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		resourceFee int64
		baseFee     int64
		want        int64
	}{
		{
			name:        "default base fee when zero",
			resourceFee: 12279,
			baseFee:     0,
			want:        12379,
		},
		{
			name:        "default base fee when negative",
			resourceFee: 5000,
			baseFee:     -10,
			want:        5100,
		},
		{
			name:        "custom base fee",
			resourceFee: 12279,
			baseFee:     200,
			want:        12479,
		},
		{
			name:        "zero resource fee",
			resourceFee: 0,
			baseFee:     100,
			want:        100,
		},
		{
			name:        "large resource fee and base fee",
			resourceFee: 10_000_000,
			baseFee:     1000,
			want:        10_001_000,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := probe.EstimateTotalFee(tc.resourceFee, tc.baseFee)
			assert.Equal(t, tc.want, got)
		})
	}
}
