package common

import (
	"math"
	"math/big"
	"math/rand"

	. "gopkg.in/check.v1"
)

type OraclePriceSuite struct{}

var _ = Suite(&OraclePriceSuite{})

func (s *OraclePriceSuite) TestNewOraclePrice(c *C) {
	testCases := []struct {
		Number           string
		ExpectedAmount   uint64
		ExpectedDecimals uint32
		ExpectedError    bool
	}{
		{
			Number:           "0.12",
			ExpectedAmount:   12,
			ExpectedDecimals: 2,
		},
		{
			Number:           "0.0000001",
			ExpectedAmount:   1,
			ExpectedDecimals: 7,
		},
		{
			Number:           "0.000000000238467",
			ExpectedAmount:   2384,
			ExpectedDecimals: 13,
		},
		{
			Number:           "0",
			ExpectedAmount:   0,
			ExpectedDecimals: 0,
		},
		{
			Number:           "4247545",
			ExpectedAmount:   4247545,
			ExpectedDecimals: 0,
		},
		{
			Number:           "337.00000000000",
			ExpectedAmount:   337,
			ExpectedDecimals: 0,
		},
		{
			Number:           "8473.67000000",
			ExpectedAmount:   847367,
			ExpectedDecimals: 2,
		},
		{
			Number:           "17.256",
			ExpectedAmount:   17256,
			ExpectedDecimals: 3,
		},
		{
			Number:           "488.1564861",
			ExpectedAmount:   4881564,
			ExpectedDecimals: 4,
		},
		{
			Number:           "5373.065",
			ExpectedAmount:   5373065,
			ExpectedDecimals: 3,
		},
		{
			Number:           "82646.00230323",
			ExpectedAmount:   82646002303,
			ExpectedDecimals: 6,
		},
		{
			Number:           "1.0020304",
			ExpectedAmount:   100203,
			ExpectedDecimals: 5,
		},
		{
			Number:           "684.00158",
			ExpectedAmount:   68400158,
			ExpectedDecimals: 5,
		},
		{
			Number:           "38738.0000072635252",
			ExpectedAmount:   38738000007263,
			ExpectedDecimals: 9,
		},
		{
			// integer already 18 chars long, no precision left
			Number:           "684682837625151837.827262",
			ExpectedAmount:   684682837625151837,
			ExpectedDecimals: 0,
		},
		{
			// integer 15 chars, 3 places left for precision
			Number:           "927365252827625.05468",
			ExpectedAmount:   927365252827625054,
			ExpectedDecimals: 3,
		},
		{
			// integer 14 chars, 4 places left for precision,only zeros
			Number:           "20474683987738.00005468",
			ExpectedAmount:   20474683987738,
			ExpectedDecimals: 0,
		},
		{
			// integer 19 chars, is too big
			Number:           "1636348227336527223",
			ExpectedAmount:   0,
			ExpectedDecimals: 0,
			ExpectedError:    true,
		},
		{
			// negative number should fail
			Number:           "-157.88",
			ExpectedAmount:   0,
			ExpectedDecimals: 0,
			ExpectedError:    true,
		},
	}

	for _, tc := range testCases {
		number, _, err := big.ParseFloat(tc.Number, 10, 128, big.ToNearestEven)
		c.Assert(err, IsNil, Commentf(tc.Number))

		price, err := NewOraclePrice(number)
		if tc.ExpectedError {
			c.Assert(err, NotNil, Commentf(tc.Number))
			c.Assert(price, IsNil)
		} else {
			c.Assert(err, IsNil, Commentf(tc.Number))
			c.Assert(price, NotNil, Commentf(tc.Number))
			c.Assert(price.Decimals, Equals, tc.ExpectedDecimals, Commentf(tc.Number))
			c.Assert(price.Amount, Equals, tc.ExpectedAmount, Commentf(tc.Number))
		}
	}
}

func (s *OraclePriceSuite) TestBigFloat(c *C) {
	// the big.Int formula BigFloat's precomputed powers of ten stand in for
	reference := func(p *OraclePrice) *big.Float {
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(p.Decimals)), nil)
		amount := new(big.Float).SetUint64(p.Amount)
		return new(big.Float).Quo(amount, new(big.Float).SetInt(divisor))
	}

	amounts := []uint64{
		0, 1, 2, 3, 7, 10, 999, 1000, 4881564, 82646002303,
		684682837625151837, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64,
	}
	r := rand.New(rand.NewSource(1)) // nolint:gosec // deterministic, reproducible test input
	for i := 0; i < 64; i++ {
		amounts = append(amounts, r.Uint64()>>i)
	}

	// past 19 decimals 10^d no longer fits in 64 bits
	var held [][2]*big.Float
	for d := uint32(0); d <= 24; d++ {
		for _, a := range amounts {
			p := &OraclePrice{Amount: a, Decimals: d}
			got, want := p.BigFloat(), reference(p)
			comment := Commentf("%d / 10^%d", a, d)
			c.Assert(got.Text('p', 0), Equals, want.Text('p', 0), comment)
			c.Assert(got.Prec(), Equals, want.Prec(), comment)
			c.Assert(got.Mode(), Equals, want.Mode(), comment)
			c.Assert(got.Acc(), Equals, want.Acc(), comment)

			scratch := p.BigFloat()
			scratch.Add(scratch, big.NewFloat(1))
			held = append(held, [2]*big.Float{got, want})
		}
	}

	// callers keep and change the Floats: every call hands out its own
	for _, h := range held {
		c.Assert(h[0].Text('p', 0), Equals, h[1].Text('p', 0))
	}
}
