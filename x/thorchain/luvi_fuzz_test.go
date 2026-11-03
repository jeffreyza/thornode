package thorchain

import (
	"fmt"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	thorlog "gitlab.com/thorchain/thornode/v3/log"
	"gitlab.com/thorchain/thornode/v3/test/luvicheck"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// FuzzLuviPoolOps uses the synth-aware LUVI index G as an oracle over random
// sequences of pool operations run through the real handlers and swapper.
// See docs/concepts/luvi-invariant.md for the invariants it asserts.
//
// The input is a 6-byte header choosing the pool (depths, synth supply)
// followed by up to luviFuzzMaxOps 4-byte operations. Each
// operation runs in its own cache context, as the handlers and the swap queue
// do in production, and is committed only on success. After every operation:
//
//   - nothing panics;
//   - a failed operation leaves the pool, synth supply and LP records as they
//     were;
//   - luvicheck.Monitor (each operation observed as one block, with the
//     operation's events) reports no G drop beyond its allowances and no LP
//     unit change the add_liquidity/withdraw events do not explain;
//   - a pool with LP units outstanding keeps both depths non-zero;
//   - a swap changes G only by rounding (luviFuzzSwapRoundingUnits base
//     units of each side), in either direction for RUNE <-> asset swaps.
//
// Known findings are pinned in luvi_fuzz_findings_test.go and kept out of
// the way here: synth swaps run at mainnet's virtual depth (m = 1), and a
// sequence stops at the overflow that deposits into an overhang pool lead to.
//
// Run: go test -tags=mocknet -run='^$' -fuzz=FuzzLuviPoolOps ./x/thorchain/
func FuzzLuviPoolOps(f *testing.F) {
	for _, seed := range luviFuzzSeeds() {
		f.Add(seed)
	}
	base := newLuviFuzzBase(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		base.run(t, data)
	})
}

const (
	luviFuzzHeaderLen = 6
	luviFuzzOpLen     = 4
	luviFuzzMaxOps    = 32
	luviFuzzLPs       = 4
)

// luviFuzzSwapRoundingUnits bounds |dG/sqrt(AR)| for a swap or outbound fee
// in base units of each side (1/A + 1/R each): a swap's output is floored and
// its fee is computed and converted separately, so it can move G by a few
// base units, never by a share of the trade.
const luviFuzzSwapRoundingUnits = 4

type luviFuzzOp uint8

const (
	opAddSym luviFuzzOp = iota
	opAddRune
	opAddAsset
	opAddMixed
	opWithdraw
	opSwapRuneToAsset
	opSwapAssetToRune
	opSwapRuneToSynth
	opSwapSynthToRune
	opSwapAssetToSynth
	opSwapSynthToAsset
	opDonate
	luviFuzzOpCount
)

var luviFuzzOpNames = [...]string{
	"add sym", "add rune", "add asset", "add mixed", "withdraw",
	"swap rune->asset", "swap asset->rune", "swap rune->synth", "swap synth->rune",
	"swap asset->synth", "swap synth->asset", "donate",
}

func (o luviFuzzOp) isSwap() bool { return o >= opSwapRuneToAsset && o <= opSwapSynthToAsset }

func (o luviFuzzOp) isL1Swap() bool { return o == opSwapRuneToAsset || o == opSwapAssetToRune }

// luviFuzzBase is the state shared by every input: managers, a vault and an
// active validator. Each input runs in a cache context on top of it that is
// thrown away, so inputs never see each other.
type luviFuzzBase struct {
	ctx    cosmos.Context
	mgr    *Mgrs
	asset  common.Asset
	lpRune [luviFuzzLPs]common.Address
	lpETH  [luviFuzzLPs]common.Address
	holder common.Address // receives minted synths
}

func newLuviFuzzBase(f *testing.F) *luviFuzzBase {
	nop := zerolog.Nop()
	ctx, mgr := setupManagerWith(f, f.TempDir, thorlog.SdkLogWrapper{Logger: &nop})
	b := &luviFuzzBase{ctx: ctx, mgr: mgr, asset: common.ETHAsset, holder: GetRandomTHORAddress()}
	for i := range b.lpRune {
		b.lpRune[i] = GetRandomRUNEAddress()
		b.lpETH[i] = GetRandomETHAddress()
	}
	if err := mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)); err != nil {
		f.Fatal(err)
	}
	return b
}

// luviFuzzRun is the per-input state.
type luviFuzzRun struct {
	t       *testing.T
	b       *luviFuzzBase
	ctx     cosmos.Context
	mult    int64
	trace   []string
	monitor *luvicheck.Monitor
	tracker *types2.LuviTracker
	// mid is the pool between the two legs of the last same-pool double
	// swap (asset <-> synth), rebuilt from its first swap event.
	mid *types2.LuviState
	// stopped ends the sequence after a known, separately pinned failure.
	stopped bool
	// overhangDeposit records a deposit into a pool in overhang, which
	// over-mints LP units (TestOverhangDepositMultipliesUnits).
	overhangDeposit bool
}

// luviFuzzHeader decodes the pool the input starts from.
type luviFuzzHeader struct {
	runeDepth, assetDepth uint64
	synthPct              uint64 // synth supply as a percentage of asset depth
	mult                  int64
}

func decodeLuviFuzzHeader(h []byte) luviFuzzHeader {
	pow := func(e byte) uint64 {
		v := uint64(1)
		for i := byte(0); i < e; i++ {
			v *= 10
		}
		return v
	}
	return luviFuzzHeader{
		runeDepth:  (1 + uint64(h[0])%100) * pow(6+h[1]%9),  // 1e6 .. 1e16
		assetDepth: (1 + uint64(h[2])%100) * pow(4+h[3]%11), // 1e4 .. 1e16
		synthPct:   uint64(h[4]) * 4,                        // 0 .. 1020%
		// mainnet's VirtualMultSynthsBasisPoints. Above 10,000 synth swaps
		// are not G-neutral (see TestLuviFindingSynthSwapsWithVirtualDepth),
		// so h[5] is reserved for when that is fixed.
		mult: 10_000,
	}
}

func (b *luviFuzzBase) run(t *testing.T, data []byte) {
	if len(data) < luviFuzzHeaderLen {
		return
	}
	hdr := decodeLuviFuzzHeader(data)
	ops := data[luviFuzzHeaderLen:]
	if len(ops) > luviFuzzMaxOps*luviFuzzOpLen {
		ops = ops[:luviFuzzMaxOps*luviFuzzOpLen]
	}

	b.mgr.gasMgr.BeginBlock()
	ctx, _ := b.ctx.CacheContext()
	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	r := &luviFuzzRun{t: t, b: b, ctx: ctx, mult: hdr.mult, monitor: luvicheck.NewMonitor()}
	r.trace = append(r.trace, fmt.Sprintf("pool rune=%d asset=%d synths=%d%% mult=%d", hdr.runeDepth, hdr.assetDepth, hdr.synthPct, hdr.mult))
	r.setup(hdr)

	start := r.state()
	r.observe(0, types2.LuviState{}, start, nil, 0, luviFuzzOpCount)
	tracker, err := types2.NewLuviTracker(start)
	if err != nil {
		r.fatalf("initial state: %s", err)
	}
	r.tracker = tracker

	for i := 0; i+luviFuzzOpLen <= len(ops); i += luviFuzzOpLen {
		if !r.state().IsValid() || r.stopped {
			return // the pool has emptied (Monitor has checked its retirement)
		}
		r.step(int64(i/luviFuzzOpLen+1), ops[i:i+luviFuzzOpLen])
	}
}

func (r *luviFuzzRun) setup(h luviFuzzHeader) {
	b, k := r.b, r.b.mgr.Keeper()
	pool := NewPool()
	pool.Asset = b.asset
	pool.BalanceRune = cosmos.NewUint(h.runeDepth)
	pool.BalanceAsset = cosmos.NewUint(h.assetDepth)
	pool.LPUnits = cosmos.NewUint(h.runeDepth)
	pool.Status = PoolAvailable
	r.must(k.SetPool(r.ctx, pool))
	k.SetLiquidityProvider(r.ctx, LiquidityProvider{
		Asset: b.asset, RuneAddress: b.lpRune[0], AssetAddress: b.lpETH[0], Units: pool.LPUnits,
	})
	// RUNE backing the pool and its payouts
	FundModule(r.t, r.ctx, k, AsgardName, 4*h.runeDepth)
	v := GetRandomVault()
	v.Type = AsgardVault
	v.Status = types2.VaultStatus_ActiveVault
	v.Coins = common.NewCoins(common.NewCoin(b.asset, cosmos.NewUint(h.assetDepth).MulUint64(100)))
	r.must(k.SetVault(r.ctx, v))
	if s := cosmos.NewUint(h.assetDepth).MulUint64(h.synthPct).QuoUint64(100); !s.IsZero() {
		coin := common.NewCoin(b.asset.GetSyntheticAsset(), s)
		r.must(k.MintToModule(r.ctx, ModuleName, coin))
		r.must(k.SendFromModuleToModule(r.ctx, ModuleName, AsgardName, common.NewCoins(coin)))
	}
}

// luviFuzzSnap is everything a failed operation must leave untouched.
type luviFuzzSnap struct {
	pool   Pool
	synths cosmos.Uint
	lps    [luviFuzzLPs]LiquidityProvider
}

func (r *luviFuzzRun) snap(ctx cosmos.Context) luviFuzzSnap {
	k := r.b.mgr.Keeper()
	p, err := k.GetPool(ctx, r.b.asset)
	r.must(err)
	s := luviFuzzSnap{pool: p, synths: k.GetTotalSupply(ctx, r.b.asset.GetSyntheticAsset())}
	for i := range s.lps {
		s.lps[i], err = k.GetLiquidityProvider(ctx, r.b.asset, r.b.lpRune[i])
		r.must(err)
	}
	return s
}

func (s luviFuzzSnap) equal(o luviFuzzSnap) bool {
	if !s.synths.Equal(o.synths) {
		return false
	}
	a, err := s.pool.Marshal()
	if err != nil {
		return false
	}
	c, err := o.pool.Marshal()
	if err != nil || string(a) != string(c) {
		return false
	}
	for i := range s.lps {
		a, err = s.lps[i].Marshal()
		if err != nil {
			return false
		}
		c, err = o.lps[i].Marshal()
		if err != nil || string(a) != string(c) {
			return false
		}
	}
	return true
}

func (r *luviFuzzRun) state() types2.LuviState {
	s := r.snap(r.ctx)
	return types2.NewLuviState(s.pool, s.synths)
}

func (r *luviFuzzRun) fatalf(format string, args ...interface{}) {
	r.t.Helper()
	r.t.Fatalf("%s\nsequence:\n  %s", fmt.Sprintf(format, args...), strings.Join(r.trace, "\n  "))
}

func (r *luviFuzzRun) must(err error) {
	r.t.Helper()
	if err != nil {
		r.fatalf("harness: %s", err)
	}
}

// amount scales depth by the operation's 16-bit fraction and shift.
func luviFuzzAmount(depth cosmos.Uint, aux byte, frac uint16) cosmos.Uint {
	a := depth.MulUint64(uint64(frac) + 1).QuoUint64(1 << 16)
	return a.QuoUint64(uint64(1) << (aux >> 4))
}

func (r *luviFuzzRun) step(height int64, op []byte) {
	kind := luviFuzzOp(op[0] % byte(luviFuzzOpCount))
	aux := op[1]
	frac := uint16(op[2])<<8 | uint16(op[3])

	before := r.snap(r.ctx)
	prevState := types2.NewLuviState(before.pool, before.synths)
	opCtx := r.ctx.WithBlockHeight(height).WithEventManager(cosmos.NewEventManager())
	cctx, write := opCtx.CacheContext()

	r.mid = nil
	if kind <= opAddMixed && prevState.IsOverhang() {
		r.overhangDeposit = true
	}
	desc, err := r.apply(cctx, kind, aux, frac, before)
	if err == nil {
		write()
	}
	r.trace = append(r.trace, fmt.Sprintf("%d: %s -> err=%v", height, desc, err))

	after := r.snap(r.ctx)
	if err != nil && !after.equal(before) {
		r.fatalf("failed operation changed state: %s", err)
	}
	if !after.pool.LPUnits.IsZero() && (after.pool.BalanceRune.IsZero() || after.pool.BalanceAsset.IsZero()) {
		r.fatalf("pool depleted with LP units outstanding: rune=%s asset=%s units=%s", after.pool.BalanceRune, after.pool.BalanceAsset, after.pool.LPUnits)
	}
	// G is a path integral sampled at observations: a same-pool double swap
	// changes the synth supply at its intermediate price, so it is observed
	// at that price too (see the luvi-invariant doc, "observation
	// granularity").
	next := types2.NewLuviState(after.pool, after.synths)
	states := []types2.LuviState{next}
	if err == nil && r.mid != nil {
		states = []types2.LuviState{*r.mid, next}
	}
	prev := prevState
	for j, st := range states {
		var events cosmos.Events
		roundings := 1 // the first leg of a double swap
		if j == len(states)-1 {
			events = opCtx.EventManager().Events()
			roundings = luviFuzzRoundings(events) - j
		}
		r.observe(2*height+int64(j), prev, st, events, roundings, kind)
		if !st.IsValid() {
			return
		}
		step, oerr := r.tracker.Observe(st)
		if oerr != nil {
			r.fatalf("observe: %s", oerr)
		}
		if err == nil && kind.isSwap() {
			lim := luviFuzzSwapLimit(prev)
			if step.NormalizedDeltaG.LT(lim.Neg()) || (kind.isL1Swap() && step.NormalizedDeltaG.GT(lim)) {
				r.fatalf("%s moved G beyond rounding: dG/sqrt(AR) = %s (limit %s)", luviFuzzOpNames[kind], step.NormalizedDeltaG, lim)
			}
		}
		prev = st
	}
}

// luviFuzzRounding is luviFuzzSwapRoundingUnits base units of each side of
// s, as a fraction of its sqrt(A*R).
func luviFuzzRounding(s types2.LuviState) cosmos.Dec {
	one := cosmos.NewDec(1)
	unit := one.Quo(cosmos.NewDecFromBigInt(s.BalanceAsset.BigInt())).Add(one.Quo(cosmos.NewDecFromBigInt(s.BalanceRune.BigInt())))
	return unit.MulInt64(luviFuzzSwapRoundingUnits)
}

// luviFuzzSwapLimit is the most a swap may move G by, normalized.
func luviFuzzSwapLimit(s types2.LuviState) cosmos.Dec {
	return luvicheck.DefaultTolerance.Add(luviFuzzRounding(s))
}

// luviFuzzRoundings counts the swap and outbound fee events in events.
func luviFuzzRoundings(events cosmos.Events) int {
	n := 0
	for _, e := range events {
		if e.Type == types2.SwapEventType || e.Type == types2.FeeEventType {
			n++
		}
	}
	return n
}

// observe feeds one observation to the Monitor. roundings is the number of
// swaps and outbound fee conversions in the step: each rounds its amounts to
// base units, which luvicheck.DefaultTolerance only covers on large pools
// (one base unit is 1/A or 1/R of sqrt(A*R)), so a Monitor drop within
// roundings * luviFuzzSwapRoundingUnits base units of each side of prev is
// rounding, not a loss.
func (r *luviFuzzRun) observe(height int64, prev, st types2.LuviState, events cosmos.Events, roundings int, kind luviFuzzOp) {
	snap := luvicheck.PoolSnapshot{
		Asset:        r.b.asset.String(),
		BalanceRune:  st.BalanceRune.String(),
		BalanceAsset: st.BalanceAsset.String(),
		LPUnits:      st.LPUnits.String(),
		PoolUnits:    st.PoolUnits.String(),
		SynthSupply:  st.SynthSupply.String(),
	}
	evs := make([]map[string]string, 0, len(events))
	for _, e := range events {
		m := map[string]string{"type": e.Type}
		for _, a := range e.Attributes {
			m[a.Key] = a.Value
		}
		evs = append(evs, m)
	}
	v, err := r.monitor.ObserveBlock(height, []luvicheck.PoolSnapshot{snap}, evs)
	if err != nil {
		r.fatalf("monitor: %s", err)
	}
	var msgs []string
	for i := range v {
		if v[i].Kind == luvicheck.Drop && roundings > 0 && prev.IsValid() {
			extra := luviFuzzRounding(prev).MulInt64(int64(roundings))
			if v[i].NormalizedDeltaG.GTE(v[i].Allowed.Add(extra).Neg()) {
				continue
			}
		}
		msgs = append(msgs, v[i].Error())
	}
	if len(msgs) > 0 {
		name := "setup"
		if kind < luviFuzzOpCount {
			name = luviFuzzOpNames[kind]
		}
		r.fatalf("%s: %s", name, strings.Join(msgs, "; "))
	}
}

// apply runs one operation in ctx, recovering panics into test failures.
func (r *luviFuzzRun) apply(ctx cosmos.Context, kind luviFuzzOp, aux byte, frac uint16, before luviFuzzSnap) (desc string, err error) {
	defer func() {
		if p := recover(); p != nil {
			if kind <= opAddMixed && r.overhangDeposit && strings.Contains(fmt.Sprint(p), "overflow") {
				// FINDING (pinned by TestLuviFindingOverhangAddOverflowPanics):
				// deposits into an overhang pool compound LP units until a
				// later deposit overflows in calculatePoolUnits. Stop this
				// sequence rather than report the same panic forever; any
				// other panic fails.
				r.stopped = true
				desc += " [known overhang overflow panic]"
				err = fmt.Errorf("panic: %v", p)
				return
			}
			r.trace = append(r.trace, "PANIC in "+desc)
			r.fatalf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	b := r.b
	pool := before.pool
	runeAmt := luviFuzzAmount(pool.BalanceRune, aux, frac)
	assetAmt := luviFuzzAmount(pool.BalanceAsset, aux, frac)
	lp := int(aux & 3)
	name := luviFuzzOpNames[kind]
	cv := constants.GetConstantValues(GetCurrentVersion())
	add := func(rn, as cosmos.Uint) error {
		return NewAddLiquidityHandler(r.b.mgr).addLiquidity(ctx, b.asset, rn, as, b.lpRune[lp], b.lpETH[lp], GetRandomTxHash(), false, cv)
	}
	switch kind {
	case opAddSym:
		assetAmt = runeAmt.Mul(pool.BalanceAsset).Quo(pool.BalanceRune)
		desc = fmt.Sprintf("%s lp%d rune=%s asset=%s", name, lp, runeAmt, assetAmt)
		return desc, add(runeAmt, assetAmt)
	case opAddRune:
		desc = fmt.Sprintf("%s lp%d rune=%s", name, lp, runeAmt)
		return desc, add(runeAmt, cosmos.ZeroUint())
	case opAddAsset:
		desc = fmt.Sprintf("%s lp%d asset=%s", name, lp, assetAmt)
		return desc, add(cosmos.ZeroUint(), assetAmt)
	case opAddMixed:
		assetAmt = pool.BalanceAsset.MulUint64(uint64(frac & 0xff)).QuoUint64(1 << 12)
		desc = fmt.Sprintf("%s lp%d rune=%s asset=%s", name, lp, runeAmt, assetAmt)
		return desc, add(runeAmt, assetAmt)
	case opWithdraw:
		bps := 1 + uint64(frac)%10_000
		to := []common.Asset{common.EmptyAsset, common.RuneAsset(), b.asset}[int(aux>>2)%3]
		desc = fmt.Sprintf("%s lp%d bps=%d to=%q", name, lp, bps, to)
		msg := MsgWithdrawLiquidity{
			WithdrawAddress: b.lpRune[lp],
			BasisPoints:     cosmos.NewUint(bps),
			Asset:           b.asset,
			Tx:              common.NewTx(GetRandomTxHash(), b.lpRune[lp], b.lpRune[lp], common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.OneUint())), nil, "-:"+b.asset.String()),
			WithdrawalAsset: to,
			Signer:          GetRandomBech32Addr(),
		}
		_, err = NewWithdrawLiquidityHandler(r.b.mgr).handle(ctx, msg)
		return desc, err
	case opSwapRuneToAsset:
		desc = fmt.Sprintf("%s %s", name, runeAmt)
		return desc, r.swap(ctx, common.NewCoin(common.RuneAsset(), runeAmt), b.asset, GetRandomETHAddress())
	case opSwapAssetToRune:
		desc = fmt.Sprintf("%s %s", name, assetAmt)
		return desc, r.swap(ctx, common.NewCoin(b.asset, assetAmt), common.RuneAsset(), GetRandomTHORAddress())
	case opSwapRuneToSynth:
		desc = fmt.Sprintf("%s %s", name, runeAmt)
		return desc, r.swap(ctx, common.NewCoin(common.RuneAsset(), runeAmt), b.asset.GetSyntheticAsset(), b.holder)
	case opSwapAssetToSynth:
		desc = fmt.Sprintf("%s %s", name, assetAmt)
		return desc, r.swap(ctx, common.NewCoin(b.asset, assetAmt), b.asset.GetSyntheticAsset(), b.holder)
	case opSwapSynthToRune, opSwapSynthToAsset:
		amt := luviFuzzAmount(before.synths, aux, frac)
		desc = fmt.Sprintf("%s %s", name, amt)
		if err = r.depositSynths(ctx, amt); err != nil {
			return desc, err
		}
		target, dest := common.RuneAsset(), GetRandomTHORAddress()
		if kind == opSwapSynthToAsset {
			target, dest = b.asset, GetRandomETHAddress()
		}
		return desc, r.swap(ctx, common.NewCoin(b.asset.GetSyntheticAsset(), amt), target, dest)
	case opDonate:
		desc = fmt.Sprintf("%s rune=%s asset=%s", name, runeAmt, assetAmt)
		tx := common.NewTx(GetRandomTxHash(), b.lpETH[lp], b.lpETH[lp],
			common.Coins{common.NewCoin(common.RuneAsset(), runeAmt), common.NewCoin(b.asset, assetAmt)},
			common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())}, "DONATE:"+b.asset.String())
		return desc, NewDonateHandler(r.b.mgr).handle(ctx, *NewMsgDonate(tx, b.asset, runeAmt, assetAmt, GetRandomBech32Addr()))
	}
	return "unknown", fmt.Errorf("unknown op %d", kind)
}

// depositSynths moves synths into Asgard as an inbound deposit would, taking
// them from the holder when Asgard (which holds the initial supply) is short.
func (r *luviFuzzRun) depositSynths(ctx cosmos.Context, amt cosmos.Uint) error {
	k, synth := r.b.mgr.Keeper(), r.b.asset.GetSyntheticAsset()
	have := k.GetBalanceOfModule(ctx, AsgardName, synth.Native())
	if have.GTE(amt) {
		return nil
	}
	acc, err := r.b.holder.AccAddress()
	if err != nil {
		return err
	}
	return k.SendFromAccountToModule(ctx, acc, AsgardName, common.NewCoins(common.NewCoin(synth, amt.Sub(have))))
}

func (r *luviFuzzRun) swap(ctx cosmos.Context, coin common.Coin, target common.Asset, dest common.Address) error {
	chain := coin.Asset.GetChain()
	if coin.Asset.IsSyntheticAsset() || coin.Asset.IsRune() {
		chain = common.THORChain
	}
	from := dest
	if !coin.Asset.IsSyntheticAsset() && !coin.Asset.IsRune() {
		from = GetRandomETHAddress()
	}
	k := r.b.mgr.Keeper()
	pool, err := k.GetPool(ctx, r.b.asset)
	if err != nil {
		return err
	}
	synths := k.GetTotalSupply(ctx, r.b.asset.GetSyntheticAsset())
	tx := common.NewTx(GetRandomTxHash(), from, dest, common.NewCoins(coin), common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())}, "")
	tx.Chain = chain
	_, _, evts, err := newSwapper().Swap(ctx, k, tx, target, dest, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, r.mult, r.b.mgr, nil, nil)
	if err != nil || len(evts) != 2 {
		return err
	}
	// rebuild the pool after the first leg (X -> RUNE)
	first := evts[0]
	in := first.InTx.Coins[0]
	pool.BalanceRune = common.SafeSub(pool.BalanceRune, first.EmitAsset.Amount.Add(first.LiquidityFeeInRune))
	if in.Asset.IsSyntheticAsset() {
		synths = common.SafeSub(synths, in.Amount)
	} else {
		pool.BalanceAsset = pool.BalanceAsset.Add(in.Amount)
	}
	mid := types2.NewLuviState(pool, synths)
	r.mid = &mid
	return nil
}

// luviFuzzSeeds returns realistic starting points: a mainnet-like pool with no
// synths, one with heavy synths, one at the S = 2A boundary, one in overhang,
// a small pool where rounding is large, and sequences that exercise every op.
func luviFuzzSeeds() [][]byte {
	op := func(kind luviFuzzOp, aux byte, frac uint16) []byte {
		return []byte{byte(kind), aux, byte(frac >> 8), byte(frac)}
	}
	all := func() []byte {
		var s []byte
		for k := luviFuzzOp(0); k < luviFuzzOpCount; k++ {
			s = append(s, op(k, byte(k)%4|0x40, 0x0800+uint16(k)*0x0111)...)
		}
		return s
	}
	headers := [][]byte{
		{9, 7, 9, 3, 0, 2},    // 1e15 RUNE, 1e11 asset (mainnet-like ETH), no synths
		{9, 7, 9, 3, 45, 2},   // heavy synths (180%)
		{9, 7, 9, 3, 50, 2},   // S = 2A
		{9, 7, 9, 3, 75, 2},   // overhang (300%)
		{9, 7, 9, 3, 175, 0},  // deep overhang (700%)
		{0, 0, 0, 0, 10, 1},   // tiny pool: 1e6 RUNE, 1e4 asset
		{99, 8, 99, 10, 1, 2}, // huge pool
	}
	var seeds [][]byte
	for _, h := range headers {
		seeds = append(seeds, append(append([]byte{}, h...), all()...))
	}
	// LP lifecycle: other LPs add asymmetrically, everyone withdraws
	life := append([]byte{}, headers[1]...)
	life = append(life, op(opAddRune, 0x31, 0x4000)...)
	life = append(life, op(opAddAsset, 0x22, 0x4000)...)
	life = append(life, op(opSwapRuneToSynth, 0x20, 0x2000)...)
	life = append(life, op(opWithdraw, 0x05, 4_999)...)
	life = append(life, op(opWithdraw, 0x09, 9_999)...)
	life = append(life, op(opSwapSynthToAsset, 0x10, 0x8000)...)
	life = append(life, op(opWithdraw, 0x00, 9_999)...)
	seeds = append(seeds, life)
	return seeds
}
