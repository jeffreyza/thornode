package keeper_test

import (
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"gitlab.com/thorchain/thornode/v3/x/denom/types"
)

func (s *KeeperTestSuite) TestMsgServerCreateDenom() {
	s.SetupTest()

	resp, err := s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[0].String(),
			Id:     "foo",
		},
	)
	s.Require().NoError(err)
	denom, err := types.GetTokenDenom("foo")
	s.Require().NoError(err)
	s.Require().Equal(resp.NewTokenDenom, denom)

	queryRes, err := s.queryClient.DenomAdmin(s.ctx,
		&types.QueryDenomAdminRequest{
			Denom: denom,
		})
	s.Require().NoError(err)
	s.Require().Equal(
		accAddrs[0].String(),
		queryRes.Admin,
	)
}

func (s *KeeperTestSuite) TestMsgServerMintTokens() {
	s.SetupTest()
	_, err := s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[0].String(),
			Id:     "foo",
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[0].String(),
			Id:     "bar",
		},
	)
	s.Require().NoError(err)

	// Incorrect Admin
	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[1].String(),
			Amount:    sdk.NewCoin("x/bar", math.NewInt(100)),
			Recipient: accAddrs[1].String(),
		},
	)
	s.Require().Error(err)

	// Invalid Denom
	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[0].String(),
			Amount:    sdk.NewCoin("foo", math.NewInt(100)),
			Recipient: accAddrs[1].String(),
		},
	)
	s.Require().Error(err)

	// Correct
	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[0].String(),
			Amount:    sdk.NewCoin("x/foo", math.NewInt(100)),
			Recipient: accAddrs[1].String(),
		},
	)
	s.Require().NoError(err)

	res, err := s.bankKeeper.Balance(s.ctx, &banktypes.QueryBalanceRequest{
		Address: accAddrs[1].String(),
		Denom:   "x/foo",
	})
	s.Require().NoError(err)
	s.Require().Equal(res.Balance.Amount, math.NewInt(100))
}

func (s *KeeperTestSuite) TestMsgServerBurnTokens() {
	s.SetupTest()
	_, err := s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[0].String(),
			Id:     "foo",
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[0].String(),
			Amount:    sdk.NewCoin("x/foo", math.NewInt(100)),
			Recipient: accAddrs[0].String(),
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[0].String(),
			Amount:    sdk.NewCoin("x/foo", math.NewInt(100)),
			Recipient: accAddrs[1].String(),
		},
	)
	s.Require().NoError(err)

	// Incorrect Admin
	_, err = s.msgServer.BurnTokens(
		s.ctx,
		&types.MsgBurnTokens{
			Sender: accAddrs[1].String(),
			Amount: sdk.NewCoin("x/bar", math.NewInt(100)),
		},
	)
	s.Require().Error(err)

	// Invalid Denom
	_, err = s.msgServer.BurnTokens(
		s.ctx,
		&types.MsgBurnTokens{
			Sender: accAddrs[0].String(),
			Amount: sdk.NewCoin("foo", math.NewInt(100)),
		},
	)
	s.Require().Error(err)

	// Correct
	_, err = s.msgServer.BurnTokens(
		s.ctx,
		&types.MsgBurnTokens{
			Sender: accAddrs[0].String(),
			Amount: sdk.NewCoin("x/foo", math.NewInt(50)),
		},
	)
	s.Require().NoError(err)

	res, err := s.bankKeeper.Balance(s.ctx, &banktypes.QueryBalanceRequest{
		Address: accAddrs[0].String(),
		Denom:   "x/foo",
	})
	s.Require().NoError(err)
	s.Require().Equal(res.Balance.Amount, math.NewInt(50))

	res, err = s.bankKeeper.Balance(s.ctx, &banktypes.QueryBalanceRequest{
		Address: accAddrs[1].String(),
		Denom:   "x/foo",
	})
	s.Require().NoError(err)
	s.Require().Equal(res.Balance.Amount, math.NewInt(100))
}

func (s *KeeperTestSuite) TestMsgServerChangeDenomAdmin() {
	s.SetupTest()
	_, err := s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[0].String(),
			Id:     "foo",
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.CreateDenom(
		s.ctx,
		&types.MsgCreateDenom{
			Sender: accAddrs[1].String(),
			Id:     "bar",
		},
	)
	s.Require().NoError(err)

	// Incorrect Admin
	_, err = s.msgServer.ChangeDenomAdmin(
		s.ctx,
		&types.MsgChangeDenomAdmin{
			Sender:   accAddrs[0].String(),
			Denom:    "x/bar",
			NewAdmin: accAddrs[0].String(),
		},
	)
	s.Require().Error(err)

	// Correct Admin
	_, err = s.msgServer.ChangeDenomAdmin(
		s.ctx,
		&types.MsgChangeDenomAdmin{
			Sender:   accAddrs[0].String(),
			Denom:    "x/foo",
			NewAdmin: accAddrs[1].String(),
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[1].String(),
			Amount:    sdk.NewCoin("x/foo", math.NewInt(100)),
			Recipient: accAddrs[1].String(),
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.BurnTokens(
		s.ctx,
		&types.MsgBurnTokens{
			Sender: accAddrs[1].String(),
			Amount: sdk.NewCoin("x/foo", math.NewInt(50)),
		},
	)
	s.Require().NoError(err)

	// Clear Admin
	_, err = s.msgServer.ChangeDenomAdmin(
		s.ctx,
		&types.MsgChangeDenomAdmin{
			Sender: accAddrs[1].String(),
			Denom:  "x/foo",
		},
	)
	s.Require().NoError(err)

	_, err = s.msgServer.MintTokens(
		s.ctx,
		&types.MsgMintTokens{
			Sender:    accAddrs[1].String(),
			Amount:    sdk.NewCoin("x/foo", math.NewInt(100)),
			Recipient: accAddrs[1].String(),
		},
	)
	s.Require().Error(err)
}

// TestMsgServerRejectsNonContractSender asserts the x/denom permission boundary
// is enforced by the Msg server itself, not only by the ante handler: an
// externally owned account cannot create denoms or manage their supply, even if
// its message reaches the Msg service without passing through the ante handler.
func (s *KeeperTestSuite) TestMsgServerRejectsNonContractSender() {
	s.SetupTest()

	// a contract creates a denom and mints into the EOA
	_, err := s.msgServer.CreateDenom(s.ctx, &types.MsgCreateDenom{
		Sender: accAddrs[0].String(),
		Id:     "foo",
	})
	s.Require().NoError(err)
	denom, err := types.GetTokenDenom("foo")
	s.Require().NoError(err)

	// the EOA cannot create a denom of its own
	_, err = s.msgServer.CreateDenom(s.ctx, &types.MsgCreateDenom{
		Sender: eoaAddr.String(),
		Id:     "bar",
	})
	s.Require().ErrorIs(err, types.ErrUnauthorized)
	barAdmin, err := s.keeper.GetAdmin(sdk.UnwrapSDKContext(s.ctx), "x/bar")
	s.Require().NoError(err)
	s.Require().Nil(barAdmin)

	// nor mint, burn or reassign the admin of an existing one
	_, err = s.msgServer.MintTokens(s.ctx, &types.MsgMintTokens{
		Sender:    eoaAddr.String(),
		Amount:    sdk.NewCoin(denom, math.NewInt(100)),
		Recipient: eoaAddr.String(),
	})
	s.Require().ErrorIs(err, types.ErrUnauthorized)

	_, err = s.msgServer.BurnTokens(s.ctx, &types.MsgBurnTokens{
		Sender: eoaAddr.String(),
		Amount: sdk.NewCoin(denom, math.NewInt(100)),
	})
	s.Require().ErrorIs(err, types.ErrUnauthorized)

	_, err = s.msgServer.ChangeDenomAdmin(s.ctx, &types.MsgChangeDenomAdmin{
		Sender:   eoaAddr.String(),
		Denom:    denom,
		NewAdmin: eoaAddr.String(),
	})
	s.Require().ErrorIs(err, types.ErrUnauthorized)

	admin, err := s.keeper.GetAdmin(sdk.UnwrapSDKContext(s.ctx), denom)
	s.Require().NoError(err)
	s.Require().Equal(accAddrs[0].String(), admin.String())
}
