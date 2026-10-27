package app_test

import (
	"errors"
	"sort"
	"testing"
	"time"

	"tradebin-mm/app"
	"tradebin-mm/config"

	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/stretchr/testify/require"
)

type ordersFixture struct {
	market    *config.Market
	orders    *fakeOrders
	submitter *fakeSubmitter
	halts     *fakeHalts
	pools     *fakePools
	filler    *app.Orders
}

// newOrdersFixture builds a BZE/USDC market (base ubze) anchored to a pool priced at 3000 ubze per uusdc,
// so the reference price is 0.000333 uusdc per ubze with a 0.000001 step.
func newOrdersFixture(t *testing.T) *ordersFixture {
	t.Helper()
	f := &ordersFixture{
		orders:    &fakeOrders{},
		submitter: &fakeSubmitter{},
		halts:     &fakeHalts{},
		pools:     &fakePools{pool: pool(1_000_000_000, 3_000_000_000_000)},
	}
	f.market = &config.Market{BaseDenom: ubze, QuoteDenom: usdc, PoolId: f.pools.pool.Id}
	ordersCfg := &config.Orders{Buy: 3, Sell: 3, Step: "0.000001", SpreadSteps: 2, StartPrice: "1", MinAmount: 100, MaxAmount: 100}
	balances := &fakeBalances{coins: map[string]int64{ubze: 1_000_000_000, usdc: 1_000_000}}

	filler, err := app.NewOrdersFiller(quietLogger(), ordersCfg, f.market, balances, fakeAddress{}, f.orders, f.submitter, f.halts, f.pools)
	require.NoError(t, err)
	f.filler = filler

	return f
}

func ownOrder(id, orderType, price string) tradebinTypes.Order {
	return tradebinTypes.Order{Id: id, MarketId: ubze + "/" + usdc, OrderType: orderType, Amount: "100", Price: price}
}

func placedPrices(adds [][]*tradebinTypes.MsgCreateOrder, orderType string) []string {
	var prices []string
	for _, batch := range adds {
		for _, m := range batch {
			if m.OrderType == orderType {
				prices = append(prices, m.Price)
			}
		}
	}
	sort.Strings(prices)

	return prices
}

func cancelledIds(cancels [][]*tradebinTypes.MsgCancelOrder) []string {
	var ids []string
	for _, batch := range cancels {
		for _, m := range batch {
			ids = append(ids, m.OrderId)
		}
	}
	sort.Strings(ids)

	return ids
}

func TestOrders_LadderAroundPoolReference(t *testing.T) {
	f := newOrdersFixture(t)

	require.NoError(t, f.filler.FillOrderBook())

	require.Len(t, f.submitter.adds, 1, "all levels in one tx")
	require.Equal(t, []string{"0.000334", "0.000335", "0.000336"}, placedPrices(f.submitter.adds, tradebinTypes.OrderTypeSell))
	require.Equal(t, []string{"0.000329", "0.00033", "0.000331"}, placedPrices(f.submitter.adds, tradebinTypes.OrderTypeBuy))
	for _, m := range f.submitter.adds[0] {
		require.Equal(t, ubze+"/"+usdc, m.MarketId)
		require.Equal(t, "100", m.Amount)
	}
	require.Empty(t, f.submitter.cancels)
}

func TestOrders_ReferenceWhenPoolHasMarketOrientation(t *testing.T) {
	f := newOrdersFixture(t)
	f.pools.pool = &tradebinTypes.LiquidityPool{
		Id: ubze + "_" + usdc, Base: ubze, Quote: usdc,
		ReserveBase: math.NewInt(3_000_000_000_000), ReserveQuote: math.NewInt(1_000_000_000),
	}

	require.NoError(t, f.filler.FillOrderBook())

	require.Equal(t, []string{"0.000334", "0.000335", "0.000336"}, placedPrices(f.submitter.adds, tradebinTypes.OrderTypeSell))
	require.Equal(t, []string{"0.000329", "0.00033", "0.000331"}, placedPrices(f.submitter.adds, tradebinTypes.OrderTypeBuy))
}

func TestOrders_RepricesOnlyOffTargetOrders(t *testing.T) {
	f := newOrdersFixture(t)
	f.orders.sells = []tradebinTypes.Order{
		ownOrder("s-keep", tradebinTypes.OrderTypeSell, "0.000334000000000000"),
		ownOrder("s-stale", tradebinTypes.OrderTypeSell, "0.000400000000000000"),
	}
	f.orders.buys = []tradebinTypes.Order{
		ownOrder("b-keep", tradebinTypes.OrderTypeBuy, "0.000331000000000000"),
		ownOrder("b-dup", tradebinTypes.OrderTypeBuy, "0.000331000000000000"),
		ownOrder("b-keep2", tradebinTypes.OrderTypeBuy, "0.000329000000000000"),
	}

	require.NoError(t, f.filler.FillOrderBook())

	require.Len(t, f.submitter.cancels, 1, "all cancels in one tx")
	require.Equal(t, []string{"b-dup", "s-stale"}, cancelledIds(f.submitter.cancels))
	require.Len(t, f.submitter.adds, 1)
	require.Equal(t, []string{"0.000335", "0.000336"}, placedPrices(f.submitter.adds, tradebinTypes.OrderTypeSell))
	require.Equal(t, []string{"0.00033"}, placedPrices(f.submitter.adds, tradebinTypes.OrderTypeBuy))
}

func TestOrders_NothingToDoWhenOnTarget(t *testing.T) {
	f := newOrdersFixture(t)
	for i, p := range []string{"0.000334", "0.000335", "0.000336"} {
		f.orders.sells = append(f.orders.sells, ownOrder("s"+string(rune('0'+i)), tradebinTypes.OrderTypeSell, p))
	}
	for i, p := range []string{"0.000331", "0.00033", "0.000329"} {
		f.orders.buys = append(f.orders.buys, ownOrder("b"+string(rune('0'+i)), tradebinTypes.OrderTypeBuy, p))
	}

	require.NoError(t, f.filler.FillOrderBook())

	require.Empty(t, f.submitter.cancels)
	require.Empty(t, f.submitter.adds)
}

func TestOrders_HoldsBackAfterForeignTrade(t *testing.T) {
	f := newOrdersFixture(t)
	f.orders.last = &tradebinTypes.HistoryOrder{Maker: "someone", Taker: "else", ExecutedAt: time.Now().Unix()}
	f.orders.sells = []tradebinTypes.Order{ownOrder("s-stale", tradebinTypes.OrderTypeSell, "0.0004")}

	require.NoError(t, f.filler.FillOrderBook())

	require.Empty(t, f.submitter.cancels)
	require.Empty(t, f.submitter.adds)
}

func TestOrders_HaltedDenomCancelsOwnOrdersAndSkips(t *testing.T) {
	f := newOrdersFixture(t)
	f.halts.halted = map[string]bool{usdc: true}
	f.orders.sells = []tradebinTypes.Order{ownOrder("s1", tradebinTypes.OrderTypeSell, "0.000334")}
	f.orders.buys = []tradebinTypes.Order{ownOrder("b1", tradebinTypes.OrderTypeBuy, "0.000331")}

	err := f.filler.FillOrderBook()

	require.ErrorIs(t, err, app.ErrMarketHalted)
	require.Equal(t, []string{"b1", "s1"}, cancelledIds(f.submitter.cancels))
	require.Empty(t, f.submitter.adds)
}

func TestOrders_HaltRefusalOnBroadcastExitsMarket(t *testing.T) {
	f := newOrdersFixture(t)
	f.submitter.addErr = errHaltedOnChain
	f.orders.sells = []tradebinTypes.Order{ownOrder("s1", tradebinTypes.OrderTypeSell, "0.000334")}

	err := f.filler.FillOrderBook()

	require.ErrorIs(t, err, app.ErrMarketHalted)
	require.Len(t, f.submitter.adds, 1, "one attempt, no retry")
	require.Equal(t, []string{"s1"}, cancelledIds(f.submitter.cancels))
}

func TestOrders_OtherBroadcastErrorsAreReturned(t *testing.T) {
	f := newOrdersFixture(t)
	f.submitter.addErr = errors.New("insufficient fees")

	err := f.filler.FillOrderBook()

	require.Error(t, err)
	require.NotErrorIs(t, err, app.ErrMarketHalted)
	require.Empty(t, f.submitter.cancels)
}

func TestOrders_PoolWithOtherDenomsIsRejected(t *testing.T) {
	f := newOrdersFixture(t)
	f.pools.pool.Base = "uatom"

	require.Error(t, f.filler.FillOrderBook())
	require.Empty(t, f.submitter.adds)
}
