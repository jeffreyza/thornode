package thorchain

import (
	"strings"

	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerObservedTxQuorumSuite struct{}

var _ = Suite(&HandlerObservedTxQuorumSuite{})

func (s *HandlerObservedTxQuorumSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// nodeWithPrivKey creates a NodeAccount with proper key setup so we can sign attestations.
func nodeWithPrivKey(c *C, k keeper.Keeper, ctx cosmos.Context) (NodeAccount, secp256k1.PrivKey) {
	privKey := secp256k1.GenPrivKey()
	pubKey := privKey.PubKey()
	nodeAddress := cosmos.AccAddress(pubKey.Address())
	commonPubKey, err := common.NewPubKeyFromCrypto(pubKey)
	c.Assert(err, IsNil)

	na := NewNodeAccount(
		nodeAddress,
		NodeActive,
		common.PubKeySet{
			Secp256k1: commonPubKey,
			Ed25519:   commonPubKey,
		},
		GetRandomBech32ConsensusPubKey(),
		cosmos.NewUint(common.One*100_000),
		GetRandomTHORAddress(),
		1,
	)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	return na, privKey
}

// attestObservedTx creates an attestation for an observed tx using the given private key.
// inbound must match the QuorumTx.Inbound the attestation will be aggregated into;
// it is included in the signed payload to prevent a proposer from flipping the bit.
func attestObservedTx(c *C, privKey secp256k1.PrivKey, obsTx common.ObservedTx, inbound bool) *common.Attestation {
	data, err := obsTx.GetSignablePayloadWithInbound(inbound)
	c.Assert(err, IsNil)

	signature, err := privKey.Sign(data)
	c.Assert(err, IsNil)

	return &common.Attestation{
		PubKey:    privKey.PubKey().Bytes(),
		Signature: signature,
	}
}

// --- Run / validate tests ---

func (s *HandlerObservedTxQuorumSuite) TestRunInvalidMessageType(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	// Pass a wrong message type
	msg := NewMsgNetworkFee(1024, common.ETHChain, 1, 10000, GetRandomBech32Addr())
	result, err := handler.Run(ctx, msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)
}

func (s *HandlerObservedTxQuorumSuite) TestValidateEmptySigner(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	// Empty signer should fail ValidateBasic
	msg := types.NewMsgObservedTxQuorum(nil, cosmos.AccAddress{})
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerObservedTxQuorumSuite) TestValidateNilQuoTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	// Nil QuoTx should fail ValidateBasic
	msg := types.NewMsgObservedTxQuorum(nil, GetRandomBech32Addr())
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

// --- handle tests ---

func (s *HandlerObservedTxQuorumSuite) TestHandleFailListActiveValidators(c *C) {
	ctx, mgr := setupManagerForTest(c)

	failKeeper := &TestObservedTxQuorumKeeper{
		Keeper:                    mgr.Keeper(),
		failListActiveNodeAccount: true,
	}
	mgr.K = failKeeper
	handler := NewObservedTxQuorumHandler(mgr)

	// Create a valid observed tx for an inbound transaction
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(failKeeper.Keeper.SetVault(ctx, vault), IsNil)

	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	quoTx := &common.QuorumTx{
		ObsTx:   obsTx,
		Inbound: true,
	}

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(failKeeper.Keeper.SetNodeAccount(ctx, na), IsNil)
	msg := types.NewMsgObservedTxQuorum(quoTx, na.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleNilQuoTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// Construct message manually with nil QuoTx bypassing ValidateBasic
	msg := &types.MsgObservedTxQuorum{
		QuoTx:  nil,
		Signer: na.NodeAddress,
	}
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, IsNil)
	c.Assert(err, NotNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleInboundVaultNotFound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	na, _ := nodeWithPrivKey(c, mgr.Keeper(), ctx)

	// Vault PubKey that doesn't exist
	vaultPubKey := GetRandomPubKey()
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	att := attestObservedTx(c, secp256k1.GenPrivKey(), obsTx, true)
	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{att},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	// Vault not found is logged but returns empty result, no error
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleOutboundVaultNotFound(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	na, _ := nodeWithPrivKey(c, mgr.Keeper(), ctx)

	// Vault PubKey that doesn't exist
	vaultPubKey := GetRandomPubKey()
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: vaultAddr,
			ToAddress:   GetRandomETHAddress(),
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "OUT:" + GetRandomTxHash().String(),
		},
		1024, vaultPubKey, 1024,
	)

	att := attestObservedTx(c, secp256k1.GenPrivKey(), obsTx, false)
	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      false,
		Attestations: []*common.Attestation{att},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	// Vault not found is logged but returns empty result, no error
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleInboundInvalidAttestations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	na, _ := nodeWithPrivKey(c, mgr.Keeper(), ctx)

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	// Attestation with bad signature (random key, not an active node)
	badAtt := attestObservedTx(c, secp256k1.GenPrivKey(), obsTx, true)

	// Attestation with empty signature
	emptyAtt := &common.Attestation{
		PubKey:    []byte("some-pubkey"),
		Signature: []byte{},
	}

	// Nil attestation
	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{nil, emptyAtt, badAtt},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	// Invalid attestations are logged but handler still returns success
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleInboundValidAttestations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	// Create 4 node accounts with known private keys
	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey2 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey3 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx) // 4th node, doesn't sign

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	// Create valid attestations from 3 nodes (enough for 2/3 consensus with 4 nodes)
	att1 := attestObservedTx(c, privKey1, obsTx, true)
	att2 := attestObservedTx(c, privKey2, obsTx, true)
	att3 := attestObservedTx(c, privKey3, obsTx, true)

	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{att1, att2, att3},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)

	// Check that the voter was updated with signatures
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, obsTx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(len(voter.Txs) > 0, Equals, true)
}

// TestHandleInboundCompromisedVaultNoOp verifies the ebifrost quorum path uses
// the same compromised-vault no-op semantics as legacy MsgObservedTxIn.
func (s *HandlerObservedTxQuorumSuite) TestHandleInboundCompromisedVaultNoOp(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(18)
	handler := NewObservedTxQuorumHandler(mgr)
	k := mgr.Keeper()

	na1, privKey1 := nodeWithPrivKey(c, k, ctx)
	_, privKey2 := nodeWithPrivKey(c, k, ctx)
	_, privKey3 := nodeWithPrivKey(c, k, ctx)
	nodeWithPrivKey(c, k, ctx)

	vaultPubKey := GetRandomPubKey()
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, vaultPubKey, common.Chains{common.ETHChain}.Strings(), nil)
	c.Assert(k.SetVault(ctx, vault), IsNil)
	// The Mimir flag disables value-changing inbound processing for this vault
	// even when enough valid attestations cross quorum.
	k.SetMimir(ctx, compromisedVaultMimirKeyPrefix+vaultPubKey.String(), 1)

	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		12, vaultPubKey, 12,
	)

	quoTx := &common.QuorumTx{
		ObsTx:   obsTx,
		Inbound: true,
		Attestations: []*common.Attestation{
			attestObservedTx(c, privKey1, obsTx, true),
			attestObservedTx(c, privKey2, obsTx, true),
			attestObservedTx(c, privKey3, obsTx, true),
		},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)

	updatedVault, err := k.GetVault(ctx, vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.GetCoin(common.ETHAsset).Amount.IsZero(), Equals, true)
	c.Assert(updatedVault.InboundTxCount, Equals, int64(0))

	voter, err := k.GetObservedTxInVoter(ctx, obsTx.Tx.ID)
	c.Assert(err, IsNil)
	// A finalized no-op is marked done so the voter does not wait for an
	// outbound action that was intentionally skipped.
	c.Assert(voter.Tx.Status, Equals, common.Status_done)
	c.Assert(voter.UpdatedVault, Equals, false)
	// The quorum path should emit the same security event as the legacy
	// observed-tx handler so operators see the compromised-vault no-op.
	securityMsg := compromisedVaultNoOpSecurityMsgFromEvents(ctx)
	c.Assert(securityMsg, Not(Equals), "")
	c.Assert(strings.Contains(securityMsg, vaultPubKey.String()), Equals, true)

	queue := newSwapQueue(k)
	swaps, err := queue.FetchQueue(ctx)
	c.Assert(err, IsNil)
	c.Assert(swaps, HasLen, 0)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleOutboundValidAttestations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	// Create 4 node accounts with known private keys
	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey2 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey3 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx) // 4th node

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: vaultAddr,
			ToAddress:   GetRandomETHAddress(),
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "OUT:" + GetRandomTxHash().String(),
		},
		1024, vaultPubKey, 1024,
	)

	// Create valid attestations from 3 nodes (enough for 2/3 consensus)
	att1 := attestObservedTx(c, privKey1, obsTx, false)
	att2 := attestObservedTx(c, privKey2, obsTx, false)
	att3 := attestObservedTx(c, privKey3, obsTx, false)

	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      false,
		Attestations: []*common.Attestation{att1, att2, att3},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)

	// Check that the voter was updated
	voter, err := mgr.Keeper().GetObservedTxOutVoter(ctx, obsTx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(len(voter.Txs) > 0, Equals, true)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleInboundNoQuorum(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	// Create 6 nodes but only 1 signs (need 4 for consensus)
	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	// Only 1 attestation, not enough for quorum
	att1 := attestObservedTx(c, privKey1, obsTx, true)

	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{att1},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleDuplicateAttestations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx) // 2nd node for active validators count

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	// Duplicate attestations from same key should be deduplicated
	att := attestObservedTx(c, privKey1, obsTx, true)
	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{att, att, att},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestHandleMixedValidInvalidAttestations(c *C) {
	ctx, mgr := setupManagerForTest(c)
	handler := NewObservedTxQuorumHandler(mgr)

	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey2 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey3 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx) // 4th node

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	// Mix of valid and invalid attestations
	att1 := attestObservedTx(c, privKey1, obsTx, true)
	att2 := attestObservedTx(c, privKey2, obsTx, true)
	att3 := attestObservedTx(c, privKey3, obsTx, true)
	badAtt := attestObservedTx(c, secp256k1.GenPrivKey(), obsTx, true) // not active node

	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{att1, badAtt, att2, att3},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)
	result, err := handler.handle(ctx, *msg)
	// Should still succeed, bad attestations are skipped
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestRunFullInboundFlow(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Create 4 node accounts with known private keys
	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey2 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey3 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: GetRandomETHAddress(),
			ToAddress:   vaultAddr,
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "swap:BTC.BTC:" + GetRandomBTCAddress().String(),
		},
		1024, vaultPubKey, 1024,
	)

	att1 := attestObservedTx(c, privKey1, obsTx, true)
	att2 := attestObservedTx(c, privKey2, obsTx, true)
	att3 := attestObservedTx(c, privKey3, obsTx, true)

	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      true,
		Attestations: []*common.Attestation{att1, att2, att3},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)

	handler := NewObservedTxQuorumHandler(mgr)
	result, err := handler.Run(ctx, msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

func (s *HandlerObservedTxQuorumSuite) TestRunFullOutboundFlow(c *C) {
	ctx, mgr := setupManagerForTest(c)

	na1, privKey1 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey2 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	_, privKey3 := nodeWithPrivKey(c, mgr.Keeper(), ctx)
	nodeWithPrivKey(c, mgr.Keeper(), ctx)

	// Set up a vault
	vaultPubKey := GetRandomPubKey()
	vault := NewVault(1024, ActiveVault, AsgardVault, vaultPubKey, []string{
		common.ETHChain.String(),
	}, nil)
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          GetRandomTxHash(),
			Chain:       common.ETHChain,
			FromAddress: vaultAddr,
			ToAddress:   GetRandomETHAddress(),
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "OUT:" + GetRandomTxHash().String(),
		},
		1024, vaultPubKey, 1024,
	)

	att1 := attestObservedTx(c, privKey1, obsTx, false)
	att2 := attestObservedTx(c, privKey2, obsTx, false)
	att3 := attestObservedTx(c, privKey3, obsTx, false)

	quoTx := &common.QuorumTx{
		ObsTx:        obsTx,
		Inbound:      false,
		Attestations: []*common.Attestation{att1, att2, att3},
	}

	msg := types.NewMsgObservedTxQuorum(quoTx, na1.NodeAddress)

	handler := NewObservedTxQuorumHandler(mgr)
	result, err := handler.Run(ctx, msg)
	c.Assert(result, NotNil)
	c.Assert(err, IsNil)
}

// TestObservedTxQuorumKeeper is a keeper wrapper that can simulate failures.
type TestObservedTxQuorumKeeper struct {
	keeper.Keeper
	failListActiveNodeAccount bool
}

func (k *TestObservedTxQuorumKeeper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.failListActiveNodeAccount {
		return NodeAccounts{}, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}
