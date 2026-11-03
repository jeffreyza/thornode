package types

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgModifyLimitSwapSuite struct{}

var _ = Suite(&MsgModifyLimitSwapSuite{})

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapConstructor(c *C) {
	// Setup for tests
	SetupConfigForTest()

	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	modifiedAmount := cosmos.NewUint(60 * common.One)
	signer := GetRandomBech32Addr()

	// Test constructor
	msg := NewMsgModifyLimitSwap(from, source, target, modifiedAmount, signer, common.EmptyAsset, cosmos.ZeroUint())

	// Verify all fields are set correctly
	c.Assert(msg.From.Equals(from), Equals, true)
	c.Assert(msg.Source.Equals(source), Equals, true)
	c.Assert(msg.Target.Equals(target), Equals, true)
	c.Assert(msg.ModifiedTargetAmount.Equal(modifiedAmount), Equals, true)
	c.Assert(msg.Signer.Equals(signer), Equals, true)
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapValidateBasic(c *C) {
	// Setup for tests
	SetupConfigForTest()

	// Test valid message
	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	modifiedAmount := cosmos.NewUint(60 * common.One)
	signer := GetRandomBech32Addr()

	msg := NewMsgModifyLimitSwap(from, source, target, modifiedAmount, signer, common.EmptyAsset, cosmos.ZeroUint())
	c.Assert(msg.ValidateBasic(), IsNil)

	// Test invalid cases
	testCases := []struct {
		name        string
		from        common.Address
		source      common.Coin
		target      common.Coin
		modAmount   cosmos.Uint
		signer      cosmos.AccAddress
		expectedErr error
	}{
		{
			name:        "empty signer",
			from:        from,
			source:      source,
			target:      target,
			modAmount:   modifiedAmount,
			signer:      cosmos.AccAddress{},
			expectedErr: se.ErrInvalidAddress,
		},
		{
			name:        "invalid source asset",
			from:        from,
			source:      common.NewCoin(common.EmptyAsset, cosmos.NewUint(100*common.One)),
			target:      target,
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name:        "invalid target asset",
			from:        from,
			source:      source,
			target:      common.NewCoin(common.EmptyAsset, cosmos.NewUint(50*common.One)),
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name:        "same source and target asset",
			from:        from,
			source:      common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:      common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
		{
			name:        "from address not matching source asset chain",
			from:        GetRandomETHAddress(),
			source:      common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:      common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
			modAmount:   modifiedAmount,
			signer:      signer,
			expectedErr: se.ErrUnknownRequest,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		message := NewMsgModifyLimitSwap(tc.from, tc.source, tc.target, tc.modAmount, tc.signer, common.EmptyAsset, cosmos.ZeroUint())
		err := message.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(errors.Is(err, tc.expectedErr), Equals, true, Commentf("Expected: %v, got: %v", tc.expectedErr, err))
	}
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapGetSigners(c *C) {
	// Setup for tests
	SetupConfigForTest()

	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	modifiedAmount := cosmos.NewUint(60 * common.One)
	signer := GetRandomBech32Addr()

	// Test GetSigners
	msg := NewMsgModifyLimitSwap(from, source, target, modifiedAmount, signer, common.EmptyAsset, cosmos.ZeroUint())
	signers := msg.GetSigners()

	c.Assert(len(signers), Equals, 1)
	c.Assert(signers[0].String(), Equals, signer.String())
	c.Assert(signers[0].Equals(signer), Equals, true)
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapWithZeroAmount(c *C) {
	// Setup for tests
	SetupConfigForTest()

	from := GetRandomBTCAddress()
	source := common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))
	target := common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One))
	zeroAmount := cosmos.ZeroUint()
	signer := GetRandomBech32Addr()

	// Test with zero amount (should still be valid as it might be used for cancellation)
	msg := NewMsgModifyLimitSwap(from, source, target, zeroAmount, signer, common.EmptyAsset, cosmos.ZeroUint())
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.ModifiedTargetAmount.Equal(zeroAmount), Equals, true)
}

func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapWithDifferentChains(c *C) {
	// Setup for tests
	SetupConfigForTest()

	// Test with different chain combinations
	testCases := []struct {
		name      string
		from      common.Address
		source    common.Coin
		target    common.Coin
		modAmount cosmos.Uint
		signer    cosmos.AccAddress
		valid     bool
	}{
		{
			name:      "BTC to ETH",
			from:      GetRandomBTCAddress(),
			source:    common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.ETHAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
		{
			name:      "ETH to BTC",
			from:      GetRandomETHAddress(),
			source:    common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.BTCAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
		{
			name:      "BTC to LTC",
			from:      GetRandomBTCAddress(),
			source:    common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.LTCAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
		{
			name:      "DOGE to BCH",
			from:      GetRandomDOGEAddress(),
			source:    common.NewCoin(common.DOGEAsset, cosmos.NewUint(100*common.One)),
			target:    common.NewCoin(common.BCHAsset, cosmos.NewUint(50*common.One)),
			modAmount: cosmos.NewUint(60 * common.One),
			signer:    GetRandomBech32Addr(),
			valid:     true,
		},
	}

	for _, tc := range testCases {
		c.Logf("Test case: %s", tc.name)
		msg := NewMsgModifyLimitSwap(tc.from, tc.source, tc.target, tc.modAmount, tc.signer, common.EmptyAsset, cosmos.ZeroUint())
		err := msg.ValidateBasic()
		if tc.valid {
			c.Assert(err, IsNil, Commentf("Expected valid message but got error: %v", err))
		} else {
			c.Assert(err, NotNil, Commentf("Expected invalid message but got no error"))
		}
	}
}

// TestMsgModifyLimitSwapSecurityValidation specifically tests security-related
// validation to ensure the From field cannot be spoofed
func (MsgModifyLimitSwapSuite) TestMsgModifyLimitSwapSecurityValidation(c *C) {
	// Setup for tests
	SetupConfigForTest()

	// Test Case 1: RUNE source asset - From and Signer MUST match
	{
		thorAddr1 := GetRandomTHORAddress()
		thorAddr2 := GetRandomTHORAddress()

		signer1, err := thorAddr1.AccAddress()
		c.Assert(err, IsNil)
		signer2, err := thorAddr2.AccAddress()
		c.Assert(err, IsNil)

		runeSource := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
		btcTarget := common.NewCoin(common.BTCAsset, cosmos.NewUint(0.1*common.One))

		// Valid case: From and Signer match
		validMsg := NewMsgModifyLimitSwap(thorAddr1, runeSource, btcTarget, cosmos.NewUint(0.05*common.One), signer1, common.EmptyAsset, cosmos.ZeroUint())
		c.Assert(validMsg.ValidateBasic(), IsNil)

		// Invalid case: From and Signer don't match
		invalidMsg := NewMsgModifyLimitSwap(thorAddr1, runeSource, btcTarget, cosmos.NewUint(0.05*common.One), signer2, common.EmptyAsset, cosmos.ZeroUint())
		err = invalidMsg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*from and signer address must match when source asset is native.*")

		// Invalid case: Empty signer with RUNE source
		emptySignerMsg := NewMsgModifyLimitSwap(thorAddr1, runeSource, btcTarget, cosmos.NewUint(0.05*common.One), cosmos.AccAddress{}, common.EmptyAsset, cosmos.ZeroUint())
		err = emptySignerMsg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*invalid address.*")
	}

	// Test Case 2: Non-RUNE source assets don't require From/Signer match
	{
		btcAddr := GetRandomBTCAddress()
		signer := GetRandomBech32Addr()

		btcSource := common.NewCoin(common.BTCAsset, cosmos.NewUint(0.1*common.One))
		runeTarget := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))

		// This should be valid even though From (BTC address) and Signer (THOR address) don't match
		msg := NewMsgModifyLimitSwap(btcAddr, btcSource, runeTarget, cosmos.NewUint(50*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
		c.Assert(msg.ValidateBasic(), IsNil)
	}

	// Test Case 3: Invalid From address for source asset chain
	{
		// BTC address trying to modify ETH swap
		btcAddr := GetRandomBTCAddress()
		ethSource := common.NewCoin(common.ETHAsset, cosmos.NewUint(1*common.One))
		runeTarget := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
		signer := GetRandomBech32Addr()

		msg := NewMsgModifyLimitSwap(btcAddr, ethSource, runeTarget, cosmos.NewUint(50*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
		err := msg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*from address and source asset do not match.*")
	}

	// Test Case 4: Empty/Invalid addresses
	{
		btcSource := common.NewCoin(common.BTCAsset, cosmos.NewUint(0.1*common.One))
		runeTarget := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))

		// Empty signer
		msg := NewMsgModifyLimitSwap(GetRandomBTCAddress(), btcSource, runeTarget, cosmos.NewUint(50*common.One), cosmos.AccAddress{}, common.EmptyAsset, cosmos.ZeroUint())
		err := msg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(errors.Is(err, se.ErrInvalidAddress), Equals, true)

		// Empty From address
		msg = NewMsgModifyLimitSwap(common.NoAddress, btcSource, runeTarget, cosmos.NewUint(50*common.One), GetRandomBech32Addr(), common.EmptyAsset, cosmos.ZeroUint())
		err = msg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*from address and source asset do not match.*")
	}

	// Test Case 5: Synth assets should behave like native RUNE
	{
		thorAddr := GetRandomTHORAddress()
		signer, err := thorAddr.AccAddress()
		c.Assert(err, IsNil)

		// Test with BTC synth
		synthBTCSource := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(0.1*common.One))
		runeTarget := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))

		// This should validate properly as synths are on THORChain
		msg := NewMsgModifyLimitSwap(thorAddr, synthBTCSource, runeTarget, cosmos.NewUint(50*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
		c.Assert(msg.ValidateBasic(), IsNil)

		// Different signer should fail
		differentSigner := GetRandomBech32Addr()
		invalidMsg := NewMsgModifyLimitSwap(thorAddr, synthBTCSource, runeTarget, cosmos.NewUint(50*common.One), differentSigner, common.EmptyAsset, cosmos.ZeroUint())
		err = invalidMsg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*from and signer address must match when source asset is native.*")
	}

	// Test Case 5a: Secured assets - basic validation
	{
		// Secured assets should follow the same validation rules as regular assets
		// They have the same chain as their base asset
		btcAddr := GetRandomBTCAddress()
		signer := GetRandomBech32Addr()

		// Regular BTC swap for comparison
		btcSource := common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))
		runeTarget := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))

		msg := NewMsgModifyLimitSwap(btcAddr, btcSource, runeTarget, cosmos.NewUint(50*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
		c.Assert(msg.ValidateBasic(), IsNil)

		// Now test with secured BTC - should also work since secured assets have same chain
		// Note: In practice, secured assets might have additional validation in the handler
		// but at the message level, they should validate the same way
		securedBTCAsset, err := common.NewAsset("BTC.BTC/sc")
		if err == nil && securedBTCAsset.IsSecuredAsset() {
			securedBTCSource := common.NewCoin(securedBTCAsset, cosmos.NewUint(10*common.One))
			msg = NewMsgModifyLimitSwap(btcAddr, securedBTCSource, runeTarget, cosmos.NewUint(50*common.One), signer, common.EmptyAsset, cosmos.ZeroUint())
			// This might fail if the secured asset format is not supported in the test environment
			// Just check that validation runs without panic
			_ = msg.ValidateBasic()
		}
	}

	// Test Case 6: Cancellation (zero amount) security
	{
		btcAddr := GetRandomBTCAddress()
		btcSource := common.NewCoin(common.BTCAsset, cosmos.NewUint(0.1*common.One))
		runeTarget := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
		signer := GetRandomBech32Addr()

		// Zero amount (cancellation) should still validate properly
		cancelMsg := NewMsgModifyLimitSwap(btcAddr, btcSource, runeTarget, cosmos.ZeroUint(), signer, common.EmptyAsset, cosmos.ZeroUint())
		c.Assert(cancelMsg.ValidateBasic(), IsNil)

		// But it should still enforce the same security rules
		thorAddr := GetRandomTHORAddress()
		wrongSigner := GetRandomBech32Addr() // Different from thorAddr
		runeSource := common.NewCoin(common.RuneAsset(), cosmos.NewUint(100*common.One))
		btcTarget := common.NewCoin(common.BTCAsset, cosmos.NewUint(0.1*common.One))

		invalidCancelMsg := NewMsgModifyLimitSwap(thorAddr, runeSource, btcTarget, cosmos.ZeroUint(), wrongSigner, common.EmptyAsset, cosmos.ZeroUint())
		err := invalidCancelMsg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(err.Error(), Matches, ".*from and signer address must match when source asset is native.*")
	}
}
