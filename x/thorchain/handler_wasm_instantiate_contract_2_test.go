package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

type HandlerWasmInstantiateContract2Suite struct{}

var _ = Suite(&HandlerWasmInstantiateContract2Suite{})

func (s *HandlerWasmInstantiateContract2Suite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmInstantiateContract2Suite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContract2Handler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmInstantiateContract2Suite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContract2Handler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgInstantiateContract2{
		Sender: GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
		Salt:   []byte("salt"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgInstantiateContract2 while THORChain is halted")
}

func (s *HandlerWasmInstantiateContract2Suite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContract2Handler(mgr)

	msg := &wasmtypes.MsgInstantiateContract2{
		Sender: "invalid-address",
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
		Salt:   []byte("salt"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmInstantiateContract2Suite) TestInvalidAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmInstantiateContract2Handler(mgr)

	msg := &wasmtypes.MsgInstantiateContract2{
		Sender: GetRandomBech32Addr().String(),
		Admin:  "invalid-address",
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
		Salt:   []byte("salt"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmInstantiateContract2Suite) TestInstantiate2Fails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingInstantiate2WasmManager{}
	h := NewWasmInstantiateContract2Handler(mgr)

	msg := &wasmtypes.MsgInstantiateContract2{
		Sender: GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
		Salt:   []byte("salt"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "instantiate contract 2 failed")
}

func (s *HandlerWasmInstantiateContract2Suite) TestSuccessNoAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmInstantiateContract2Handler(mgr)

	msg := &wasmtypes.MsgInstantiateContract2{
		Sender: GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
		Salt:   []byte("salt"),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerWasmInstantiateContract2Suite) TestSuccessWithAdmin(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmInstantiateContract2Handler(mgr)

	msg := &wasmtypes.MsgInstantiateContract2{
		Sender: GetRandomBech32Addr().String(),
		Admin:  GetRandomBech32Addr().String(),
		CodeID: 1,
		Label:  "test",
		Msg:    []byte(`{}`),
		Salt:   []byte("salt"),
		FixMsg: true,
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

type failingInstantiate2WasmManager struct {
	DummyWasmManager
}

func (f *failingInstantiate2WasmManager) InstantiateContract2(ctx cosmos.Context,
	codeID uint64, creator, admin sdk.AccAddress, initMsg []byte, label string, deposit sdk.Coins, salt []byte, fixMsg bool,
) (sdk.AccAddress, []byte, error) {
	return nil, nil, fmt.Errorf("instantiate contract 2 failed")
}
