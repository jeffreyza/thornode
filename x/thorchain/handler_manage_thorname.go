package thorchain

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var IsValidTHORName = regexp.MustCompile(`^[a-zA-Z0-9+_-]+$`).MatchString

// ManageTHORNameHandler a handler to process MsgNetworkFee messages
type ManageTHORNameHandler struct {
	mgr Manager
}

// NewManageTHORNameHandler create a new instance of network fee handler
func NewManageTHORNameHandler(mgr Manager) ManageTHORNameHandler {
	return ManageTHORNameHandler{mgr: mgr}
}

// Run is the main entry point for network fee logic
func (h ManageTHORNameHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgManageTHORName)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgManageTHORName failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgManageTHORName", "error", err)
	}
	return result, err
}

func (h ManageTHORNameHandler) validateName(n string) error {
	// validate THORName
	if len(n) > 30 {
		return errors.New("THORName cannot exceed 30 characters")
	}
	if !IsValidTHORName(n) {
		return errors.New("invalid THORName")
	}
	return nil
}

func (h ManageTHORNameHandler) validate(ctx cosmos.Context, msg MsgManageTHORName) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// TODO on hard fork move network check to ValidateBasic
	if !msg.Address.IsCurrentNetwork(msg.Address.GetChain()) {
		return fmt.Errorf("address(%s) is not same network", msg.Address)
	}

	exists := h.mgr.Keeper().THORNameExists(ctx, msg.Name)
	newOwner := msg.Owner

	if !exists {
		if newOwner.Empty() {
			newOwner = msg.Signer
		}
		// thorname doesn't appear to exist, let's validate the name
		if err := h.validateName(msg.Name); err != nil {
			return err
		}
		registrationFee := h.mgr.Keeper().GetTHORNameRegisterFee(ctx)
		if msg.Coin.Amount.LTE(registrationFee) {
			return fmt.Errorf("not enough funds")
		}
	} else {
		name, err := h.mgr.Keeper().GetTHORName(ctx, msg.Name)
		if err != nil {
			return err
		}

		// if this thorname is already owned, check signer has ownership. If
		// expiration is past, allow different user to take ownership
		if !name.Owner.Equals(msg.Signer) && ctx.BlockHeight() <= name.ExpireBlockHeight {
			ctx.Logger().Error("no authorization", "owner", name.Owner)
			return fmt.Errorf("no authorization: owned by %s", name.Owner)
		}

		// ensure user isn't inflating their expire block height artificaially
		if name.ExpireBlockHeight < msg.ExpireBlockHeight {
			return errors.New("cannot artificially inflate expire block height")
		}
		if newOwner.Equals(name.Owner) {
			newOwner = nil
		}
	}
	// A module cannot receive ownership: late collector earnings could prevent
	// re-registration after expiry because settlement cannot pay a module owner.
	if !newOwner.Empty() {
		if err := validateAffiliateCollectorOwner(ctx, h.mgr.Keeper(), newOwner); err != nil {
			return fmt.Errorf("invalid THORName owner: %w", err)
		}
	}

	// validate preferred asset pool exists and is active
	// RUNE is allowed as a sentinel to explicitly clear the preferred asset
	if !msg.PreferredAsset.IsEmpty() && !msg.PreferredAsset.IsRune() {
		if !h.mgr.Keeper().PoolExist(ctx, msg.PreferredAsset) {
			return fmt.Errorf("pool %s does not exist", msg.PreferredAsset)
		}
		pool, err := h.mgr.Keeper().GetPool(ctx, msg.PreferredAsset)
		if err != nil {
			return err
		}
		if pool.Status != PoolAvailable {
			return fmt.Errorf("pool %s is not available", msg.PreferredAsset)
		}
	}

	return nil
}

// handle process MsgManageTHORName
func (h ManageTHORNameHandler) handle(ctx cosmos.Context, msg MsgManageTHORName) (*cosmos.Result, error) {
	var err error

	enable, _ := h.mgr.Keeper().GetMimir(ctx, "THORNames")
	if enable == 0 {
		return nil, fmt.Errorf("THORNames are currently disabled")
	}

	tn := THORName{Name: msg.Name, Owner: msg.Signer, PreferredAsset: common.EmptyAsset}
	exists := h.mgr.Keeper().THORNameExists(ctx, msg.Name)
	if exists {
		tn, err = h.mgr.Keeper().GetTHORName(ctx, msg.Name)
		if err != nil {
			return nil, err
		}
	}
	previous := tn
	if !exists {
		// GetTHORName hides expired ownership, but re-registration must still
		// pay the available collector balance to the previous owner.
		previous, err = h.mgr.Keeper().GetTHORNameRecord(ctx, msg.Name)
		if err != nil {
			return nil, err
		}
	}
	// Settle the previous owner's available fees before transfer or re-registration
	// after expiry. NewInternalHandler commits the payout and ownership together.
	if !previous.Owner.Empty() && (!exists || (!msg.Owner.Empty() && !msg.Owner.Equals(previous.Owner))) {
		collector, collectorErr := h.mgr.Keeper().GetAffiliateCollector(ctx, previous.Name)
		if collectorErr != nil {
			return nil, collectorErr
		}
		if err = settleTHORNameCollector(ctx, h.mgr.Keeper(), collector, previous.Owner); err != nil {
			return nil, err
		}
	}

	registrationFeePaid := cosmos.ZeroUint()
	fundPaid := cosmos.ZeroUint()

	// check if user is trying to extend expiration
	if !msg.Coin.Amount.IsZero() {
		// check that THORName is still valid, can't top up an invalid THORName
		if err = h.validateName(msg.Name); err != nil {
			return nil, err
		}
		var addBlocks int64
		// registration fee is for THORChain addresses only
		if !exists {
			// minus registration fee
			registrationFee := h.mgr.Keeper().GetTHORNameRegisterFee(ctx)
			msg.Coin.Amount = common.SafeSub(msg.Coin.Amount, registrationFee)
			registrationFeePaid = registrationFee
			addBlocks = h.mgr.GetConstants().GetInt64Value(constants.BlocksPerYear) // registration comes with 1 free year
		}
		feePerBlock := h.mgr.Keeper().GetTHORNamePerBlockFee(ctx)
		// Validate feePerBlock is not zero to prevent division by zero
		if feePerBlock.IsZero() {
			return nil, errors.New("per block fee cannot be zero")
		}
		fundPaid = msg.Coin.Amount
		// Validate msg.Coin.Amount doesn't exceed int64 max to prevent overflow during conversion
		if msg.Coin.Amount.GT(cosmos.NewUint(math.MaxInt64)) {
			return nil, errors.New("coin amount exceeds maximum allowed value")
		}
		// Validate feePerBlock doesn't exceed int64 max to prevent overflow during conversion
		if feePerBlock.GT(cosmos.NewUint(math.MaxInt64)) {
			return nil, errors.New("per block fee exceeds maximum allowed value")
		}
		blocksFromFund := int64(msg.Coin.Amount.Uint64()) / int64(feePerBlock.Uint64())
		// Check for overflow before adding blocks
		if addBlocks > 0 && blocksFromFund > math.MaxInt64-addBlocks {
			return nil, errors.New("block calculation overflow: addBlocks")
		}
		addBlocks += blocksFromFund
		// Determine base block height for expiration
		baseHeight := tn.ExpireBlockHeight
		if baseHeight < ctx.BlockHeight() {
			baseHeight = ctx.BlockHeight()
		}
		// Check for overflow when computing new expiration
		if addBlocks > 0 && baseHeight > math.MaxInt64-addBlocks {
			return nil, errors.New("block calculation overflow: expire block height")
		}
		tn.ExpireBlockHeight = baseHeight + addBlocks
	}

	// check if we need to reduce the expire time, upon user request
	if msg.ExpireBlockHeight > 0 && msg.ExpireBlockHeight < tn.ExpireBlockHeight {
		tn.ExpireBlockHeight = msg.ExpireBlockHeight
	}

	// check if we need to update the preferred asset
	// RUNE is used as a sentinel to explicitly clear the preferred asset
	if !msg.PreferredAsset.IsEmpty() {
		if msg.PreferredAsset.IsRune() {
			tn.PreferredAsset = common.EmptyAsset
		} else if !tn.PreferredAsset.Equals(msg.PreferredAsset) {
			tn.PreferredAsset = msg.PreferredAsset
		}
	}

	// check if we need to update the preferred asset outbound fee multiplier
	// -1 = not provided (no-op), 0 = reset to global default, >= 1 = custom value
	if msg.PreferredAssetOutboundFeeMultiplier >= 0 {
		tn.PreferredAssetOutboundFeeMultiplier = msg.PreferredAssetOutboundFeeMultiplier
	}

	tn.SetAlias(msg.Chain, msg.Address) // update address
	// Update owner if it has changed. Intentionally placed after the field updates above
	// so that ownership transfer unconditionally resets PreferredAsset, Aliases, and
	// PreferredAssetOutboundFeeMultiplier — ensuring the new owner starts with a clean slate.
	if !msg.Owner.Empty() && !bytes.Equal(msg.Owner, tn.Owner) {
		tn.Owner = msg.Owner
		tn.PreferredAsset = common.EmptyAsset
		tn.PreferredAssetOutboundFeeMultiplier = 0
		tn.Aliases = []types.THORNameAlias{}
	}
	h.mgr.Keeper().SetTHORName(ctx, tn)

	evt := NewEventTHORName(tn.Name, msg.Chain, msg.Address, registrationFeePaid, fundPaid, tn.ExpireBlockHeight, tn.Owner)
	if err = h.mgr.EventMgr().EmitEvent(ctx, evt); nil != err {
		ctx.Logger().Error("fail to emit THORName event", "error", err)
	}

	return &cosmos.Result{}, nil
}
