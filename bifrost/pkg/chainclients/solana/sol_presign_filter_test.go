package solana

import (
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// presignOutcome signs a durable outbound against the scenario built for it and
// reports whether it reached the keysign (signed) or was dropped by the pre-sign
// filters. setup may adjust the tx and returns the RPC scenario to serve.
func (s *SolTestSuite) presignOutcome(c *C, setup func(tx *stypes.TxOutItem, vaultAddr string) solRPCScenario) (signed bool, cl *SOLClient, tx stypes.TxOutItem) {
	f := newDurableFixture(c)
	tx = durableTestTx(c, f.vault)
	addr, err := f.vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	sc := presignScenario()
	if setup != nil {
		sc = setup(&tx, addr.String())
	}
	srv := sc.server(c)
	defer srv.Close()
	cl = s.durableTestClient(c, &committedNonceBridge{mockSOLThorchainBridge: &mockSOLThorchainBridge{}, committed: f.committed}, srv)
	cl.tssKeyManager = &mockSOLThorchainKeyManager{err: errors.New("keysign reached")}
	result, checkpoint, obs, err := cl.SignTx(tx, keysignHeight)
	if err != nil {
		c.Assert(err, ErrorMatches, ".*keysign reached.*")
		return true, cl, tx
	}
	c.Assert(result, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(obs, IsNil)
	return false, cl, tx
}

func presignScenario() solRPCScenario {
	return strandedHistoryScenario(testConsensusBlockTimeUnix - solFinalizationMarginSeconds)
}

func acct(owner string, executable bool, lamports uint64) string {
	return fmt.Sprintf(`{"owner":%q,"executable":%t,"lamports":%d,"data":["","base64"]}`, owner, executable, lamports)
}

// Baseline: a creditable funded recipient, a funded vault and a clean simulation sign.
func (s *SolTestSuite) TestPreSignFilterPasses(c *C) {
	signed, _, _ := s.presignOutcome(c, nil)
	c.Assert(signed, Equals, true)
}

const system = "11111111111111111111111111111111"

// Each failure the runtime would hit is refused before signing (dropped for
// reschedule, remembered for the serialize gate).
func (s *SolTestSuite) TestPreSignFilterRejects(c *C) {
	// durableTestTx sends 11047250 (1e8 units) = 110,472,500 lamports.
	const amount, fee = uint64(110_472_500), uint64(5000)
	cases := map[string]func(tx *stypes.TxOutItem, vault string) solRPCScenario{
		"recipient is a program": func(tx *stypes.TxOutItem, _ string) solRPCScenario {
			sc := presignScenario()
			sc.accounts = map[string]string{tx.ToAddress.String(): acct("BPFLoaderUpgradeab1e11111111111111111111111", true, 1)}
			return sc
		},
		"recipient is a sysvar": func(tx *stypes.TxOutItem, _ string) solRPCScenario {
			sc := presignScenario()
			sc.accounts = map[string]string{tx.ToAddress.String(): acct(solSysvarOwner, false, 1)}
			return sc
		},
		"new recipient below rent-exempt": func(tx *stypes.TxOutItem, _ string) solRPCScenario {
			tx.Coins = common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(50_000))} // 500,000 lamports
			sc := presignScenario()
			sc.accounts = map[string]string{tx.ToAddress.String(): "null"}
			return sc
		},
		"vault cannot pay": func(_ *stypes.TxOutItem, vault string) solRPCScenario {
			sc := presignScenario()
			sc.accounts = map[string]string{vault: acct(system, false, amount)}
			return sc
		},
		"vault left below rent-exempt": func(_ *stypes.TxOutItem, vault string) solRPCScenario {
			sc := presignScenario()
			sc.accounts = map[string]string{vault: acct(system, false, amount+fee+100)}
			return sc
		},
		"simulation fails": func(_ *stypes.TxOutItem, _ string) solRPCScenario {
			sc := presignScenario()
			sc.simErr = `{"InstructionError":[1,{"Custom":1}]}`
			return sc
		},
	}
	for name, setup := range cases {
		signed, cl, tx := s.presignOutcome(c, setup)
		c.Check(signed, Equals, false, Commentf(name))
		c.Check(cl.durableRejected(pendingOf(tx, keysignHeight)), Equals, true, Commentf(name))
	}
}

// Boundaries that must still sign: a new recipient funded at the rent-exempt minimum,
// and a vault drained exactly to zero.
func (s *SolTestSuite) TestPreSignFilterBoundaries(c *C) {
	signed, _, _ := s.presignOutcome(c, func(tx *stypes.TxOutItem, _ string) solRPCScenario {
		tx.Coins = common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(89_088))} // 890,880 lamports
		sc := presignScenario()
		sc.accounts = map[string]string{tx.ToAddress.String(): "null"}
		return sc
	})
	c.Check(signed, Equals, true, Commentf("recipient at rent-exempt"))

	signed, _, _ = s.presignOutcome(c, func(_ *stypes.TxOutItem, vault string) solRPCScenario {
		sc := presignScenario()
		sc.accounts = map[string]string{vault: acct(system, false, 110_472_500+5000)}
		return sc
	})
	c.Check(signed, Equals, true, Commentf("vault drained to zero"))
}

// RPC failures fail OPEN: the filters must never become a liveness gate.
func (s *SolTestSuite) TestPreSignFilterFailsOpen(c *C) {
	signed, _, _ := s.presignOutcome(c, func(tx *stypes.TxOutItem, vault string) solRPCScenario {
		sc := presignScenario()
		sc.simErr = "rpcerror"
		sc.accounts = map[string]string{tx.ToAddress.String(): "rpcerror", vault: "rpcerror"}
		return sc
	})
	c.Assert(signed, Equals, true)
}

func pendingOf(tx stypes.TxOutItem, height int64) thorclient.PendingOutbound {
	return thorclient.PendingOutbound{
		Chain: common.SOLChain.String(), ToAddress: tx.ToAddress.String(),
		VaultPubKey: tx.VaultPubKey.String(), VaultPubKeyEddsa: tx.VaultPubKeyEddsa.String(),
		Coin: thorclient.PendingOutboundCoin{Asset: tx.Coins[0].Asset.String(), Amount: tx.Coins[0].Amount.String()},
		Memo: tx.Memo, InHash: tx.InHash.String(), Height: height,
	}
}

// A lower-height sibling this node refused to sign does not hold later siblings
// behind it in the serialize gate (else one bad outbound would stall the vault).
func (s *SolTestSuite) TestSerializeGateSkipsRejectedSibling(c *C) {
	mk := func(h int64, hash string) stypes.TxOutItem {
		return stypes.TxOutItem{
			Chain: common.SOLChain, VaultPubKey: "vaultA", InHash: common.TxID(hash), Height: h,
			ToAddress: "dest", Coins: common.Coins{common.NewCoin(common.SOLAsset, cosmos.NewUint(1))},
		}
	}
	bad, next := mk(100, "h100"), mk(101, "h101")
	cl := &SOLClient{bridge: &pendingBridge{items: []thorclient.PendingOutbound{pendingOf(bad, 100), pendingOf(next, 101)}}, logger: log.Logger}
	c.Assert(cl.durableSerializeAllows(next, 101), Equals, false) // waits behind @100
	cl.markDurableRejected(bad, 100)
	c.Assert(cl.durableSerializeAllows(next, 101), Equals, true) // @100 will not be signed here
}
