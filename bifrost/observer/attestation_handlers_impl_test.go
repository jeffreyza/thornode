package observer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

// Test the AttestMessage interface implementations
func TestAttestMessageInterface(t *testing.T) {
	// Create sample data for each message type
	tx := common.Tx{
		ID:    "tx1",
		Chain: common.BTCChain,
	}
	obsTx := common.ObservedTx{
		Tx: tx,
	}
	networkFee := common.NetworkFee{
		Chain:  common.BTCChain,
		Height: 100,
	}
	solvency := common.Solvency{
		Chain:  common.BTCChain,
		Height: 200,
	}
	errataTx := common.ErrataTx{
		Chain: common.BTCChain,
		Id:    "tx1",
	}

	// Create attestation messages
	attestTx := common.AttestTx{
		ObsTx:       obsTx,
		Attestation: &common.Attestation{},
	}
	attestNetworkFee := common.AttestNetworkFee{
		NetworkFee:  &networkFee,
		Attestation: &common.Attestation{},
	}
	attestSolvency := common.AttestSolvency{
		Solvency:    &solvency,
		Attestation: &common.Attestation{},
	}
	attestErrataTx := common.AttestErrataTx{
		ErrataTx:    &errataTx,
		Attestation: &common.Attestation{},
	}

	// Test GetAttestation for each type
	t.Run("GetAttestation returns correct attestation", func(t *testing.T) {
		assert.Equal(t, attestTx.Attestation, attestTx.GetAttestation())
		assert.Equal(t, attestNetworkFee.Attestation, attestNetworkFee.GetAttestation())
		assert.Equal(t, attestSolvency.Attestation, attestSolvency.GetAttestation())
		assert.Equal(t, attestErrataTx.Attestation, attestErrataTx.GetAttestation())
	})

	// Test GetSignablePayload for each type
	t.Run("GetSignablePayload returns marshaled data", func(t *testing.T) {
		// ObservedTx — payload covers the full wrapper (with KeysignMs zeroed)
		// plus the inbound bit, so a proposer cannot mutate FinaliseHeight,
		// BlockHeight, or QuorumTx.Inbound without invalidating signatures.
		txPayload, err := attestTx.GetSignablePayload()
		require.NoError(t, err)
		canonical := obsTx
		canonical.KeysignMs = 0
		expectedTxPayload, err := canonical.Marshal()
		require.NoError(t, err)
		// AttestTx.Inbound defaults to false here; trailing byte is 0.
		expectedTxPayload = append(expectedTxPayload, 0)
		assert.Equal(t, expectedTxPayload, txPayload)

		// NetworkFee
		feePayload, err := attestNetworkFee.GetSignablePayload()
		require.NoError(t, err)
		expectedFeePayload, err := networkFee.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedFeePayload, feePayload)

		// Solvency
		solvencyPayload, err := attestSolvency.GetSignablePayload()
		require.NoError(t, err)
		expectedSolvencyPayload, err := solvency.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedSolvencyPayload, solvencyPayload)

		// ErrataTx
		errataPayload, err := attestErrataTx.GetSignablePayload()
		require.NoError(t, err)
		expectedErrataPayload, err := errataTx.Marshal()
		require.NoError(t, err)
		assert.Equal(t, expectedErrataPayload, errataPayload)
	})
}

// TestProcessAttestation tests the generic attestation processing function
func TestProcessAttestation(t *testing.T) {
	// Override verifySignature for testing
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil // Always succeed in tests
	}

	t.Run("adds valid attestation to empty list", func(t *testing.T) {
		attestations := []attestationSentState{}

		// Create a simple attestation message
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}
		obsTx := common.ObservedTx{
			Tx: tx,
		}
		attestation := &common.Attestation{
			PubKey:    []byte("pubkey1"),
			Signature: []byte("sig1"),
		}
		attestTx := common.AttestTx{
			ObsTx:       obsTx,
			Attestation: attestation,
		}

		// Process the attestation
		err := ProcessAttestation(&attestations, &attestTx)
		require.NoError(t, err)

		// Verify the attestation was added
		assert.Len(t, attestations, 1)
		assert.Equal(t, attestation, attestations[0].attestation)
		assert.False(t, attestations[0].sent)
	})

	t.Run("ignores duplicate attestations (same signature)", func(t *testing.T) {
		attestations := []attestationSentState{
			{
				attestation: &common.Attestation{
					PubKey:    []byte("pubkey1"),
					Signature: []byte("sig1"),
				},
				sent: true,
			},
		}

		// Create an attestation with the same signature
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}
		obsTx := common.ObservedTx{
			Tx: tx,
		}
		attestation := &common.Attestation{
			PubKey:    []byte("pubkey1"),
			Signature: []byte("sig1"),
		}
		attestTx := common.AttestTx{
			ObsTx:       obsTx,
			Attestation: attestation,
		}

		// Process the attestation
		err := ProcessAttestation(&attestations, &attestTx)
		require.NoError(t, err)

		// Verify no new attestation was added
		assert.Len(t, attestations, 1)
	})

	t.Run("rejects attestation with same pubkey but different signature", func(t *testing.T) {
		attestations := []attestationSentState{
			{
				attestation: &common.Attestation{
					PubKey:    []byte("pubkey1"),
					Signature: []byte("sig1"),
				},
				sent: true,
			},
		}

		// Create an attestation with the same pubkey but different signature
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}
		obsTx := common.ObservedTx{
			Tx: tx,
		}
		attestation := &common.Attestation{
			PubKey:    []byte("pubkey1"),
			Signature: []byte("sig2"), // Different signature
		}
		attestTx := common.AttestTx{
			ObsTx:       obsTx,
			Attestation: attestation,
		}

		// Process the attestation
		err := ProcessAttestation(&attestations, &attestTx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "signature already present for")

		// Verify no new attestation was added
		assert.Len(t, attestations, 1)
	})
}

// TestSendAttestationsToThornode tests sending attestations to Thornode
func TestSendAttestationsToThornode(t *testing.T) {
	// Create test instances
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)

	// Create a test tx
	tx := &common.Tx{
		ID:    "send-tx",
		Chain: common.BTCChain,
	}
	obsTx := &common.ObservedTx{
		Tx: *tx,
	}

	// Create a new attestation state
	state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: obsTx})

	// Add two attestations
	state.attestations = []attestationSentState{
		{
			attestation: &common.Attestation{
				PubKey:    []byte("pubkey1"),
				Signature: []byte("sig1"),
			},
			sent: false,
		},
		{
			attestation: &common.Attestation{
				PubKey:    []byte("pubkey2"),
				Signature: []byte("sig2"),
			},
			sent: false,
		},
	}

	// Set up GRPC client to capture sent attestations
	var sentTx *common.QuorumTx
	grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		sentTx = quorumTx
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	// Send attestations to Thornode
	ag.sendObservedTxAttestationsToThornode(context.Background(), *obsTx, state, true, false, true)

	// Verify the tx was sent with the correct data
	require.NotNil(t, sentTx)
	assert.Equal(t, obsTx.Tx.ID, sentTx.ObsTx.Tx.ID)
	assert.Equal(t, obsTx.Tx.Chain, sentTx.ObsTx.Tx.Chain)
	assert.True(t, sentTx.Inbound)
	assert.Len(t, sentTx.Attestations, 2)

	// Verify all attestations are marked as sent
	for _, att := range state.attestations {
		assert.True(t, att.sent)
	}

	// Verify timestamps are updated
	assert.False(t, state.initialAttestationsSent.IsZero())
	assert.False(t, state.quorumAttestationsSent.IsZero())
	assert.False(t, state.lastAttestationsSent.IsZero())
}

// TestHandleObservedTxAttestationSeparatesFutureObservation ensures the same
// observed tx is tracked separately when AllowFutureObservation differs. The
// quorum path must not merge future outbound migration observations with normal
// observations for the same tx hash.
func TestHandleObservedTxAttestationSeparatesFutureObservation(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "same-observed-tx",
			Chain: common.BTCChain,
		},
	}

	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:                  obsTx,
		Inbound:                false,
		AllowFutureObservation: true,
		Attestation: &common.Attestation{
			PubKey:    []byte("pubkey-future"),
			Signature: []byte("sig-future"),
		},
	})
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:                  obsTx,
		Inbound:                false,
		AllowFutureObservation: false,
		Attestation: &common.Attestation{
			PubKey:    []byte("pubkey-normal"),
			Signature: []byte("sig-normal"),
		},
	})

	futureKey := txKey{
		Chain:                  obsTx.Tx.Chain,
		ID:                     obsTx.Tx.ID,
		UniqueHash:             obsTx.Tx.Hash(obsTx.BlockHeight),
		AllowFutureObservation: true,
		Finalized:              obsTx.IsFinal(),
		Inbound:                false,
	}
	normalKey := futureKey
	normalKey.AllowFutureObservation = false

	ag.mu.Lock()
	defer ag.mu.Unlock()

	require.Contains(t, ag.observedTxs, futureKey)
	require.Contains(t, ag.observedTxs, normalKey)
	assert.Equal(t, 1, ag.observedTxs[futureKey].AttestationCount())
	assert.Equal(t, 1, ag.observedTxs[normalKey].AttestationCount())
}

// TestHandleObservedTxAttestationFutureObservationUsesActiveValidatorQuorum
// locks in the denominator for future outbound observations. Bifrost must wait
// for the active-validator supermajority that THORNode accounting will accept,
// not the smaller keysign party that produced the outbound.
func TestHandleObservedTxAttestationFutureObservationUsesActiveValidatorQuorum(t *testing.T) {
	ag, _, _, grpcClient, bridge, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	valPrivs := make([]*secp256k1.PrivKey, 4)
	valPubKeys := make(common.PubKeys, 4)
	for i := range valPrivs {
		valPrivs[i] = secp256k1.GenPrivKey()
		pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, valPrivs[i].PubKey())
		require.NoError(t, err)
		valPubKeys[i] = common.PubKey(pub)
	}
	ag.setActiveValidators(valPubKeys)

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey, _ int64) (common.PubKeys, error) {
		t.Fatal("future observation quorum must not query keysign party")
		return nil, nil
	}

	var sent []*common.QuorumTx
	grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		sent = append(sent, quorumTx)
		return &ebifrost.SendQuorumTxResult{}, nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "future-active-validator-quorum",
			Chain: common.BTCChain,
		},
	}

	attest := func(i int) {
		ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
			ObsTx:                  obsTx,
			Inbound:                false,
			AllowFutureObservation: true,
			Attestation: &common.Attestation{
				PubKey:    valPrivs[i].PubKey().Bytes(),
				Signature: []byte(fmt.Sprintf("sig-%d", i)),
			},
		})
	}

	// With four active validators, two attestations are not enough. This would
	// have been enough if the future-observation denominator fell back to a
	// smaller keysign party.
	attest(0)
	attest(1)
	require.Empty(t, sent)

	// The third attestation is the active-validator supermajority for 4 nodes.
	attest(2)
	require.Len(t, sent, 1)
	assert.True(t, sent[0].AllowFutureObservation)
	assert.Equal(t, obsTx.Tx.ID, sent[0].ObsTx.Tx.ID)
	assert.Len(t, sent[0].Attestations, 3)
}

// TestAttestableItemImplementation tests the AttestableItem interface implementation for all types
func TestAttestableItemImplementation(t *testing.T) {
	t.Run("common.Tx implements AttestableItem", func(t *testing.T) {
		tx := common.Tx{
			ID:    "tx1",
			Chain: common.BTCChain,
		}

		// Marshal the tx
		data, err := tx.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("common.NetworkFee implements AttestableItem", func(t *testing.T) {
		networkFee := common.NetworkFee{
			Chain:  common.BTCChain,
			Height: 100,
		}

		// Marshal the network fee
		data, err := networkFee.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("common.Solvency implements AttestableItem", func(t *testing.T) {
		solvency := common.Solvency{
			Chain:  common.BTCChain,
			Height: 200,
		}

		// Marshal the solvency
		data, err := solvency.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})

	t.Run("common.ErrataTx implements AttestableItem", func(t *testing.T) {
		errataTx := common.ErrataTx{
			Chain: common.BTCChain,
			Id:    "tx1",
		}

		// Marshal the errata tx
		data, err := errataTx.Marshal()
		require.NoError(t, err)
		assert.NotEmpty(t, data)
	})
}

// TestRefactoredHandlersIntegration tests the refactored handlers integration
func TestRefactoredHandlersIntegration(t *testing.T) {
	ag, _, _, grpcClient, _, _ := setupTestGossip(t)

	// Override verifySignature for testing
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil // Always succeed in tests
	}

	t.Run("handles all attestation types through the same flow", func(t *testing.T) {
		// Create private keys for testing
		privKey1 := secp256k1.GenPrivKey()
		privKey2 := secp256k1.GenPrivKey()
		privKey3 := secp256k1.GenPrivKey()

		// Get bech32 public keys
		pub1, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey1.PubKey())
		require.NoError(t, err)
		pub2, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey2.PubKey())
		require.NoError(t, err)
		pub3, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, privKey3.PubKey())
		require.NoError(t, err)

		// Set active validators
		ag.setActiveValidators([]common.PubKey{common.PubKey(pub1), common.PubKey(pub2), common.PubKey(pub3)})

		// Track sent messages for each type
		var sentTxs []*common.QuorumTx
		var sentFees []*common.QuorumNetworkFee
		var sentSolvencies []*common.QuorumSolvency
		var sentErratas []*common.QuorumErrataTx

		// Setup GRPC client mocks
		grpcClient.sendQuorumTxFunc = func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
			sentTxs = append(sentTxs, quorumTx)
			return &ebifrost.SendQuorumTxResult{}, nil
		}

		grpcClient.sendQuorumNetworkFeeFunc = func(ctx context.Context, quorumNetworkFee *common.QuorumNetworkFee, opts ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
			sentFees = append(sentFees, quorumNetworkFee)
			return &ebifrost.SendQuorumNetworkFeeResult{}, nil
		}

		grpcClient.sendQuorumSolvencyFunc = func(ctx context.Context, quorumSolvency *common.QuorumSolvency, opts ...grpc.CallOption) (*ebifrost.SendQuorumSolvencyResult, error) {
			sentSolvencies = append(sentSolvencies, quorumSolvency)
			return &ebifrost.SendQuorumSolvencyResult{}, nil
		}

		grpcClient.sendQuorumErrataFunc = func(ctx context.Context, quorumErrataTx *common.QuorumErrataTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumErrataTxResult, error) {
			sentErratas = append(sentErratas, quorumErrataTx)
			return &ebifrost.SendQuorumErrataTxResult{}, nil
		}

		// Create test data for each type
		tx := common.ObservedTx{
			Tx: common.Tx{
				ID:    "tx-quorum",
				Chain: common.BTCChain,
			},
		}

		networkFee := common.NetworkFee{
			Chain:  common.BTCChain,
			Height: 100,
		}

		solvency := common.Solvency{
			Chain:  common.BTCChain,
			Height: 200,
		}

		errataTx := common.ErrataTx{
			Chain: common.BTCChain,
			Id:    "tx-errata",
		}

		// Process each type with multiple attestations to reach quorum
		for i, privKey := range []*secp256k1.PrivKey{privKey1, privKey2, privKey3} {
			// ObservedTx attestation
			ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
				ObsTx:   tx,
				Inbound: true,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-tx-%d", i)),
				},
			})

			// NetworkFee attestation
			ag.handleNetworkFeeAttestation(context.Background(), common.AttestNetworkFee{
				NetworkFee: &networkFee,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-fee-%d", i)),
				},
			})

			// Solvency attestation
			ag.handleSolvencyAttestation(context.Background(), common.AttestSolvency{
				Solvency: &solvency,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-solvency-%d", i)),
				},
			})

			// ErrataTx attestation
			ag.handleErrataAttestation(context.Background(), common.AttestErrataTx{
				ErrataTx: &errataTx,
				Attestation: &common.Attestation{
					PubKey:    privKey.PubKey().Bytes(),
					Signature: []byte(fmt.Sprintf("sig-errata-%d", i)),
				},
			})
		}

		// Wait for processing to complete
		time.Sleep(200 * time.Millisecond)

		// Verify all types were sent to thornode
		assert.Len(t, sentTxs, 2, "Should have sent txs to thornode")
		assert.Len(t, sentFees, 2, "Should have sent network fee to thornode")
		assert.Len(t, sentSolvencies, 2, "Should have sent solvency to thornode")
		assert.Len(t, sentErratas, 2, "Should have sent errata tx to thornode")

		// Verify correct data was sent for each type
		sentAttestations := 0
		for _, sentTx := range sentTxs {
			assert.Equal(t, tx.Tx.ID, sentTx.ObsTx.Tx.ID)
			assert.Equal(t, tx.Tx.Chain, sentTx.ObsTx.Tx.Chain)
			sentAttestations += len(sentTx.Attestations)
		}
		assert.Equal(t, sentAttestations, 3, "Should have sent 3 attestations in total")

		sentAttestations = 0
		for _, sentFee := range sentFees {
			assert.Equal(t, networkFee.Chain, sentFee.NetworkFee.Chain)
			assert.Equal(t, networkFee.Height, sentFee.NetworkFee.Height)
			sentAttestations += len(sentFee.Attestations)
		}
		assert.Equal(t, sentAttestations, 3, "Should have sent 3 attestations in total")

		sentAttestations = 0
		for _, sentSolvency := range sentSolvencies {
			assert.Equal(t, solvency.Chain, sentSolvency.Solvency.Chain)
			assert.Equal(t, solvency.Height, sentSolvency.Solvency.Height)
			sentAttestations += len(sentSolvency.Attestations)
		}
		assert.Equal(t, sentAttestations, 3, "Should have sent 3 attestations in total")

		sentAttestations = 0
		for _, sentErrata := range sentErratas {
			assert.Equal(t, errataTx.Chain, sentErrata.ErrataTx.Chain)
			assert.Equal(t, errataTx.Id, sentErrata.ErrataTx.Id)
			sentAttestations += len(sentErrata.Attestations)
		}

		// Verify all state data is stored correctly
		ag.mu.Lock()
		defer ag.mu.Unlock()

		// Check observed tx state
		k := txKey{
			Chain:      tx.Tx.Chain,
			ID:         tx.Tx.ID,
			UniqueHash: tx.Tx.Hash(0),
			Finalized:  tx.IsFinal(),
			Inbound:    true,
		}
		txState, exists := ag.observedTxs[k]
		assert.True(t, exists, "TX should exist in observed txs map")
		if exists {
			assert.Equal(t, 3, txState.AttestationCount(), "Should have 3 tx attestations")
		}

		// Check network fee state
		feeState, exists := ag.networkFees[networkFee]
		assert.True(t, exists, "Network fee should exist in networkFees map")
		if exists {
			assert.Equal(t, 3, feeState.AttestationCount(), "Should have 3 fee attestations")
		}

		// Check solvency state - this uses the hash as a key
		solvencyHash, err := solvency.Hash()
		require.NoError(t, err)
		solvencyState, exists := ag.solvencies[solvencyHash]
		assert.True(t, exists, "Solvency should exist in solvencies map")
		if exists {
			assert.Equal(t, 3, solvencyState.AttestationCount(), "Should have 3 solvency attestations")
		}

		// Check errata tx state
		errataState, exists := ag.errataTxs[errataTx]
		assert.True(t, exists, "Errata tx should exist in errataTxs map")
		if exists {
			assert.Equal(t, 3, errataState.AttestationCount(), "Should have 3 errata attestations")
		}
	})
}

// TestXMRNonSignerDoesNotBlindCoAttest verifies that a non-signing node does
// not sign a peer-supplied XMR outbound observation it cannot prove locally.
func TestXMRNonSignerDoesNotBlindCoAttest(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey, _ int64) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	var importedTx *common.ObservedTx
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		importedTx = &tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "xmr-outbound-tx-1",
			Chain: common.XMRChain,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
		SpentOutputRefs: []common.OutputRef{
			{TxHash: "aabb", OutputIndex: 0, KeyImage: "ki_aabb", SpendTxHash: "xmr-outbound-tx-1"},
			{TxHash: "ccdd", OutputIndex: 1, KeyImage: "ki_ccdd", SpendTxHash: "xmr-outbound-tx-1"},
		},
	}

	// Simulate a signing peer sending its attestation to the non-signer.
	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	ag.handleObservedTxAttestation(context.Background(), peerAttestation)

	time.Sleep(200 * time.Millisecond)

	k := observedTxKey(obsTx, false, false)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists, "attestation state should exist")

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 1, count, "should only retain the peer attestation")
	// Peer refs are no longer imported merely because they are XMR outbound
	// refs. The peer must pass the XMR source-vault/planned-outbound gates.
	require.Nil(t, importedTx, "unplanned peer spent refs should not be imported for sidecar verification")
}

// TestXMRBlindCoAttestationSkipsNonXMR verifies that non-XMR observations do
// not trigger any extra local attestation.
func TestXMRBlindCoAttestationSkipsNonXMR(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey, _ int64) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	// BTC outbound — should NOT trigger co-attestation.
	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "btc-outbound-tx-1",
			Chain: common.BTCChain,
			Coins: common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
	}

	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)

	k := observedTxKey(obsTx, false, false)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists)

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 1, count, "only the peer attestation — no co-attestation for non-XMR chains")
}

// TestXMRBlindCoAttestationIdempotent verifies that receiving the same XMR
// outbound peer attestation twice does not add a local attestation.
func TestXMRBlindCoAttestationIdempotent(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)

	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error {
		return nil
	}

	bridge.getKeysignPartyFunc = func(pubKey common.PubKey, _ int64) (common.PubKeys, error) {
		return common.PubKeys{"pubkey1", "pubkey2", "pubkey3"}, nil
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:    "xmr-outbound-dedup",
			Chain: common.XMRChain,
			Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
		},
		ObservedPubKey: "thorpub1addwnpepqtest",
		SpentOutputRefs: []common.OutputRef{
			{TxHash: "aabb", OutputIndex: 0, KeyImage: "ki_aabb", SpendTxHash: "xmr-outbound-dedup"},
		},
	}

	signerKey := secp256k1.GenPrivKey()
	signBz, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	signerSig, err := signerKey.Sign(signBz)
	require.NoError(t, err)

	peerAttestation := common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: signerSig,
		},
	}

	// Send the same attestation twice.
	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), peerAttestation)
	time.Sleep(200 * time.Millisecond)

	k := observedTxKey(obsTx, false, false)

	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()

	require.True(t, exists)

	state.mu.Lock()
	count := state.AttestationCount()
	state.mu.Unlock()

	assert.Equal(t, 1, count, "should only retain one peer attestation")
}

// TestXMRObservedTxAttestationsWithDifferentSpentRefsUseDifferentBuckets
// verifies that conflicting spent-ref sets do not collapse into one
// attestation state. Each exact observed-tx/ref-set variant must collect its
// own quorum so a malicious alternative ref set cannot overwrite the valid one.
func TestXMRObservedTxAttestationsWithDifferentSpentRefsUseDifferentBuckets(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)

	baseTx := common.Tx{
		ID:    "xmr-conflicting-refs",
		Chain: common.XMRChain,
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500000000))),
	}
	obsTxA := common.ObservedTx{
		Tx:             baseTx,
		ObservedPubKey: "thorpub1addwnpepqtest",
		SpentOutputRefs: []common.OutputRef{
			{TxHash: "aaaa", OutputIndex: 0, KeyImage: "ki_aaaa", SpendTxHash: "xmr-conflicting-refs"},
			{TxHash: "bbbb", OutputIndex: 1, KeyImage: "ki_bbbb", SpendTxHash: "xmr-conflicting-refs"},
		},
	}
	obsTxB := common.ObservedTx{
		Tx:             baseTx,
		ObservedPubKey: "thorpub1addwnpepqtest",
		SpentOutputRefs: []common.OutputRef{
			{TxHash: "cccc", OutputIndex: 0, KeyImage: "ki_cccc", SpendTxHash: "xmr-conflicting-refs"},
			{TxHash: "dddd", OutputIndex: 1, KeyImage: "ki_dddd", SpendTxHash: "xmr-conflicting-refs"},
		},
	}

	signerKey := secp256k1.GenPrivKey()
	signBzA, err := obsTxA.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	sigA, err := signerKey.Sign(signBzA)
	require.NoError(t, err)
	signBzB, err := obsTxB.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	sigB, err := signerKey.Sign(signBzB)
	require.NoError(t, err)

	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:   obsTxA,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: sigA,
		},
	})
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:   obsTxB,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signerKey.PubKey().Bytes(),
			Signature: sigB,
		},
	})

	ag.mu.Lock()
	defer ag.mu.Unlock()

	keyA := observedTxKey(obsTxA, false, false)
	keyB := observedTxKey(obsTxB, false, false)
	stateA, okA := ag.observedTxs[keyA]
	stateB, okB := ag.observedTxs[keyB]
	require.True(t, okA, "first spent-ref set should retain its own bucket")
	require.True(t, okB, "second spent-ref set should retain its own bucket")
	assert.NotEqual(t, keyA.UniqueHash, keyB.UniqueHash)
	assert.Equal(t, 1, stateA.AttestationCount())
	assert.Equal(t, 1, stateB.AttestationCount())
}
