package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmInstantiateContractSuite struct{}

var _ = Suite(&HandlerWasmInstantiateContractSuite{})

func (s *HandlerWasmInstantiateContractSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmInstantiateContractSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContractHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmInstantiateContractSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContractHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgInstantiateContract{
		Sender: GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgInstantiateContract while THORChain is halted")
}

func (s *HandlerWasmInstantiateContractSuite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContractHandler(mgr)

	msg := &wasmtypes.MsgInstantiateContract{
		Sender: "invalid-address",
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmInstantiateContractSuite) TestInvalidAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContractHandler(mgr)

	msg := &wasmtypes.MsgInstantiateContract{
		Sender: GetRandomBech32Addr().String(),
		Admin:  "invalid-address",
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmInstantiateContractSuite) TestInstantiateFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingInstantiateWasmManager{}
	h := NewWasmInstantiateContractHandler(mgr)

	msg := &wasmtypes.MsgInstantiateContract{
		Sender: GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "instantiate contract failed")
}

func (s *HandlerWasmInstantiateContractSuite) TestSuccessNoAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmInstantiateContractHandler(mgr)

	msg := &wasmtypes.MsgInstantiateContract{
		Sender: GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerWasmInstantiateContractSuite) TestSuccessWithAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmInstantiateContractHandler(mgr)

	msg := &wasmtypes.MsgInstantiateContract{
		Sender: GetRandomBech32Addr().String(),
		Admin:  GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingInstantiateWasmManager struct {
	DummyWasmManager
}

func (f *failingInstantiateWasmManager) InstantiateContract(ctx cosmos.Context,
	codeID uint64, creator, admin sdk.AccAddress, initMsg []byte, label string, deposit sdk.Coins,
) (cosmos.AccAddress, []byte, error) {
	return nil, nil, fmt.Errorf("instantiate contract failed")
}
