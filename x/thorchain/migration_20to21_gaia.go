//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// GAIA.ATOM write-down. Five GAIA inbounds (THOR h28,296,457–28,296,522) were
// observed into vault ...jy4h and refunded with real ATOM, but the vault never
// received them on the Cosmos Hub. The vault's books therefore carry
// 515.766222 ATOM it does not hold, which solvency-halted GAIA.
//
// The step writes the phantom amount off both the vault and the GAIA.ATOM pool,
// and pairs Reserve RUNE into the pool so its constant product is unchanged.
// After arbitrage back to the market price the pool returns to its pre-loss
// depth, so LPs and savers are made whole and the Reserve bears the loss.
const (
	gaiaWritedownVault = "thorpub1addwnpepqvlena222zluk5p3r0s9uey758rd43qedtxf2xalprynuaxp69rscr0jy4h"
	// gaiaWritedownMaxRune hard-caps the Reserve RUNE paired into the pool at about
	// twice the 1,394.57555508 RUNE the pairing needs at the h28,297,751 depths
	// (78,997.31887692 ATOM / 212,205.55008924 RUNE). A pool pushed toward RUNE
	// before the upgrade pulls at most the cap: above it the pairing is clamped,
	// not aborted, so the write-down still restores solvency.
	gaiaWritedownMaxRune = uint64(280_000_000_000) // 2,800 RUNE
)

// gaiaPhantomInbounds are the observed inbounds the vault never received, pinned to
// their observed GAIA.ATOM amounts. The write-down is their sum: 51,576,622,200
// (515.766222 ATOM). The vault's Hub balance at h28,297,751 was 515.766217 ATOM below
// its books, so the write-down leaves the vault 5 uatom over-collateralized.
var gaiaPhantomInbounds = []struct {
	txID   string
	amount uint64
}{
	{"99CA561549738DF9D0FD6F0E44A60365B2F4D099C1B56468364D506FA584F9D8", 1_676_719_200},
	{"020DFE5F418CDF2E97A6911A7F2C12CBB90E611BB3D2144A666C864F45BE01E8", 3_337_989_500},
	{"B5B753C3A6202AA7F48E351E2F7061B4275B677EE7F66C945E575456C7389818", 6_660_530_000},
	{"8F7F83BAF6ADFF91F2C4376B0EFBF18F626341F368A8E8C9726903009F0A2E98", 13_305_610_900},
	{"649787F70CE80FF68A673A9F6DF6B8B41E5C0D0ABD5D6A039C4C0E792CF62EC8", 26_595_772_600},
}

// migrate20to21Mainnet writes down the phantom GAIA.ATOM on vault ...jy4h and
// replenishes the GAIA.ATOM pool with Reserve RUNE (soft-skip on drift).
func (m Migrator) migrate20to21Mainnet(ctx cosmos.Context) error {
	runGaiaWritedown20to21(ctx, m.mgr)
	return nil
}

// runGaiaWritedown20to21 applies the write-down atomically or not at all: if the
// pinned inbounds, the vault, the pool or the Reserve no longer match, it logs and
// skips rather than halting the upgrade.
func runGaiaWritedown20to21(ctx cosmos.Context, mgr *Mgrs) {
	cacheCtx, commit := ctx.CacheContext()
	if err := applyGaiaWritedown20to21(cacheCtx, mgr); err != nil {
		ctx.Logger().Error("GAIA.ATOM write-down skipped", "error", err)
		return
	}
	commit()
}

func applyGaiaWritedown20to21(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()
	pubKey, err := common.NewPubKey(gaiaWritedownVault)
	if err != nil {
		return fmt.Errorf("parse vault pubkey: %w", err)
	}

	// Confirm each pinned inbound against its observed voter, so the write-down is
	// THORChain's own record of what was credited to the vault.
	writedown := cosmos.ZeroUint()
	for _, in := range gaiaPhantomInbounds {
		voter, voterErr := k.GetObservedTxInVoter(ctx, common.TxID(in.txID))
		if voterErr != nil {
			return fmt.Errorf("get observed inbound %s: %w", in.txID, voterErr)
		}
		tx := voter.Tx
		amount := cosmos.NewUint(in.amount)
		if !tx.ObservedPubKey.Equals(pubKey) ||
			!tx.Tx.Chain.Equals(common.GAIAChain) ||
			len(tx.Tx.Coins) != 1 ||
			!tx.Tx.Coins[0].Asset.Equals(common.ATOMAsset) ||
			!tx.Tx.Coins[0].Amount.Equal(amount) {
			return fmt.Errorf("observed inbound %s does not match the pinned %s GAIA.ATOM into the vault", in.txID, amount)
		}
		writedown = writedown.Add(amount)
	}

	vault, err := k.GetVault(ctx, pubKey)
	if err != nil {
		return fmt.Errorf("get vault: %w", err)
	}
	held := vault.GetCoin(common.ATOMAsset).Amount
	if held.LT(writedown) {
		return fmt.Errorf("vault GAIA.ATOM %s below write-down %s", held, writedown)
	}

	pool, err := k.GetPool(ctx, common.ATOMAsset)
	if err != nil {
		return fmt.Errorf("get pool: %w", err)
	}
	if !pool.IsAvailable() || pool.BalanceAsset.LTE(writedown.MulUint64(2)) {
		return fmt.Errorf("pool %s (status %s, asset depth %s) cannot absorb write-down %s",
			common.ATOMAsset, pool.Status, pool.BalanceAsset, writedown)
	}

	// Pair RUNE so the constant product is unchanged: (R+ΔR)·(A−ΔA) = R·A.
	remaining := pool.BalanceAsset.Sub(writedown)
	runeToAdd := common.GetUncappedShare(writedown, remaining, pool.BalanceRune)
	if maxRune := cosmos.NewUint(gaiaWritedownMaxRune); runeToAdd.GT(maxRune) {
		ctx.Logger().Info("GAIA.ATOM write-down RUNE pairing clamped to cap", "uncapped", runeToAdd, "cap", maxRune)
		runeToAdd = maxRune
	}
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(runeToAdd) {
		return fmt.Errorf("reserve %s below GAIA.ATOM write-down pairing %s", bal, runeToAdd)
	}
	if err := k.SendFromModuleToModule(ctx, ReserveName, AsgardName, common.NewCoins(common.NewCoin(common.RuneAsset(), runeToAdd))); err != nil {
		return fmt.Errorf("reserve->asgard pairing RUNE: %w", err)
	}

	vault.SubFundsWithClamping(common.NewCoins(common.NewCoin(common.ATOMAsset, writedown)))
	if err := k.SetVault(ctx, vault); err != nil {
		return fmt.Errorf("set vault: %w", err)
	}

	oldAsset, oldRune := pool.BalanceAsset, pool.BalanceRune
	pool.BalanceAsset = remaining
	pool.BalanceRune = pool.BalanceRune.Add(runeToAdd)
	if err := k.SetPool(ctx, pool); err != nil {
		return fmt.Errorf("set pool: %w", err)
	}
	// Emit the depth change so Midgard (event-sourced) tracks the write-down and the
	// paired RUNE. A skipped step's cache is discarded, so no event leaks.
	emitPoolBalanceChangedEvent(ctx, NewPoolMod(common.ATOMAsset, runeToAdd, true, writedown, false), "gaia_atom_writedown_20to21", mgr)

	ctx.Logger().Info("GAIA.ATOM written down",
		"vault", gaiaWritedownVault, "writedown", writedown,
		"vault_before", held, "vault_after", vault.GetCoin(common.ATOMAsset).Amount,
		"pool_asset_before", oldAsset, "pool_asset_after", pool.BalanceAsset,
		"pool_rune_before", oldRune, "pool_rune_after", pool.BalanceRune,
		"rune_paired", runeToAdd)
	return nil
}
