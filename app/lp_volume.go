package app

import (
	"fmt"
	"math/rand"
	"strconv"

	"tradebin-mm/app/internal"

	basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"
	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

const (
	ubzeDenom   = "ubze"
	ubzeReserve = 300000

	// minSwapAmount is the smallest input (in the input denom's micro-units) worth broadcasting
	minSwapAmount = 1000
)

type haltChecker interface {
	IsDenomHalted(denom string) (bool, error)
}

type lpVolumeConfig interface {
	GetId() string
	GetSlippage() float64
	GetMinReservePct() float64
	GetMaxReservePct() float64
}

type lpProvider interface {
	GetLiquidityPool(poolId string) (*tradebinTypes.LiquidityPool, error)
}

type lpBroadcaster interface {
	BroadcastBlock(msgs []sdk.Msg) error
}

type LPVolumeMaker struct {
	logger          logrus.FieldLogger
	lpProvider      lpProvider
	balanceProvider addressBalanceProvider
	addrProvider    addressProvider
	broadcaster     lpBroadcaster
	haltChecker     haltChecker
	poolId          string
	slippage        math.LegacyDec
	minReservePct   float64
	maxReservePct   float64
}

func NewLPVolumeMaker(
	logger logrus.FieldLogger,
	lpProvider lpProvider,
	balanceProvider addressBalanceProvider,
	addrProvider addressProvider,
	broadcaster lpBroadcaster,
	haltChecker haltChecker,
	cfg lpVolumeConfig,
) (*LPVolumeMaker, error) {
	if logger == nil || lpProvider == nil || balanceProvider == nil || addrProvider == nil || broadcaster == nil || haltChecker == nil || cfg == nil {
		return nil, internal.NewInvalidDependenciesErr("NewLPVolumeMaker")
	}

	if cfg.GetId() == "" {
		return nil, fmt.Errorf("pool id is required")
	}

	if cfg.GetMinReservePct() <= 0 || cfg.GetMinReservePct() > cfg.GetMaxReservePct() {
		return nil, fmt.Errorf("invalid reserve pct bounds")
	}

	return &LPVolumeMaker{
		logger:          logger.WithField("service", "LPVolumeMaker"),
		lpProvider:      lpProvider,
		balanceProvider: balanceProvider,
		addrProvider:    addrProvider,
		broadcaster:     broadcaster,
		haltChecker:     haltChecker,
		poolId:          cfg.GetId(),
		slippage:        math.LegacyMustNewDecFromStr(fmt.Sprintf("%f", cfg.GetSlippage())),
		minReservePct:   cfg.GetMinReservePct(),
		maxReservePct:   cfg.GetMaxReservePct(),
	}, nil
}

func (lv *LPVolumeMaker) MakeVolume() error {
	l := lv.logger.WithField("func", "MakeVolume")

	lp, err := lv.lpProvider.GetLiquidityPool(lv.poolId)
	if err != nil {
		return fmt.Errorf("failed to get liquidity pool: %w", err)
	}
	if lp == nil {
		return fmt.Errorf("liquidity pool not found")
	}

	l.WithField("pool_id", lp.Id).
		WithField("base", lp.Base).
		WithField("quote", lp.Quote).
		Debug("fetched liquidity pool")

	halted, err := lv.isPoolHalted(lp)
	if err != nil {
		return fmt.Errorf("failed to check halted denoms: %w", err)
	}
	if halted {
		l.Warn("a pool denom is halted, skipping swap")
		return nil
	}

	balances, err := lv.balanceProvider.GetAddressBalances(lv.addrProvider.GetAddress().String())
	if err != nil {
		return fmt.Errorf("failed to get balances: %w", err)
	}

	baseBalance := lv.findBalance(balances, lp.Base)
	quoteBalance := lv.findBalance(balances, lp.Quote)

	swapCoin, outputDenom := lv.determineSwap(lp, baseBalance, quoteBalance, l)
	if swapCoin.IsNil() || !swapCoin.IsPositive() {
		l.Info("no suitable balance to swap")
		return nil
	}

	reserveIn, reserveOut := lv.getReserves(lp, swapCoin.Denom)

	minOutput := lv.calculateMinOutput(swapCoin.Amount, reserveIn, reserveOut)
	if !minOutput.IsPositive() {
		l.Warn("calculated min output is not positive, skipping swap")
		return nil
	}

	minOutputCoin := sdk.NewCoin(outputDenom, minOutput)

	msg := tradebinTypes.NewMsgMultiSwap(
		lv.addrProvider.GetAddress().String(),
		[]string{lv.poolId},
		swapCoin,
		minOutputCoin,
	)

	l.WithField("input", swapCoin.String()).
		WithField("min_output", minOutputCoin.String()).
		Info("broadcasting swap")

	err = lv.broadcaster.BroadcastBlock([]sdk.Msg{msg})
	if internal.IsDenomHaltedErr(err) {
		l.WithError(err).Warn("swap refused: a pool denom is halted")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to broadcast swap: %w", err)
	}

	l.Info("swap broadcasted successfully")

	return nil
}

func (lv *LPVolumeMaker) isPoolHalted(lp *tradebinTypes.LiquidityPool) (bool, error) {
	for _, denom := range []string{lp.Base, lp.Quote} {
		halted, err := lv.haltChecker.IsDenomHalted(denom)
		if err != nil {
			return false, err
		}
		if halted {
			return true, nil
		}
	}

	return false, nil
}

// determineSwap swaps from the side holding more value (valued in the pool's quote denom at spot price),
// sized at a random fraction in [minReservePct, maxReservePct] of that side's pool reserve and capped by
// the wallet balance, so inventory drifts back towards 50/50 and the pool price only moves a little per swap.
func (lv *LPVolumeMaker) determineSwap(lp *tradebinTypes.LiquidityPool, baseBalance, quoteBalance math.Int, l logrus.FieldLogger) (sdk.Coin, string) {
	if !lp.ReserveBase.IsPositive() || !lp.ReserveQuote.IsPositive() {
		l.Warn("pool has an empty reserve")
		return sdk.Coin{}, ""
	}

	availBase := lv.spendable(lp.Base, baseBalance)
	availQuote := lv.spendable(lp.Quote, quoteBalance)
	if !availBase.IsPositive() && !availQuote.IsPositive() {
		l.Warn("no spendable balance for either LP denom")
		return sdk.Coin{}, ""
	}

	baseValue := math.LegacyNewDecFromInt(availBase).MulInt(lp.ReserveQuote).QuoInt(lp.ReserveBase)

	swapDenom, outputDenom, available, reserveIn := lp.Base, lp.Quote, availBase, lp.ReserveBase
	if math.LegacyNewDecFromInt(availQuote).GT(baseValue) {
		swapDenom, outputDenom, available, reserveIn = lp.Quote, lp.Base, availQuote, lp.ReserveQuote
	}

	pct := lv.minReservePct + rand.Float64()*(lv.maxReservePct-lv.minReservePct)
	pctDec, err := math.LegacyNewDecFromStr(strconv.FormatFloat(pct, 'f', 12, 64))
	if err != nil {
		l.WithError(err).Error("invalid reserve pct")
		return sdk.Coin{}, ""
	}

	swapAmount := pctDec.MulInt(reserveIn).TruncateInt()
	if swapAmount.GT(available) {
		swapAmount = available
	}

	if swapAmount.LT(math.NewInt(minSwapAmount)) {
		l.WithField("amount", swapAmount.String()).WithField("denom", swapDenom).Info("swap amount below minimum, skipping")
		return sdk.Coin{}, ""
	}

	return sdk.NewCoin(swapDenom, swapAmount), outputDenom
}

// spendable is the balance usable for a swap: ubze keeps a reserve for fees
func (lv *LPVolumeMaker) spendable(denom string, balance math.Int) math.Int {
	if balance.IsNil() || !balance.IsPositive() {
		return math.ZeroInt()
	}

	if denom != ubzeDenom {
		return balance
	}

	left := balance.SubRaw(ubzeReserve)
	if !left.IsPositive() {
		return math.ZeroInt()
	}

	return left
}

func (lv *LPVolumeMaker) getReserves(lp *tradebinTypes.LiquidityPool, inputDenom string) (reserveIn, reserveOut math.Int) {
	if inputDenom == lp.Base {
		return lp.ReserveBase, lp.ReserveQuote
	}
	return lp.ReserveQuote, lp.ReserveBase
}

func (lv *LPVolumeMaker) calculateMinOutput(input, reserveIn, reserveOut math.Int) math.Int {
	// expected_output = (reserve_out * input) / (reserve_in + input)
	inputDec := math.LegacyNewDecFromInt(input)
	reserveInDec := math.LegacyNewDecFromInt(reserveIn)
	reserveOutDec := math.LegacyNewDecFromInt(reserveOut)

	expectedOutput := reserveOutDec.Mul(inputDec).Quo(reserveInDec.Add(inputDec))

	// min_output = expected_output * (1 - slippage)
	slippageFactor := math.LegacyOneDec().Sub(lv.slippage)
	minOutput := expectedOutput.Mul(slippageFactor)

	return minOutput.TruncateInt()
}

func (lv *LPVolumeMaker) findBalance(balances []*basev1beta1.Coin, denom string) math.Int {
	for _, coin := range balances {
		if coin.Denom == denom {
			amt, ok := math.NewIntFromString(coin.Amount)
			if ok {
				return amt
			}
		}
	}
	return math.ZeroInt()
}
