package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmClearAdminSuite struct{}

var _ = Suite(&HandlerWasmClearAdminSuite{})

func (s *HandlerWasmClearAdminSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmClearAdminSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmClearAdminHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmClearAdminSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmClearAdminHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	sender := GetRandomBech32Addr()
	contract := GetRandomBech32Addr()
	msg := &wasmtypes.MsgClearAdmin{
		Sender:   sender.String(),
		Contract: contract.String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgClearAdmin while THORChain is halted")
}

func (s *HandlerWasmClearAdminSuite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmClearAdminHandler(mgr)

	msg := &wasmtypes.MsgClearAdmin{
		Sender:   "invalid-address",
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmClearAdminSuite) TestInvalidContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmClearAdminHandler(mgr)

	msg := &wasmtypes.MsgClearAdmin{
		Sender:   GetRandomBech32Addr().String(),
		Contract: "invalid-address",
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmClearAdminSuite) TestClearAdminFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingClearAdminWasmManager{}
	h := NewWasmClearAdminHandler(mgr)

	msg := &wasmtypes.MsgClearAdmin{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "clear admin failed")
}

func (s *HandlerWasmClearAdminSuite) TestSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmClearAdminHandler(mgr)

	msg := &wasmtypes.MsgClearAdmin{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingClearAdminWasmManager struct {
	DummyWasmManager
}

func (f *failingClearAdminWasmManager) ClearAdmin(ctx cosmos.Context, contractAddr, sender cosmos.AccAddress) ([]byte, error) {
	return nil, fmt.Errorf("clear admin failed")
}
