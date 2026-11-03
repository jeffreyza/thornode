package thorchain

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/blang/semver"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerTssSuite struct{}

var _ = Suite(&HandlerTssSuite{})

type tssHandlerTestHelper struct {
	ctx           cosmos.Context
	version       semver.Version
	keeper        *tssKeeperHelper
	poolPk        common.PubKey
	poolPkEddsa   common.PubKey
	constAccessor constants.ConstantValues
	nodeAccount   NodeAccount
	mgr           Manager
	members       common.PubKeys
	signer        cosmos.AccAddress
}

type tssKeeperHelper struct {
	keeper.Keeper
	errListActiveAccounts bool
	errGetTssVoter        bool
	errFailSaveVault      bool
	errFailGetNodeAccount bool
	errFailGetNetwork     bool
	errFailSetNetwork     bool
	errFailSetNodeAccount bool
}

func (k *tssKeeperHelper) GetNodeAccountByPubKey(ctx cosmos.Context, pk common.PubKey) (NodeAccount, error) {
	if k.errFailGetNodeAccount {
		return NodeAccount{}, errKaboom
	}
	return k.Keeper.GetNodeAccountByPubKey(ctx, pk)
}

func (k *tssKeeperHelper) SetVault(ctx cosmos.Context, vault Vault) error {
	if k.errFailSaveVault {
		return errKaboom
	}
	return k.Keeper.SetVault(ctx, vault)
}

func (k *tssKeeperHelper) GetTssVoter(ctx cosmos.Context, id string) (TssVoter, error) {
	if k.errGetTssVoter {
		return TssVoter{}, errKaboom
	}
	return k.Keeper.GetTssVoter(ctx, id)
}

func (k *tssKeeperHelper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.errListActiveAccounts {
		return NodeAccounts{}, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func (k *tssKeeperHelper) GetNetwork(ctx cosmos.Context) (Network, error) {
	if k.errFailGetNetwork {
		return Network{}, errKaboom
	}
	return k.Keeper.GetNetwork(ctx)
}

func (k *tssKeeperHelper) SetNetwork(ctx cosmos.Context, data Network) error {
	if k.errFailSetNetwork {
		return errKaboom
	}
	return k.Keeper.SetNetwork(ctx, data)
}

func (k *tssKeeperHelper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if k.errFailSetNodeAccount {
		return errKaboom
	}
	return k.Keeper.SetNodeAccount(ctx, na)
}

func newTssKeeperHelper(keeper keeper.Keeper) *tssKeeperHelper {
	return &tssKeeperHelper{
		Keeper: keeper,
	}
}

func newTssHandlerTestHelper(c *C) tssHandlerTestHelper {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1023)

	keeperHelper := newTssKeeperHelper(k)
	FundModule(c, ctx, k, BondName, 500*common.One)
	// active account
	nodeAccount := GetRandomValidatorNode(NodeActive)
	nodeAccount.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(keeperHelper.SetNodeAccount(ctx, nodeAccount), IsNil)

	mgr := NewDummyMgr()
	var members common.PubKeys
	for i := 0; i < 8; i++ {
		members = append(members, GetRandomPubKey())
	}
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].String() < members[j].String()
	})
	signer, err := members[0].GetThorAddress()
	c.Assert(err, IsNil)
	nodeReady := GetRandomValidatorNode(NodeReady)
	nodeReady.NodeAddress = signer
	nodeReady.Bond = cosmos.NewUint(1000000 * common.One)
	c.Assert(keeperHelper.SetNodeAccount(ctx, nodeReady), IsNil)
	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{
		{
			Type:    AsgardKeygen,
			Members: members.Strings(),
		},
	}
	keeperHelper.SetKeygenBlock(ctx, keygenBlock)
	keygenTime := int64(1024)
	poolPk := GetRandomPubKey()
	fakeSig := []byte("fakeSignature")
	poolPkEddsa := GetRandomPubKey()
	msg, err := NewMsgTssPoolV2(members.Strings(), poolPk, fakeSig, nil, AsgardKeygen, ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), signer, keygenTime, poolPkEddsa, nil, nil, common.EmptyPubKey)
	c.Assert(err, IsNil)
	voter := NewTssVoter(msg.ID, members.Strings(), poolPk, poolPkEddsa)
	keeperHelper.SetTssVoter(ctx, voter)

	asgardVault := NewVaultV2(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.RuneAsset().Chain}.Strings(), []ChainContract{}, poolPkEddsa)
	c.Assert(keeperHelper.SetVault(ctx, asgardVault), IsNil)

	return tssHandlerTestHelper{
		ctx:           ctx,
		version:       mgr.GetVersion(),
		keeper:        keeperHelper,
		poolPk:        poolPk,
		poolPkEddsa:   poolPkEddsa,
		constAccessor: constants.GetConstantValues(GetCurrentVersion()),
		nodeAccount:   nodeAccount,
		mgr:           mgr,
		members:       members,
		signer:        signer,
	}
}

func (s *HandlerTssSuite) TestTssHandler(c *C) {
	fakeSig := []byte("fakeSignature")
	keygenTime := int64(1024)
	testCases := []struct {
		name           string
		messageCreator func(helper tssHandlerTestHelper) cosmos.Msg
		runner         func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error)
		validator      func(helper tssHandlerTestHelper, msg cosmos.Msg, result *cosmos.Result, c *C)
		expectedResult error
	}{
		{
			name: "invalid message should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				return NewMsgNoOp(GetRandomObservedTx(), helper.signer, "")
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errInvalidMessage,
		},
		{
			name: "Not signed by an active account should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssPool(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), GetRandomValidatorNode(NodeActive).NodeAddress, keygenTime)
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "empty signer should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				msg, err := NewMsgTssPool(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), cosmos.AccAddress{}, keygenTime)
				c.Assert(err, IsNil)
				return msg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrInvalidAddress,
		},
		{
			name: "empty id should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPool(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime)
				c.Assert(err, IsNil)
				tssMsg.ID = ""
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "empty member pubkeys should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPool(nil, GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "less than two member pubkeys should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPool(common.PubKeys{GetRandomPubKey()}.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "there are empty pubkeys in member pubkey should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPool([]string{GetRandomPubKey().String(), GetRandomPubKey().String(), ""}, GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "success while pool pub key is empty should return error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPool(helper.members.Strings(), common.EmptyPubKey, fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "invalid pool pub key should return error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPool(helper.members.Strings(), "whatever", fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "fail to get tss voter should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errGetTssVoter = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "fail to save vault should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), helper.poolPk, fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, helper.poolPkEddsa, nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
					voter.Secp256K1Signatures = append(voter.Secp256K1Signatures, string(fakeSig))
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errFailSaveVault = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "not having consensus should not perform any actions",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				for i := 0; i < 8; i++ {
					na := GetRandomValidatorNode(NodeActive)
					_ = helper.keeper.SetNodeAccount(helper.ctx, na)
				}
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "if signer already sign the voter, it should just return",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, _ := helper.keeper.Keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				if voter.PoolPubKey.IsEmpty() {
					voter.PoolPubKey = tssMsg.PoolPubKey
					voter.PoolPubKeyEddsa = tssMsg.PoolPubKeyEddsa
					voter.PubKeys = tssMsg.PubKeys
				}
				voter.Sign(tssMsg.Signer, tssMsg.Chains, "")
				helper.keeper.Keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "normal success",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "When tss message integrity compromised, it should result an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				tssMsg.PoolPubKey = GetRandomPubKey()
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "blame node not in keygen participants should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				sort.SliceStable(helper.members, func(i, j int) bool {
					return helper.members[i].String() < helper.members[j].String()
				})
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{Pubkey: "whatever"},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnknownRequest,
		},
		{
			name: "fail to keygen retry should be slashed",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				thorAddr, _ := helper.members[3].GetThorAddress()
				na, _ := helper.keeper.GetNodeAccount(helper.ctx, thorAddr)
				na.UpdateStatus(NodeActive, helper.ctx.BlockHeight())
				_ = helper.keeper.SetNodeAccount(helper.ctx, na)
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{
							Pubkey: helper.members[3].String(),
						},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				constAccessor := constants.GetConstantValues(helper.version)
				observeSlashPoints := constAccessor.GetInt64Value(constants.ObserveSlashPoints)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.mgr.Slasher().IncSlashPoints(helper.ctx, observeSlashPoints, voter.GetSigners()...)
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				m, _ := msg.(*MsgTssPool)
				voter, _ := helper.keeper.GetTssVoter(helper.ctx, m.ID)
				if voter.PoolPubKey.IsEmpty() {
					voter.PoolPubKey = m.PoolPubKey
					voter.PoolPubKeyEddsa = m.PoolPubKeyEddsa
					voter.PubKeys = m.PubKeys
				}
				addr, _ := helper.members[3].GetThorAddress()
				voter.Sign(addr, common.Chains{common.ETHChain}.Strings(), string(fakeSig))
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return handler.Run(helper.ctx, msg)
			},
			validator: func(helper tssHandlerTestHelper, msg cosmos.Msg, result *cosmos.Result, c *C) {
				// make sure node get slashed
				pubKey := helper.members[3]
				na, err := helper.keeper.GetNodeAccountByPubKey(helper.ctx, pubKey)
				c.Assert(err, IsNil)
				slashPts, err := helper.keeper.GetNodeAccountSlashPoints(helper.ctx, na.NodeAddress)
				c.Assert(err, IsNil)
				c.Assert(slashPts > 0, Equals, true)
			},
			expectedResult: nil,
		},
		{
			name: "fail to keygen but can't get network data should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{Pubkey: helper.members[3].String()},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errFailGetNetwork = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "fail to keygen retry and none active account should be slashed with bond",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{Pubkey: helper.members[3].String()},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				vd := Network{
					BondRewardRune: cosmos.NewUint(5000 * common.One),
					TotalBondUnits: cosmos.NewUint(10000),
				}
				_ = helper.keeper.SetNetwork(helper.ctx, vd)
				return handler.Run(helper.ctx, msg)
			},
			validator: func(helper tssHandlerTestHelper, msg cosmos.Msg, result *cosmos.Result, c *C) {
				// make sure node get slashed
				pubKey := helper.members[3]
				na, err := helper.keeper.GetNodeAccountByPubKey(helper.ctx, pubKey)
				c.Assert(err, IsNil)
				c.Assert(na.Bond.Equal(cosmos.ZeroUint()), Equals, true)
				jail, err := helper.keeper.GetNodeAccountJail(helper.ctx, na.NodeAddress)
				c.Assert(err, IsNil)
				c.Check(jail.ReleaseHeight > 0, Equals, true)
			},
			expectedResult: nil,
		},
		{
			name: "fail to keygen and none active account, fail to set network data should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{Pubkey: helper.members[3].String()},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				vd := Network{
					BondRewardRune: cosmos.NewUint(5000 * common.One),
					TotalBondUnits: cosmos.NewUint(10000),
				}
				_ = helper.keeper.SetNetwork(helper.ctx, vd)
				helper.keeper.errFailSetNetwork = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: nil,
		},
		{
			name: "fail to keygen and fail to get node account should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{Pubkey: helper.members[3].String()},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errFailGetNodeAccount = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "fail to keygen and fail to set node account should return an error",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				b := Blame{
					FailReason: "who knows",
					BlameNodes: []Node{
						{Pubkey: helper.members[3].String()},
					},
				}
				tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), []Blame{b}, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				voter, err := helper.keeper.GetTssVoter(helper.ctx, tssMsg.ID)
				c.Assert(err, IsNil)
				for _, pk := range helper.members {
					addr, err := pk.GetThorAddress()
					c.Assert(err, IsNil)
					if addr.Equals(helper.signer) {
						continue
					}
					voter.Signers = append(voter.Signers, addr.String())
				}
				helper.keeper.SetTssVoter(helper.ctx, voter)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				helper.keeper.errFailSetNodeAccount = true
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: errKaboom,
		},
		{
			name: "When members in message doesn't match members in keygen blocks should fail",
			messageCreator: func(helper tssHandlerTestHelper) cosmos.Msg {
				members := common.PubKeys{
					GetRandomPubKey(),
					GetRandomPubKey(),
					helper.members[0],
				}
				tssMsg, err := NewMsgTssPoolV2(members.Strings(), GetRandomPubKey(), fakeSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, GetRandomPubKey(), nil, nil, common.EmptyPubKey)
				c.Assert(err, IsNil)
				return tssMsg
			},
			runner: func(handler TssHandler, msg cosmos.Msg, helper tssHandlerTestHelper) (*cosmos.Result, error) {
				return handler.Run(helper.ctx, msg)
			},
			expectedResult: se.ErrUnauthorized,
		},
	}

	// bypass signature checks on TssPool messages
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error {
		return nil
	}

	for _, tc := range testCases {
		helper := newTssHandlerTestHelper(c)
		handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
		msg := tc.messageCreator(helper)
		result, err := tc.runner(handler, msg, helper)
		if tc.expectedResult == nil {
			c.Assert(err, IsNil)
		} else {
			c.Assert(errors.Is(err, tc.expectedResult), Equals, true, Commentf("name:%s, %s", tc.name, err))
		}
		if tc.validator != nil {
			tc.validator(helper, msg, result, c)
		}
	}
}

// A keygen result expires exactly at the retry boundary (age == ChurnRetryInterval),
// the same block the churn retry schedules a replacement keygen. Accepting it there
// would let a stale result finalize a vault the retry is replacing; it must be
// accepted only up to the block before the boundary.
func (s *HandlerTssSuite) TestTssHandlerRejectsKeygenAtRetryBoundary(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))

	const retryBlocks int64 = 30
	helper.keeper.SetMimir(helper.ctx, constants.ChurnRetryInterval.String(), retryBlocks)

	msg, err := NewMsgTssPoolV2(
		helper.members.Strings(),
		helper.poolPk,
		nil,
		nil,
		AsgardKeygen,
		helper.ctx.BlockHeight(),
		nil,
		common.Chains{common.RuneAsset().Chain}.Strings(),
		helper.signer,
		1024,
		helper.poolPkEddsa,
		nil,
		nil,
		common.EmptyPubKey,
	)
	c.Assert(err, IsNil)

	// One block before the boundary: still accepted.
	lastValidCtx := helper.ctx.WithBlockHeight(helper.ctx.BlockHeight() + retryBlocks - 1)
	c.Assert(handler.validate(lastValidCtx, msg), IsNil)

	// At the retry boundary: rejected (was accepted before the off-by-one fix).
	boundaryCtx := helper.ctx.WithBlockHeight(helper.ctx.BlockHeight() + retryBlocks)
	c.Assert(handler.validate(boundaryCtx, msg), ErrorMatches, "invalid keygen block: unknown request")

	expiredCtx := helper.ctx.WithBlockHeight(helper.ctx.BlockHeight() + retryBlocks + 1)
	c.Assert(handler.validate(expiredCtx, msg), ErrorMatches, "invalid keygen block: unknown request")
}

// TestTssHandlerRejectsFutureKeygenBlock ensures a keygen result cannot report a
// keygen block that has not been reached yet. Keygen blocks can be scheduled
// ahead of the current height (the genesis XMR monovault keygen is scheduled 128
// blocks out), so an unbounded height would let members vote on such a keygen
// early and finalize the vault before its scheduled block (issue #3046).
func (s *HandlerTssSuite) TestTssHandlerRejectsFutureKeygenBlock(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))

	futureHeight := helper.ctx.BlockHeight() + 128
	keygenBlock := NewKeygenBlock(futureHeight)
	keygenBlock.Keygens = []Keygen{{
		Type:    AsgardKeygen,
		Members: helper.members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	newMsgAtHeight := func(height int64) *MsgTssPool {
		msg, err := NewMsgTssPoolV2(
			helper.members.Strings(),
			helper.poolPk,
			nil,
			nil,
			AsgardKeygen,
			height,
			nil,
			common.Chains{common.RuneAsset().Chain}.Strings(),
			helper.signer,
			1024,
			helper.poolPkEddsa,
			nil,
			nil,
			common.EmptyPubKey,
		)
		c.Assert(err, IsNil)
		return msg
	}

	c.Assert(
		handler.validate(helper.ctx, newMsgAtHeight(futureHeight)),
		ErrorMatches,
		"invalid keygen block: unknown request",
	)
	c.Assert(
		handler.validate(helper.ctx, newMsgAtHeight(helper.ctx.BlockHeight()+1)),
		ErrorMatches,
		"invalid keygen block: unknown request",
	)

	// once the scheduled height is reached the same report is accepted
	c.Assert(handler.validate(helper.ctx.WithBlockHeight(futureHeight), newMsgAtHeight(futureHeight)), IsNil)
}

func (s *HandlerTssSuite) TestTssHandlerPreservesDisabledRetryValidity(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	helper.keeper.SetMimir(helper.ctx, constants.ChurnRetryInterval.String(), 0)

	c.Assert(keygenResultValidityBlocks(helper.ctx, helper.keeper, AsgardKeygen), Equals, int64(0))
	c.Assert(keygenResultValidityBlocks(helper.ctx, helper.keeper, XMRMonovaultKeygen), Equals, int64(0))

	msg, err := NewMsgTssPoolV2(
		helper.members.Strings(),
		helper.poolPk,
		nil,
		nil,
		AsgardKeygen,
		helper.ctx.BlockHeight(),
		nil,
		common.Chains{common.RuneAsset().Chain}.Strings(),
		helper.signer,
		1024,
		helper.poolPkEddsa,
		nil,
		nil,
		common.EmptyPubKey,
	)
	c.Assert(err, IsNil)
	c.Assert(handler.validate(helper.ctx, msg), IsNil)
	c.Assert(
		handler.validate(helper.ctx.WithBlockHeight(helper.ctx.BlockHeight()+1), msg),
		ErrorMatches,
		"invalid keygen block: unknown request",
	)
}

func (s *HandlerTssSuite) TestKeygenSuccessHandler(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	slasher := handler.mgr.Slasher()
	dummySlasher, ok := slasher.(*DummySlasher)
	c.Assert(ok, Equals, true)
	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	failKeyGenSlashPoints := helper.constAccessor.GetInt64Value(constants.FailKeygenSlashPoints)
	lackOfObservationPenalty := helper.constAccessor.GetInt64Value(constants.LackOfObservationPenalty)
	for idx, item := range helper.members {
		thorAddr, err := item.GetThorAddress()
		c.Assert(err, IsNil)
		tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, nil, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), thorAddr, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		err = handler.handle(helper.ctx, tssMsg)
		c.Assert(err, IsNil)
		if HasSuperMajority(idx+1, len(helper.members)) {
			// ensure the late vote members get slashed
			for _, m := range helper.members[idx+1:] {
				slashThorAddr, err := m.GetThorAddress()
				c.Assert(err, IsNil)
				points, pointsOk := dummySlasher.pts[slashThorAddr.String()]
				c.Assert(pointsOk, Equals, true)
				c.Assert(points == failKeyGenSlashPoints+lackOfObservationPenalty, Equals, true)
				j, err := helper.keeper.GetNodeAccountJail(helper.ctx, slashThorAddr)
				c.Assert(err, IsNil)
				c.Assert(j.ReleaseHeight > helper.ctx.BlockHeight(), Equals, true)
			}
		}
	}
	// no one should be slashed
	for _, item := range helper.members {
		thorAddr, err := item.GetThorAddress()
		c.Assert(err, IsNil)
		points, pointsOk := dummySlasher.pts[thorAddr.String()]
		c.Assert(pointsOk, Equals, true)
		c.Assert(points == 0, Equals, true)
		j, err := helper.keeper.GetNodeAccountJail(helper.ctx, thorAddr)
		c.Assert(err, IsNil)
		c.Assert(j.ReleaseHeight <= helper.ctx.BlockHeight(), Equals, true)
	}
}

// TestAsgardLateKeygenDoesNotReleaseLaterFailedKeygenJail pins the reason
// guard on the non-XMR late-signer path. A later failed-keygen sentence has the
// same duration but a later release height, so SetNodeAccountJail replaces the
// keygen-vote sentence. The late success report must not release that newer
// sentence.
func (s *HandlerTssSuite) TestAsgardLateKeygenDoesNotReleaseLaterFailedKeygenJail(c *C) {
	helper := newTssHandlerTestHelper(c)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	handler := NewTssHandler(mgr)
	ctx := helper.ctx

	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	keygenTime := int64(1024)
	keygenHeight := ctx.BlockHeight()
	superMajorityCount := 0
	for superMajorityCount < len(helper.members) && !HasSuperMajority(superMajorityCount, len(helper.members)) {
		superMajorityCount++
	}
	c.Assert(superMajorityCount < len(helper.members), Equals, true)

	newMsg := func(member common.PubKey) *MsgTssPool {
		addr, err := member.GetThorAddress()
		c.Assert(err, IsNil)
		msg, err := NewMsgTssPoolV2(
			helper.members.Strings(),
			poolPubKey,
			nil,
			nil,
			AsgardKeygen,
			keygenHeight,
			nil,
			common.Chains{common.RuneAsset().Chain}.Strings(),
			addr,
			keygenTime,
			poolEddsaPubKey,
			nil,
			nil,
			common.EmptyPubKey,
		)
		c.Assert(err, IsNil)
		return msg
	}

	for _, member := range helper.members[:superMajorityCount] {
		c.Assert(handler.handle(ctx, newMsg(member)), IsNil)
	}

	victim := helper.members[superMajorityCount]
	victimAddr, err := victim.GetThorAddress()
	c.Assert(err, IsNil)

	jail, err := helper.keeper.GetNodeAccountJail(ctx, victimAddr)
	c.Assert(err, IsNil)
	c.Assert(jail.Reason, Equals, jailReasonFailedToVoteKeygen)

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	jailTime := mgr.GetConstants().GetInt64Value(constants.JailTimeKeygen)
	releaseHeight := ctx.BlockHeight() + jailTime
	c.Assert(helper.keeper.SetNodeAccountJail(ctx, victimAddr, releaseHeight, jailReasonFailedToPerformKeygen), IsNil)

	c.Assert(handler.handle(ctx, newMsg(victim)), IsNil)

	jail, err = helper.keeper.GetNodeAccountJail(ctx, victimAddr)
	c.Assert(err, IsNil)
	c.Assert(jail.IsJailed(ctx), Equals, true)
	c.Assert(jail.ReleaseHeight, Equals, releaseHeight)
	c.Assert(jail.Reason, Equals, jailReasonFailedToPerformKeygen)
}

func (s *HandlerTssSuite) TestXMRMonovaultKeygenFinalizesOnSuperMajority(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{
		Type:    XMRMonovaultKeygen,
		Members: helper.members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	observedMoneroHeight := int64(777)
	keygenTime := int64(1024)
	msg, err := NewMsgTssPoolV3(
		helper.members.Strings(),
		poolPubKey,
		nil,
		nil,
		XMRMonovaultKeygen,
		helper.ctx.BlockHeight(),
		nil,
		xmrMonovaultChains().Strings(),
		helper.signer,
		keygenTime,
		common.EmptyPubKey,
		nil,
		validTssKeyshareBackup(),
		frostPubKey,
		observedMoneroHeight,
	)
	c.Assert(err, IsNil)
	msg.ObservedMoneroHeight = observedMoneroHeight

	voter := NewTssVoter(msg.ID, helper.members.Strings(), poolPubKey, common.EmptyPubKey)
	voter.PoolPubKeyFrost = frostPubKey
	voter.ObservedMoneroHeight = observedMoneroHeight
	for _, member := range helper.members[1:6] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		voter.Signers = append(voter.Signers, addr.String())
	}
	helper.keeper.SetTssVoter(helper.ctx, voter)
	c.Assert(helper.keeper.SetLastChainHeight(helper.ctx, common.XMRChain, observedMoneroHeight+500), IsNil)

	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)

	updatedVoter, err := helper.keeper.GetTssVoter(helper.ctx, msg.ID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.HasConsensus(), Equals, true)
	c.Assert(updatedVoter.HasCompleteConsensus(), Equals, false)
	c.Assert(updatedVoter.PoolPubKeyFrost.Equals(frostPubKey), Equals, true)
	c.Assert(updatedVoter.ObservedMoneroHeight, Equals, observedMoneroHeight)

	vault, err := helper.keeper.GetVault(helper.ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.Status, Equals, InitVault)
	c.Assert(vault.IsXMRMonovault(), Equals, true)
	c.Assert(vault.PubKeyEddsa.IsEmpty(), Equals, true)
	c.Assert(vault.PubKeyFrost.Equals(frostPubKey), Equals, true)
	c.Assert(vault.VaultCreationHeightXmr, Equals, observedMoneroHeight)
}

// TestXMRMonovaultKeygenFailureSubmitterIsSlashedButNotJailedAfterSuccessConsensus
// pins the on-chain half of the XMR keygen mass-jailing fix for a failure
// submitter whose vote has a different ID from the eventual success vote:
//
// Defect 1 (different-ID trap): a member whose local FROST DKG timed out
// submits a keygen *failure* with blame, which carries a different msg ID
// than the success (getTssID hashes the blame). When the finishers reach 2/3
// success consensus, the failure submitter has still not signed the success
// ID and may never be able to.
//
// Defect 2 (broken self-heal): judgeLateSigner receives the voter by value
// and persists MajorityConsensusBlockHeight itself, but the vault-finalize
// path that runs right after it calls SetTssVoter with the handler's stale
// copy, zeroing the field again. XMR monovault keygen always finalizes on
// the consensus-reaching message, so the first late same-ID signer hits the
// "first consensus" branch instead of the release branch: it is NOT released,
// and every remaining absentee is RE-jailed with a fresh release height.
//
// The fix keeps the penalty as slash points only for XMR monovault keygen: a
// same-ID late success reporter is refunded once it reports, while the
// different-ID failure submitter remains slash-pointed but must not be jailed.
func (s *HandlerTssSuite) TestXMRMonovaultKeygenFailureSubmitterIsSlashedButNotJailedAfterSuccessConsensus(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	dummySlasher, ok := handler.mgr.Slasher().(*DummySlasher)
	c.Assert(ok, Equals, true)

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{
		Type:    XMRMonovaultKeygen,
		Members: helper.members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	observedMoneroHeight := int64(777)
	keygenTime := int64(1024)
	keygenHeight := helper.ctx.BlockHeight()

	// members[0] is the victim: its DKG timed out waiting for peers, so it
	// blames the members it did not hear from. members[1] is merely slow.
	// members[2:] finish in time; 6 of 8 is a supermajority.
	victim := helper.members[0]
	victimAddr, err := victim.GetThorAddress()
	c.Assert(err, IsNil)
	slowMember := helper.members[1]
	slowAddr, err := slowMember.GetThorAddress()
	c.Assert(err, IsNil)

	failureMsg, err := NewMsgTssPoolV3(
		helper.members.Strings(),
		poolPubKey,
		nil,
		nil,
		XMRMonovaultKeygen,
		keygenHeight,
		[]Blame{{
			FailReason: "round1 commitments timeout",
			Round:      "frostDKGRound1",
			BlameNodes: []Node{
				{Pubkey: helper.members[6].String()},
				{Pubkey: helper.members[7].String()},
			},
		}},
		xmrMonovaultChains().Strings(),
		victimAddr,
		keygenTime,
		common.EmptyPubKey,
		nil,
		nil,
		common.EmptyPubKey,
		0,
	)
	c.Assert(err, IsNil)
	c.Assert(handler.handle(helper.ctx, failureMsg), IsNil)

	successMsgFor := func(member common.PubKey) *MsgTssPool {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		msg, msgErr := NewMsgTssPoolV3(
			helper.members.Strings(),
			poolPubKey,
			nil,
			nil,
			XMRMonovaultKeygen,
			keygenHeight,
			nil,
			xmrMonovaultChains().Strings(),
			addr,
			keygenTime,
			common.EmptyPubKey,
			nil,
			validTssKeyshareBackup(),
			frostPubKey,
			observedMoneroHeight,
		)
		c.Assert(msgErr, IsNil)
		return msg
	}

	var successID string
	for _, member := range helper.members[2:] {
		msg := successMsgFor(member)
		successID = msg.ID
		c.Assert(handler.handle(helper.ctx, msg), IsNil)
	}
	c.Assert(successID == failureMsg.ID, Equals, false)

	// Success consensus reached and the vault finalized, despite the victim.
	voter, err := helper.keeper.GetTssVoter(helper.ctx, successID)
	c.Assert(err, IsNil)
	c.Assert(voter.HasConsensus(), Equals, true)
	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, true)

	// XMR monovault keygen is slash-only: both non-signers of the success
	// ID are penalized, but neither is jailed.
	failKeygenSlashPoints := helper.constAccessor.GetInt64Value(constants.FailKeygenSlashPoints)
	for _, addr := range []cosmos.AccAddress{victimAddr, slowAddr} {
		jail, jailErr := helper.keeper.GetNodeAccountJail(helper.ctx, addr)
		c.Assert(jailErr, IsNil)
		c.Assert(jail.IsJailed(helper.ctx), Equals, false)
		points, pointsOk := dummySlasher.pts[addr.String()]
		c.Assert(pointsOk, Equals, true)
		c.Assert(points >= failKeygenSlashPoints, Equals, true)
	}

	// Defect 2 fixed: the slow member signs the same success ID late and the
	// persisted MajorityConsensusBlockHeight makes the slash-point refund
	// branch reachable.
	helper.ctx = helper.ctx.WithBlockHeight(helper.ctx.BlockHeight() + 10)
	c.Assert(handler.handle(helper.ctx, successMsgFor(slowMember)), IsNil)
	slowJail, err := helper.keeper.GetNodeAccountJail(helper.ctx, slowAddr)
	c.Assert(err, IsNil)
	c.Assert(slowJail.IsJailed(helper.ctx), Equals, false)
	c.Assert(dummySlasher.pts[slowAddr.String()], Equals, int64(0))
	victimJailAfterLateSign, err := helper.keeper.GetNodeAccountJail(helper.ctx, victimAddr)
	c.Assert(err, IsNil)
	c.Assert(victimJailAfterLateSign.IsJailed(helper.ctx), Equals, false)

	// Defect 1 fixed by slash-only treatment: the victim's failure carries a
	// different ID, so it still cannot satisfy the success ID, but the stuck
	// penalty is slash points rather than jail.
	c.Assert(handler.handle(helper.ctx, failureMsg), IsNil)
	victimJail, err := helper.keeper.GetNodeAccountJail(helper.ctx, victimAddr)
	c.Assert(err, IsNil)
	c.Assert(victimJail.IsJailed(helper.ctx), Equals, false)
	victimPoints, ok := dummySlasher.pts[victimAddr.String()]
	c.Assert(ok, Equals, true)
	c.Assert(victimPoints >= failKeygenSlashPoints, Equals, true)
}

// TestXMRMonovaultKeygenReleasesLateSigner pins the fix for defect 2 of the XMR
// keygen mass-jailing (issue #3092). Every member eventually reports the same
// keygen success; members report in slice order, so 2/3 consensus crosses at
// reporter index smCount-1 and the member at index smCount is the first success
// report to land strictly after consensus - the one judgeLateSigner jails at the
// crossing. judgeLateSigner mutates the caller's voter so the vault-finalize
// SetTssVoter persists MajorityConsensusBlockHeight, which makes the release
// path reachable: once all members have reported, none may remain in keygen
// jail. Without the fix the first post-consensus reporter stayed stuck. Under
// slash-only (C2) no member is jailed for XMR keygen, so the surviving mechanism
// this pins is the slash-point refund on the release branch: absentees are
// slash-pointed at the consensus crossing and must be refunded to zero once they
// report late, which only happens if MajorityConsensusBlockHeight persists.
func (s *HandlerTssSuite) TestXMRMonovaultKeygenReleasesLateSigner(c *C) {
	helper := newTssHandlerTestHelper(c)
	dummyMgr := NewDummyMgrWithKeeper(helper.keeper)
	handler := NewTssHandler(dummyMgr)
	dummySlasher, ok := dummyMgr.Slasher().(*DummySlasher)
	c.Assert(ok, Equals, true)
	ctx := helper.ctx

	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{Type: XMRMonovaultKeygen, Members: helper.members.Strings()}}
	helper.keeper.SetKeygenBlock(ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	observedMoneroHeight := int64(777)

	for _, member := range helper.members {
		addr, aerr := member.GetThorAddress()
		c.Assert(aerr, IsNil)
		msg, merr := NewMsgTssPoolV3(
			helper.members.Strings(),
			poolPubKey,
			nil,
			nil,
			XMRMonovaultKeygen,
			ctx.BlockHeight(),
			nil,
			xmrMonovaultChains().Strings(),
			addr,
			int64(1024),
			common.EmptyPubKey,
			nil,
			validTssKeyshareBackup(),
			frostPubKey,
			observedMoneroHeight,
		)
		c.Assert(merr, IsNil)
		msg.ObservedMoneroHeight = observedMoneroHeight
		c.Assert(handler.handle(ctx, msg), IsNil)
	}

	// No member may remain in keygen jail once all have reported success - in
	// particular the first reporter after consensus, which defect 2 left stuck.
	for _, member := range helper.members {
		addr, err := member.GetThorAddress()
		c.Assert(err, IsNil)
		jail, err := helper.keeper.GetNodeAccountJail(ctx, addr)
		c.Assert(err, IsNil)
		c.Assert(jail.IsJailed(ctx), Equals, false)
		// Late reporters are slash-pointed at the consensus crossing and refunded
		// by the release branch once they report. If C1 regresses and
		// MajorityConsensusBlockHeight fails to persist, the release branch is
		// never reached and post-consensus reporters keep positive slash points.
		c.Assert(dummySlasher.pts[addr.String()], Equals, int64(0))
	}
}

// TestXMRMonovaultKeygenSlashesButDoesNotJailAbsentees pins C2 (slash-only) for
// the XMR keygen mass-jailing (issue #3092). Only enough members to cross 2/3
// consensus report; the rest never do. For XMR monovault keygen those absentees
// accrue slash points but must NOT be jailed - jailing honest-but-skewed members
// at scale is what threatened quorum. Asgard keygen keeps the jail penalty
// (asserted by TestKeygenSuccessHandler).
func (s *HandlerTssSuite) TestXMRMonovaultKeygenSlashesButDoesNotJailAbsentees(c *C) {
	helper := newTssHandlerTestHelper(c)
	dummyMgr := NewDummyMgrWithKeeper(helper.keeper)
	handler := NewTssHandler(dummyMgr)
	dummySlasher, ok := dummyMgr.Slasher().(*DummySlasher)
	c.Assert(ok, Equals, true)
	ctx := helper.ctx

	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{Type: XMRMonovaultKeygen, Members: helper.members.Strings()}}
	helper.keeper.SetKeygenBlock(ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	observedMoneroHeight := int64(777)

	n := len(helper.members)
	smCount := 0
	for k := 1; k <= n; k++ {
		if HasSuperMajority(k, n) {
			smCount = k
			break
		}
	}
	c.Assert(smCount > 0 && smCount < n, Equals, true)

	// Only the first smCount members report - enough to cross 2/3 consensus and
	// finalize the vault. The remaining members never report.
	for _, member := range helper.members[:smCount] {
		addr, aerr := member.GetThorAddress()
		c.Assert(aerr, IsNil)
		msg, merr := NewMsgTssPoolV3(
			helper.members.Strings(),
			poolPubKey,
			nil,
			nil,
			XMRMonovaultKeygen,
			ctx.BlockHeight(),
			nil,
			xmrMonovaultChains().Strings(),
			addr,
			int64(1024),
			common.EmptyPubKey,
			nil,
			validTssKeyshareBackup(),
			frostPubKey,
			observedMoneroHeight,
		)
		c.Assert(merr, IsNil)
		msg.ObservedMoneroHeight = observedMoneroHeight
		c.Assert(handler.handle(ctx, msg), IsNil)
	}

	// Absentees are slash-pointed for the missed keygen but not jailed.
	for _, member := range helper.members[smCount:] {
		addr, err := member.GetThorAddress()
		c.Assert(err, IsNil)
		jail, err := helper.keeper.GetNodeAccountJail(ctx, addr)
		c.Assert(err, IsNil)
		c.Assert(jail.IsJailed(ctx), Equals, false)
		pts, ptsOk := dummySlasher.pts[addr.String()]
		c.Assert(ptsOk, Equals, true)
		c.Assert(pts > 0, Equals, true)
	}
}

// TestXMRMonovaultKeygenDoesNotReleaseForeignKeygenJail pins that a slash-only
// XMR monovault keygen never lifts a jail it did not impose. The keygen-vote jail
// reason is a generic string shared across keygen types, and Asgard + XMR keygens
// can live in the same block over the same members, so a late XMR success report
// must not clear a "failed to vote keygen in time" jail set by an Asgard keygen.
func (s *HandlerTssSuite) TestXMRMonovaultKeygenDoesNotReleaseForeignKeygenJail(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	ctx := helper.ctx

	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{Type: XMRMonovaultKeygen, Members: helper.members.Strings()}}
	helper.keeper.SetKeygenBlock(ctx, keygenBlock)

	// Pre-jail the last member as if a different (Asgard) keygen had jailed it for
	// missing its vote. It reports the XMR success last, so it exercises the
	// release branch as a late signer.
	victim := helper.members[len(helper.members)-1]
	victimAddr, aerr := victim.GetThorAddress()
	c.Assert(aerr, IsNil)
	releaseHeight := ctx.BlockHeight() + 4320
	c.Assert(helper.keeper.SetNodeAccountJail(ctx, victimAddr, releaseHeight, jailReasonFailedToVoteKeygen), IsNil)

	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	observedMoneroHeight := int64(777)

	for _, member := range helper.members {
		addr, merr := member.GetThorAddress()
		c.Assert(merr, IsNil)
		msg, mErr := NewMsgTssPoolV3(
			helper.members.Strings(),
			poolPubKey,
			nil,
			nil,
			XMRMonovaultKeygen,
			ctx.BlockHeight(),
			nil,
			xmrMonovaultChains().Strings(),
			addr,
			int64(1024),
			common.EmptyPubKey,
			nil,
			validTssKeyshareBackup(),
			frostPubKey,
			observedMoneroHeight,
		)
		c.Assert(mErr, IsNil)
		msg.ObservedMoneroHeight = observedMoneroHeight
		c.Assert(handler.handle(ctx, msg), IsNil)
	}

	// The foreign Asgard keygen jail must remain: XMR keygen is slash-only and
	// releases nothing.
	jail, err := helper.keeper.GetNodeAccountJail(ctx, victimAddr)
	c.Assert(err, IsNil)
	c.Assert(jail.IsJailed(ctx), Equals, true)
	c.Assert(jail.Reason, Equals, jailReasonFailedToVoteKeygen)
}

func (s *HandlerTssSuite) TestXMRMonovaultKeygenRejectsMismatchedLatchedFields(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{
		Type:    XMRMonovaultKeygen,
		Members: helper.members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	keygenTime := int64(1024)
	observedMoneroHeight := int64(777)
	msg, err := NewMsgTssPoolV3(
		helper.members.Strings(),
		poolPubKey,
		nil,
		nil,
		XMRMonovaultKeygen,
		helper.ctx.BlockHeight(),
		nil,
		xmrMonovaultChains().Strings(),
		helper.signer,
		keygenTime,
		common.EmptyPubKey,
		nil,
		validTssKeyshareBackup(),
		frostPubKey,
		observedMoneroHeight,
	)
	c.Assert(err, IsNil)

	voter := NewTssVoter(msg.ID, helper.members.Strings(), poolPubKey, common.EmptyPubKey)
	voter.PoolPubKeyFrost = frostPubKey
	voter.ObservedMoneroHeight = observedMoneroHeight
	helper.keeper.SetTssVoter(helper.ctx, voter)

	mismatchedFrost := *msg
	mismatchedFrost.PoolPubKeyFrost = GetRandomEd25519PubKey()
	_, err = handler.Run(helper.ctx, &mismatchedFrost)
	c.Assert(err, ErrorMatches, ".*invalid tss message.*")

	mismatchedHeight := *msg
	mismatchedHeight.ObservedMoneroHeight = observedMoneroHeight + 1
	_, err = handler.Run(helper.ctx, &mismatchedHeight)
	c.Assert(err, ErrorMatches, ".*invalid tss message.*")
}

func (s *HandlerTssSuite) TestXMRMonovaultRejectsMismatchedFrostKeyOrHeight(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))

	members := helper.members[:4]
	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{
		Type:    XMRMonovaultKeygen,
		Members: members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	for _, member := range members {
		setKeygenMemberNodeAccount(c, helper, member, NodeReady)
	}

	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	frostPubKey := GetRandomEd25519PubKey()
	signerOne, err := members[0].GetThorAddress()
	c.Assert(err, IsNil)
	observedMoneroHeight := int64(777)
	msg, err := NewMsgTssPoolV3(
		members.Strings(),
		poolPubKey,
		nil,
		nil,
		XMRMonovaultKeygen,
		helper.ctx.BlockHeight(),
		nil,
		xmrMonovaultChains().Strings(),
		signerOne,
		keygenTime,
		common.EmptyPubKey,
		nil,
		validTssKeyshareBackup(),
		frostPubKey,
		observedMoneroHeight,
	)
	c.Assert(err, IsNil)

	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)

	updatedVoter, err := helper.keeper.GetTssVoter(helper.ctx, msg.ID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.PoolPubKeyFrost.Equals(frostPubKey), Equals, true)
	c.Assert(updatedVoter.ObservedMoneroHeight, Equals, observedMoneroHeight)

	signerTwo, err := members[1].GetThorAddress()
	c.Assert(err, IsNil)
	mismatchedMsg, err := NewMsgTssPoolV3(
		members.Strings(),
		poolPubKey,
		nil,
		nil,
		XMRMonovaultKeygen,
		helper.ctx.BlockHeight(),
		nil,
		xmrMonovaultChains().Strings(),
		signerTwo,
		keygenTime,
		common.EmptyPubKey,
		nil,
		validTssKeyshareBackup(),
		GetRandomEd25519PubKey(),
		observedMoneroHeight+1,
	)
	c.Assert(err, IsNil)
	mismatchedMsg.ID = msg.ID

	_, err = handler.Run(helper.ctx, mismatchedMsg)
	c.Assert(err, ErrorMatches, ".*invalid tss message.*")
}

func setKeygenMemberNodeAccount(c *C, helper tssHandlerTestHelper, member common.PubKey, status NodeStatus) {
	na := GetRandomValidatorNode(status)
	na.NodeAddress, _ = member.GetThorAddress()
	na.PubKeySet.Secp256k1 = member
	na.Bond = cosmos.NewUint(1000000 * common.One)
	c.Assert(helper.keeper.SetNodeAccount(helper.ctx, na), IsNil)
}

func validTssKeyshareBackup() []byte {
	backup := make([]byte, 2048)
	for i := range backup {
		backup[i] = byte(i)
	}
	return backup
}

func (s *HandlerTssSuite) TestAsgardKeygenWithReadyMembersFinalizesOnSuperMajority(c *C) {
	helper := newTssHandlerTestHelper(c)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	c.Assert(helper.keeper.Keeper.DeleteVault(helper.ctx, helper.poolPk), IsNil)
	members := helper.members[:4]

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{
		Type:    AsgardKeygen,
		Members: members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	keygenTime := int64(1024)
	msg, err := NewMsgTssPoolV2(
		members.Strings(),
		poolPubKey,
		nil,
		nil,
		AsgardKeygen,
		helper.ctx.BlockHeight(),
		nil,
		common.Chains{common.RuneAsset().Chain}.Strings(),
		helper.signer,
		keygenTime,
		poolEddsaPubKey,
		nil,
		nil,
		common.EmptyPubKey,
	)
	c.Assert(err, IsNil)

	setKeygenMemberNodeAccount(c, helper, members[0], NodeActive)
	setKeygenMemberNodeAccount(c, helper, members[1], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[2], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[3], NodeReady)

	voter := NewTssVoter(msg.ID, members.Strings(), poolPubKey, poolEddsaPubKey)
	for _, member := range members[1:3] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		voter.Signers = append(voter.Signers, addr.String())
	}
	voter.Secp256K1Signatures = []string{"sig-a", "sig-a", "sig-a"}
	helper.keeper.SetTssVoter(helper.ctx, voter)

	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)

	updatedVoter, err := helper.keeper.GetTssVoter(helper.ctx, msg.ID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.HasConsensus(), Equals, true)
	c.Assert(updatedVoter.HasCompleteConsensus(), Equals, false)

	vault, err := helper.keeper.GetVault(helper.ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.Status, Equals, InitVault)
}

func (s *HandlerTssSuite) TestSharedKeygenBlockRotatesRegularAsgardWithoutWaitingForXMR(c *C) {
	helper := newTssHandlerTestHelper(c)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	mgr.networkMgr = newNetworkMgr(helper.keeper, mgr.TxOutStore(), mgr.EventMgr())
	handler := NewTssHandler(mgr)
	c.Assert(helper.keeper.Keeper.DeleteVault(helper.ctx, helper.poolPk), IsNil)
	members := helper.members[:4]

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{
		{
			Type:    AsgardKeygen,
			Members: members.Strings(),
		},
		{
			Type:    XMRMonovaultKeygen,
			Members: members.Strings(),
		},
	}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	keygenTime := int64(1024)
	msg, err := NewMsgTssPoolV2(
		members.Strings(),
		poolPubKey,
		nil,
		nil,
		AsgardKeygen,
		helper.ctx.BlockHeight(),
		nil,
		common.Chains{common.RuneAsset().Chain}.Strings(),
		helper.signer,
		keygenTime,
		poolEddsaPubKey,
		nil,
		nil,
		common.EmptyPubKey,
	)
	c.Assert(err, IsNil)

	setKeygenMemberNodeAccount(c, helper, members[0], NodeActive)
	setKeygenMemberNodeAccount(c, helper, members[1], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[2], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[3], NodeReady)

	voter := NewTssVoter(msg.ID, members.Strings(), poolPubKey, poolEddsaPubKey)
	for _, member := range members[1:3] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		voter.Signers = append(voter.Signers, addr.String())
	}
	voter.Secp256K1Signatures = []string{"sig-a", "sig-a", "sig-a"}
	helper.keeper.SetTssVoter(helper.ctx, voter)

	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)

	regularVault, err := helper.keeper.GetVault(helper.ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(regularVault.Status, Equals, ActiveVault)
	c.Assert(regularVault.IsXMRMonovault(), Equals, false)
}

func (s *HandlerTssSuite) TestXMRMonovaultRotatesWithoutRegularVaultRotation(c *C) {
	helper := newTssHandlerTestHelper(c)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	mgr.networkMgr = newNetworkMgr(helper.keeper, mgr.TxOutStore(), mgr.EventMgr())
	handler := NewTssHandler(mgr)
	c.Assert(helper.keeper.Keeper.DeleteVault(helper.ctx, helper.poolPk), IsNil)
	members := helper.members[:4]

	oldRegular := NewVaultV2(
		helper.ctx.BlockHeight()-10,
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.THORChain, common.BTCChain}.Strings(),
		nil,
		GetRandomEd25519PubKey(),
	)
	oldRegular.Membership = members.Strings()
	c.Assert(helper.keeper.SetVault(helper.ctx, oldRegular), IsNil)

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{
		{
			Type:    AsgardKeygen,
			Members: members.Strings(),
		},
		{
			Type:    XMRMonovaultKeygen,
			Members: members.Strings(),
		},
	}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	setKeygenMemberNodeAccount(c, helper, members[0], NodeActive)
	setKeygenMemberNodeAccount(c, helper, members[1], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[2], NodeReady)
	setKeygenMemberNodeAccount(c, helper, members[3], NodeReady)

	keygenTime := int64(1024)
	xmrPoolPubKey := GetRandomPubKey()
	xmrFrostPubKey := GetRandomEd25519PubKey()
	observedMoneroHeight := int64(777)
	xmrMsg, err := NewMsgTssPoolV3(
		members.Strings(),
		xmrPoolPubKey,
		nil,
		nil,
		XMRMonovaultKeygen,
		helper.ctx.BlockHeight(),
		nil,
		xmrMonovaultChains().Strings(),
		helper.signer,
		keygenTime,
		common.EmptyPubKey,
		nil,
		validTssKeyshareBackup(),
		xmrFrostPubKey,
		observedMoneroHeight,
	)
	c.Assert(err, IsNil)
	xmrVoter := NewTssVoter(xmrMsg.ID, members.Strings(), xmrPoolPubKey, common.EmptyPubKey)
	xmrVoter.PoolPubKeyFrost = xmrFrostPubKey
	xmrVoter.ObservedMoneroHeight = observedMoneroHeight
	for _, member := range members[1:3] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		xmrVoter.Signers = append(xmrVoter.Signers, addr.String())
	}
	helper.keeper.SetTssVoter(helper.ctx, xmrVoter)

	_, err = handler.Run(helper.ctx, xmrMsg)
	c.Assert(err, IsNil)

	xmrVault, err := helper.keeper.GetVault(helper.ctx, xmrPoolPubKey)
	c.Assert(err, IsNil)
	c.Assert(xmrVault.Status, Equals, ActiveVault)
	c.Assert(xmrVault.IsXMRMonovault(), Equals, true)

	oldRegular, err = helper.keeper.GetVault(helper.ctx, oldRegular.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldRegular.Status, Equals, ActiveVault)

	regularPoolPubKey := GetRandomPubKey()
	regularEddsaPubKey := GetRandomPubKey()
	regularMsg, err := NewMsgTssPoolV2(
		members.Strings(),
		regularPoolPubKey,
		nil,
		nil,
		AsgardKeygen,
		helper.ctx.BlockHeight(),
		nil,
		common.Chains{common.RuneAsset().Chain}.Strings(),
		helper.signer,
		keygenTime,
		regularEddsaPubKey,
		nil,
		nil,
		common.EmptyPubKey,
	)
	c.Assert(err, IsNil)
	regularVoter := NewTssVoter(regularMsg.ID, members.Strings(), regularPoolPubKey, regularEddsaPubKey)
	for _, member := range members[1:3] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		regularVoter.Signers = append(regularVoter.Signers, addr.String())
	}
	regularVoter.Secp256K1Signatures = []string{"sig-a", "sig-a", "sig-a"}
	helper.keeper.SetTssVoter(helper.ctx, regularVoter)

	_, err = handler.Run(helper.ctx, regularMsg)
	c.Assert(err, IsNil)

	regularVault, err := helper.keeper.GetVault(helper.ctx, regularPoolPubKey)
	c.Assert(err, IsNil)
	c.Assert(regularVault.Status, Equals, ActiveVault)
	c.Assert(regularVault.HasChain(common.XMRChain), Equals, false)

	xmrVault, err = helper.keeper.GetVault(helper.ctx, xmrPoolPubKey)
	c.Assert(err, IsNil)
	c.Assert(xmrVault.Status, Equals, ActiveVault)
	c.Assert(xmrVault.IsXMRMonovault(), Equals, true)

	oldRegular, err = helper.keeper.GetVault(helper.ctx, oldRegular.PubKey)
	c.Assert(err, IsNil)
	c.Assert(oldRegular.Status, Equals, RetiringVault)
}

func (s *HandlerTssSuite) TestAsgardKeygenAllActiveStillWaitsForCompleteConsensus(c *C) {
	helper := newTssHandlerTestHelper(c)
	helper.keeper.SetMimir(helper.ctx, constants.MaxAbsentKeygenReporters.String(), 0)
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	c.Assert(helper.keeper.Keeper.DeleteVault(helper.ctx, helper.poolPk), IsNil)
	members := helper.members[:4]

	keygenBlock := NewKeygenBlock(helper.ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{{
		Type:    AsgardKeygen,
		Members: members.Strings(),
	}}
	helper.keeper.SetKeygenBlock(helper.ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	keygenTime := int64(1024)
	msg, err := NewMsgTssPoolV2(
		members.Strings(),
		poolPubKey,
		nil,
		nil,
		AsgardKeygen,
		helper.ctx.BlockHeight(),
		nil,
		common.Chains{common.RuneAsset().Chain}.Strings(),
		helper.signer,
		keygenTime,
		poolEddsaPubKey,
		nil,
		nil,
		common.EmptyPubKey,
	)
	c.Assert(err, IsNil)

	for _, member := range members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}

	voter := NewTssVoter(msg.ID, members.Strings(), poolPubKey, poolEddsaPubKey)
	for _, member := range members[1:3] {
		addr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		voter.Signers = append(voter.Signers, addr.String())
	}
	voter.Secp256K1Signatures = []string{"sig-a", "sig-a", "sig-a"}
	helper.keeper.SetTssVoter(helper.ctx, voter)

	_, err = handler.Run(helper.ctx, msg)
	c.Assert(err, IsNil)

	updatedVoter, err := helper.keeper.GetTssVoter(helper.ctx, msg.ID)
	c.Assert(err, IsNil)
	c.Assert(updatedVoter.HasConsensus(), Equals, true)
	c.Assert(updatedVoter.HasCompleteConsensus(), Equals, false)
	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, false)
}

// TestAsgardKeygenRejectsXMROnlyFields ensures an asgard keygen report cannot
// carry the FROST pubkey or the observed Monero height. Those fields are only
// bound into the message ID for XMR keygens, so for asgard they would be pinned
// into the voter by whichever report is processed first and every honest report
// would then be rejected before it is counted (issue #2953).
func (s *HandlerTssSuite) TestAsgardKeygenRejectsXMROnlyFields(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()

	newAsgardMsg := func() *MsgTssPool {
		msg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), helper.signer, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		return msg
	}

	// an untainted asgard report is accepted
	c.Assert(handler.validate(helper.ctx, newAsgardMsg()), IsNil)

	frostMsg := newAsgardMsg()
	frostMsg.PoolPubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(handler.validate(helper.ctx, frostMsg), NotNil)

	moneroHeightMsg := newAsgardMsg()
	moneroHeightMsg.ObservedMoneroHeight = 1
	c.Assert(handler.validate(helper.ctx, moneroHeightMsg), NotNil)
}

// TestAsgardKeygenEddsaMismatchDoesNotBlockConsensus ensures a member reporting
// a divergent eddsa pubkey first cannot stall the keygen: the eddsa pubkey is
// part of the message ID, so the divergent report tallies in its own voter and
// the honest members still reach consensus and finalise the vault (issue #2953).
func (s *HandlerTssSuite) TestAsgardKeygenEddsaMismatchDoesNotBlockConsensus(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}
	helper.keeper.SetMimir(helper.ctx, constants.MaxAbsentKeygenReporters.String(), 1)

	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()

	newReport := func(signer cosmos.AccAddress, eddsa common.PubKey) *MsgTssPool {
		msg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), signer, keygenTime, eddsa, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		return msg
	}

	// the last member reports a divergent eddsa pubkey before anyone else
	poisonAddr, err := helper.members[len(helper.members)-1].GetThorAddress()
	c.Assert(err, IsNil)
	poisonMsg := newReport(poisonAddr, GetRandomPubKey())
	honestMsg := newReport(helper.signer, poolEddsaPubKey)
	c.Assert(poisonMsg.ID, Not(Equals), honestMsg.ID)
	_, err = handler.Run(helper.ctx, poisonMsg)
	c.Assert(err, IsNil)

	// the honest members still reach consensus among themselves
	for _, member := range helper.members[:len(helper.members)-1] {
		thorAddr, addrErr := member.GetThorAddress()
		c.Assert(addrErr, IsNil)
		_, runErr := handler.Run(helper.ctx, newReport(thorAddr, poolEddsaPubKey))
		c.Assert(runErr, IsNil)
	}

	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, true)
	vault, err := helper.keeper.GetVault(helper.ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.PubKeyEddsa.Equals(poolEddsaPubKey), Equals, true)

	// the divergent report tallied in its own voter and never reached consensus
	poisonVoter, err := helper.keeper.GetTssVoter(helper.ctx, poisonMsg.ID)
	c.Assert(err, IsNil)
	c.Assert(poisonVoter.HasConsensus(), Equals, false)
}

// TestAsgardKeygenZeroNodeFailureCannotCompleteSuccessVoter ensures a failure
// report that cannot attribute blame to a specific node does not join the
// success voter. Otherwise the failure report could supply the quorum-crossing
// vote and make the handler execute the failure branch for honest success
// reports, including resetting Ready nodes to Standby.
func (s *HandlerTssSuite) TestAsgardKeygenZeroNodeFailureCannotCompleteSuccessVoter(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeReady)
	}
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	keygenTime := int64(1024)

	newReport := func(member common.PubKey, blame []Blame) *MsgTssPool {
		signer, err := member.GetThorAddress()
		c.Assert(err, IsNil)
		msg, err := NewMsgTssPoolV2(
			helper.members.Strings(),
			poolPubKey,
			keygenCheckSig,
			nil,
			AsgardKeygen,
			helper.ctx.BlockHeight(),
			blame,
			common.Chains{common.RuneAsset().Chain}.Strings(),
			signer,
			keygenTime,
			poolEddsaPubKey,
			nil,
			nil,
			common.EmptyPubKey,
		)
		c.Assert(err, IsNil)
		return msg
	}

	// Five reports are one short of the six-of-eight supermajority.
	var successID string
	for _, member := range helper.members[:5] {
		msg := newReport(member, nil)
		successID = msg.ID
		_, err := handler.Run(helper.ctx, msg)
		c.Assert(err, IsNil)
	}

	zeroNodeFailure := newReport(helper.members[5], []Blame{{
		FailReason: "failure without an attributable node",
		Round:      "keygen",
	}})
	c.Assert(zeroNodeFailure.IsSuccess(), Equals, false)
	c.Assert(zeroNodeFailure.Blame[0].BlameNodes, HasLen, 0)
	c.Assert(zeroNodeFailure.ID, Not(Equals), successID)
	_, err := handler.Run(helper.ctx, zeroNodeFailure)
	c.Assert(err, IsNil)

	successVoter, err := helper.keeper.GetTssVoter(helper.ctx, successID)
	c.Assert(err, IsNil)
	c.Assert(successVoter.Signers, HasLen, 5)
	c.Assert(successVoter.HasConsensus(), Equals, false)
	failureVoter, err := helper.keeper.GetTssVoter(helper.ctx, zeroNodeFailure.ID)
	c.Assert(err, IsNil)
	c.Assert(failureVoter.Signers, HasLen, 1)
	c.Assert(failureVoter.HasConsensus(), Equals, false)

	// Since the failure voter did not reach quorum, Ready nodes are untouched.
	readyAddr, err := helper.members[7].GetThorAddress()
	c.Assert(err, IsNil)
	readyNode, err := helper.keeper.GetNodeAccount(helper.ctx, readyAddr)
	c.Assert(err, IsNil)
	c.Assert(readyNode.Status, Equals, NodeReady)

	// The same reporter can still submit the sixth success vote, which reaches
	// consensus in the separate success voter and finalizes the vault.
	_, err = handler.Run(helper.ctx, newReport(helper.members[5], nil))
	c.Assert(err, IsNil)
	successVoter, err = helper.keeper.GetTssVoter(helper.ctx, successID)
	c.Assert(err, IsNil)
	c.Assert(successVoter.HasConsensus(), Equals, true)
	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, true)
}

// keygenCheckSig is a non-empty placeholder check signature used to satisfy
// ConsensusCheckSignature quorum in tests; signature-verification is bypassed
// via the verifySecp256K1Signature override.
var keygenCheckSig = []byte("fakeCheckSignature")

func (s *HandlerTssSuite) TestKeygenSuccessHandlerBufferDefault(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	helper.keeper.SetMimir(helper.ctx, constants.MaxAbsentKeygenReporters.String(), 0)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}
	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()

	// With the default mocknet buffer of 0, the vault must not exist until every
	// member reports; the final reporter triggers vault creation.
	for idx, item := range helper.members {
		thorAddr, err := item.GetThorAddress()
		c.Assert(err, IsNil)
		tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), thorAddr, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		c.Assert(handler.handle(helper.ctx, tssMsg), IsNil)
		if idx < len(helper.members)-1 {
			c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, false, Commentf("vault should not exist after %d/%d reports with buffer=0", idx+1, len(helper.members)))
		}
	}
	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, true)
}

func (s *HandlerTssSuite) TestKeygenSuccessHandlerBufferOne(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}
	helper.keeper.SetMimir(helper.ctx, constants.MaxAbsentKeygenReporters.String(), 1)

	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()

	// First n-2 reports should not yet finalize the vault.
	for i := 0; i < len(helper.members)-2; i++ {
		thorAddr, err := helper.members[i].GetThorAddress()
		c.Assert(err, IsNil)
		tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), thorAddr, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		c.Assert(handler.handle(helper.ctx, tssMsg), IsNil)
		c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, false)
	}

	// The (n-1)th report crosses the buffered threshold and creates the vault.
	penultimate := helper.members[len(helper.members)-2]
	penultimateAddr, err := penultimate.GetThorAddress()
	c.Assert(err, IsNil)
	penultimateMsg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), penultimateAddr, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
	c.Assert(err, IsNil)
	c.Assert(handler.handle(helper.ctx, penultimateMsg), IsNil)
	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, true)

	createdVault, err := helper.keeper.GetVault(helper.ctx, poolPubKey)
	c.Assert(err, IsNil)
	createdHeight := createdVault.BlockHeight
	// Membership must be the full keygen list, not just the reporters.
	c.Assert(createdVault.Membership, HasLen, len(helper.members))

	// Re-entry guard: the late reporter must not re-create or modify the vault.
	lastAddr, err := helper.members[len(helper.members)-1].GetThorAddress()
	c.Assert(err, IsNil)
	lastMsg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight()+5, nil, common.Chains{common.RuneAsset().Chain}.Strings(), lastAddr, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
	c.Assert(err, IsNil)
	lateCtx := helper.ctx.WithBlockHeight(helper.ctx.BlockHeight() + 5)
	c.Assert(handler.handle(lateCtx, lastMsg), IsNil)
	postVault, err := helper.keeper.GetVault(lateCtx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(postVault.BlockHeight, Equals, createdHeight, Commentf("vault BlockHeight must not change on late reporter"))
}

func (s *HandlerTssSuite) TestKeygenSuccessHandlerBufferNegativeClamped(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	for _, member := range helper.members {
		setKeygenMemberNodeAccount(c, helper, member, NodeActive)
	}
	helper.keeper.SetMimir(helper.ctx, constants.MaxAbsentKeygenReporters.String(), -5)

	handler := NewTssHandler(NewDummyMgrWithKeeper(helper.keeper))
	keygenTime := int64(1024)
	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()

	// Negative buffers must clamp to 0, behaving like the unbuffered default.
	for i := 0; i < len(helper.members)-1; i++ {
		thorAddr, err := helper.members[i].GetThorAddress()
		c.Assert(err, IsNil)
		tssMsg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, helper.ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), thorAddr, keygenTime, poolEddsaPubKey, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		c.Assert(handler.handle(helper.ctx, tssMsg), IsNil)
	}
	c.Assert(helper.keeper.VaultExists(helper.ctx, poolPubKey), Equals, false)
}

func (s *HandlerTssSuite) TestObservingSlashing(c *C) {
	ctx, mgr := setupManagerForTest(c)
	height := int64(1024)
	ctx = ctx.WithBlockHeight(height)

	// Check expected slash point amounts
	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := mgr.GetConstants().GetInt64Value(constants.ObservationDelayFlexibility)
	failKeygenSlashPoints := mgr.GetConstants().GetInt64Value(constants.FailKeygenSlashPoints)
	c.Assert(observeSlashPoints, Equals, int64(1))
	c.Assert(lackOfObservationPenalty, Equals, int64(2))
	c.Assert(observeFlex, Equals, int64(10))
	c.Assert(failKeygenSlashPoints, Equals, int64(720))

	asgardVault := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, asgardVault), IsNil)

	nas := NodeAccounts{
		// 6 Active nodes, 1 Standby node; 2/3rd consensus needs 4, 3/3rds success needs 6.
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeActive),
		GetRandomValidatorNode(NodeStandby),
	}
	for _, item := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, item), IsNil)
	}

	var members common.PubKeys
	for i := 0; i < 6; i++ {
		members = append(members, nas[i].PubKeySet.Secp256k1)
	}
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].String() < members[j].String()
	})
	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{
		{
			Type:    AsgardKeygen,
			Members: members.Strings(),
		},
	}
	mgr.Keeper().SetKeygenBlock(ctx, keygenBlock)
	keygenTime := int64(1024)
	poolPk := GetRandomPubKey()

	msg, err := NewMsgTssPool(members.Strings(), poolPk, nil, nil, AsgardKeygen, ctx.BlockHeight(), nil, common.Chains{common.RuneAsset().Chain}.Strings(), cosmos.AccAddress{}, keygenTime)
	c.Assert(err, IsNil)

	handler := NewTssHandler(mgr)

	broadcast := func(c *C, ctx cosmos.Context, na NodeAccount, msg *MsgTssPool) {
		msg.Signer = na.NodeAddress
		err := handler.handle(ctx, msg)
		c.Assert(err, IsNil)
	}

	checkSlashPoints := func(c *C, ctx cosmos.Context, nas NodeAccounts, expected [7]int64) {
		var slashPoints [7]int64
		for i, na := range nas {
			slashPoint, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			slashPoints[i] = slashPoint
		}
		c.Assert(slashPoints == expected, Equals, true, Commentf(fmt.Sprint(slashPoints)))
	}

	checkSlashPoints(c, ctx, nas, [7]int64{0, 0, 0, 0, 0, 0, 0})

	// 3/6 Active nodes observe.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 0, 0, 0, 0, 0, 0})
	broadcast(c, ctx, nas[1], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 1, 0, 0, 0, 0, 0})
	broadcast(c, ctx, nas[2], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 1, 1, 0, 0, 0, 0})

	// nas[0] observes again.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 1, 1, 0, 0, 0, 0})

	// nas[3] observes, reaching consensus (4/6, being exactly the 2/3 threshold).
	// (Active nodes which observed are decremented ObserveSlashPoints;
	//  those which haven't are incremented LackOfObservationPenalty.)
	// Also, those which haven't observed are incremented FailKeygenSlashPoints.
	broadcast(c, ctx, nas[3], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{1, 0, 0, 0, 722, 722, 0})

	// nas[0] observes again.
	broadcast(c, ctx, nas[0], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 722, 722, 0})

	// Within the ObservationDelayFlexibility period, nas[4] observes
	// (and is decremented LackOfObservationPenalty as well as FailKeygenSlashPoints).
	height += observeFlex
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[4], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 722, 0})

	// The ObservationDelayFlexibility period ends, after which nas[5] observes (still within ChurnRetryInterval);
	// it is not incremented ObserveSlashPoints (and it is added to the list of signers)
	// and is also not decremented LackOfObservationPenalty.
	// However, it is decremented FailKeygenSlashPoints as the keygen can still succeed.
	height++
	ctx = ctx.WithBlockHeight(height)
	broadcast(c, ctx, nas[5], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 2, 0})

	// nas[5] observes again, this time incremented ObserveSlashPoints for the extra signing.
	broadcast(c, ctx, nas[5], msg)
	checkSlashPoints(c, ctx, nas, [7]int64{2, 0, 0, 0, 0, 3, 0})

	// Note that nas[6], the Standby node, remains unaffected by the Actives nodes' observations.
}

func makeInitVault(chains []string) Vault {
	return Vault{
		PubKey: GetRandomPubKey(),
		Status: InitVault,
		Type:   AsgardVault,
		Chains: chains,
	}
}

func makeInitVaultAt(height int64, membership common.PubKeys, chains []string) Vault {
	return Vault{
		BlockHeight: height,
		PubKey:      GetRandomPubKey(),
		Status:      InitVault,
		Type:        AsgardVault,
		Membership:  membership.Strings(),
		Chains:      chains,
	}
}

func (s *HandlerTssSuite) TestInitVaultsAgreeOnChains(c *C) {
	// "NEWCHAIN" is not in the chain enum, which proves the helper compares
	// strings; any chain rolling out mid-churn is handled the same way.
	with := []string{
		common.BTCChain.String(),
		common.ETHChain.String(),
		common.THORChain.String(),
		"NEWCHAIN",
	}
	without := with[:len(with)-1]

	c.Assert(initVaultsAgreeOnChains(Vaults{}), Equals, true)
	c.Assert(initVaultsAgreeOnChains(Vaults{makeInitVault(with)}), Equals, true)
	c.Assert(initVaultsAgreeOnChains(Vaults{
		makeInitVault(with),
		makeInitVault(with),
		makeInitVault(with),
		makeInitVault(with),
	}), Equals, true)
	c.Assert(initVaultsAgreeOnChains(Vaults{
		makeInitVault(with),
		makeInitVault(with),
		makeInitVault(without),
		makeInitVault(with),
	}), Equals, false)
}

func (s *HandlerTssSuite) TestRotateInitVaultsGuardrail(c *C) {
	helper := newTssHandlerTestHelper(c)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	// DummyEventMgr.EmitEvent is a no-op, which would silently mask a
	// missing security event. Use the real one.
	mgr.eventMgr = newEventMgr()
	handler := NewTssHandler(mgr)
	netMgr, ok := mgr.NetworkMgr().(*NetworkMgrDummy)
	c.Assert(ok, Equals, true)

	chainSet := []string{
		common.BTCChain.String(),
		common.ETHChain.String(),
		common.THORChain.String(),
		"NEWCHAIN",
	}
	matching := Vaults{
		makeInitVault(chainSet),
		makeInitVault(chainSet),
		makeInitVault(chainSet),
		makeInitVault(chainSet),
	}
	c.Assert(handler.tryRotateInitVaults(helper.ctx, matching), IsNil)
	c.Assert(netMgr.rotated, HasLen, len(matching))
	for i, v := range matching {
		c.Assert(netMgr.rotated[i].PubKey.Equals(v.PubKey), Equals, true)
	}

	netMgr.rotated = nil
	ctx := helper.ctx.WithEventManager(cosmos.NewEventManager())
	dropped := chainSet[:len(chainSet)-1]
	mismatched := Vaults{
		makeInitVault(chainSet),
		makeInitVault(chainSet),
		makeInitVault(dropped),
		makeInitVault(chainSet),
	}
	c.Assert(handler.tryRotateInitVaults(ctx, mismatched), IsNil)
	c.Assert(netMgr.rotated, HasLen, 0)

	var securityMsg string
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != "security" {
			continue
		}
		for _, attr := range evt.Attributes {
			if attr.Key == "msg" {
				securityMsg = attr.Value
			}
		}
	}
	c.Assert(securityMsg, Not(Equals), "")
	c.Assert(strings.HasPrefix(securityMsg, "init vault chain set mismatch"), Equals, true)
	// Operators page on the security event; the diverging vault's pubkey
	// must be in the message body so they can identify the drifted keygen.
	c.Assert(strings.Contains(securityMsg, mismatched[2].PubKey.String()), Equals, true)
}

func (s *HandlerTssSuite) TestVaultCreationHeightXmrSetForXMRChain(c *C) {
	ctx, mgr := setupManagerForTest(c)

	xmrVault := NewVaultV2(ctx.BlockHeight(), InitVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.XMRChain}.Strings(), nil, GetRandomPubKey())

	c.Assert(xmrVault.VaultCreationHeightXmr, Equals, int64(0))

	if xmrVault.GetChains().Has(common.XMRChain) {
		xmrVault.VaultCreationHeightXmr = ctx.BlockHeight()
	}
	c.Assert(mgr.Keeper().SetVault(ctx, xmrVault), IsNil)

	stored, err := mgr.Keeper().GetVault(ctx, xmrVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(stored.VaultCreationHeightXmr, Equals, ctx.BlockHeight())

	nonXmrVault := NewVaultV2(ctx.BlockHeight(), InitVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), nil, GetRandomPubKey())
	c.Assert(nonXmrVault.GetChains().Has(common.XMRChain), Equals, false)
	c.Assert(nonXmrVault.VaultCreationHeightXmr, Equals, int64(0))
}

// TestLateKeygenReportWidensInitVaultChains reproduces the mainnet churn stall at
// block 27820568. A churn that promotes Ready nodes finalizes the vault at first
// supermajority, so the chain set is computed from only 2/3 of the reports and a
// single member running with a chain disabled leaves that chain one vote short.
// The later reports all carry it, and must be allowed to repair the vault and
// unblock rotation.
func (s *HandlerTssSuite) TestLateKeygenReportWidensInitVaultChains(c *C) {
	verifySecp256K1Signature = func(_ common.PubKey, _ []byte) error { return nil }

	helper := newTssHandlerTestHelper(c)
	mgr := NewDummyMgrWithKeeper(helper.keeper)
	// DummyEventMgr.EmitEvent is a no-op, which would mask the security event.
	mgr.eventMgr = newEventMgr()
	handler := NewTssHandler(mgr)
	netMgr, ok := mgr.NetworkMgr().(*NetworkMgrDummy)
	c.Assert(ok, Equals, true)
	ctx := helper.ctx.WithEventManager(cosmos.NewEventManager())

	// ETH is the dropped chain so the test also covers routers: they are derived
	// from the same chain slice at vault creation.
	fullChains := common.Chains{common.BTCChain, common.ETHChain, common.THORChain}.Strings()
	dropped := common.Chains{common.BTCChain, common.THORChain}.Strings()
	router := ChainContract{Chain: common.ETHChain, Router: GetRandomETHAddress()}
	helper.keeper.SetChainContract(ctx, router)

	// One non-Active member is what makes asgardKeygenRequiresCompleteConsensus
	// false and so arms the finalize-on-first-supermajority path.
	for i, member := range helper.members {
		status := NodeActive
		if i == 0 {
			status = NodeReady
		}
		setKeygenMemberNodeAccount(c, helper, member, status)
	}

	// A second keygen group is required. With a single group, keygenTypeReady is
	// satisfied the moment its vault finalizes and rotation succeeds immediately
	// with the wrong chain set, so the late-report path would never be reached.
	membersB := common.PubKeys{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}
	vaultB := makeInitVaultAt(ctx.BlockHeight(), membersB, fullChains)
	c.Assert(helper.keeper.SetVault(ctx, vaultB), IsNil)

	keygenBlock := NewKeygenBlock(ctx.BlockHeight())
	keygenBlock.Keygens = []Keygen{
		{Type: AsgardKeygen, Members: helper.members.Strings()},
		{Type: AsgardKeygen, Members: membersB.Strings()},
	}
	helper.keeper.SetKeygenBlock(ctx, keygenBlock)

	poolPubKey := GetRandomPubKey()
	poolEddsaPubKey := GetRandomPubKey()
	report := func(i int, chains []string) {
		addr, err := helper.members[i].GetThorAddress()
		c.Assert(err, IsNil)
		msg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, ctx.BlockHeight(), nil, chains, addr, int64(1024), poolEddsaPubKey, nil, nil, common.EmptyPubKey)
		c.Assert(err, IsNil)
		c.Assert(handler.handle(ctx, msg), IsNil)
	}

	// Supermajority of 8 is 6. Member 1 reports without ETH, so ETH reaches only
	// 5 of the required 6 by the time the vault finalizes.
	supermajority := 6
	for i := 0; i < supermajority; i++ {
		chains := fullChains
		if i == 1 {
			chains = dropped
		}
		report(i, chains)
	}

	vault, err := helper.keeper.GetVault(ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.Status, Equals, InitVault)
	c.Assert(vault.Chains, DeepEquals, dropped, Commentf("one dissenter in the early reports must still drop the chain"))
	c.Assert(vault.Routers, HasLen, 0)
	c.Assert(netMgr.rotated, HasLen, 0, Commentf("asymmetric chain sets must not rotate"))
	c.Assert(securityEventMsg(ctx), Matches, "init vault chain set mismatch.*")

	// The next report pushes ETH to 6 of 8, repairing the vault and unblocking
	// the churn.
	report(supermajority, fullChains)

	vault, err = helper.keeper.GetVault(ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.Chains, DeepEquals, fullChains)
	c.Assert(vault.Routers, DeepEquals, []ChainContract{router}, Commentf("a late-added chain must pick up its router"))
	c.Assert(netMgr.rotated, HasLen, 2)

	// A further report changes nothing, so it must not write or re-rotate.
	report(supermajority+1, fullChains)
	c.Assert(netMgr.rotated, HasLen, 2)

	// Once the vault is Active the refresh must be inert even when the chain
	// sets do differ. NetworkMgrDummy leaves the status at InitVault, so drive
	// this directly: RotateVault is not idempotent, and a second call would
	// match the Active vault against its own membership and retire it.
	addr, err := helper.members[0].GetThorAddress()
	c.Assert(err, IsNil)
	msg, err := NewMsgTssPoolV2(helper.members.Strings(), poolPubKey, keygenCheckSig, nil, AsgardKeygen, ctx.BlockHeight(), nil, fullChains, addr, int64(1024), poolEddsaPubKey, nil, nil, common.EmptyPubKey)
	c.Assert(err, IsNil)
	voter, err := helper.keeper.GetTssVoter(ctx, msg.ID)
	c.Assert(err, IsNil)
	vault.Status = ActiveVault
	vault.Chains = dropped
	c.Assert(helper.keeper.SetVault(ctx, vault), IsNil)

	c.Assert(handler.refreshInitVaultChains(ctx, msg, voter), IsNil)
	vault, err = helper.keeper.GetVault(ctx, poolPubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.Chains, DeepEquals, dropped, Commentf("an active vault's chain set must not be rewritten"))
	c.Assert(netMgr.rotated, HasLen, 2)
}

func securityEventMsg(ctx cosmos.Context) string {
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != "security" {
			continue
		}
		for _, attr := range evt.Attributes {
			if attr.Key == "msg" {
				return attr.Value
			}
		}
	}
	return ""
}
