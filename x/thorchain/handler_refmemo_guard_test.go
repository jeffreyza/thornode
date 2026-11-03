package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// HandlerRefMemoGuardSuite covers HANDLER-026: a reference-read memo (r:<ref>)
// passes the inbound IsOutbound()/IsInternal() guard while unresolved, then
// resolves to a reserved validator-internal memo type. Without the re-applied
// guard the resolved OUT:/REFUND:/RAGNAROK:/MIGRATE:/CONSOLIDATE memo reaches
// CommonOutboundTxHandler, which finds no matching TxOutItem and slashes the
// (real) Asgard vault 1.5x, halting the chain past PauseOnSlashThreshold.
type HandlerRefMemoGuardSuite struct{}

var _ = Suite(&HandlerRefMemoGuardSuite{})

// reservedReference is one exploit scenario: a planted reference whose stored
// target memo is a reserved validator-internal type.
type reservedReference struct {
	name   string
	target string
}

func (s *HandlerRefMemoGuardSuite) reservedCases() []reservedReference {
	outTxID := GetRandomTxHash().String()
	refundTxID := GetRandomTxHash().String()
	return []reservedReference{
		{"OUT", "OUT:" + outTxID},
		{"REFUND", "REFUND:" + refundTxID},
		{"RAGNAROK", "RAGNAROK:1"},
		{"MIGRATE", "MIGRATE:1"},
		{"CONSOLIDATE", "CONSOLIDATE"},
	}
}

// TestObservedTxInResolvedForbiddenTypeNoSlashNoHalt drives the exploitable
// observed-tx-in path for every reserved type and asserts the patched outcome:
// the inbound is refunded, no bond is slashed, no per-chain halt mimir is set,
// and LastSignedHeight is not poisoned.
func (s *HandlerRefMemoGuardSuite) TestObservedTxInResolvedForbiddenTypeNoSlashNoHalt(c *C) {
	for _, tc := range s.reservedCases() {
		s.runObservedTxInCase(c, tc)
	}
}

func (s *HandlerRefMemoGuardSuite) runObservedTxInCase(c *C, tc reservedReference) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(18)
	k := mgr.Keeper()

	// Pause-on-slash threshold and 1.5x penalty in effect.
	c.Assert(mgr.GetConstants().GetInt64Value(constants.SlashPenalty), Equals, int64(15000))

	// Three active validators bonded; they form the vault membership.
	nas := NodeAccounts{
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
	}
	membership := make([]string, 0, len(nas))
	for _, na := range nas {
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		membership = append(membership, na.PubKeySet.Secp256k1.String())
	}
	initialBond := s.totalMembershipBond(c, ctx, k, membership)

	// Real active Asgard vault, recipient of the inbound, holding ETH.
	vault := GetRandomVault()
	vault.Membership = membership
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(200*common.One))})
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// 1:1 ETH pool so the inbound's RUNE value (and thus 1.5x slash) exceeds
	// PauseOnSlashThreshold (100 RUNE) — i.e. enough to trip the halt pre-fix.
	pool := NewPool()
	pool.Asset = common.ETHAsset
	pool.BalanceAsset = cosmos.NewUint(10_000 * common.One)
	pool.BalanceRune = cosmos.NewUint(10_000 * common.One)
	pool.Decimals = 8
	pool.Status = PoolAvailable
	c.Assert(k.SetPool(ctx, pool), IsNil)

	// Plant the dangerous reference (as if registered before the Phase 2a gate).
	mgr.Keeper().SetMimir(ctx, constants.MemolessTxnTTL.String(), 1000)
	const ref = "12345"
	k.SetReferenceMemo(ctx, NewReferenceMemo(common.ETHAsset, tc.target, ref, 1))

	lastSignedBefore, err := k.GetLastSignedHeight(ctx)
	c.Assert(err, IsNil)

	// One genuine finalised L1 inbound to the vault carrying r:<ref>.
	from := GetRandomETHAddress()
	to := GetRandomETHAddress()
	inTx := common.NewTx(
		GetRandomTxHash(),
		from,
		to,
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10_000))},
		"r:"+ref,
	)
	inTx.Chain = common.ETHChain
	obsTx := common.NewObservedTx(inTx, 18, vault.PubKey, 18) // BlockHeight==FinaliseHeight => final

	voter := NewObservedTxVoter(inTx.ID, []common.ObservedTx{})
	voter.Tx = obsTx
	voter.Height = 18
	voter.FinalisedHeight = 18
	voter.UnfinalizedHeight = 18 // observation height used for reference resolution (> memo height 1)

	handler := NewInternalHandler(mgr)
	err = handleObservedTxInQuorum(ctx, mgr, nas[0].NodeAddress, nas, handler, obsTx, voter, voter.Tx.GetSigners(), true)
	c.Assert(err, IsNil, Commentf("case %s", tc.name))

	// No slash: membership bond is untouched.
	finalBond := s.totalMembershipBond(c, ctx, k, membership)
	c.Check(finalBond.Equal(initialBond), Equals, true,
		Commentf("case %s: bond changed from %s to %s", tc.name, initialBond, finalBond))

	// No per-chain halt: signing/trading mimirs were never set.
	haltSigning, err := k.GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltSigning, common.ETHChain))
	c.Assert(err, IsNil)
	c.Check(haltSigning <= 0, Equals, true, Commentf("case %s: HaltSigningETH=%d", tc.name, haltSigning))
	haltTrading, err := k.GetMimir(ctx, fmt.Sprintf(constants.MimirTemplateHaltTrading, common.ETHChain))
	c.Assert(err, IsNil)
	c.Check(haltTrading <= 0, Equals, true, Commentf("case %s: HaltETHTrading=%d", tc.name, haltTrading))

	// No security event for an unmatched outbound.
	c.Check(hasMissingTxOutSecurityEvent(ctx), Equals, false, Commentf("case %s", tc.name))

	// No telemetry/genesis poisoning via LastSignedHeight (migrate/ragnarok).
	lastSignedAfter, err := k.GetLastSignedHeight(ctx)
	c.Assert(err, IsNil)
	c.Check(lastSignedAfter, Equals, lastSignedBefore, Commentf("case %s", tc.name))
}

func (s *HandlerRefMemoGuardSuite) totalMembershipBond(c *C, ctx cosmos.Context, k interface {
	GetNodeAccountByPubKey(cosmos.Context, common.PubKey) (NodeAccount, error)
}, membership []string,
) cosmos.Uint {
	total := cosmos.ZeroUint()
	for _, m := range membership {
		pk, err := common.NewPubKey(m)
		c.Assert(err, IsNil)
		na, err := k.GetNodeAccountByPubKey(ctx, pk)
		c.Assert(err, IsNil)
		total = total.Add(na.Bond)
	}
	return total
}

// TestDepositResolvedForbiddenTypeRejected covers the native MsgDeposit path:
// an r:<ref> resolving to a reserved type must error and roll back (fee-only
// loss), never reaching module routing.
func (s *HandlerRefMemoGuardSuite) TestDepositResolvedForbiddenTypeRejected(c *C) {
	for _, tc := range s.reservedCases() {
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithBlockHeight(18)
		k := mgr.Keeper()

		activeNode := GetRandomValidatorNode(NodeActive)
		c.Assert(k.SetNodeAccount(ctx, activeNode), IsNil)

		signer := GetRandomBech32Addr()
		FundAccount(c, ctx, k, signer, 300*common.One)

		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.Status = PoolAvailable
		c.Assert(k.SetPool(ctx, pool), IsNil)

		k.SetMimir(ctx, constants.MemolessTxnTTL.String(), 1000)
		const ref = "12345"
		k.SetReferenceMemo(ctx, NewReferenceMemo(common.RuneNative, tc.target, ref, 1))

		coins := common.Coins{common.NewCoin(common.RuneNative, cosmos.NewUint(10*common.One))}
		msg := NewMsgDeposit(coins, "r:"+ref, signer)

		handler := NewDepositHandler(mgr)
		_, err := handler.handle(ctx, *msg, 0)
		c.Assert(err, NotNil, Commentf("case %s", tc.name))
		c.Check(err.Error(), Matches, ".*forbidden type.*", Commentf("case %s", tc.name))
	}
}

// hasMissingTxOutSecurityEvent reports whether a vault-slash "missing tx out"
// security event was emitted in this context.
func hasMissingTxOutSecurityEvent(ctx cosmos.Context) bool {
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "security" {
			continue
		}
		for _, attr := range event.Attributes {
			if attr.Key == "msg" && len(attr.Value) >= len("missing tx out") && attr.Value[:len("missing tx out")] == "missing tx out" {
				return true
			}
		}
	}
	return false
}
