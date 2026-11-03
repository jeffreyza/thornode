package common

import (
	"math"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (s ObservedTxSuite) TestXMRInboundObservationCredit(c *C) {
	for _, final := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			change func(*ObservedTx, *ObservedTx)
			want   bool
		}{
			{"legacy and mined", func(_, _ *ObservedTx) {}, true},
			{"different gas", func(_, b *ObservedTx) { b.Tx.Gas = Gas{NewCoin(XMRAsset, cosmos.NewUint(500))} }, true},
			{"different memo", func(_, b *ObservedTx) { b.Tx.Memo = "different-memo" }, false},
			{"different vault", func(_, b *ObservedTx) { b.ObservedPubKey = PubKey("different-vault") }, false},
			{"different coins", func(_, b *ObservedTx) { b.Tx.Coins = NewCoins(NewCoin(XMRAsset, cosmos.NewUint(2*One))) }, false},
			{"different tx key", func(_, b *ObservedTx) { b.XmrTxKey = strings.Repeat("b", 64) }, false},
			{"conflicting refs", func(_, b *ObservedTx) {
				b.SpentOutputRefs = []OutputRef{makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)}
			}, false},
			{"invalid legacy height", func(a, _ *ObservedTx) { a.BlockHeight = 0 }, false},
			{"invalid mined height", func(_, b *ObservedTx) { b.XmrMinedHeight = b.BlockHeight + 1 }, false},
			{"negative mined height", func(_, b *ObservedTx) { b.XmrMinedHeight = -1 }, false},
			{"different concrete heights", func(a, _ *ObservedTx) { a.XmrMinedHeight = 99 }, false},
			{"different finality", func(a, _ *ObservedTx) {
				if final {
					a.BlockHeight = 100
				} else {
					a.BlockHeight = a.FinaliseHeight
				}
			}, false},
		} {
			legacy := makeXMRObservedTx(c, []OutputRef{makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)})
			legacy.BlockHeight, legacy.FinaliseHeight = 100, 110
			if final {
				legacy.BlockHeight = legacy.FinaliseHeight
			}
			mined := legacy
			mined.XmrMinedHeight = 100
			tc.change(&legacy, &mined)
			beforeLegacy, beforeMined := legacy.XmrMinedHeight, mined.XmrMinedHeight
			c.Assert(legacy.EqualsInboundObservationCredit(mined), Equals, tc.want, Commentf("final=%v %s", final, tc.name))
			c.Assert(mined.EqualsInboundObservationCredit(legacy), Equals, tc.want, Commentf("final=%v %s", final, tc.name))
			c.Assert(legacy.XmrMinedHeight, Equals, beforeLegacy)
			c.Assert(mined.XmrMinedHeight, Equals, beforeMined)
			c.Assert(legacy.EqualsConsensusIgnoreGas(mined), Equals, false)
			c.Assert(legacy.EqualsConsensus(mined), Equals, false)
			c.Assert(legacy.AttestationUniqueHash() == mined.AttestationUniqueHash(), Equals, false)
		}
	}
}

func (s ObservedTxSuite) TestXMROutboundObservationCredit(c *C) {
	for _, tc := range []struct {
		name   string
		change func(*ObservedTx, *ObservedTx)
		want   bool
	}{
		{"pre-mining and mined", func(_, _ *ObservedTx) {}, true},
		{"same gas", func(a, b *ObservedTx) { b.Tx.Gas = a.Tx.Gas }, true},
		{"missing optional refs", func(a, _ *ObservedTx) { a.SpentOutputRefs = nil }, true},
		{"conflicting refs", func(_, b *ObservedTx) {
			b.SpentOutputRefs = []OutputRef{makeRef(testRefTxHashB, 1, testKeyImageB, testSpendTxHash)}
		}, false},
		{"different tx key", func(_, b *ObservedTx) { b.XmrTxKey = strings.Repeat("b", 64) }, false},
		{"different vault", func(_, b *ObservedTx) { b.ObservedPubKey = PubKey("different-vault") }, false},
		{"different memo", func(_, b *ObservedTx) { b.Tx.Memo = "different-memo" }, false},
		{"different coins", func(_, b *ObservedTx) {
			b.Tx.Coins = NewCoins(NewCoin(XMRAsset, cosmos.NewUint(2*One)))
		}, false},
		{"non-final pre-mining", func(a, _ *ObservedTx) { a.FinaliseHeight++ }, false},
		{"non-final mined", func(_, b *ObservedTx) { b.BlockHeight = b.XmrMinedHeight }, false},
		{"legacy non-final and mined", func(a, b *ObservedTx) {
			a.BlockHeight, a.FinaliseHeight, b.BlockHeight = 100, 110, 100
		}, true},
		{"legacy non-final before mined block", func(a, b *ObservedTx) {
			a.BlockHeight, a.FinaliseHeight, b.BlockHeight = 99, 110, 100
		}, false},
		{"legacy non-final after mined block", func(a, b *ObservedTx) {
			a.BlockHeight, a.FinaliseHeight, b.BlockHeight = 101, 110, 100
		}, false},
		{"invalid legacy non-final height", func(a, b *ObservedTx) {
			a.BlockHeight, a.FinaliseHeight, b.BlockHeight = 0, 110, 100
		}, false},
		{"invalid pre-mining height", func(a, _ *ObservedTx) { a.BlockHeight, a.FinaliseHeight = 0, 0 }, false},
		{"invalid mined height", func(_, b *ObservedTx) { b.XmrMinedHeight = b.BlockHeight + 1 }, false},
		{"negative mined height", func(_, b *ObservedTx) { b.XmrMinedHeight = -1 }, false},
		{"different concrete locations", func(a, _ *ObservedTx) {
			a.BlockHeight, a.FinaliseHeight, a.XmrMinedHeight = 110, 110, 99
		}, false},
	} {
		instant := makeXMRObservedTx(c, []OutputRef{makeRef(testRefTxHashA, 0, testKeyImageA, testSpendTxHash)})
		instant.BlockHeight, instant.FinaliseHeight = 90, 90
		instant.XmrTxKey = strings.Repeat("a", 64)
		mined := instant
		mined.BlockHeight, mined.FinaliseHeight, mined.XmrMinedHeight = 110, 110, 100
		mined.Tx.Gas = Gas{NewCoin(XMRAsset, cosmos.NewUint(500))}
		tc.change(&instant, &mined)
		beforeInstant, beforeMined := instant.XmrMinedHeight, mined.XmrMinedHeight
		c.Assert(instant.EqualsOutboundObservationCredit(mined), Equals, tc.want, Commentf("%s", tc.name))
		c.Assert(mined.EqualsOutboundObservationCredit(instant), Equals, tc.want, Commentf("%s", tc.name))
		c.Assert(instant.XmrMinedHeight, Equals, beforeInstant)
		c.Assert(mined.XmrMinedHeight, Equals, beforeMined)
		c.Assert(instant.EqualsConsensusIgnoreGas(mined), Equals, false, Commentf("%s", tc.name))
		c.Assert(instant.EqualsConsensus(mined), Equals, false, Commentf("%s", tc.name))
		c.Assert(instant.AttestationUniqueHash() == mined.AttestationUniqueHash(), Equals, false, Commentf("%s", tc.name))
	}
}

func (s ObservedTxSuite) TestXMRCanonicalMinedHeight(c *C) {
	for _, tc := range []struct {
		mined, reported, final int64
		valid                  bool
	}{
		{0, 100, 110, false},
		{-1, 100, 110, false},
		{100, 0, 110, false},
		{100, 100, 0, false},
		{100, 110, 100, false},
		{111, 110, 110, false},
		{99, 100, 110, false},
		{100, 100, 110, true},
		{100, 110, 110, true},
		{1, 1, 1, true},
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, true},
	} {
		tx := makeXMRObservedTx(c, nil)
		tx.XmrMinedHeight, tx.BlockHeight, tx.FinaliseHeight = tc.mined, tc.reported, tc.final
		height, err := tx.CanonicalMinedHeight()
		if tc.valid {
			c.Assert(err, IsNil)
			c.Assert(height, Equals, tc.mined)
			c.Assert(tx.Valid(), IsNil)
		} else {
			c.Assert(err, NotNil, Commentf("%+v", tc))
			if tc.mined != 0 {
				c.Assert(tx.Valid(), NotNil, Commentf("%+v", tc))
			}
		}
	}
	tx := makeXMRObservedTx(c, nil)
	c.Assert(tx.Valid(), IsNil) // pre-mining outbound representation
	tx.Tx.Chain = BTCChain
	height, err := tx.CanonicalMinedHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, tx.BlockHeight)
	tx.XmrMinedHeight = 100
	_, err = tx.CanonicalMinedHeight()
	c.Assert(err, NotNil)
}

func (s ObservedTxSuite) TestXMRMinedHeightIdentityAndWire(c *C) {
	zero := makeXMRObservedTx(c, nil)
	zero.BlockHeight, zero.FinaliseHeight = 110, 110
	mined := zero
	mined.XmrMinedHeight = 100
	other := mined
	other.XmrMinedHeight++
	for _, variant := range []ObservedTx{zero, other} {
		c.Assert(mined.Equals(variant), Equals, false)
		c.Assert(mined.EqualsConsensus(variant), Equals, false)
		c.Assert(mined.EqualsConsensusIgnoreGas(variant), Equals, false)
		c.Assert(mined.AttestationUniqueHash() == variant.AttestationUniqueHash(), Equals, false)
		for _, inbound := range []bool{false, true} {
			payload, err := mined.GetSignablePayloadWithInbound(inbound)
			c.Assert(err, IsNil)
			otherPayload, err := variant.GetSignablePayloadWithInbound(inbound)
			c.Assert(err, IsNil)
			c.Assert(string(payload) == string(otherPayload), Equals, false)
		}
	}
	for _, original := range []ObservedTx{zero, mined} {
		encoded, err := original.Marshal()
		c.Assert(err, IsNil)
		var decoded ObservedTx
		c.Assert(decoded.Unmarshal(encoded), IsNil)
		c.Assert(decoded.XmrMinedHeight, Equals, original.XmrMinedHeight)
		c.Assert(decoded.Equals(original), Equals, true)
		c.Assert(decoded.EqualsConsensus(original), Equals, true)
		c.Assert(decoded.EqualsConsensusIgnoreGas(original), Equals, true)
		c.Assert(decoded.AttestationUniqueHash(), Equals, original.AttestationUniqueHash())
	}
	// Missing protobuf field decodes as unavailable, not the reported height.
	var absent ObservedTx
	c.Assert(absent.Unmarshal([]byte{0x20, 100, 0x40, 100}), IsNil)
	absent.Tx.Chain = XMRChain
	_, err := absent.CanonicalMinedHeight()
	c.Assert(err, NotNil)
}

func (s ObservedTxSuite) TestNonXMRMinedHeightIdentityUnchanged(c *C) {
	for _, chain := range AllChains {
		c.Assert(chain.UsesHeightScopedErrata(), Equals, chain.Equals(XMRChain))
		if chain.UsesHeightScopedErrata() {
			continue
		}
		tx := makeXMRObservedTx(c, nil)
		tx.Tx.Chain = chain
		variant := tx
		variant.XmrMinedHeight = 99 // ignored by the pre-existing equality contract
		c.Assert(tx.Equals(variant), Equals, true)
		c.Assert(tx.EqualsConsensus(variant), Equals, true)
		c.Assert(tx.EqualsConsensusIgnoreGas(variant), Equals, true)
		c.Assert(tx.EqualsInboundObservationCredit(variant), Equals, true)
		c.Assert(tx.EqualsOutboundObservationCredit(variant), Equals, true)
		c.Assert(tx.AttestationUniqueHash(), Equals, tx.Tx.Hash(tx.BlockHeight))
		c.Assert(variant.AttestationUniqueHash(), Equals, tx.AttestationUniqueHash())
	}
}
