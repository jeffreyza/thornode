package thorchain

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// AddLiquidityHandler is to handle add liquidity
type AddLiquidityHandler struct {
	mgr Manager
}

// NewAddLiquidityHandler create a new instance of AddLiquidityHandler
func NewAddLiquidityHandler(mgr Manager) AddLiquidityHandler {
	return AddLiquidityHandler{
		mgr: mgr,
	}
}

// Run execute the handler
func (h AddLiquidityHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgAddLiquidity)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("received add liquidity request",
		"asset", msg.Asset.String(),
		"tx", msg.Tx)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("msg add liquidity fail validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process msg add liquidity", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h AddLiquidityHandler) validate(ctx cosmos.Context, msg MsgAddLiquidity) error {
	if !msg.Tx.ID.IsBlank() { // don't validate tx if internal txn
		if err := msg.ValidateBasic(); err != nil {
			ctx.Logger().Error("msg add liquidity fail validation", "error", err)
			return errAddLiquidityFailValidation
		}
	}

	if msg.Asset.IsTradeAsset() {
		return fmt.Errorf("asset cannot be a trade asset")
	}

	// TODO on hard fork move network check to ValidateBasic
	if !msg.AssetAddress.IsEmpty() {
		if !msg.AssetAddress.IsCurrentNetwork(msg.AssetAddress.GetChain()) {
			return fmt.Errorf("address(%s) is not same network", msg.AssetAddress)
		}
	}

	if h.mgr.Keeper().IsRagnarok(ctx, []common.Asset{msg.Asset}) {
		return fmt.Errorf("cannot add liquidity to Ragnaroked pool (%s)", msg.Asset.String())
	}

	// Note that GetChain() without GetLayer1Asset() would indicate THORChain for synthetic assets.
	gasAsset := msg.Asset.GetLayer1Asset().GetChain().GetGasAsset()
	// Even if a destination gas asset pool is empty, the first add liquidity has to be symmetrical,
	// and so there is no need to check at this stage for whether the addition is of RUNE or Asset or with needsSwap.
	if !msg.Asset.Equals(gasAsset) && !msg.Asset.IsTCY() && !msg.Asset.IsWhitelisted() {
		gasPool, err := h.mgr.Keeper().GetPool(ctx, gasAsset)
		// Note that for a synthetic asset msg.Asset.Chain (unlike msg.Asset.GetChain())
		// is intentionally used to be the external chain rather than THOR.
		// Any destination asset starting with THOR should be rejected for no THOR.RUNE
		// gas asset pool existing.
		if err != nil {
			return ErrInternal(err, "fail to get gas pool")
		}
		// Note that NewPool from GetPool would return a pool with status;
		// use IsEmpty to check for prior existence.
		if gasPool.IsEmpty() {
			return fmt.Errorf("asset (%s)'s gas asset pool (%s) does not exist yet", msg.Asset.String(), gasAsset.String())
		}
	}

	if msg.Asset.IsDerivedAsset() {
		return fmt.Errorf("asset cannot be a derived asset")
	}

	// Check if user is trying to deposit into savers
	// At present, savers are authorized for:
	// * layer 1 assets  (BTC, ETH, etc.)
	// * stable pools (as determined by TOR anchor pool settings)
	if msg.Asset.IsSyntheticAsset() {
		// Check if the asset is in the anchor pools
		isAnchorAsset := false
		for _, asset := range h.mgr.Keeper().GetAnchors(ctx, common.TOR) {
			if msg.Asset.GetLayer1Asset().Equals(asset) {
				isAnchorAsset = true
				break
			}
		}
		// Saver asset must be either L1 gas asset or isAnchorAsset
		if !msg.Asset.GetLayer1Asset().IsGasAsset() && !isAnchorAsset {
			return fmt.Errorf("asset must be a gas asset or TOR anchor pool")
		}
		// Ensure that the message is being sent from an address on a chain that matches the L1 asset chain
		if !msg.AssetAddress.IsChain(msg.Asset.GetLayer1Asset().GetChain()) {
			return fmt.Errorf("asset address must be layer1 chain")
		}
		// Savers vaults are not currently enabled for RUNE
		if !msg.RuneAmount.IsZero() {
			return fmt.Errorf("cannot deposit rune into a vault")
		}
	}

	if !msg.RuneAddress.IsEmpty() && !msg.RuneAddress.IsChain(common.THORChain) {
		ctx.Logger().Error("rune address must be THORChain")
		return errAddLiquidityFailValidation
	}

	// For secured assets, verify signer is authorized to withdraw from runeAddr
	// This prevents attackers from specifying a victim's address and draining their secured assets
	if msg.Asset.IsSecuredAsset() && !msg.AssetAmount.IsZero() {
		runeAccAddr, err := msg.RuneAddress.AccAddress()
		if err != nil {
			return fmt.Errorf("invalid rune address for secured asset: %w", err)
		}
		if !runeAccAddr.Equals(msg.Signer) {
			return fmt.Errorf("signer %s not authorized to withdraw secured assets from %s", msg.Signer, msg.RuneAddress)
		}
	}

	assetSender := common.ResolveObservedSender(msg.Asset.GetLayer1Asset().GetChain(), msg.AssetAddress)
	txSender := msg.Tx.ObservedSender()
	isUnknownSenderObserved := assetSender.RefundAddressRequired || txSender.RefundAddressRequired

	if !msg.AssetAddress.IsEmpty() {
		// If the needsSwap check disallows a cross-chain AssetAddress,
		// a position with pending RUNE cannot be completed with Asset,
		// so fail validation here if the AssetAddress chain is different from the Asset's.
		if msg.Asset.IsSecuredAsset() {
			// set thor1... address as lp for secured assets
			if !msg.AssetAddress.IsChain(common.THORChain) {
				return errAddLiquidityMismatchAddr
			}
		} else if !isUnknownSenderObserved && !msg.AssetAddress.IsChain(msg.Asset.GetLayer1Asset().GetChain()) {
			return errAddLiquidityMismatchAddr
		}

		polAddress, err := h.mgr.Keeper().GetModuleAddress(ReserveName)
		if err != nil {
			return err
		}
		if msg.RuneAddress.Equals(polAddress) {
			return fmt.Errorf("pol lp cannot have asset address")
		}
	}

	// check if swap meets standards
	if h.needsSwap(msg) {
		if !msg.Asset.IsSyntheticAsset() {
			return fmt.Errorf("swap & add liquidity is only available for synthetic pools")
		}
		if !msg.Asset.GetLayer1Asset().Equals(msg.Tx.Coins[0].Asset) {
			return fmt.Errorf("deposit asset must be the layer1 equivalent for the synthetic asset")
		}
	}

	pool, err := h.mgr.Keeper().GetPool(ctx, msg.Asset)
	if err != nil {
		return ErrInternal(err, "fail to get pool")
	}
	if err = pool.EnsureValidPoolStatus(&msg); err != nil {
		ctx.Logger().Error("fail to check pool status", "error", err)
		return errInvalidPoolStatus
	}

	if h.mgr.Keeper().IsChainHalted(ctx, msg.Asset.Chain) || h.mgr.Keeper().IsLPPaused(ctx, msg.Asset.Chain) {
		return fmt.Errorf("unable to add liquidity while chain has paused LP actions")
	}
	if h.mgr.Keeper().IsPoolDepositPaused(ctx, msg.Asset) {
		return fmt.Errorf("unable to add liquidity, deposits are paused for asset (%s)", msg.Asset.String())
	}

	ensureLiquidityNoLargerThanBond := h.mgr.GetConstants().GetBoolValue(constants.StrictBondLiquidityRatio)
	// if the pool is THORChain no need to check economic security
	if msg.Asset.IsSyntheticAsset() || !ensureLiquidityNoLargerThanBond {
		return nil
	}

	// the following is only applicable for mainnet
	totalLiquidityRUNE, err := h.getTotalLiquidityRUNE(ctx)
	if err != nil {
		return ErrInternal(err, "fail to get total liquidity RUNE")
	}

	// total liquidity RUNE after current add liquidity
	totalLiquidityRUNE = totalLiquidityRUNE.Add(msg.RuneAmount)
	totalLiquidityRUNE = totalLiquidityRUNE.Add(pool.AssetValueInRune(msg.AssetAmount))
	maximumLiquidityRune := h.mgr.Keeper().GetConfigInt64(ctx, constants.MaximumLiquidityRune)
	if maximumLiquidityRune > 0 {
		if totalLiquidityRUNE.GT(cosmos.NewUint(uint64(maximumLiquidityRune))) {
			return errAddLiquidityRUNEOverLimit
		}
	}

	coins := common.NewCoins(
		common.NewCoin(common.RuneAsset(), msg.RuneAmount),
		common.NewCoin(msg.Asset, msg.AssetAmount),
	)
	if atTVLCap(ctx, coins, h.mgr) {
		return errAddLiquidityRUNEMoreThanBond
	}

	return nil
}

func (h AddLiquidityHandler) handle(ctx cosmos.Context, msg MsgAddLiquidity) error {
	// check if we need to swap before adding asset
	if h.needsSwap(msg) {
		return h.swap(ctx, msg)
	}

	assetAddress := msg.AssetAddress
	assetSender := common.ResolveObservedSender(msg.Asset.GetLayer1Asset().GetChain(), msg.AssetAddress)
	if assetSender.RefundAddressRequired {
		// The inbound observation cannot reliably recover the sender address.
		// Normalize an unknown-sender marker here as a final guard so staged LP
		// reconciliation can reuse the paired address from the RUNE-side memo.
		// Resolve from the address value itself, not the observed tx sender: a
		// memoless XMR add carries a real asset address registered in the memo
		// even though the observed tx sender is the unknown marker.
		assetAddress = assetSender.Address
	}

	// Use L1 pool for secured assets only, don't change behaviour for synths
	asset := msg.Asset
	if asset.IsSecuredAsset() {
		asset = asset.GetLayer1Asset()
	}

	pool, err := h.mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		return ErrInternal(err, "fail to get pool")
	}

	if pool.IsEmpty() {
		ctx.Logger().Info("pool doesn't exist yet, creating a new one...", "symbol", msg.Asset.String(), "creator", msg.RuneAddress)

		pool.Asset = msg.Asset

		defaultPoolStatus := PoolAvailable.String()
		// only set the pool to default pool status if not for gas asset on the chain
		if !pool.Asset.Equals(pool.Asset.GetChain().GetGasAsset()) &&
			!pool.Asset.IsSyntheticAsset() {
			defaultPoolStatus = h.mgr.GetConstants().GetStringValue(constants.DefaultPoolStatus)
		}
		pool.Status = GetPoolStatus(defaultPoolStatus)

		if err = h.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return ErrInternal(err, "fail to save pool to key value store")
		}
	}

	// if the pool decimals hasn't been set, it will still be 0. If we have a
	// pool asset coin, get the decimals from that transaction. This will only
	// set the decimals once.
	if pool.Decimals == 0 {
		coin := msg.GetTx().Coins.GetCoin(pool.Asset)
		if !coin.IsEmpty() && coin.Decimals > 0 {
			pool.Decimals = coin.Decimals
			if err = h.mgr.Keeper().SetPool(ctx, pool); err != nil {
				return ErrInternal(err, "fail to save pool to key value store")
			}
			ctx.Logger().Info("update pool decimals", "asset", msg.Asset, "pool decimals", pool.Decimals)
		}
	}

	// figure out if we need to stage the funds and wait for a follow on
	// transaction to commit all funds atomically. For pools of native assets
	// only, stage is always false
	stage := false
	if !msg.Asset.IsSyntheticAsset() && !msg.Tx.ID.IsBlank() {
		if !msg.AssetAddress.IsEmpty() && msg.AssetAmount.IsZero() {
			stage = true
		}
		if !msg.RuneAddress.IsEmpty() && msg.RuneAmount.IsZero() {
			stage = true
		}
	}

	if msg.AffiliateBasisPoints.IsZero() {
		return h.addLiquidity(
			ctx,
			msg.Asset,
			msg.RuneAmount,
			msg.AssetAmount,
			msg.RuneAddress,
			assetAddress,
			msg.Tx.ID,
			stage,
			h.mgr.GetConstants(),
		)
	}

	// add liquidity has an affiliate fee, add liquidity for both the user and their affiliate
	affiliateRune := common.GetSafeShare(msg.AffiliateBasisPoints, cosmos.NewUint(10000), msg.RuneAmount)
	affiliateAsset := common.GetSafeShare(msg.AffiliateBasisPoints, cosmos.NewUint(10000), msg.AssetAmount)
	userRune := common.SafeSub(msg.RuneAmount, affiliateRune)
	userAsset := common.SafeSub(msg.AssetAmount, affiliateAsset)

	err = h.addLiquidity(
		ctx,
		msg.Asset,
		userRune,
		userAsset,
		msg.RuneAddress,
		assetAddress,
		msg.Tx.ID,
		stage,
		h.mgr.GetConstants(),
	)
	if err != nil {
		return err
	}

	affiliateRuneAddress := common.NoAddress
	affiliateAssetAddress := common.NoAddress
	if msg.AffiliateAddress.IsChain(common.THORChain) {
		affiliateRuneAddress = msg.AffiliateAddress
	} else {
		affiliateAssetAddress = msg.AffiliateAddress
	}

	err = h.addLiquidity(
		ctx,
		msg.Asset,
		affiliateRune,
		affiliateAsset,
		affiliateRuneAddress,
		affiliateAssetAddress,
		msg.Tx.ID,
		false,
		h.mgr.GetConstants(),
	)
	if err != nil {
		ctx.Logger().Error("fail to add liquidity for affiliate", "address", msg.AffiliateAddress, "error", err)
		return err
	}
	return nil
}

func (h AddLiquidityHandler) swap(ctx cosmos.Context, msg MsgAddLiquidity) error {
	// ensure TxID does NOT have a collision with another swap
	if ok := h.mgr.Keeper().HasSwapQueueItem(ctx, msg.Tx.ID, 0); ok {
		return fmt.Errorf("txn hash conflict")
	}

	// sanity check, ensure address or asset doesn't have separator within them
	if strings.Contains(fmt.Sprintf("%s%s", msg.Asset, msg.AffiliateAddress), ":") {
		return fmt.Errorf("illegal character")
	}
	memo := fmt.Sprintf("+:%s::%s:%d", msg.Asset, msg.AffiliateAddress, msg.AffiliateBasisPoints.Uint64())
	msg.Tx.Memo = memo

	// Get streaming swaps interval to use for native -> synth swap
	ssInterval := h.mgr.Keeper().GetConfigInt64(ctx, constants.SaversStreamingSwapsInterval)
	if ssInterval <= 0 {
		ssInterval = 0
	}

	// Determine version based on configuration
	version := types.SwapVersion_v1
	if h.mgr.Keeper().AdvSwapQueueEnabled(ctx) {
		version = types.SwapVersion_v2
	}

	swapMsg := NewMsgSwap(msg.Tx, msg.Asset, common.NoopAddress, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(), "", "", nil, MarketSwap, 0, uint64(ssInterval), version, msg.Signer)

	// sanity check swap msg
	handler := NewSwapHandler(h.mgr)
	if err := handler.validate(ctx, *swapMsg); err != nil {
		return err
	}
	if err := h.mgr.Keeper().SetSwapQueueItem(ctx, *swapMsg, 0); err != nil {
		ctx.Logger().Error("fail to add swap to queue", "error", err)
		return err
	}

	return nil
}

// validateAddLiquidityMessage is to do some validation, and make sure it is legit
func (h AddLiquidityHandler) validateAddLiquidityMessage(ctx cosmos.Context, keeper keeper.Keeper, asset common.Asset, requestTxHash common.TxID, runeAddr, assetAddr common.Address) error {
	if asset.IsEmpty() {
		return errors.New("asset is empty")
	}
	if requestTxHash.IsEmpty() {
		return errors.New("request tx hash is empty")
	}
	if runeAddr.IsEmpty() && assetAddr.IsEmpty() {
		return errors.New("rune address and asset address is empty")
	}
	if !keeper.PoolExist(ctx, asset) {
		return fmt.Errorf("%s doesn't exist", asset)
	}
	pool, err := h.mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get pool(%s)", asset))
	}
	if pool.Status == PoolStaged && (runeAddr.IsEmpty() || assetAddr.IsEmpty()) {
		return fmt.Errorf("cannot add single sided liquidity while a pool is staged")
	}
	return nil
}

// r = rune provided;
// a = asset provided
// R = rune Balance (before)
// A = asset Balance (before)
// P = Pool Units (before)
// units / (P + units) = (1/2) * ((r / (R + r)) + (a / (A + a)))
// units = P * (r*A + a*R + 2*r*a) / (r*A + a*R + 2*R*A)
func calculatePoolUnits(oldPoolUnits, poolRune, poolAsset, addRune, addAsset cosmos.Uint) (cosmos.Uint, cosmos.Uint, error) {
	if addRune.Add(poolRune).IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("total RUNE in the pool is zero")
	}
	if addAsset.Add(poolAsset).IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("total asset in the pool is zero")
	}

	if poolRune.IsZero() || poolAsset.IsZero() || oldPoolUnits.IsZero() {
		sUnits := addRune
		if addRune.IsZero() {
			if !poolAsset.IsZero() {
				// Where possible, keep a RUNE scale for new units.
				if !fitsUint256(new(big.Int).Mul(addAsset.BigInt(), poolRune.BigInt())) {
					return cosmos.ZeroUint(), cosmos.ZeroUint(), errPoolUnitsOverflow
				}
				sUnits = addAsset.Mul(poolRune).Quo(poolAsset)
			} else {
				sUnits = addAsset
			}
		}
		if !fitsUint256(new(big.Int).Add(oldPoolUnits.BigInt(), sUnits.BigInt())) {
			return cosmos.ZeroUint(), cosmos.ZeroUint(), errPoolUnitsOverflow
		}
		return oldPoolUnits.Add(sUnits), sUnits, nil
	}

	// The LegacyDec operations below panic once any intermediate value leaves
	// their range, which in an EndBlock caller (POL) halts the chain. Check
	// every intermediate with exact integer arithmetic first and return an
	// error instead; for inputs that pass, the LegacyDec math below runs
	// unchanged, so results are identical.
	if err := checkPoolUnitsRange(oldPoolUnits, poolRune, poolAsset, addRune, addAsset); err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}

	P := cosmos.NewDecFromBigInt(oldPoolUnits.BigInt())
	R := cosmos.NewDecFromBigInt(poolRune.BigInt())
	A := cosmos.NewDecFromBigInt(poolAsset.BigInt())
	r := cosmos.NewDecFromBigInt(addRune.BigInt())
	a := cosmos.NewDecFromBigInt(addAsset.BigInt())

	// r*A + a*R
	cross := r.Mul(A).Add(a.Mul(R))

	// P * (r*A + a*R + 2*r*a)
	numerator := P.Mul(cross.Add(cosmos.NewDec(2).Mul(r).Mul(a)))
	// r*A + a*R + 2*R*A
	denominator := cross.Add(cosmos.NewDec(2).Mul(A).Mul(R))
	if denominator.IsZero() {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errors.New("denominator of new pool units calculation is zero")
	}

	liquidityUnits := numerator.Quo(denominator)
	// numerator <= 2^256-1 and denominator >= 1, so the quotient is in range,
	// but P + liquidityUnits can still overflow; check it on the exact
	// fixed-point values before the Add.
	if !fitsLegacyDec(new(big.Int).Add(P.BigInt(), liquidityUnits.BigInt())) {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), errPoolUnitsOverflow
	}
	newPoolUnit := P.Add(liquidityUnits)

	pUnits := cosmos.NewUintFromBigInt(newPoolUnit.TruncateInt().BigInt())
	sUnits := cosmos.NewUintFromBigInt(liquidityUnits.TruncateInt().BigInt())

	return pUnits, sUnits, nil
}

// errAddLiquidityPoolOverhang is returned for a liquidity add into a pool in
// synth overhang while the LUVIOverhangGuard mimir is on.
var errAddLiquidityPoolOverhang = errors.New("cannot add liquidity: pool is in synth overhang (synth supply is at least twice the asset depth)")

// isLUVIOverhangGuarded reports whether the LUVIOverhangGuard blocks liquidity
// adds into pool: its synth supply is non-zero and at least twice its asset
// depth (types.LuviState.IsOverhang), and the mimir is above zero. The mimir
// is only read for an overhang pool, so other adds do no extra store reads.
func isLUVIOverhangGuarded(ctx cosmos.Context, k keeper.Keeper, pool Pool, synthSupply cosmos.Uint) bool {
	if synthSupply.IsZero() || !types.NewLuviState(pool, synthSupply).IsOverhang() {
		return false
	}
	return k.GetConfigInt64(ctx, constants.LUVIOverhangGuard) > 0
}

// errPoolUnitsOverflow is returned when a pool unit calculation would leave
// the range of cosmos.Uint or cosmos.LegacyDec (2^256), which would otherwise
// panic.
var errPoolUnitsOverflow = errors.New("pool units calculation overflows")

var (
	// legacyDecPrecision is the LegacyDec fixed-point scale (10^18).
	legacyDecPrecision = new(big.Int).Exp(big.NewInt(10), big.NewInt(sdkmath.LegacyPrecision), nil)
	// legacyDecMaxRaw is the largest raw (scaled) value LegacyDec accepts:
	// 2^256 * 10^18 - 1 (see LegacyDec.IsInValidRange).
	legacyDecMaxRaw = new(big.Int).Sub(
		new(big.Int).Mul(new(big.Int).Lsh(big.NewInt(1), 256), legacyDecPrecision),
		big.NewInt(1),
	)
)

// fitsUint256 reports whether a non-negative integer fits in cosmos.Uint, and
// equally whether it is a whole number LegacyDec can hold: n*10^18 <=
// 2^256*10^18-1 exactly when n < 2^256.
func fitsUint256(n *big.Int) bool {
	return n.Sign() >= 0 && n.BitLen() <= 256
}

// fitsLegacyDec reports whether a raw (already scaled by 10^18) value is
// inside the LegacyDec range.
func fitsLegacyDec(raw *big.Int) bool {
	return raw.CmpAbs(legacyDecMaxRaw) <= 0
}

// checkPoolUnitsRange mirrors every whole-number intermediate of the
// LegacyDec computation in calculatePoolUnits, in the same order, and returns
// errPoolUnitsOverflow if any of them is out of range. Products of whole
// LegacyDecs are exact, so these integers are exactly the values LegacyDec
// range-checks.
func checkPoolUnitsRange(oldPoolUnits, poolRune, poolAsset, addRune, addAsset cosmos.Uint) error {
	P, R, A := oldPoolUnits.BigInt(), poolRune.BigInt(), poolAsset.BigInt()
	r, a := addRune.BigInt(), addAsset.BigInt()
	two := big.NewInt(2)

	rA := new(big.Int).Mul(r, A)
	aR := new(big.Int).Mul(a, R)
	cross := new(big.Int).Add(rA, aR)
	twoR := new(big.Int).Mul(two, r)
	twoRA := new(big.Int).Mul(twoR, a)
	crossRA := new(big.Int).Add(cross, twoRA)
	numerator := new(big.Int).Mul(P, crossRA)
	twoA := new(big.Int).Mul(two, A)
	twoAR := new(big.Int).Mul(twoA, R)
	denominator := new(big.Int).Add(cross, twoAR)

	for _, v := range []*big.Int{P, R, A, r, a, rA, aR, cross, twoR, twoRA, crossRA, numerator, twoA, twoAR, denominator} {
		if !fitsUint256(v) {
			return errPoolUnitsOverflow
		}
	}
	return nil
}

func calculateVaultUnits(oldPoolUnits, poolAmt, addAmt cosmos.Uint) (cosmos.Uint, cosmos.Uint) {
	if oldPoolUnits.IsZero() {
		return addAmt, addAmt
	}
	if poolAmt.IsZero() {
		// Pool has units but no assets - inconsistent state.
		// Return addAmt as liquidityUnits to match first-depositor semantics,
		// but the caller must not add this to existing stale units.
		// Return addAmt as newPoolUnits to signal a reset.
		return addAmt, addAmt
	}
	if addAmt.IsZero() {
		return oldPoolUnits, cosmos.ZeroUint()
	}
	lpUnits := common.GetUncappedShare(addAmt, poolAmt, oldPoolUnits)
	return oldPoolUnits.Add(lpUnits), lpUnits
}

func (h AddLiquidityHandler) addLiquidity(ctx cosmos.Context,
	asset common.Asset,
	addRuneAmount, addAssetAmount cosmos.Uint,
	runeAddr, assetAddr common.Address,
	requestTxHash common.TxID,
	stage bool,
	constAccessor constants.ConstantValues,
) (err error) {
	if asset.IsSecuredAsset() {
		if !addAssetAmount.IsZero() {
			var accAddr cosmos.AccAddress
			accAddr, err = runeAddr.AccAddress()
			if err != nil {
				return err
			}

			requestedAmount := addAssetAmount
			var coins common.Coin
			coins, err = h.mgr.SecuredAssetManager().Withdraw(
				ctx, asset, addAssetAmount, accAddr, common.NoAddress, requestTxHash,
			)
			if err != nil {
				return err
			}

			if !coins.Asset.Equals(asset.GetLayer1Asset()) {
				return fmt.Errorf("secured asset manager withdraw return unexpected asset: %s", coins.Asset.String())
			}

			// Verify withdrawn amount is not zero when requested amount was non-zero
			if coins.Amount.IsZero() {
				return fmt.Errorf("secured asset withdrawal returned zero amount for request of %s", requestedAmount)
			}

			// Log if withdrawn amount differs from requested (due to share-based calculation rounding)
			if !coins.Amount.Equal(requestedAmount) {
				ctx.Logger().Info("secured asset withdrawal amount differs from requested",
					"asset", asset, "requested", requestedAmount, "withdrawn", coins.Amount)
			}

			addAssetAmount = coins.Amount
		}

		asset = asset.GetLayer1Asset()
	}

	ctx.Logger().Info("liquidity provision", "asset", asset, "rune amount", addRuneAmount, "asset amount", addAssetAmount)
	if err = h.validateAddLiquidityMessage(ctx, h.mgr.Keeper(), asset, requestTxHash, runeAddr, assetAddr); err != nil {
		return fmt.Errorf("add liquidity message fail validation: %w", err)
	}

	// NOTE: When multiple MsgAddLiquidity messages for the same pool exist in a single transaction,
	// each subsequent message will read the modified pool state from previous messages.
	// This is standard Cosmos SDK behavior - all messages in a tx share the same state context.
	// Pool unit calculations remain mathematically sound as they use current pool balances.
	pool, err := h.mgr.Keeper().GetPool(ctx, asset)
	if err != nil {
		return ErrInternal(err, fmt.Sprintf("fail to get pool(%s)", asset))
	}
	synthSupply := h.mgr.Keeper().GetTotalSupply(ctx, pool.Asset.GetSyntheticAsset())
	originalUnits := pool.CalcUnits(synthSupply)
	// Covers every add that reaches the pool, staged (pending) halves and
	// POL adds included. Savers vaults have no synth overhang.
	if !asset.IsSyntheticAsset() && isLUVIOverhangGuarded(ctx, h.mgr.Keeper(), pool, synthSupply) {
		return fmt.Errorf("%w: %s", errAddLiquidityPoolOverhang, asset)
	}

	fetchAddr := runeAddr
	if fetchAddr.IsEmpty() {
		fetchAddr = assetAddr
	}
	effectiveRuneAddr := runeAddr
	effectiveAssetAddr := assetAddr
	// addLiquidity is called both from handle(), which pre-normalizes
	// unknown senders to NoAddress, and from direct/unit-test paths that can
	// still supply the raw unknown-sender marker. Re-resolve here so both entry
	// points converge on the same staged-LP behavior.
	effectiveAssetSender := common.ResolveObservedSender(asset.GetLayer1Asset().GetChain(), effectiveAssetAddr)
	if effectiveAssetSender.RefundAddressRequired {
		// Some inbound observations cannot recover the sender address, so the
		// asset-side tx may arrive with an xmrunknown marker instead of the user's
		// real address. Preserve the staged LP address instead of treating that as
		// a mismatched counterparty.
		effectiveAssetAddr = effectiveAssetSender.Address
	}
	su, err := h.mgr.Keeper().GetLiquidityProvider(ctx, asset, fetchAddr)
	if err != nil {
		return ErrInternal(err, "fail to get liquidity provider")
	}
	if asset.GetLayer1Asset().GetChain().RequiresExplicitRefundAddress() &&
		!effectiveRuneAddr.IsEmpty() &&
		!effectiveAssetAddr.IsEmpty() {
		// Once a real asset address is known for a sender-opaque chain, do not
		// allow a different RUNE address to bind against the same asset-side
		// address. Unknown-sender staging still works, but it cannot overwrite
		// an established LP pairing.
		//
		// Keeper only indexes LPs by the canonical fetch address, so this is
		// currently an O(n) scan across the asset LP set. That is acceptable for
		// low-cardinality sender-opaque pools; add a reverse index if LP counts
		// grow enough for this guard to become hot.
		iter := h.mgr.Keeper().GetLiquidityProviderIterator(ctx, asset)
		defer iter.Close()
		for ; iter.Valid(); iter.Next() {
			var assetLP types.LiquidityProvider
			if err = h.mgr.Keeper().Cdc().Unmarshal(iter.Value(), &assetLP); err != nil {
				return ErrInternal(err, "fail to decode liquidity provider")
			}
			if assetLP.AssetAddress.Equals(effectiveAssetAddr) &&
				!assetLP.RuneAddress.IsEmpty() &&
				!assetLP.RuneAddress.Equals(effectiveRuneAddr) {
				return errAddLiquidityMismatchAddr
			}
		}
	}

	su.LastAddHeight = ctx.BlockHeight()
	if su.Units.IsZero() {
		if su.PendingTxID.IsEmpty() {
			if su.RuneAddress.IsEmpty() {
				su.RuneAddress = effectiveRuneAddr
			}
			if su.AssetAddress.IsEmpty() {
				su.AssetAddress = effectiveAssetAddr
			}
		}

		if asset.IsSyntheticAsset() {
			// new SU, by default, places the thor address to the rune address,
			// but here we want it to be on the asset address only
			su.AssetAddress = effectiveAssetAddr
			su.RuneAddress = common.NoAddress // no rune to add/withdraw
		} else {
			if su.RuneAddress.IsEmpty() {
				su.RuneAddress = effectiveRuneAddr
			}
			if su.AssetAddress.IsEmpty() {
				su.AssetAddress = effectiveAssetAddr
			}
			// ensure input addresses match LP position addresses
			if !effectiveRuneAddr.IsEmpty() && !effectiveRuneAddr.Equals(su.RuneAddress) {
				return errAddLiquidityMismatchAddr
			}
			if !effectiveAssetAddr.IsEmpty() && !effectiveAssetAddr.Equals(su.AssetAddress) {
				return errAddLiquidityMismatchAddr
			}
		}
	}

	if asset.IsSyntheticAsset() {
		if su.AssetAddress.IsEmpty() || !su.AssetAddress.IsChain(asset.GetLayer1Asset().GetChain()) {
			return errAddLiquidityMismatchAddr
		}
	} else if !effectiveAssetAddr.IsEmpty() && !su.AssetAddress.Equals(effectiveAssetAddr) {
		// mismatch of asset addresses from what is known to the address
		// given. Refund it.
		return errAddLiquidityMismatchAddr
	}

	// get tx hashes
	runeTxID := requestTxHash
	assetTxID := requestTxHash
	if addRuneAmount.IsZero() {
		runeTxID = su.PendingTxID
	} else {
		assetTxID = su.PendingTxID
	}

	pendingRuneAmt := su.PendingRune.Add(addRuneAmount)
	pendingAssetAmt := su.PendingAsset.Add(addAssetAmount)

	// if we have an asset address and no asset amount, put the rune pending
	if stage && pendingAssetAmt.IsZero() {
		pool.PendingInboundRune = pool.PendingInboundRune.Add(addRuneAmount)
		su.PendingRune = pendingRuneAmt
		su.PendingTxID = requestTxHash
		h.mgr.Keeper().SetLiquidityProvider(ctx, su)
		if err = h.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return ErrInternal(err, "fail to save pool pending inbound rune")
		}

		// add pending liquidity event
		evt := NewEventPendingLiquidity(pool.Asset, AddPendingLiquidity, su.RuneAddress, addRuneAmount, su.AssetAddress, cosmos.ZeroUint(), requestTxHash, common.TxID(""))
		if err = h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			return ErrInternal(err, "fail to emit partial add liquidity event")
		}
		return nil
	}

	// if we have a rune address and no rune asset, put the asset in pending
	if stage && pendingRuneAmt.IsZero() {
		pool.PendingInboundAsset = pool.PendingInboundAsset.Add(addAssetAmount)
		su.PendingAsset = pendingAssetAmt
		su.PendingTxID = requestTxHash
		h.mgr.Keeper().SetLiquidityProvider(ctx, su)
		if err = h.mgr.Keeper().SetPool(ctx, pool); err != nil {
			return ErrInternal(err, "fail to save pool pending inbound asset")
		}

		evt := NewEventPendingLiquidity(pool.Asset, AddPendingLiquidity, su.RuneAddress, cosmos.ZeroUint(), su.AssetAddress, addAssetAmount, common.TxID(""), requestTxHash)
		if err = h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			return ErrInternal(err, "fail to emit partial add liquidity event")
		}
		return nil
	}

	// Validate pending amounts before subtraction to detect accounting inconsistencies
	if su.PendingRune.GT(pool.PendingInboundRune) {
		return ErrInternal(nil, fmt.Sprintf("pending rune accounting error: LP pending %s > pool pending %s", su.PendingRune, pool.PendingInboundRune))
	}
	if su.PendingAsset.GT(pool.PendingInboundAsset) {
		return ErrInternal(nil, fmt.Sprintf("pending asset accounting error: LP pending %s > pool pending %s", su.PendingAsset, pool.PendingInboundAsset))
	}
	pool.PendingInboundRune = pool.PendingInboundRune.Sub(su.PendingRune)
	pool.PendingInboundAsset = pool.PendingInboundAsset.Sub(su.PendingAsset)
	su.PendingAsset = cosmos.ZeroUint()
	su.PendingRune = cosmos.ZeroUint()
	su.PendingTxID = ""

	ctx.Logger().Info("pre add liquidity", "pool", pool.Asset, "rune", pool.BalanceRune, "asset", pool.BalanceAsset, "LP units", pool.LPUnits, "synth units", pool.SynthUnits)
	ctx.Logger().Info("adding liquidity", "rune", addRuneAmount, "asset", addAssetAmount)

	balanceRune := pool.BalanceRune
	balanceAsset := pool.BalanceAsset

	oldPoolUnits := pool.GetPoolUnits()
	var newPoolUnits, liquidityUnits cosmos.Uint
	if asset.IsSyntheticAsset() {
		pendingRuneAmt = cosmos.ZeroUint() // sanity check
		if balanceAsset.IsZero() && !oldPoolUnits.IsZero() {
			return fmt.Errorf("cannot add liquidity: pool has non-zero units with zero asset balance")
		}
		newPoolUnits, liquidityUnits = calculateVaultUnits(oldPoolUnits, balanceAsset, pendingAssetAmt)
	} else {
		newPoolUnits, liquidityUnits, err = calculatePoolUnits(oldPoolUnits, balanceRune, balanceAsset, pendingRuneAmt, pendingAssetAmt)
		if err != nil {
			return ErrInternal(err, "fail to calculate pool unit")
		}
	}

	ctx.Logger().Info("current pool status", "pool units", newPoolUnits, "liquidity units", liquidityUnits)
	if liquidityUnits.IsZero() && (!pendingRuneAmt.IsZero() || !pendingAssetAmt.IsZero()) {
		return ErrInternal(nil, "liquidity units cannot be zero when deposit amounts are non-zero")
	}
	// NOTE: pendingRuneAmt and pendingAssetAmt are derived from amounts verified by:
	// - External chains: Bifrost observers verify actual on-chain transactions before reporting
	// - Native chain: Cosmos SDK bank module ante handler validates transfers
	// - Secured assets: SecuredAssetManager.Withdraw validates and burns share tokens
	// Therefore, explicit balance verification here is unnecessary as it would be redundant.
	poolRune := balanceRune.Add(pendingRuneAmt)
	poolAsset := balanceAsset.Add(pendingAssetAmt)
	pool.LPUnits = pool.LPUnits.Add(liquidityUnits)
	pool.BalanceRune = poolRune
	pool.BalanceAsset = poolAsset
	ctx.Logger().Info("post add liquidity", "pool", pool.Asset, "rune", pool.BalanceRune, "asset", pool.BalanceAsset, "LP units", pool.LPUnits, "synth units", pool.SynthUnits, "add liquidity units", liquidityUnits)
	if (pool.BalanceRune.IsZero() && !asset.IsSyntheticAsset()) || pool.BalanceAsset.IsZero() {
		return ErrInternal(nil, "pool cannot have zero rune or asset balance")
	}

	// CalcUnits multiplies LP units by the synth supply as cosmos.Uint, which
	// panics past 2^256; refuse an add that would leave the pool there.
	if !asset.IsSyntheticAsset() && !fitsUint256(new(big.Int).Mul(pool.LPUnits.BigInt(), synthSupply.BigInt())) {
		return ErrInternal(errPoolUnitsOverflow, "fail to calculate synth units")
	}
	// CalcUnits to set the correct SynthUnits before the DepositValue calculations
	pool.CalcUnits(synthSupply)

	if err = h.mgr.Keeper().SetPool(ctx, pool); err != nil {
		return ErrInternal(err, "fail to save pool")
	}

	if originalUnits.IsZero() && !pool.GetPoolUnits().IsZero() {
		poolEvent := NewEventPool(pool.Asset, pool.Status)
		if err = h.mgr.EventMgr().EmitEvent(ctx, poolEvent); err != nil {
			ctx.Logger().Error("fail to emit pool event", "error", err)
		}
	}

	su.Units = su.Units.Add(liquidityUnits)
	if pool.Status == PoolAvailable {
		if su.AssetDepositValue.IsZero() && su.RuneDepositValue.IsZero() {
			su.RuneDepositValue = common.GetSafeShare(su.Units, pool.GetPoolUnits(), pool.BalanceRune)
			su.AssetDepositValue = common.GetSafeShare(su.Units, pool.GetPoolUnits(), pool.BalanceAsset)
		} else {
			su.RuneDepositValue = su.RuneDepositValue.Add(common.GetSafeShare(liquidityUnits, pool.GetPoolUnits(), pool.BalanceRune))
			su.AssetDepositValue = su.AssetDepositValue.Add(common.GetSafeShare(liquidityUnits, pool.GetPoolUnits(), pool.BalanceAsset))
		}
	}
	h.mgr.Keeper().SetLiquidityProvider(ctx, su)

	evt := NewEventAddLiquidity(asset, liquidityUnits, su.RuneAddress, pendingRuneAmt, pendingAssetAmt, runeTxID, assetTxID, su.AssetAddress)
	if err = h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return ErrInternal(err, "fail to emit add liquidity event")
	}

	// if its the POL is adding, track rune added
	polAddress, err := h.mgr.Keeper().GetModuleAddress(ReserveName)
	if err != nil {
		return err
	}

	if polAddress.Equals(su.RuneAddress) {
		var pol ProtocolOwnedLiquidity
		pol, err = h.mgr.Keeper().GetPOL(ctx)
		if err != nil {
			return err
		}
		pol.RuneDeposited = pol.RuneDeposited.Add(pendingRuneAmt)

		if err = h.mgr.Keeper().SetPOL(ctx, pol); err != nil {
			return err
		}

		ctx.Logger().Info("POL deposit", "pool", pool.Asset, "rune", pendingRuneAmt)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "pol", "pool", "rune_deposited"},
			telem(pendingRuneAmt),
			[]metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())},
		)
	}

	// Counterpart to the withdraw metrics: together they explain pool depth as a
	// flow instead of an unattributable level. The POL counter above covers only
	// Reserve/POL adds; a real user add emits nothing else.
	// Placed after every `return err` path so only committed adds are counted.
	addLabels := []metrics.Label{telemetry.NewLabel("pool", pool.Asset.String())}
	telemetry.IncrCounterWithLabels([]string{"thornode", "add_liquidity", "count"}, 1, addLabels)
	telemetry.IncrCounterWithLabels([]string{"thornode", "add_liquidity", "rune"}, telem(pendingRuneAmt), addLabels)
	telemetry.IncrCounterWithLabels([]string{"thornode", "add_liquidity", "asset"}, telem(pendingAssetAmt), addLabels)

	return nil
}

// getTotalLiquidityRUNE we have in all pools
func (h AddLiquidityHandler) getTotalLiquidityRUNE(ctx cosmos.Context) (cosmos.Uint, error) {
	pools, err := h.mgr.Keeper().GetPools(ctx)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get pools from data store: %w", err)
	}
	total := cosmos.ZeroUint()
	for _, p := range pools {
		// ignore suspended pools
		if p.Status == PoolSuspended {
			continue
		}
		if p.Asset.IsSyntheticAsset() {
			continue
		}
		if p.Asset.IsDerivedAsset() {
			continue
		}
		total = total.Add(p.BalanceRune)
	}
	return total, nil
}

func (h AddLiquidityHandler) needsSwap(msg MsgAddLiquidity) bool {
	return len(msg.Tx.Coins) == 1 && !msg.Tx.Coins[0].IsRune() && !msg.Asset.Equals(msg.Tx.Coins[0].Asset)
}
