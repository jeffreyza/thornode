package thorchain

import (
	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type XMRMonovaultSuite struct{}

var _ = Suite(&XMRMonovaultSuite{})

func getRandomXMRFrostPubKey(c *C) common.PubKey {
	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	_, err = pubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	return pubKey
}

func (s *XMRMonovaultSuite) TestShouldMaintainXMRMonovaultSingleValidator(c *C) {
	c.Assert(shouldMaintainXMRMonovault(nil, 1), Equals, true)
}

func (s *XMRMonovaultSuite) TestHasDedicatedXMRMonovault(c *C) {
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)
	xmr := NewVaultV2(1, RetiringVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, common.EmptyPubKey)

	c.Assert(hasDedicatedXMRMonovault(nil), Equals, false)
	c.Assert(hasDedicatedXMRMonovault(Vaults{regular}), Equals, false)
	// any vault carrying the XMR chain counts, whatever its status
	c.Assert(hasDedicatedXMRMonovault(Vaults{regular, xmr}), Equals, true)

	c.Assert(shouldMaintainXMRMonovault(Vaults{regular}, 0), Equals, false)
	c.Assert(shouldMaintainXMRMonovault(Vaults{regular, xmr}, 0), Equals, true)
}

func (s *XMRMonovaultSuite) TestXMRVaultMembershipNeedsKeygen(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	needs, err := vm.xmrVaultMembershipNeedsKeygen(ctx, nil)
	c.Assert(err, IsNil)
	c.Assert(needs, Equals, false)

	active := NodeAccounts{GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive)}
	members := make([]string, 0, len(active))
	for _, na := range active {
		members = append(members, na.PubKeySet.Secp256k1.String())
	}

	// a regular asgard with the same membership does not satisfy the XMR family
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)
	regular.Membership = members
	c.Assert(k.SetVault(ctx, regular), IsNil)
	needs, err = vm.xmrVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needs, Equals, true)

	// an XMR vault for a different membership still needs a keygen
	stale := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, common.EmptyPubKey)
	stale.PubKeyFrost = getRandomXMRFrostPubKey(c)
	stale.Membership = members[:1]
	c.Assert(k.SetVault(ctx, stale), IsNil)
	needs, err = vm.xmrVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needs, Equals, true)

	// a retiring XMR vault with matching membership does not count
	retiring := NewVaultV2(1, RetiringVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, common.EmptyPubKey)
	retiring.PubKeyFrost = getRandomXMRFrostPubKey(c)
	retiring.Membership = members
	c.Assert(k.SetVault(ctx, retiring), IsNil)
	needs, err = vm.xmrVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needs, Equals, true)

	// an Init (pending) or Active XMR vault with matching membership does
	for _, status := range []VaultStatus{InitVault, ActiveVault} {
		current := NewVaultV2(1, status, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, common.EmptyPubKey)
		current.PubKeyFrost = getRandomXMRFrostPubKey(c)
		current.Membership = members
		c.Assert(k.SetVault(ctx, current), IsNil)
		needs, err = vm.xmrVaultMembershipNeedsKeygen(ctx, active)
		c.Assert(err, IsNil)
		c.Assert(needs, Equals, false, Commentf("status %s", status))
		c.Assert(k.DeleteVault(ctx, current.PubKey), IsNil)
	}
}

func (s *XMRMonovaultSuite) TestSelectActiveVaultForChainPrefersRegularTHORVaults(c *C) {
	ctx, k := setupKeeperForTest(c)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), nil)
	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, GetRandomPubKey())
	xmr.PubKeyFrost = getRandomXMRFrostPubKey(c)

	selected, err := selectActiveVaultForChain(ctx, k, Vaults{xmr, regular}, common.THORChain, 0)
	c.Assert(err, IsNil)
	c.Assert(selected.PubKey.Equals(regular.PubKey), Equals, true)
}

func (s *XMRMonovaultSuite) TestPreferredActiveVaultsForChainAcceptsXMRCapableVault(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)

	filtered := preferredActiveVaultsForChain(Vaults{hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].PubKey.Equals(hybrid.PubKey), Equals, true)
}

func (s *XMRMonovaultSuite) TestPreferredActiveVaultsForChainRejectsXMRVaultWithoutFrost(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)

	filtered := preferredActiveVaultsForChain(Vaults{regular, hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 0)
}

func (s *XMRMonovaultSuite) TestFilterActiveVaultsForOutboundChainRejectsXMRVaultWithoutFrost(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)

	filtered := filterActiveVaultsForOutboundChain(Vaults{regular, hybrid}, common.XMRChain)
	c.Assert(filtered, HasLen, 0)
}

func (s *XMRMonovaultSuite) TestFilterActiveVaultsForOutboundChainKeepsXMRCapableVaultForOtherChains(c *C) {
	hybrid := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.XMRChain, common.BTCChain}.Strings(), nil)
	hybrid.PubKeyFrost = getRandomXMRFrostPubKey(c)

	filtered := filterActiveVaultsForOutboundChain(Vaults{hybrid}, common.BTCChain)
	c.Assert(filtered, HasLen, 1)
	c.Assert(filtered[0].PubKey.Equals(hybrid.PubKey), Equals, true)
}
