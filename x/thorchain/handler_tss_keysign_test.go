package thorchain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/blang/semver"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerTssKeysignSuite struct{}

var _ = Suite(&HandlerTssKeysignSuite{})

const keysignSlashTelemetryChild = "THORNODE_TEST_KEYSIGN_SLASH_TELEMETRY_CHILD"

type tssKeysignFailHandlerTestHelper struct {
	ctx           cosmos.Context
	version       semver.Version
	keeper        *tssKeysignKeeperHelper
	constAccessor constants.ConstantValues
	nodeAccount   NodeAccount
	mgr           Manager
	blame         Blame
	retiringVault Vault
}

type tssKeysignKeeperHelper struct {
	keeper.Keeper
	errListActiveAccounts           bool
	errGetTssVoter                  bool
	errFailToGetNodeAccountByPubKey bool
	errFailSetNodeAccount           bool
	failIncNodeAccountSlashPointsAt int
	incNodeAccountSlashPointsCalls  int
	errDecNodeAccountSlashPoints    bool
	errSetNodeAccountJail           bool
	returnEmptyVault                bool
	failSetTssKeysignFailVoterAt    int
	setTssKeysignFailVoterCalls     int
	nextTssKeysignFailVoter         *TssKeysignFailVoter
}

func newTssKeysignFailKeeperHelper(keeper keeper.Keeper) *tssKeysignKeeperHelper {
	return &tssKeysignKeeperHelper{
		Keeper: keeper,
	}
}

func (k *tssKeysignKeeperHelper) GetNodeAccountByPubKey(ctx cosmos.Context, pk common.PubKey) (NodeAccount, error) {
	if k.errFailToGetNodeAccountByPubKey {
		return NodeAccount{}, errKaboom
	}
	return k.Keeper.GetNodeAccountByPubKey(ctx, pk)
}

func (k *tssKeysignKeeperHelper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if k.errFailSetNodeAccount {
		return errKaboom
	}
	return k.Keeper.SetNodeAccount(ctx, na)
}

func (k *tssKeysignKeeperHelper) IncNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress, pts int64) error {
	k.incNodeAccountSlashPointsCalls++
	if k.failIncNodeAccountSlashPointsAt == k.incNodeAccountSlashPointsCalls {
		return errKaboom
	}
	return k.Keeper.IncNodeAccountSlashPoints(ctx, addr, pts)
}

func (k *tssKeysignKeeperHelper) DecNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress, pts int64) error {
	if k.errDecNodeAccountSlashPoints {
		return errKaboom
	}
	return k.Keeper.DecNodeAccountSlashPoints(ctx, addr, pts)
}

func (k *tssKeysignKeeperHelper) SetNodeAccountJail(ctx cosmos.Context, addr cosmos.AccAddress, height int64, reason string) error {
	if k.errSetNodeAccountJail {
		return errKaboom
	}
	return k.Keeper.SetNodeAccountJail(ctx, addr, height, reason)
}

func (k *tssKeysignKeeperHelper) GetVault(ctx cosmos.Context, pubKey common.PubKey) (Vault, error) {
	if k.returnEmptyVault {
		return Vault{}, nil
	}
	return k.Keeper.GetVault(ctx, pubKey)
}

func (k *tssKeysignKeeperHelper) GetTssKeysignFailVoter(ctx cosmos.Context, id string) (TssKeysignFailVoter, error) {
	if k.errGetTssVoter {
		return TssKeysignFailVoter{}, errKaboom
	}
	if k.nextTssKeysignFailVoter != nil {
		voter := *k.nextTssKeysignFailVoter
		k.nextTssKeysignFailVoter = nil
		return voter, nil
	}
	return k.Keeper.GetTssKeysignFailVoter(ctx, id)
}

func (k *tssKeysignKeeperHelper) SetTssKeysignFailVoter(ctx cosmos.Context, voter TssKeysignFailVoter) error {
	k.setTssKeysignFailVoterCalls++
	if k.setTssKeysignFailVoterCalls == k.failSetTssKeysignFailVoterAt {
		return errKaboom
	}
	return k.Keeper.SetTssKeysignFailVoter(ctx, voter)
}

func (k *tssKeysignKeeperHelper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.errListActiveAccounts {
		return NodeAccounts{}, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func (k *tssKeysignKeeperHelper) CountActiveValidators(ctx cosmos.Context) (int, error) {
	if k.errListActiveAccounts {
		return 0, errKaboom
	}
	return k.Keeper.CountActiveValidators(ctx)
}

func signVoter(ctx cosmos.Context, keeper keeper.Keeper, except cosmos.AccAddress) (result []cosmos.AccAddress) {
	active, _ := keeper.ListActiveValidators(ctx)
	for _, na := range active {
		if na.NodeAddress.Equals(except) {
			continue
		}
		result = append(result, na.NodeAddress)
	}
	return
}

// legacyKeysignFailID reproduces the raw round hashing used before canonical
// round normalization so tests can assert those IDs are rejected.
func legacyKeysignFailID(members []Node, height int64, memo string, coins common.Coins, pubKey common.PubKey, round string) (string, error) {
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].Pubkey < members[j].Pubkey
	})

	var sb strings.Builder
	for _, item := range members {
		sb.WriteString(item.Pubkey)
	}
	sb.WriteString(fmt.Sprintf("%d", height))
	sb.WriteString(memo)
	sb.WriteString(pubKey.String())
	for _, coin := range coins {
		sb.WriteString(coin.String())
	}
	sb.WriteString(round)

	hash := sha256.New()
	if _, err := hash.Write([]byte(sb.String())); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func newTssKeysignHandlerTestHelper(c *C) tssKeysignFailHandlerTestHelper {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1023)
	keeperHelper := newTssKeysignFailKeeperHelper(k)
	// active account
	nodeAccount := GetRandomValidatorNode(NodeActive)
	nodeAccount.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(keeperHelper.SetNodeAccount(ctx, nodeAccount), IsNil)
	constAccessor := constants.GetConstantValues(GetCurrentVersion())
	mgr := NewDummyMgr()

	var members []Node
	for i := 0; i < 8; i++ {
		na := GetRandomValidatorNode(NodeActive)
		members = append(members, Node{Pubkey: na.PubKeySet.Secp256k1.String()})
		_ = keeperHelper.SetNodeAccount(ctx, na)
	}
	blame := Blame{
		FailReason: "whatever",
		Round:      tssMessages.KEYSIGN1aUnicast,
		BlameNodes: []Node{members[0], members[1]},
	}
	asgardVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(keeperHelper.SetVault(ctx, asgardVault), IsNil)
	retiringVault := NewVault(ctx.BlockHeight(), RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	for _, item := range members {
		retiringVault.Membership = append(retiringVault.Membership, item.Pubkey)
	}
	c.Assert(keeperHelper.SetVault(ctx, retiringVault), IsNil)
	return tssKeysignFailHandlerTestHelper{
		ctx:           ctx,
		version:       GetCurrentVersion(),
		keeper:        keeperHelper,
		constAccessor: constAccessor,
		nodeAccount:   nodeAccount,
		mgr:           mgr,
		blame:         blame,
		retiringVault: retiringVault,
	}
}

func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler(c *C) {
	testCases := []struct {
		name           string
		messageCreator func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg
		runner         func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error)
		validator      func(helper tssKeysignFailHandlerTestHelper, msg cosmos.Msg, result *cosmos.Result, c *C)
		expectedResult error
	}{
		{
			name: "invalid message should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				return NewMsgNoOp(GetRandomObservedTx(), helper.nodeAccount.NodeAddress, "")
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errInvalidMessage,
		},
		{
			name: "Not signed by an active account should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, GetRandomBech32Addr(), GetRandomPubKey())
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnauthorized,
		},
		{
			name: "empty signer should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, cosmos.AccAddress{}, GetRandomPubKey())
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrInvalidAddress,
		},
		{
			name: "empty id should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, GetRandomPubKey())
				c.Assert(err, IsNil)
				tssMsg.ID = ""
				return tssMsg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "empty member pubkeys should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), Blame{
					FailReason: "",
					Round:      tssMessages.KEYSIGN1aUnicast,
					BlameNodes: []Node{},
				}, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, GetRandomPubKey())
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "normal blame should works fine",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, helper.retiringVault.PubKey)
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "when the same signer already sign the tss keysign failure , it should not do anything",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, GetRandomPubKey())
				c.Assert(err, IsNil)
				voter, _ := helper.keeper.Keeper.GetTssKeysignFailVoter(helper.ctx, msg.ID)
				voter.Sign(msg.Signer)
				c.Assert(helper.keeper.Keeper.SetTssKeysignFailVoter(helper.ctx, voter), IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "fail to list active node accounts should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, GetRandomPubKey())
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errListActiveAccounts = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "fail to get Tss Keysign fail voter should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, GetRandomPubKey())
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errGetTssVoter = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "fail to get node account should return an error",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, GetRandomPubKey())
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				mmsg, ok := msg.(*MsgTssKeysignFail)
				c.Assert(ok, Equals, true)
				// prepopulate the voter with other signers
				voter, _ := helper.keeper.GetTssKeysignFailVoter(helper.ctx, mmsg.ID)
				signers := signVoter(helper.ctx, helper.keeper, mmsg.Signer)
				voter.Signers = make([]string, len(signers))
				for i, sign := range signers {
					voter.Signers[i] = sign.String()
				}
				c.Assert(helper.keeper.SetTssKeysignFailVoter(helper.ctx, voter), IsNil)
				helper.keeper.errFailToGetNodeAccountByPubKey = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errInternal,
		},
		{
			name: "without majority it should not take any actions",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, helper.retiringVault.PubKey)
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				for i := 0; i < 3; i++ {
					na := GetRandomValidatorNode(NodeActive)
					if err := helper.keeper.SetNodeAccount(helper.ctx, na); err != nil {
						return nil, ErrInternal(err, "fail to set node account")
					}
				}
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "with majority it should take actions",
			messageCreator: func(helper tssKeysignFailHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, helper.nodeAccount.NodeAddress, helper.retiringVault.PubKey)
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssKeysignHandler, msg cosmos.Msg, helper tssKeysignFailHandlerTestHelper) (*cosmos.Result, error) {
				var na NodeAccount
				for i := 0; i < 3; i++ {
					na = GetRandomValidatorNode(NodeActive)
					if err := helper.keeper.SetNodeAccount(helper.ctx, na); err != nil {
						return nil, ErrInternal(err, "fail to set node account")
					}
				}
				_, err := handler.Run(helper.ctx, msg)
				if err != nil {
					return nil, err
				}
				msg, err = NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, na.NodeAddress, helper.retiringVault.PubKey)
				c.Assert(err, IsNil)
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
	}
	for _, tc := range testCases {
		helper := newTssKeysignHandlerTestHelper(c)
		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
		msg := tc.messageCreator(helper)

		c.Logf(">Name: %s\n", tc.name)
		result, err := tc.runner(handler, msg, helper)
		if tc.expectedResult == nil {
			c.Logf("Name: %s, %s\n", tc.name, err)
			c.Assert(err, IsNil)
		} else {
			c.Assert(errors.Is(err, tc.expectedResult), Equals, true, Commentf("name:%s, %w", tc.name, err))
		}
		if tc.validator != nil {
			tc.validator(helper, msg, result, c)
		}
	}
}

func (h HandlerTssKeysignSuite) TestKeysignFailVoterSetterErrorsPropagate(c *C) {
	newMessage := func(helper *tssKeysignFailHandlerTestHelper, signer cosmos.AccAddress, pubKey common.PubKey) *MsgTssKeysignFail {
		msg, err := NewMsgTssKeysignFail(
			helper.ctx.BlockHeight(),
			helper.blame,
			"setter-error",
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
			signer,
			pubKey,
		)
		c.Assert(err, IsNil)
		return msg
	}

	tests := []struct {
		name    string
		failAt  int
		prepare func(*tssKeysignFailHandlerTestHelper) *MsgTssKeysignFail
	}{
		{
			name:   "empty vault voter",
			failAt: 1,
			prepare: func(helper *tssKeysignFailHandlerTestHelper) *MsgTssKeysignFail {
				helper.keeper.returnEmptyVault = true
				return newMessage(helper, helper.nodeAccount.NodeAddress, GetRandomPubKey())
			},
		},
		{
			name:   "pending voter",
			failAt: 1,
			prepare: func(helper *tssKeysignFailHandlerTestHelper) *MsgTssKeysignFail {
				return newMessage(helper, helper.nodeAccount.NodeAddress, helper.retiringVault.PubKey)
			},
		},
		{
			name:   "completed consensus voter",
			failAt: 1,
			prepare: func(helper *tssKeysignFailHandlerTestHelper) *MsgTssKeysignFail {
				signerPubKey, err := common.NewPubKey(helper.retiringVault.Membership[0])
				c.Assert(err, IsNil)
				signer, err := signerPubKey.GetThorAddress()
				c.Assert(err, IsNil)
				msg := newMessage(helper, signer, helper.retiringVault.PubKey)

				voter := types.NewTssKeysignFailVoter(msg.ID)
				for _, member := range helper.retiringVault.Membership[1:4] {
					memberPubKey, err := common.NewPubKey(member)
					c.Assert(err, IsNil)
					memberAddress, err := memberPubKey.GetThorAddress()
					c.Assert(err, IsNil)
					c.Assert(voter.Sign(memberAddress), Equals, true)
				}
				c.Assert(helper.keeper.Keeper.SetTssKeysignFailVoter(helper.ctx, voter), IsNil)
				return msg
			},
		},
	}

	for _, test := range tests {
		c.Logf("case: %s", test.name)
		helper := newTssKeysignHandlerTestHelper(c)
		msg := test.prepare(&helper)
		helper.keeper.setTssKeysignFailVoterCalls = 0
		helper.keeper.failSetTssKeysignFailVoterAt = test.failAt

		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
		_, err := handler.Run(helper.ctx, msg)
		c.Assert(errors.Is(err, errKaboom), Equals, true, Commentf("case: %s, error: %v", test.name, err))
		c.Check(helper.keeper.setTssKeysignFailVoterCalls, Equals, test.failAt)
	}
}

// TestTerminalKeysignFailureEffectsAreAtomic exercises the production
// DeliverTx commit boundary: a failed handler result discards the cache, while
// a nil result commits it. Required node penalties must therefore fail the
// handler rather than allowing the completed voter tombstone to commit without
// the corresponding slash or jail state.
func (h HandlerTssKeysignSuite) TestTerminalKeysignFailureEffectsAreAtomic(c *C) {
	tests := []struct {
		name          string
		terminalRound bool
		injectErr     func(*tssKeysignKeeperHelper)
	}{
		{
			name: "observation slash point persistence",
			injectErr: func(k *tssKeysignKeeperHelper) {
				k.failIncNodeAccountSlashPointsAt = 1
			},
		},
		{
			name: "blamed-node slash point persistence",
			injectErr: func(k *tssKeysignKeeperHelper) {
				// The quorum observation increment is the first call; the blamed-
				// node terminal penalty is the second.
				k.failIncNodeAccountSlashPointsAt = 2
			},
		},
		{
			name: "jail persistence",
			injectErr: func(k *tssKeysignKeeperHelper) {
				k.errSetNodeAccountJail = true
			},
		},
		{
			name: "observation slash point refund",
			injectErr: func(k *tssKeysignKeeperHelper) {
				k.errDecNodeAccountSlashPoints = true
			},
		},
		{
			name:          "final replay barrier persistence",
			terminalRound: true,
			injectErr: func(k *tssKeysignKeeperHelper) {
				k.failSetTssKeysignFailVoterAt = 1
			},
		},
	}

	for _, test := range tests {
		c.Logf("case: %s", test.name)
		helper := newTssKeysignHandlerTestHelper(c)
		signerPubKey, err := common.NewPubKey(helper.retiringVault.Membership[0])
		c.Assert(err, IsNil)
		signer, err := signerPubKey.GetThorAddress()
		c.Assert(err, IsNil)
		blame := helper.blame
		if test.terminalRound {
			blame.Round = tssMessages.KEYSIGN9
		}
		msg, err := NewMsgTssKeysignFail(
			helper.ctx.BlockHeight(),
			blame,
			"terminal-effect-atomicity",
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
			signer,
			helper.retiringVault.PubKey,
		)
		c.Assert(err, IsNil)

		// Three existing votes plus this member's vote reach the four-of-eight
		// simple-majority threshold.
		voter := types.NewTssKeysignFailVoter(msg.ID)
		for _, member := range helper.retiringVault.Membership[1:4] {
			memberPubKey, memberErr := common.NewPubKey(member)
			c.Assert(memberErr, IsNil)
			memberAddress, memberErr := memberPubKey.GetThorAddress()
			c.Assert(memberErr, IsNil)
			c.Assert(voter.Sign(memberAddress), Equals, true)
		}
		if test.terminalRound {
			voter.LastRoundCount = 1
		}
		c.Assert(helper.keeper.Keeper.SetTssKeysignFailVoter(helper.ctx, voter), IsNil)
		helper.keeper.setTssKeysignFailVoterCalls = 0
		test.injectErr(helper.keeper)
		beforeEvents := len(helper.ctx.EventManager().Events())
		beforeVault, vaultErr := helper.keeper.GetVault(helper.ctx, helper.retiringVault.PubKey)
		c.Assert(vaultErr, IsNil)

		cacheCtx, commit := helper.ctx.CacheContext()
		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
		_, runErr := handler.Run(cacheCtx, msg)
		if runErr == nil {
			commit()
		}

		c.Check(errors.Is(runErr, errKaboom), Equals, true, Commentf("case: %s, error: %v", test.name, runErr))
		if test.terminalRound {
			// Inspect the uncommitted cache to prove the failing setter was the
			// final replay barrier, reached only after the terminal effects.
			c.Check(helper.keeper.setTssKeysignFailVoterCalls, Equals, 1)
			cachedVault, cachedVaultErr := helper.keeper.GetVault(cacheCtx, helper.retiringVault.PubKey)
			c.Assert(cachedVaultErr, IsNil)
			c.Check(cachedVault.Frozen, DeepEquals, []string{common.ETHChain.String()})
			failureEventFound := false
			for _, event := range cacheCtx.EventManager().Events() {
				if event.Type == types.TSSKeysignFailure {
					failureEventFound = true
					break
				}
			}
			c.Check(failureEventFound, Equals, true)
			for _, node := range blame.BlameNodes {
				pubKey, nodeErr := common.NewPubKey(node.Pubkey)
				c.Assert(nodeErr, IsNil)
				address, nodeErr := pubKey.GetThorAddress()
				c.Assert(nodeErr, IsNil)
				points, nodeErr := helper.keeper.GetNodeAccountSlashPoints(cacheCtx, address)
				c.Assert(nodeErr, IsNil)
				c.Check(points > 0, Equals, true)
				jail, nodeErr := helper.keeper.GetNodeAccountJail(cacheCtx, address)
				c.Assert(nodeErr, IsNil)
				c.Check(jail.IsJailed(cacheCtx), Equals, true)
			}
		}
		stored, getErr := helper.keeper.Keeper.GetTssKeysignFailVoter(helper.ctx, msg.ID)
		c.Assert(getErr, IsNil)
		c.Check(stored.State, Equals, types.TssKeysignFailVoterState_PendingVoter, Commentf("case: %s", test.name))
		c.Check(stored.Signers, HasLen, 3, Commentf("case: %s", test.name))
		if test.terminalRound {
			c.Check(stored.LastRoundCount, Equals, int64(1))
			c.Check(len(helper.ctx.EventManager().Events()), Equals, beforeEvents)
			afterVault, afterVaultErr := helper.keeper.GetVault(helper.ctx, helper.retiringVault.PubKey)
			c.Assert(afterVaultErr, IsNil)
			c.Check(afterVault.Frozen, DeepEquals, beforeVault.Frozen)
			points, pointsErr := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, signer)
			c.Assert(pointsErr, IsNil)
			c.Check(points, Equals, int64(0))
			for _, node := range blame.BlameNodes {
				pubKey, nodeErr := common.NewPubKey(node.Pubkey)
				c.Assert(nodeErr, IsNil)
				address, nodeErr := pubKey.GetThorAddress()
				c.Assert(nodeErr, IsNil)
				points, nodeErr = helper.keeper.GetNodeAccountSlashPoints(helper.ctx, address)
				c.Assert(nodeErr, IsNil)
				c.Check(points, Equals, int64(0))
				jail, nodeErr := helper.keeper.GetNodeAccountJail(helper.ctx, address)
				c.Assert(nodeErr, IsNil)
				c.Check(jail.ReleaseHeight, Equals, int64(0))
			}
		}
	}
}

func (h HandlerTssKeysignSuite) TestTerminalVotersPerformNoWrites(c *C) {
	tests := []struct {
		name  string
		state types.TssKeysignFailVoterState
	}{
		{name: "legacy terminal", state: types.TssKeysignFailVoterState_LegacyVoter},
		{name: "current completed", state: types.TssKeysignFailVoterState_CompletedVoter},
	}

	for _, test := range tests {
		c.Logf("case: %s", test.name)
		helper := newTssKeysignHandlerTestHelper(c)
		msg, err := NewMsgTssKeysignFail(
			helper.ctx.BlockHeight(),
			helper.blame,
			"completed-write",
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
			helper.nodeAccount.NodeAddress,
			GetRandomPubKey(),
		)
		c.Assert(err, IsNil)
		helper.keeper.nextTssKeysignFailVoter = &TssKeysignFailVoter{
			ID:    msg.ID,
			State: test.state,
		}

		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
		_, err = handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)
		c.Check(helper.keeper.setTssKeysignFailVoterCalls, Equals, 0)
	}
}

// TestLegacyTerminalRoundVoterReplayHasNoSideEffects documents the
// accepted upgrade-boundary residual: an exact voter ID already persisted in
// legacy form is terminal, so later reports cannot complete its side effects.
func (h HandlerTssKeysignSuite) TestLegacyTerminalRoundVoterReplayHasNoSideEffects(c *C) {
	helper := newTssKeysignHandlerTestHelper(c)
	signerPubKey, err := common.NewPubKey(helper.retiringVault.Membership[0])
	c.Assert(err, IsNil)
	signer, err := signerPubKey.GetThorAddress()
	c.Assert(err, IsNil)

	blame := helper.blame
	blame.Round = tssMessages.KEYSIGN9
	msg, err := NewMsgTssKeysignFail(
		helper.ctx.BlockHeight(),
		blame,
		"legacy-terminal-round",
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
		signer,
		helper.retiringVault.PubKey,
	)
	c.Assert(err, IsNil)

	legacySigners := make([]string, 0, 3)
	for _, member := range helper.retiringVault.Membership[1:4] {
		legacySignerPubKey, signerErr := common.NewPubKey(member)
		c.Assert(signerErr, IsNil)
		legacySigner, signerErr := legacySignerPubKey.GetThorAddress()
		c.Assert(signerErr, IsNil)
		legacySigners = append(legacySigners, legacySigner.String())
	}
	helper.keeper.nextTssKeysignFailVoter = &TssKeysignFailVoter{
		ID:             msg.ID,
		Signers:        legacySigners,
		LastRoundCount: 1,
		State:          types.TssKeysignFailVoterState_LegacyVoter,
	}

	beforeEvents := len(helper.ctx.EventManager().Events())
	beforeVault, err := helper.keeper.GetVault(helper.ctx, helper.retiringVault.PubKey)
	c.Assert(err, IsNil)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	handler := NewTssKeysignHandler(mgr)
	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)

	c.Assert(helper.keeper.setTssKeysignFailVoterCalls, Equals, 0)

	slasher, ok := mgr.Slasher().(*DummySlasher)
	c.Assert(ok, Equals, true)
	c.Assert(slasher.pts, HasLen, 0)
	c.Assert(len(helper.ctx.EventManager().Events()), Equals, beforeEvents)
	afterVault, err := helper.keeper.GetVault(helper.ctx, helper.retiringVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(afterVault.Frozen, DeepEquals, beforeVault.Frozen)
	for _, node := range blame.BlameNodes {
		pubKey, nodeErr := common.NewPubKey(node.Pubkey)
		c.Assert(nodeErr, IsNil)
		address, nodeErr := pubKey.GetThorAddress()
		c.Assert(nodeErr, IsNil)
		points, nodeErr := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, address)
		c.Assert(nodeErr, IsNil)
		c.Assert(points, Equals, int64(0))
		jail, nodeErr := helper.keeper.GetNodeAccountJail(helper.ctx, address)
		c.Assert(nodeErr, IsNil)
		c.Assert(jail.ReleaseHeight, Equals, int64(0))
	}
}

// TestCompletedKeysignFailureCannotBeReplayed proves a completed common-chain
// failure remains terminal after durable reload, jail expiry, and a deliberate
// vault unfreeze. Replays must perform neither accounting effects nor writes.
func (h HandlerTssKeysignSuite) TestCompletedKeysignFailureCannotBeReplayed(c *C) {
	helper := newTssKeysignHandlerTestHelper(c)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	handler := NewTssKeysignHandler(mgr)
	coins := common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}

	vault := NewVault(
		helper.ctx.BlockHeight(),
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.ETHChain}.Strings(),
		[]ChainContract{},
	)
	members := make(NodeAccounts, 0, 8)
	for i := 0; i < 8; i++ {
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(helper.keeper.SetNodeAccount(helper.ctx, na), IsNil)
		vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
		members = append(members, na)
	}
	c.Assert(helper.keeper.SetVault(helper.ctx, vault), IsNil)

	blamed := members[7]
	blame := Blame{
		FailReason: "terminal keysign failure",
		Round:      tssMessages.KEYSIGN9,
		BlameNodes: []Node{{Pubkey: blamed.PubKeySet.Secp256k1.String()}},
	}
	const memo = "completed-keysign-replay"

	failureEventCount := func(ctx cosmos.Context) int {
		count := 0
		for _, event := range ctx.EventManager().Events() {
			if event.Type == types.TSSKeysignFailure {
				count++
			}
		}
		return count
	}

	// Four of eight vault members meet the current simple-majority threshold.
	var voterID string
	for i := 0; i < 4; i++ {
		msg, err := NewMsgTssKeysignFail(
			helper.ctx.BlockHeight(),
			blame,
			memo,
			coins,
			members[i].NodeAddress,
			vault.PubKey,
		)
		c.Assert(err, IsNil)
		voterID = msg.ID
		if i == 3 {
			helper.keeper.setTssKeysignFailVoterCalls = 0
		}
		_, err = handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)
		if i == 3 {
			c.Assert(helper.keeper.setTssKeysignFailVoterCalls, Equals, 1)
		}
		if i == 2 {
			pending, err := helper.keeper.GetTssKeysignFailVoter(helper.ctx, voterID)
			c.Assert(err, IsNil)
			c.Assert(pending.State, Equals, types.TssKeysignFailVoterState_PendingVoter)
			c.Assert(pending.Signers, HasLen, 3)
			c.Assert(failureEventCount(helper.ctx), Equals, 0)
		}
	}

	voter, err := helper.keeper.GetTssKeysignFailVoter(helper.ctx, voterID)
	c.Assert(err, IsNil)
	c.Assert(voter.State, Equals, types.TssKeysignFailVoterState_CompletedVoter)
	c.Assert(voter.Signers, HasLen, 0)
	c.Assert(voter.LastRoundCount, Equals, int64(0))

	blamedSlashPoints, err := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, blamed.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(blamedSlashPoints, Equals, helper.constAccessor.GetInt64Value(constants.FailKeysignSlashPoints))
	originalJail, err := helper.keeper.GetNodeAccountJail(helper.ctx, blamed.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(originalJail.IsJailed(helper.ctx), Equals, true)

	gotVault, err := helper.keeper.GetVault(helper.ctx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(gotVault.Frozen, DeepEquals, []string{common.ETHChain.String()})
	originalFailureEvents := failureEventCount(helper.ctx)
	c.Assert(originalFailureEvents, Equals, 1)

	for i := 0; i < 4; i++ {
		points, pointsErr := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, members[i].NodeAddress)
		c.Assert(pointsErr, IsNil)
		c.Assert(points, Equals, int64(0))
	}

	// Model a later replay after the original penalty has expired and the vault
	// has been deliberately unfrozen.
	replayCtx := helper.ctx.WithBlockHeight(originalJail.ReleaseHeight + 1)
	gotVault.Frozen = nil
	c.Assert(helper.keeper.SetVault(replayCtx, gotVault), IsNil)
	helper.keeper.setTssKeysignFailVoterCalls = 0
	replayMgr := NewDummyMgrWithKeeper(helper.keeper)
	replayHandler := NewTssKeysignHandler(replayMgr)
	replaySlasher, ok := replayMgr.Slasher().(*DummySlasher)
	c.Assert(ok, Equals, true)

	for i := 0; i < 4; i++ {
		replay, replayErr := NewMsgTssKeysignFail(
			helper.ctx.BlockHeight(),
			blame,
			memo,
			coins,
			members[i].NodeAddress,
			vault.PubKey,
		)
		c.Assert(replayErr, IsNil)
		c.Assert(replay.ID, Equals, voterID)
		_, replayErr = replayHandler.Run(replayCtx, replay)
		c.Assert(replayErr, IsNil)

		c.Assert(helper.keeper.setTssKeysignFailVoterCalls, Equals, 0)
		c.Assert(replaySlasher.pts, HasLen, 0)
		c.Assert(failureEventCount(replayCtx), Equals, originalFailureEvents)
	}

	afterSlashPoints, err := helper.keeper.GetNodeAccountSlashPoints(replayCtx, blamed.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(afterSlashPoints, Equals, blamedSlashPoints)
	afterJail, err := helper.keeper.GetNodeAccountJail(replayCtx, blamed.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(afterJail.ReleaseHeight, Equals, originalJail.ReleaseHeight)
	c.Assert(afterJail.IsJailed(replayCtx), Equals, false)
	gotVault, err = helper.keeper.GetVault(replayCtx, vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(gotVault.Frozen, HasLen, 0)
}

func (h HandlerTssKeysignSuite) TestDuplicateKeysignFailureVoteDoesNotSlashAgain(c *C) {
	helper := newTssKeysignHandlerTestHelper(c)
	handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
	msg, err := NewMsgTssKeysignFail(
		helper.ctx.BlockHeight(),
		helper.blame,
		"duplicate-vote",
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
		helper.nodeAccount.NodeAddress,
		helper.retiringVault.PubKey,
	)
	c.Assert(err, IsNil)
	voter, err := helper.keeper.GetTssKeysignFailVoter(helper.ctx, msg.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Sign(msg.Signer), Equals, true)
	c.Assert(helper.keeper.SetTssKeysignFailVoter(helper.ctx, voter), IsNil)

	before, err := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, msg.Signer)
	c.Assert(err, IsNil)
	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)
	after, err := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, msg.Signer)
	c.Assert(err, IsNil)
	c.Assert(after, Equals, before)
}

func (h HandlerTssKeysignSuite) TestKeysignFailureSlashTelemetryKeepsHandlerCaller(c *C) {
	if os.Getenv(keysignSlashTelemetryChild) == "1" {
		helper := newTssKeysignHandlerTestHelper(c)
		productionKeeper := helper.keeper.Keeper

		// Establish that the slash telemetry harness can attribute the caller when
		// slash points pass through SlasherImpl.
		slasher := newSlasher(productionKeeper, NewDummyEventMgr())
		slasher.IncSlashPoints(helper.ctx, 1, helper.nodeAccount.NodeAddress)

		// The keysign-failure handler currently bypasses SlasherImpl and invokes the
		// keeper directly. That must retain equally useful caller attribution.
		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(productionKeeper))
		msg, err := NewMsgTssKeysignFail(
			helper.ctx.BlockHeight(),
			helper.blame,
			"slash-telemetry-caller",
			common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
			helper.nodeAccount.NodeAddress,
			helper.retiringVault.PubKey,
		)
		c.Assert(err, IsNil)
		_, err = handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)
		return
	}

	tempHome := c.MkDir()
	c.Assert(os.MkdirAll(filepath.Join(tempHome, ".thornode"), 0o700), IsNil)

	testExecutable, err := os.Executable()
	c.Assert(err, IsNil)
	command := exec.Command(
		testExecutable,
		"-test.run=^TestPackage$",
		"-check.f=^HandlerTssKeysignSuite.TestKeysignFailureSlashTelemetryKeepsHandlerCaller$",
	)
	command.Env = make([]string, 0, len(os.Environ())+3)
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		switch name {
		case "HOME", "THOR_TELEMETRY_SLASH_POINTS", keysignSlashTelemetryChild:
			continue
		}
		command.Env = append(command.Env, item)
	}
	command.Env = append(
		command.Env,
		"HOME="+tempHome,
		"THOR_TELEMETRY_SLASH_POINTS=true",
		keysignSlashTelemetryChild+"=1",
	)
	output, err := command.CombinedOutput()
	c.Assert(err, IsNil, Commentf("telemetry child failed:\n%s", output))

	raw, err := os.ReadFile(filepath.Join(tempHome, ".thornode", "slash_telemetry.json"))
	c.Assert(err, IsNil)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	c.Assert(lines, HasLen, 2)

	type slashTelemetryRecord struct {
		Caller string `json:"caller"`
	}
	var slasherRecord, handlerRecord slashTelemetryRecord
	c.Assert(json.Unmarshal([]byte(lines[0]), &slasherRecord), IsNil)
	c.Assert(json.Unmarshal([]byte(lines[1]), &handlerRecord), IsNil)
	c.Assert(slasherRecord.Caller, Not(Equals), "")
	c.Assert(strings.Contains(handlerRecord.Caller, "handler_tss_keysign.go:"), Equals, true,
		Commentf("direct keeper slash telemetry attributed %q while SlasherImpl recorded %q", handlerRecord.Caller, slasherRecord.Caller))
}

func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_accept_standby_node_messages(c *C) {
	helper := newTssKeysignHandlerTestHelper(c)
	handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
	vault := NewVault(1024, RetiringVault, AsgardVault, GetRandomPubKey(), []string{
		common.ETHChain.String(),
	}, []ChainContract{})
	accounts := NodeAccounts{}
	for i := 0; i < 8; i++ {
		na := GetRandomValidatorNode(NodeActive)
		_ = helper.keeper.SetNodeAccount(helper.ctx, na)
		vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
		accounts = append(accounts, na)
	}
	naStandby := GetRandomValidatorNode(NodeStandby)
	_ = helper.keeper.SetNodeAccount(helper.ctx, naStandby)
	vault.Membership = append(vault.Membership, naStandby.PubKeySet.Secp256k1.String())
	c.Assert(helper.keeper.SetVault(helper.ctx, vault), IsNil)
	for idx, item := range accounts {
		if idx >= 4 {
			break
		}
		msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, item.NodeAddress, vault.PubKey)
		c.Assert(err, IsNil)
		result, err := handler.Run(helper.ctx, msg)
		c.Assert(result, NotNil)
		c.Assert(err, IsNil)
	}
	msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, naStandby.NodeAddress, vault.PubKey)
	c.Assert(err, IsNil)
	result, err := handler.Run(helper.ctx, msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)

	msg1, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}, naStandby.NodeAddress, vault.PubKey)
	msg1.Blame.BlameNodes = []Node{}
	c.Assert(err, IsNil)
	result, err = handler.Run(helper.ctx, msg1)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, se.ErrUnknownRequest), Equals, true)
}

// TestTssKeysignFailHandler_TerminalRoundIncrementsLastRoundCount asserts the
// consensus-affecting LastRoundCount++ path: a MsgTssKeysignFail carrying one
// of the accepted terminal-round names (v3 secp256k1 KEYSIGN9, EdDSA
// EDDSAKEYSIGN3, FROST frost_share/frost_result) must increment
// voter.LastRoundCount for
// the signer's vault membership. Non-terminal rounds — including KEYSIGN7,
// which was terminal under tss-lib v1 but is an interior consistency-check
// round under v3 — must
// NOT increment. Regressing this would either freeze vaults too eagerly (on
// mid-protocol round-7 timeouts) or fail to freeze vaults that should be frozen
// on terminal-round consensus.
func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_TerminalRoundIncrementsLastRoundCount(c *C) {
	cases := []struct {
		name            string
		round           string
		emptyBlameNodes bool
		wantIncrement   bool
	}{
		{"v3 secp terminal round 9 increments", tssMessages.KEYSIGN9, false, true},
		{"eddsa terminal round 3 increments", tssMessages.EDDSAKEYSIGN3, false, true},
		{"frost share exposure boundary increments", tssMessages.FROSTKEYSIGNSHARE, false, true},
		// frost_result sits PAST the exposure boundary, not before it: the only
		// blame stamped with this round comes from WaitForResult, which runs
		// after exchangeShares has already broadcast the local signature share.
		// A quorum of peers can therefore aggregate and broadcast the original
		// transaction, so the signer must stick to the same vault. Treating it
		// as non-terminal would let the outbound reschedule to a different
		// vault while the share is already out — a double spend.
		{"frost terminal result increments", tssMessages.FROSTKEYSIGNRESULT, false, true},
		{"frost preprocess does not increment", tssMessages.FROSTKEYSIGNPREPROCESS, false, false},
		// KEYSIGN7 was terminal under tss-lib v1 but is an interior round under
		// v3 (the U_i/T_i consistency-check commitment, before shares reveal at
		// round 9), so it must NOT increment — otherwise a routine round-7 peer
		// timeout would freeze the vault.
		{"v3 secp interior round 7 does not increment", tssMessages.KEYSIGN7, false, false},
		{"v3 secp non-terminal round 8 does not increment", tssMessages.KEYSIGN8, false, false},
		{"v3 secp non-terminal round 5 does not increment", tssMessages.KEYSIGN5, false, false},
		{"eddsa non-terminal round 1 does not increment", tssMessages.EDDSAKEYSIGN1, false, false},
		// Empty-BlameNodes guard: terminal-round reports that blame nobody
		// must not bump LastRoundCount, otherwise a colluding minority can
		// freeze a vault by spamming terminal-round reports with no blames.
		{"v3 secp terminal round 9 with empty BlameNodes does not increment", tssMessages.KEYSIGN9, true, false},
		{"eddsa terminal round 3 with empty BlameNodes does not increment", tssMessages.EDDSAKEYSIGN3, true, false},
		{"frost share with empty BlameNodes does not increment", tssMessages.FROSTKEYSIGNSHARE, true, false},
		{"frost terminal result with empty BlameNodes does not increment", tssMessages.FROSTKEYSIGNRESULT, true, false},
	}

	for _, tc := range cases {
		c.Logf(">Name: %s", tc.name)
		helper := newTssKeysignHandlerTestHelper(c)
		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))

		// The LastRoundCount increment fires only when the signer is in the
		// vault's membership (the freeze path counts terminal-round failures
		// from the actual signing committee). The default helper doesn't add
		// helper.nodeAccount to retiringVault.Membership, so push it in.
		helper.retiringVault.Membership = append(helper.retiringVault.Membership, helper.nodeAccount.PubKeySet.Secp256k1.String())
		c.Assert(helper.keeper.SetVault(helper.ctx, helper.retiringVault), IsNil)

		helper.blame.Round = tc.round
		if tc.emptyBlameNodes {
			helper.blame.BlameNodes = []Node{}
		}
		coins := common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}
		msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), helper.blame, "hello", coins, helper.nodeAccount.NodeAddress, helper.retiringVault.PubKey)
		c.Assert(err, IsNil)

		_, err = handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)

		voter, vErr := helper.keeper.GetTssKeysignFailVoter(helper.ctx, msg.ID)
		c.Assert(vErr, IsNil)
		if tc.wantIncrement {
			c.Assert(voter.LastRoundCount, Equals, int64(1), Commentf("%s: expected LastRoundCount=1 after terminal-round report", tc.name))
		} else {
			c.Assert(voter.LastRoundCount, Equals, int64(0), Commentf("%s: expected LastRoundCount=0 for non-terminal round", tc.name))
		}
	}
}

// TestTssKeysignFailHandler_rejects_legacy_round_ids ensures Thornode only
// accepts the canonical voter ID once round normalization is enforced.
func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_rejects_legacy_round_ids(c *C) {
	helper := newTssKeysignHandlerTestHelper(c)
	handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
	coins := common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}

	helper.blame.Round = tssMessages.KEYSIGN7
	legacyID, err := legacyKeysignFailID(helper.blame.BlameNodes, helper.ctx.BlockHeight(), "hello", coins, helper.retiringVault.PubKey, "thorchain.tsslib.ecdsa.signing.SignRound7Message")
	c.Assert(err, IsNil)

	msg := &MsgTssKeysignFail{
		ID:     legacyID,
		Height: helper.ctx.BlockHeight(),
		Blame:  helper.blame,
		Memo:   "hello",
		Coins:  coins,
		Signer: helper.nodeAccount.NodeAddress,
		PubKey: helper.retiringVault.PubKey,
	}

	result, err := handler.Run(helper.ctx, msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, se.ErrUnknownRequest), Equals, true)
}

// runTerminalRoundFreezeScenario submits an MsgTssKeysignFail with the given
// terminal round from a simple majority of vault members. The blameBuilder
// callback receives the registered member accounts and returns the BlameNodes
// list to use, so positive-path callers can reference real registered pubkeys
// (the handler resolves blame entries through GetNodeAccountByPubKey).
func runTerminalRoundFreezeScenario(c *C, round string, coin common.Coin, blameBuilder func(members NodeAccounts) []Node) Vault {
	helper := newTssKeysignHandlerTestHelper(c)
	handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
	coins := common.Coins{coin}

	vault := NewVault(helper.ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{coin.Asset.GetChain()}.Strings(), []ChainContract{})
	accounts := NodeAccounts{}
	for i := 0; i < 8; i++ {
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(helper.keeper.SetNodeAccount(helper.ctx, na), IsNil)
		vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
		accounts = append(accounts, na)
	}
	c.Assert(helper.keeper.SetVault(helper.ctx, vault), IsNil)

	blame := Blame{
		FailReason: "tss timeout",
		Round:      round,
		BlameNodes: blameBuilder(accounts),
	}
	// Submit from 5 of 8 vault members to clear simple-majority consensus.
	for i := 0; i < 5; i++ {
		msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), blame, "hello", coins, accounts[i].NodeAddress, vault.PubKey)
		c.Assert(err, IsNil)
		_, err = handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)
	}

	got, err := helper.keeper.GetVault(helper.ctx, vault.PubKey)
	c.Assert(err, IsNil)
	return got
}

// TestTssKeysignFailHandler_terminal_round_empty_blamenodes_does_not_freeze is
// the regression for the BlameNodes-guard fix: a terminal-round MsgTssKeysignFail
// with len(BlameNodes) == 0 must NOT increment LastRoundCount and therefore
// must NOT freeze the vault. Without the guard, a colluding minority could
// lock a vault by reporting terminal-round failures that blame nobody, since
// FailReason alone satisfies Blame.IsEmpty().
func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_terminal_round_empty_blamenodes_does_not_freeze(c *C) {
	cases := []struct {
		round string
		coin  common.Coin
	}{
		{tssMessages.KEYSIGN9, common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
		{tssMessages.EDDSAKEYSIGN3, common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
		{tssMessages.FROSTKEYSIGNSHARE, common.NewCoin(common.XMRAsset, cosmos.NewUint(100))},
		{tssMessages.FROSTKEYSIGNRESULT, common.NewCoin(common.XMRAsset, cosmos.NewUint(100))},
	}
	for _, tc := range cases {
		got := runTerminalRoundFreezeScenario(c, tc.round, tc.coin, func(_ NodeAccounts) []Node { return []Node{} })
		c.Assert(got.Frozen, HasLen, 0, Commentf("round=%s: empty BlameNodes must not freeze", tc.round))
	}
}

// TestTssKeysignFailHandler_terminal_round_with_blamenodes_freezes covers the
// positive path: a terminal-round MsgTssKeysignFail with a non-empty BlameNodes
// list reported by a majority of vault members must freeze the vault on the
// blamed chain. Together with the empty-BlameNodes regression above, this
// pins down the exact condition the guard enforces.
func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_terminal_round_with_blamenodes_freezes(c *C) {
	cases := []struct {
		round string
		coin  common.Coin
	}{
		{tssMessages.KEYSIGN9, common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
		{tssMessages.EDDSAKEYSIGN3, common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
		{tssMessages.FROSTKEYSIGNSHARE, common.NewCoin(common.XMRAsset, cosmos.NewUint(100))},
		{tssMessages.FROSTKEYSIGNRESULT, common.NewCoin(common.XMRAsset, cosmos.NewUint(100))},
	}
	for _, tc := range cases {
		got := runTerminalRoundFreezeScenario(c, tc.round, tc.coin, func(members NodeAccounts) []Node {
			return []Node{{Pubkey: members[7].PubKeySet.Secp256k1.String()}}
		})
		c.Assert(got.Frozen, DeepEquals, []string{tc.coin.Asset.GetChain().String()}, Commentf("round=%s: non-empty BlameNodes must freeze", tc.round))
	}
}

// TestTssKeysignFailHandler_frost_preprocess_does_not_freeze pins the other
// side of the FROST safety boundary: preprocess failures occur before any
// signature share is broadcast, so they may be rescheduled to another vault.
func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_frost_preprocess_does_not_freeze(c *C) {
	got := runTerminalRoundFreezeScenario(
		c,
		tssMessages.FROSTKEYSIGNPREPROCESS,
		common.NewCoin(common.XMRAsset, cosmos.NewUint(100)),
		func(members NodeAccounts) []Node {
			return []Node{{Pubkey: members[7].PubKeySet.Secp256k1.String()}}
		},
	)
	c.Assert(got.Frozen, HasLen, 0)
}

// TestTssKeysignFailHandler_emits_failure_event_on_consensus asserts the
// tss_keysign_failure event fires exactly once — when the vault's signing
// committee reaches simple-majority consensus, and not before — and carries
// full round and blame granularity (canonical round name, blamed pubkeys,
// failure id, vault pubkey, txout height) for off-chain failure detection.
func (h HandlerTssKeysignSuite) TestTssKeysignFailHandler_emits_failure_event_on_consensus(c *C) {
	cases := []struct {
		name       string
		round      string
		emptyBlame bool
	}{
		{"interior ecdsa round with blame", tssMessages.KEYSIGN1aUnicast, false},
		{"terminal ecdsa round with blame", tssMessages.KEYSIGN9, false},
		{"eddsa round with blame", tssMessages.EDDSAKEYSIGN2, false},
		{"frost round with empty blame", tssMessages.FROSTKEYSIGNPREPROCESS, true},
	}
	for _, tc := range cases {
		c.Logf(">Name: %s", tc.name)
		helper := newTssKeysignHandlerTestHelper(c)
		handler := NewTssKeysignHandler(NewDummyMgrWithKeeper(helper.keeper))
		coins := common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))}

		vault := NewVault(helper.ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
		accounts := NodeAccounts{}
		for i := 0; i < 8; i++ {
			na := GetRandomValidatorNode(NodeActive)
			c.Assert(helper.keeper.SetNodeAccount(helper.ctx, na), IsNil)
			vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
			accounts = append(accounts, na)
		}
		c.Assert(helper.keeper.SetVault(helper.ctx, vault), IsNil)

		blame := Blame{
			FailReason: "tss timeout",
			Round:      tc.round,
			BlameNodes: []Node{{Pubkey: accounts[7].PubKeySet.Secp256k1.String()}},
		}
		if tc.emptyBlame {
			blame.BlameNodes = []Node{}
		}

		failureEvents := func() []cosmos.Event {
			found := []cosmos.Event{}
			for _, e := range helper.ctx.EventManager().Events() {
				if e.Type == types.TSSKeysignFailure {
					found = append(found, e)
				}
			}
			return found
		}

		// submit from 3 of 8 members — one short of the simple majority of 4
		var msgID string
		for i := 0; i < 3; i++ {
			msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), blame, "hello", coins, accounts[i].NodeAddress, vault.PubKey)
			c.Assert(err, IsNil)
			msgID = msg.ID
			_, err = handler.Run(helper.ctx, msg)
			c.Assert(err, IsNil)
		}
		c.Assert(failureEvents(), HasLen, 0, Commentf("%s: no event before consensus", tc.name))

		// the 4th member report reaches consensus and must emit the event
		msg, err := NewMsgTssKeysignFail(helper.ctx.BlockHeight(), blame, "hello", coins, accounts[3].NodeAddress, vault.PubKey)
		c.Assert(err, IsNil)
		c.Assert(msg.ID, Equals, msgID)
		_, err = handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)

		evts := failureEvents()
		c.Assert(evts, HasLen, 1, Commentf("%s: exactly one event on consensus", tc.name))
		attrs := make(map[string]string)
		for _, attr := range evts[0].Attributes {
			attrs[attr.Key] = attr.Value
		}
		c.Assert(attrs["reason"], Equals, blame.FailReason)
		c.Assert(attrs["round"], Equals, tc.round)
		c.Assert(attrs["is_unicast"], Equals, "false")
		c.Assert(attrs["height"], Equals, strconv.FormatInt(helper.ctx.BlockHeight(), 10))
		c.Assert(attrs["id"], Equals, msg.ID)
		c.Assert(attrs["pubkey"], Equals, vault.PubKey.String())
		if tc.emptyBlame {
			c.Assert(attrs["blame"], Equals, "")
		} else {
			c.Assert(attrs["blame"], Equals, accounts[7].PubKeySet.Secp256k1.String())
		}
	}
}
