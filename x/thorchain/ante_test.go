package thorchain

import (
	math "math"

	"github.com/stretchr/testify/suite"

	. "gopkg.in/check.v1"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type AnteTestSuite struct {
	suite.Suite
}

var _ = Suite(&AnteTestSuite{})

func (s *AnteTestSuite) TestRejectMutlipleDepositMsgs(c *C) {
	_, k := setupKeeperForTest(c)

	ad := AnteDecorator{
		keeper: k,
	}

	// no deposit or send msgs is ok
	err := ad.rejectMultipleDepositMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}})
	c.Assert(err, IsNil)

	// one deposit msg is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}, &types.MsgDeposit{}})
	c.Assert(err, IsNil)

	// one send msg is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}, &types.MsgSend{}})
	c.Assert(err, IsNil)

	// two deposit msgs is not ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}, &types.MsgDeposit{}, &types.MsgDeposit{}})
	c.Assert(err, NotNil)

	// one deposit and one send is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}, &types.MsgDeposit{}, &types.MsgSend{}})
	c.Assert(err, IsNil)

	// two bank sends is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&banktypes.MsgSend{}, &banktypes.MsgSend{}})
	c.Assert(err, IsNil)

	bankSendDeposit := banktypes.MsgSend{
		ToAddress: k.GetModuleAccAddress(ModuleName).String(),
	}
	// one bank send to module account is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&bankSendDeposit})
	c.Assert(err, IsNil)

	// two bank sends to module account is not ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&bankSendDeposit, &bankSendDeposit})
	c.Assert(err, NotNil)

	// one deposit and one send is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}, &types.MsgDeposit{}, &types.MsgSend{}})
	c.Assert(err, IsNil)

	// two bank sends is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&banktypes.MsgSend{}, &banktypes.MsgSend{}})
	c.Assert(err, IsNil)

	// one bank send to module account is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&bankSendDeposit})
	c.Assert(err, IsNil)

	// two bank sends to module account is not ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&bankSendDeposit, &bankSendDeposit})
	c.Assert(err, NotNil)

	// one normal bank send and one bank send to module account is ok
	err = ad.rejectMultipleDepositMsgs([]cosmos.Msg{&banktypes.MsgSend{}, &bankSendDeposit})
	c.Assert(err, IsNil)
}

func (s *AnteTestSuite) TestRejectDuplicateTHORNameMsgs(c *C) {
	_, k := setupKeeperForTest(c)

	ad := AnteDecorator{
		keeper: k,
	}

	addr := GetRandomTHORAddress()
	acc, _ := addr.AccAddress()
	coin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))

	msg1 := types.NewMsgManageTHORName("test-name", common.THORChain, addr, coin, 0, common.EmptyAsset, acc, acc, 0)
	msg2 := types.NewMsgManageTHORName("other-name", common.THORChain, addr, coin, 0, common.EmptyAsset, acc, acc, 0)
	msg3 := types.NewMsgManageTHORName("test-name", common.THORChain, addr, coin, 0, common.EmptyAsset, acc, acc, 0)

	// no THORName msgs is ok
	err := ad.rejectDuplicateTHORNameMsgs([]cosmos.Msg{&types.MsgBan{}, &types.MsgBond{}})
	c.Assert(err, IsNil)

	// one THORName msg is ok
	err = ad.rejectDuplicateTHORNameMsgs([]cosmos.Msg{&types.MsgBan{}, msg1})
	c.Assert(err, IsNil)

	// two THORName msgs with different names is ok
	err = ad.rejectDuplicateTHORNameMsgs([]cosmos.Msg{msg1, msg2})
	c.Assert(err, IsNil)

	// two THORName msgs with same name is not ok
	err = ad.rejectDuplicateTHORNameMsgs([]cosmos.Msg{msg1, msg3})
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*duplicate THORName operation in same tx.*")

	// three msgs with same name in middle is not ok
	err = ad.rejectDuplicateTHORNameMsgs([]cosmos.Msg{msg1, msg2, msg3})
	c.Assert(err, NotNil)
}

func (s *AnteTestSuite) TestAnteHandleMessage(c *C) {
	ctx, k := setupKeeperForTest(c)
	version := GetCurrentVersion()

	ad := AnteDecorator{
		keeper: k,
	}

	fromAddr := GetRandomBech32Addr()
	toAddr := GetRandomBech32Addr()

	// fund an addr so it can pass the fee deduction ante
	FundAccount(c, ctx, k, fromAddr, 200*common.One)
	coin, err := common.NewCoin(common.RuneNative, cosmos.NewUint(1*common.One)).Native()
	c.Assert(err, IsNil)

	goodMsg := types.MsgSend{
		FromAddress: fromAddr,
		ToAddress:   toAddr,
		Amount:      cosmos.NewCoins(coin),
	}
	newCtx, err := ad.anteHandleMessage(ctx, version, &goodMsg)
	c.Assert(err, IsNil)
	c.Assert(newCtx.Priority(), Equals, int64(0))

	// bank sends are allowed
	bankSendMsg := banktypes.MsgSend{
		FromAddress: fromAddr.String(),
		ToAddress:   toAddr.String(),
	}
	k.SetMimir(ctx, "BankSendEnabled", 1)
	_, err = ad.anteHandleMessage(ctx, version, &bankSendMsg)
	c.Assert(err, IsNil)

	// other non-thorchain msgs should be rejected
	badMsg := banktypes.MsgMultiSend{}
	_, err = ad.anteHandleMessage(ctx, version, &badMsg)
	c.Assert(err, NotNil)

	// authz msgs must be rejected - the module is unwired, and MsgExec in
	// particular would dispatch its inner msgs straight to the msg service
	// router, bypassing every ante check in this switch
	for _, m := range []cosmos.Msg{&authz.MsgGrant{}, &authz.MsgRevoke{}, &authz.MsgExec{}} {
		_, err = ad.anteHandleMessage(ctx, version, m)
		c.Assert(err, NotNil)
	}

	activeNodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, activeNodeAccount), IsNil)

	// Node-signed msgs should have priority
	priorityMsg := types.MsgMimir{
		Key:    "",
		Value:  0,
		Signer: activeNodeAccount.NodeAddress,
	}
	newCtx, err = ad.anteHandleMessage(ctx, version, &priorityMsg)
	c.Assert(err, IsNil)
	c.Assert(newCtx.Priority(), Equals, int64(math.MaxInt64))
}

// fakeSigVerifiableTx is a minimal tx implementation for exercising the ante
// decorator, which needs the signers and memo of the transaction body.
type fakeSigVerifiableTx struct {
	sdk.Tx

	msgs    []cosmos.Msg
	signers [][]byte
	memo    string
}

func (t fakeSigVerifiableTx) GetMsgs() []cosmos.Msg                     { return t.msgs }
func (t fakeSigVerifiableTx) GetSigners() ([][]byte, error)             { return t.signers, nil }
func (t fakeSigVerifiableTx) GetPubKeys() ([]cryptotypes.PubKey, error) { return nil, nil }
func (t fakeSigVerifiableTx) GetMemo() string                           { return t.memo }

func (t fakeSigVerifiableTx) GetSignaturesV2() ([]signing.SignatureV2, error) {
	return nil, nil
}

func (s *AnteTestSuite) TestAnteHandleRecordsTxSigners(c *C) {
	ctx, k := setupKeeperForTest(c)
	ad := AnteDecorator{keeper: k}

	nodeAccount := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	signer := GetRandomBech32Addr()
	other := GetRandomBech32Addr()

	tx := fakeSigVerifiableTx{
		msgs:    []cosmos.Msg{&types.MsgMimir{Signer: nodeAccount.NodeAddress}},
		signers: [][]byte{signer},
		memo:    "SWAP:BTC.BTC",
	}

	reached := false
	_, err := ad.AnteHandle(ctx, tx, false, func(innerCtx cosmos.Context, _ sdk.Tx, _ bool) (cosmos.Context, error) {
		reached = true
		// the sender of a msg dispatched on behalf of another account is not a
		// signer of the tx body, and so cannot inherit what it authorises
		c.Assert(TxSignedBy(innerCtx, signer), Equals, true)
		c.Assert(TxSignedBy(innerCtx, other), Equals, false)
		return innerCtx, nil
	})
	c.Assert(err, IsNil)
	c.Assert(reached, Equals, true)

	// without a recorded signer set there is nothing to check against
	c.Assert(TxSignedBy(ctx, other), Equals, true)
}

func (s *AnteTestSuite) TestAnteHandleRejectsAuthzMsgs(c *C) {
	ctx, k := setupKeeperForTest(c)
	ad := AnteDecorator{keeper: k}

	// authz is unwired, and MsgExec would dispatch its inner msgs straight to
	// the msg service router, bypassing every check in the ante handler
	for _, m := range []cosmos.Msg{&authz.MsgGrant{}, &authz.MsgRevoke{}, &authz.MsgExec{}} {
		tx := fakeSigVerifiableTx{
			msgs:    []cosmos.Msg{m},
			signers: [][]byte{GetRandomBech32Addr()},
		}
		reached := false
		_, err := ad.AnteHandle(ctx, tx, false, func(innerCtx cosmos.Context, _ sdk.Tx, _ bool) (cosmos.Context, error) {
			reached = true
			return innerCtx, nil
		})
		c.Assert(err, NotNil)
		c.Assert(reached, Equals, false)
	}
}
