package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

type HandlerModifyLimitSwapSuite struct{}

var _ = Suite(&HandlerModifyLimitSwapSuite{})

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapHandler(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create a valid MsgModifytypes.SwapType_limit
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)
	signer := GetRandomBech32Addr()

	msg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, modifiedTargetAmount, signer, common.EmptyAsset, cosmos.ZeroUint())

	// Test when no matching limit swap exists
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "could not find matching limit swap")

	// Create a limit swap in the keeper
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

	// Set up the swap book item and index
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Test successful modification
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the limit swap was modified
	modifiedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap.TradeTarget.Equal(modifiedTargetAmount), Equals, true)

	// verify original index is no longer there
	items, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, *limitSwap)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)

	// verify new index IS there
	items, err = mgr.Keeper().GetAdvSwapQueueIndex(ctx, modifiedSwap)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 1)

	// Test cancellation (setting modified amount to zero)
	cancelMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, common.NewCoin(targetAsset, modifiedTargetAmount), cosmos.ZeroUint(), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err = handler.Run(ctx, cancelMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the limit swap was removed from the swap book
	items, err = mgr.Keeper().GetAdvSwapQueueIndex(ctx, modifiedSwap)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)

	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil) // Should be removed
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Create a manager with our test keeper
	mgr := NewDummyMgr()
	mgr.K = k

	handler := NewModifyLimitSwapHandler(mgr)

	// Test with invalid message (empty signer)
	fromAddr := GetRandomTHORAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)

	invalidMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, modifiedTargetAmount, cosmos.AccAddress{}, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, invalidMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapAddressCheck(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create addresses
	fromAddr := GetRandomTHORAddress()
	differentAddr := GetRandomTHORAddress()
	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)
	// When source asset is RUNE, signer must match fromAddr
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	// Create a limit swap in the keeper
	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

	// Set up the swap book item and index
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Try to modify with a different address
	// When source asset is RUNE, signer must match the From address
	differentSigner, err := differentAddr.AccAddress()
	c.Assert(err, IsNil)
	msg := types.NewMsgModifyLimitSwap(differentAddr, sourceCoin, targetCoin, modifiedTargetAmount, differentSigner, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "could not find matching limit swap")

	// Verify the original limit swap is unchanged
	originalSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil)
	c.Assert(originalSwap.TradeTarget.Equal(targetCoin.Amount), Equals, true)
}

func (s *HandlerModifyLimitSwapSuite) TestModifyMultipleLimitSwaps(c *C) {
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create addresses and assets
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	modifiedTargetAmount := cosmos.NewUint(600 * common.One)
	signer := GetRandomBech32Addr()

	// Create multiple limit swaps with the same source/target
	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Modify both limit swaps
	msg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, modifiedTargetAmount, signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify only the first limit swap was modified (only one swap should be modified)
	modifiedSwap1, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap1.TradeTarget.Equal(modifiedTargetAmount), Equals, true)

	// The second swap should remain unchanged
	modifiedSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap2.TradeTarget.Equal(targetCoin.Amount), Equals, true) // Should still be original amount
}

func (s *HandlerModifyLimitSwapSuite) TestCancelMultipleLimitSwaps(c *C) {
	// Create a manager with our test keeper
	ctx, mgr := setupManagerForTest(c)

	handler := NewModifyLimitSwapHandler(mgr)

	// Create addresses and assets
	fromAddr := GetRandomTHORAddress()
	sourceAsset := common.RuneAsset()
	targetAsset := common.BTCAsset
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	// When source asset is RUNE, signer must match fromAddr
	signer, err := fromAddr.AccAddress()
	c.Assert(err, IsNil)

	// Create multiple limit swaps with the same source/target
	txID1 := GetRandomTxHash()
	tx1 := common.NewTx(
		txID1,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap1 := NewMsgSwap(tx1, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap1), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap1), IsNil)

	txID2 := GetRandomTxHash()
	tx2 := common.NewTx(
		txID2,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap2 := NewMsgSwap(tx2, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

	// Cancel both limit swaps by setting modified amount to zero
	cancelMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, cosmos.ZeroUint(), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, cancelMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify only the first limit swap was cancelled (removed)
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID1, 0)
	c.Assert(err, NotNil) // Should be removed

	// The second swap should remain unchanged as a limit swap
	mSwap2, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2, 0)
	c.Assert(err, IsNil)
	c.Check(mSwap2.SwapType, Equals, types.SwapType_limit) // Should still be a limit swap
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapErrorHandling(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Test with invalid assets (same source and target)
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	invalidCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	signer := GetRandomBech32Addr()

	invalidMsg := types.NewMsgModifyLimitSwap(fromAddr, invalidCoin, invalidCoin, cosmos.NewUint(200*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, invalidMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*source asset and target asset cannot be the same.*")

	// Test with mismatched from address and source asset chain
	thorAddr := GetRandomTHORAddress()
	btcCoin := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	runeCoin := common.NewCoin(common.RuneAsset(), cosmos.NewUint(500*common.One))

	invalidChainMsg := types.NewMsgModifyLimitSwap(thorAddr, btcCoin, runeCoin, cosmos.NewUint(600*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err = handler.Run(ctx, invalidChainMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Matches, ".*from address and source asset do not match.*")
}

func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapCancellationLogic(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Create a limit swap with a very large target amount
	fromAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	largeAmount := cosmos.NewUint(1 << 62) // Very large amount
	targetCoin := common.NewCoin(targetAsset, largeAmount)
	signer := GetRandomBech32Addr()

	txID := GetRandomTxHash()
	tx := common.NewTx(
		txID,
		fromAddr,
		fromAddr,
		common.Coins{sourceCoin},
		common.Gas{},
		"",
	)
	limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

	// Set up the swap book item and index
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

	// Test cancellation with zero amount
	cancelMsg := types.NewMsgModifyLimitSwap(fromAddr, sourceCoin, targetCoin, cosmos.ZeroUint(), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, cancelMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the swap was removed
	_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, NotNil) // Should be removed
}

// TestModifyLimitSwapSecurityFromFieldSpoofing tests various scenarios where
// a malicious actor attempts to spoof the From field to modify or cancel
// another user's limit swap. This test verifies that the handler properly
// checks both the From address AND the Signer to prevent unauthorized modifications.
func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapSecurityFromFieldSpoofing(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Test Case 1: Malicious actor tries to modify another user's BTC limit swap by using wrong From address
	{
		// Legitimate user creates a limit swap
		legitimateUser := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			legitimateUser,
			legitimateUser,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		limitSwap := NewMsgSwap(tx, targetAsset, legitimateUser, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

		// Set up the swap in the keeper
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

		// Malicious actor attempts to modify the limit swap using a different From address
		maliciousUser := GetRandomBTCAddress()
		maliciousActor := GetRandomBech32Addr()
		maliciousMsg := types.NewMsgModifyLimitSwap(
			maliciousUser, // Using wrong From address
			sourceCoin,
			targetCoin,
			cosmos.NewUint(100*common.One), // Trying to reduce the target amount
			maliciousActor,
			common.EmptyAsset,
			cosmos.ZeroUint(),
		)

		// The handler should reject this because the From address doesn't match
		result, err := handler.Run(ctx, maliciousMsg)
		c.Assert(err, NotNil)
		c.Assert(result, IsNil)
		c.Assert(err.Error(), Equals, "could not find matching limit swap")

		// Verify the original limit swap is unchanged
		originalSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, IsNil)
		c.Assert(originalSwap.TradeTarget.Equal(targetCoin.Amount), Equals, true)
	}

	// Test Case 2: Malicious actor tries to cancel another user's limit swap
	{
		// Create a new limit swap for a different user
		legitimateUser2 := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(200*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(1000*common.One))
		signer2 := GetRandomBech32Addr()

		txID2 := GetRandomTxHash()
		tx2 := common.NewTx(
			txID2,
			legitimateUser2,
			legitimateUser2,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		limitSwap2 := NewMsgSwap(tx2, targetAsset, legitimateUser2, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer2)

		// Set up the swap in the keeper
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap2), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap2), IsNil)

		// Malicious actor attempts to cancel using wrong From address
		maliciousUser := GetRandomBTCAddress()
		maliciousActor := GetRandomBech32Addr()
		cancelMsg := types.NewMsgModifyLimitSwap(
			maliciousUser, // Using wrong From address
			sourceCoin,
			targetCoin,
			cosmos.ZeroUint(), // Trying to cancel
			maliciousActor,
			common.EmptyAsset,
			cosmos.ZeroUint(),
		)

		// The handler should reject this because From doesn't match
		result, err := handler.Run(ctx, cancelMsg)
		c.Assert(err, NotNil)
		c.Assert(result, IsNil)
		c.Assert(err.Error(), Equals, "could not find matching limit swap")

		// Verify the limit swap still exists
		stillExists, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID2, 0)
		c.Assert(err, IsNil)
		c.Assert(stillExists.TradeTarget.Equal(targetCoin.Amount), Equals, true)
	}

	// Test Case 3: RUNE-based limit swap - From and Signer mismatch validation
	{
		// For RUNE swaps, the From address must match the Signer
		legitimateUser := GetRandomTHORAddress()
		maliciousUser := GetRandomTHORAddress()
		sourceAsset := common.RuneAsset()
		targetAsset := common.BTCAsset
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(0.1*common.One))

		_, err := legitimateUser.AccAddress()
		c.Assert(err, IsNil)
		maliciousSigner, err := maliciousUser.AccAddress()
		c.Assert(err, IsNil)

		// Try to create a modify message where From doesn't match Signer
		invalidMsg := types.NewMsgModifyLimitSwap(
			legitimateUser, // From: legitimate user
			sourceCoin,
			targetCoin,
			cosmos.NewUint(0.05*common.One),
			maliciousSigner, // Signer: malicious user
			common.EmptyAsset,
			cosmos.ZeroUint(),
		)

		// This should fail validation
		err = invalidMsg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*from and signer address must match when source asset is native.*")
	}

	// Test Case 4: Direct handler invocation with mismatched From/Signer for external chain swaps.
	//
	// This test calls handler.Run() directly with a hand-crafted MsgModifyLimitSwap where
	// From != Signer. While this "succeeds" in the test, it is NOT an exploitable vulnerability
	// because MsgModifyLimitSwap cannot be submitted directly to the chain:
	//   - It is not registered in the gRPC service descriptor.
	//   - For native deposits (MsgDeposit), From is derived from the signer's account in DepositHandler.handle().
	//   - For external chains, From comes from the observed on-chain tx via getMsgModifyLimitSwap(),
	//     which requires the victim's private key to spoof.
	//
	// This test exists to document this design decision and verify the handler's behavior
	// when called directly (e.g., from other internal code paths).
	{
		// User A creates a limit swap
		userAAddr := GetRandomBTCAddress()
		// User B creates a similar limit swap
		userBAddr := GetRandomBTCAddress()

		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
		signerA := GetRandomBech32Addr()
		signerB := GetRandomBech32Addr()

		// Create User A's swap
		txIDA := GetRandomTxHash()
		txA := common.NewTx(txIDA, userAAddr, userAAddr, common.Coins{sourceCoin}, common.Gas{}, "")
		limitSwapA := NewMsgSwap(txA, targetAsset, userAAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signerA)
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwapA), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwapA), IsNil)

		// Create User B's swap
		txIDB := GetRandomTxHash()
		txB := common.NewTx(txIDB, userBAddr, userBAddr, common.Coins{sourceCoin}, common.Gas{}, "")
		limitSwapB := NewMsgSwap(txB, targetAsset, userBAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signerB)
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwapB), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwapB), IsNil)

		// In a direct handler.Run() call, the From/Signer mismatch is not checked for external chains.
		// This is safe because this code path is unreachable in production (see comment above).
		maliciousMsg := types.NewMsgModifyLimitSwap(
			userAAddr, // Using User A's address
			sourceCoin,
			targetCoin,
			cosmos.NewUint(100*common.One),
			signerB, // But signing with User B's key
			common.EmptyAsset,
			cosmos.ZeroUint(),
		)

		// This succeeds because the handler only checks From address match, not From/Signer ownership.
		// In production, the From field is always authenticated by the message flow (see validate() comment).
		result, err := handler.Run(ctx, maliciousMsg)
		c.Assert(err, IsNil)
		c.Assert(result, NotNil)

		// The swap was modified via direct handler invocation (not reachable in production)
		modifiedSwapA, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txIDA, 0)
		c.Assert(err, IsNil)
		c.Assert(modifiedSwapA.TradeTarget.Equal(cosmos.NewUint(100*common.One)), Equals, true)

		// User B's swap remains unchanged
		swapB, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txIDB, 0)
		c.Assert(err, IsNil)
		c.Assert(swapB.TradeTarget.Equal(targetCoin.Amount), Equals, true)

		// User A can also modify their own swap (which was already modified to 100 above)
		validMsg := types.NewMsgModifyLimitSwap(
			userAAddr,
			sourceCoin,
			common.NewCoin(targetAsset, cosmos.NewUint(100*common.One)), // Current target
			cosmos.NewUint(600*common.One),
			signerA,
			common.EmptyAsset,
			cosmos.ZeroUint(),
		)
		result, err = handler.Run(ctx, validMsg)
		c.Assert(err, IsNil)
		c.Assert(result, NotNil)

		// Verify User A's swap was modified to 600
		finalSwapA, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txIDA, 0)
		c.Assert(err, IsNil)
		c.Assert(finalSwapA.TradeTarget.Equal(cosmos.NewUint(600*common.One)), Equals, true)

		// User B's swap should remain unchanged
		unchangedSwapB, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txIDB, 0)
		c.Assert(err, IsNil)
		c.Assert(unchangedSwapB.TradeTarget.Equal(targetCoin.Amount), Equals, true)
	}
}

// TestCancelLimitSwap tests the canceltypes.SwapType_limit method directly
func (s *HandlerModifyLimitSwapSuite) TestCancelLimitSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Test Case 1: Cancel a limit swap with no partial execution
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

		// Set up the swap in the keeper
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

		// Cancel the limit swap
		err := handler.cancelLimitSwap(ctx, *limitSwap)
		c.Assert(err, IsNil)

		// Verify the swap was removed from the queue
		_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, NotNil) // Should be removed

		// Verify index was removed
		items, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, *limitSwap)
		c.Assert(err, IsNil)
		c.Assert(items, HasLen, 0)
	}

	// Test Case 2: Cancel a streaming limit swap with partial execution
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		// Create a streaming limit swap with 10 sub-swaps
		streamingSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 10, 1, types.SwapVersion_v2, signer)

		// Simulate partial execution - 3 successful swaps
		streamingSwap.State.Count = 3
		streamingSwap.State.In = cosmos.NewUint(30 * common.One)   // 30% executed
		streamingSwap.State.Out = cosmos.NewUint(150 * common.One) // Got 150 RUNE

		// Set up the swap in the keeper
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *streamingSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *streamingSwap), IsNil)

		// Cancel the streaming swap
		err := handler.cancelLimitSwap(ctx, *streamingSwap)
		c.Assert(err, IsNil)

		// Verify the swap was removed
		_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, NotNil) // Should be removed
	}

	// Test Case 3: Cancel limit swap with failed sub-swaps
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		// Create a streaming limit swap with some failed swaps
		failedSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 5, 1, types.SwapVersion_v2, signer)

		// Simulate 2 failed swaps out of 3 attempts
		failedSwap.State.Count = 3
		failedSwap.State.FailedSwaps = []uint64{0, 2}
		failedSwap.State.In = cosmos.NewUint(20 * common.One) // Only 1 successful swap
		failedSwap.State.Out = cosmos.NewUint(100 * common.One)

		// Set up the swap in the keeper
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *failedSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *failedSwap), IsNil)

		// Cancel the swap
		err := handler.cancelLimitSwap(ctx, *failedSwap)
		c.Assert(err, IsNil)

		// Verify removal
		_, err = mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, NotNil)
	}
}

// TestModifyLimitSwap tests the modifytypes.SwapType_limit method directly
func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Test Case 1: Successfully modify trade target amount
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		originalTarget := cosmos.NewUint(500 * common.One)
		newTarget := cosmos.NewUint(600 * common.One)
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, originalTarget, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

		// Set up the swap in the keeper
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

		// Verify original index exists
		originalHashes, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, *limitSwap)
		c.Assert(err, IsNil)
		c.Assert(originalHashes, HasLen, 1)

		// Modify the swap
		err = handler.modifyLimitSwap(ctx, *limitSwap, newTarget)
		c.Assert(err, IsNil)

		// Verify the trade target was updated
		modifiedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, IsNil)
		c.Assert(modifiedSwap.TradeTarget.Equal(newTarget), Equals, true)

		// Verify old index was removed
		oldItems, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, *limitSwap)
		c.Assert(err, IsNil)
		c.Assert(oldItems, HasLen, 0)

		// Verify new index was created
		newItems, err := mgr.Keeper().GetAdvSwapQueueIndex(ctx, modifiedSwap)
		c.Assert(err, IsNil)
		c.Assert(newItems, HasLen, 1)
		c.Assert(newItems[0].TxID.Equals(txID), Equals, true)
	}

	// Test Case 2: Modify to same amount (edge case)
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(50*common.One))
		targetAmount := cosmos.NewUint(250 * common.One)
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, targetAmount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

		// Set up the swap
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

		// Modify to same amount
		err := handler.modifyLimitSwap(ctx, *limitSwap, targetAmount)
		c.Assert(err, IsNil)

		// Verify nothing broke
		sameSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, IsNil)
		c.Assert(sameSwap.TradeTarget.Equal(targetAmount), Equals, true)
	}

	// Test Case 3: Modify with very large amount
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(1000*common.One))
		originalTarget := cosmos.NewUint(5000 * common.One)
		largeTarget := cosmos.NewUint(1 << 62) // Very large amount
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		limitSwap := NewMsgSwap(tx, targetAsset, fromAddr, originalTarget, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)

		// Set up the swap
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *limitSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *limitSwap), IsNil)

		// Modify to very large amount
		err := handler.modifyLimitSwap(ctx, *limitSwap, largeTarget)
		c.Assert(err, IsNil)

		// Verify the large amount was set correctly
		largeSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, IsNil)
		c.Assert(largeSwap.TradeTarget.Equal(largeTarget), Equals, true)
	}

	// Test Case 4: Modify streaming limit swap mid-execution
	{
		fromAddr := GetRandomBTCAddress()
		sourceAsset := common.BTCAsset
		targetAsset := common.RuneAsset()
		sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
		originalTarget := cosmos.NewUint(500 * common.One)
		newTarget := cosmos.NewUint(800 * common.One)
		signer := GetRandomBech32Addr()

		txID := GetRandomTxHash()
		tx := common.NewTx(
			txID,
			fromAddr,
			fromAddr,
			common.Coins{sourceCoin},
			common.Gas{},
			"",
		)
		// Create streaming swap with partial execution
		streamingSwap := NewMsgSwap(tx, targetAsset, fromAddr, originalTarget, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 10, 1, types.SwapVersion_v2, signer)
		streamingSwap.State.Count = 3
		streamingSwap.State.In = cosmos.NewUint(30 * common.One)
		streamingSwap.State.Out = cosmos.NewUint(150 * common.One)

		// Set up the swap
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *streamingSwap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *streamingSwap), IsNil)

		// Modify mid-execution
		err := handler.modifyLimitSwap(ctx, *streamingSwap, newTarget)
		c.Assert(err, IsNil)

		// Verify modification
		modifiedStreaming, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, txID, 0)
		c.Assert(err, IsNil)
		c.Assert(modifiedStreaming.TradeTarget.Equal(newTarget), Equals, true)
		// Verify execution state is preserved
		c.Assert(modifiedStreaming.State.Count, Equals, uint64(3))
		c.Assert(modifiedStreaming.State.In.Equal(cosmos.NewUint(30*common.One)), Equals, true)
	}
}

// TestDonateToPool tests the donateToPool method directly
func (s *HandlerModifyLimitSwapSuite) TestDonateToPool(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Test Case 1: Successfully donate BTC to BTC pool
	{
		// Create BTC pool
		btcPool := NewPool()
		btcPool.Asset = common.BTCAsset
		btcPool.BalanceRune = cosmos.NewUint(1000 * common.One)
		btcPool.BalanceAsset = cosmos.NewUint(10 * common.One)
		btcPool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, btcPool), IsNil)

		donationAmount := cosmos.NewUint(1 * common.One)
		fromAddr := GetRandomBTCAddress()

		// Donate BTC to BTC pool
		err := handler.donateToPool(ctx, common.BTCAsset, donationAmount, fromAddr)
		c.Assert(err, IsNil)

		// Verify pool balance increased
		updatedPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(updatedPool.BalanceRune.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
		c.Assert(updatedPool.BalanceAsset.Equal(cosmos.NewUint(11*common.One)), Equals, true)
	}

	// Test Case 2: Successfully donate asset to pool
	{
		// Create ETH pool
		ethPool := NewPool()
		ethPool.Asset = common.ETHAsset
		ethPool.BalanceRune = cosmos.NewUint(2000 * common.One)
		ethPool.BalanceAsset = cosmos.NewUint(20 * common.One)
		ethPool.Status = PoolAvailable
		c.Assert(mgr.Keeper().SetPool(ctx, ethPool), IsNil)

		donationAmount := cosmos.NewUint(5 * common.One)
		fromAddr := GetRandomETHAddress()

		// Donate ETH to ETH pool
		err := handler.donateToPool(ctx, common.ETHAsset, donationAmount, fromAddr)
		c.Assert(err, IsNil)

		// Verify pool balance increased
		updatedPool, err := mgr.Keeper().GetPool(ctx, common.ETHAsset)
		c.Assert(err, IsNil)
		c.Assert(updatedPool.BalanceRune.Equal(cosmos.NewUint(2000*common.One)), Equals, true)
		c.Assert(updatedPool.BalanceAsset.Equal(cosmos.NewUint(25*common.One)), Equals, true)
	}

	// Test Case 3: Donate to non-existent pool
	{
		donationAmount := cosmos.NewUint(50 * common.One)
		fromAddr := GetRandomBCHAddress()

		// Try to donate to non-existent BCH pool
		err := handler.donateToPool(ctx, common.BCHAsset, donationAmount, fromAddr)
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*pool does not exist.*")
	}

	// Test Case 4: Donate to suspended pool
	{
		// Create suspended DOGE pool
		dogePool := NewPool()
		dogePool.Asset = common.DOGEAsset
		dogePool.BalanceRune = cosmos.NewUint(500 * common.One)
		dogePool.BalanceAsset = cosmos.NewUint(50000 * common.One)
		dogePool.Status = PoolSuspended
		c.Assert(mgr.Keeper().SetPool(ctx, dogePool), IsNil)

		donationAmount := cosmos.NewUint(10 * common.One)
		fromAddr := GetRandomDOGEAddress()

		// Try to donate to suspended pool (should succeed)
		err := handler.donateToPool(ctx, common.DOGEAsset, donationAmount, fromAddr)
		c.Assert(err, IsNil)

		// Verify the donation was added
		updatedPool, err := mgr.Keeper().GetPool(ctx, common.DOGEAsset)
		c.Assert(err, IsNil)
		c.Assert(updatedPool.BalanceAsset.Equal(cosmos.NewUint(50010*common.One)), Equals, true)
	}

	// Test Case 5: Zero amount donation (edge case)
	{
		// Use existing BTC pool from test case 1
		donationAmount := cosmos.ZeroUint()
		fromAddr := GetRandomBTCAddress()

		// Try zero donation
		err := handler.donateToPool(ctx, common.BTCAsset, donationAmount, fromAddr)
		c.Assert(err, IsNil) // Should succeed even with zero

		// Verify balance unchanged
		btcPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(btcPool.BalanceAsset.Equal(cosmos.NewUint(11*common.One)), Equals, true) // Unchanged from test case 1
	}

	// Test Case 6: Donate synthetic asset
	{
		// Create synth BTC pool if needed
		synthBTC := common.BTCAsset.GetSyntheticAsset()

		donationAmount := cosmos.NewUint(25 * common.One)
		fromAddr := GetRandomTHORAddress()

		// For native assets (like synths), funds must be in Reserve module first
		// (simulating what handler_deposit.go does when receiving a native deposit)
		synthCoin := common.NewCoin(synthBTC, donationAmount)
		c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, synthCoin), IsNil)
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, ReserveName, common.NewCoins(synthCoin)), IsNil)

		// Donate synth BTC (should go to BTC pool as asset)
		err := handler.donateToPool(ctx, synthBTC, donationAmount, fromAddr)
		c.Assert(err, IsNil)

		// Verify it went to the BTC pool's asset balance
		btcPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		c.Assert(btcPool.BalanceAsset.Equal(cosmos.NewUint(36*common.One)), Equals, true) // 11 + 25
	}

	// Test Case 7: Large donation amount
	{
		largeAmount := cosmos.NewUint(1 << 62) // Very large amount
		fromAddr := GetRandomBTCAddress()

		// Large BTC donation
		err := handler.donateToPool(ctx, common.BTCAsset, largeAmount, fromAddr)
		c.Assert(err, IsNil)

		// Verify large amount was added correctly
		btcPool, err := mgr.Keeper().GetPool(ctx, common.BTCAsset)
		c.Assert(err, IsNil)
		expectedBalance := cosmos.NewUint(36 * common.One).Add(largeAmount) // 11 + 25 from previous tests
		c.Assert(btcPool.BalanceAsset.Equal(expectedBalance), Equals, true)
	}

	// Test Case 8: Donate RUNE - RUNE stays in Reserve, no pool donation
	{
		// When the asset is RUNE, donateToPool skips the donation since RUNE has no pool.
		// The funds were already sent to Reserve by handler_deposit.go.
		donationAmount := cosmos.NewUint(100 * common.One)
		fromAddr := GetRandomTHORAddress()

		// RUNE donation should succeed (it just does nothing)
		err := handler.donateToPool(ctx, common.RuneAsset(), donationAmount, fromAddr)
		c.Assert(err, IsNil)
	}
}

// TestModifyLimitSwapIterationLimit verifies that the handle() loop respects
// the ModifyLimitSwapMaxIterations limit to prevent DoS via bloated index buckets.
func (s *HandlerModifyLimitSwapSuite) TestModifyLimitSwapIterationLimit(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewModifyLimitSwapHandler(mgr)

	// Set a low iteration limit for testing
	mgr.Keeper().SetMimir(ctx, constants.ModifyLimitSwapMaxIterations.String(), 5)

	fromAddr := GetRandomBTCAddress()
	victimAddr := GetRandomBTCAddress()
	sourceAsset := common.BTCAsset
	targetAsset := common.RuneAsset()
	sourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	targetCoin := common.NewCoin(targetAsset, cosmos.NewUint(500*common.One))
	signer := GetRandomBech32Addr()

	// Create many swaps from different addresses at the same ratio to fill the index bucket
	for i := 0; i < 10; i++ {
		addr := GetRandomBTCAddress()
		txID := GetRandomTxHash()
		tx := common.NewTx(txID, addr, addr, common.Coins{sourceCoin}, common.Gas{}, "")
		swap := NewMsgSwap(tx, targetAsset, addr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, GetRandomBech32Addr())
		c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *swap), IsNil)
		c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *swap), IsNil)
	}

	// Create victim's swap (appended after the 10 above)
	victimTxID := GetRandomTxHash()
	victimTx := common.NewTx(victimTxID, victimAddr, victimAddr, common.Coins{sourceCoin}, common.Gas{}, "")
	victimSwap := NewMsgSwap(victimTx, targetAsset, victimAddr, targetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *victimSwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *victimSwap), IsNil)

	// Try to modify the victim's swap - should fail because max iterations (5) is exceeded
	// before reaching the victim's entry (at position 10)
	msg := types.NewMsgModifyLimitSwap(victimAddr, sourceCoin, targetCoin, cosmos.NewUint(600*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, fmt.Sprintf("swap index too large, exceeded max iterations (%d)", 5))

	// Verify the victim's swap was NOT modified
	unchangedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, victimTxID, 0)
	c.Assert(err, IsNil)
	c.Assert(unchangedSwap.TradeTarget.Equal(targetCoin.Amount), Equals, true)

	// Now set a higher limit and verify the modification succeeds
	mgr.Keeper().SetMimir(ctx, constants.ModifyLimitSwapMaxIterations.String(), 100)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify the victim's swap WAS modified
	modifiedSwap, err := mgr.Keeper().GetAdvSwapQueueItem(ctx, victimTxID, 0)
	c.Assert(err, IsNil)
	c.Assert(modifiedSwap.TradeTarget.Equal(cosmos.NewUint(600*common.One)), Equals, true)

	// Test that swap found within the limit succeeds
	// Create a swap from fromAddr in a DIFFERENT ratio bucket (different target amount = different ratio)
	earlyTxID := GetRandomTxHash()
	earlySourceCoin := common.NewCoin(sourceAsset, cosmos.NewUint(100*common.One))
	earlyTargetCoin := common.NewCoin(targetAsset, cosmos.NewUint(1000*common.One)) // different ratio than 100:500
	earlyTx := common.NewTx(earlyTxID, fromAddr, fromAddr, common.Coins{earlySourceCoin}, common.Gas{}, "")
	earlySwap := NewMsgSwap(earlyTx, targetAsset, fromAddr, earlyTargetCoin.Amount, common.NoAddress, cosmos.ZeroUint(), "", "", nil, types.SwapType_limit, 0, 0, types.SwapVersion_v2, signer)
	c.Assert(mgr.Keeper().SetAdvSwapQueueItem(ctx, *earlySwap), IsNil)
	c.Assert(mgr.Keeper().SetAdvSwapQueueIndex(ctx, *earlySwap), IsNil)

	// Set low iteration limit - swap at position 0 in its own ratio bucket should still be found
	mgr.Keeper().SetMimir(ctx, constants.ModifyLimitSwapMaxIterations.String(), 1)
	earlyMsg := types.NewMsgModifyLimitSwap(fromAddr, earlySourceCoin, earlyTargetCoin, cosmos.NewUint(1200*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
	result, err = handler.Run(ctx, earlyMsg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}
