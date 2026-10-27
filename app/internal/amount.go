package internal

import (
	"fmt"
	"strings"

	sdkMath "cosmossdk.io/math"
)

func TrimAmountTrailingZeros(amount string) string {
	if !strings.Contains(amount, ".") {
		return amount
	}

	result := strings.TrimRight(amount, "0") // Remove trailing zeros
	result = strings.TrimSuffix(result, ".") // Remove decimal point if no fractional part

	return result
}

// TruncateToStep rounds num down to the nearest multiple of step (exact decimal math, any step size)
func TruncateToStep(num, step *sdkMath.LegacyDec) (*sdkMath.LegacyDec, error) {
	if step == nil || !step.IsPositive() {
		return nil, fmt.Errorf("step must be positive")
	}

	truncated := num.Quo(*step).TruncateDec().Mul(*step)

	return &truncated, nil
}
