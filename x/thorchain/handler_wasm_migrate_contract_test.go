package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmMigrateContractSuite struct{}

var _ = Suite(&HandlerWasmMigrateContractSuite{})

func (s *HandlerWasmMigrateContractSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmMigrateContractSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmMigrateContractHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmMigrateContractSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmMigrateContractHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgMigrateContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
		CodeID:   2,
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgMigrateContract while THORChain is halted")
}

func (s *HandlerWasmMigrateContractSuite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmMigrateContractHandler(mgr)

	msg := &wasmtypes.MsgMigrateContract{
		Sender:   "invalid-address",
		Contract: GetRandomBech32Addr().String(),
		CodeID:   2,
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmMigrateContractSuite) TestInvalidContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmMigrateContractHandler(mgr)

	msg := &wasmtypes.MsgMigrateContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: "invalid-address",
		CodeID:   2,
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmMigrateContractSuite) TestMigrateFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingMigrateWasmManager{}
	h := NewWasmMigrateContractHandler(mgr)

	msg := &wasmtypes.MsgMigrateContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
		CodeID:   2,
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "migrate contract failed")
}

func (s *HandlerWasmMigrateContractSuite) TestSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmMigrateContractHandler(mgr)

	msg := &wasmtypes.MsgMigrateContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
		CodeID:   2,
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingMigrateWasmManager struct {
	DummyWasmManager
}

func (f *failingMigrateWasmManager) MigrateContract(ctx cosmos.Context, contractAddress, caller sdk.AccAddress, newCodeID uint64, msg []byte) ([]byte, error) {
	return nil, fmt.Errorf("migrate contract failed")
}
