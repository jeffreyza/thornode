package thorchain

import (
	"sort"
	"time"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type HandlerQuorumSecuritySuite struct{}

var _ = Suite(&HandlerQuorumSecuritySuite{})

// The expected identities are selected by the fixture, independently of the
// voter/handler. Four active nodes require three distinct attestations.
func (s *HandlerQuorumSecuritySuite) TestNetworkFeeAdversarialMatrix(c *C) {
	cases := []struct {
		name      string
		indices   []int
		mutate    bool
		want      []int
		consensus bool
	}{
		{"threshold minus one", []int{0, 1}, false, []int{0, 1}, false},
		{"threshold", []int{0, 1, 2}, false, []int{0, 1, 2}, true},
		{"threshold plus one", []int{0, 1, 2, 3}, false, []int{0, 1, 2, 3}, true},
		{"reordered", []int{2, 0, 1}, false, []int{0, 1, 2}, true},
		{"duplicate below threshold", []int{0, 1, 0, 1}, false, []int{0, 1}, false},
		{"departed signer", []int{0, 1, 4}, false, []int{0, 1}, false},
		{"changed signed rate", []int{0, 1, 2}, true, nil, false},
	}
	for _, tc := range cases {
		c.Logf("network fee: %s", tc.name)
		ctx, mgr := setupManagerForTest(c)
		keys := make([]secp256k1.PrivKey, 5)
		nodes := make(NodeAccounts, 5)
		for i := 0; i < 4; i++ {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		keys[4] = secp256k1.GenPrivKey() // valid signature, no active account
		fee := makeNetworkFee(common.ETHChain, 1024, 256, 100)
		baseline, err := mgr.Keeper().GetNetworkFee(ctx, fee.Chain)
		c.Assert(err, IsNil)
		atts := make([]*common.Attestation, 0, len(tc.indices))
		for _, i := range tc.indices {
			atts = append(atts, attestNetworkFee(c, keys[i], fee))
		}
		if tc.mutate {
			fee.TransactionRate++
		}
		msg := types.NewMsgNetworkFeeQuorum(&common.QuorumNetworkFee{NetworkFee: fee, Attestations: atts}, nodes[0].NodeAddress)
		_, err = NewNetworkFeeQuorumHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		voter, err := mgr.Keeper().GetObservedNetworkFeeVoter(ctx, fee.Height, fee.Chain, int64(fee.TransactionRate), int64(fee.TransactionSize))
		c.Assert(err, IsNil)
		var want []string
		for _, i := range tc.want {
			want = append(want, nodes[i].NodeAddress.String())
		}
		sort.Strings(want)
		got := append([]string(nil), voter.Signers...)
		sort.Strings(got)
		c.Assert(got, DeepEquals, want, Commentf("%s", tc.name))
		c.Assert(voter.BlockHeight > 0, Equals, tc.consensus, Commentf("%s", tc.name))
		saved, err := mgr.Keeper().GetNetworkFee(ctx, fee.Chain)
		c.Assert(err, IsNil)
		if tc.consensus {
			c.Assert(saved.TransactionFeeRate, Equals, fee.TransactionRate)
			c.Assert(saved.TransactionSize, Equals, fee.TransactionSize)
		} else {
			c.Assert(saved.TransactionFeeRate, Equals, baseline.TransactionFeeRate, Commentf("%s", tc.name))
			c.Assert(saved.TransactionSize, Equals, baseline.TransactionSize, Commentf("%s", tc.name))
		}
	}
}

// A valid active price reporter must not be able to lend its signature to a
// changed rate, timestamp, or required-symbol version. The handler silently
// discards bad feeds, so absence of a saved price is the security assertion;
// the unchanged row proves the fixture would otherwise be saved.
func (s *HandlerPriceFeedQuorumBatchSuite) TestSignedFeedTamperSecurity(c *C) {
	for _, change := range []struct {
		name   string
		mutate func(crypto.PrivKey, *common.QuorumPriceFeed)
		saved  bool
	}{
		{"unchanged", func(crypto.PrivKey, *common.QuorumPriceFeed) {}, true},
		{"rate", func(_ crypto.PrivKey, q *common.QuorumPriceFeed) { q.PriceFeed.Rates[0].Amount++ }, false},
		{"timestamp", func(_ crypto.PrivKey, q *common.QuorumPriceFeed) {
			q.PriceFeed.Time += int64(time.Minute / time.Millisecond)
		}, false},
		// Sign another version but present the required one: a changed version
		// alone is already rejected by the version check before verification.
		{"version", func(key crypto.PrivKey, q *common.QuorumPriceFeed) {
			signed := *q.PriceFeed
			signed.Version = []byte("revision")
			att, err := s.attestPriceFeed(key, &signed)
			c.Assert(err, IsNil)
			q.Attestations = []*common.Attestation{att}
		}, false},
	} {
		c.Logf("price feed: %s", change.name)
		ctx, mgr := setupManagerForTest(c)
		key, feed, node, err := s.setUp(map[string]float64{"BTC": 100000})
		c.Assert(err, IsNil)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
		change.mutate(key, feed)
		msg := types.NewMsgPriceFeedQuorumBatch([]*common.QuorumPriceFeed{feed}, node.NodeAddress)
		_, err = NewPriceFeedQuorumBatchHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil)
		iterator := mgr.Keeper().GetPriceIterator(ctx)
		c.Assert(iterator.Valid(), Equals, change.saved, Commentf("%s", change.name))
		iterator.Close()
	}
}

func (s *HandlerQuorumSecuritySuite) TestErrataAdversarialMatrix(c *C) {
	cases := []struct {
		name        string
		indices     []int
		changeChain bool
		want        []int
		consensus   bool
	}{
		{"threshold minus one", []int{0, 1}, false, []int{0, 1}, false},
		{"threshold", []int{0, 1, 2}, false, []int{0, 1, 2}, true},
		{"threshold plus one", []int{0, 1, 2, 3}, false, []int{0, 1, 2, 3}, true},
		{"reordered", []int{2, 0, 1}, false, []int{0, 1, 2}, true},
		{"duplicate below threshold", []int{0, 1, 0, 1}, false, []int{0, 1}, false},
		{"departed signer", []int{0, 1, 4}, false, []int{0, 1}, false},
		{"cross chain replay", []int{0, 1, 2}, true, nil, false},
	}
	for _, tc := range cases {
		c.Logf("errata: %s", tc.name)
		ctx, mgr := setupManagerForTest(c)
		keys := make([]secp256k1.PrivKey, 5)
		nodes := make(NodeAccounts, 5)
		for i := 0; i < 4; i++ {
			nodes[i], keys[i] = nodeWithPrivKey(c, mgr.Keeper(), ctx)
		}
		keys[4] = secp256k1.GenPrivKey()
		er := makeErrataTx(GetRandomTxHash(), common.ETHChain)
		mgr.Keeper().SetObservedTxInVoter(ctx, types.ObservedTxVoter{
			TxID: er.Id,
			Txs:  common.ObservedTxs{{Tx: common.Tx{ID: er.Id, Chain: er.Chain}}},
		})
		atts := make([]*common.Attestation, 0, len(tc.indices))
		for _, i := range tc.indices {
			atts = append(atts, attestErrataTx(c, keys[i], er))
		}
		if tc.changeChain {
			er.Chain = common.BTCChain
		}
		msg := types.NewMsgErrataTxQuorum(&common.QuorumErrataTx{ErrataTx: er, Attestations: atts}, nodes[0].NodeAddress)
		_, err := NewErrataTxQuorumHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		voter, err := mgr.Keeper().GetErrataTxVoter(ctx, er.Id, er.Chain)
		c.Assert(err, IsNil)
		var want []string
		for _, i := range tc.want {
			want = append(want, nodes[i].NodeAddress.String())
		}
		sort.Strings(want)
		got := append([]string(nil), voter.Signers...)
		sort.Strings(got)
		c.Assert(got, DeepEquals, want, Commentf("%s", tc.name))
		c.Assert(voter.BlockHeight > 0, Equals, tc.consensus, Commentf("%s", tc.name))
	}
}

// ValidateBasic only admits both directions for a vault self-send, so that is
// the tx a proposer can flip. Processed as an outbound, its swap memo makes it
// an unauthorised spend: the vault is debited and its members' bonds slashed
// (SlashVault needs a non-empty ETH pool to price the loss). The control row
// (attested as an outbound) proves that path is reachable; the flipped row
// reuses honest inbound attestations with Inbound=false. Bond loss is checked
// separately from slash points, since the non-attesting fourth node earns
// observation slash points whenever consensus is reached.
func (s *HandlerQuorumSecuritySuite) TestObservedTxDirectionFlip(c *C) {
	for _, tc := range []struct {
		name        string
		signInbound bool
		consensus   bool
	}{
		{"outbound attestations (control)", false, true},
		{"inbound attestations submitted as outbound", true, false},
	} {
		c.Logf("observed tx: %s", tc.name)
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		nodes := make(NodeAccounts, 4)
		keys := make([]secp256k1.PrivKey, 4)
		members := make(common.PubKeys, 4)
		for i := range nodes {
			nodes[i], keys[i] = nodeWithPrivKey(c, k, ctx)
			members[i] = nodes[i].PubKeySet.Secp256k1
		}
		vaultPubKey := GetRandomPubKey()
		vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{common.ETHChain.String()}, nil)
		vault.Membership = members.Strings()
		vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.BalanceRune = cosmos.NewUint(10000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, pool), IsNil)
		vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
		c.Assert(err, IsNil)
		obsTx := common.NewObservedTx(common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: vaultAddr,
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		}, 1024, vaultPubKey, 1024)
		atts := make([]*common.Attestation, 0, 3)
		for _, key := range keys[:3] {
			atts = append(atts, attestObservedTx(c, key, obsTx, tc.signInbound))
		}
		quoTx := &common.QuorumTx{ObsTx: obsTx, Inbound: false, Attestations: atts}
		_, err = NewObservedTxQuorumHandler(mgr).Run(ctx, types.NewMsgObservedTxQuorum(quoTx, nodes[0].NodeAddress))
		c.Assert(err, IsNil, Commentf("%s", tc.name))

		outVoter, err := k.GetObservedTxOutVoter(ctx, obsTx.Tx.ID)
		c.Assert(err, IsNil)
		var signers int
		for _, tx := range outVoter.Txs {
			signers += len(tx.Signers)
		}
		c.Assert(signers > 0, Equals, tc.consensus, Commentf("%s", tc.name))
		saved, err := k.GetVault(ctx, vaultPubKey)
		c.Assert(err, IsNil)
		c.Assert(saved.GetCoin(common.ETHAsset).Amount.Equal(vault.GetCoin(common.ETHAsset).Amount), Equals, !tc.consensus, Commentf("%s", tc.name))
		var pointed bool
		for _, na := range nodes {
			bonded, err := k.GetNodeAccount(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Assert(bonded.Bond.LT(na.Bond), Equals, tc.consensus, Commentf("%s: %s", tc.name, na.NodeAddress))
			pts, err := k.GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			pointed = pointed || pts > 0
		}
		c.Assert(pointed, Equals, tc.consensus, Commentf("%s", tc.name))
	}
}

// Solvency consensus is a minority (two of four active nodes). An insolvent
// report at consensus halts the chain, so a forged report must not.
func (s *HandlerQuorumSecuritySuite) TestSolvencyAdversarialMatrix(c *C) {
	cases := []struct {
		name      string
		indices   []int
		forge     bool
		want      []int
		consensus bool
	}{
		{"threshold minus one", []int{0}, false, []int{0}, false},
		{"threshold", []int{0, 1}, false, []int{0, 1}, true},
		{"threshold plus one", []int{0, 1, 2}, false, []int{0, 1, 2}, true},
		{"reordered", []int{1, 0}, false, []int{0, 1}, true},
		{"duplicate below threshold", []int{0, 0, 0}, false, []int{0}, false},
		{"departed signer", []int{0, 4}, false, []int{0}, false},
		{"solvent report presented as insolvent", []int{0, 1, 2}, true, nil, false},
	}
	for _, tc := range cases {
		c.Logf("solvency: %s", tc.name)
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(1024)
		k := mgr.Keeper()
		keys := make([]secp256k1.PrivKey, 5)
		nodes := make(NodeAccounts, 5)
		for i := 0; i < 4; i++ {
			nodes[i], keys[i] = nodeWithPrivKey(c, k, ctx)
		}
		keys[4] = secp256k1.GenPrivKey()
		vaultPubKey := GetRandomPubKey()
		vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{common.ETHChain.String()}, nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
		pool.BalanceRune = cosmos.NewUint(10000 * common.One)
		pool.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, pool), IsNil)
		k.SetMimir(ctx, "DollarsPerRune", 5_00000000)

		insolvent := common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)))
		signed := insolvent
		if tc.forge {
			signed = vault.Coins
		}
		sol := makeSolvency(c, common.ETHChain, vaultPubKey, signed, 1024)
		atts := make([]*common.Attestation, 0, len(tc.indices))
		for _, i := range tc.indices {
			atts = append(atts, attestSolvency(c, keys[i], sol))
		}
		if tc.forge {
			sol = makeSolvency(c, common.ETHChain, vaultPubKey, insolvent, 1024)
		}
		msg, err := types.NewMsgSolvencyQuorum(&common.QuorumSolvency{Solvency: sol, Attestations: atts}, nodes[0].NodeAddress)
		c.Assert(err, IsNil)
		_, err = NewSolvencyQuorumHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("%s", tc.name))

		voter, err := k.GetSolvencyVoter(ctx, sol.Id, sol.Chain)
		c.Assert(err, IsNil)
		var want []string
		for _, i := range tc.want {
			want = append(want, nodes[i].NodeAddress.String())
		}
		sort.Strings(want)
		got := append([]string(nil), voter.Signers...)
		sort.Strings(got)
		c.Assert(got, DeepEquals, want, Commentf("%s", tc.name))
		c.Assert(voter.ConsensusBlockHeight > 0, Equals, tc.consensus, Commentf("%s", tc.name))
		halt, err := k.GetMimir(ctx, "SolvencyHaltETHChain")
		c.Assert(err, IsNil)
		c.Assert(halt > 0, Equals, tc.consensus, Commentf("%s", tc.name))
	}
}
