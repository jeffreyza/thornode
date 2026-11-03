package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/blang/semver"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	"gopkg.in/check.v1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerObservedTxInSuite struct{}

// compromisedVaultNoOpSecurityMsgFromEvents returns the security-event message
// for a compromised-vault no-op so tests can assert the operator alert path.
func compromisedVaultNoOpSecurityMsgFromEvents(ctx cosmos.Context) string {
	for _, event := range ctx.EventManager().Events() {
		if event.Type != types.SecurityEventType {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key == "msg" && strings.HasPrefix(attr.Value, compromisedVaultNoOpSecurityMsg) {
				return attr.Value
			}
		}
	}
	return ""
}

type TestObservedTxInValidateKeeper struct {
	keeper.KVStoreDummy
	activeNodeAccount NodeAccount
	standbyAccount    NodeAccount
}

func (k *TestObservedTxInValidateKeeper) GetNodeAccount(_ cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if addr.Equals(k.standbyAccount.NodeAddress) {
		return k.standbyAccount, nil
	}
	if addr.Equals(k.activeNodeAccount.NodeAddress) {
		return k.activeNodeAccount, nil
	}
	return NodeAccount{}, errKaboom
}

func (k *TestObservedTxInValidateKeeper) SetNodeAccount(_ cosmos.Context, na NodeAccount) error {
	if na.NodeAddress.Equals(k.standbyAccount.NodeAddress) {
		k.standbyAccount = na
		return nil
	}
	return errKaboom
}

var _ = Suite(&HandlerObservedTxInSuite{})

// TestCommittedXMRErrataIsAppliedWhenInboundLaterReachesQuorum covers the
// ordering where errata consensus arrives while the original inbound has only
// partial observations. The later inbound quorum must become Reverted before
// any normal inbound accounting can execute.
func (s *HandlerObservedTxInSuite) TestCommittedXMRErrataIsAppliedWhenInboundLaterReachesQuorum(c *C) {
	w := getHandlerTestWrapper(c, 20, true, false)
	tx := NewObservedTx(GetRandomTx(), 10, GetRandomPubKey(), 10)
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)))

	// Partial inbound state exists when the errata reaches consensus, so the
	// errata handler cannot mark the selected transaction yet.
	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	w.keeper.SetObservedTxInVoter(w.ctx, voter)
	errataVoter := NewErrataTxVoter(tx.Tx.ID, common.XMRChain)
	err := processErrataTxAttestation(
		w.ctx,
		w.mgr,
		&errataVoter,
		w.activeNodeAccount.NodeAddress,
		NodeAccounts{w.activeNodeAccount},
		&common.ErrataTx{Id: tx.Tx.ID, Chain: common.XMRChain},
		true,
	)
	c.Assert(err, IsNil)
	c.Check(errataVoter.BlockHeight, Equals, w.ctx.BlockHeight())
	pending, err := w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Check(pending.Tx.IsEmpty(), Equals, true)
	c.Check(pending.Reverted, Equals, false)
	w.keeper.SetErrataTxVoter(w.ctx, errataVoter)

	// Model processTxInAttestation selecting and persisting the transaction at
	// the later quorum before the shared quorum handler runs.
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	err = handleObservedTxInQuorum(
		w.ctx,
		w.mgr,
		w.activeNodeAccount.NodeAddress,
		NodeAccounts{w.activeNodeAccount},
		nil,
		tx,
		voter,
		nil,
		true,
	)
	c.Assert(err, IsNil)

	stored, err := w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Check(stored.Reverted, Equals, true)
	c.Check(stored.UpdatedVault, Equals, false)
	c.Assert(stored.Actions, HasLen, 0)

	// Late/final observations are idempotent once the monotonic state is set.
	err = applyCommittedXMRErrataToInboundConsensus(w.ctx, w.keeper, &stored)
	c.Assert(err, IsNil)
	c.Check(stored.Reverted, Equals, true)
}

func (s *HandlerObservedTxInSuite) TestUncommittedOrForeignErrataDoesNotRevertXMRInbound(c *C) {
	for _, tc := range []struct {
		name   string
		chain  common.Chain
		height int64
	}{
		{name: "uncommitted", chain: common.XMRChain, height: 0},
		{name: "different chain", chain: common.BTCChain, height: 19},
	} {
		c.Log(tc.name)
		w := getHandlerTestWrapper(c, 20, true, false)
		tx := NewObservedTx(GetRandomTx(), 10, GetRandomPubKey(), 10)
		tx.Tx.Chain = common.XMRChain
		tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)))
		voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
		voter.Tx = tx
		voter.Height = w.ctx.BlockHeight()
		voter.FinalisedHeight = w.ctx.BlockHeight()
		w.keeper.SetObservedTxInVoter(w.ctx, voter)
		errataVoter := NewErrataTxVoter(tx.Tx.ID, tc.chain)
		errataVoter.BlockHeight = tc.height
		w.keeper.SetErrataTxVoter(w.ctx, errataVoter)

		err := applyCommittedXMRErrataToInboundConsensus(w.ctx, w.keeper, &voter)
		c.Assert(err, IsNil)
		c.Check(voter.Reverted, Equals, false)
	}
}

func (s *HandlerObservedTxInSuite) TestValidate(c *C) {
	var err error
	ctx, _ := setupKeeperForTest(c)
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	standbyAccount := GetRandomValidatorNode(NodeStandby)
	keeper := &TestObservedTxInValidateKeeper{
		activeNodeAccount: activeNodeAccount,
		standbyAccount:    standbyAccount,
	}

	handler := NewObservedTxInHandler(NewDummyMgrWithKeeper(keeper))

	// happy path
	pk := GetRandomPubKey()
	txs := ObservedTxs{NewObservedTx(GetRandomTx(), 12, pk, 12)}
	txs[0].Tx.ToAddress, err = pk.GetAddress(txs[0].Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	msg := NewMsgObservedTxIn(txs, activeNodeAccount.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// inactive node account
	msg = NewMsgObservedTxIn(txs, GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// invalid msg
	msg = &MsgObservedTxIn{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

type TestObservedTxInFailureKeeper struct {
	keeper.KVStoreDummy
	pool Pool
}

func (k *TestObservedTxInFailureKeeper) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	if k.pool.IsEmpty() {
		return NewPool(), nil
	}
	return k.pool, nil
}

func (k *TestObservedTxInFailureKeeper) GetVault(_ cosmos.Context, pubKey common.PubKey) (Vault, error) {
	return Vault{
		PubKey:      pubKey,
		PubKeyEddsa: pubKey,
	}, nil
}

func (s *HandlerObservedTxInSuite) TestFailure(c *C) {
	ctx, _ := setupKeeperForTest(c)
	// w := getHandlerTestWrapper(c, 1, true, false)

	keeper := &TestObservedTxInFailureKeeper{
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
	}
	mgr := NewDummyMgrWithKeeper(keeper)

	tx := NewObservedTx(GetRandomTx(), 12, GetRandomPubKey(), 12)
	err := refundTx(ctx, tx, mgr, CodeInvalidMemo, "Invalid memo", "")
	c.Assert(err, IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)
}

func (s *HandlerObservedTxInSuite) TestXMRRefundSkipsUnknownSenderWithoutExplicitRefundAddress(c *C) {
	ctx, _ := setupKeeperForTest(c)

	keeper := &TestObservedTxInFailureKeeper{
		pool: Pool{
			Asset:        common.XMRAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
	}
	mgr := NewDummyMgrWithKeeper(keeper)

	tx := NewObservedTx(GetRandomTx(), 12, GetRandomPubKey(), 12)
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(100)),
	}
	tx.Tx.Gas = common.Gas{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
	}
	tx.Tx.ToAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-vault")))
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-destination")))
	tx.Tx.Memo = fmt.Sprintf("=:%s:%s", common.XMRAsset.String(), destination.String())
	tx.Tx.FromAddress, _ = common.NewAddress(common.XMRUnknownSenderAddress(tx.Tx.ID.String(), tx.Tx.ToAddress.String()))

	err := refundTx(ctx, tx, mgr, CodeInvalidMemo, "Invalid memo", "")
	c.Assert(err, IsNil)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)

	tx.Tx.ID = GetRandomTxHash()
	refundAddress := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-address")))
	tx.Tx.Memo = fmt.Sprintf("=:%s:%s/%s", common.XMRAsset.String(), destination.String(), refundAddress.String())
	tx.Tx.FromAddress, _ = common.NewAddress(common.XMRUnknownSenderAddress(tx.Tx.ID.String(), tx.Tx.ToAddress.String()))

	err = refundTx(ctx, tx, mgr, CodeInvalidMemo, "Invalid memo", "")
	c.Assert(err, IsNil)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)
	c.Check(items[0].ToAddress.Equals(refundAddress), Equals, true)
}

// TestAddRefundAddressScopedToExplicitRefundChains ensures ADD memo asset
// addresses are refund destinations only on chains whose observations may lack
// a usable sender.
func (s *HandlerObservedTxInSuite) TestAddRefundAddressScopedToExplicitRefundChains(c *C) {
	ctx, _ := setupKeeperForTest(c)

	xmrKeeper := &TestObservedTxInFailureKeeper{
		pool: Pool{
			Asset:        common.XMRAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
	}
	xmrMgr := NewDummyMgrWithKeeper(xmrKeeper)
	xmrTx := NewObservedTx(GetRandomTx(), 12, GetRandomPubKey(), 12)
	xmrTx.Tx.Chain = common.XMRChain
	xmrTx.Tx.Coins = common.Coins{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(100)),
	}
	xmrTx.Tx.Gas = common.Gas{
		common.NewCoin(common.XMRAsset, cosmos.NewUint(1)),
	}
	xmrTx.Tx.ToAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-add-refund-vault")))
	xmrRefundAddress := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-add-refund-address")))
	xmrTx.Tx.Memo = fmt.Sprintf("ADD:%s:%s/%s", common.XMRAsset, GetRandomTHORAddress(), xmrRefundAddress)
	xmrTx.Tx.FromAddress, _ = common.NewAddress(common.XMRUnknownSenderAddress(xmrTx.Tx.ID.String(), xmrTx.Tx.ToAddress.String()))

	c.Assert(refundTx(ctx, xmrTx, xmrMgr, CodeInvalidMemo, "Invalid memo", ""), IsNil)
	xmrItems, err := xmrMgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(xmrItems, HasLen, 1)
	c.Check(xmrItems[0].ToAddress.Equals(xmrRefundAddress), Equals, true)

	if common.CurrentChainNetwork == common.MockNet {
		wrongNetworkAddress, addressErr := common.NewAddress(common.XMRChecksummedDummyAddress('C'))
		c.Assert(addressErr, IsNil)
		c.Assert(wrongNetworkAddress.IsChain(common.XMRChain), Equals, true)
		c.Assert(common.XMRChain.IsValidAddress(wrongNetworkAddress), Equals, false)

		xmrTx.Tx.ID = GetRandomTxHash()
		xmrTx.Tx.Memo = fmt.Sprintf("ADD:%s:%s/%s", common.XMRAsset, GetRandomTHORAddress(), wrongNetworkAddress)
		xmrTx.Tx.FromAddress, addressErr = common.NewAddress(common.XMRUnknownSenderAddress(xmrTx.Tx.ID.String(), xmrTx.Tx.ToAddress.String()))
		c.Assert(addressErr, IsNil)
		c.Assert(refundTx(ctx, xmrTx, xmrMgr, CodeInvalidMemo, "Invalid memo", ""), IsNil)
		xmrItems, err = xmrMgr.TxOutStore().GetOutboundItems(ctx)
		c.Assert(err, IsNil)
		c.Check(xmrItems, HasLen, 1)
	}

	btcKeeper := &TestObservedTxInFailureKeeper{
		pool: Pool{
			Asset:        common.BTCAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
	}
	btcMgr := NewDummyMgrWithKeeper(btcKeeper)
	btcTx := NewObservedTx(GetRandomTx(), 12, GetRandomPubKey(), 12)
	btcTx.Tx.Chain = common.BTCChain
	btcTx.Tx.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(100)),
	}
	btcTx.Tx.Gas = common.Gas{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1)),
	}
	btcTx.Tx.ToAddress = GetRandomBTCAddress()
	btcSender := GetRandomBTCAddress()
	btcMemoAddress := GetRandomBTCAddress()
	btcTx.Tx.FromAddress = btcSender
	btcTx.Tx.Memo = fmt.Sprintf("ADD:%s:%s/%s", common.BTCAsset, GetRandomTHORAddress(), btcMemoAddress)

	c.Assert(refundTx(ctx, btcTx, btcMgr, CodeInvalidMemo, "Invalid memo", ""), IsNil)
	btcItems, err := btcMgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(btcItems, HasLen, 1)
	c.Check(btcItems[0].ToAddress.Equals(btcSender), Equals, true)
	c.Check(btcItems[0].ToAddress.Equals(btcMemoAddress), Equals, false)
}

// TestInactiveVaultReferenceRefundUsesResolvedAddress ensures reference memo
// resolution runs before the inactive-vault refund path needs an XMR target
// without consuming a reference for a transaction that is only refunded.
func (s *HandlerObservedTxInSuite) TestInactiveVaultReferenceRefundUsesResolvedAddress(c *C) {
	w := getHandlerTestWrapper(c, 20, true, false)

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(200000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(300000 * common.One)
	pool.LPUnits = cosmos.NewUint(200000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 10000, 10000)), IsNil)
	w.keeper.SetMimir(w.ctx, constants.MemolessTxnTTL.String(), 100)
	w.keeper.SetMimir(w.ctx, constants.MemolessTxnMaxUse.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.EnableMemolessOutbound.String(), 1)

	sourceVault := NewVaultV2(1, InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	sourceVault.PubKeyFrost = GetRandomEd25519PubKey()
	sourceVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, sourceVault), IsNil)

	activeVault := NewVaultV2(2, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	activeVault.PubKeyFrost = GetRandomEd25519PubKey()
	activeVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, activeVault), IsNil)

	refundAddress := common.Address(common.XMRSeededDummyAddress('8', []byte("inactive-reference-refund")))
	registeredMemo := fmt.Sprintf("+:XMR.XMR:%s/%s", GetRandomTHORAddress(), refundAddress)
	w.keeper.SetReferenceMemo(w.ctx, NewReferenceMemo(common.XMRAsset, registeredMemo, "40005", 10))

	tx := NewObservedTx(GetRandomTx(), 15, sourceVault.PubKey, 15)
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(20000*common.One)))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	tx.Tx.Memo = "r:40005"
	var err error
	tx.Tx.ToAddress, err = sourceVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	tx.Tx.FromAddress, err = common.NewAddress(common.XMRUnknownSenderAddress(tx.Tx.ID.String(), tx.Tx.ToAddress.String()))
	c.Assert(err, IsNil)

	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	voter.UnfinalizedHeight = 15

	err = handleObservedTxInQuorum(
		w.ctx,
		w.mgr,
		w.activeNodeAccount.NodeAddress,
		NodeAccounts{w.activeNodeAccount},
		nil,
		tx,
		voter,
		nil,
		true,
	)
	c.Assert(err, IsNil)

	voter, err = w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Check(voter.Tx.Tx.Memo, Equals, registeredMemo)
	c.Assert(voter.Actions, HasLen, 1)
	c.Check(voter.Actions[0].ToAddress.Equals(refundAddress), Equals, true)
	c.Check(voter.Actions[0].Coin.Asset.Equals(common.XMRAsset), Equals, true)
	c.Check(voter.Actions[0].GetMemo(), Equals, NewRefundMemo(tx.Tx.ID).String())
	storedReference, err := w.keeper.GetReferenceMemo(w.ctx, common.XMRAsset, "40005")
	c.Assert(err, IsNil)
	c.Check(storedReference.GetUsageCount(), Equals, int64(0))

	blockOut, err := w.keeper.GetTxOut(w.ctx, voter.OutboundHeight)
	c.Assert(err, IsNil)
	foundRefund := false
	for _, item := range blockOut.TxArray {
		if item.InHash.Equals(tx.Tx.ID) {
			foundRefund = true
			c.Check(item.ToAddress.Equals(refundAddress), Equals, true)
			break
		}
	}
	c.Check(foundRefund, Equals, true)
}

// TestHaltedReferenceRefundUsesResolvedAddress ensures HaltMemoless rejects an
// otherwise-valid reference without discarding the explicit address needed to
// refund an in-flight XMR deposit or consuming the reference.
func (s *HandlerObservedTxInSuite) TestHaltedReferenceRefundUsesResolvedAddress(c *C) {
	w := getHandlerTestWrapper(c, 20, true, false)

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(200000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(300000 * common.One)
	pool.LPUnits = cosmos.NewUint(200000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 10000, 10000)), IsNil)
	w.keeper.SetMimir(w.ctx, constants.MemolessTxnTTL.String(), 100)
	w.keeper.SetMimir(w.ctx, constants.MemolessTxnMaxUse.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.HaltMemoless.String(), 1)
	w.keeper.SetMimir(w.ctx, constants.EnableMemolessOutbound.String(), 1)

	sourceVault := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	sourceVault.PubKeyFrost = GetRandomEd25519PubKey()
	sourceVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, sourceVault), IsNil)

	refundAddress := common.Address(common.XMRSeededDummyAddress('8', []byte("halted-reference-refund")))
	registeredMemo := fmt.Sprintf("=:BTC.BTC:%s/%s", GetRandomBTCAddress(), refundAddress)
	w.keeper.SetReferenceMemo(w.ctx, NewReferenceMemo(common.XMRAsset, registeredMemo, "40007", 10))

	tx := NewObservedTx(GetRandomTx(), 15, sourceVault.PubKey, 15)
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(20000*common.One)))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	tx.Tx.Memo = "r:40007"
	var err error
	tx.Tx.ToAddress, err = sourceVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	tx.Tx.FromAddress, err = common.NewAddress(common.XMRUnknownSenderAddress(tx.Tx.ID.String(), tx.Tx.ToAddress.String()))
	c.Assert(err, IsNil)

	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	voter.UnfinalizedHeight = 15

	err = handleObservedTxInQuorum(
		w.ctx,
		w.mgr,
		w.activeNodeAccount.NodeAddress,
		NodeAccounts{w.activeNodeAccount},
		nil,
		tx,
		voter,
		nil,
		true,
	)
	c.Assert(err, IsNil)

	voter, err = w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Check(voter.Tx.Tx.Memo, Equals, "")
	c.Assert(voter.Actions, HasLen, 1)
	c.Check(voter.Actions[0].ToAddress.Equals(refundAddress), Equals, true)
	c.Check(voter.Actions[0].Coin.Asset.Equals(common.XMRAsset), Equals, true)
	c.Check(voter.Actions[0].GetMemo(), Equals, NewRefundMemo(tx.Tx.ID).String())
	c.Check(w.keeper.HasSwapQueueItem(w.ctx, tx.Tx.ID, 0), Equals, false)

	storedReference, err := w.keeper.GetReferenceMemo(w.ctx, common.XMRAsset, "40007")
	c.Assert(err, IsNil)
	c.Check(storedReference.GetUsageCount(), Equals, int64(0))
}

// TestInactiveVaultReferenceSwapRefundUsesExplicitAddress verifies that a
// resolved cross-chain swap memo redirects an inbound-chain refund to its
// explicit refund address instead of the observable sender address.
func (s *HandlerObservedTxInSuite) TestInactiveVaultReferenceSwapRefundUsesExplicitAddress(c *C) {
	w := getHandlerTestWrapper(c, 20, true, false)

	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(200000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(300000 * common.One)
	pool.LPUnits = cosmos.NewUint(200000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.BTCChain, NewNetworkFee(common.BTCChain, 10000, 10000)), IsNil)
	w.keeper.SetMimir(w.ctx, constants.MemolessTxnTTL.String(), 100)
	w.keeper.SetMimir(w.ctx, constants.MemolessTxnMaxUse.String(), 1)

	sourceVault := NewVault(1, InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	sourceVault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, sourceVault), IsNil)

	activeVault := NewVault(2, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	activeVault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, activeVault), IsNil)

	senderAddress := GetRandomBTCAddress()
	refundAddress := GetRandomBTCAddress()
	destinationAddress := GetRandomETHAddress()
	c.Assert(refundAddress.Equals(senderAddress), Equals, false)
	registeredMemo := fmt.Sprintf("=:ETH.ETH:%s/%s", destinationAddress, refundAddress)
	w.keeper.SetReferenceMemo(w.ctx, NewReferenceMemo(common.BTCAsset, registeredMemo, "40006", 10))

	tx := NewObservedTx(GetRandomTx(), 15, sourceVault.PubKey, 15)
	tx.Tx.Chain = common.BTCChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(20000*common.One)))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}
	tx.Tx.Memo = "r:40006"
	var err error
	tx.Tx.ToAddress, err = sourceVault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	tx.Tx.FromAddress = senderAddress

	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	voter.UnfinalizedHeight = 15

	err = handleObservedTxInQuorum(
		w.ctx,
		w.mgr,
		w.activeNodeAccount.NodeAddress,
		NodeAccounts{w.activeNodeAccount},
		nil,
		tx,
		voter,
		nil,
		true,
	)
	c.Assert(err, IsNil)

	voter, err = w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Check(voter.Tx.Tx.Memo, Equals, registeredMemo)
	c.Assert(voter.Actions, HasLen, 1)
	c.Check(voter.Actions[0].ToAddress.Equals(refundAddress), Equals, true)
	c.Check(voter.Actions[0].ToAddress.Equals(senderAddress), Equals, false)
	c.Check(voter.Actions[0].Coin.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(voter.Actions[0].GetMemo(), Equals, NewRefundMemo(tx.Tx.ID).String())

	storedReference, err := w.keeper.GetReferenceMemo(w.ctx, common.BTCAsset, "40006")
	c.Assert(err, IsNil)
	c.Check(storedReference.GetUsageCount(), Equals, int64(0))
}

func (s *HandlerObservedTxInSuite) TestRefundDoesNotDuplicatePendingOutbound(c *C) {
	w := getHandlerTestWrapper(c, 12345, true, false)

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(200000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(300000 * common.One)
	pool.LPUnits = cosmos.NewUint(200000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 10000, 10000)), IsNil)
	w.keeper.SetMimir(w.ctx, constants.EnableMemolessOutbound.String(), 1)

	sourceVault := NewVaultV2(1, InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	sourceVault.PubKeyFrost = GetRandomEd25519PubKey()
	sourceVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, sourceVault), IsNil)

	activeVault := NewVaultV2(2, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	activeVault.PubKeyFrost = GetRandomEd25519PubKey()
	activeVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, activeVault), IsNil)

	tx := NewObservedTx(GetRandomTx(), 3673000, sourceVault.PubKey, w.ctx.BlockHeight())
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(20000*common.One)))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	var err error
	tx.Tx.ToAddress, err = sourceVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	refundAddr := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-sender")))
	tx.Tx.FromAddress, err = common.NewAddress(refundAddr.String())
	c.Assert(err, IsNil)
	tx.Tx.Memo = fmt.Sprintf("ADD:%s:%s", common.XMRAsset.String(), GetRandomTHORAddress().String())

	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	c.Assert(refundTx(w.ctx, tx, w.mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""), IsNil)
	c.Assert(refundTx(w.ctx, tx, w.mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""), IsNil)

	voter, err = w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 1)
	c.Check(voter.Actions[0].ToAddress.Equals(refundAddr), Equals, true)
	c.Check(voter.Actions[0].GetMemo(), Equals, NewRefundMemo(tx.Tx.ID).String())
	c.Check(voter.Actions[0].Memo, Equals, "")
	c.Check(voter.Actions[0].OriginalMemo, Equals, NewRefundMemo(tx.Tx.ID).String())
	c.Check(voter.Actions[0].VaultPubKey.Equals(sourceVault.PubKey), Equals, true)
	blockOut, err := w.keeper.GetTxOut(w.ctx, voter.OutboundHeight)
	c.Assert(err, IsNil)
	c.Assert(blockOut.TxArray, HasLen, 1)
	c.Check(blockOut.TxArray[0].InHash.Equals(tx.Tx.ID), Equals, true)
	c.Check(blockOut.TxArray[0].Coin.Asset.Equals(common.XMRAsset), Equals, true)
}

func (s *HandlerObservedTxInSuite) TestRefundDoesNotDuplicatePendingOutboundWithMemoPassthrough(c *C) {
	w := getHandlerTestWrapper(c, 12345, true, false)

	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(200000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(300000 * common.One)
	pool.LPUnits = cosmos.NewUint(200000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)
	c.Assert(w.keeper.SaveNetworkFee(w.ctx, common.XMRChain, NewNetworkFee(common.XMRChain, 10000, 10000)), IsNil)
	w.keeper.SetMimir(w.ctx, constants.EnableMemolessOutbound.String(), 1)

	sourceVault := NewVaultV2(1, InactiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	sourceVault.PubKeyFrost = GetRandomEd25519PubKey()
	sourceVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, sourceVault), IsNil)

	activeVault := NewVaultV2(2, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil, GetRandomEd25519PubKey())
	activeVault.PubKeyFrost = GetRandomEd25519PubKey()
	activeVault.AddFunds(common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50000*common.One))))
	c.Assert(w.keeper.SetVault(w.ctx, activeVault), IsNil)

	tx := NewObservedTx(GetRandomTx(), 3673000, sourceVault.PubKey, w.ctx.BlockHeight())
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(20000*common.One)))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	var err error
	tx.Tx.ToAddress, err = sourceVault.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	refundAddr := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-sender")))
	tx.Tx.FromAddress, err = common.NewAddress(refundAddr.String())
	c.Assert(err, IsNil)
	tx.Tx.Memo = fmt.Sprintf("ADD:%s:%s|memo-passthrough", common.XMRAsset.String(), GetRandomTHORAddress().String())

	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	c.Assert(refundTx(w.ctx, tx, w.mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""), IsNil)
	c.Assert(refundTx(w.ctx, tx, w.mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""), IsNil)

	expectedMemo := NewRefundMemo(tx.Tx.ID).String() + "|memo-passthrough"
	voter, err = w.keeper.GetObservedTxInVoter(w.ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Actions, HasLen, 1)
	c.Check(voter.Actions[0].ToAddress.Equals(refundAddr), Equals, true)
	c.Check(voter.Actions[0].GetMemo(), Equals, expectedMemo)
	c.Check(voter.Actions[0].Memo, Equals, expectedMemo)
	c.Check(voter.Actions[0].OriginalMemo, Equals, expectedMemo)
	c.Check(voter.Actions[0].VaultPubKey.Equals(sourceVault.PubKey), Equals, true)
	blockOut, err := w.keeper.GetTxOut(w.ctx, voter.OutboundHeight)
	c.Assert(err, IsNil)
	c.Assert(blockOut.TxArray, HasLen, 1)
	c.Check(blockOut.TxArray[0].InHash.Equals(tx.Tx.ID), Equals, true)
	c.Check(blockOut.TxArray[0].Coin.Asset.Equals(common.XMRAsset), Equals, true)
}

func (s *HandlerObservedTxInSuite) TestRefundCleanupDoesNotDuplicatePoolAccounting(c *C) {
	w := getHandlerTestWrapper(c, 12345, true, false)

	refundAmount := cosmos.NewUint(20000 * common.One)
	pool := NewPool()
	pool.Asset = common.XMRAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(200000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(300000 * common.One)
	pool.LPUnits = cosmos.NewUint(200000 * common.One)
	c.Assert(w.keeper.SetPool(w.ctx, pool), IsNil)

	tx := NewObservedTx(GetRandomTx(), 3673000, common.EmptyPubKey, w.ctx.BlockHeight())
	tx.Tx.Chain = common.XMRChain
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, refundAmount))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	tx.Tx.ToAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-vault")))
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-refund-destination")))
	tx.Tx.Memo = fmt.Sprintf("=:%s:%s", common.XMRAsset.String(), destination.String())
	var err error
	tx.Tx.FromAddress, err = common.NewAddress(common.XMRUnknownSenderAddress(tx.Tx.ID.String(), tx.Tx.ToAddress.String()))
	c.Assert(err, IsNil)

	voter := NewObservedTxVoter(tx.Tx.ID, ObservedTxs{tx})
	voter.Tx = tx
	voter.Height = w.ctx.BlockHeight()
	voter.FinalisedHeight = w.ctx.BlockHeight()
	w.keeper.SetObservedTxInVoter(w.ctx, voter)

	c.Assert(refundTx(w.ctx, tx, w.mgr, CodeInvalidMemo, "Invalid memo", ""), IsNil)
	c.Assert(refundTx(w.ctx, tx, w.mgr, CodeInvalidMemo, "Invalid memo", ""), IsNil)

	pool, err = w.keeper.GetPool(w.ctx, common.XMRAsset)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(300000*common.One).Add(refundAmount)), Equals, true)
	items, err := w.mgr.TxOutStore().GetOutboundItems(w.ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)
}

type TestObservedTxInHandleKeeper struct {
	keeper.KVStoreDummy
	nas                  NodeAccounts
	voter                ObservedTxVoter
	vaultExists          bool
	height               int64
	msg                  MsgSwap
	pool                 Pool
	observing            []cosmos.AccAddress
	vault                Vault
	txOut                *TxOut
	setLastObserveHeight bool
	referenceMemos       map[string]ReferenceMemo // key: asset:reference
}

func (k *TestObservedTxInHandleKeeper) SetSwapQueueItem(_ cosmos.Context, msg MsgSwap, _ int) error {
	k.msg = msg
	return nil
}

func (k *TestObservedTxInHandleKeeper) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return k.nas, nil
}

func (k *TestObservedTxInHandleKeeper) GetErrataTxVoter(_ cosmos.Context, txID common.TxID, chain common.Chain) (ErrataTxVoter, error) {
	return NewErrataTxVoter(txID, chain), nil
}

func (k *TestObservedTxInHandleKeeper) GetObservedTxInVoter(_ cosmos.Context, _ common.TxID) (ObservedTxVoter, error) {
	return k.voter, nil
}

func (k *TestObservedTxInHandleKeeper) SetObservedTxInVoter(_ cosmos.Context, voter ObservedTxVoter) {
	k.voter = voter
}

func (k *TestObservedTxInHandleKeeper) VaultExists(_ cosmos.Context, _ common.PubKey) bool {
	return k.vaultExists
}

func (k *TestObservedTxInHandleKeeper) SetLastChainHeight(_ cosmos.Context, _ common.Chain, height int64) error {
	k.height = height
	return nil
}

func (k *TestObservedTxInHandleKeeper) GetPool(_ cosmos.Context, _ common.Asset) (Pool, error) {
	if k.pool.IsEmpty() {
		return NewPool(), nil
	}
	return k.pool, nil
}

func (k *TestObservedTxInHandleKeeper) AddObservingAddresses(_ cosmos.Context, addrs []cosmos.AccAddress) error {
	k.observing = addrs
	return nil
}

func (k *TestObservedTxInHandleKeeper) GetVault(_ cosmos.Context, key common.PubKey) (Vault, error) {
	if k.vault.PubKey.Equals(key) {
		return k.vault, nil
	}
	return GetRandomVault(), errKaboom
}

func (k *TestObservedTxInHandleKeeper) GetAsgardVaults(_ cosmos.Context) (Vaults, error) {
	return Vaults{k.vault}, nil
}

func (k *TestObservedTxInHandleKeeper) SetVault(_ cosmos.Context, vault Vault) error {
	if k.vault.PubKey.Equals(vault.PubKey) {
		k.vault = vault
		return nil
	}
	return errKaboom
}

func (k *TestObservedTxInHandleKeeper) GetLowestActiveVersion(_ cosmos.Context) semver.Version {
	return GetCurrentVersion()
}

func (k *TestObservedTxInHandleKeeper) IsActiveObserver(_ cosmos.Context, addr cosmos.AccAddress) bool {
	return addr.Equals(k.nas[0].NodeAddress)
}

func (k *TestObservedTxInHandleKeeper) GetTxOut(ctx cosmos.Context, blockHeight int64) (*TxOut, error) {
	if k.txOut != nil && k.txOut.Height == blockHeight {
		return k.txOut, nil
	}
	return nil, errKaboom
}

func (k *TestObservedTxInHandleKeeper) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	if k.txOut.Height == blockOut.Height {
		k.txOut = blockOut
		return nil
	}
	return errKaboom
}

func (k *TestObservedTxInHandleKeeper) SetLastObserveHeight(ctx cosmos.Context, chain common.Chain, address cosmos.AccAddress, height int64) error {
	k.setLastObserveHeight = true
	return nil
}

func (k *TestObservedTxInHandleKeeper) ReferenceMemoExists(_ cosmos.Context, asset common.Asset, reference string) bool {
	if k.referenceMemos == nil {
		return false
	}
	key := fmt.Sprintf("%s:%s", asset.String(), reference)
	_, exists := k.referenceMemos[key]
	return exists
}

func (k *TestObservedTxInHandleKeeper) GetReferenceMemo(_ cosmos.Context, asset common.Asset, reference string) (ReferenceMemo, error) {
	if k.referenceMemos == nil {
		return ReferenceMemo{}, fmt.Errorf("reference memo not found")
	}
	key := fmt.Sprintf("%s:%s", asset.String(), reference)
	if memo, exists := k.referenceMemos[key]; exists {
		return memo, nil
	}
	return ReferenceMemo{}, fmt.Errorf("reference memo not found")
}

func (k *TestObservedTxInHandleKeeper) SetReferenceMemo(_ cosmos.Context, memo ReferenceMemo) {
	if k.referenceMemos == nil {
		k.referenceMemos = make(map[string]ReferenceMemo)
	}
	key := fmt.Sprintf("%s:%s", memo.Asset.String(), memo.Reference)
	k.referenceMemos[key] = memo
}

func (k *TestObservedTxInHandleKeeper) GetMimir(_ cosmos.Context, key string) (int64, error) {
	// For tests, memoless transactions are not halted
	if key == "HaltMemoless" {
		return 0, nil
	}
	if strings.HasPrefix(key, compromisedVaultMimirKeyPrefix) {
		return -1, nil
	}
	return 0, fmt.Errorf("mimir not found")
}

func (k *TestObservedTxInHandleKeeper) GetConfigInt64(_ cosmos.Context, key constants.ConstantName) int64 {
	// Set reasonable defaults for test
	switch key {
	case constants.MemolessTxnTTL:
		return 100 // blocks
	case constants.MemolessTxnMaxUse:
		return 10 // max usage
	case constants.MemolessTxnRefCount:
		return 99999 // memoless txn reference id counts
	default:
		return 0
	}
}

func (s *HandlerObservedTxInSuite) TestHandle(c *C) {
	s.testHandleWithVersion(c)
	s.testHandleWithConfirmation(c)
}

func (s *HandlerObservedTxInSuite) testHandleWithConfirmation(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	tx := GetRandomTx()
	tx.Memo = "SWAP:BTC.BTC:" + GetRandomBTCAddress().String()
	obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 15)
	txs := ObservedTxs{obTx}
	pk := GetRandomPubKey()
	txs[0].Tx.ToAddress, err = pk.GetAddress(txs[0].Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey

	keeper := &TestObservedTxInHandleKeeper{
		nas: NodeAccounts{
			GetRandomValidatorNode(NodeActive),
			GetRandomValidatorNode(NodeActive),
			GetRandomValidatorNode(NodeActive),
			GetRandomValidatorNode(NodeActive),
		},
		vault: vault,
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
		vaultExists: true,
	}
	mgr.K = keeper
	handler := NewObservedTxInHandler(mgr)

	// first not confirmed message
	msg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	voter, err := keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 1)
	// tx has not reach consensus yet, thus fund should not be credit to vault
	c.Assert(keeper.vault.HasFunds(), Equals, false)
	c.Assert(voter.UpdatedVault, Equals, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(voter.Height, Equals, int64(0))
	mgr.ObMgr().EndBlock(ctx, keeper)

	// second not confirmed message
	msg1 := NewMsgObservedTxIn(txs, keeper.nas[1].NodeAddress)
	_, err = handler.handle(ctx, *msg1)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.UpdatedVault, Equals, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(voter.Height, Equals, int64(0))
	c.Assert(keeper.vault.HasFunds(), Equals, false)

	// third not confirmed message
	msg2 := NewMsgObservedTxIn(txs, keeper.nas[2].NodeAddress)
	_, err = handler.handle(ctx, *msg2)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.UpdatedVault, Equals, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Check(keeper.height, Equals, int64(12))
	ethCoin := keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.ZeroUint()), Equals, true)
	// make sure the logic has not been processed , as tx has not been finalised , still waiting for confirmation
	c.Check(keeper.msg.Tx.ID.Equals(tx.ID), Equals, false)

	// fourth not confirmed message
	msg3 := NewMsgObservedTxIn(txs, keeper.nas[3].NodeAddress)
	_, err = handler.handle(ctx, *msg3)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.UpdatedVault, Equals, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Check(keeper.height, Equals, int64(12))
	ethCoin = keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(keeper.msg.Tx.ID.Equals(tx.ID), Equals, false)

	//  first finalised message
	txs[0].BlockHeight = 15
	fMsg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *fMsg)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.UpdatedVault, Equals, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(voter.Height, Equals, int64(18))
	ethCoin = keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(keeper.msg.Tx.ID.Equals(tx.ID), Equals, false)

	// second finalised message
	fMsg1 := NewMsgObservedTxIn(txs, keeper.nas[1].NodeAddress)
	_, err = handler.handle(ctx, *fMsg1)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.UpdatedVault, Equals, false)
	c.Assert(voter.FinalisedHeight, Equals, int64(0))
	c.Assert(voter.Height, Equals, int64(18))
	ethCoin = keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.ZeroUint()), Equals, true)
	c.Check(keeper.msg.Tx.ID.Equals(tx.ID), Equals, false)

	// third finalised message
	fMsg2 := NewMsgObservedTxIn(txs, keeper.nas[2].NodeAddress)
	_, err = handler.handle(ctx, *fMsg2)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.UpdatedVault, Equals, true)
	c.Assert(voter.FinalisedHeight, Equals, int64(18))
	c.Assert(voter.Height, Equals, int64(18))
	// make sure fund has been credit to vault correctly
	ethCoin = keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.OneUint()), Equals, true)
	c.Check(keeper.msg.Tx.ID.String(), Equals, tx.ID.String())

	// third finalised message
	fMsg3 := NewMsgObservedTxIn(txs, keeper.nas[3].NodeAddress)
	_, err = handler.handle(ctx, *fMsg3)
	c.Assert(err, IsNil)
	voter, err = keeper.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.UpdatedVault, Equals, true)
	c.Assert(voter.FinalisedHeight, Equals, int64(18))
	c.Assert(voter.Height, Equals, int64(18))
	// make sure fund has not been doubled
	ethCoin = keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.OneUint()), Equals, true)
	c.Check(keeper.msg.Tx.ID.String(), Equals, tx.ID.String())
}

func (s *HandlerObservedTxInSuite) testHandleWithVersion(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)

	tx := GetRandomTx()
	tx.Memo = "SWAP:BTC.BTC:" + GetRandomBTCAddress().String()
	obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 12)
	txs := ObservedTxs{obTx}
	pk := GetRandomPubKey()
	txs[0].Tx.ToAddress, err = pk.GetAddress(txs[0].Tx.Coins[0].Asset.Chain)

	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey

	keeper := &TestObservedTxInHandleKeeper{
		nas:   NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter: NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		vault: vault,
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
		vaultExists: true,
	}
	mgr.K = keeper
	handler := NewObservedTxInHandler(mgr)

	c.Assert(err, IsNil)
	msg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	mgr.ObMgr().EndBlock(ctx, keeper)
	c.Check(keeper.msg.Tx.ID.String(), Equals, tx.ID.String())
	c.Check(keeper.observing, HasLen, 1)
	c.Check(keeper.height, Equals, int64(12))
	ethCoin := keeper.vault.Coins.GetCoin(common.ETHAsset)
	c.Assert(ethCoin.Amount.Equal(cosmos.OneUint()), Equals, true)
}

// TestCompromisedVaultInboundIsNoOp verifies the legacy MsgObservedTxIn path
// still records quorum, but does not credit the vault, queue swaps, or schedule
// refunds/outbounds for a vault flagged by CompromisedVault-<pubkey>.
func (s *HandlerObservedTxInSuite) TestCompromisedVaultInboundIsNoOp(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(18)
	k := mgr.Keeper()

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	vaultPubKey := GetRandomPubKey()
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault), IsNil)
	// The flag is per vault pubkey, matching the existing keysign and inbound
	// address filtering semantics.
	k.SetMimir(ctx, compromisedVaultMimirKeyPrefix+vaultPubKey.String(), 1)

	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		vaultAddr,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(37500))},
		"swap:BTC.BTC:"+GetRandomBTCAddress().String(),
	)
	obsTx := NewObservedTx(tx, 12, vaultPubKey, 12)
	msg := NewMsgObservedTxIn(ObservedTxs{obsTx}, na.NodeAddress)

	handler := NewObservedTxInHandler(mgr)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	updatedVault, err := k.GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.IsZero(), Equals, true)
	c.Assert(updatedVault.InboundTxCount, Equals, int64(0))

	voter, err := k.GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)
	// Finalised observations are closed out even though no tx handler is run.
	c.Assert(voter.FinalisedHeight, Equals, ctx.BlockHeight())
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	c.Assert(voter.UpdatedVault, Equals, false)
	// Operators need an explicit security signal that the tx was skipped
	// because the destination vault has been flagged compromised.
	securityMsg := compromisedVaultNoOpSecurityMsgFromEvents(ctx)
	c.Assert(securityMsg, Not(Equals), "")
	c.Assert(strings.Contains(securityMsg, vaultPubKey.String()), Equals, true)

	queue := newSwapQueue(k)
	swaps, err := queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Assert(swaps, HasLen, 0)

	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
}

// Test migrate memo
func (s *HandlerObservedTxInSuite) TestMigrateMemo(c *C) {
	var err error
	ctx, _ := setupKeeperForTest(c)

	vault := GetRandomVault()
	addr, err := vault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	newVault := GetRandomVault()
	txout := NewTxOut(12)
	newVaultAddr, err := newVault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	txout.TxArray = append(txout.TxArray, TxOutItem{
		Chain:       common.ETHChain,
		InHash:      common.BlankTxID,
		ToAddress:   newVaultAddr,
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.ETHAsset, cosmos.NewUint(1024)),
		Memo:        NewMigrateMemo(1).String(),
	})
	tx := NewObservedTx(common.Tx{
		ID:    GetRandomTxHash(),
		Chain: common.ETHChain,
		Coins: common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(1024)),
		},
		Memo:        NewMigrateMemo(12).String(),
		FromAddress: addr,
		ToAddress:   newVaultAddr,
		Gas: common.Gas{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
		},
	}, 13, vault.PubKey, 13)

	txs := ObservedTxs{tx}
	keeper := &TestObservedTxInHandleKeeper{
		nas:   NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter: NewObservedTxVoter(tx.Tx.ID, make(ObservedTxs, 0)),
		vault: vault,
		pool: Pool{
			Asset:        common.ETHAsset,
			BalanceRune:  cosmos.NewUint(200),
			BalanceAsset: cosmos.NewUint(300),
		},
		vaultExists: true,
		txOut:       txout,
	}

	handler := NewObservedTxInHandler(NewDummyMgrWithKeeper(keeper))

	c.Assert(err, IsNil)
	msg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
}

type xmrMigrateInboundTestCase struct {
	unknownSource   bool
	scheduledAmount cosmos.Uint
	maxGas          cosmos.Uint
	receivedAmount  cosmos.Uint
	observedGas     cosmos.Uint
}

func defaultXMRMigrateInboundTestCase() xmrMigrateInboundTestCase {
	return xmrMigrateInboundTestCase{
		scheduledAmount: cosmos.NewUint(1024),
		maxGas:          cosmos.NewUint(100),
		receivedAmount:  cosmos.NewUint(1024),
		observedGas:     cosmos.NewUint(1),
	}
}

func (s *HandlerObservedTxInSuite) TestXMRMigrateInboundRequiresScheduledSource(c *C) {
	unknownCase := defaultXMRMigrateInboundTestCase()
	unknownCase.unknownSource = true
	unknownVault, unknownVoter := s.runXMRMigrateInbound(c, unknownCase)
	c.Check(unknownVault.Coins.GetCoin(common.XMRAsset).IsEmpty(), Equals, true)
	c.Check(unknownVault.InboundTxCount, Equals, int64(0))
	c.Check(unknownVoter.UpdatedVault, Equals, false)

	validCase := defaultXMRMigrateInboundTestCase()
	validVault, validVoter := s.runXMRMigrateInbound(c, validCase)
	c.Check(validVault.Coins.GetCoin(common.XMRAsset).Amount.Equal(cosmos.NewUint(1024)), Equals, true)
	c.Check(validVault.InboundTxCount, Equals, int64(1))
	c.Check(validVoter.UpdatedVault, Equals, true)

	leftoverMaxGasCase := xmrMigrateInboundTestCase{
		scheduledAmount: cosmos.NewUint(12076340400),
		maxGas:          cosmos.NewUint(1800000),
		receivedAmount:  cosmos.NewUint(12077865900),
		observedGas:     cosmos.NewUint(1200000),
	}
	leftoverMaxGasVault, leftoverMaxGasVoter := s.runXMRMigrateInbound(c, leftoverMaxGasCase)
	c.Check(leftoverMaxGasVault.Coins.GetCoin(common.XMRAsset).Amount.Equal(leftoverMaxGasCase.receivedAmount), Equals, true)
	c.Check(leftoverMaxGasVault.InboundTxCount, Equals, int64(1))
	c.Check(leftoverMaxGasVoter.UpdatedVault, Equals, true)

	overMaxCase := leftoverMaxGasCase
	overMaxCase.receivedAmount = overMaxCase.scheduledAmount.Add(overMaxCase.maxGas).Add(cosmos.NewUint(1))
	overMaxVault, overMaxVoter := s.runXMRMigrateInbound(c, overMaxCase)
	c.Check(overMaxVault.Coins.GetCoin(common.XMRAsset).IsEmpty(), Equals, true)
	c.Check(overMaxVault.InboundTxCount, Equals, int64(0))
	c.Check(overMaxVoter.UpdatedVault, Equals, false)
}

func (s *HandlerObservedTxInSuite) runXMRMigrateInbound(c *C, testCase xmrMigrateInboundTestCase) (Vault, ObservedTxVoter) {
	ctx, _ := setupKeeperForTest(c)

	sourcePubKey := GetRandomEd25519PubKey()
	sourceAddr, err := sourcePubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	targetPubKey := GetRandomEd25519PubKey()
	targetAddr, err := targetPubKey.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	targetVault := NewVaultV2(
		1,
		ActiveVault,
		AsgardVault,
		targetPubKey,
		common.Chains{common.XMRChain}.Strings(),
		nil,
		GetRandomEd25519PubKey(),
	)
	targetVault.PubKeyFrost = targetPubKey

	txout := NewTxOut(12)
	txout.TxArray = append(txout.TxArray, TxOutItem{
		Chain:            common.XMRChain,
		InHash:           common.BlankTxID,
		ToAddress:        targetAddr,
		VaultPubKeyFrost: sourcePubKey,
		Coin:             common.NewCoin(common.XMRAsset, testCase.scheduledAmount),
		MaxGas:           common.Gas{common.NewCoin(common.XMRAsset, testCase.maxGas)},
		Memo:             NewMigrateMemo(12).String(),
	})

	txID := GetRandomTxHash()
	fromAddr := sourceAddr
	if testCase.unknownSource {
		fromAddr, err = common.NewAddress(common.XMRUnknownSenderAddress(txID.String(), targetAddr.String()))
		c.Assert(err, IsNil)
	}
	tx := NewObservedTx(common.Tx{
		ID:          txID,
		Chain:       common.XMRChain,
		Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, testCase.receivedAmount)),
		Memo:        NewMigrateMemo(12).String(),
		FromAddress: fromAddr,
		ToAddress:   targetAddr,
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, testCase.observedGas)},
	}, 13, targetPubKey, 13)

	keeper := &TestObservedTxInHandleKeeper{
		nas:         NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:       NewObservedTxVoter(tx.Tx.ID, make(ObservedTxs, 0)),
		vault:       targetVault,
		vaultExists: true,
		txOut:       txout,
	}
	handler := NewObservedTxInHandler(NewDummyMgrWithKeeper(keeper))
	msg := NewMsgObservedTxIn(ObservedTxs{tx}, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	voter, err := keeper.GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	return keeper.vault, voter
}

type ObservedTxInHandlerTestHelper struct {
	keeper.Keeper
	failListActiveValidators bool
	failVaultExist           bool
	failGetObservedTxInVote  bool
	failGetVault             bool
	failSetVault             bool
}

func NewObservedTxInHandlerTestHelper(k keeper.Keeper) *ObservedTxInHandlerTestHelper {
	return &ObservedTxInHandlerTestHelper{
		Keeper: k,
	}
}

func (h *ObservedTxInHandlerTestHelper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if h.failListActiveValidators {
		return NodeAccounts{}, errKaboom
	}
	return h.Keeper.ListActiveValidators(ctx)
}

func (h *ObservedTxInHandlerTestHelper) VaultExists(ctx cosmos.Context, pk common.PubKey) bool {
	if h.failVaultExist {
		return false
	}
	return h.Keeper.VaultExists(ctx, pk)
}

func (h *ObservedTxInHandlerTestHelper) GetObservedTxInVoter(ctx cosmos.Context, hash common.TxID) (ObservedTxVoter, error) {
	if h.failGetObservedTxInVote {
		return ObservedTxVoter{}, errKaboom
	}
	return h.Keeper.GetObservedTxInVoter(ctx, hash)
}

func (h *ObservedTxInHandlerTestHelper) GetVault(ctx cosmos.Context, pk common.PubKey) (Vault, error) {
	if h.failGetVault {
		return Vault{}, errKaboom
	}
	return h.Keeper.GetVault(ctx, pk)
}

func (h *ObservedTxInHandlerTestHelper) SetVault(ctx cosmos.Context, vault Vault) error {
	if h.failSetVault {
		return errKaboom
	}
	return h.Keeper.SetVault(ctx, vault)
}

func setupAnLegitObservedTx(ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper, c *C) *MsgObservedTxIn {
	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	pk := GetRandomPubKey()
	tx := GetRandomTx()
	tx.Coins = common.Coins{
		common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One*3)),
	}
	tx.Memo = "SWAP:RUNE"
	addr, err := pk.GetAddress(tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	tx.ToAddress = addr
	obTx := NewObservedTx(tx, ctx.BlockHeight(), pk, ctx.BlockHeight())
	txs := ObservedTxs{obTx}
	txs[0].Tx.ToAddress, err = pk.GetAddress(txs[0].Tx.Coins[0].Asset.Chain)
	c.Assert(err, IsNil)
	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey
	c.Assert(helper.Keeper.SetNodeAccount(ctx, activeNodeAccount), IsNil)
	c.Assert(helper.SetVault(ctx, vault), IsNil)
	p := NewPool()
	p.Asset = common.ETHAsset
	p.BalanceRune = cosmos.NewUint(100 * common.One)
	p.BalanceAsset = cosmos.NewUint(100 * common.One)
	p.Status = PoolAvailable
	c.Assert(helper.Keeper.SetPool(ctx, p), IsNil)
	return NewMsgObservedTxIn(ObservedTxs{
		obTx,
	}, activeNodeAccount.NodeAddress)
}

func (HandlerObservedTxInSuite) TestObservedTxHandler_validations(c *C) {
	testCases := []struct {
		name            string
		messageProvider func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg
		validator       func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string)
	}{
		{
			name: "invalid message should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				return NewMsgNetworkFee(ctx.BlockHeight(), common.ETHChain, 1, 10000, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil)
				c.Check(errors.Is(err, errInvalidMessage), Equals, true)
			},
		},
		{
			name: "message fail validation should return an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				return NewMsgObservedTxIn(ObservedTxs{
					NewObservedTx(GetRandomTx(), ctx.BlockHeight(), GetRandomPubKey(), ctx.BlockHeight()),
				}, GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil)
			},
		},
		{
			name: "signer vote for the same tx should be slashed , and not doing anything else",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				voter, err := helper.Keeper.GetObservedTxInVoter(ctx, m.Txs[0].Tx.ID)
				c.Assert(err, IsNil)
				voter.Add(m.Txs[0], m.Signer)
				helper.Keeper.SetObservedTxInVoter(ctx, voter)
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to list active node accounts should result in an error",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				helper.failListActiveValidators = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil, Commentf(name))
			},
		},
		{
			name: "vault not exist should not result in an error, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				helper.failVaultExist = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to get observedTxInVoter should not result in an error, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				helper.failGetObservedTxInVote = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "empty memo should not result in an error, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				m.Txs[0].Tx.Memo = ""
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
				txOut, err := helper.GetTxOut(ctx, ctx.BlockHeight())
				c.Assert(err, IsNil, Commentf(name))
				c.Assert(txOut.IsEmpty(), Equals, false)
			},
		},
		{
			name: "fail to get vault, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				helper.failGetVault = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "fail to set vault, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				helper.failSetVault = true
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "if the vault is not asgard, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				vault, err := helper.Keeper.GetVault(ctx, m.Txs[0].ObservedPubKey)
				c.Assert(err, IsNil)
				vault.Type = UnknownVault
				c.Assert(helper.Keeper.SetVault(ctx, vault), IsNil)
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "inactive vault, it should continue",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				vault, err := helper.Keeper.GetVault(ctx, m.Txs[0].ObservedPubKey)
				c.Assert(err, IsNil)
				vault.Status = InactiveVault
				c.Assert(helper.Keeper.SetVault(ctx, vault), IsNil)
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
		{
			name: "chain halt, it should refund",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				helper.Keeper.SetMimir(ctx, "HaltTrading", 1)
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
				txOut, err := helper.GetTxOut(ctx, ctx.BlockHeight())
				c.Assert(err, IsNil, Commentf(name))
				c.Assert(txOut.IsEmpty(), Equals, false)
			},
		},
		{
			name: "normal provision, it should success",
			messageProvider: func(c *C, ctx cosmos.Context, helper *ObservedTxInHandlerTestHelper) cosmos.Msg {
				m := setupAnLegitObservedTx(ctx, helper, c)
				m.Txs[0].Tx.Memo = "add:ETH.ETH"
				return m
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *ObservedTxInHandlerTestHelper, name string) {
				c.Check(err, IsNil, Commentf(name))
				c.Check(result, NotNil, Commentf(name))
			},
		},
	}
	versions := []semver.Version{
		GetCurrentVersion(),
	}
	for _, tc := range testCases {
		for _, ver := range versions {
			ctx, mgr := setupManagerForTest(c)
			helper := NewObservedTxInHandlerTestHelper(mgr.Keeper())
			mgr.K = helper
			mgr.currentVersion = ver
			handler := NewObservedTxInHandler(mgr)
			msg := tc.messageProvider(c, ctx, helper)
			result, err := handler.Run(ctx, msg)
			tc.validator(c, ctx, result, err, helper, tc.name)
		}
	}
}

func (s HandlerObservedTxInSuite) TestSwapWithAffiliate(c *C) {
	ctx, mgr := setupManagerForTest(c)

	queue := newSwapQueue(mgr.Keeper())

	affAddr := GetRandomTHORAddress()

	msg := NewMsgSwap(
		common.Tx{
			ID:          common.TxID("5E1DF027321F1FE37CA19B9ECB11C2B4ABEC0D8322199D335D9CE4C39F85F115"),
			FromAddress: GetRandomETHAddress(),
			ToAddress:   GetRandomETHAddress(),
			Gas: common.Gas{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(10000)),
			},
			Chain: common.ETHChain,
			Coins: common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(2*common.One))},
			Memo:  "=:THOR.RUNE:" + GetRandomTHORAddress().String() + "::" + affAddr.String() + ":1000",
		}, common.RuneAsset(), GetRandomTHORAddress(), cosmos.ZeroUint(), affAddr, cosmos.NewUint(1000),
		"",
		"", nil,
		types.SwapType_market,
		0, 0, types.SwapVersion_v1, GetRandomBech32Addr(),
	)
	// no affiliate fees
	c.Assert(addSwap(ctx, mgr, *msg), IsNil)

	// Check if the swap was added to the appropriate queue
	// Route based on message version (V1 goes to regular queue, V2 goes to advanced queue)
	if msg.IsV2() {
		// Swap should be in advanced queue
		swap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, msg.Tx.ID, 0)
		c.Assert(err, IsNil)
		c.Check(swap.Tx.Coins[0].Amount.Uint64(), Equals, uint64(200000000))
	} else {
		// Swap should be in regular queue
		swaps, err := queue.FetchQueue(ctx)
		c.Assert(err, IsNil)
		c.Assert(swaps, HasLen, 1, Commentf("%d", len(swaps)))
		c.Check(swaps[0].msg.Tx.Coins[0].Amount.Uint64(), Equals, uint64(200000000))
	}
}

func (s HandlerObservedTxInSuite) TestAddSwapEmitsLimitEventOnlyForLimitSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Setup target pool required by validation for RUNE -> BTC swaps.
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(10000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	countEvents := func(eventType string) int {
		count := 0
		for _, e := range ctx.EventManager().Events() {
			if e.Type == eventType {
				count++
			}
		}
		return count
	}

	limitSwapEventsBefore := countEvents(types.LimitSwapEventType)

	// V2 market swap should not emit a limit_swap event.
	marketMsg := NewMsgSwap(
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomTHORAddress(),
			ToAddress:   GetRandomTHORAddress(),
			Gas:         common.Gas{},
			Chain:       common.THORChain,
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One))},
			Memo:        "=:BTC.BTC:" + GetRandomBTCAddress().String(),
		}, common.BTCAsset, GetRandomBTCAddress(), cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"",
		"", nil,
		types.SwapType_market,
		0, 0, types.SwapVersion_v2, GetRandomBech32Addr(),
	)
	c.Assert(addSwap(ctx, mgr, *marketMsg), IsNil)
	c.Assert(countEvents(types.LimitSwapEventType), Equals, limitSwapEventsBefore)

	// V2 limit swap should emit exactly one limit_swap event.
	limitMsg := NewMsgSwap(
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomTHORAddress(),
			ToAddress:   GetRandomTHORAddress(),
			Gas:         common.Gas{},
			Chain:       common.THORChain,
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One))},
			Memo:        "=<:BTC.BTC:" + GetRandomBTCAddress().String() + ":1",
		}, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(1), common.NoAddress, cosmos.ZeroUint(),
		"",
		"", nil,
		types.SwapType_limit,
		0, 0, types.SwapVersion_v2, GetRandomBech32Addr(),
	)
	c.Assert(addSwap(ctx, mgr, *limitMsg), IsNil)
	c.Assert(countEvents(types.LimitSwapEventType), Equals, limitSwapEventsBefore+1)
}

func (s HandlerObservedTxInSuite) TestAddSwapDoesNotEmitLimitEventWhenQueueInsertFails(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Put advanced swap queue into market-only mode so v2 limit swaps are rejected.
	mgr.Keeper().SetMimir(ctx, "EnableAdvSwapQueue", int64(AdvSwapQueueModeMarketOnly))

	// Setup target pool required by validation for RUNE -> BTC swaps.
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	pool.BalanceRune = cosmos.NewUint(10000 * common.One)
	pool.Status = PoolAvailable
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	countEvents := func(eventType string) int {
		count := 0
		for _, e := range ctx.EventManager().Events() {
			if e.Type == eventType {
				count++
			}
		}
		return count
	}

	limitSwapEventsBefore := countEvents(types.LimitSwapEventType)

	limitMsg := NewMsgSwap(
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomTHORAddress(),
			ToAddress:   GetRandomTHORAddress(),
			Gas:         common.Gas{},
			Chain:       common.THORChain,
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(2*common.One))},
			Memo:        "=<:BTC.BTC:" + GetRandomBTCAddress().String() + ":1",
		}, common.BTCAsset, GetRandomBTCAddress(), cosmos.NewUint(1), common.NoAddress, cosmos.ZeroUint(),
		"",
		"", nil,
		types.SwapType_limit,
		0, 0, types.SwapVersion_v2, GetRandomBech32Addr(),
	)

	err := addSwap(ctx, mgr, *limitMsg)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "limit swaps are not allowed in market-only mode")
	c.Assert(countEvents(types.LimitSwapEventType), Equals, limitSwapEventsBefore)
}

func (s *HandlerObservedTxInSuite) TestVaultStatus(c *C) {
	testCases := []struct {
		name                 string
		statusAtConsensus    VaultStatus
		statusAtFinalisation VaultStatus
	}{
		{
			name:                 "should observe if active on consensus and finalisation",
			statusAtConsensus:    ActiveVault,
			statusAtFinalisation: ActiveVault,
		}, {
			name:                 "should observe if active on consensus, inactive on finalisation",
			statusAtConsensus:    ActiveVault,
			statusAtFinalisation: InactiveVault,
		}, {
			name:                 "should not observe if inactive on consensus",
			statusAtConsensus:    InactiveVault,
			statusAtFinalisation: InactiveVault,
		},
	}
	for _, tc := range testCases {
		var err error
		ctx, mgr := setupManagerForTest(c)
		tx := GetRandomTx()
		tx.Memo = "SWAP:BTC.BTC:" + GetRandomBTCAddress().String()
		obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 15)
		txs := ObservedTxs{obTx}
		vault := GetRandomVault()
		vault.PubKey = obTx.ObservedPubKey
		keeper := &TestObservedTxInHandleKeeper{
			nas:         NodeAccounts{GetRandomValidatorNode(NodeActive)},
			voter:       NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
			vault:       vault,
			vaultExists: true,
		}
		mgr.K = keeper
		handler := NewObservedTxInHandler(mgr)

		keeper.vault.Status = tc.statusAtConsensus
		msg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
		_, err = handler.handle(ctx, *msg)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Check(keeper.voter.Height, Equals, int64(18), Commentf(tc.name))

		c.Check(keeper.voter.UpdatedVault, Equals, false, Commentf(tc.name))
		c.Check(keeper.vault.InboundTxCount, Equals, int64(0), Commentf(tc.name))

		keeper.vault.Status = tc.statusAtFinalisation
		txs[0].BlockHeight = 15
		msg = NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
		ctx = ctx.WithBlockHeight(30)
		_, err = handler.handle(ctx, *msg)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Check(keeper.voter.FinalisedHeight, Equals, int64(30), Commentf(tc.name))

		c.Check(keeper.voter.UpdatedVault, Equals, true, Commentf(tc.name))
		c.Check(keeper.vault.InboundTxCount, Equals, int64(1), Commentf(tc.name))
	}
}

func (s *HandlerObservedTxInSuite) TestMemolessTxns(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)
	asset := common.BTCAsset

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 10)

	// Test when memo is not empty
	testTx := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "myMemo")
	memo := fetchMemoFromReference(ctx, mgr, asset, testTx, 15)
	c.Check(memo, check.Equals, "myMemo", check.Commentf("Expected to return the same memo when memo is not empty"))

	testTx2 := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx2, 15)
	c.Check(memo, check.Equals, "", check.Commentf("Expected to return empty memo when memo is empty"))

	// Test when reference memo is not expired, happy path
	refMemo := NewReferenceMemo(asset, "the memo", "30000", 10)
	mgr.Keeper().SetReferenceMemo(ctx, refMemo)
	testTx3 := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:30000")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx3, 15) // tx observed at height 15, memo created at height 10
	c.Check(memo, check.Equals, "the memo", check.Commentf("Expected to return reference memo when it's not expired"))

	// Test when reference memo is expired
	ctx = ctx.WithBlockHeight(25)
	testTx4 := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:30000")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx4, 15) // tx observed at height 15, memo created at height 10
	c.Check(memo, check.Equals, "", check.Commentf("Expected to return empty memo when reference memo is expired"))
}

// TestMemolessERC20EndToEnd drives the full observed-tx-in handler pipeline for
// ERC20 memoless inbounds. It verifies that an empty-memo ERC20 tx with an
// amount-encoded reference passes through processTxInAttestation +
// handleObservedTxInQuorum, has its memo synthesized and resolved, and is
// dispatched as a MsgSwap carrying the original registered memo.
//
// Coverage:
//   - 6-decimal amount-mode (USDC): exercises the divide-by-100 normalization
//     branch of ExtractReferenceFromAmount at the handler level.
//   - 18-decimal amount-mode (WETH): exercises the no-op (decimals >= 8)
//     branch and the max ref boundary (99999) at the handler level.
func (s *HandlerObservedTxInSuite) TestMemolessERC20EndToEnd(c *C) {
	s.testMemolessERC20AmountMode6Dec(c)
	s.testMemolessERC20AmountMode18Dec(c)
}

func (s *HandlerObservedTxInSuite) testMemolessERC20AmountMode6Dec(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	// Block height must exceed the reference memo's registration height so the
	// anti-front-running guard in fetchMemoFromReference (tx observation height
	// must be > ref height) passes.
	ctx = ctx.WithBlockHeight(100)

	// Real mainnet USDC contract at 6 decimals.
	usdcAsset := common.Asset{
		Chain:  common.ETHChain,
		Symbol: "USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
		Ticker: "USDC",
		Synth:  false,
	}

	// The registered memo that memoless should resolve to. Kept as a real,
	// parseable swap memo so processOneTxIn can build a MsgSwap from it.
	resolvedMemo := "SWAP:BTC.BTC:" + GetRandomBTCAddress().String()

	// Build the inbound observed tx:
	//   - Empty memo → triggers amount-mode synth path in handleObservedTxInQuorum.
	//   - Amount 100_004_200 at 1e8 precision decodes to ref "00042":
	//       100_004_200 / 100 (= 10^(8-6))  = 1_000_042
	//       1_000_042 % 100_000            = 42 → padded "00042"
	//   - BlockHeight == FinaliseHeight so IsFinal() is true immediately and
	//     a single handler.handle call reaches quorum + runs the memoless logic.
	tx := GetRandomTx()
	tx.Chain = common.ETHChain
	tx.Memo = ""
	tx.Coins = common.NewCoins(common.NewCoin(usdcAsset, cosmos.NewUint(100_004_200)))

	obTx := NewObservedTx(tx, 100, GetRandomPubKey(), 100)
	txs := ObservedTxs{obTx}

	// ToAddress is derived from a chain address; the vault is located by
	// ObservedPubKey (matching the existing testHandleWithVersion pattern).
	pk := GetRandomPubKey()
	txs[0].Tx.ToAddress, err = pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey

	keeper := &TestObservedTxInHandleKeeper{
		nas:   NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter: NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		vault: vault,
		pool: Pool{
			Asset:        usdcAsset,
			Decimals:     6,
			BalanceRune:  cosmos.NewUint(1000 * common.One),
			BalanceAsset: cosmos.NewUint(1000 * common.One),
		},
		vaultExists: true,
	}

	// Pre-register the reference memo for ref "00042" at height 0.
	// Tx observation height will be 100 → 100 > 0 passes the height guard.
	refMemo := NewReferenceMemo(usdcAsset, resolvedMemo, "00042", 0)
	keeper.SetReferenceMemo(ctx, refMemo)

	mgr.K = keeper
	handler := NewObservedTxInHandler(mgr)

	msg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	mgr.ObMgr().EndBlock(ctx, keeper)

	// After the handler runs, the captured MsgSwap should carry the RESOLVED
	// memo (not the synthesized "r:00042", not the empty original memo).
	c.Check(keeper.msg.Tx.ID.String(), Equals, tx.ID.String(),
		Commentf("swap should be dispatched for the inbound tx"))
	c.Check(keeper.msg.Tx.Memo, Equals, resolvedMemo,
		Commentf("resolved memo should be attached to the swap, not r:00042"))

	// The swap target should be BTC.BTC as declared in the registered memo.
	c.Check(keeper.msg.TargetAsset.Equals(common.BTCAsset), Equals, true,
		Commentf("swap target asset should match the resolved memo"))

	// Reference memo usage should be tracked for our tx.
	got, err := keeper.GetReferenceMemo(ctx, usdcAsset, "00042")
	c.Assert(err, IsNil)
	c.Check(got.GetUsageCount(), Equals, int64(1),
		Commentf("reference memo usage count should be 1 after successful resolve"))
	c.Check(got.HasBeenUsedBy(tx.ID), Equals, true,
		Commentf("reference memo should record our tx ID"))
}

func (s *HandlerObservedTxInSuite) testMemolessERC20AmountMode18Dec(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(100)

	// Real mainnet WETH contract at 18 decimals.
	wethAsset := common.Asset{
		Chain:  common.ETHChain,
		Symbol: "WETH-0XC02AAA39B223FE8D0A0E5C4F27EAD9083C756CC2",
		Ticker: "WETH",
		Synth:  false,
	}

	// A different resolved memo from the 6-decimal sub-test so a regression
	// returning stale state is detectable.
	resolvedMemo := "SWAP:BTC.BTC:" + GetRandomBTCAddress().String()

	// Build the inbound observed tx:
	//   - Empty memo → amount-mode synth path.
	//   - Amount 100_099_999 at 1e8 precision decodes to ref "99999":
	//       decimals = 18, so the "decimals < 8" branch is SKIPPED (no division).
	//       100_099_999 % 100_000 = 99999 → "99999" (max ref boundary).
	//   - User-side derivation: 1.00099999 WETH native = 1_000_999_990_000_000_000
	//     raw, which bifrost divides by 10^10 to normalize → 100_099_999 at 1e8.
	tx := GetRandomTx()
	tx.Chain = common.ETHChain
	tx.Memo = ""
	tx.Coins = common.NewCoins(common.NewCoin(wethAsset, cosmos.NewUint(100_099_999)))

	obTx := NewObservedTx(tx, 100, GetRandomPubKey(), 100)
	txs := ObservedTxs{obTx}

	pk := GetRandomPubKey()
	txs[0].Tx.ToAddress, err = pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey

	keeper := &TestObservedTxInHandleKeeper{
		nas:   NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter: NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		vault: vault,
		pool: Pool{
			Asset:        wethAsset,
			Decimals:     18,
			BalanceRune:  cosmos.NewUint(1000 * common.One),
			BalanceAsset: cosmos.NewUint(1000 * common.One),
		},
		vaultExists: true,
	}

	// Register ref "99999" at height 0. Tx observation height is 100 so the
	// anti-front-running guard in fetchMemoFromReference passes.
	refMemo := NewReferenceMemo(wethAsset, resolvedMemo, "99999", 0)
	keeper.SetReferenceMemo(ctx, refMemo)

	mgr.K = keeper
	handler := NewObservedTxInHandler(mgr)

	msg := NewMsgObservedTxIn(txs, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	mgr.ObMgr().EndBlock(ctx, keeper)

	c.Check(keeper.msg.Tx.ID.String(), Equals, tx.ID.String(),
		Commentf("swap should be dispatched for the inbound tx"))
	c.Check(keeper.msg.Tx.Memo, Equals, resolvedMemo,
		Commentf("resolved memo should be attached to the swap, not r:99999"))
	c.Check(keeper.msg.TargetAsset.Equals(common.BTCAsset), Equals, true,
		Commentf("swap target asset should match the resolved memo"))

	got, err := keeper.GetReferenceMemo(ctx, wethAsset, "99999")
	c.Assert(err, IsNil)
	c.Check(got.GetUsageCount(), Equals, int64(1),
		Commentf("reference memo usage count should be 1 after successful resolve"))
	c.Check(got.HasBeenUsedBy(tx.ID), Equals, true,
		Commentf("reference memo should record our tx ID"))
}

func (s *HandlerObservedTxInSuite) TestMemoUsageTracking(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)
	asset := common.BTCAsset

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)
	// Set max usage limit to 1
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnMaxUse.String(), 1)

	// Create a reference memo
	refMemo := NewReferenceMemo(asset, "test memo", "12345", ctx.BlockHeight())
	mgr.Keeper().SetReferenceMemo(ctx, refMemo)

	// Create test transaction IDs
	txID1, _ := common.NewTxID("0000000000000000000000000000000000000000000000000000000000000001")
	txID2, _ := common.NewTxID("0000000000000000000000000000000000000000000000000000000000000002")

	// First usage should succeed
	testTx := common.NewTx(txID1, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:12345")
	memo := fetchMemoFromReference(ctx, mgr, asset, testTx, 16) // tx observed at height 16, memo created at height 15
	c.Check(memo, check.Equals, "test memo", check.Commentf("First usage should succeed"))
	trackReferenceMemoUsage(ctx, mgr, asset, testTx.Memo, txID1)

	// Verify usage count and transaction tracking
	updatedMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, "12345")
	c.Assert(err, check.IsNil)
	c.Check(updatedMemo.GetUsageCount(), check.Equals, int64(1))
	c.Check(updatedMemo.HasBeenUsedBy(txID1), check.Equals, true)

	// Second usage should fail (exceed limit of 1)
	testTx2 := common.NewTx(txID2, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:12345")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx2, 17) // tx observed at height 17, memo created at height 15
	c.Check(memo, check.Equals, "", check.Commentf("Second usage should fail"))

	// A rejected attempt does not consume the reference.
	updatedMemo, err = mgr.Keeper().GetReferenceMemo(ctx, asset, "12345")
	c.Assert(err, check.IsNil)
	c.Check(updatedMemo.GetUsageCount(), check.Equals, int64(1))
	c.Check(updatedMemo.HasBeenUsedBy(txID1), check.Equals, true)
	c.Check(updatedMemo.HasBeenUsedBy(txID2), check.Equals, false)

	// Test duplicate tracking - same txID should not increase count
	trackReferenceMemoUsage(ctx, mgr, asset, testTx.Memo, txID1)
	updatedMemo, err = mgr.Keeper().GetReferenceMemo(ctx, asset, "12345")
	c.Assert(err, check.IsNil)
	c.Check(updatedMemo.GetUsageCount(), check.Equals, int64(1), check.Commentf("Duplicate tracking should not increase count"))
}

func (s *HandlerObservedTxInSuite) TestMemoUsageTrackingDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)
	asset := common.BTCAsset

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)
	// Set max usage limit to 0 (disabled)
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnMaxUse.String(), 0)

	// Create a reference memo
	refMemo := NewReferenceMemo(asset, "test memo", "54321", ctx.BlockHeight())
	mgr.Keeper().SetReferenceMemo(ctx, refMemo)

	// Multiple usages should succeed when limit is disabled (0)
	for i := 0; i < 10; i++ {
		// Track each usage with unique transaction ID
		txID, _ := common.NewTxID(fmt.Sprintf("%064d", i+1))
		testTx := common.NewTx(txID, common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:54321")
		memo := fetchMemoFromReference(ctx, mgr, asset, testTx, int64(16+i)) // tx observed at heights 16-25, memo created at height 15
		c.Check(memo, check.Equals, "test memo", check.Commentf("Usage %d should succeed when limit disabled", i+1))
		trackReferenceMemoUsage(ctx, mgr, asset, testTx.Memo, txID)
	}

	// Verify usage count was incremented for all transactions
	updatedMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, "54321")
	c.Assert(err, check.IsNil)
	c.Check(updatedMemo.GetUsageCount(), check.Equals, int64(10))
}

func (s *HandlerObservedTxInSuite) TestMemolessTransactionReferenceGeneration(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)

	// Setup BTC pool with 8 decimals
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create observed tx with empty memo and specific amount for predictable reference
	tx := GetRandomTx()
	tx.Memo = "" // Empty memo
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))
	obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 15)

	// Setup vault
	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey
	vault.Status = ActiveVault

	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Create reference memo that matches our expected generated reference
	expectedRef := "56789" // last 5 digits of 123456789
	refMemo := NewReferenceMemo(common.BTCAsset, "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890", expectedRef, ctx.BlockHeight())
	mgr.Keeper().SetReferenceMemo(ctx, refMemo)

	// Test that empty memo transaction gets reference memo generated
	handler := NewObservedTxInHandler(mgr)
	msg := NewMsgObservedTxIn(ObservedTxs{obTx}, na.NodeAddress)

	// Process the transaction
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)

	// Verify that the voter's memo was updated with reference memo
	c.Assert(voter.Tx.Tx.Memo, check.Matches, "r:\\d{5}", check.Commentf("Expected reference memo pattern"))
	c.Assert(voter.Tx.Tx.Memo, Equals, "r:"+expectedRef, check.Commentf("Expected specific reference memo"))
}

func (s *HandlerObservedTxInSuite) TestMemolessTransactionWithDifferentAssetDecimals(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)

	// Setup GAIA pool with 6 decimals
	gaiaAsset := common.Asset{Chain: common.GAIAChain, Symbol: "ATOM", Ticker: "ATOM", Synth: false}
	gaiaPool := NewPool()
	gaiaPool.Asset = gaiaAsset
	gaiaPool.Decimals = 6 // GAIA has 6 decimals
	gaiaPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	gaiaPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, gaiaPool), IsNil)

	// Create observed tx with empty memo
	tx := GetRandomTx()
	tx.Memo = "" // Empty memo
	// For 6 decimals, amount should be divided by 100, so 123456780000 / 100 = 1234567800, last 5 = 67800
	tx.Coins = common.NewCoins(common.NewCoin(gaiaAsset, cosmos.NewUint(123456780000)))
	obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 15)

	// Setup vault
	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey
	vault.Status = ActiveVault
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Create reference memo for expected reference
	expectedRef := "67800" // (123456780000 / 100) % 100000 = 1234567800 % 100000 = 67800
	refMemo := NewReferenceMemo(gaiaAsset, "SWAP:BTC.BTC:bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", expectedRef, ctx.BlockHeight())
	mgr.Keeper().SetReferenceMemo(ctx, refMemo)

	// Process the transaction
	handler := NewObservedTxInHandler(mgr)
	msg := NewMsgObservedTxIn(ObservedTxs{obTx}, na.NodeAddress)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.ID)
	c.Assert(err, IsNil)

	// Verify the generated reference memo accounts for decimal precision
	c.Assert(voter.Tx.Tx.Memo, Equals, "r:"+expectedRef, check.Commentf("Expected reference memo adjusted for 6 decimals"))
}

func (s *HandlerObservedTxInSuite) TestMemolessTransactionErrorCases(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Test case 1: Transaction with no coins
	txNoCoin := GetRandomTx()
	txNoCoin.Memo = ""
	txNoCoin.Coins = common.NewCoins() // No coins
	obTxNoCoin := NewObservedTx(txNoCoin, 12, GetRandomPubKey(), 15)

	vault := GetRandomVault()
	vault.PubKey = obTxNoCoin.ObservedPubKey
	vault.Status = ActiveVault

	keeper := &TestObservedTxInHandleKeeper{
		nas:         NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:       NewObservedTxVoter(txNoCoin.ID, make(ObservedTxs, 0)),
		vault:       vault,
		vaultExists: true,
	}
	mgr.K = keeper

	// Process transaction with no coins - should not generate reference memo
	handler := NewObservedTxInHandler(mgr)
	msg := NewMsgObservedTxIn(ObservedTxs{obTxNoCoin}, keeper.nas[0].NodeAddress)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Memo should remain empty since reference generation failed
	c.Assert(keeper.voter.Tx.Tx.Memo, Equals, "", check.Commentf("Memo should remain empty when no coins"))

	// Test case 2: Transaction with zero amount
	txZeroAmount := GetRandomTx()
	txZeroAmount.Memo = ""
	txZeroAmount.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.ZeroUint()))
	obTxZero := NewObservedTx(txZeroAmount, 12, GetRandomPubKey(), 15)

	keeper.voter = NewObservedTxVoter(txZeroAmount.ID, make(ObservedTxs, 0))
	vault.PubKey = obTxZero.ObservedPubKey

	msg = NewMsgObservedTxIn(ObservedTxs{obTxZero}, keeper.nas[0].NodeAddress)
	_, err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Memo should remain empty since amount is zero
	c.Assert(keeper.voter.Tx.Tx.Memo, Equals, "", check.Commentf("Memo should remain empty when amount is zero"))
}

func (s *HandlerObservedTxInSuite) TestMemolessTransactionWithExistingMemo(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)

	// Setup BTC pool
	btcPool := NewPool()
	btcPool.Asset = common.BTCAsset
	btcPool.Decimals = 8
	btcPool.BalanceAsset = cosmos.NewUint(100 * common.One)
	btcPool.BalanceRune = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

	// Create observed tx with existing memo
	existingMemo := "SWAP:ETH.ETH:0x1234567890123456789012345678901234567890"
	tx := GetRandomTx()
	tx.Memo = existingMemo
	tx.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(123456789)))
	obTx := NewObservedTx(tx, 12, GetRandomPubKey(), 15)

	vault := GetRandomVault()
	vault.PubKey = obTx.ObservedPubKey
	vault.Status = ActiveVault

	keeper := &TestObservedTxInHandleKeeper{
		nas:         NodeAccounts{GetRandomValidatorNode(NodeActive)},
		voter:       NewObservedTxVoter(tx.ID, make(ObservedTxs, 0)),
		vault:       vault,
		vaultExists: true,
	}
	mgr.K = keeper

	// Process transaction with existing memo
	handler := NewObservedTxInHandler(mgr)
	msg := NewMsgObservedTxIn(ObservedTxs{obTx}, keeper.nas[0].NodeAddress)
	_, err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	// Memo should remain the original memo (no reference memo generation for non-empty memos)
	c.Assert(keeper.voter.Tx.Tx.Memo, Equals, existingMemo, check.Commentf("Existing memo should be preserved"))
}

func (s *HandlerObservedTxInSuite) TestFetchMemoFromReferenceHeightValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)
	asset := common.BTCAsset

	// Set TTL for memoless transactions
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	// Test case 1: Transaction observed BEFORE memo creation (should fail)
	refMemo1 := NewReferenceMemo(asset, "test memo 1", "11111", 50) // memo created at height 50
	mgr.Keeper().SetReferenceMemo(ctx, refMemo1)

	testTx1 := common.NewTx(GetRandomTxHash(), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:11111")
	memo := fetchMemoFromReference(ctx, mgr, asset, testTx1, 30) // tx observed at height 30 (BEFORE memo creation)
	c.Check(memo, check.Equals, "", check.Commentf("Transaction observed before memo creation should be rejected"))
	storedRefMemo1, err := mgr.Keeper().GetReferenceMemo(ctx, asset, "11111")
	c.Assert(err, IsNil)
	c.Check(storedRefMemo1.GetUsageCount(), Equals, int64(0), Commentf("An older observation must not consume a recycled record"))

	// Test case 2: Transaction observed AT SAME HEIGHT as memo creation (should fail for safety)
	refMemo2 := NewReferenceMemo(asset, "test memo 2", "22222", 60) // memo created at height 60
	mgr.Keeper().SetReferenceMemo(ctx, refMemo2)

	testTx2 := common.NewTx(GetRandomTxHash(), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:22222")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx2, 60) // tx observed at height 60 (SAME as memo creation)
	c.Check(memo, check.Equals, "", check.Commentf("Transaction observed at same height as memo creation should be rejected"))
	storedRefMemo2, err := mgr.Keeper().GetReferenceMemo(ctx, asset, "22222")
	c.Assert(err, IsNil)
	c.Check(storedRefMemo2.GetUsageCount(), Equals, int64(0), Commentf("A same-height observation must not consume an ambiguous record"))

	// Test case 3: Transaction observed AFTER memo creation (should succeed)
	refMemo3 := NewReferenceMemo(asset, "test memo 3", "33333", 70) // memo created at height 70
	mgr.Keeper().SetReferenceMemo(ctx, refMemo3)

	ctx = ctx.WithBlockHeight(80) // current height is 80, memo not expired (70 + 100 > 80)
	testTx3 := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:33333")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx3, 75) // tx observed at height 75 (AFTER memo creation)
	c.Check(memo, check.Equals, "test memo 3", check.Commentf("Transaction observed after memo creation should succeed"))

	// Test case 4: Transaction observed way after memo creation (should succeed)
	refMemo4 := NewReferenceMemo(asset, "test memo 4", "44444", 10) // memo created at height 10
	mgr.Keeper().SetReferenceMemo(ctx, refMemo4)

	ctx = ctx.WithBlockHeight(50) // current height is 50, memo not expired (10 + 100 > 50)
	testTx4 := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:44444")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx4, 45) // tx observed at height 45 (way after memo creation)
	c.Check(memo, check.Equals, "test memo 4", check.Commentf("Transaction observed way after memo creation should succeed"))

	// Test case 5: Edge case - transaction observed 1 block after memo creation (should succeed)
	refMemo5 := NewReferenceMemo(asset, "test memo 5", "55555", 100) // memo created at height 100
	mgr.Keeper().SetReferenceMemo(ctx, refMemo5)

	ctx = ctx.WithBlockHeight(150) // current height is 150, memo not expired (100 + 100 > 150 is false, but we test height validation first)
	testTx5 := common.NewTx(common.TxID(""), common.NoAddress, common.NoAddress, common.Coins{}, common.Gas{}, "r:55555")
	memo = fetchMemoFromReference(ctx, mgr, asset, testTx5, 101) // tx observed at height 101 (1 block after memo creation)
	c.Check(memo, check.Equals, "test memo 5", check.Commentf("Transaction observed 1 block after memo creation should succeed"))
}

func (s *HandlerObservedTxInSuite) TestResolveReferenceMemoUpdatesObservedTxCopy(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(15)
	asset := common.XMRAsset

	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)

	thorAddr := GetRandomTHORAddress()
	xmrAddr, err := GetRandomEd25519PubKey().GetAddress(common.XMRChain)
	c.Assert(err, IsNil)
	registered := fmt.Sprintf("+:XMR.XMR:%s/%s", thorAddr, xmrAddr)
	mgr.Keeper().SetReferenceMemo(ctx, NewReferenceMemo(asset, registered, "40000", 10))

	unknownSender, err := common.NewAddress(common.XMRUnknownSenderAddress(GetRandomTxHash().String(), xmrAddr.String()))
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.XMRChain,
		Coins:       common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))},
		Memo:        "r:40000",
		FromAddress: unknownSender,
		ToAddress:   GetRandomTHORAddress(),
		Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10000))},
	}, 1024, common.EmptyPubKey, 1024)
	voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
	voter.Tx = tx
	voter.UnfinalizedHeight = 15

	resolvedMemo, ok := resolveReferenceMemo(ctx, mgr, mgr.Keeper(), &tx, &voter)

	// Refund paths parse tx.Tx.Memo, while processing uses voter.Tx.Tx.Memo.
	// Both copies and downstream completion logic must use the registered memo.
	c.Assert(ok, Equals, true)
	c.Check(resolvedMemo.IsType(TxAdd), Equals, true)
	c.Check(resolvedMemo.GetType().HasOutbound(), Equals, false)
	c.Check(tx.Tx.Memo, Equals, registered)
	c.Check(voter.Tx.Tx.Memo, Equals, registered)

	stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Check(stored.Tx.Tx.Memo, Equals, registered)
}

// TestResolveReferenceMemoPreservesRawMemoOnFailure verifies failed reference
// lookups reject processing without erasing the observed r:<ref> from refunds.
func (s *HandlerObservedTxInSuite) TestResolveReferenceMemoPreservesRawMemoOnFailure(c *C) {
	testCases := []struct {
		name      string
		reference string
		setup     func(cosmos.Context, *Mgrs)
	}{
		{
			name:      "missing",
			reference: "40001",
			setup: func(ctx cosmos.Context, mgr *Mgrs) {
				mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)
			},
		},
		{
			name:      "expired",
			reference: "40003",
			setup: func(ctx cosmos.Context, mgr *Mgrs) {
				mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 5)
				mgr.Keeper().SetReferenceMemo(ctx, NewReferenceMemo(common.XMRAsset, "+:XMR.XMR", "40003", 10))
			},
		},
		{
			name:      "usage limit exceeded",
			reference: "40004",
			setup: func(ctx cosmos.Context, mgr *Mgrs) {
				mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 100)
				mgr.Keeper().SetMimir(ctx, constants.MemolessTxnMaxUse.String(), 1)
				refMemo := NewReferenceMemo(common.XMRAsset, "+:XMR.XMR", "40004", 10)
				refMemo.AddUsage(GetRandomTxHash())
				mgr.Keeper().SetReferenceMemo(ctx, refMemo)
			},
		},
	}

	for _, tc := range testCases {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(20)
		tc.setup(ctx, mgr)

		rawMemo := "r:" + tc.reference
		tx := common.NewObservedTx(common.Tx{
			ID:    GetRandomTxHash(),
			Chain: common.XMRChain,
			Coins: common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(100*common.One))},
			Memo:  rawMemo,
		}, 15, common.EmptyPubKey, 15)
		voter := NewObservedTxVoter(tx.Tx.ID, []common.ObservedTx{tx})
		voter.Tx = tx
		voter.UnfinalizedHeight = 15

		resolvedMemo, ok := resolveReferenceMemo(ctx, mgr, mgr.Keeper(), &tx, &voter)

		c.Check(ok, Equals, false, Commentf(tc.name))
		c.Check(resolvedMemo.IsEmpty(), Equals, true, Commentf(tc.name))
		c.Check(tx.Tx.Memo, Equals, rawMemo, Commentf(tc.name))
		c.Check(voter.Tx.Tx.Memo, Equals, "", Commentf(tc.name))

		stored, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
		c.Assert(err, IsNil, Commentf(tc.name))
		c.Check(stored.Tx.Tx.Memo, Equals, "", Commentf(tc.name))
	}
}
