package types

import (
	"testing"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"
)

func TestDynamicFeeTypes(t *testing.T) { TestingT(t) }

type DynamicFeeTypesSuite struct{}

var _ = Suite(&DynamicFeeTypesSuite{})

func (s *DynamicFeeTypesSuite) TestNormalizePairKey(c *C) {
	a, b := NormalizePairAssets("ETH.ETH", "BTC.BTC")
	c.Check(a, Equals, "BTC.BTC")
	c.Check(b, Equals, "ETH.ETH")

	a, b = NormalizePairAssets("AVAX.AVAX", "ETH.ETH")
	c.Check(a, Equals, "AVAX.AVAX")
	c.Check(b, Equals, "ETH.ETH")

	a, b = NormalizePairAssets("THOR.RUNE", "BTC.BTC")
	c.Check(a, Equals, "BTC.BTC")
	c.Check(b, Equals, "THOR.RUNE")
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeRecordKey(c *C) {
	rec := DynamicFeeRecord{
		Thorname:   "myname",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
	}
	c.Check(rec.Key(), Equals, "MYNAME|BTC.BTC|ETH.ETH")
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeAccumulatorKey(c *C) {
	acc := DynamicFeeAccumulator{
		Thorname:   "myname",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
	}
	c.Check(acc.Key(), Equals, "MYNAME|BTC.BTC|ETH.ETH")
}

func (s *DynamicFeeTypesSuite) TestAppendAndCapHistory(c *C) {
	rec := DynamicFeeRecord{
		Thorname:   "test",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
	}
	for i := uint64(0); i < 31; i++ {
		rec.AppendHistory(DynamicFeeEpochRecord{Epoch: i})
	}
	c.Check(len(rec.History), Equals, 30)
	c.Check(rec.History[0].Epoch, Equals, uint64(1))
	c.Check(rec.History[29].Epoch, Equals, uint64(30))
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeRecordLatestHistory(c *C) {
	rec := DynamicFeeRecord{}
	_, ok := rec.LatestHistory()
	c.Check(ok, Equals, false)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 1, BpsAtClose: 10})
	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 2, BpsAtClose: 20})
	latest, ok := rec.LatestHistory()
	c.Check(ok, Equals, true)
	c.Check(latest.Epoch, Equals, uint64(2))
	c.Check(latest.BpsAtClose, Equals, uint64(20))
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeRecordLastBpsChangeIndex(c *C) {
	rec := DynamicFeeRecord{}
	_, ok := rec.LastBpsChangeIndex()
	c.Check(ok, Equals, false)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 1, BpsAtClose: 5})
	_, ok = rec.LastBpsChangeIndex()
	c.Check(ok, Equals, false)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 2, BpsAtClose: 5})
	_, ok = rec.LastBpsChangeIndex()
	c.Check(ok, Equals, false)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 3, BpsAtClose: 6})
	idx, ok := rec.LastBpsChangeIndex()
	c.Check(ok, Equals, true)
	c.Check(idx, Equals, 2)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 4, BpsAtClose: 6})
	idx, ok = rec.LastBpsChangeIndex()
	c.Check(ok, Equals, true)
	c.Check(idx, Equals, 2)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 5, BpsAtClose: 7})
	idx, ok = rec.LastBpsChangeIndex()
	c.Check(ok, Equals, true)
	c.Check(idx, Equals, 4)
}

func (s *DynamicFeeTypesSuite) TestNewDynamicFeeAccumulator(c *C) {
	acc := NewDynamicFeeAccumulator("myname", "BTC.BTC", "ETH.ETH")
	c.Check(acc.Thorname, Equals, "myname")
	c.Check(acc.PairAssetA, Equals, "BTC.BTC")
	c.Check(acc.PairAssetB, Equals, "ETH.ETH")
	c.Check(acc.VolumeTor.Equal(math.ZeroUint()), Equals, true)
	c.Check(acc.FeesTor.Equal(math.ZeroUint()), Equals, true)
}

func (s *DynamicFeeTypesSuite) TestNormalizePairAssetsEqual(c *C) {
	a, b := NormalizePairAssets("BTC.BTC", "BTC.BTC")
	c.Check(a, Equals, "BTC.BTC")
	c.Check(b, Equals, "BTC.BTC")
}
