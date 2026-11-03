package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmStoreCodeSuite struct{}

var _ = Suite(&HandlerWasmStoreCodeSuite{})

func (s *HandlerWasmStoreCodeSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmStoreCodeSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmStoreCodeHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmStoreCodeSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmStoreCodeHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgStoreCode{
		Sender:       GetRandomBech32Addr().String(),
		WASMByteCode: []byte("wasm-code"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgStoreCode while THORChain is halted")
}

func (s *HandlerWasmStoreCodeSuite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmStoreCodeHandler(mgr)

	msg := &wasmtypes.MsgStoreCode{
		Sender:       "invalid-address",
		WASMByteCode: []byte("wasm-code"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmStoreCodeSuite) TestStoreCodeFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingStoreCodeWasmManager{}
	h := NewWasmStoreCodeHandler(mgr)

	msg := &wasmtypes.MsgStoreCode{
		Sender:       GetRandomBech32Addr().String(),
		WASMByteCode: []byte("wasm-code"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "store code failed")
}

func (s *HandlerWasmStoreCodeSuite) TestSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmStoreCodeHandler(mgr)

	msg := &wasmtypes.MsgStoreCode{
		Sender:       GetRandomBech32Addr().String(),
		WASMByteCode: []byte("wasm-code"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingStoreCodeWasmManager struct {
	DummyWasmManager
}

func (f *failingStoreCodeWasmManager) StoreCode(ctx cosmos.Context, creator cosmos.AccAddress, wasmCode []byte) (uint64, []byte, error) {
	return 0, nil, fmt.Errorf("store code failed")
}
