package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmUpdateAdminSuite struct{}

var _ = Suite(&HandlerWasmUpdateAdminSuite{})

func (s *HandlerWasmUpdateAdminSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmUpdateAdminSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmUpdateAdminHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmUpdateAdminSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmUpdateAdminHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgUpdateAdmin{
		Sender:   GetRandomBech32Addr().String(),
		NewAdmin: GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgUpdateAdmin while THORChain is halted")
}

func (s *HandlerWasmUpdateAdminSuite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmUpdateAdminHandler(mgr)

	msg := &wasmtypes.MsgUpdateAdmin{
		Sender:   "invalid-address",
		NewAdmin: GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmUpdateAdminSuite) TestInvalidNewAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmUpdateAdminHandler(mgr)

	msg := &wasmtypes.MsgUpdateAdmin{
		Sender:   GetRandomBech32Addr().String(),
		NewAdmin: "invalid-address",
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmUpdateAdminSuite) TestInvalidContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmUpdateAdminHandler(mgr)

	msg := &wasmtypes.MsgUpdateAdmin{
		Sender:   GetRandomBech32Addr().String(),
		NewAdmin: GetRandomBech32Addr().String(),
		Contract: "invalid-address",
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmUpdateAdminSuite) TestUpdateAdminFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingUpdateAdminWasmManager{}
	h := NewWasmUpdateAdminHandler(mgr)

	msg := &wasmtypes.MsgUpdateAdmin{
		Sender:   GetRandomBech32Addr().String(),
		NewAdmin: GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "update admin failed")
}

func (s *HandlerWasmUpdateAdminSuite) TestSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmUpdateAdminHandler(mgr)

	msg := &wasmtypes.MsgUpdateAdmin{
		Sender:   GetRandomBech32Addr().String(),
		NewAdmin: GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingUpdateAdminWasmManager struct {
	DummyWasmManager
}

func (f *failingUpdateAdminWasmManager) UpdateAdmin(ctx cosmos.Context, contractAddr, sender, newAdmin cosmos.AccAddress) ([]byte, error) {
	return nil, fmt.Errorf("update admin failed")
}
