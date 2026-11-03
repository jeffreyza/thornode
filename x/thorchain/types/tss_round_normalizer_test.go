package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/tssrounds"
)

type TssRoundNormalizerSuite struct{}

var _ = Suite(&TssRoundNormalizerSuite{})

func (s *TssRoundNormalizerSuite) TestNormalizeTssKeysignRoundAcceptsFrostRounds(c *C) {
	validRounds := []string{
		tssrounds.FROSTKEYSIGNPREPROCESS,
		tssrounds.FROSTKEYSIGNSHARE,
		tssrounds.FROSTKEYSIGNRESULT,
		tssrounds.KEYSIGN9,
		tssrounds.EDDSAKEYSIGN3,
	}

	for _, round := range validRounds {
		normalized, err := NormalizeTssKeysignRound(round)
		c.Assert(err, IsNil)
		c.Assert(normalized, Equals, round)
	}

	normalized, err := NormalizeTssKeysignRound("bogus_round")
	c.Assert(err, NotNil)
	c.Assert(normalized, Equals, "")
}
