package app_test

import (
	"errors"
	"testing"

	"tradebin-mm/app"
	"tradebin-mm/config"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/stretchr/testify/require"
)

type lpFixture struct {
	pools       *fakePools
	balances    *fakeBalances
	broadcaster *fakeBroadcaster
	halts       *fakeHalts
	maker       *app.LPVolumeMaker
}

func newLPFixture(t *testing.T, coins map[string]int64, minPct, maxPct float64) *lpFixture {
	t.Helper()

	return newLPFixtureWithFeeReserve(t, coins, minPct, maxPct, 1_000_000)
}

func newLPFixtureWithFeeReserve(t *testing.T, coins map[string]int64, minPct, maxPct float64, feeReserve int64) *lpFixture {
	t.Helper()
	f := &lpFixture{
		pools:       &fakePools{pool: pool(1_000_000_000, 3_000_000_000_000)},
		balances:    &fakeBalances{coins: coins},
		broadcaster: &fakeBroadcaster{},
		halts:       &fakeHalts{},
	}
	cfg := &config.LiquidityPool{Id: f.pools.pool.Id, Slippage: 0.01, Interval: 30, MinReservePct: minPct, MaxReservePct: maxPct, FeeReserve: feeReserve}

	maker, err := app.NewLPVolumeMaker(quietLogger(), f.pools, f.balances, fakeAddress{}, f.broadcaster, f.halts, cfg)
	require.NoError(t, err)
	f.maker = maker

	return f
}

func (f *lpFixture) onlySwap(t *testing.T) *tradebinTypes.MsgMultiSwap {
	t.Helper()
	require.Len(t, f.broadcaster.sent, 1)
	require.Len(t, f.broadcaster.sent[0], 1)
	msg, ok := f.broadcaster.sent[0][0].(*tradebinTypes.MsgMultiSwap)
	require.True(t, ok)

	return msg
}

func TestLPVolume_SwapIsCappedByReservePctNotWholeBalance(t *testing.T) {
	f := newLPFixture(t, map[string]int64{usdc: 100_000_000, ubze: 1_000_000}, 0.005, 0.005)

	require.NoError(t, f.maker.MakeVolume())

	msg := f.onlySwap(t)
	require.Equal(t, usdc, msg.Input.Denom)
	require.Equal(t, int64(5_000_000), msg.Input.Amount.Int64(), "0.5% of the 1000 USDC reserve, not the 100 USDC balance")
	require.Equal(t, []string{f.pools.pool.Id}, msg.Routes)

	// constant product expectation x (1 - slippage)
	in := math.LegacyNewDec(5_000_000)
	expected := math.LegacyNewDec(3_000_000_000_000).Mul(in).Quo(math.LegacyNewDec(1_000_000_000).Add(in))
	require.Equal(t, ubze, msg.MinOutput.Denom)
	require.Equal(t, expected.Mul(math.LegacyMustNewDecFromStr("0.99")).TruncateInt(), msg.MinOutput.Amount)
}

func TestLPVolume_SwapAmountStaysWithinReservePctBounds(t *testing.T) {
	for i := 0; i < 50; i++ {
		f := newLPFixture(t, map[string]int64{usdc: 100_000_000, ubze: 1_000_000}, 0.001, 0.005)

		require.NoError(t, f.maker.MakeVolume())

		amount := f.onlySwap(t).Input.Amount.Int64()
		require.GreaterOrEqual(t, amount, int64(1_000_000))
		require.LessOrEqual(t, amount, int64(5_000_000))
	}
}

func TestLPVolume_SwapIsCappedByBalance(t *testing.T) {
	f := newLPFixture(t, map[string]int64{usdc: 2_000_000, ubze: 1_000_000}, 0.005, 0.005)

	require.NoError(t, f.maker.MakeVolume())

	msg := f.onlySwap(t)
	require.Equal(t, usdc, msg.Input.Denom)
	require.Equal(t, int64(2_000_000), msg.Input.Amount.Int64())
}

func TestLPVolume_UbzeInputKeepsFeeReserve(t *testing.T) {
	f := newLPFixture(t, map[string]int64{ubze: 2_000_000}, 0.005, 0.005)

	require.NoError(t, f.maker.MakeVolume())

	msg := f.onlySwap(t)
	require.Equal(t, ubze, msg.Input.Denom)
	require.Equal(t, int64(1_000_000), msg.Input.Amount.Int64(), "fee_reserve ubze stay for fees")
}

func TestLPVolume_UbzeInputKeepsConfiguredFeeReserve(t *testing.T) {
	// a balance-capped ubze swap must leave fee_reserve behind, not a fixed 300000 ubze
	f := newLPFixtureWithFeeReserve(t, map[string]int64{ubze: 15_000_000}, 0.005, 0.005, 10_000_000)

	require.NoError(t, f.maker.MakeVolume())

	msg := f.onlySwap(t)
	require.Equal(t, ubze, msg.Input.Denom)
	require.Equal(t, int64(5_000_000), msg.Input.Amount.Int64())
}

func TestLPVolume_SkipsWhenUbzeCannotPayFees(t *testing.T) {
	// a USDC-side swap still pays its fees in ubze: below 1 BZE it would only fail on chain
	f := newLPFixtureWithFeeReserve(t, map[string]int64{usdc: 100_000_000, ubze: 999_999}, 0.001, 0.005, 10_000_000)

	require.NoError(t, f.maker.MakeVolume())
	require.Empty(t, f.broadcaster.sent)
}

func TestLPVolume_SwapsAfterFeesDipBelowFeeReserve(t *testing.T) {
	// mainnet state after a balance-capped ubze swap: its fees left the wallet just under fee_reserve
	f := newLPFixtureWithFeeReserve(t, map[string]int64{usdc: 4_067_048, ubze: 9_842_635}, 0.001, 0.005, 10_000_000)

	require.NoError(t, f.maker.MakeVolume())

	msg := f.onlySwap(t)
	require.Equal(t, usdc, msg.Input.Denom)
}

func TestNewLPVolumeMaker_RejectsFeeReserveBelowOneBZE(t *testing.T) {
	p := pool(1_000_000_000, 3_000_000_000_000)
	cfg := &config.LiquidityPool{Id: p.Id, Slippage: 0.01, Interval: 30, MinReservePct: 0.001, MaxReservePct: 0.005, FeeReserve: 999_999}

	_, err := app.NewLPVolumeMaker(quietLogger(), &fakePools{pool: p}, &fakeBalances{}, fakeAddress{}, &fakeBroadcaster{}, &fakeHalts{}, cfg)

	require.Error(t, err)
}

func TestLPVolume_SkipsBelowMinimumAmount(t *testing.T) {
	f := newLPFixture(t, map[string]int64{ubze: 1_000_999}, 0.005, 0.005)

	require.NoError(t, f.maker.MakeVolume())
	require.Empty(t, f.broadcaster.sent)
}

func TestLPVolume_SwapsFromTheLargerValueSide(t *testing.T) {
	t.Run("base worth more", func(t *testing.T) {
		// 10000 uusdc are worth 30M ubze at 3000 ubze per uusdc, against 20M ubze
		f := newLPFixture(t, map[string]int64{usdc: 10_000, ubze: 20_000_000}, 0.001, 0.001)
		require.NoError(t, f.maker.MakeVolume())
		require.Equal(t, usdc, f.onlySwap(t).Input.Denom)
	})

	t.Run("quote worth more", func(t *testing.T) {
		// 10000 uusdc are worth 30M ubze, against 40M ubze
		f := newLPFixture(t, map[string]int64{usdc: 10_000, ubze: 40_000_000}, 0.001, 0.001)
		require.NoError(t, f.maker.MakeVolume())
		msg := f.onlySwap(t)
		require.Equal(t, ubze, msg.Input.Denom)
		require.Equal(t, usdc, msg.MinOutput.Denom)
	})
}

func TestLPVolume_HaltedDenomSkipsSwap(t *testing.T) {
	f := newLPFixture(t, map[string]int64{usdc: 100_000_000, ubze: 1_000_000}, 0.001, 0.005)
	f.halts.halted = map[string]bool{usdc: true}

	require.NoError(t, f.maker.MakeVolume())
	require.Empty(t, f.broadcaster.sent)
}

func TestLPVolume_HaltRefusalIsNotAnError(t *testing.T) {
	f := newLPFixture(t, map[string]int64{usdc: 100_000_000, ubze: 1_000_000}, 0.001, 0.005)
	f.broadcaster.err = errHaltedOnChain

	require.NoError(t, f.maker.MakeVolume())
	require.Len(t, f.broadcaster.sent, 1, "one attempt, no retry")
}

func TestLPVolume_OtherBroadcastErrorsAreReturned(t *testing.T) {
	f := newLPFixture(t, map[string]int64{usdc: 100_000_000, ubze: 1_000_000}, 0.001, 0.005)
	f.broadcaster.err = errors.New("insufficient fees")

	require.Error(t, f.maker.MakeVolume())
}

func TestLPVolume_HaltQueryErrorIsReturned(t *testing.T) {
	f := newLPFixture(t, map[string]int64{usdc: 100_000_000}, 0.001, 0.005)
	f.halts.err = errors.New("node down")

	require.Error(t, f.maker.MakeVolume())
	require.Empty(t, f.broadcaster.sent)
}
