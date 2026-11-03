package thorchain

import (
	"encoding/base32"
	"strings"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
	"gitlab.com/thorchain/thornode/v3/common/wasmpermissions"
)

var _ WasmManager = &WasmMgr{}

// WasmMgr is current implementation of slasher
type WasmMgr struct {
	keeper      keeper.Keeper
	wasmKeeper  wasmkeeper.Keeper
	eventMgr    EventManager
	permissions wasmpermissions.WasmPermissions
}

// newWasmMgr create a new instance of Slasher
func newWasmMgr(
	keeper keeper.Keeper,
	wasmKeeper wasmkeeper.Keeper,
	permissions wasmpermissions.WasmPermissions,
	eventMgr EventManager,
) (*WasmMgr, error) {
	return &WasmMgr{
		keeper:      keeper,
		wasmKeeper:  wasmKeeper,
		permissions: permissions,
		eventMgr:    eventMgr,
	}, nil
}

// StoreCode stores a new wasm code on chain
func (m WasmMgr) StoreCode(
	ctx cosmos.Context,
	creator sdk.AccAddress,
	wasmCode []byte,
) (codeID uint64, checksum []byte, err error) {
	if err = m.checkGlobalHalt(ctx); err != nil {
		return 0, nil, err
	}

	if err = m.checkCanStore(ctx, creator); err != nil {
		return 0, nil, err
	}

	codeID, checksum, err = m.permissionedKeeper().Create(
		ctx,
		creator,
		wasmCode,
		nil,
	)
	if err != nil {
		return 0, nil, err
	}

	if err := m.checkChecksumHalt(ctx, checksum); err != nil {
		return 0, nil, err
	}

	return codeID, checksum, nil
}

// InstantiateContract instantiate a new contract with classic sequence based address generation
func (m WasmMgr) InstantiateContract(
	ctx cosmos.Context,
	codeID uint64,
	creator, admin sdk.AccAddress,
	initMsg []byte,
	label string,
	deposit sdk.Coins,
) (sdk.AccAddress, []byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, nil, err
	}

	err := m.checkInstantiateAuthorization(ctx, creator)
	if err != nil {
		return nil, nil, err
	}

	err = m.maybePin(ctx, codeID)
	if err != nil {
		return nil, nil, err
	}

	return m.permissionedKeeper().Instantiate(
		ctx,
		codeID,
		creator,
		admin,
		initMsg,
		label,
		deposit,
	)
}

// InstantiateContract2 instantiate a new contract with predicatable address generated
func (m WasmMgr) InstantiateContract2(
	ctx cosmos.Context,
	codeID uint64,
	creator, admin sdk.AccAddress,
	initMsg []byte,
	label string,
	deposit sdk.Coins,
	salt []byte,
	fixMsg bool,
) (sdk.AccAddress, []byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, nil, err
	}

	err := m.checkInstantiateAuthorization(ctx, creator)
	if err != nil {
		return nil, nil, err
	}

	err = m.maybePin(ctx, codeID)
	if err != nil {
		return nil, nil, err
	}

	return m.permissionedKeeper().Instantiate2(
		ctx,
		codeID,
		creator,
		admin,
		initMsg,
		label,
		deposit,
		salt,
		fixMsg,
	)
}

// HasContract returns true if addr is an instantiated wasm contract.
func (m WasmMgr) HasContract(ctx cosmos.Context, addr cosmos.AccAddress) bool {
	return m.wasmKeeper.HasContractInfo(ctx, addr)
}

func (m WasmMgr) ExecuteContract(
	ctx cosmos.Context,
	contractAddress, caller sdk.AccAddress,
	msg []byte,
	coins sdk.Coins,
) ([]byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, err
	}

	// The default `Messenger` configured in wasm keeper, used for routing sub-messages, uses
	// the app's `Router`. Therefore, any SDK submessages that call a contract will route
	// back through this code path and will be halted where necessary
	contractInfo, err := m.getContractInfo(ctx, contractAddress)
	if err != nil {
		return nil, err
	}

	codeInfo, err := m.getCodeInfo(ctx, contractInfo.CodeID)
	if err != nil {
		return nil, err
	}

	if err := m.checkContractHalt(ctx, contractAddress); err != nil {
		return nil, err
	}

	if err := m.checkChecksumHalt(ctx, codeInfo.CodeHash); err != nil {
		return nil, err
	}

	return m.permissionedKeeper().Execute(
		ctx,
		contractAddress,
		caller,
		msg, coins,
	)
}

func (m WasmMgr) MigrateContract(
	ctx cosmos.Context,
	contractAddress, caller sdk.AccAddress,
	newCodeID uint64,
	msg []byte,
) ([]byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, err
	}

	contractInfo, err := m.getContractInfo(ctx, contractAddress)
	if err != nil {
		return nil, err
	}

	err = m.checkIsContractAdmin(contractInfo, caller)
	if err != nil {
		return nil, err
	}

	err = m.checkInstantiateAuthorization(ctx, caller)
	if err != nil {
		return nil, err
	}

	// NOTE: checkChecksumHalt is intentionally NOT called here. A halted checksum
	// indicates buggy execute entry point code, but migrating a contract onto a halted
	// code ID is valid — it serves as the halt mechanism itself (the "halt contract mimic").

	err = m.maybePin(ctx, newCodeID)
	if err != nil {
		return nil, err
	}

	data, err := m.permissionedKeeper().Migrate(
		ctx,
		contractAddress,
		caller,
		newCodeID,
		msg,
	)
	if err != nil {
		return nil, err
	}

	err = m.maybeUnpin(ctx, contractInfo.CodeID)
	if err != nil {
		return nil, err
	}

	return data, nil
}

// SudoContract calls sudo on a contract.
func (m WasmMgr) SudoContract(
	ctx cosmos.Context,
	contractAddress, caller sdk.AccAddress,
	msg []byte,
) ([]byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, err
	}

	contractInfo, err := m.getContractInfo(ctx, contractAddress)
	if err != nil {
		return nil, err
	}

	err = m.checkIsContractAdmin(contractInfo, caller)
	if err != nil {
		return nil, err
	}

	return m.permissionedKeeper().Sudo(ctx, contractAddress, msg)
}

func (m WasmMgr) UpdateAdmin(
	ctx cosmos.Context,
	contractAddress, sender, newAdmin sdk.AccAddress,
) ([]byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, err
	}

	contractInfo, err := m.getContractInfo(ctx, contractAddress)
	if err != nil {
		return nil, err
	}

	err = m.checkIsContractAdmin(contractInfo, sender)
	if err != nil {
		return nil, err
	}

	return nil, m.permissionedKeeper().UpdateContractAdmin(ctx, contractAddress, sender, newAdmin)
}

func (m WasmMgr) ClearAdmin(
	ctx cosmos.Context,
	contractAddress, sender sdk.AccAddress,
) ([]byte, error) {
	if err := m.checkGlobalHalt(ctx); err != nil {
		return nil, err
	}

	contractInfo, err := m.getContractInfo(ctx, contractAddress)
	if err != nil {
		return nil, err
	}

	err = m.checkIsContractAdmin(contractInfo, sender)
	if err != nil {
		return nil, err
	}

	return nil, m.permissionedKeeper().ClearContractAdmin(ctx, contractAddress, sender)
}

func (m WasmMgr) checkGlobalHalt(ctx cosmos.Context) error {
	v, err := m.keeper.GetMimir(ctx, constants.MimirKeyWasmHaltGlobal)
	if err != nil {
		return err
	}
	if v > 0 && ctx.BlockHeight() > v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "wasm halted")
	}
	return nil
}

func (m WasmMgr) checkContractHalt(ctx cosmos.Context, address sdk.AccAddress) error {
	// Use checksum for brevity and to fit inside mimir's 64 char length
	addrStr := address.String()
	contractKey := addrStr[len(addrStr)-6:]
	v, err := m.keeper.GetMimirWithRef(ctx, constants.MimirTemplateWasmHaltContract, contractKey)
	if err != nil {
		return err
	}
	if v > 0 && ctx.BlockHeight() > v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "contract halted")
	}
	return nil
}

func (m WasmMgr) checkChecksumHalt(ctx cosmos.Context, checksum []byte) error {
	encoder := base32.StdEncoding
	encoded := encoder.EncodeToString(checksum)
	key := strings.TrimRight(encoded, "=")
	v, err := m.keeper.GetMimirWithRef(ctx, constants.MimirTemplateWasmHaltChecksum, key)
	if err != nil {
		return err
	}
	if v > 0 && ctx.BlockHeight() > v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "checksum halted")
	}
	return nil
}

func (m WasmMgr) permissionedKeeper() *wasmkeeper.PermissionedKeeper {
	return wasmkeeper.NewGovPermissionKeeper(m.wasmKeeper)
}

func (m WasmMgr) checkCanStore(ctx cosmos.Context, actor cosmos.AccAddress) error {
	err := m.checkActor(ctx, actor)
	if err != nil {
		return err
	}

	v, err := m.keeper.GetMimir(ctx, constants.MimirKeyWasmPermissionless)
	if err != nil {
		return err
	}
	if v > 0 && ctx.BlockHeight() > v {
		return nil
	}

	return m.permissions.CanStore(actor)
}

func (m WasmMgr) checkCanInstantiate(ctx cosmos.Context, actor cosmos.AccAddress) error {
	err := m.checkActor(ctx, actor)
	if err != nil {
		return err
	}

	v, err := m.keeper.GetMimir(ctx, constants.MimirKeyWasmPermissionless)
	if err != nil {
		return err
	}
	if v > 0 && ctx.BlockHeight() > v {
		return nil
	}

	return m.permissions.CanInstantiate(actor)
}

func (m WasmMgr) checkActor(ctx cosmos.Context, actor cosmos.AccAddress) error {
	actorKey := actor.String()
	v, err := m.keeper.GetMimirWithRef(ctx, constants.MimirTemplateWasmHaltDeployer, actorKey)
	if err != nil {
		return err
	}
	if v > 0 && ctx.BlockHeight() > v {
		return errors.ErrUnauthorized
	}
	return nil
}

func (m WasmMgr) checkInstantiateAuthorization(ctx cosmos.Context, actor cosmos.AccAddress) error {
	// If the actor is a contract, it can instantiate new contracts without explicit permission
	// wasmKeeper.QueryContractInfo panics if the contract does not exist, so query for non zero length history instead
	result := m.wasmKeeper.GetContractHistory(ctx, actor)
	if len(result) > 0 {
		return nil
	}

	return m.checkCanInstantiate(ctx, actor)
}

func (m WasmMgr) checkIsContractAdmin(contractInfo *wasmtypes.ContractInfo, actor cosmos.AccAddress) error {
	// Migration authorization is handled here, not by the wasmkeeper's GovAuthorizationPolicy
	// (which always returns true for CanModifyContract). We use a direct equality check to
	// ensure only the contract admin can migrate or sudo.
	if !actor.Equals(contractInfo.AdminAddr()) {
		return errors.ErrUnauthorized
	}

	return nil
}

func (m WasmMgr) getCodeInfo(ctx cosmos.Context, id uint64) (*wasmtypes.CodeInfo, error) {
	codeInfo := m.wasmKeeper.GetCodeInfo(ctx, id)
	if codeInfo == nil {
		return nil, wasmtypes.ErrNotFound
	}
	return codeInfo, nil
}

func (m WasmMgr) getContractInfo(ctx cosmos.Context, contractAddress sdk.AccAddress) (*wasmtypes.ContractInfo, error) {
	contractInfo := m.wasmKeeper.GetContractInfo(ctx, contractAddress)
	if contractInfo == nil {
		return nil, wasmtypes.ErrNotFound
	}
	return contractInfo, nil
}

func (m WasmMgr) maybePin(ctx cosmos.Context, codeId uint64) error {
	var instanceCount int

	m.wasmKeeper.IterateContractsByCode(ctx, codeId, func(address sdk.AccAddress) bool {
		instanceCount++
		return true
	})
	// This is called before the instantiation is executed
	if instanceCount == 0 {
		err := m.permissionedKeeper().PinCode(ctx, codeId)
		if err != nil {
			return err
		}
	}
	return nil
}

func (m WasmMgr) maybeUnpin(ctx cosmos.Context, codeId uint64) error {
	var instanceCount int

	m.wasmKeeper.IterateContractsByCode(ctx, codeId, func(address sdk.AccAddress) bool {
		instanceCount++
		return true
	})
	if instanceCount == 0 {
		err := m.permissionedKeeper().UnpinCode(ctx, codeId)
		if err != nil {
			return err
		}
	}
	return nil
}
