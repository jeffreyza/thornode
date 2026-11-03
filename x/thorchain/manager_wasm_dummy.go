package thorchain

import (
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var _ WasmManager = &DummyWasmManager{}

// DummyWasmManager is current implementation of slasher
type DummyWasmManager struct {
	wasmtypes.UnimplementedMsgServer
}

// newDummyWasmManager create a new instance of Slasher
func NewDummyWasmManager() *DummyWasmManager {
	return &DummyWasmManager{}
}

func (s *DummyWasmManager) StoreCode(ctx cosmos.Context,
	creator cosmos.AccAddress,
	wasmCode []byte,
) (codeID uint64, checksum []byte, err error) {
	return 0, nil, nil
}

func (s *DummyWasmManager) InstantiateContract(ctx cosmos.Context,
	codeID uint64,
	creator, admin sdk.AccAddress,
	initMsg []byte,
	label string,
	deposit sdk.Coins,
) (cosmos.AccAddress, []byte, error) {
	return nil, nil, nil
}

func (s *DummyWasmManager) InstantiateContract2(ctx cosmos.Context,
	codeID uint64,
	creator, admin sdk.AccAddress,
	initMsg []byte,
	label string,
	deposit sdk.Coins,
	salt []byte,
	fixMsg bool,
) (sdk.AccAddress, []byte, error) {
	return nil, nil, nil
}

func (s *DummyWasmManager) ExecuteContract(ctx cosmos.Context,
	contractAddr, senderAddr cosmos.AccAddress,
	msg []byte,
	coins cosmos.Coins,
) ([]byte, error) {
	return nil, nil
}

func (s *DummyWasmManager) MigrateContract(ctx cosmos.Context,
	contractAddress, caller sdk.AccAddress,
	newCodeID uint64,
	msg []byte,
) ([]byte, error) {
	return nil, nil
}

func (s *DummyWasmManager) SudoContract(ctx cosmos.Context,
	contractAddress, caller sdk.AccAddress,
	msg []byte,
) ([]byte, error) {
	return nil, nil
}

func (s *DummyWasmManager) UpdateAdmin(ctx cosmos.Context,
	contractAddress, sender, newAdmin sdk.AccAddress,
) ([]byte, error) {
	return nil, nil
}

func (s *DummyWasmManager) ClearAdmin(ctx cosmos.Context,
	contractAddress, sender sdk.AccAddress,
) ([]byte, error) {
	return nil, nil
}

func (s *DummyWasmManager) HasContract(ctx cosmos.Context, addr cosmos.AccAddress) bool {
	return false
}
