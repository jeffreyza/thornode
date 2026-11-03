package thorchain

import (
	"errors"
	"fmt"
	"sync"

	"cosmossdk.io/core/store"
	storetypes "cosmossdk.io/store/types"
	upgradekeeper "cosmossdk.io/x/upgrade/keeper"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/blang/semver"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/wasmpermissions"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	genesisBlockHeight = 1
)

// ErrNotEnoughToPayFee will happen when the emitted asset is not enough to pay for fee
var ErrNotEnoughToPayFee = errors.New("not enough asset to pay for fees")

// Manager is an interface to define all the required methods
type Manager interface {
	GetConstants() constants.ConstantValues
	GetVersion() semver.Version
	Keeper() keeper.Keeper
	GasMgr() GasManager
	EventMgr() EventManager
	TxOutStore() TxOutStore
	NetworkMgr() NetworkManager
	ValidatorMgr() ValidatorManager
	ObMgr() ObserverManager
	PoolMgr() PoolManager
	SwapQ() SwapQueue
	AdvSwapQueueMgr() AdvSwapQueue
	Slasher() Slasher
	TradeAccountManager() TradeAccountManager
	SecuredAssetManager() SecuredAssetManager
	StableReserveManager() StableReserveManager
	WasmManager() WasmManager
	SwitchManager() SwitchManager
	ScheduledMigrationManager() ScheduledMigrationManager
	DynamicFeeMgr() DynamicFeeManager
}

type TradeAccountManager interface {
	EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error
	Deposit(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (cosmos.Uint, error)
	Withdrawal(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (cosmos.Uint, error)
	BalanceOf(_ cosmos.Context, _ common.Asset, owner cosmos.AccAddress) cosmos.Uint
}

type SecuredAssetManager interface {
	EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error
	Deposit(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (cosmos.Coin, error)
	Withdraw(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (common.Coin, error)
	BalanceOf(_ cosmos.Context, _ common.Asset, owner cosmos.AccAddress) cosmos.Uint
	GetSecuredAssetStatus(_ cosmos.Context, _ common.Asset) (keeper.SecuredAsset, cosmos.Uint, error)
	GetShareSupply(_ cosmos.Context, _ common.Asset) cosmos.Uint
	CheckHalt(_ cosmos.Context) error
}

type StableReserveManager interface {
	EndBlock(ctx cosmos.Context, mgr Manager) error
	IsEnabled(ctx cosmos.Context) bool
	IsEligibleStable(ctx cosmos.Context, asset common.Asset) (bool, string)
	Deposit(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, contributor common.Address, txID common.TxID) error
	Withdraw(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, contributor common.Address, txID common.TxID) (common.Coin, bool, error)
	TrySwap(ctx cosmos.Context, mgr Manager, msg MsgSwap) (bool, cosmos.Uint, error)
}

// GasManager define all the methods required to manage gas
type GasManager interface {
	BeginBlock()
	EndBlock(ctx cosmos.Context, keeper keeper.Keeper, eventManager EventManager)
	AddGasAsset(outAsset common.Asset, gas common.Gas, increaseTxCount bool)
	ProcessGas(ctx cosmos.Context, keeper keeper.Keeper)
	GetGas() common.Gas
	GetAssetOutboundFee(ctx cosmos.Context, asset common.Asset, inRune bool) (cosmos.Uint, error)
	GetGasDetails(ctx cosmos.Context, chain common.Chain) (common.Coin, int64, error)
	GetMaxGas(ctx cosmos.Context, chain common.Chain) (common.Coin, error)
	GetGasRate(ctx cosmos.Context, chain common.Chain) cosmos.Uint
	GetNetworkFee(ctx cosmos.Context, chain common.Chain) (types.NetworkFee, error)
	CalcOutboundFeeMultiplier(ctx cosmos.Context, targetSurplusRune, gasSpentRune, gasWithheldRune, maxMultiplier, minMultiplier cosmos.Uint) cosmos.Uint
}

// EventManager define methods need to be support to manage events
type EventManager interface {
	EmitEvent(ctx cosmos.Context, evt EmitEventItem) error
	EmitGasEvent(ctx cosmos.Context, gasEvent *EventGas) error
	EmitSwapEvent(ctx cosmos.Context, swap *EventSwap) error
	EmitFeeEvent(ctx cosmos.Context, feeEvent *EventFee) error
}

// TxOutStore define the method required for TxOutStore
type TxOutStore interface {
	EndBlock(ctx cosmos.Context, mgr Manager) error
	GetBlockOut(ctx cosmos.Context) (*TxOut, error)
	ClearOutboundItems(ctx cosmos.Context)
	GetOutboundItems(ctx cosmos.Context) ([]TxOutItem, error)
	TryAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, minOut cosmos.Uint) (bool, error)
	UnSafeAddTxOutItem(ctx cosmos.Context, mgr Manager, toi TxOutItem, height int64) error
	GetOutboundItemByToAddress(cosmos.Context, common.Address) []TxOutItem
	CalcTxOutHeight(cosmos.Context, semver.Version, TxOutItem) (int64, cosmos.Uint, error)
	DiscoverOutbounds(ctx cosmos.Context, transactionFeeAsset cosmos.Uint, maxGasAsset common.Coin, toi TxOutItem, vaults Vaults) ([]TxOutItem, cosmos.Uint)
}

// ObserverManager define the method to manage observes
type ObserverManager interface {
	BeginBlock()
	EndBlock(ctx cosmos.Context, keeper keeper.Keeper)
	AppendObserver(chain common.Chain, addrs []cosmos.AccAddress)
	List() []cosmos.AccAddress
}

// ValidatorManager define the method to manage validators
type ValidatorManager interface {
	BeginBlock(ctx cosmos.Context, mgr Manager, existingValidators []string) error
	EndBlock(ctx cosmos.Context, mgr Manager) []abci.ValidatorUpdate
	processRagnarok(ctx cosmos.Context, mgr Manager) error
	NodeAccountPreflightCheck(ctx cosmos.Context, na NodeAccount, constAccessor constants.ConstantValues) (NodeStatus, error)
}

// NetworkManager interface define the contract of network Manager
type NetworkManager interface {
	TriggerKeygen(ctx cosmos.Context, nas NodeAccounts) error
	TriggerXMRKeygen(ctx cosmos.Context, nas NodeAccounts) error
	RotateVault(ctx cosmos.Context, vault Vault) error
	BeginBlock(ctx cosmos.Context, mgr Manager) error
	EndBlock(ctx cosmos.Context, mgr Manager) error
	UpdateNetwork(ctx cosmos.Context, mgr Manager) error
	SpawnDerivedAsset(ctx cosmos.Context, asset common.Asset, mgr Manager)
	CalcAnchor(_ cosmos.Context, _ Manager, _ common.Asset) (cosmos.Uint, cosmos.Uint, cosmos.Uint)
	SwapToOverSolvencyIncome(ctx cosmos.Context, mgr Manager, coin common.Coin) error
}

// PoolManager interface define the contract of PoolManager
type PoolManager interface {
	EndBlock(ctx cosmos.Context, mgr Manager) error
}

type WasmManager interface {
	// StoreCode to submit Wasm code to the system
	StoreCode(ctx cosmos.Context,
		creator cosmos.AccAddress,
		wasmCode []byte,
	) (codeID uint64, checksum []byte, err error)
	//  InstantiateContract creates a new smart contract instance for the given
	//  code id.
	InstantiateContract(ctx cosmos.Context,
		codeID uint64,
		creator, admin sdk.AccAddress,
		initMsg []byte,
		label string,
		deposit sdk.Coins,
	) (cosmos.AccAddress, []byte, error)
	//  InstantiateContract2 creates a new smart contract instance for the given
	//  code id with a predictable address
	InstantiateContract2(ctx cosmos.Context,
		codeID uint64,
		creator, admin sdk.AccAddress,
		initMsg []byte,
		label string,
		deposit sdk.Coins,
		salt []byte,
		fixMsg bool,
	) (sdk.AccAddress, []byte, error)
	// Execute submits the given message data to a smart contract
	ExecuteContract(ctx cosmos.Context,
		contractAddr, senderAddr cosmos.AccAddress,
		msg []byte,
		coins cosmos.Coins,
	) ([]byte, error)
	// Migrate runs a code upgrade/ downgrade for a smart contract
	MigrateContract(ctx cosmos.Context,
		contractAddress, caller sdk.AccAddress,
		newCodeID uint64,
		msg []byte,
	) ([]byte, error)
	// SudoContract defines an operation for calling sudo
	// on a contract. The authority is verified against deployer permissions.
	//
	// Since: 0.40
	SudoContract(ctx cosmos.Context,
		contractAddress, caller sdk.AccAddress,
		msg []byte,
	) ([]byte, error)
	UpdateAdmin(ctx cosmos.Context,
		contractAddress, caller, newAdmin sdk.AccAddress,
	) ([]byte, error)
	ClearAdmin(ctx cosmos.Context,
		contractAddress, caller sdk.AccAddress,
	) ([]byte, error)
	// HasContract reports whether the address belongs to an instantiated
	// contract, which is how a message dispatched by a contract is told apart
	// from one sent by an externally owned account.
	HasContract(ctx cosmos.Context, addr cosmos.AccAddress) bool
}

// SwapQueue interface define the contract of Swap Queue
type SwapQueue interface {
	EndBlock(ctx cosmos.Context, mgr Manager) error
}

// AdvSwapQueue interface define the contract of Advanced Swap Queue
type AdvSwapQueue interface {
	AddSwapQueueItem(ctx cosmos.Context, mgr Manager, msg *MsgSwap) error
	EndBlock(ctx cosmos.Context, mgr Manager, telemetryEnabled bool) error
}

// Slasher define all the method to perform slash
type Slasher interface {
	BeginBlock(ctx cosmos.Context, constAccessor constants.ConstantValues)
	LackSigning(ctx cosmos.Context, mgr Manager) error
	SlashVault(ctx cosmos.Context, vaultPK common.PubKey, coins common.Coins, mgr Manager) error
	IncSlashPoints(ctx cosmos.Context, point int64, addresses ...cosmos.AccAddress)
	DecSlashPoints(ctx cosmos.Context, point int64, addresses ...cosmos.AccAddress)
}

// Though Swapper is not a full manager, it is recorded here for versioning convenience.
type Swapper interface {
	Swap(ctx cosmos.Context,
		keeper keeper.Keeper,
		tx common.Tx,
		target common.Asset,
		destination common.Address,
		swapTarget cosmos.Uint,
		dexAgg string,
		dexAggTargetAsset string,
		dexAggLimit *cosmos.Uint,
		swp StreamingSwap,
		synthVirtualDepthMult int64,
		mgr Manager,
		affiliates []string, affiliateBps []cosmos.Uint,
	) (cosmos.Uint, cosmos.Uint, []*EventSwap, error)
	CalcAssetEmission(X, x, Y cosmos.Uint) cosmos.Uint
	CalcLiquidityFee(X, x, Y cosmos.Uint) cosmos.Uint
	CalcSwapSlip(Xi, xi cosmos.Uint) cosmos.Uint
	GetSwapCalc(X, x, Y, slipBps, minSlipBps cosmos.Uint) (emitAssets, liquidityFee, slip cosmos.Uint)
}

type SwitchManager interface {
	Switch(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (common.Address, error)
	IsSwitch(_ cosmos.Context, _ common.Asset) bool
}

type ScheduledMigrationManager interface {
	EndBlock(ctx cosmos.Context, mgr Manager) error
}

type OracleManager interface {
	BeginBlock(ctx cosmos.Context) error
}

type VolumeManager interface {
	EndBlock(ctx cosmos.Context) error
}

type DynamicFeeManager interface {
	CreditSwap(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, sourceAsset, targetAsset common.Asset, volumeTor, feesTor cosmos.Uint)
	GetDynamicBps(ctx cosmos.Context, affiliates []string, affiliateBps []cosmos.Uint, sourceAsset, targetAsset common.Asset) uint64
	EndBlock(ctx cosmos.Context) error
}

// Mgrs is an implementation of Manager interface
type Mgrs struct {
	// mu guards the version-dependent fields below (currentVersion through K).
	// Consensus is the only writer (recreateManagers), but queries served off the
	// direct gRPC path read them concurrently, no longer serialized behind the
	// comet ABCI lock. loadMu serializes LoadManagerIfNecessary itself (BeginBlock
	// vs the regtest querier's initManager).
	mu     sync.RWMutex
	loadMu sync.Mutex

	currentVersion            semver.Version
	constAccessor             constants.ConstantValues
	gasMgr                    GasManager
	eventMgr                  EventManager
	txOutStore                TxOutStore
	networkMgr                NetworkManager
	validatorMgr              ValidatorManager
	obMgr                     ObserverManager
	poolMgr                   PoolManager
	swapQ                     SwapQueue
	advSwapQueue              AdvSwapQueue
	slasher                   Slasher
	tradeManager              TradeAccountManager
	securedManager            SecuredAssetManager
	stableReserveManager      StableReserveManager
	wasmManager               WasmManager
	switchManager             SwitchManager
	scheduledMigrationManager ScheduledMigrationManager
	oracleManager             OracleManager
	volumeManager             VolumeManager
	dynamicFeeManager         DynamicFeeManager

	K             keeper.Keeper
	cdc           codec.Codec
	coinKeeper    bankkeeper.Keeper
	accountKeeper authkeeper.AccountKeeper
	upgradeKeeper *upgradekeeper.Keeper
	wasmKeeper    wasmkeeper.Keeper
	storeKey      *storetypes.KVStoreKey
	storeService  store.KVStoreService
}

// NewManagers  create a new Manager
func NewManagers(
	keeper keeper.Keeper,
	cdc codec.Codec,
	storeKey *storetypes.KVStoreKey,
	coinKeeper bankkeeper.Keeper,
	accountKeeper authkeeper.AccountKeeper,
	upgradeKeeper *upgradekeeper.Keeper,
	wasmKeeper wasmkeeper.Keeper,
) *Mgrs {
	return &Mgrs{
		K:             keeper,
		cdc:           cdc,
		coinKeeper:    coinKeeper,
		accountKeeper: accountKeeper,
		upgradeKeeper: upgradeKeeper,
		wasmKeeper:    wasmKeeper,
		storeKey:      storeKey,
		storeService:  runtime.NewKVStoreService(storeKey),
	}
}

func (mgr *Mgrs) GetVersion() semver.Version {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.currentVersion
}

func (mgr *Mgrs) GetConstants() constants.ConstantValues {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.constAccessor
}

// LoadManagerIfNecessary detect whether there are new version available, if it is available then create a new version of Mgr
func (mgr *Mgrs) LoadManagerIfNecessary(ctx cosmos.Context) error {
	mgr.loadMu.Lock()
	defer mgr.loadMu.Unlock()

	v := mgr.Keeper().GetLowestActiveVersion(ctx)

	if !v.Equals(mgr.GetVersion()) {
		// version is different , thus all the manager need to re-create
		if err := mgr.recreateManagers(ctx, v); err != nil {
			return err
		}
	}

	storedVer, hasStoredVer := mgr.Keeper().GetVersionWithCtx(ctx)
	if !hasStoredVer || v.GT(storedVer) {
		// store the version for contextual lookups if it has been upgraded
		mgr.Keeper().SetVersionWithCtx(ctx, v)
	}

	// Emit a version event as long as there was indeed a previously-recorded version changed from.
	// (As indicated by the keeper rather than by the manager.)
	if hasStoredVer && v.GT(storedVer) {
		evt := NewEventVersion(v)
		if err := mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			ctx.Logger().Error("fail to emit version event", "error", err)
		}
	}

	return nil
}

func (mgr *Mgrs) recreateManagers(ctx cosmos.Context, v semver.Version) error {
	// Build the full manager set into locals first, then swap under the write
	// lock, so concurrent queries never observe a partially updated set and a
	// construction error leaves the previous set intact.
	constAccessor := constants.GetConstantValues(v)

	k, err := GetKeeper(v, mgr.cdc, mgr.storeKey, mgr.coinKeeper, mgr.accountKeeper, mgr.upgradeKeeper)
	if err != nil {
		return fmt.Errorf("fail to create keeper: %w", err)
	}

	gasMgr, err := GetGasManager(v, k)
	if err != nil {
		return fmt.Errorf("fail to create gas manager: %w", err)
	}

	eventMgr, err := GetEventManager(v)
	if err != nil {
		return fmt.Errorf("fail to get event manager: %w", err)
	}

	txOutStore, err := GetTxOutStore(v, k, eventMgr, gasMgr)
	if err != nil {
		return fmt.Errorf("fail to get tx out store: %w", err)
	}

	networkMgr, err := GetNetworkManager(v, k, txOutStore, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to get vault manager: %w", err)
	}

	poolMgr, err := GetPoolManager(v)
	if err != nil {
		return fmt.Errorf("fail to get pool manager: %w", err)
	}

	validatorMgr, err := GetValidatorManager(v, k, networkMgr, txOutStore, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to get validator manager: %w", err)
	}

	obMgr, err := GetObserverManager(v)
	if err != nil {
		return fmt.Errorf("fail to get observer manager: %w", err)
	}

	swapQ, err := GetSwapQueue(v, k)
	if err != nil {
		return fmt.Errorf("fail to create swap queue: %w", err)
	}

	advSwapQueue, err := GetAdvSwapQueue(v, k)
	if err != nil {
		return fmt.Errorf("fail to create adv swap queue: %w", err)
	}

	slasher, err := GetSlasher(v, k, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create swap queue: %w", err)
	}

	tradeManager, err := GetTradeAccountManager(v, k, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create trade manager: %w", err)
	}

	securedManager, err := GetSecuredAssetManager(v, k, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create secured manager: %w", err)
	}

	stableReserveManager, err := GetStableReserveManager(v, k, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create stable reserve manager: %w", err)
	}

	wasmManager, err := GetWasmManager(ctx, k, mgr.wasmKeeper, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create wasm manager: %w", err)
	}

	switchManager, err := GetSwitchManager(v, k, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create switch manager: %w", err)
	}

	oracleManager, err := GetOracleManager(v, k, eventMgr)
	if err != nil {
		return fmt.Errorf("fail to create oracle manager: %w", err)
	}

	volumeManager, err := GetVolumeManager(v, k)
	if err != nil {
		return fmt.Errorf("fail to create volume manager: %w", err)
	}

	dynamicFeeManager, err := GetDynamicFeeManager(v, k)
	if err != nil {
		return fmt.Errorf("fail to create dynamic fee manager: %w", err)
	}

	scheduledMigrationManager, err := GetScheduledMigrationManager(v, mgr)
	if err != nil {
		return fmt.Errorf("fail to create scheduled migration manager: %w", err)
	}

	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	mgr.currentVersion = v
	mgr.constAccessor = constAccessor
	mgr.K = k
	mgr.gasMgr = gasMgr
	mgr.eventMgr = eventMgr
	mgr.txOutStore = txOutStore
	mgr.networkMgr = networkMgr
	mgr.poolMgr = poolMgr
	mgr.validatorMgr = validatorMgr
	mgr.obMgr = obMgr
	mgr.swapQ = swapQ
	mgr.advSwapQueue = advSwapQueue
	mgr.slasher = slasher
	mgr.tradeManager = tradeManager
	mgr.securedManager = securedManager
	mgr.stableReserveManager = stableReserveManager
	mgr.wasmManager = wasmManager
	mgr.switchManager = switchManager
	mgr.oracleManager = oracleManager
	mgr.volumeManager = volumeManager
	mgr.dynamicFeeManager = dynamicFeeManager
	mgr.scheduledMigrationManager = scheduledMigrationManager

	return nil
}

// Keeper return Keeper
func (mgr *Mgrs) Keeper() keeper.Keeper {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.K
}

// GasMgr return GasManager
func (mgr *Mgrs) GasMgr() GasManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.gasMgr
}

// EventMgr return EventMgr
func (mgr *Mgrs) EventMgr() EventManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.eventMgr
}

// TxOutStore return an TxOutStore
func (mgr *Mgrs) TxOutStore() TxOutStore {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.txOutStore
}

// VaultMgr return a valid NetworkManager
func (mgr *Mgrs) NetworkMgr() NetworkManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.networkMgr
}

// PoolMgr return a valid PoolManager
func (mgr *Mgrs) PoolMgr() PoolManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.poolMgr
}

// ValidatorMgr return an implementation of ValidatorManager
func (mgr *Mgrs) ValidatorMgr() ValidatorManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.validatorMgr
}

// ObMgr return an implementation of ObserverManager
func (mgr *Mgrs) ObMgr() ObserverManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.obMgr
}

// SwapQ return an implementation of SwapQueue
func (mgr *Mgrs) SwapQ() SwapQueue {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.swapQ
}

// AdvSwapQueueMgr
func (mgr *Mgrs) AdvSwapQueueMgr() AdvSwapQueue {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.advSwapQueue
}

// Slasher return an implementation of Slasher
func (mgr *Mgrs) Slasher() Slasher {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.slasher
}

func (mgr *Mgrs) TradeAccountManager() TradeAccountManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.tradeManager
}

func (mgr *Mgrs) SecuredAssetManager() SecuredAssetManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.securedManager
}

func (mgr *Mgrs) StableReserveManager() StableReserveManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.stableReserveManager
}

func (mgr *Mgrs) WasmManager() WasmManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.wasmManager
}

func (mgr *Mgrs) SwitchManager() SwitchManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.switchManager
}

func (mgr *Mgrs) ScheduledMigrationManager() ScheduledMigrationManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.scheduledMigrationManager
}

func (mgr *Mgrs) OracleManager() OracleManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.oracleManager
}

func (mgr *Mgrs) VolumeManager() VolumeManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.volumeManager
}

func (mgr *Mgrs) DynamicFeeMgr() DynamicFeeManager {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()
	return mgr.dynamicFeeManager
}

// GetKeeper return Keeper
func GetKeeper(
	version semver.Version,
	cdc codec.BinaryCodec,
	storeKey *storetypes.KVStoreKey,
	coinKeeper bankkeeper.Keeper,
	accountKeeper authkeeper.AccountKeeper,
	upgradeKeeper *upgradekeeper.Keeper,
) (keeper.Keeper, error) {
	kvs := kv1.NewKVStore(cdc, storeKey, coinKeeper, accountKeeper, upgradeKeeper, version)
	return &kvs, nil
}

// GetGasManager return GasManager
func GetGasManager(version semver.Version, keeper keeper.Keeper) (GasManager, error) {
	constAccessor := constants.GetConstantValues(version)
	return newGasMgr(constAccessor, keeper), nil
}

// GetEventManager will return an implementation of EventManager
func GetEventManager(version semver.Version) (EventManager, error) {
	return newEventMgr(), nil
}

// GetTxOutStore will return an implementation of the txout store that
func GetTxOutStore(version semver.Version, keeper keeper.Keeper, eventMgr EventManager, gasManager GasManager) (TxOutStore, error) {
	constAccessor := constants.GetConstantValues(version)
	return newTxOutStorage(keeper, constAccessor, eventMgr, gasManager), nil
}

// GetNetworkManager  retrieve a NetworkManager that is compatible with the given version
func GetNetworkManager(version semver.Version, keeper keeper.Keeper, txOutStore TxOutStore, eventMgr EventManager) (NetworkManager, error) {
	return newNetworkMgr(keeper, txOutStore, eventMgr), nil
}

// GetValidatorManager create a new instance of Validator Manager
func GetValidatorManager(_ semver.Version, keeper keeper.Keeper, networkMgr NetworkManager, txOutStore TxOutStore, eventMgr EventManager) (ValidatorManager, error) {
	return newValidatorMgr(keeper, networkMgr, txOutStore, eventMgr), nil
}

// GetObserverManager return an instance that implements ObserverManager interface
// when there is no version can match the given semver , it will return nil
func GetObserverManager(version semver.Version) (ObserverManager, error) {
	return newObserverMgr(), nil
}

// GetPoolManager return an implementation of PoolManager
func GetPoolManager(version semver.Version) (PoolManager, error) {
	return newPoolMgr(), nil
}

// GetSwapQueue retrieve a SwapQueue that is compatible with the given version
func GetSwapQueue(version semver.Version, keeper keeper.Keeper) (SwapQueue, error) {
	return newSwapQueue(keeper), nil
}

// GetAdvSwapQueue retrieve a AdvSwapQueue that is compatible with the given version
func GetAdvSwapQueue(version semver.Version, keeper keeper.Keeper) (AdvSwapQueue, error) {
	return newSwapQueueAdv(keeper), nil
}

// GetSlasher return an implementation of Slasher
func GetSlasher(version semver.Version, keeper keeper.Keeper, eventMgr EventManager) (Slasher, error) {
	return newSlasher(keeper, eventMgr), nil
}

// Though Swapper is not a full manager, it is recorded here for versioning convenience.
// GetSwapper return an implementation of Swapper
func GetSwapper(version semver.Version) (Swapper, error) {
	return newSwapper(), nil
}

func GetTradeAccountManager(version semver.Version, keeper keeper.Keeper, eventMgr EventManager) (TradeAccountManager, error) {
	return newTradeMgr(keeper, eventMgr), nil
}

func GetSecuredAssetManager(version semver.Version, keeper keeper.Keeper, eventMgr EventManager) (SecuredAssetManager, error) {
	return newSecuredAssetMgr(keeper, eventMgr), nil
}

func GetStableReserveManager(version semver.Version, keeper keeper.Keeper, eventMgr EventManager) (StableReserveManager, error) {
	return newStableReserveMgr(keeper, eventMgr), nil
}

func GetWasmManager(ctx cosmos.Context, keeper keeper.Keeper, wasmKeeper wasmkeeper.Keeper, eventMgr EventManager) (WasmManager, error) {
	return newWasmMgr(keeper, wasmKeeper, wasmpermissions.GetWasmPermissions(), eventMgr)
}

func GetSwitchManager(version semver.Version, keeper keeper.Keeper, eventMgr EventManager) (SwitchManager, error) {
	return newSwitchMgr(keeper, eventMgr), nil
}

func GetScheduledMigrationManager(_ semver.Version, mgr Manager) (ScheduledMigrationManager, error) {
	return newScheduledMigrationMgr(mgr), nil
}

func GetOracleManager(_ semver.Version, keeper keeper.Keeper, eventMgr EventManager) (OracleManager, error) {
	return newOracleMgr(keeper), nil
}

func GetVolumeManager(_ semver.Version, keeper keeper.Keeper) (VolumeManager, error) {
	return newVolumeMgr(keeper), nil
}

func GetDynamicFeeManager(_ semver.Version, keeper keeper.Keeper) (DynamicFeeManager, error) {
	return newDynamicFeeMgr(keeper), nil
}
