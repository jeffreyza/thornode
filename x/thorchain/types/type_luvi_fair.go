package types

import (
	"errors"
	"fmt"
	"math/big"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Fair LP pricing (in the style of Alpha Homora's fair LP price) values the LP
// side of a pool from the k-invariant and an external price instead of the
// pool's own depths:
//
//	fair  = 2 * sqrt(A*R) * sqrt(P) - S*P        (RUNE)
//	spot  = 2R - S*R/A                           (RUNE, fair at P = R/A)
//
// where P is the oracle price of the asset in RUNE and S the synth supply, a
// liability the LPs owe. Swaps send their fee to the Reserve, so they leave
// sqrt(A*R) unchanged: a swap (or a sandwich) moves the spot value but not the
// fair value. P must come from an oracle, never from the pool, or the price is
// as manipulable as the spot value. Both values are divided by LP_units (not
// pool units: synth holders own the synth units) for the per-unit figures.
//
// Everything is big.Int fixed point at LuviPrecision decimals and read-only.

// luviMaxPriceLen bounds the length of a decimal price string accepted by
// NewLuviPriceFromUSD.
const luviMaxPriceLen = 128

// luviMaxDeviationBits keeps DeviationBps within sdkmath.Int's 256-bit bound.
const luviMaxDeviationBits = 192

// LuviPrice is the price of one unit of the asset in RUNE, as a fixed-point
// value scaled by 10^LuviPrecision. The zero value is the zero price.
type LuviPrice struct {
	v *big.Int
}

// IsZero reports whether the price is zero (or unset).
func (p LuviPrice) IsZero() bool { return p.v == nil || p.v.Sign() == 0 }

// Dec returns the price as a Dec (truncated to 18 decimals).
func (p LuviPrice) Dec() cosmos.Dec {
	if p.v == nil {
		return cosmos.ZeroDec()
	}
	return luviToDec(p.v)
}

// NewLuviPriceFromDec builds a price from a Dec RUNE-per-asset price. Negative
// values are rejected.
func NewLuviPriceFromDec(d cosmos.Dec) (LuviPrice, error) {
	if d.IsNil() || d.IsNegative() {
		return LuviPrice{}, errors.New("luvi: invalid price")
	}
	return LuviPrice{v: new(big.Int).Mul(d.BigInt(), luviDecDiv)}, nil
}

// NewLuviPriceFromUSD derives the RUNE price of the asset from the oracle's USD
// prices of the asset and of RUNE (decimal strings as stored by the price
// feed): P = assetUSD / runeUSD, truncated to LuviPrecision decimals. Both
// pool depths are in 1e8 base units, so the ratio needs no decimal adjustment.
func NewLuviPriceFromUSD(assetUSD, runeUSD string) (LuviPrice, error) {
	an, ad, err := parseLuviDecimal(assetUSD)
	if err != nil {
		return LuviPrice{}, fmt.Errorf("luvi: asset price: %w", err)
	}
	rn, rd, err := parseLuviDecimal(runeUSD)
	if err != nil {
		return LuviPrice{}, fmt.Errorf("luvi: rune price: %w", err)
	}
	if an.Sign() == 0 || rn.Sign() == 0 {
		return LuviPrice{}, errors.New("luvi: zero price")
	}
	// (an/ad) / (rn/rd) = an*rd / (ad*rn)
	num := new(big.Int).Mul(an, rd)
	num.Mul(num, luviScale)
	den := new(big.Int).Mul(ad, rn)
	return LuviPrice{v: num.Quo(num, den)}, nil
}

// parseLuviDecimal parses a non-negative plain decimal ("123", "0.5", "1.")
// into num/den with den a power of ten. Signs, exponents and anything else are
// rejected, so the result never depends on float parsing.
func parseLuviDecimal(s string) (*big.Int, *big.Int, error) {
	if s == "" || len(s) > luviMaxPriceLen {
		return nil, nil, fmt.Errorf("invalid decimal %q", s)
	}
	digits := make([]byte, 0, len(s))
	frac, dot := 0, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= '0' && ch <= '9':
			digits = append(digits, ch)
			if dot {
				frac++
			}
		case ch == '.' && !dot:
			dot = true
		default:
			return nil, nil, fmt.Errorf("invalid decimal %q", s)
		}
	}
	if len(digits) == 0 {
		return nil, nil, fmt.Errorf("invalid decimal %q", s)
	}
	num, ok := new(big.Int).SetString(string(digits), 10)
	if !ok {
		return nil, nil, fmt.Errorf("invalid decimal %q", s)
	}
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(frac)), nil)
	return num, den, nil
}

// SpotPrice returns the pool's own price R/A in RUNE per asset (zero when A is
// zero). It is manipulable by swaps: use it for comparison, never as the
// oracle input of FairValue.
func (s LuviState) SpotPrice() LuviPrice {
	if s.BalanceAsset.IsZero() {
		return LuviPrice{v: new(big.Int)}
	}
	v := new(big.Int).Mul(s.BalanceRune.BigInt(), luviScale)
	return LuviPrice{v: v.Quo(v, s.BalanceAsset.BigInt())}
}

// LuviFairValue is the fair (oracle-priced) and spot value of a pool's LP side.
type LuviFairValue struct {
	// OraclePriceRune is P, the oracle price of one asset unit in RUNE.
	OraclePriceRune cosmos.Dec
	// FairValueRune is 2*sqrt(A*R)*sqrt(P) - S*P, signed: zero or negative
	// when the synth liability exceeds the pool's fair value.
	FairValueRune cosmos.Dec
	// SpotValueRune is 2R - S*R/A, signed.
	SpotValueRune cosmos.Dec
	// UnitFairValueRune is max(FairValueRune, 0) / LP_units.
	UnitFairValueRune cosmos.Dec
	// UnitFairValueAsset is UnitFairValueRune / P.
	UnitFairValueAsset cosmos.Dec
	// UnitSpotValueRune is max(SpotValueRune, 0) / LP_units.
	UnitSpotValueRune cosmos.Dec
	// DeviationBps is (spot - fair) / fair in basis points, both clamped at
	// zero: how far the pool's own price has been pushed from the oracle, as
	// seen by an LP unit. Only set when HasDeviation.
	DeviationBps sdkmath.Int
	// HasDeviation is false when the fair value is not positive.
	HasDeviation bool
	// Overhang reports a fair value <= 0: the LP side is worth nothing at the
	// oracle price and the per-unit values are clamped to zero.
	Overhang bool
}

// FairValue values the LP side of the pool at the oracle price p. It fails
// on an invalid state (see IsValid) or a zero price.
func (s LuviState) FairValue(p LuviPrice) (LuviFairValue, error) {
	if !s.IsValid() {
		return LuviFairValue{}, errors.New("luvi: invalid state")
	}
	if p.IsZero() || p.v.Sign() < 0 {
		return LuviFairValue{}, errors.New("luvi: invalid price")
	}
	lpUnits := s.LPUnits.BigInt()
	synth := s.SynthSupply.BigInt()

	// fair = 2*sqrt(A*R)*sqrt(P) - S*P, all scaled by luviScale
	sqrtP := new(big.Int).Mul(p.v, luviScale)
	sqrtP.Sqrt(sqrtP)
	fair := new(big.Int).Mul(s.sqrtAR(), sqrtP)
	fair.Lsh(fair, 1)
	fair.Quo(fair, luviScale)
	fair.Sub(fair, new(big.Int).Mul(synth, p.v))

	// spot = (2R*A - S*R) / A, scaled
	r := s.BalanceRune.BigInt()
	spot := new(big.Int).Lsh(new(big.Int).Mul(r, s.BalanceAsset.BigInt()), 1)
	spot.Sub(spot, new(big.Int).Mul(synth, r))
	spot.Mul(spot, luviScale)
	spot.Quo(spot, s.BalanceAsset.BigInt())

	fairPos := clampLuviZero(fair)
	spotPos := clampLuviZero(spot)
	unitFair := new(big.Int).Quo(fairPos, lpUnits)
	unitSpot := new(big.Int).Quo(spotPos, lpUnits)
	unitFairAsset := new(big.Int).Mul(unitFair, luviScale)
	unitFairAsset.Quo(unitFairAsset, p.v)

	out := LuviFairValue{
		OraclePriceRune:    luviToDec(p.v),
		FairValueRune:      luviToDec(fair),
		SpotValueRune:      luviToDec(spot),
		UnitFairValueRune:  luviToDec(unitFair),
		UnitFairValueAsset: luviToDec(unitFairAsset),
		UnitSpotValueRune:  luviToDec(unitSpot),
		DeviationBps:       sdkmath.ZeroInt(),
		Overhang:           fair.Sign() <= 0,
	}
	if fairPos.Sign() > 0 {
		dev := new(big.Int).Sub(spotPos, fairPos)
		dev.Mul(dev, big.NewInt(10_000))
		dev.Quo(dev, fairPos)
		// a fair value of a few 1e-36 RUNE against a real spot value cannot be
		// expressed as an Int; it is overhang in all but name
		if dev.BitLen() <= luviMaxDeviationBits {
			out.DeviationBps = sdkmath.NewIntFromBigInt(dev)
			out.HasDeviation = true
		}
	}
	return out, nil
}

func clampLuviZero(x *big.Int) *big.Int {
	if x.Sign() < 0 {
		return new(big.Int)
	}
	return x
}
