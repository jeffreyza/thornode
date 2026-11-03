package types

import (
	"math/rand"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type VoterSignersSuite struct{}

var _ = Suite(&VoterSignersSuite{})

// the GetSigners + IsNodeKeys / Equals loops HasConsensus and HasSigned used
// before countActiveSigners and signersInclude
func refCountActiveSigners(signers []string, nas NodeAccounts) int {
	count := 0
	for _, a := range signers {
		addr, err := cosmos.AccAddressFromBech32(a)
		if err != nil {
			continue
		}
		if nas.IsNodeKeys(addr) {
			count++
		}
	}
	return count
}

func refSignersInclude(signers []string, signer cosmos.AccAddress) bool {
	for _, a := range signers {
		addr, err := cosmos.AccAddressFromBech32(a)
		if err != nil {
			continue
		}
		if addr.Equals(signer) {
			return true
		}
	}
	return false
}

func (VoterSignersSuite) TestMatchesDecodeAndScan(c *C) {
	rng := rand.New(rand.NewSource(7)) // #nosec G404 deterministic test data
	addrs := make([]cosmos.AccAddress, 12)
	for i := range addrs {
		addrs[i] = GetRandomBech32Addr()
	}
	statuses := []NodeStatus{NodeStatus_Active, NodeStatus_Active, NodeStatus_Standby, NodeStatus_Disabled}
	for round := 0; round < 500; round++ {
		var nas NodeAccounts
		for i := rng.Intn(10); i > 0; i-- {
			na := NodeAccount{Status: statuses[rng.Intn(len(statuses))]}
			if rng.Intn(10) > 0 {
				na.NodeAddress = addrs[rng.Intn(len(addrs))] // repeats included
			}
			nas = append(nas, na)
		}
		var signers []string
		for i := rng.Intn(12); i > 0; i-- {
			s := addrs[rng.Intn(len(addrs))].String()
			switch rng.Intn(6) {
			case 0:
				s = strings.ToUpper(s) // decodes to the same address
			case 1:
				s = s[:len(s)-1] + "q" // bad checksum: skipped
			case 2:
				s = ""
			}
			signers = append(signers, s)
		}
		c.Assert(countActiveSigners(signers, nas), Equals, refCountActiveSigners(signers, nas), Commentf("round %d", round))
		for _, signer := range []cosmos.AccAddress{addrs[rng.Intn(len(addrs))], nil, {}} {
			c.Assert(signersInclude(signers, signer), Equals, refSignersInclude(signers, signer), Commentf("round %d", round))
		}

		fee := ObservedNetworkFeeVoter{Signers: signers}
		solvency := SolvencyVoter{Signers: signers}
		c.Assert(fee.HasConsensus(nas), Equals, HasSuperMajority(refCountActiveSigners(signers, nas), len(nas)))
		c.Assert(solvency.HasConsensus(nas), Equals, HasMinority(refCountActiveSigners(signers, nas), len(nas)))
		signer := addrs[rng.Intn(len(addrs))]
		c.Assert(fee.HasSigned(signer), Equals, refSignersInclude(signers, signer))
		c.Assert(solvency.HasSigned(signer), Equals, refSignersInclude(signers, signer))
	}
}
