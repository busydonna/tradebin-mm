package data_provider

import (
	"context"

	"tradebin-mm/app/internal"

	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
	"github.com/sirupsen/logrus"
)

type Halt struct {
	provider clientProvider
	logger   logrus.FieldLogger
}

func NewHaltProvider(logger logrus.FieldLogger, provider clientProvider) (*Halt, error) {
	if provider == nil || logger == nil {
		return nil, internal.NewInvalidDependenciesErr("NewHaltProvider")
	}

	return &Halt{
		provider: provider,
		logger:   logger.WithField("service", "DataProvider.Halt"),
	}, nil
}

// IsDenomHalted asks the chain whether trading in denom is halted (MsgHaltDenoms, chain v8.2.0+)
func (h *Halt) IsDenomHalted(denom string) (bool, error) {
	qc, err := h.provider.GetTradebinQueryClient()
	if err != nil {
		return false, err
	}

	res, err := qc.DenomHalted(context.Background(), &tradebinTypes.QueryDenomHaltedRequest{Denom: denom})
	if err != nil {
		return false, err
	}

	return res.GetHalted(), nil
}
