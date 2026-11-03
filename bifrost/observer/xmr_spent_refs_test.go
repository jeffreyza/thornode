package observer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// observerTestHash gives tests deterministic 32-byte hex values that satisfy
// XMR spent-ref validation without depending on real Monero fixtures.
func observerTestHash(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// observerTestPubKey converts a generated secp256k1 key into the bech32/common
// pubkey format used by vault membership. The gossip attestation itself carries
// the raw secp bytes, so tests need both representations.
func observerTestPubKey(t *testing.T, priv *secp256k1.PrivKey) common.PubKey {
	t.Helper()

	pk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, priv.PubKey())
	require.NoError(t, err)
	pubKey, err := common.NewPubKey(pk)
	require.NoError(t, err)
	return pubKey
}

// observerTestOutputRef builds a self-consistent spent ref whose SpendTxHash
// matches the observed XMR tx. The sidecar import gate rejects mismatched spend
// hashes before it ever reaches sidecar verification.
func observerTestOutputRef(seed, spend string) common.OutputRef {
	return common.OutputRef{
		TxHash:      observerTestHash(seed + "-tx"),
		OutputIndex: 0,
		KeyImage:    observerTestHash(seed + "-ki"),
		SpendTxHash: spend,
	}
}

// observerTestXMRPlan returns a matching observed XMR outbound and planned
// THORChain txout. Individual tests mutate one field at a time to make sure the
// peer-import gate fails closed on mismatches.
func observerTestXMRPlan(t *testing.T) (common.ObservedTx, plannedXMROutbound) {
	t.Helper()

	txID := common.TxID(observerTestHash("xmr-peer-spent-ref-spend"))
	vaultPubKey := randomXMRObservedPubKey(t)
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)

	to := common.Address(common.XMRSeededDummyAddress('8', []byte("xmr-peer-spent-ref-dest")))
	memo := "MIGRATE:123"
	coins := common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500_000_000)))
	gas := common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(2))}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   to,
			Coins:       coins,
			Gas:         gas,
			Memo:        memo,
		},
		7,
		vaultPubKey,
		8,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{observerTestOutputRef("xmr-peer-spent-ref", txID.String())}
	obsTx.XmrMinedHeight = obsTx.BlockHeight

	planned := plannedXMROutbound{
		chain:            common.XMRChain,
		toAddress:        to,
		vaultPubKeyFrost: vaultPubKey,
		coins:            coins,
		memo:             memo,
		maxGas:           common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(5))},
	}

	return obsTx, planned
}

// TestXMRPlannedOutboundMatcherRejectsMismatches covers the THORChain-intent
// half of the peer import gate. The sidecar can verify Monero key images, but
// only Bifrost can verify that the observation matches a planned outbound.
func TestXMRPlannedOutboundMatcherRejectsMismatches(t *testing.T) {
	obsTx, planned := observerTestXMRPlan(t)

	t.Run("matches exact planned outbound", func(t *testing.T) {
		assert.True(t, xmrObservedTxMatchesPlanned(obsTx, planned))
	})

	t.Run("accepts migration amount with exact unused max gas sweep", func(t *testing.T) {
		mutated := obsTx
		plannedCoin := planned.coins.GetCoin(common.XMRAsset)
		observedGas := mutated.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
		plannedMaxGas := planned.maxGas.ToCoins().GetCoin(common.XMRAsset)
		unusedMaxGas := common.SafeSub(plannedMaxGas.Amount, observedGas.Amount)
		mutated.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, plannedCoin.Amount.Add(unusedMaxGas)))
		assert.True(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("accepts migration amount rounded down for non-aligned atomic fee", func(t *testing.T) {
		mutated := obsTx
		plannedCoin := planned.coins.GetCoin(common.XMRAsset)
		mutated.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
		// feeAtomic=12345 floors to observed gas 1, while the swept unused
		// MaxGas is floor((5*10000 - 12345)/10000) = 3.
		mutated.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, plannedCoin.Amount.Add(cosmos.NewUint(3))))
		assert.True(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects migration amount above unused max gas sweep", func(t *testing.T) {
		mutated := obsTx
		plannedCoin := planned.coins.GetCoin(common.XMRAsset)
		mutated.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, plannedCoin.Amount.Add(cosmos.NewUint(4))))
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects wrong destination", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.ToAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("wrong-xmr-dest")))
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects case-variant destination", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.ToAddress = common.Address(strings.ToUpper(mutated.Tx.ToAddress.String()))
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects wrong amount", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1)))
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects wrong memo", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.Memo = "MIGRATE:456"
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects wrong vault", func(t *testing.T) {
		mutated := obsTx
		mutated.ObservedPubKey = randomXMRObservedPubKey(t)
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects wrong source address", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.FromAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("wrong-xmr-source")))
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects case-variant source address", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.FromAddress = common.Address(strings.ToUpper(mutated.Tx.FromAddress.String()))
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects zero gas", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.ZeroUint())}
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("rejects gas above planned max", func(t *testing.T) {
		mutated := obsTx
		mutated.Tx.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(6))}
		assert.False(t, xmrObservedTxMatchesPlanned(mutated, planned))
	})

	t.Run("accepts missing planned max gas", func(t *testing.T) {
		mutatedPlan := planned
		mutatedPlan.maxGas = nil
		assert.True(t, xmrObservedTxMatchesPlanned(obsTx, mutatedPlan))
	})
}

// TestPeerXMRSpentRefsRequirePlannedOutboundAndSourceVaultQuorum verifies that
// pre-chain peer refs are importable only when the exact observation has a
// historical source-vault quorum and matches THORChain's plan.
//
// These tests intentionally assert that no local attestation is added: peer
// import only seeds the sidecar so the scanner can later observe independently.
func TestPeerXMRSpentRefsRequirePlannedOutboundAndSourceVaultQuorum(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	// The tests below focus on the authorization gates that run after
	// AddAttestation. Signature verification itself is covered elsewhere, so
	// make every synthetic attestation valid for this file.
	verifySignature = func(signBz, signature, attester []byte) error { return nil }

	t.Run("without planned outbound match", func(t *testing.T) {
		ag, _, _, _, bridge, _ := setupTestGossip(t)
		obsTx, _ := observerTestXMRPlan(t)
		attester := secp256k1.GenPrivKey()
		attesterPubKey := observerTestPubKey(t, attester)
		ag.setActiveValidators(common.PubKeys{attesterPubKey})
		// The attester is active and belongs to the source vault, but the
		// keysign lookup returns no planned txout. This proves source membership
		// alone is not enough to import peer refs.
		bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, attesterPubKey)
		bridge.getKeysignFunc = func(height int64, pk string) (tctypes.TxOut, error) {
			return tctypes.TxOut{Height: height}, nil
		}

		callback := assertNoSpentRefCallback(t, ag)
		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, attester))
		callback()
		assertObserverStateAttestationCount(t, ag, obsTx, 1)
	})

	t.Run("active non-source-vault member", func(t *testing.T) {
		ag, _, _, _, bridge, _ := setupTestGossip(t)
		obsTx, planned := observerTestXMRPlan(t)
		attester := secp256k1.GenPrivKey()
		attesterPubKey := observerTestPubKey(t, attester)
		otherMember := observerTestPubKey(t, secp256k1.GenPrivKey())
		ag.setActiveValidators(common.PubKeys{attesterPubKey})
		// The planned outbound exists, but this active peer is not in the source
		// vault membership. A newly churned-in validator cannot certify an
		// old-vault spend it never controlled.
		bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, otherMember)
		bridge.getKeysignFunc = observerTestKeysignFunc(planned)

		callback := assertNoSpentRefCallback(t, ag)
		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, attester))
		callback()
		assertObserverStateAttestationCount(t, ag, obsTx, 1)
	})

	t.Run("inactive source-vault quorum remains authoritative", func(t *testing.T) {
		ag, _, _, _, bridge, _ := setupTestGossip(t)
		obsTx, planned := observerTestXMRPlan(t)
		first := secp256k1.GenPrivKey()
		second := secp256k1.GenPrivKey()
		third := secp256k1.GenPrivKey()
		members := common.PubKeys{
			observerTestPubKey(t, first),
			observerTestPubKey(t, second),
			observerTestPubKey(t, third),
		}
		// Historical membership—not the post-churn active set—controls an old
		// vault's spend certificate. Two of three exact attestations are enough.
		ag.setActiveValidators(nil)
		bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, members...)
		bridge.getKeysignFunc = observerTestKeysignFunc(planned)

		applied := make(chan common.ObservedTx, 1)
		wireMemos := make(chan string, 1)
		ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
			applied <- request.Tx
			wireMemos <- request.WireMemo
			if onOutcome != nil {
				onOutcome(true)
			}
		})

		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, first))
		assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, second))
		select {
		case got := <-applied:
			assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
			assert.Equal(t, obsTx.Tx.Memo, got.Tx.Memo)
			assert.Equal(t, obsTx.Tx.Memo, <-wireMemos)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for historical source-vault quorum import")
		}
		assertObserverStateAttestationCount(t, ag, obsTx, 2)
	})

	t.Run("one source-vault member and local self do not bypass quorum", func(t *testing.T) {
		ag, _, _, _, bridge, _ := setupTestGossip(t)
		obsTx, planned := observerTestXMRPlan(t)
		self := secp256k1.GenPrivKey()
		second := secp256k1.GenPrivKey()
		third := secp256k1.GenPrivKey()
		members := common.PubKeys{
			observerTestPubKey(t, self),
			observerTestPubKey(t, second),
			observerTestPubKey(t, third),
		}
		ag.pubKey = self.PubKey().Bytes()
		ag.setActiveValidators(members)
		bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, members...)
		bridge.getKeysignFunc = observerTestKeysignFunc(planned)

		applied := make(chan common.ObservedTx, 1)
		ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
			tx := request.Tx
			applied <- tx
			if onOutcome != nil {
				onOutcome(true)
			}
		})

		// Even an attestation produced by this process is only one certificate
		// share and must leave sidecar state untouched.
		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, self))
		assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)

		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, second))

		select {
		case got := <-applied:
			assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
			assert.Equal(t, obsTx.SpentOutputRefs, got.SpentOutputRefs)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for peer spent-ref import")
		}
		assertObserverStateAttestationCount(t, ag, obsTx, 2)
	})
}

// TestPeerXMRSpentRefsUnauthorizedFirstDoesNotBlockAuthorizedImport protects
// the state-machine detail that spentRefsApplied is set only after a peer
// attestation passes authorization. A bad early peer should not be able to DoS a
// later valid source-vault peer import for the same observed tx/ref set.
func TestPeerXMRSpentRefsUnauthorizedFirstDoesNotBlockAuthorizedImport(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	// This test cares about the ordering of the XMR authorization gate, not
	// cryptographic signature mechanics. Use distinct fake signatures so both
	// peer attestations are accepted into the same state bucket.
	verifySignature = func(signBz, signature, attester []byte) error { return nil }

	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	unauthorized := secp256k1.GenPrivKey()
	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	members := common.PubKeys{observerTestPubKey(t, first), observerTestPubKey(t, second), observerTestPubKey(t, third)}
	ag.setActiveValidators(members)
	bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	// This first peer is neither active nor in the source vault. It should not
	// set spentRefsApplied, otherwise the authorized peer below would be blocked.
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, unauthorized))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, first))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, second))

	select {
	case got := <-applied:
		assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
		assert.Equal(t, obsTx.SpentOutputRefs, got.SpentOutputRefs)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for authorized peer spent-ref import")
	}
}

// TestPeerXMRSpentRefsFailedImportDoesNotBlockAuthorizedRetry protects the
// latch around sidecar import. A failed import must not consume the tx's
// spent-ref opportunity because the peer sidecar can be behind the scanner.
func TestPeerXMRSpentRefsFailedImportDoesNotBlockAuthorizedRetry(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error { return nil }

	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	firstPubKey := observerTestPubKey(t, first)
	secondPubKey := observerTestPubKey(t, second)
	thirdPubKey := observerTestPubKey(t, third)
	ag.setActiveValidators(common.PubKeys{firstPubKey, secondPubKey, thirdPubKey})
	bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, firstPubKey, secondPubKey, thirdPubKey)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	applied := make(chan common.ObservedTx, 2)
	results := make(chan bool, 2)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(<-results)
		} else {
			<-results
		}
	})

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, first))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, second))
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
		results <- false
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first spent-ref import attempt")
	}

	// Wait for the failed outcome to release both the conflict reservation and
	// the bounded import slot. spentRefsApplied starts false, so polling only
	// that latch can race the async callback and submit the retry too early.
	require.Eventually(t, func() bool {
		state, ok := observerTestConflictState(ag, obsTx)
		return ok && state == xmrSpentRefsImportFailed && len(ag.applySpentRefsSem) == 0
	}, time.Second, 10*time.Millisecond)
	require.False(t, observerTestSpentRefsApplied(t, ag, obsTx))

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, third))
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
		results <- true
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for second spent-ref import attempt")
	}

	require.Eventually(t, func() bool {
		return observerTestSpentRefsApplied(t, ag, obsTx)
	}, time.Second, 10*time.Millisecond)
}

// TestPeerXMRSpentRefsAuthorizedConflictBlocksSecondImport verifies that two
// authorized source-vault peers cannot race different spent-ref mappings for the
// same outbound into the local sidecar.
func TestPeerXMRSpentRefsAuthorizedConflictBlocksSecondImport(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error { return nil }

	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	conflicting := obsTx
	conflicting.SpentOutputRefs = []common.OutputRef{observerTestOutputRef("xmr-peer-conflict", obsTx.Tx.ID.String())}

	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	firstPubKey := observerTestPubKey(t, first)
	secondPubKey := observerTestPubKey(t, second)
	thirdPubKey := observerTestPubKey(t, third)
	ag.setActiveValidators(common.PubKeys{firstPubKey, secondPubKey, thirdPubKey})
	bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, firstPubKey, secondPubKey, thirdPubKey)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	applied := make(chan common.ObservedTx, 2)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, first))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, second))
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.SpentOutputRefs, got.SpentOutputRefs)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first spent-ref import")
	}

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(conflicting, second))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(conflicting, third))
	assertNoSpentRefImportDuring(t, applied, 150*time.Millisecond)
}

// TestXMRSpentRefGateFailsClosedForNilAttestation verifies the import gate does
// not treat malformed/empty gossip as source authority for sidecar mutation.
func TestXMRSpentRefGateFailsClosedForNilAttestation(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	obsTx, _ := observerTestXMRPlan(t)

	_, ok := ag.shouldApplyXMRSpentRefsFromAttestations(obsTx, nil)
	assert.False(t, ok)
}

// TestSingleVaultMemberCannotScheduleSpentRefVariantFlood is the direct
// regression for XMR-01/XMR-08: one historical member may gossip arbitrarily
// many real-looking ref variants, but none may reserve an authorized identity,
// consume sidecar worker capacity, or create retry work.
func TestSingleVaultMemberCannotScheduleSpentRefVariantFlood(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error { return nil }

	ag, _, _, _, bridge, _ := setupTestGossip(t)
	base, planned := observerTestXMRPlan(t)
	malicious := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	members := common.PubKeys{
		observerTestPubKey(t, malicious),
		observerTestPubKey(t, second),
		observerTestPubKey(t, third),
	}
	ag.setActiveValidators(members)
	bridge.getVaultFunc = observerTestVaultFunc(base.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	const variants = 128
	for i := 0; i < variants; i++ {
		variant := base
		variant.SpentOutputRefs = []common.OutputRef{
			observerTestOutputRef(fmt.Sprintf("single-member-variant-%d", i), base.Tx.ID.String()),
		}
		ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(variant, malicious))
	}

	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.authorizedXMRSpentRefsMu.Lock()
	require.Empty(t, ag.authorizedXMRSpentRefs)
	ag.authorizedXMRSpentRefsMu.Unlock()
	require.Empty(t, ag.applySpentRefsSem)
}

// TestSpentRefCertificateUsesTheExactlySignedStatePayload protects against a
// subtle txKey collision: memo is signed but is not part of Tx.Hash. A peer may
// wrap a signature over the existing state item in an envelope with a changed
// memo; sidecar authorization must use the signed state item, never the latest
// envelope fields.
func TestSpentRefCertificateUsesTheExactlySignedStatePayload(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	signed, planned := observerTestXMRPlan(t)
	mutatedEnvelope := signed
	mutatedEnvelope.Tx.Memo = "MIGRATE:999"
	require.Equal(t,
		observedTxKey(signed, false, false),
		observedTxKey(mutatedEnvelope, false, false),
		"fixture must exercise one attestation-state bucket",
	)

	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	members := common.PubKeys{
		observerTestPubKey(t, first),
		observerTestPubKey(t, second),
		observerTestPubKey(t, third),
	}
	ag.setActiveValidators(members)
	bridge.getVaultFunc = observerTestVaultFunc(signed.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	signable, err := signed.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	firstSig, err := first.Sign(signable)
	require.NoError(t, err)
	secondSig, err := second.Sign(signable)
	require.NoError(t, err)

	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:   signed,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    first.PubKey().Bytes(),
			Signature: firstSig,
		},
	})
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)

	// This envelope carries a different memo, but its valid signature is over
	// the original state item. It may complete that original certificate; it
	// must never authorize the mutated envelope.
	ag.handleObservedTxAttestation(context.Background(), common.AttestTx{
		ObsTx:   mutatedEnvelope,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    second.PubKey().Bytes(),
			Signature: secondSig,
		},
	})

	select {
	case got := <-applied:
		require.Equal(t, signed.Tx.Memo, got.Tx.Memo)
		require.NotEqual(t, mutatedEnvelope.Tx.Memo, got.Tx.Memo)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for exact-payload source-vault certificate")
	}
}

// TestXMRSpentRefImportCapacityIsAcquiredBeforeSpawn verifies overload is
// rejected at the boundary rather than accumulating goroutines blocked on a
// full semaphore. It also pins the committed reserve: pre-quorum saturation
// must not consume the fast path for an already-committed observation.
func TestXMRSpentRefImportCapacityIsAcquiredBeforeSpawn(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.applySpentRefsSem = make(chan struct{}, 1)
	ag.applyCommittedSpentRefsSem = make(chan struct{}, 1)

	first, _ := observerTestXMRPlan(t)
	second := first
	second.Tx.ID = common.TxID(observerTestHash("xmr-overload-second"))
	committed := first
	committed.Tx.ID = common.TxID(observerTestHash("xmr-overload-committed"))

	started := make(chan common.TxID, 2)
	release := make(chan struct{})
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		started <- tx.Tx.ID
		<-release
		if onOutcome != nil {
			onOutcome(true)
		}
	})
	ag.SetObserverApplyCommittedSpentRefs(func(_ context.Context, _ common.PubKey, txID common.TxID) (common.ObservedTx, bool) {
		started <- txID
		<-release
		return committed, true
	})

	ag.applySpentRefsAsync(spentOutputRefsImport{Tx: first}, nil)
	select {
	case got := <-started:
		require.Equal(t, first.Tx.ID, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first pre-quorum import")
	}

	rejected := make(chan bool, 1)
	ag.applySpentRefsAsync(spentOutputRefsImport{Tx: second}, func(applied bool) { rejected <- applied })
	select {
	case applied := <-rejected:
		require.False(t, applied)
	case <-time.After(time.Second):
		t.Fatal("over-capacity pre-quorum import did not fail promptly")
	}
	require.Len(t, ag.applySpentRefsSem, 1)

	ag.applyCommittedSpentRefsAsync(committed.ObservedPubKey, committed.Tx.ID, nil)
	select {
	case got := <-started:
		require.Equal(t, committed.Tx.ID, got)
	case <-time.After(time.Second):
		t.Fatal("committed import was starved by pre-quorum capacity")
	}

	close(release)
	require.Eventually(t, func() bool {
		return len(ag.applySpentRefsSem) == 0 && len(ag.applyCommittedSpentRefsSem) == 0
	}, time.Second, 10*time.Millisecond)
}

// TestXMRPlannedOutboundMatcherUsesInboundVoterActions covers the OUT/REFUND
// style path, where THORChain intent comes from the inbound observed-tx voter
// actions instead of the keysign-height txout used by MIGRATE/RAGNAROK.
func TestXMRPlannedOutboundMatcherUsesInboundVoterActions(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	inboundID := common.TxID(observerTestHash("xmr-peer-spent-ref-inbound"))
	obsTx.Tx.Memo = memo.NewOutboundMemo(inboundID).String()
	planned.memo = obsTx.Tx.Memo
	bridge.getObservedTxVoterFunc = observerTestObservedTxVoterFunc(inboundID, planned)

	wireMemo, ok := ag.plannedXMRWireMemo(obsTx)
	assert.True(t, ok)
	assert.Equal(t, obsTx.Tx.Memo, wireMemo)

	planned.wireMemo = planned.memo
	bridge.getObservedTxVoterFunc = observerTestObservedTxVoterFunc(inboundID, planned)
	wireMemo, ok = ag.plannedXMRWireMemo(obsTx)
	assert.True(t, ok)
	assert.Equal(t, obsTx.Tx.Memo, wireMemo)

	// Mutating the observed tx after the voter action is fixed should break the
	// match, proving this path compares the full planned outbound rather than
	// accepting the memo/inbound ID alone.
	obsTx.Tx.ToAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("wrong-voter-action-dest")))
	_, ok = ag.plannedXMRWireMemo(obsTx)
	assert.False(t, ok)
}

// TestXMRPlannedOutboundMatcherUsesKeysignHeightForRagnarok verifies the
// RAGNAROK path first checks the keysign height recorded in the memo.
// Without this path, pre-quorum peer spent-ref imports would be disabled for
// XMR ragnarok outbounds even when the observation exactly matches the plan.
func TestXMRPlannedOutboundMatcherUsesKeysignHeightForRagnarok(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	ragnarokMemo := memo.NewRagnarokMemo(123).String()
	obsTx.Tx.Memo = ragnarokMemo
	planned.memo = ragnarokMemo
	planned.wireMemo = ragnarokMemo
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	_, ok := ag.plannedXMRWireMemo(obsTx)
	assert.True(t, ok)

	// The same RAGNAROK memo must still fail closed when the planned txout no
	// longer matches the observed outbound.
	obsTx.Tx.ToAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("wrong-ragnarok-dest")))
	_, ok = ag.plannedXMRWireMemo(obsTx)
	assert.False(t, ok)
}

func TestXMRPlannedOutboundMatcherResolvesRagnarokRetry(t *testing.T) {
	const originalHeight, retryHeight = int64(123), int64(1040)
	type fixture struct {
		observation common.ObservedTx
		original    []plannedXMROutbound
		retries     map[int64][]plannedXMROutbound
		pending     []thorclient.PendingOutbound
		queueErr    error
		keysignErrs map[int64]error
	}
	tests := []struct {
		name          string
		change        func(*fixture)
		want          bool
		wantHeights   []int64
		wantQueueCall bool
	}{
		{
			name: "same vault retry at coalesced height", want: true,
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry discovery uses the semantic original memo",
			change: func(f *fixture) {
				f.pending = []thorclient.PendingOutbound{{
					Chain: "XMR", OriginalMemo: f.observation.Tx.Memo, Height: retryHeight,
				}}
			},
			want: true, wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry discovery preserves a forced memo over the original memo",
			change: func(f *fixture) {
				f.pending = []thorclient.PendingOutbound{{
					Chain: "XMR", Memo: f.observation.Tx.Memo, OriginalMemo: "RAGNAROK:999", Height: retryHeight,
				}}
			},
			want: true, wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry completed between queue and keysign reads",
			change: func(f *fixture) {
				f.retries[retryHeight][0].outHash = f.observation.Tx.ID
			},
			want: true, wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "completed retry absent from pending queue declines peer import",
			change: func(f *fixture) {
				f.retries[retryHeight][0].outHash = f.observation.Tx.ID
				f.pending = nil
			},
			wantHeights: []int64{originalHeight}, wantQueueCall: true,
		},
		{
			name: "retry is validated against updated payment fields",
			change: func(f *fixture) {
				coins := common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(123456)))
				f.retries[retryHeight][0].coins = coins
				f.observation.Tx.Coins = coins
			},
			want: true, wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "cancelled original without a pending retry",
			change: func(f *fixture) {
				f.pending = nil
			},
			wantHeights: []int64{originalHeight}, wantQueueCall: true,
		},
		{
			name: "queue hint without an authoritative plan",
			change: func(f *fixture) {
				f.retries = nil
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry cancelled after queue read",
			change: func(f *fixture) {
				f.retries[retryHeight][0].outHash = common.BlankTxID
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry completed with another transaction",
			change: func(f *fixture) {
				f.retries[retryHeight][0].outHash = common.TxID(observerTestHash("other-ragnarok-spend"))
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry destination mismatch",
			change: func(f *fixture) {
				f.retries[retryHeight][0].toAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("different-retry-destination")))
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry amount mismatch",
			change: func(f *fixture) {
				f.retries[retryHeight][0].coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1)))
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry gas budget mismatch",
			change: func(f *fixture) {
				f.retries[retryHeight][0].maxGas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry vault mismatch",
			change: func(f *fixture) {
				f.retries[retryHeight][0].vaultPubKeyFrost = common.EmptyPubKey
				f.retries[retryHeight][0].vaultPubKey = common.EmptyPubKey
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "retry memo mismatch",
			change: func(f *fixture) {
				f.retries[retryHeight][0].memo = memo.NewRagnarokMemo(originalHeight + 1).String()
				f.retries[retryHeight][0].wireMemo = f.retries[retryHeight][0].memo
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "queue lookup failure",
			change: func(f *fixture) {
				f.queueErr = fmt.Errorf("queue unavailable")
			},
			wantHeights: []int64{originalHeight}, wantQueueCall: true,
		},
		{
			name: "retry keysign lookup failure",
			change: func(f *fixture) {
				f.keysignErrs = map[int64]error{retryHeight: fmt.Errorf("keysign unavailable")}
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "unreadable retry before a valid candidate fails closed",
			change: func(f *fixture) {
				f.keysignErrs = map[int64]error{retryHeight - 40: fmt.Errorf("keysign unavailable")}
				f.pending = append(f.pending, thorclient.PendingOutbound{
					Chain: "XMR", Memo: f.observation.Tx.Memo, Height: retryHeight - 40,
				})
			},
			wantHeights: []int64{originalHeight, retryHeight - 40}, wantQueueCall: true,
		},
		{
			name: "unreadable retry after a valid candidate fails closed",
			change: func(f *fixture) {
				f.keysignErrs = map[int64]error{retryHeight + 40: fmt.Errorf("keysign unavailable")}
				f.pending = append(f.pending, thorclient.PendingOutbound{
					Chain: "XMR", Memo: f.observation.Tx.Memo, Height: retryHeight + 40,
				})
			},
			wantHeights: []int64{originalHeight, retryHeight, retryHeight + 40}, wantQueueCall: true,
		},
		{
			name: "exact original mismatch never falls back",
			change: func(f *fixture) {
				exact := f.original[0]
				exact.outHash = f.observation.Tx.ID
				exact.coins = nil
				f.original = append(f.original, exact)
			},
			wantHeights: []int64{originalHeight},
		},
		{
			name: "exact retry mismatch overrides another pending match",
			change: func(f *fixture) {
				exact := f.retries[retryHeight][0]
				exact.outHash = f.observation.Tx.ID
				exact.coins = nil
				f.retries[retryHeight+40] = []plannedXMROutbound{exact}
				f.pending = append(f.pending, thorclient.PendingOutbound{
					Chain: "XMR", Memo: f.observation.Tx.Memo, Height: retryHeight + 40,
				})
			},
			wantHeights: []int64{originalHeight, retryHeight, retryHeight + 40}, wantQueueCall: true,
		},
		{
			name: "conflicting retry wire memos fail closed",
			change: func(f *fixture) {
				other := f.retries[retryHeight][0]
				other.wireMemo = strings.ToLower(other.wireMemo)
				f.retries[retryHeight] = append(f.retries[retryHeight], other)
			},
			wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "exact retry hash disambiguates wire memo",
			change: func(f *fixture) {
				other := f.retries[retryHeight][0]
				other.wireMemo = strings.ToLower(other.wireMemo)
				f.retries[retryHeight][0].outHash = f.observation.Tx.ID
				f.retries[retryHeight] = append(f.retries[retryHeight], other)
			},
			want: true, wantHeights: []int64{originalHeight, retryHeight}, wantQueueCall: true,
		},
		{
			name: "too many retry heights are rejected before keysign lookups",
			change: func(f *fixture) {
				f.pending = nil
				for i := int64(0); i < 17; i++ {
					f.pending = append(f.pending, thorclient.PendingOutbound{
						Chain: "XMR", Memo: f.observation.Tx.Memo, Height: retryHeight + i,
					})
				}
			},
			wantHeights: []int64{originalHeight}, wantQueueCall: true,
		},
		{
			name: "original pending plan needs no retry lookup",
			change: func(f *fixture) {
				f.original[0].outHash = common.TxID("")
			},
			want: true, wantHeights: []int64{originalHeight},
		},
		{
			name: "completed original needs no retry lookup",
			change: func(f *fixture) {
				f.original[0].outHash = f.observation.Tx.ID
			},
			want: true, wantHeights: []int64{originalHeight},
		},
		{
			name: "different completed original does not authorize retry lookup",
			change: func(f *fixture) {
				f.original[0].outHash = common.TxID(observerTestHash("different-original-spend"))
			},
			wantHeights: []int64{originalHeight},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ag, _, _, _, bridge, _ := setupTestGossip(t)
			obsTx, planned := observerTestXMRPlan(t)
			obsTx.Tx.Memo = memo.NewRagnarokMemo(originalHeight).String()
			planned.memo, planned.wireMemo = obsTx.Tx.Memo, obsTx.Tx.Memo
			original := planned
			original.outHash = common.BlankTxID
			f := fixture{
				observation: obsTx,
				original:    []plannedXMROutbound{original},
				retries:     map[int64][]plannedXMROutbound{retryHeight: {planned}},
				pending: []thorclient.PendingOutbound{
					{Chain: "XMR", Memo: obsTx.Tx.Memo, Height: retryHeight},
					{Chain: "XMR", Memo: obsTx.Tx.Memo, Height: retryHeight},
					{Chain: "XMR", Memo: obsTx.Tx.Memo, Height: originalHeight},
					{Chain: "XMR", Memo: "RAGNAROK:999", Height: retryHeight + 1},
					{Chain: "BTC", Memo: obsTx.Tx.Memo, Height: retryHeight + 2},
				},
			}
			if test.change != nil {
				test.change(&f)
			}
			var heights []int64
			bridge.getKeysignFunc = func(height int64, pk string) (tctypes.TxOut, error) {
				require.Empty(t, pk)
				heights = append(heights, height)
				plans := f.original
				if height != originalHeight {
					if err := f.keysignErrs[height]; err != nil {
						return tctypes.TxOut{}, err
					}
					plans = f.retries[height]
				}
				out := tctypes.TxOut{Height: height}
				for _, plan := range plans {
					item, err := observerTestKeysignFunc(plan)(height, pk)
					require.NoError(t, err)
					out.TxArray = append(out.TxArray, item.TxArray...)
				}
				return out, nil
			}
			queueCalled := false
			bridge.getPendingOutboundsFunc = func(height int64) ([]thorclient.PendingOutbound, error) {
				require.Zero(t, height, "retry discovery must use the live queue")
				queueCalled = true
				return f.pending, f.queueErr
			}

			wireMemo, ok := ag.plannedXMRWireMemo(f.observation)
			assert.Equal(t, test.want, ok)
			if test.want {
				assert.Equal(t, obsTx.Tx.Memo, wireMemo)
			} else {
				assert.Empty(t, wireMemo)
			}
			assert.Equal(t, test.wantHeights, heights)
			assert.Equal(t, test.wantQueueCall, queueCalled)
		})
	}
}

// TestXMRPlannedOutboundMatcherAcceptsLegacyVaultPubKey keeps the matcher
// compatible with planned txouts that do not carry PubKeyFrost yet. XMR
// monovault records normally use PubKeyFrost, but the fallback avoids rejecting
// older/non-FROST txout shapes.
func TestXMRPlannedOutboundMatcherAcceptsLegacyVaultPubKey(t *testing.T) {
	obsTx, planned := observerTestXMRPlan(t)
	planned.vaultPubKey = planned.vaultPubKeyFrost
	planned.vaultPubKeyFrost = common.PubKey("")

	assert.True(t, xmrObservedTxMatchesPlanned(obsTx, planned))
}

// observerTestAttestTx builds the peer attestation envelope consumed by
// handleObservedTxAttestation. The fake signature is accepted by the overridden
// verifier in TestPeerXMRSpentRefsRequirePlannedOutboundAndSourceVaultMember.
func observerTestAttestTx(obsTx common.ObservedTx, attester *secp256k1.PrivKey) common.AttestTx {
	return common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    attester.PubKey().Bytes(),
			Signature: append([]byte("sig-"), attester.PubKey().Bytes()...),
		},
	}
}

// observerTestVaultFunc returns a source vault with explicit membership so the
// peer-import tests can distinguish "active validator" from "source-vault
// member". Both gates are required for peer refs.
func observerTestVaultFunc(vaultPubKey common.PubKey, members ...common.PubKey) func(string) (thortypes.Vault, error) {
	return func(pubkey string) (thortypes.Vault, error) {
		if pubkey != vaultPubKey.String() {
			return thortypes.Vault{}, nil
		}
		return thortypes.Vault{
			PubKey:      vaultPubKey,
			PubKeyFrost: vaultPubKey,
			Membership:  common.PubKeys(members).Strings(),
		}, nil
	}
}

// observerTestKeysignFunc returns a keysign block containing the planned XMR
// outbound. The matcher calls this with an empty pubkey to exercise the
// height-only /thorchain/keysign/{height} path used by newly churned-in nodes.
func observerTestKeysignFunc(planned plannedXMROutbound) func(int64, string) (tctypes.TxOut, error) {
	return func(height int64, pk string) (tctypes.TxOut, error) {
		return tctypes.TxOut{
			Height: height,
			TxArray: []tctypes.TxArrayItem{
				{
					Chain:            planned.chain,
					ToAddress:        planned.toAddress,
					VaultPubKey:      planned.vaultPubKey,
					VaultPubKeyFrost: planned.vaultPubKeyFrost,
					Coin:             planned.coins.GetCoin(common.XMRAsset),
					Memo:             planned.wireMemo,
					OriginalMemo:     planned.memo,
					MaxGas:           planned.maxGas,
					OutHash:          planned.outHash,
				},
			},
		}, nil
	}
}

// observerTestObservedTxVoterFunc returns a voter with one planned action. It
// mirrors THORChain's OUT/REFUND lookup path while keeping the test independent
// from a full keeper/voter fixture.
func observerTestObservedTxVoterFunc(inboundID common.TxID, planned plannedXMROutbound) func(string) (*thortypes.QueryObservedTxVoter, error) {
	return func(txID string) (*thortypes.QueryObservedTxVoter, error) {
		if !strings.EqualFold(txID, inboundID.String()) {
			return nil, nil
		}
		return &thortypes.QueryObservedTxVoter{
			TxID: inboundID,
			Actions: []thortypes.TxOutItem{
				{
					Chain:            planned.chain,
					ToAddress:        planned.toAddress,
					VaultPubKey:      planned.vaultPubKey,
					VaultPubKeyFrost: planned.vaultPubKeyFrost,
					Coin:             planned.coins.GetCoin(common.XMRAsset),
					Memo:             planned.wireMemo,
					OriginalMemo:     planned.memo,
					MaxGas:           planned.maxGas,
					OutHash:          planned.outHash,
				},
			},
		}, nil
	}
}

// assertNoSpentRefCallback installs a callback that fails the test if the
// pre-quorum peer import path fires. The short wait gives the async callback
// path time to run without making negative tests slow.
func assertNoSpentRefCallback(t *testing.T, ag *AttestationGossip) func() {
	t.Helper()

	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})
	return func() {
		t.Helper()
		select {
		case tx := <-applied:
			t.Fatalf("unexpected spent-ref import for %s", tx.Tx.ID)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// assertNoSpentRefImportDuring is used when the same callback channel later
// needs to receive a valid import. Unlike assertNoSpentRefCallback, it does not
// install a new callback and therefore preserves the positive assertion path.
func assertNoSpentRefImportDuring(t *testing.T, applied <-chan common.ObservedTx, timeout time.Duration) {
	t.Helper()

	select {
	case tx := <-applied:
		t.Fatalf("unexpected spent-ref import for %s", tx.Tx.ID)
	case <-time.After(timeout):
	}
}

// assertObserverStateAttestationCount checks the important safety invariant:
// accepting peer refs for sidecar import does not manufacture a local
// attestation or blind co-vote.
func assertObserverStateAttestationCount(t *testing.T, ag *AttestationGossip, obsTx common.ObservedTx, expected int) {
	t.Helper()

	k := observedTxKey(obsTx, false, false)
	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	ag.mu.Unlock()
	require.True(t, exists)

	state.mu.Lock()
	defer state.mu.Unlock()
	assert.Equal(t, expected, state.AttestationCount())
}

// observerTestSpentRefsApplied reads the live bucket instead of a captured
// pointer, matching the production rule that async outcomes must not mutate
// stale pooled state.
func observerTestSpentRefsApplied(t *testing.T, ag *AttestationGossip, obsTx common.ObservedTx) bool {
	t.Helper()

	k := observedTxKey(obsTx, false, false)
	ag.mu.Lock()
	state, exists := ag.observedTxs[k]
	if exists {
		state.mu.Lock()
	}
	ag.mu.Unlock()
	require.True(t, exists)
	defer state.mu.Unlock()
	return state.spentRefsApplied
}

// observerTestConflictState exposes only the import state for conflict-map
// assertions; the tests should not depend on timestamps or map entry internals.
func observerTestConflictState(ag *AttestationGossip, obsTx common.ObservedTx) (xmrSpentRefsImportState, bool) {
	ag.authorizedXMRSpentRefsMu.Lock()
	defer ag.authorizedXMRSpentRefsMu.Unlock()
	entry, ok := ag.authorizedXMRSpentRefs[xmrSpentRefsConflictKeyFor(obsTx)]
	if !ok {
		return 0, false
	}
	return entry.state, true
}

// TestPeerXMRSpentRefsTerminalFailureAllowsDifferingAuthorizedSet covers the
// conflict-map release: an authorized ref set whose sidecar import fails
// terminally holds no authority, so a differing authorized set for the same
// outbound must still be importable instead of being pinned out until restart.
func TestPeerXMRSpentRefsTerminalFailureAllowsDifferingAuthorizedSet(t *testing.T) {
	origVerifySignature := verifySignature
	defer func() { verifySignature = origVerifySignature }()
	verifySignature = func(signBz, signature, attester []byte) error { return nil }

	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	differing := obsTx
	differing.SpentOutputRefs = []common.OutputRef{observerTestOutputRef("xmr-peer-honest", obsTx.Tx.ID.String())}

	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	firstPubKey := observerTestPubKey(t, first)
	secondPubKey := observerTestPubKey(t, second)
	thirdPubKey := observerTestPubKey(t, third)
	ag.setActiveValidators(common.PubKeys{firstPubKey, secondPubKey, thirdPubKey})
	bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, firstPubKey, secondPubKey, thirdPubKey)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	applied := make(chan common.ObservedTx, 2)
	results := make(chan bool, 2)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(applied bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(<-results)
		} else {
			<-results
		}
	})

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, first))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(obsTx, second))
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.SpentOutputRefs, got.SpentOutputRefs)
		results <- false
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first spent-ref import attempt")
	}

	// The failed outcome lands asynchronously; the differing set is only
	// admissible once the conflict entry has been marked failed.
	require.Eventually(t, func() bool {
		state, ok := observerTestConflictState(ag, obsTx)
		return ok && state == xmrSpentRefsImportFailed
	}, time.Second, 10*time.Millisecond)

	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(differing, second))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	ag.handleObservedTxAttestation(context.Background(), observerTestAttestTx(differing, third))
	select {
	case got := <-applied:
		assert.Equal(t, differing.SpentOutputRefs, got.SpentOutputRefs)
		results <- true
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for differing authorized spent-ref import")
	}

	require.Eventually(t, func() bool {
		state, ok := observerTestConflictState(ag, differing)
		return ok && state == xmrSpentRefsImportConfirmed
	}, time.Second, 10*time.Millisecond)
}

// TestAuthorizedXMRSpentRefsLifecycle pins the conflict-entry state machine:
// pending blocks differing sets, terminal failure releases the entry, late
// outcomes for replaced identities are ignored, confirmation is sticky, and
// committed quorum refs overwrite everything.
func TestAuthorizedXMRSpentRefsLifecycle(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	obsTx, _ := observerTestXMRPlan(t)
	differing := obsTx
	differing.SpentOutputRefs = []common.OutputRef{observerTestOutputRef("xmr-lifecycle-b", obsTx.Tx.ID.String())}

	key := xmrSpentRefsConflictKeyFor(obsTx)
	identityA := xmrSpentRefsIdentity(obsTx.SpentOutputRefs)
	identityB := xmrSpentRefsIdentity(differing.SpentOutputRefs)
	require.NotEqual(t, identityA, identityB)

	// A pending set blocks both a differing set and duplicate scheduling of
	// itself; the client owns retries for the one admitted import.
	require.True(t, ag.rememberAuthorizedXMRSpentRefs(obsTx))
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(differing))
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(obsTx))

	// A terminal failure releases the entry for a differing authorized set.
	ag.resolveAuthorizedXMRSpentRefs(key, identityA, false)
	require.True(t, ag.rememberAuthorizedXMRSpentRefs(differing))

	// Cancellation cleanup for the old identity must leave the replacement pending.
	ag.releaseAuthorizedXMRSpentRefs(key, identityA)
	replacementState, exists := observerTestConflictState(ag, differing)
	require.True(t, exists)
	require.Equal(t, xmrSpentRefsImportPending, replacementState)

	// A late outcome for the replaced identity must not clobber the new entry.
	ag.resolveAuthorizedXMRSpentRefs(key, identityA, true)
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(obsTx))

	// Confirmation makes the current set sticky against differing sets.
	ag.resolveAuthorizedXMRSpentRefs(key, identityB, true)
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(obsTx))
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(differing))

	// A failed outcome must not demote a confirmed entry.
	ag.resolveAuthorizedXMRSpentRefs(key, identityB, false)
	state, ok := observerTestConflictState(ag, differing)
	require.True(t, ok)
	require.Equal(t, xmrSpentRefsImportConfirmed, state)

	// Thornode-committed quorum refs overwrite pre-quorum conflict memory.
	ag.recordCommittedXMRSpentRefs(key, identityA)
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(obsTx))
	require.False(t, ag.rememberAuthorizedXMRSpentRefs(differing))
}

// TestPruneAuthorizedXMRSpentRefsExpiresOldEntries bounds the conflict map:
// entries survive within the TTL and are dropped once it elapses.
func TestPruneAuthorizedXMRSpentRefsExpiresOldEntries(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	obsTx, _ := observerTestXMRPlan(t)
	require.True(t, ag.rememberAuthorizedXMRSpentRefs(obsTx))

	ttl := 2 * ag.config.NonQuorumTimeout
	if ttl <= 0 {
		ttl = 2 * defaultNonQuorumTimeout
	}

	ag.pruneAuthorizedXMRSpentRefs(time.Now().Add(ttl / 2))
	ag.authorizedXMRSpentRefsMu.Lock()
	surviving := len(ag.authorizedXMRSpentRefs)
	ag.authorizedXMRSpentRefsMu.Unlock()
	assert.Equal(t, 1, surviving, "entry within TTL must survive pruning")

	ag.pruneAuthorizedXMRSpentRefs(time.Now().Add(ttl + time.Minute))
	ag.authorizedXMRSpentRefsMu.Lock()
	remaining := len(ag.authorizedXMRSpentRefs)
	ag.authorizedXMRSpentRefsMu.Unlock()
	assert.Equal(t, 0, remaining, "entry past TTL must be pruned")
}

func TestSelectPlannedXMROutboundExactHashPrecedence(t *testing.T) {
	obsTx, planned := observerTestXMRPlan(t)
	planned.outHash = obsTx.Tx.ID

	t.Run("exact hash mismatch fails closed instead of falling back", func(t *testing.T) {
		mismatchedExact := planned
		mismatchedExact.toAddress = common.Address(common.XMRSeededDummyAddress('8', []byte("wrong-exact-hash-dest")))
		matchingUnhashed := planned
		matchingUnhashed.outHash = common.TxID("")

		for _, candidates := range [][]plannedXMROutbound{
			{mismatchedExact, matchingUnhashed},
			{matchingUnhashed, mismatchedExact},
		} {
			_, ok := selectPlannedXMROutbound(obsTx, candidates)
			assert.False(t, ok)
		}
	})

	t.Run("exact hash disambiguates matching plans", func(t *testing.T) {
		matchingUnhashed := planned
		matchingUnhashed.outHash = common.TxID("")
		matchingUnhashed.wireMemo = "unhashed-wire-memo"
		matchingExact := planned
		matchingExact.wireMemo = "exact-wire-memo"

		selected, ok := selectPlannedXMROutbound(obsTx, []plannedXMROutbound{
			matchingUnhashed,
			matchingExact,
		})
		require.True(t, ok)
		assert.Equal(t, matchingExact.wireMemo, selected.wireMemo)
	})

	t.Run("unhashed plan is used only when no exact hash exists", func(t *testing.T) {
		matchingUnhashed := planned
		matchingUnhashed.outHash = common.TxID("")
		matchingUnhashed.wireMemo = "unhashed-wire-memo"
		matchingWrongHash := planned
		matchingWrongHash.outHash = common.TxID(observerTestHash("other-outbound"))

		selected, ok := selectPlannedXMROutbound(obsTx, []plannedXMROutbound{
			matchingWrongHash,
			matchingUnhashed,
		})
		require.True(t, ok)
		assert.Equal(t, matchingUnhashed.wireMemo, selected.wireMemo)
	})
}
