package thorclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var oracleSymbol = regexp.MustCompile(`^[A-Z0-9]{1,32}$`)

// GetOraclePrice returns a positive USD price per whole asset in 1e8 base units,
// read from the enshrined oracle. It bypasses the response cache so a missing
// feed cannot reuse an old price.
// Errors return zero; the caller may fall back to pool pricing.
func (b *thorchainBridge) GetOraclePrice(symbol string) (cosmos.Uint, error) {
	if !oracleSymbol.MatchString(symbol) {
		return cosmos.ZeroUint(), fmt.Errorf("invalid oracle symbol %q", symbol)
	}
	buf, _, err := b.getWithPathUncached("thorchain/oracle/price/" + symbol)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get oracle price for %s: %w", symbol, err)
	}
	var response stypes.QueryOraclePriceResponse
	if err = json.Unmarshal(buf, &response); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("decode oracle price: %w", err)
	}
	if response.Price == nil || response.Price.Symbol != symbol || response.Price.Price == "" {
		return cosmos.ZeroUint(), fmt.Errorf("missing or mismatched oracle price for %s", symbol)
	}
	return parseOraclePrice(response.Price.Price)
}

var decimalOraclePrice = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// parseOraclePrice converts a decimal USD price to 1e8 base units. Round up a
// fractional base unit so converting the price cannot lower the reported value.
func parseOraclePrice(price string) (cosmos.Uint, error) {
	if len(price) > 100 || !decimalOraclePrice.MatchString(price) {
		return cosmos.ZeroUint(), errors.New("invalid oracle price")
	}
	whole, fraction, _ := strings.Cut(price, ".")
	value, ok := new(big.Int).SetString(whole+fraction, 10)
	if !ok || value.Sign() <= 0 {
		return cosmos.ZeroUint(), errors.New("oracle price must be positive")
	}
	// The validated input length bounds both the integer and the decimal scale.
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction))), nil)
	// For a positive price, ceil(n/d) = (n+d-1)/d.
	value.Mul(value, big.NewInt(common.One))
	value.Add(value, denominator)
	value.Sub(value, big.NewInt(1))
	value.Quo(value, denominator)
	if value.BitLen() > sdkmath.MaxBitLen {
		return cosmos.ZeroUint(), errors.New("oracle price overflows Uint")
	}
	return cosmos.NewUintFromBigInt(value), nil
}
