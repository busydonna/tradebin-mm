package app

import (
	"errors"
	"fmt"
	"time"

	"tradebin-mm/app/data_provider"
	"tradebin-mm/app/dto"
	"tradebin-mm/app/internal"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"
)

const (
	cancelOrdersDelta = 5

	haltedCancelLimit = 500
)

// ErrMarketHalted is returned by FillOrderBook when a market denom is halted: the wallet's orders were
// cancelled (exit only) and nothing else should trade on the market this cycle.
var ErrMarketHalted = errors.New("market denom is halted")

type ordersMarketConfig interface {
	data_provider.MarketProvider
	GetPoolId() string
}

type balanceProvider interface {
	GetAddressBalancesForMarket(address string, marketId data_provider.MarketProvider) (*dto.MarketBalance, error)
	GetMarketBalance(address string, marketCfg data_provider.MarketProvider) (*dto.MarketBalance, error)
}

type addressProvider interface {
	GetAddress() types.AccAddress
}

type ordersProvider interface {
	GetActiveOrders(mb *dto.MarketBalance) (buys, sells []tradebinTypes.AggregatedOrder, err error)
	GetAddressActiveOrders(marketId, address string, limit int) (buys, sells []tradebinTypes.Order, err error)
	GetLastMarketOrder(marketId string) (*tradebinTypes.HistoryOrder, error)
}

type orderSubmitter interface {
	CancelOrders([]*tradebinTypes.MsgCancelOrder) error
	AddOrders([]*tradebinTypes.MsgCreateOrder) error
}

type ordersConfig interface {
	GetBuyNo() int
	GetSellNo() int
	GetStartPriceDec() *math.LegacyDec
	GetPriceStepDec() *math.LegacyDec
	GetOrderMinAmount() *math.Int
	GetOrderMaxAmount() *math.Int
	GetSpreadSteps() *math.Int
	GetHoldBackSeconds() int
}

type Orders struct {
	ordersConfig ordersConfig
	marketConfig ordersMarketConfig

	balanceProvider balanceProvider
	addressProvider addressProvider
	ordersProvider  ordersProvider
	orderSubmitter  orderSubmitter
	haltChecker     haltChecker
	poolProvider    lpProvider

	l logrus.FieldLogger
}

func NewOrdersFiller(
	l logrus.FieldLogger,
	ordersConfig ordersConfig,
	marketConfig ordersMarketConfig,
	balanceProvider balanceProvider,
	addressProvider addressProvider,
	ordersProvider ordersProvider,
	orderSubmitter orderSubmitter,
	haltChecker haltChecker,
	poolProvider lpProvider,
) (*Orders, error) {
	if l == nil || ordersConfig == nil || marketConfig == nil || balanceProvider == nil || addressProvider == nil || ordersProvider == nil || orderSubmitter == nil || haltChecker == nil || poolProvider == nil {
		return nil, internal.NewInvalidDependenciesErr("NewOrdersFiller")
	}

	return &Orders{
		ordersConfig:    ordersConfig,
		marketConfig:    marketConfig,
		balanceProvider: balanceProvider,
		addressProvider: addressProvider,
		ordersProvider:  ordersProvider,
		orderSubmitter:  orderSubmitter,
		haltChecker:     haltChecker,
		poolProvider:    poolProvider,
		l:               l.WithField("service", "OrdersFiller"),
	}, nil
}

// FillOrderBook keeps the wallet's ladder on the market. A halted market denom (queried up front, or a
// tx refused with tradebin error 4017) cancels the wallet's orders and returns ErrMarketHalted.
func (v *Orders) FillOrderBook() error {
	halted, err := v.isMarketHalted()
	if err != nil {
		return fmt.Errorf("failed to check halted denoms: %v", err)
	}

	if halted {
		return v.exitHaltedMarket()
	}

	err = v.fillOrderBook()
	if internal.IsDenomHaltedErr(err) {
		return v.exitHaltedMarket()
	}

	return err
}

func (v *Orders) isMarketHalted() (bool, error) {
	for _, denom := range []string{v.marketConfig.GetBaseDenom(), v.marketConfig.GetQuoteDenom()} {
		halted, err := v.haltChecker.IsDenomHalted(denom)
		if err != nil {
			return false, err
		}
		if halted {
			return true, nil
		}
	}

	return false, nil
}

func (v *Orders) exitHaltedMarket() error {
	v.l.WithField("market", v.marketConfig.GetMarketId()).Warn("market denom is halted: cancelling own orders and skipping this cycle")

	myBuys, mySells, err := v.ordersProvider.GetAddressActiveOrders(v.marketConfig.GetMarketId(), v.addressProvider.GetAddress().String(), haltedCancelLimit)
	if err != nil {
		return errors.Join(ErrMarketHalted, fmt.Errorf("failed to get address active orders: %v", err))
	}

	all := append(myBuys, mySells...)
	if len(all) == 0 {
		return ErrMarketHalted
	}

	if err := v.cancelOrders(all, len(all)); err != nil {
		return errors.Join(ErrMarketHalted, err)
	}

	return ErrMarketHalted
}

func (v *Orders) fillOrderBook() error {
	balances, err := v.balanceProvider.GetMarketBalance(v.addressProvider.GetAddress().String(), v.marketConfig)
	if err != nil {
		return fmt.Errorf("failed to get balances: %v", err)
	}

	requiredOrders := v.ordersConfig.GetBuyNo() + v.ordersConfig.GetSellNo()
	myBuys, mySells, err := v.ordersProvider.GetAddressActiveOrders(balances.MarketId, v.addressProvider.GetAddress().String(), requiredOrders*2)
	if err != nil {
		return fmt.Errorf("failed to get address active orders: %v", err)
	}

	if v.marketConfig.GetPoolId() != "" {
		if !v.shouldFillOrderBook(balances.MarketId) {
			v.l.Info("should not reprice order book yet. hold back.")

			return nil
		}

		return v.repriceToPool(myBuys, mySells)
	}

	myOrdersCount := len(myBuys) + len(mySells)
	if myOrdersCount > (requiredOrders + cancelOrdersDelta) {
		v.l.Info("too many orders placed, cancelling some")

		err = v.cancelExtraOrders(myBuys, mySells)
		if err != nil {
			v.l.WithError(err).Errorf("failed to cancel extra orders")
		}
	}

	if !v.shouldFillOrderBook(balances.MarketId) {
		v.l.Info("should not fill order book yet. hold back.")

		return nil
	}

	buys, sells, err := v.ordersProvider.GetActiveOrders(balances)
	if err != nil {
		return err
	}

	bBuy, sSell := v.getSpread(buys, sells)
	toTruncate := v.getStartPrice(v.marketConfig.GetMarketId(), bBuy, sSell)
	v.l.WithField("start_price_raw", toTruncate).Debug("found start price")
	step := v.ordersConfig.GetPriceStepDec()

	startPrice, err := internal.TruncateToStep(toTruncate, step)
	if err != nil {
		return fmt.Errorf("failed to truncate start price to step: %v", err)
	}
	v.l.WithField("start_price", startPrice).Debug("start price truncated")

	err = v.fillOrders(sells, startPrice, tradebinTypes.OrderTypeSell, v.ordersConfig.GetSellNo(), v.buildPricesMap(mySells))
	if err != nil {
		return fmt.Errorf("failed to fill sell orders: %v", err)
	}

	buyStartPrice := startPrice.Sub(*step)
	err = v.fillOrders(buys, &buyStartPrice, tradebinTypes.OrderTypeBuy, v.ordersConfig.GetBuyNo(), v.buildPricesMap(myBuys))
	if err != nil {
		return fmt.Errorf("failed to fill buy orders: %v", err)
	}

	return nil
}

func (v *Orders) buildPricesMap(orders []tradebinTypes.Order) map[string]struct{} {
	prices := make(map[string]struct{})
	for _, order := range orders {
		//for safety, we convert it to dec and trim trailing zeros to make sure the prices are unique
		orderPriceDec := math.LegacyMustNewDecFromStr(order.Price)
		prices[internal.TrimAmountTrailingZeros(orderPriceDec.String())] = struct{}{}
	}

	return prices
}

func (v *Orders) fillOrders(existingOrders []tradebinTypes.AggregatedOrder, startPrice *math.LegacyDec, orderType string, neededOrders int, excludedPrices map[string]struct{}) error {
	l := v.l.WithField("orderType", orderType)

	l.Info("start building order add messages")
	if neededOrders <= 0 {
		l.Info("no orders needed to be placed")
		return nil
	}

	minAmount := v.ordersConfig.GetOrderMinAmount()
	maxAmount := v.ordersConfig.GetOrderMaxAmount()
	newStartPrice := *startPrice
	//leave configured spread empty
	if v.ordersConfig.GetSpreadSteps().IsPositive() {
		//divide the total spread steps required to be empty in two (half for sells/half for buys)
		sideSpreadSteps := v.ordersConfig.GetSpreadSteps().QuoRaw(2)
		spreadTotal := v.ordersConfig.GetPriceStepDec().MulInt(sideSpreadSteps)
		if orderType == tradebinTypes.OrderTypeBuy {
			newStartPrice = newStartPrice.Sub(spreadTotal)
		} else {
			newStartPrice = newStartPrice.Add(spreadTotal)
		}
	}

	var newOrdersMsgs []*tradebinTypes.MsgCreateOrder
	l.Info("not enough existing orders to fill the needed number of orders")
	for neededOrders > 0 {
		//excluded prices are the prices we already have an order for
		if _, ok := excludedPrices[internal.TrimAmountTrailingZeros(newStartPrice.String())]; ok {
			if orderType == tradebinTypes.OrderTypeBuy {
				newStartPrice = newStartPrice.Sub(*v.ordersConfig.GetPriceStepDec())
			} else {
				newStartPrice = newStartPrice.Add(*v.ordersConfig.GetPriceStepDec())
			}

			neededOrders--
			continue
		}

		shouldPlace := true
		for _, existing := range existingOrders {
			if existing.OrderType != orderType {
				return fmt.Errorf("expected order type to be %s but encountered order of different type: %s", orderType, existing.OrderType)
			}

			existingPrice := math.LegacyMustNewDecFromStr(existing.Price)
			if existingPrice.Equal(newStartPrice) {
				shouldPlace = false
				break
			}

			if orderType == tradebinTypes.OrderTypeBuy && existingPrice.LT(newStartPrice) {
				shouldPlace = true
				break
			} else if orderType == tradebinTypes.OrderTypeSell && existingPrice.GT(newStartPrice) {
				shouldPlace = true
				break
			}
		}

		if !newStartPrice.IsPositive() {
			l.Info("reached a non positive start price. stop creating order messages")
			break
		}

		if shouldPlace {
			randAmount := internal.MustRandomInt(minAmount, maxAmount)
			msg := tradebinTypes.NewMsgCreateOrder(
				v.addressProvider.GetAddress().String(),
				orderType,
				randAmount.String(),
				internal.TrimAmountTrailingZeros(newStartPrice.String()),
				v.marketConfig.GetMarketId(),
			)
			newOrdersMsgs = append(newOrdersMsgs, msg)
			neededOrders--
		}

		if orderType == tradebinTypes.OrderTypeBuy {
			newStartPrice = newStartPrice.Sub(*v.ordersConfig.GetPriceStepDec())
		} else {
			newStartPrice = newStartPrice.Add(*v.ordersConfig.GetPriceStepDec())
		}
	}

	if len(newOrdersMsgs) == 0 {
		l.Debug("no new orders to fill")
		return nil
	}

	l.Info("submitting new orders")

	return v.orderSubmitter.AddOrders(newOrdersMsgs)
}

func (v *Orders) getStartPrice(marketId string, biggestBuy *math.LegacyDec, smallestSell *math.LegacyDec) *math.LegacyDec {
	if v.ordersConfig.GetSpreadSteps().IsPositive() {
		//if the config requires an empty spread we take the price from the spread
		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	history, err := v.ordersProvider.GetLastMarketOrder(marketId)
	if history == nil {
		//in this case fallback on taking the price from the spread
		if err != nil {
			v.l.WithError(err).Error("failed to get last order for market")
		}

		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	histPrice := math.LegacyMustNewDecFromStr(history.Price)
	if !histPrice.IsPositive() || histPrice.GT(*smallestSell) || histPrice.LT(*biggestBuy) {

		return v.getStartPriceFromSpread(biggestBuy, smallestSell)
	}

	if history.GetOrderType() == tradebinTypes.OrderTypeSell {
		step := v.ordersConfig.GetPriceStepDec()
		start := histPrice.Add(*step)

		return &start
	}

	return &histPrice
}

func (v *Orders) getStartPriceFromSpread(biggestBuy *math.LegacyDec, smallestSell *math.LegacyDec) *math.LegacyDec {
	if biggestBuy.IsZero() && smallestSell.IsZero() {
		//start from configured price
		return v.ordersConfig.GetStartPriceDec()
	}

	if biggestBuy.IsZero() {
		//start from the smallest sell, there's no buy to use
		return smallestSell
	}
	step := v.ordersConfig.GetPriceStepDec()

	if smallestSell.IsZero() {
		//start from the biggest buy price + step
		start := biggestBuy.Add(*step)

		return &start
	}

	//let's see the diff between buy and sell prices
	diff := smallestSell.Sub(*biggestBuy)
	if diff.LTE(*step) {
		return smallestSell
	}

	// calculate how many steps are between the biggest buy and the smallest sell and divide by two to find the middle
	noOfSteps := diff.Quo(*step).Quo(math.LegacyNewDec(2)).TruncateDec()
	start := smallestSell.Sub(step.Mul(noOfSteps))

	return &start
}

func (v *Orders) cancelExtraOrders(buys []tradebinTypes.Order, sells []tradebinTypes.Order) error {
	if len(buys) > v.ordersConfig.GetBuyNo() {
		internal.SortOrdersByPrice(buys, false)
		err := v.cancelOrders(buys, len(buys)-v.ordersConfig.GetBuyNo())
		if err != nil {
			return fmt.Errorf("failed to cancel extra buy orders: %v", err)
		}
	}

	if len(sells) > v.ordersConfig.GetSellNo() {
		internal.SortOrdersByPrice(sells, true)
		err := v.cancelOrders(sells, len(sells)-v.ordersConfig.GetSellNo())
		if err != nil {
			return fmt.Errorf("failed to cancel extra sell orders: %v", err)
		}
	}

	return nil
}

func (v *Orders) cancelOrders(sortedOrders []tradebinTypes.Order, limit int) error {
	var msgs []*tradebinTypes.MsgCancelOrder
	for _, order := range sortedOrders[:limit] {
		m := tradebinTypes.NewMsgCancelOrder(v.addressProvider.GetAddress().String(), order.MarketId, order.Id, order.OrderType)
		msgs = append(msgs, m)
	}

	err := v.orderSubmitter.CancelOrders(msgs)
	if err != nil {
		return fmt.Errorf("failed to cancel order: %v", err)
	}

	return nil
}

func (v *Orders) getSpread(buys, sells []tradebinTypes.AggregatedOrder) (biggestBuy *math.LegacyDec, smallestSell *math.LegacyDec) {
	b := math.LegacyZeroDec()
	s := math.LegacyZeroDec()

	if len(buys) > 0 {
		b = math.LegacyMustNewDecFromStr(buys[0].Price)
	}

	if len(sells) > 0 {
		s = math.LegacyMustNewDecFromStr(sells[0].Price)
	}

	return &b, &s
}

func (v *Orders) shouldFillOrderBook(marketId string) bool {
	l := v.l.WithField("method", "shouldFillOrderBook")
	history, err := v.ordersProvider.GetLastMarketOrder(marketId)
	if err != nil {
		l.WithError(err).Error("failed to get last order for market")
		return false
	}

	if history == nil {
		return true
	}

	if history.Maker == v.addressProvider.GetAddress().String() && history.Taker == v.addressProvider.GetAddress().String() {
		return true
	}

	return time.Now().Unix()-history.ExecutedAt > int64(v.ordersConfig.GetHoldBackSeconds())
}

// repriceToPool moves the wallet's ladder onto the targets derived from the pool spot price: own orders
// off the targets are cancelled in one tx and the missing targets are placed in one tx.
func (v *Orders) repriceToPool(myBuys, mySells []tradebinTypes.Order) error {
	reference, err := v.poolReferencePrice()
	if err != nil {
		return err
	}
	v.l.WithField("reference_price", reference.String()).Debug("pool reference price")

	sellTargets, buyTargets := v.targetPrices(reference)

	sellCancels, sellMissing := v.diffOrders(mySells, sellTargets)
	buyCancels, buyMissing := v.diffOrders(myBuys, buyTargets)

	cancels := append(sellCancels, buyCancels...)
	if len(cancels) > 0 {
		v.l.Infof("cancelling %d off-target orders", len(cancels))
		if err := v.orderSubmitter.CancelOrders(cancels); err != nil {
			return fmt.Errorf("failed to cancel off-target orders: %w", err)
		}
	}

	var creates []*tradebinTypes.MsgCreateOrder
	creates = append(creates, v.createMsgs(tradebinTypes.OrderTypeSell, sellMissing)...)
	creates = append(creates, v.createMsgs(tradebinTypes.OrderTypeBuy, buyMissing)...)
	if len(creates) == 0 {
		v.l.Debug("order book already on target")
		return nil
	}

	v.l.Infof("placing %d orders", len(creates))
	if err := v.orderSubmitter.AddOrders(creates); err != nil {
		return fmt.Errorf("failed to place orders: %w", err)
	}

	return nil
}

// poolReferencePrice is the pool spot price expressed as the market's quote per base, truncated to step
func (v *Orders) poolReferencePrice() (*math.LegacyDec, error) {
	lp, err := v.poolProvider.GetLiquidityPool(v.marketConfig.GetPoolId())
	if err != nil {
		return nil, fmt.Errorf("failed to get liquidity pool: %w", err)
	}
	if lp == nil {
		return nil, fmt.Errorf("liquidity pool %s not found", v.marketConfig.GetPoolId())
	}

	base, quote := v.marketConfig.GetBaseDenom(), v.marketConfig.GetQuoteDenom()
	if (lp.Base != base || lp.Quote != quote) && (lp.Base != quote || lp.Quote != base) {
		return nil, fmt.Errorf("liquidity pool %s does not trade the market denoms", lp.Id)
	}

	if !lp.ReserveBase.IsPositive() || !lp.ReserveQuote.IsPositive() {
		return nil, fmt.Errorf("liquidity pool %s has an empty reserve", lp.Id)
	}

	var spot math.LegacyDec
	if lp.Base == quote {
		spot = math.LegacyNewDecFromInt(lp.ReserveBase).QuoInt(lp.ReserveQuote)
	} else {
		spot = math.LegacyNewDecFromInt(lp.ReserveQuote).QuoInt(lp.ReserveBase)
	}

	reference, err := internal.TruncateToStep(&spot, v.ordersConfig.GetPriceStepDec())
	if err != nil {
		return nil, err
	}

	if !reference.IsPositive() {
		return nil, fmt.Errorf("pool price %s is below one step", spot.String())
	}

	return reference, nil
}

// targetPrices builds the ladder around reference: sells at reference + step*(half+i), buys at
// reference - step*(half+1+i), where half is spread_steps/2. Non-positive buy prices are dropped.
func (v *Orders) targetPrices(reference *math.LegacyDec) (sells, buys []math.LegacyDec) {
	step := *v.ordersConfig.GetPriceStepDec()
	half := v.ordersConfig.GetSpreadSteps().QuoRaw(2).Int64()

	for i := 0; i < v.ordersConfig.GetSellNo(); i++ {
		sells = append(sells, reference.Add(step.MulInt64(half+int64(i))))
	}

	for i := 0; i < v.ordersConfig.GetBuyNo(); i++ {
		price := reference.Sub(step.MulInt64(half + 1 + int64(i)))
		if !price.IsPositive() {
			break
		}
		buys = append(buys, price)
	}

	return sells, buys
}

// diffOrders returns cancel messages for own orders not on a target (or duplicating one) and the targets with no own order
func (v *Orders) diffOrders(own []tradebinTypes.Order, targets []math.LegacyDec) ([]*tradebinTypes.MsgCancelOrder, []math.LegacyDec) {
	wanted := make(map[string]struct{}, len(targets))
	for _, t := range targets {
		wanted[priceKey(t)] = struct{}{}
	}

	covered := make(map[string]struct{}, len(targets))
	var cancels []*tradebinTypes.MsgCancelOrder
	for _, order := range own {
		key := priceKey(math.LegacyMustNewDecFromStr(order.Price))
		_, isTarget := wanted[key]
		_, isCovered := covered[key]
		if isTarget && !isCovered {
			covered[key] = struct{}{}
			continue
		}

		cancels = append(cancels, tradebinTypes.NewMsgCancelOrder(v.addressProvider.GetAddress().String(), order.MarketId, order.Id, order.OrderType))
	}

	var missing []math.LegacyDec
	for _, t := range targets {
		if _, ok := covered[priceKey(t)]; !ok {
			missing = append(missing, t)
		}
	}

	return cancels, missing
}

func (v *Orders) createMsgs(orderType string, prices []math.LegacyDec) []*tradebinTypes.MsgCreateOrder {
	msgs := make([]*tradebinTypes.MsgCreateOrder, 0, len(prices))
	for _, price := range prices {
		amount := internal.MustRandomInt(v.ordersConfig.GetOrderMinAmount(), v.ordersConfig.GetOrderMaxAmount())
		msgs = append(msgs, tradebinTypes.NewMsgCreateOrder(
			v.addressProvider.GetAddress().String(),
			orderType,
			amount.String(),
			priceKey(price),
			v.marketConfig.GetMarketId(),
		))
	}

	return msgs
}

func priceKey(price math.LegacyDec) string {
	return internal.TrimAmountTrailingZeros(price.String())
}
