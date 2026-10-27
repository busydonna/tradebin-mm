package internal_test

import (
	"testing"

	"tradebin-mm/app/internal"

	"cosmossdk.io/math"
	"github.com/stretchr/testify/require"
)

func TestTruncateToStep(t *testing.T) {
	cases := []struct{ num, step, want string }{
		{"0.000333333333", "0.000001", "0.000333"},
		{"0.000333333333", "0.0000001", "0.0003333"}, // below 1e-6: the old float formatting truncated this to 0
		{"0.7", "0.5", "0.5"},
		{"12.34", "1", "12"},
		{"0.0000004", "0.000001", "0"},
	}

	for _, c := range cases {
		num := math.LegacyMustNewDecFromStr(c.num)
		step := math.LegacyMustNewDecFromStr(c.step)

		got, err := internal.TruncateToStep(&num, &step)

		require.NoError(t, err)
		require.True(t, math.LegacyMustNewDecFromStr(c.want).Equal(*got), "%s by %s: got %s", c.num, c.step, got)
	}
}

func TestTruncateToStep_RejectsNonPositiveStep(t *testing.T) {
	num := math.LegacyOneDec()
	zero := math.LegacyZeroDec()

	_, err := internal.TruncateToStep(&num, &zero)

	require.Error(t, err)
}
