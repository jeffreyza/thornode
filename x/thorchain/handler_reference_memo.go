package thorchain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// ReferenceMemoHandler a handler to process reference memo messages
type ReferenceMemoHandler struct {
	mgr Manager
}

// NewReferenceMemoHandler create a new instance of network fee handler
func NewReferenceMemoHandler(mgr Manager) ReferenceMemoHandler {
	return ReferenceMemoHandler{mgr: mgr}
}

// Run is the main entry point for network fee logic
func (h ReferenceMemoHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgReferenceMemo)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("MsgReferenceMemo", "asset", msg.Asset, "memo", msg.Memo, "signer", msg.Signer)
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgReferenceMemo failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgReferenceMemo", "error", err)
	}
	return result, err
}

func (h ReferenceMemoHandler) validate(ctx cosmos.Context, msg MsgReferenceMemo) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	parsed, err := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), msg.Memo)
	if err != nil {
		return err
	}
	if parsed.IsOutbound() || parsed.IsInternal() {
		return fmt.Errorf("reference memo type %s is reserved for validator-internal use and cannot be registered", parsed.GetType().String())
	}

	// Chains whose observations may omit the inbound sender cannot issue a safe
	// refund after funds arrive. Only register memo types that can carry an
	// explicit asset-chain refund address, and require that address before funds
	// move. Keep this fail-closed so newly added memo types are not accidentally
	// enabled for sender-opaque chains without a safe refund path.
	inboundChain := msg.Asset.GetLayer1Asset().GetChain()
	addressRole := ""
	addressField := ""
	if inboundChain.RequiresExplicitRefundAddress() {
		switch {
		case parsed.IsType(TxAdd):
			addressRole = "add liquidity asset address"
			addressField = "pair_address/asset_address"
		case parsed.IsType(TxSwap):
			addressRole = "swap refund address"
			addressField = "destination/refund_address"
		default:
			return fmt.Errorf("memoless %s %s memo is not supported because it cannot provide an explicit refund address", inboundChain, parsed.GetType())
		}

		refundAddr := parsed.GetRefundAddress()
		if refundAddr.IsEmpty() {
			return fmt.Errorf("memoless %s %s is required in the memo (%s)", inboundChain, addressRole, addressField)
		}
		if !inboundChain.IsValidAddress(refundAddr) {
			return fmt.Errorf("memoless %s %s %s is not valid for the current network", inboundChain, addressRole, refundAddr)
		}
	}

	// Check if memoless transactions are halted
	haltMemoless, err := h.mgr.Keeper().GetMimir(ctx, constants.HaltMemoless.String())
	if err != nil {
		ctx.Logger().Error("fail to get HaltMemoless mimir", "error", err)
	}
	if err == nil && haltMemoless > 0 {
		return errors.New("memoless transactions are currently halted")
	}

	ttl := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
	if ttl <= 0 {
		return errors.New("memoless transactions is currently disabled")
	}

	cost := memolessCost(ctx, h.mgr.Keeper(), msg.Asset)
	if !cost.IsZero() {
		runeAmount := cosmos.NewIntFromBigInt(cost.BigInt())
		cosmosRuneCoin := cosmos.NewCoin(common.RuneNative.Native(), runeAmount)
		if !h.mgr.Keeper().HasCoins(ctx, msg.Signer, cosmos.NewCoins(cosmosRuneCoin)) {
			return fmt.Errorf("insufficient RUNE balance to pay memoless transaction cost of %s", cost)
		}
	}

	return nil
}

// handle process MsgReferenceMemo
func (h ReferenceMemoHandler) handle(ctx cosmos.Context, msg MsgReferenceMemo) (*cosmos.Result, error) {
	ttl := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
	// Priced before the registration is recorded, so the registrant pays for the
	// congestion they arrive into, not the congestion they create.
	cost := memolessCost(ctx, h.mgr.Keeper(), msg.Asset)

	// Find an available reference BEFORE charging cost to avoid charging users
	// when no references are available. References are allocated sequentially
	// starting from the last assigned number, preventing collisions even when
	// multiple users register concurrently.
	posStr := h.mgr.Keeper().GetLastReferenceNumber(ctx, msg.Asset)
	next := h.nextReference(ctx, msg.Asset, posStr, ttl)
	if next == "" {
		return nil, fmt.Errorf("unable to find an available reference")
	}

	// Only charge cost AFTER we've confirmed a reference is available.
	// This prevents users from losing RUNE when all references are exhausted.
	if !cost.IsZero() {
		runeCoin := common.NewCoin(common.RuneNative, cost)
		if err := h.mgr.Keeper().SendFromAccountToModule(ctx, msg.Signer, ReserveName, common.NewCoins(runeCoin)); err != nil {
			return nil, fmt.Errorf("fail to transfer memoless transaction cost to reserve: %w", err)
		}
	}

	hash := sha256.New()
	_, err := hash.Write(ctx.TxBytes())
	if err != nil {
		return nil, fmt.Errorf("fail to get txid: %w", err)
	}
	txid := hex.EncodeToString(hash.Sum(nil))
	txID, err := common.NewTxID(txid)
	if err != nil {
		return nil, fmt.Errorf("fail to get txid: %w", err)
	}

	refMemo := NewReferenceMemo(msg.Asset, msg.Memo, next, ctx.BlockHeight())
	refMemo.RegisteredBy = msg.Signer
	refMemo.RegistrationHash = txID

	h.mgr.Keeper().SetReferenceMemo(ctx, refMemo)
	h.mgr.Keeper().SetLastReferenceNumber(ctx, msg.Asset, next)
	bumpMemolessWindow(ctx, h.mgr.Keeper(), msg.Asset, ttl)
	ctx.Logger().Info("successfully registered memo", "asset", refMemo.Asset, "reference", refMemo.Reference, "memo", refMemo.Memo, "height", refMemo.Height, "registration_hash", refMemo.RegistrationHash)
	return &cosmos.Result{}, nil
}

// nextReference finds the next available reference for the asset
func (h ReferenceMemoHandler) nextReference(ctx cosmos.Context, asset common.Asset, posStr string, ttl int64) string {
	end := h.mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnRefCount)
	if end <= 0 {
		return ""
	}
	pos, _ := strconv.ParseInt(posStr, 10, 64)
	pos++ // next

	txnRefLength := len(fmt.Sprintf("%d", end))

	if pos > end {
		pos = 1
	}

	// reachedEnd prevents infinite loops by tracking wraparounds.
	// We allow looping to the start once to search all references,
	// but return empty if we reach the end a second time (no available refs).
	reachedEnd := false
	// Cap iterations to the lesser of end and a hard limit, decoupling the
	// loop bound from the full governance parameter range to prevent DoS
	// from excessively large MemolessTxnRefCount values.
	const maxRefScan int64 = 100_000
	maxIter := end
	if maxIter > maxRefScan {
		maxIter = maxRefScan
	}
	for iter := int64(0); iter < maxIter; iter++ {
		if pos > end {
			pos = 1
			if reachedEnd {
				return ""
			}
			reachedEnd = true
		}
		ref := leadingZeros(txnRefLength, fmt.Sprintf("%d", pos))
		refMemo, err := h.mgr.Keeper().GetReferenceMemo(ctx, asset, ref)
		if err != nil {
			ctx.Logger().Error("fail to get ref memo", "error", err)
			pos++
			continue
		}

		// see if this memo is available, if its not being used, return it
		if refMemo.Height == 0 || refMemo.IsExpired(ctx.BlockHeight(), ttl) {
			return ref
		}
		pos++
	}
	return ""
}

// rollMemolessWindow advances the two-bucket sliding window to the given height
// without persisting it.
//
// The window holds two facts about registration heights, and they are what makes
// the roll safe when governance moves MemolessTxnTTL under a live window:
//
//   - everything counted in prev was registered strictly before start
//   - everything counted in cur was registered before start+width, where width is
//     the TTL the current bucket was opened under
//
// A registration at r stays live until r+ttl (see ReferenceMemo.IsExpired), so
// prev is safe to drop once height >= start+ttl and cur once height >= start+
// width+ttl. Rolling re-anchors start to the current height rather than to a
// TTL-aligned grid, which is what keeps the prev invariant true across a TTL
// change: every registration is at a height <= the height we anchor to.
//
// Comparisons are written as subtractions off a clamped start so that a
// governance-set MemolessTxnTTL near the int64 ceiling cannot overflow. A start
// ahead of the current height (never produced here, but cheap to tolerate)
// yields a negative elapsed and is left alone.
func rollMemolessWindow(start, cur, prev, width, height, ttl int64) (int64, int64, int64, int64) {
	// The counters are only ever written non-negative, but they are raw state
	// rather than mimir, so nothing else floors them. Clamp here so that a corrupt
	// or hand-edited window cannot yield a negative occupancy downstream.
	start, cur, prev, width = max(start, 0), max(cur, 0), max(prev, 0), max(width, 0)
	if ttl <= 0 {
		// both callers already refuse a non-positive TTL; guard here too so that a
		// future one cannot turn a mimir into a chain halt
		return start, cur, prev, width
	}
	if width == 0 {
		// never written, or written before the width was tracked: the current TTL is
		// the only width available, and an empty window is unaffected either way
		width = ttl
	}
	elapsed := height - start
	switch {
	case elapsed >= width && elapsed-width >= ttl:
		return height, 0, 0, ttl
	case elapsed >= ttl:
		return height, 0, cur, ttl
	default:
		return start, cur, prev, width
	}
}

// memolessLiveRefs estimates how many reference slots are currently occupied for
// an asset.
//
// A registration occupies exactly one slot for exactly MemolessTxnTTL blocks, and
// nothing ever frees a slot early, so the number of live slots equals the number
// of successful registrations in the last TTL blocks. That holds no matter which
// slot nextReference picks, so it survives the skipping and wrapping that make
// the reference ring itself impossible to reason about positionally.
//
// The two buckets together always span a superset of the live window
// (height-ttl, height], so cur+prev can never undercount, including across a
// MemolessTxnTTL change - see rollMemolessWindow for why. It can overcount by at
// most prev, giving a fee that is at worst 2x too high under steady load and
// never too low - the safe direction for a congestion charge.
//
// The one case that does undercount is raising MemolessTxnTTL: IsExpired is
// evaluated against the current TTL, so a raise brings back references the window
// has already aged out, and only a full scan of the ring could find them again.
// That resolves itself within one TTL of the change; see docs/mimir.md.
//
// ponytail: whole-bucket counting, so the fee ramps within a bucket and resets at
// the roll. Swap for a real expiry index if the estimate ever needs to be exact.
func memolessLiveRefs(ctx cosmos.Context, k keeper.Keeper, asset common.Asset, ttl int64) int64 {
	if ttl <= 0 {
		return 0
	}
	start, cur, prev, width := k.GetReferenceMemoWindow(ctx, asset)
	_, cur, prev, _ = rollMemolessWindow(start, cur, prev, width, ctx.BlockHeight(), ttl)
	if prev > math.MaxInt64-cur {
		return math.MaxInt64 // saturate rather than wrap negative
	}
	return cur + prev
}

// bumpMemolessWindow records one registration for an asset.
func bumpMemolessWindow(ctx cosmos.Context, k keeper.Keeper, asset common.Asset, ttl int64) {
	if ttl <= 0 {
		return
	}
	start, cur, prev, width := k.GetReferenceMemoWindow(ctx, asset)
	start, cur, prev, width = rollMemolessWindow(start, cur, prev, width, ctx.BlockHeight(), ttl)
	k.SetReferenceMemoWindow(ctx, asset, start, cur+1, prev, width)
}

// memolessCost is the RUNE cost of registering a reference memo for an asset. It
// rises as the asset's reference slots fill up:
//
//	cost = MemolessTxnCost * (1 + slope * used / free)
//
// with slope = MemolessTxnCostSlopeBPS / 10_000. A slope of 0 (the default) keeps
// the flat MemolessTxnCost; 10_000 gives cost = base / (1 - occupancy), i.e. 2x at
// half full and 10x at 90% full; higher slopes bite earlier. free is floored at 1,
// so the cost is bounded by base * (1 + slope * (refCount-1)).
func memolessCost(ctx cosmos.Context, k keeper.Keeper, asset common.Asset) cosmos.Uint {
	base := k.GetConfigInt64(ctx, constants.MemolessTxnCost)
	if base <= 0 {
		return cosmos.ZeroUint()
	}
	cost := cosmos.NewUint(uint64(base))

	slope := k.GetConfigInt64(ctx, constants.MemolessTxnCostSlopeBPS)
	refCount := k.GetConfigInt64(ctx, constants.MemolessTxnRefCount)
	if slope <= 0 || refCount <= 0 {
		return cost
	}

	// used is floored at 0 and capped one below refCount, so free below is always
	// in [1, refCount-1]: the divisor can never be zero and no conversion to
	// uint64 can see a negative.
	used := memolessLiveRefs(ctx, k, asset, k.GetConfigInt64(ctx, constants.MemolessTxnTTL))
	if used > refCount-1 {
		used = refCount - 1
	}
	if used <= 0 {
		return cost
	}
	free := refCount - used

	// cost * (10_000*free + slope*used) / (10_000*free)
	denominator := cosmos.NewUint(uint64(free)).MulUint64(10_000)
	numerator := denominator.Add(cosmos.NewUint(uint64(slope)).MulUint64(uint64(used)))
	return cost.Mul(numerator).Quo(denominator)
}
