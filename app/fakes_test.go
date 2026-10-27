package app_test

import (
	"errors"
	"io"

	"tradebin-mm/app/dto"

	basev1beta1 "cosmossdk.io/api/cosmos/base/v1beta1"
	"cosmossdk.io/math"
	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/sirupsen/logrus"

	"tradebin-mm/app/data_provider"
)

const (
	usdc = "uusdc"
	ubze = "ubze"
)

var errHaltedOnChain = errors.New("rpc error: code = Unknown desc = failed to execute message; message index: 0: denom is halted")

func quietLogger() logrus.FieldLogger {
	l := logrus.New()
	l.SetOutput(io.Discard)

	return l
}

type fakeAddress struct{}

func (fakeAddress) GetAddress() sdk.AccAddress { return sdk.AccAddress("tradebin-mm-test-addr") }

type fakeHalts struct {
	halted map[string]bool
	err    error
}

func (f *fakeHalts) IsDenomHalted(denom string) (bool, error) {
	return f.halted[denom], f.err
}

type fakePools struct{ pool *tradebinTypes.LiquidityPool }

func (f *fakePools) GetLiquidityPool(string) (*tradebinTypes.LiquidityPool, error) {
	return f.pool, nil
}

// pool returns a USDC/BZE pool priced at 3000 ubze per uusdc (base uusdc, like the mainnet pool)
func pool(reserveUsdc, reserveBze int64) *tradebinTypes.LiquidityPool {
	return &tradebinTypes.LiquidityPool{
		Id:           usdc + "_" + ubze,
		Base:         usdc,
		Quote:        ubze,
		ReserveBase:  math.NewInt(reserveUsdc),
		ReserveQuote: math.NewInt(reserveBze),
	}
}

type fakeBalances struct{ coins map[string]int64 }

func (f *fakeBalances) GetAddressBalances(string) ([]*basev1beta1.Coin, error) {
	var res []*basev1beta1.Coin
	for denom, amount := range f.coins {
		res = append(res, &basev1beta1.Coin{Denom: denom, Amount: math.NewInt(amount).String()})
	}

	return res, nil
}

func (f *fakeBalances) GetAddressBalancesForMarket(_ string, m data_provider.MarketProvider) (*dto.MarketBalance, error) {
	base := sdk.NewInt64Coin(m.GetBaseDenom(), f.coins[m.GetBaseDenom()])
	quote := sdk.NewInt64Coin(m.GetQuoteDenom(), f.coins[m.GetQuoteDenom()])

	return &dto.MarketBalance{MarketId: m.GetMarketId(), BaseBalance: &base, QuoteBalance: &quote}, nil
}

func (f *fakeBalances) GetMarketBalance(address string, m data_provider.MarketProvider) (*dto.MarketBalance, error) {
	return f.GetAddressBalancesForMarket(address, m)
}

type fakeBroadcaster struct {
	sent [][]sdk.Msg
	err  error
}

func (f *fakeBroadcaster) BroadcastBlock(msgs []sdk.Msg) error {
	f.sent = append(f.sent, msgs)

	return f.err
}

type fakeOrders struct {
	buys, sells []tradebinTypes.Order
	last        *tradebinTypes.HistoryOrder
}

func (f *fakeOrders) GetActiveOrders(*dto.MarketBalance) ([]tradebinTypes.AggregatedOrder, []tradebinTypes.AggregatedOrder, error) {
	return nil, nil, nil
}

func (f *fakeOrders) GetAddressActiveOrders(string, string, int) ([]tradebinTypes.Order, []tradebinTypes.Order, error) {
	return f.buys, f.sells, nil
}

func (f *fakeOrders) GetLastMarketOrder(string) (*tradebinTypes.HistoryOrder, error) {
	return f.last, nil
}

type fakeSubmitter struct {
	cancels [][]*tradebinTypes.MsgCancelOrder
	adds    [][]*tradebinTypes.MsgCreateOrder
	addErr  error
}

func (f *fakeSubmitter) CancelOrders(msgs []*tradebinTypes.MsgCancelOrder) error {
	f.cancels = append(f.cancels, msgs)

	return nil
}

func (f *fakeSubmitter) AddOrders(msgs []*tradebinTypes.MsgCreateOrder) error {
	f.adds = append(f.adds, msgs)

	return f.addErr
}
