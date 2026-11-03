package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerNoOpSuite struct{}

var _ = Suite(&HandlerNoOpSuite{})

// authorizeNoVault opens the chain-scoped mimir gate so a noop:novault debit is permitted.
func authorizeNoVault(w handlerTestWrapper, chain common.Chain) {
	w.mgr.Keeper().SetMimir(w.ctx, fmt.Sprintf(constants.MimirTemplateAllowNoVault, chain.String()), 1)
}

func (HandlerNoOpSuite) TestNoOpValidation(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// Test 1: Empty action (should pass, does nothing)
	m := NewMsgNoOp(GetRandomObservedTx(), w.activeNodeAccount.NodeAddress, "")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Test 2: Non-novault action (should pass, does nothing)
	m = NewMsgNoOp(GetRandomObservedTx(), w.activeNodeAccount.NodeAddress, "other")
	result, err = h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (HandlerNoOpSuite) TestNoOpVaultValidation(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// Test with nonexistent vault
	obsTx := GetRandomObservedTx()
	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	// The keeper returns "vault not found" when vault doesn't exist
	c.Assert(err.Error(), Matches, ".*vault not found.*")
}

func (HandlerNoOpSuite) TestNoOpInactiveVault(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// Create an inactive vault
	pk := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), InactiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))})
	c.Assert(w.mgr.Keeper().SetVault(w.ctx, vault), IsNil)

	// Create observed tx pointing to the inactive vault
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		common.Gas{{Asset: common.ETHAsset, Amount: cosmos.NewUint(37500)}},
		"",
	)
	obsTx := common.NewObservedTx(tx, 33, pk, 33)

	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*vault is not active or retiring.*")
}

func (HandlerNoOpSuite) TestNoOpInsufficientFunds(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// Create an active vault with limited funds
	pk := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))})
	c.Assert(w.mgr.Keeper().SetVault(w.ctx, vault), IsNil)

	// Create observed tx with more coins than the vault has
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(10*common.One))},
		common.Gas{{Asset: common.ETHAsset, Amount: cosmos.NewUint(37500)}},
		"",
	)
	obsTx := common.NewObservedTx(tx, 33, pk, 33)

	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*vault has insufficient funds.*")
}

// TestNoOpInsufficientFundsAggregatesDuplicateCoins verifies duplicate assets are checked by total amount.
func (HandlerNoOpSuite) TestNoOpInsufficientFundsAggregatesDuplicateCoins(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	pk := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))})
	c.Assert(w.mgr.Keeper().SetVault(w.ctx, vault), IsNil)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{
			common.NewCoin(common.ETHAsset, cosmos.NewUint(30*common.One)),
			common.NewCoin(common.ETHAsset, cosmos.NewUint(30*common.One)),
		},
		common.Gas{{Asset: common.ETHAsset, Amount: cosmos.NewUint(37500)}},
		"",
	)
	obsTx := common.NewObservedTx(tx, 33, pk, 33)

	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*vault has insufficient funds.*")

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(cosmos.NewUint(50*common.One)), Equals, true)
	c.Assert(updatedVault.GetDeficit(common.ETHAsset).IsEmpty(), Equals, true)
}

func (HandlerNoOpSuite) TestNoOpSuccessfulSubtraction(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// Create an active vault with sufficient funds
	pk := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), ActiveVault, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	initialAmount := cosmos.NewUint(100 * common.One)
	subtractAmount := cosmos.NewUint(10 * common.One)
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, initialAmount)})
	c.Assert(w.mgr.Keeper().SetVault(w.ctx, vault), IsNil)

	// Create observed tx with coins to subtract
	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, subtractAmount)},
		common.Gas{{Asset: common.ETHAsset, Amount: cosmos.NewUint(37500)}},
		"",
	)
	obsTx := common.NewObservedTx(tx, 33, pk, 33)

	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify funds were subtracted
	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	expectedAmount := initialAmount.Sub(subtractAmount)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(expectedAmount), Equals, true)
}

func (HandlerNoOpSuite) TestNoOpNoVaultUnauthorized(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// An active vault that has already been credited by the observation pipeline.
	initialAmount := cosmos.NewUint(100 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, ActiveVault, initialAmount, cosmos.NewUint(10*common.One))

	// Gate is closed (no AllowNoVault mimir set): the debit must be skipped and the
	// observation credit left in place, so tracked balance is unchanged from credited.
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(initialAmount), Equals, true)
}

// newNoVaultVaultAndTx returns an ETH vault of the given status credited with initialAmount and an
// observed tx (already credited by the observation pipeline) that a noop:novault
// would debit by subtractAmount.
func newNoVaultVaultAndTx(c *C, w handlerTestWrapper, status VaultStatus, initialAmount, subtractAmount cosmos.Uint) (common.PubKey, common.ObservedTx) {
	pk := GetRandomPubKey()
	vault := NewVault(w.ctx.BlockHeight(), status, AsgardVault, pk, common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.AddFunds(common.Coins{common.NewCoin(common.ETHAsset, initialAmount)})
	c.Assert(w.mgr.Keeper().SetVault(w.ctx, vault), IsNil)

	tx := common.NewTx(
		GetRandomTxHash(),
		GetRandomETHAddress(),
		GetRandomETHAddress(),
		common.Coins{common.NewCoin(common.ETHAsset, subtractAmount)},
		common.Gas{{Asset: common.ETHAsset, Amount: cosmos.NewUint(37500)}},
		"",
	)
	return pk, common.NewObservedTx(tx, 33, pk, 33)
}

// TestNoOpNoVaultOtherChainGate pins the chain scoping of the gate: opening the
// window on one chain must not authorize debits on any other.
func (HandlerNoOpSuite) TestNoOpNoVaultOtherChainGate(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	initialAmount := cosmos.NewUint(100 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, ActiveVault, initialAmount, cosmos.NewUint(10*common.One))

	// Gate open for BTC only; the observed tx is on ETH.
	authorizeNoVault(w, common.BTCChain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(initialAmount), Equals, true)
}

// TestNoOpNoVaultManualOverride pins the indefinite form of the gate: a mimir of 1
// authorizes the debit at any height, not only while 1 >= ctx.BlockHeight().
func (HandlerNoOpSuite) TestNoOpNoVaultManualOverride(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	w.ctx = w.ctx.WithBlockHeight(1000)
	h := NewNoOpHandler(w.mgr)

	initialAmount := cosmos.NewUint(100 * common.One)
	subtractAmount := cosmos.NewUint(10 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, ActiveVault, initialAmount, subtractAmount)

	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(initialAmount.Sub(subtractAmount)), Equals, true)
}

// TestNoOpNoVaultHeightWindowOpen verifies the windowed form of the gate: a mimir set
// to a block height that has not yet passed authorizes the debit.
func (HandlerNoOpSuite) TestNoOpNoVaultHeightWindowOpen(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	w.ctx = w.ctx.WithBlockHeight(1000)
	h := NewNoOpHandler(w.mgr)

	initialAmount := cosmos.NewUint(100 * common.One)
	subtractAmount := cosmos.NewUint(10 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, ActiveVault, initialAmount, subtractAmount)

	// Window closes 100 blocks from now: still open.
	w.mgr.Keeper().SetMimir(w.ctx, fmt.Sprintf(constants.MimirTemplateAllowNoVault, obsTx.Tx.Chain.String()), w.ctx.BlockHeight()+100)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(initialAmount.Sub(subtractAmount)), Equals, true)
}

// TestNoOpNoVaultHeightWindowInclusive pins the inclusive edge of the window: a mimir
// equal to the current block height still authorizes the debit.
func (HandlerNoOpSuite) TestNoOpNoVaultHeightWindowInclusive(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	w.ctx = w.ctx.WithBlockHeight(1000)
	h := NewNoOpHandler(w.mgr)

	initialAmount := cosmos.NewUint(100 * common.One)
	subtractAmount := cosmos.NewUint(10 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, ActiveVault, initialAmount, subtractAmount)

	// Window closes at exactly this height: still open (>=, not >).
	w.mgr.Keeper().SetMimir(w.ctx, fmt.Sprintf(constants.MimirTemplateAllowNoVault, obsTx.Tx.Chain.String()), w.ctx.BlockHeight())
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(initialAmount.Sub(subtractAmount)), Equals, true)
}

// TestNoOpNoVaultHeightWindowExpired verifies the windowed form auto-closes: a mimir
// set to a block height that has already passed leaves the vault credit in place.
func (HandlerNoOpSuite) TestNoOpNoVaultHeightWindowExpired(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	w.ctx = w.ctx.WithBlockHeight(1000)
	h := NewNoOpHandler(w.mgr)

	initialAmount := cosmos.NewUint(100 * common.One)
	subtractAmount := cosmos.NewUint(10 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, ActiveVault, initialAmount, subtractAmount)

	// Window closed one block ago: the gate must be shut again.
	w.mgr.Keeper().SetMimir(w.ctx, fmt.Sprintf(constants.MimirTemplateAllowNoVault, obsTx.Tx.Chain.String()), w.ctx.BlockHeight()-1)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(initialAmount), Equals, true)
}

func (HandlerNoOpSuite) TestNoOpRetiringVaultAllowed(c *C) {
	w := getHandlerTestWrapper(c, 1, true, true)
	h := NewNoOpHandler(w.mgr)

	// A retiring vault with sufficient funds
	initialAmount := cosmos.NewUint(100 * common.One)
	subtractAmount := cosmos.NewUint(10 * common.One)
	pk, obsTx := newNoVaultVaultAndTx(c, w, RetiringVault, initialAmount, subtractAmount)

	authorizeNoVault(w, obsTx.Tx.Chain)
	m := NewMsgNoOp(obsTx, w.activeNodeAccount.NodeAddress, "novault")
	result, err := h.Run(w.ctx, m)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify funds were subtracted
	updatedVault, err := w.mgr.Keeper().GetVault(w.ctx, pk)
	c.Assert(err, IsNil)
	expectedAmount := initialAmount.Sub(subtractAmount)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.Equal(expectedAmount), Equals, true)
}
