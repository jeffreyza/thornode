package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmSudoContractSuite struct{}

var _ = Suite(&HandlerWasmSudoContractSuite{})

func (s *HandlerWasmSudoContractSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmSudoContractSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmSudoContractHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmSudoContractSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmSudoContractHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgSudoContract{
		Authority: GetRandomBech32Addr().String(),
		Contract:  GetRandomBech32Addr().String(),
		Msg:       []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgSudoContract while THORChain is halted")
}

func (s *HandlerWasmSudoContractSuite) TestInvalidAuthority(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmSudoContractHandler(mgr)

	msg := &wasmtypes.MsgSudoContract{
		Authority: "invalid-address",
		Contract:  GetRandomBech32Addr().String(),
		Msg:       []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmSudoContractSuite) TestInvalidContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmSudoContractHandler(mgr)

	msg := &wasmtypes.MsgSudoContract{
		Authority: GetRandomBech32Addr().String(),
		Contract:  "invalid-address",
		Msg:       []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmSudoContractSuite) TestSudoFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingSudoWasmManager{}
	h := NewWasmSudoContractHandler(mgr)

	msg := &wasmtypes.MsgSudoContract{
		Authority: GetRandomBech32Addr().String(),
		Contract:  GetRandomBech32Addr().String(),
		Msg:       []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "sudo contract failed")
}

func (s *HandlerWasmSudoContractSuite) TestSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmSudoContractHandler(mgr)

	msg := &wasmtypes.MsgSudoContract{
		Authority: GetRandomBech32Addr().String(),
		Contract:  GetRandomBech32Addr().String(),
		Msg:       []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingSudoWasmManager struct {
	DummyWasmManager
}

func (f *failingSudoWasmManager) SudoContract(ctx cosmos.Context, contractAddress, caller sdk.AccAddress, msg []byte) ([]byte, error) {
	return nil, fmt.Errorf("sudo contract failed")
}
