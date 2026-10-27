package internal

import (
	"errors"
	"fmt"
	"strings"

	tradebinTypes "github.com/bze-alphateam/bze/x/tradebin/types"
)

func NewInvalidDependenciesErr(name string) error {
	return fmt.Errorf("invalid dependencies for: %s", name)
}

// IsDenomHaltedErr reports whether err is the chain refusing a tx because one of its denoms is halted
// (tradebin error 4017). Errors coming back over gRPC lose their type, so the message is matched too.
func IsDenomHaltedErr(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, tradebinTypes.ErrDenomHalted) || strings.Contains(err.Error(), tradebinTypes.ErrDenomHalted.Error())
}
