package keeperv1

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	storetypes "cosmossdk.io/store/types"
	upgradekeeper "cosmossdk.io/x/upgrade/keeper"
	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/telemetry"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// NOTE: Always end a dbPrefix with a slash ("/"). This is to ensure that there
// are no prefixes that contain another prefix. In the scenario where this is
// true, an iterator for a specific type, will get more than intended, and may
// include a different type. The slash is used to protect us from this
// scenario.
// Also, use underscores between words and use lowercase characters only

const (
	prefixObservedTxIn              types.DbPrefix = "observed_tx_in/"
	prefixObservedTxOut             types.DbPrefix = "observed_tx_out/"
	prefixObservedLink              types.DbPrefix = "ob_link/"
	prefixRefundCleanup             types.DbPrefix = "refund_cleanup/"
	prefixRefundCleanupExpiry       types.DbPrefix = "refund_cleanup_expiry/"
	prefixPool                      types.DbPrefix = "pool/"
	prefixPoolLUVI                  types.DbPrefix = "luvi/"
	prefixPoolLuviAccumulator       types.DbPrefix = "pool_luvi/"
	prefixTxOut                     types.DbPrefix = "txout/"
	prefixTotalLiquidityFee         types.DbPrefix = "total_liquidity_fee/"
	prefixPoolLiquidityFee          types.DbPrefix = "pool_liquidity_fee/"
	prefixPoolSwapSlip              types.DbPrefix = "pool_swap_slip/"
	prefixPoolSwapSlipLong          types.DbPrefix = "pool_swap_slip_long/"
	prefixPoolSwapSnapShot          types.DbPrefix = "pool_swap_slip_ss/"
	prefixLiquidityProvider         types.DbPrefix = "lp/"
	prefixLastChainHeight           types.DbPrefix = "last_chain_height/"
	prefixLastSignedHeight          types.DbPrefix = "last_signed_height/"
	prefixLastObserveHeight         types.DbPrefix = "last_observe_height/"
	prefixLastKeygenChain           types.DbPrefix = "last_keygen_chain/"
	prefixLastKeygenReport          types.DbPrefix = "last_keygen_report/"
	prefixChainSetStall             types.DbPrefix = "chain_set_stall/"
	prefixObservationTallyDue       types.DbPrefix = "observation_tally_due/"
	prefixObservationTotal          types.DbPrefix = "observation_tally_total/"
	prefixObservationMissed         types.DbPrefix = "observation_tally_missed/"
	prefixObservationBase           types.DbPrefix = "observation_tally_base/"
	prefixObservationGuard          types.DbPrefix = "observation_tally_guard/"
	prefixObservationGuardFull      types.DbPrefix = "observation_tally_guard_full/"
	prefixNodeAccount               types.DbPrefix = "node_account/"
	prefixBondProviders             types.DbPrefix = "bond_providers/"
	prefixVault                     types.DbPrefix = "vault/"
	prefixVaultAsgardIndex          types.DbPrefix = "vault_asgard_index/"
	prefixVaultAsgardEDDSAIndex     types.DbPrefix = "vault_asgard_eddsa_index/"
	prefixVaultAsgardFrostIndex     types.DbPrefix = "vault_asgard_frost_index/"
	prefixXMRSpentOutputRef         types.DbPrefix = "xmr_spent_refs/"
	prefixSolVaultNonce             types.DbPrefix = "sol_vault_nonce/"
	prefixSolNonceAttestVoter       types.DbPrefix = "sol_nonce_attest/"
	prefixNetwork                   types.DbPrefix = "network/"
	prefixSwapperClout              types.DbPrefix = "sclout/"
	prefixPOL                       types.DbPrefix = "pol/"
	prefixPOLReserve                types.DbPrefix = "pol_reserve/"
	prefixStableReserveCredit       types.DbPrefix = "stable_reserve_credit/"
	prefixTradeAccount              types.DbPrefix = "tr_acct/"
	prefixSecuredAsset              types.DbPrefix = "sa/"
	prefixRUNEProvider              types.DbPrefix = "rune_provider/"
	prefixRUNEPool                  types.DbPrefix = "rune_pool/"
	prefixTradeUnit                 types.DbPrefix = "tr_unit/"
	prefixStreamingSwap             types.DbPrefix = "stream/"
	prefixObservingAddresses        types.DbPrefix = "observing_addresses/"
	prefixTss                       types.DbPrefix = "tss/"
	prefixTssKeysignFailure         types.DbPrefix = "tssKeysignFailure/"
	prefixKeygen                    types.DbPrefix = "keygen/"
	prefixRagnarokHeight            types.DbPrefix = "ragnarokHeight/"
	prefixRagnarokNth               types.DbPrefix = "ragnarokNth/"
	prefixRagnarokPending           types.DbPrefix = "ragnarokPending/"
	prefixRagnarokPosition          types.DbPrefix = "ragnarokPosition/"
	prefixRagnarokPoolHeight        types.DbPrefix = "ragnarokPool/"
	prefixErrataTx                  types.DbPrefix = "errata/"
	prefixBanVoter                  types.DbPrefix = "ban/"
	prefixNodeSlashPoints           types.DbPrefix = "slash/"
	prefixNodeJail                  types.DbPrefix = "jail/"
	prefixSwapQueueItem             types.DbPrefix = "swapitem/"
	prefixAdvSwapQueueItem          types.DbPrefix = "aq/"
	prefixAdvSwapQueueLimitIndex    types.DbPrefix = "aqlim/"
	prefixAdvSwapQueueMarketIndex   types.DbPrefix = "aqmark/"
	prefixAdvSwapQueueTTL           types.DbPrefix = "aqttl/"
	prefixOutboundFeeWithheldRune   types.DbPrefix = "outbound_fee_withheld_rune/"
	prefixOutboundFeeSpentRune      types.DbPrefix = "outbound_fee_spent_rune/"
	prefixMimir                     types.DbPrefix = "mimir/"
	prefixMinJoinLast               types.DbPrefix = "minjoinlast/"
	prefixNodeMimir                 types.DbPrefix = "nodemimir/"
	prefixNodePauseChain            types.DbPrefix = "node_pause_chain/"
	prefixNetworkFee                types.DbPrefix = "network_fee/"
	prefixNetworkFeeVoter           types.DbPrefix = "network_fee_voter/"
	prefixTssKeygenMetric           types.DbPrefix = "tss_keygen_metric/"
	prefixTssKeysignMetric          types.DbPrefix = "tss_keysign_metric/"
	prefixTssKeysignMetricLatest    types.DbPrefix = "latest_tss_keysign_metric/"
	prefixChainContract             types.DbPrefix = "chain_contract/"
	prefixSolvencyVoter             types.DbPrefix = "solvency_voter/"
	prefixSolvencyVoterQuota        types.DbPrefix = "solvency_voter_quota/"
	prefixTHORName                  types.DbPrefix = "thorname/"
	prefixAffiliateCollector        types.DbPrefix = "affcol_name/"
	prefixAffiliateLiquidityFee     types.DbPrefix = "affiliate_liquidity_fee/"
	prefixReferenceMemo             types.DbPrefix = "refm/"
	prefixReferenceMemoIndex        types.DbPrefix = "refmi/"
	prefixReferenceMemoHash         types.DbPrefix = "refmh/"
	prefixReferenceMemoWindow       types.DbPrefix = "refmw/"
	prefixRollingPoolLiquidityFee   types.DbPrefix = "rolling_pool_liquidity_fee/"
	prefixPrevCyclePoolLiquidityFee types.DbPrefix = "prev_cycle_pool_liquidity_fee/"
	prefixVersion                   types.DbPrefix = "version/"
	prefixUpgradeProposals          types.DbPrefix = "upgr_props/"
	prefixUpgradeVotes              types.DbPrefix = "upgr_votes/"
	prefixTCYClaimer                types.DbPrefix = "tcy_claimer/"
	prefixTCYStaker                 types.DbPrefix = "tcy_staker/"
	prefixOraclePrice               types.DbPrefix = "oracle_price/"
	prefixVolumeBucket              types.DbPrefix = "volume_bucket/"
	prefixVolume                    types.DbPrefix = "volume/"
	prefixDynamicFee                types.DbPrefix = "dynfee/"
	prefixDynamicFeeAcc             types.DbPrefix = "dynfeeacc/"
	prefixDynamicFeeEpochBlocks     types.DbPrefix = "dynfeeebo/"
)

func dbError(ctx cosmos.Context, wrapper string, err error) error {
	err = fmt.Errorf("KVStore Error: %s: %w", wrapper, err)
	ctx.Logger().Error("keeper error", "error", err)
	return err
}

// KVStore Keeper maintains the link to data storage and exposes getter/setter methods for the various parts of the state machine
type KVStore struct {
	cdc           codec.BinaryCodec
	coinKeeper    bankkeeper.Keeper
	accountKeeper authkeeper.AccountKeeper
	upgradeKeeper *upgradekeeper.Keeper
	storeKey      *storetypes.KVStoreKey
	version       semver.Version
	constAccessor constants.ConstantValues
}

// NewKVStore creates new instances of the thorchain Keeper
func NewKVStore(cdc codec.BinaryCodec, storeKey *storetypes.KVStoreKey, coinKeeper bankkeeper.Keeper, accountKeeper authkeeper.AccountKeeper, upgradeKeeper *upgradekeeper.Keeper, version semver.Version) KVStore {
	return KVStore{
		coinKeeper:    coinKeeper,
		accountKeeper: accountKeeper,
		upgradeKeeper: upgradeKeeper,
		storeKey:      storeKey,
		cdc:           cdc,
		version:       version,
		constAccessor: constants.GetConstantValues(version),
	}
}

// NewKeeper creates new instances of the thorchain Keeper
func NewKeeper(cdc codec.BinaryCodec, storeKey *storetypes.KVStoreKey, coinKeeper bankkeeper.Keeper, accountKeeper authkeeper.AccountKeeper, upgradeKeeper *upgradekeeper.Keeper) keeper.Keeper {
	version := semver.MustParse("0.0.0")
	return NewKVStore(cdc, storeKey, coinKeeper, accountKeeper, upgradeKeeper, version)
}

// kvStore returns the thorchain module store for ctx. It is the same gas-metered
// store that runtime.KVStoreAdapter(storeService.OpenKVStore(ctx)) returns, opened
// directly from the sdk.Context so the context is not boxed into a
// context.Context (a heap copy of the whole struct) on every store access.
func (k KVStore) kvStore(ctx cosmos.Context) storetypes.KVStore {
	return ctx.KVStore(k.storeKey)
}

// Cdc return the amino codec
func (k KVStore) Cdc() codec.BinaryCodec {
	return k.cdc
}

// GetVersion return the current version
func (k KVStore) GetVersion() semver.Version {
	return k.version
}

func (k *KVStore) SetVersion(ver semver.Version) {
	k.version = ver
}

// GetKey return a key that can be used to store into key value store
func (k KVStore) GetKey(prefix types.DbPrefix, key string, other ...string) []byte {
	// prefix + "/" + upper(key) + upper(other...), built in one buffer: this runs
	// for every keeper read and write
	size := len(prefix) + 1 + len(key)
	for _, item := range other {
		size += len(item)
	}
	newKey := make([]byte, 0, size)
	newKey = append(newKey, prefix...)
	newKey = append(newKey, '/')
	newKey = appendUpper(newKey, key)

	// TODO: should this handle the slashes automatically?
	// ref: x/thorchain/keeper/v1/keeper_last_height.go#GetLastObserveHeight
	for _, item := range other {
		newKey = appendUpper(newKey, item)
	}

	return newKey
}

// appendUpper appends strings.ToUpper(s) to dst. An ASCII s, as store keys
// are, is upper-cased byte by byte straight into dst instead of through an
// intermediate string; anything else goes through strings.ToUpper.
func appendUpper(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return append(dst, strings.ToUpper(s)...)
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		dst = append(dst, c)
	}
	return dst
}

// getKeyInt64 returns GetKey(prefix, strconv.FormatInt(n, 10)) without the
// intermediate digit string: digits and '-' have no upper case.
func (k KVStore) getKeyInt64(prefix types.DbPrefix, n int64) []byte {
	var digits [20]byte // len("-9223372036854775808")
	d := strconv.AppendInt(digits[:0], n, 10)
	newKey := make([]byte, 0, len(prefix)+1+len(d))
	newKey = append(newKey, prefix...)
	newKey = append(newKey, '/')
	return append(newKey, d...)
}

// getIterator - get an iterator for given prefix
func (k KVStore) getIterator(ctx cosmos.Context, prefix types.DbPrefix) cosmos.Iterator {
	store := k.kvStore(ctx)
	return cosmos.KVStorePrefixIterator(store, []byte(prefix))
}

func (k KVStore) DeleteKey(ctx cosmos.Context, key []byte) {
	k.del(ctx, key)
}

// del - delete data from the kvstore
func (k KVStore) del(ctx cosmos.Context, key []byte) {
	if bytes.HasPrefix(key, []byte(prefixNodeAccount)) {
		keeper.ActiveValidatorsCacheFromContext(ctx).Disable()
	}
	if bytes.HasPrefix(key, []byte(prefixTxOut)) {
		keeper.TxOutCacheFromContext(ctx).Disable()
	}
	store := k.kvStore(ctx)
	if store.Has(key) {
		store.Delete(key)
	}
}

// has - kvstore has key
func (k KVStore) has(ctx cosmos.Context, key []byte) bool {
	store := k.kvStore(ctx)
	return store.Has(key)
}

func (k KVStore) setInt64(ctx cosmos.Context, key []byte, record int64) {
	store := k.kvStore(ctx)
	value := ProtoInt64{Value: record}
	buf := k.cdc.MustMarshal(&value)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getInt64(ctx cosmos.Context, key []byte, record *int64) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	// The getters decode a zero wrapper with its own Unmarshal, which is what
	// k.cdc.Unmarshal does after resetting it (the wrappers hold no Any), and
	// name record's type as %T would: through the codec interface the wrapper,
	// and through fmt every caller's record, would move to the heap.
	value := ProtoInt64{}
	bz := store.Get(key)
	if err := value.Unmarshal(bz); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (*int64) %s", key), err)
	}
	*record = value.GetValue()
	return true, nil
}

func (k KVStore) setUint64(ctx cosmos.Context, key []byte, record uint64) {
	store := k.kvStore(ctx)
	value := ProtoUint64{Value: record}
	buf := k.cdc.MustMarshal(&value)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getUint64(ctx cosmos.Context, key []byte, record *uint64) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	var value ProtoUint64
	bz := store.Get(key)
	if err := value.Unmarshal(bz); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (*uint64) %s", key), err)
	}
	*record = value.GetValue()
	return true, nil
}

func (k KVStore) setAccAddresses(ctx cosmos.Context, key []byte, record []cosmos.AccAddress) {
	store := k.kvStore(ctx)
	value := ProtoAccAddresses{Value: record}
	buf := k.cdc.MustMarshal(&value)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getAccAddresses(ctx cosmos.Context, key []byte, record *[]cosmos.AccAddress) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	var value ProtoAccAddresses
	bz := store.Get(key)
	if err := value.Unmarshal(bz); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (*[]types.AccAddress) %s", key), err)
	}
	*record = value.Value
	return true, nil
}

func (k KVStore) setString(ctx cosmos.Context, key, record string) {
	store := k.kvStore(ctx)
	value := ProtoString{Value: record}
	buf := k.cdc.MustMarshal(&value)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getString(ctx cosmos.Context, key string, record *string) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has([]byte(key)) {
		return false, nil
	}

	var value ProtoString
	bz := store.Get([]byte(key))
	if err := value.Unmarshal(bz); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (*string) %s", key), err)
	}
	*record = value.Value
	return true, nil
}

func (k KVStore) setStrings(ctx cosmos.Context, key []byte, record []string) {
	store := k.kvStore(ctx)
	if len(record) == 0 {
		store.Delete(key)
		return
	}
	value := ProtoStrings{Value: record}
	buf := k.cdc.MustMarshal(&value)
	store.Set(key, buf)
}

func (k KVStore) getStrings(ctx cosmos.Context, key []byte, record *[]string) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	var value ProtoStrings
	bz := store.Get(key)
	if err := value.Unmarshal(bz); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (*[]string) %s", key), err)
	}
	*record = value.Value
	return true, nil
}

func (k KVStore) setUint(ctx cosmos.Context, key []byte, record cosmos.Uint) {
	store := k.kvStore(ctx)
	value := ProtoUint{Value: record}
	buf := k.cdc.MustMarshal(&value)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getUint(ctx cosmos.Context, key []byte, record *cosmos.Uint) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	var value ProtoUint
	bz := store.Get(key)
	if err := value.Unmarshal(bz); err != nil {
		return false, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (*math.Uint) %s", key), err)
	}
	*record = value.Value
	return true, nil
}

// GetRuneBalanceOfModule get the RUNE balance
func (k KVStore) GetRuneBalanceOfModule(ctx cosmos.Context, moduleName string) cosmos.Uint {
	return k.GetBalanceOfModule(ctx, moduleName, common.RuneNative.Native())
}

func (k KVStore) GetBalanceOfModule(ctx cosmos.Context, moduleName, denom string) cosmos.Uint {
	addr := k.accountKeeper.GetModuleAddress(moduleName)
	coin := k.coinKeeper.GetBalance(ctx, addr, denom)
	return cosmos.NewUintFromBigInt(coin.Amount.BigInt())
}

// SendFromModuleToModule transfer asset from one module to another
func (k KVStore) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	cosmosCoins := make(cosmos.Coins, len(coins))
	for i, c := range coins {
		cosmosCoins[i] = cosmos.NewCoin(c.Asset.Native(), cosmos.NewIntFromBigInt(c.Amount.BigInt()))
	}
	return k.coinKeeper.SendCoinsFromModuleToModule(ctx, from, to, cosmosCoins)
}

func (k KVStore) SendCoins(ctx cosmos.Context, from, to cosmos.AccAddress, coins cosmos.Coins) error {
	return k.coinKeeper.SendCoins(ctx, from, to, coins)
}

// SendFromAccountToModule transfer fund from one account to a module
func (k KVStore) SendFromAccountToModule(ctx cosmos.Context, from cosmos.AccAddress, to string, coins common.Coins) error {
	cosmosCoins := make(cosmos.Coins, len(coins))
	for i, c := range coins {
		cosmosCoins[i] = cosmos.NewCoin(c.Asset.Native(), cosmos.NewIntFromBigInt(c.Amount.BigInt()))
	}
	return k.coinKeeper.SendCoinsFromAccountToModule(ctx, from, to, cosmosCoins)
}

// SendFromModuleToAccount transfer fund from module to an account
func (k KVStore) SendFromModuleToAccount(ctx cosmos.Context, from string, to cosmos.AccAddress, coins common.Coins) error {
	cosmosCoins := make(cosmos.Coins, len(coins))
	for i, c := range coins {
		cosmosCoins[i] = cosmos.NewCoin(c.Asset.Native(), cosmos.NewIntFromBigInt(c.Amount.BigInt()))
	}
	return k.coinKeeper.SendCoinsFromModuleToAccount(ctx, from, to, cosmosCoins)
}

func (k KVStore) BurnFromModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if coin.Asset.IsSyntheticAsset() {
		keeper.RecordLuviTouch(ctx, coin.Asset, k.luviCapture(ctx))
	}
	coinToBurn, err := coin.Native()
	if err != nil {
		return fmt.Errorf("fail to parse coins: %w", err)
	}
	coinsToBurn := cosmos.Coins{coinToBurn}
	err = k.coinKeeper.BurnCoins(ctx, module, coinsToBurn)
	if err != nil {
		return fmt.Errorf("fail to burn assets: %w", err)
	}

	return nil
}

// BurnEntireSynthSupply burns every unit of the given synth denom from every
// holder account, reducing the bank supply to zero. Used by store migrations to
// write off settled ThorFi-era synth residuals. For each holder it sends the
// balance to the thorchain module and burns it.
func (k KVStore) BurnEntireSynthSupply(ctx cosmos.Context, synthAsset common.Asset) (cosmos.Uint, error) {
	keeper.RecordLuviTouch(ctx, synthAsset, k.luviCapture(ctx))
	denom := synthAsset.Native()
	type holderBalance struct {
		addr cosmos.AccAddress
		amt  cosmos.Coin
	}
	var holders []holderBalance
	k.coinKeeper.IterateAllBalances(ctx, func(addr cosmos.AccAddress, coin cosmos.Coin) bool {
		if coin.Denom == denom && coin.Amount.IsPositive() {
			holders = append(holders, holderBalance{addr: addr, amt: coin})
		}
		return false
	})
	total := cosmos.ZeroUint()
	for _, h := range holders {
		coins := cosmos.NewCoins(h.amt)
		if err := k.coinKeeper.SendCoinsFromAccountToModule(ctx, h.addr, ModuleName, coins); err != nil {
			return total, fmt.Errorf("fail to send %s synth from %s to module: %w", synthAsset, h.addr, err)
		}
		if err := k.coinKeeper.BurnCoins(ctx, ModuleName, coins); err != nil {
			return total, fmt.Errorf("fail to burn %s synth from module: %w", synthAsset, err)
		}
		total = total.Add(cosmos.NewUintFromBigInt(h.amt.Amount.BigInt()))
	}
	return total, nil
}

func (k KVStore) MintToModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if coin.Asset.IsSyntheticAsset() {
		keeper.RecordLuviTouch(ctx, coin.Asset, k.luviCapture(ctx))
	}
	// circuit breaker
	// mint new rune coins until we hit the cap. Once we do, borrow
	// from the reserve instead of minting new tokens
	// from the reserve instead of minting new tokens
	maxAmt, _ := k.GetMimir(ctx, constants.MaxRuneSupply.String())
	if coin.IsRune() && maxAmt > 0 {
		currentSupply := k.GetTotalSupply(ctx, common.RuneAsset())  // current circulating supply of rune
		maxSupply := cosmos.NewUint(uint64(maxAmt))                 // max supply of rune (ie 500m)
		availableSupply := common.SafeSub(maxSupply, currentSupply) // available supply to be mint
		// if available supply is less than the coin.Amount, we need to
		// borrow from the reserve
		if availableSupply.LT(coin.Amount) {
			// Never mint an amount that would exceed MaxRuneSupply.
			borrowReserveAmt := common.SafeSub(coin.Amount, availableSupply) // to borrow from reserve
			coin.Amount = common.SafeSub(coin.Amount, borrowReserveAmt)      // to mint later in this func

			reserveCoin := common.NewCoin(common.RuneAsset(), borrowReserveAmt)
			if err := k.SendFromModuleToModule(ctx, ReserveName, module, common.NewCoins(reserveCoin)); err != nil {
				// If unable to move the needed surplus coin from the Reserve, error out without any minting.
				ctx.Logger().Error("fail to move coins during circuit breaker", "error", err)
				return err
			}

			// The supply cap has begun converting minting into Reserve drainage.
			// Guard the uint64 conversion: this is a handler path with no
			// recover(), and cosmos.Uint.Uint64() panics when out of range.
			borrowTelem := float32(0)
			if borrowReserveAmt.BigInt().IsUint64() {
				borrowTelem = float32(borrowReserveAmt.Uint64()) / 100000000
			}
			// Quote simulation reaches this through synth->RUNE swaps; counters
			// are process-local, so API traffic must not count as borrowing.
			if sim, _ := ctx.Value(constants.CtxSimulationMode).(bool); !sim {
				telemetry.IncrCounter(1, "thornode", "rune_supply", "reserve_borrow")
				telemetry.IncrCounter(borrowTelem, "thornode", "rune_supply", "reserve_borrow_amount")
			}
		}
	}
	if coin.Amount.IsZero() {
		// Don't proceed if the remaining amount to mint is zero.
		return nil
	}

	coinToMint, err := coin.Native()
	if err != nil {
		return fmt.Errorf("fail to parse coins: %w", err)
	}
	coinsToMint := cosmos.Coins{coinToMint}
	err = k.coinKeeper.MintCoins(ctx, module, coinsToMint)
	if err != nil {
		return fmt.Errorf("fail to mint assets: %w", err)
	}

	// check if we've exceeded max rune supply cap. If we have, there could
	// be an issue (infinite mint bug/exploit), or maybe runway rune
	// hyperinflation. In any case, pause everything and allow the
	// community time to find a solution to the issue.
	coin2 := k.coinKeeper.GetSupply(ctx, common.RuneAsset().Native())
	maxAmt, _ = k.GetMimir(ctx, "MaxRuneSupply")
	if maxAmt > 0 && coin2.Amount.GT(cosmos.NewInt(maxAmt)) {
		k.SetMimir(ctx, "HaltTrading", 1)
		k.SetMimir(ctx, "HaltChainGlobal", 1)
		k.SetMimir(ctx, "PauseLP", 1)
		k.SetMimir(ctx, "HaltTHORChain", 1)
	}

	return nil
}

func (k KVStore) MintAndSendToAccount(ctx cosmos.Context, to cosmos.AccAddress, coin common.Coin) error {
	// Mint coins into the reserve
	if err := k.MintToModule(ctx, ModuleName, coin); err != nil {
		return err
	}
	return k.SendFromModuleToAccount(ctx, ModuleName, to, common.NewCoins(coin))
}

func (k KVStore) GetModuleAddress(module string) (common.Address, error) {
	return common.NewAddress(k.accountKeeper.GetModuleAddress(module).String())
}

func (k KVStore) GetModuleAccAddress(module string) cosmos.AccAddress {
	return k.accountKeeper.GetModuleAddress(module)
}

// IsModuleAccount includes configured modules whose accounts are not yet created.
func (k KVStore) IsModuleAccount(ctx cosmos.Context, addr cosmos.AccAddress) bool {
	if addr.Empty() {
		return false
	}
	// This loop and the methods it calls perform no gas-metered operations.
	// analyze-ignore(map-iteration) - membership only; order cannot change the result or gas usage.
	for _, permissions := range k.accountKeeper.GetModulePermissions() {
		if permissions.GetAddress().Equals(addr) {
			return true
		}
	}
	_, module := k.accountKeeper.GetAccount(ctx, addr).(sdk.ModuleAccountI)
	return module
}

func (k KVStore) GetBalance(ctx cosmos.Context, addr cosmos.AccAddress) cosmos.Coins {
	return k.coinKeeper.GetAllBalances(ctx, addr)
}

func (k KVStore) GetBalanceOf(ctx cosmos.Context, addr cosmos.AccAddress, asset common.Asset) cosmos.Coin {
	return k.coinKeeper.GetBalance(ctx, addr, asset.Native())
}

func (k KVStore) HasCoins(ctx cosmos.Context, addr cosmos.AccAddress, coins cosmos.Coins) bool {
	balance := k.coinKeeper.GetAllBalances(ctx, addr)
	return balance.IsAllGTE(coins)
}

func (k KVStore) GetAccount(ctx cosmos.Context, addr cosmos.AccAddress) cosmos.Account {
	return k.accountKeeper.GetAccount(ctx, addr)
}

func (k KVStore) GetNativeTxFee(ctx cosmos.Context) cosmos.Uint {
	if k.usdFeesEnabled(ctx) {
		return k.DollarConfigInRune(ctx, constants.NativeTransactionFeeUSD)
	}
	fee := k.GetConfigInt64(ctx, constants.NativeTransactionFee)
	return cosmos.NewUint(uint64(fee))
}

func (k KVStore) GetTHORNameRegisterFee(ctx cosmos.Context) cosmos.Uint {
	if k.usdFeesEnabled(ctx) {
		return k.DollarConfigInRune(ctx, constants.TNSRegisterFeeUSD)
	}
	fee := k.GetConstants().GetInt64Value(constants.TNSRegisterFee)
	return cosmos.NewUint(uint64(fee))
}

func (k KVStore) GetTHORNamePerBlockFee(ctx cosmos.Context) cosmos.Uint {
	if k.usdFeesEnabled(ctx) {
		return k.DollarConfigInRune(ctx, constants.TNSFeePerBlockUSD)
	}
	fee := k.GetConstants().GetInt64Value(constants.TNSFeePerBlock)
	return cosmos.NewUint(uint64(fee))
}

// DollarConfigInRune returns the dollar denominated config value in RUNE. If the RUNE
// price feed returns zero, the USD value will be returned.
func (k KVStore) DollarConfigInRune(ctx cosmos.Context, value constants.ConstantName) cosmos.Uint {
	usd := cosmos.SafeUintFromInt64(k.GetConfigInt64(ctx, value))
	runeUSDPrice := k.DollarsPerRune(ctx)
	if !runeUSDPrice.IsZero() {
		runeValue := usd.MulUint64(common.One).Quo(runeUSDPrice)

		// round to the first 2 significant digits of the USD price
		return cosmos.NewUint(common.RoundSignificantFigures(runeValue.Uint64(), k.GetConfigInt64(ctx, constants.FeeUSDRoundSignificantDigits)))
	}
	return usd
}

func (k KVStore) usdFeesEnabled(ctx cosmos.Context) bool {
	usdFees, _ := k.GetMimir(ctx, constants.EnableUSDFees.String())
	return usdFees > 0
}

func (k KVStore) DeductNativeTxFeeFromAccount(ctx cosmos.Context, acctAddr cosmos.AccAddress) error {
	fee := k.GetNativeTxFee(ctx)
	if fee.IsZero() {
		return nil // no fee
	}
	coins := common.NewCoins(common.NewCoin(common.RuneNative, fee))
	return k.SendFromAccountToModule(ctx, acctAddr, ReserveName, coins)
}

// RagnarokAccount remove account and return all assets to the protocol
func (k KVStore) RagnarokAccount(ctx cosmos.Context, addr cosmos.AccAddress) {
	acc := k.accountKeeper.GetAccount(ctx, addr)
	if acc != nil {
		balances := k.GetBalance(ctx, addr)
		for _, bal := range balances {
			if !bal.IsPositive() {
				continue
			}
			asset, err := common.NewAsset(bal.Denom)
			if err != nil {
				ctx.Logger().Error("invalid denom", "error", err)
				continue
			}
			coin := common.NewCoin(asset, cosmos.NewUintFromBigInt(bal.Amount.BigInt()))
			coins := common.NewCoins(coin)
			if asset.IsRune() {
				err = k.SendFromAccountToModule(ctx, addr, ReserveName, coins)
				if err != nil {
					ctx.Logger().Error("failed to transfer", "error", err)
				}
			} else {
				err = k.SendFromAccountToModule(ctx, addr, ModuleName, coins)
				if err != nil {
					ctx.Logger().Error("failed to transfer", "error", err)
				} else {
					err = k.BurnFromModule(ctx, ModuleName, coin)
					if err != nil {
						ctx.Logger().Error("failed to burn", "error", err)
					}
				}
			}
		}
		k.accountKeeper.RemoveAccount(ctx, acc)
	}
}

func kvPrefix(key []byte) types.DbPrefix {
	return types.DbPrefix(key)
}
