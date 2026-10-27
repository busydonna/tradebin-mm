package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"tradebin-mm/config"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestLoadConfig_MnemonicFromEnvironmentOverridesFile(t *testing.T) {
	t.Setenv(config.MnemonicEnv, "from env")
	path := writeConfig(t, "wallet:\n    mnemonic: \"from file\"\n")

	cfg, err := config.LoadConfig(path)

	require.NoError(t, err)
	require.Equal(t, "from env", cfg.Wallet.Mnemonic)
}

func TestLoadConfig_MnemonicFromFileWithoutEnvironment(t *testing.T) {
	t.Setenv(config.MnemonicEnv, "")
	path := writeConfig(t, "wallet:\n    mnemonic: \"from file\"\n")

	cfg, err := config.LoadConfig(path)

	require.NoError(t, err)
	require.Equal(t, "from file", cfg.Wallet.Mnemonic)
}

func TestLoadConfig_ReservePctDefaults(t *testing.T) {
	path := writeConfig(t, "liquidity_pool:\n    id: \"a_b\"\n    slippage: 0.01\n    interval: 30\n")

	cfg, err := config.LoadConfig(path)

	require.NoError(t, err)
	require.InDelta(t, 0.001, cfg.LiquidityPool.MinReservePct, 1e-12)
	require.InDelta(t, 0.005, cfg.LiquidityPool.MaxReservePct, 1e-12)
	require.Equal(t, int64(10_000_000), cfg.LiquidityPool.FeeReserve)
	require.NoError(t, cfg.LiquidityPool.Validate())
}

func TestLiquidityPoolValidate_ReservePctBounds(t *testing.T) {
	cases := []struct {
		name     string
		min, max float64
		ok       bool
	}{
		{"defaults", 0.001, 0.005, true},
		{"equal", 0.01, 0.01, true},
		{"upper limit", 0.01, 0.05, true},
		{"min above max", 0.006, 0.005, false},
		{"max above 5%", 0.001, 0.051, false},
		{"negative min", -0.001, 0.005, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lp := config.LiquidityPool{Id: "a_b", Slippage: 0.01, Interval: 30, MinReservePct: c.min, MaxReservePct: c.max, FeeReserve: 10_000_000}
			if c.ok {
				require.NoError(t, lp.Validate())
			} else {
				require.Error(t, lp.Validate())
			}
		})
	}
}

func TestLiquidityPoolValidate_FeeReserveMustBePositive(t *testing.T) {
	for _, reserve := range []int64{0, -1} {
		lp := config.LiquidityPool{Id: "a_b", Slippage: 0.01, Interval: 30, MinReservePct: 0.001, MaxReservePct: 0.005, FeeReserve: reserve}
		require.Error(t, lp.Validate())
	}
}

func TestDistConfigs_USDCInjAreValid(t *testing.T) {
	t.Setenv(config.MnemonicEnv, "test mnemonic")
	const usdcInj = "ibc/81DA528F4C5546208D1D50F84C2D0B388B45D37AA36FCA622A3CB2D8EF014838"

	orderbook, err := config.LoadConfig("../configs/usdc-inj-orderbook.yml.dist")
	require.NoError(t, err)
	require.NoError(t, orderbook.Validate())
	require.Equal(t, "ubze", orderbook.Market.BaseDenom)
	require.Equal(t, usdcInj, orderbook.Market.QuoteDenom)
	require.Equal(t, usdcInj+"_ubze", orderbook.Market.PoolId)

	lp, err := config.LoadConfig("../configs/usdc-inj-lp.yml.dist")
	require.NoError(t, err)
	require.NoError(t, lp.LiquidityPool.Validate())
	require.NoError(t, lp.Transaction.Validate())
	require.Equal(t, usdcInj+"_ubze", lp.LiquidityPool.Id)
}
