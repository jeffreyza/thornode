package keeper

import (
	"context"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/x/denom/types"
)

type msgServer struct {
	Keeper
}

// NewMsgServerImpl returns an implementation of the MsgServer interface
// for the provided Keeper.
func NewMsgServerImpl(keeper Keeper) types.MsgServer {
	return &msgServer{Keeper: keeper}
}

var _ types.MsgServer = msgServer{}

// assertContractSender enforces the x/denom permission boundary: tokens may
// only be created and their supply managed by approved CosmWasm contracts.
//
// The ante handler rejects x/denom messages submitted directly in a
// transaction, but it only inspects the top level messages of that
// transaction. Any message router that dispatches an inner message on behalf
// of its signer would reach the Msg service without passing through the ante
// handler at all, so the restriction is repeated here, where every dispatch
// path converges.
func (server msgServer) assertContractSender(ctx sdk.Context, sender string) error {
	addr, err := sdk.AccAddressFromBech32(sender)
	if err != nil {
		return err
	}
	if server.Keeper.contractKeeper == nil {
		return errors.Wrap(types.ErrUnauthorized, "no contract keeper configured")
	}
	if !server.Keeper.contractKeeper.HasContractInfo(ctx, addr) {
		return errors.Wrapf(types.ErrUnauthorized, "sender %s is not a contract", sender)
	}
	return nil
}

func (server msgServer) CreateDenom(goCtx context.Context, msg *types.MsgCreateDenom) (*types.MsgCreateDenomResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := server.assertContractSender(ctx, msg.Sender); err != nil {
		return nil, err
	}

	denom, err := server.Keeper.CreateDenom(ctx, msg.Id, msg.Sender)
	if err != nil {
		return nil, err
	}
	_, found := server.bankKeeper.GetDenomMetaData(ctx, denom)
	if found {
		return nil, types.ErrDenomExists
	}

	metadata := msg.GetMetadata()
	metadata.Base = denom
	server.bankKeeper.SetDenomMetaData(ctx, metadata)

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventCreateDenom,
			sdk.NewAttribute(types.AttributeCreator, msg.Sender),
			sdk.NewAttribute(types.AttributeNewTokenDenom, denom),
		),
	})

	return &types.MsgCreateDenomResponse{
		NewTokenDenom: denom,
	}, nil
}

func (server msgServer) MintTokens(goCtx context.Context, msg *types.MsgMintTokens) (*types.MsgMintTokensResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := server.assertContractSender(ctx, msg.Sender); err != nil {
		return nil, err
	}

	admin, err := server.Keeper.GetAdmin(ctx, msg.Amount.GetDenom())
	if err != nil {
		return nil, err
	}

	if msg.Sender != admin.String() {
		return nil, types.ErrUnauthorized
	}

	err = server.Keeper.mintTo(ctx, msg.Amount, msg.Recipient)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventMintTokens,
			sdk.NewAttribute(types.AttributeMintToAddress, msg.Sender),
			sdk.NewAttribute(types.AttributeAmount, msg.Amount.String()),
		),
	})

	return &types.MsgMintTokensResponse{}, nil
}

func (server msgServer) BurnTokens(goCtx context.Context, msg *types.MsgBurnTokens) (*types.MsgBurnTokensResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := server.assertContractSender(ctx, msg.Sender); err != nil {
		return nil, err
	}

	admin, err := server.Keeper.GetAdmin(ctx, msg.Amount.GetDenom())
	if err != nil {
		return nil, err
	}

	if msg.Sender != admin.String() {
		return nil, types.ErrUnauthorized
	}

	err = server.Keeper.burnFrom(ctx, msg.Amount, msg.Sender)
	if err != nil {
		return nil, err
	}

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventBurnTokens,
			sdk.NewAttribute(types.AttributeBurnFromAddress, msg.Sender),
			sdk.NewAttribute(types.AttributeAmount, msg.Amount.String()),
		),
	})

	return &types.MsgBurnTokensResponse{}, nil
}

func (server msgServer) ChangeDenomAdmin(goCtx context.Context, msg *types.MsgChangeDenomAdmin) (*types.MsgChangeDenomAdminResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	if err := server.assertContractSender(ctx, msg.Sender); err != nil {
		return nil, err
	}

	admin, err := server.Keeper.GetAdmin(ctx, msg.Denom)
	if err != nil {
		return nil, err
	}

	if msg.Sender != admin.String() {
		return nil, types.ErrUnauthorized
	}

	if msg.NewAdmin == "" {
		err = server.Keeper.SetAdmin(ctx, msg.Denom, nil)
		if err != nil {
			return nil, err
		}
	} else {
		admin, err = sdk.AccAddressFromBech32(msg.NewAdmin)
		if err != nil {
			return nil, err
		}
		err = server.Keeper.SetAdmin(ctx, msg.Denom, &admin)
		if err != nil {
			return nil, err
		}
	}

	ctx.EventManager().EmitEvents(sdk.Events{
		sdk.NewEvent(
			types.EventChangeDenomAdmin,
			sdk.NewAttribute(types.AttributeDenom, msg.GetDenom()),
			sdk.NewAttribute(types.AttributeNewAdmin, msg.NewAdmin),
		),
	})

	return &types.MsgChangeDenomAdminResponse{}, nil
}
