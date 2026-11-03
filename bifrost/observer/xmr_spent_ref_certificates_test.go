package observer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type historicalXMRConfirmationChain struct {
	mockChainClient
	confirmations     int64
	ready             bool
	height            int64
	confirmationCount func(tctypes.TxIn) int64
}

func (c *historicalXMRConfirmationChain) GetConfirmationCount(tx tctypes.TxIn) int64 {
	if c.confirmationCount != nil {
		return c.confirmationCount(tx)
	}
	return c.confirmations
}

func (c *historicalXMRConfirmationChain) ConfirmationCountReady(tctypes.TxIn) bool {
	return c.ready
}

// TestObservedXMRPayloadLookupMatchesAllLiveKeys verifies that ordinary
// attestations are collected and marked across both values of the unsigned
// AllowFutureObservation routing flag.
func TestObservedXMRPayloadLookupMatchesAllLiveKeys(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	obsTx, _ := observerTestXMRPlan(t)
	first := secp256k1.GenPrivKey()
	second := secp256k1.GenPrivKey()

	normal := observerTestSignedHistoricalXMRCertificate(t, obsTx, first)
	future := observerTestSignedHistoricalXMRCertificate(t, obsTx, second)
	future.AllowFutureObservation = true

	ag.handleObservedTxAttestation(context.Background(), normal)
	ag.handleObservedTxAttestation(context.Background(), future)

	payload, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	payloadHash := sha256.Sum256(payload)
	attestations := ag.observedXMRPayloadAttestations(obsTx, payloadHash)
	require.Len(t, attestations, 2)
	attesters := make(map[string]struct{}, len(attestations))
	for _, attestation := range attestations {
		attesters[string(attestation.PubKey)] = struct{}{}
	}
	assert.Contains(t, attesters, string(first.PubKey().Bytes()))
	assert.Contains(t, attesters, string(second.PubKey().Bytes()))

	normalKey := observedTxKey(obsTx, false, false)
	futureKey := observedTxKey(obsTx, false, true)
	ag.markXMRSpentRefsAppliedByPayload(obsTx, payloadHash)
	assert.True(t, observedXMRPayloadApplied(t, ag, normalKey))
	assert.True(t, observedXMRPayloadApplied(t, ag, futureKey))
}

// TestObservedXMRPayloadLookupRejectsSameKeyDifferentPayload verifies that a
// lossy txKey match cannot contribute signatures or mark a state unless the
// complete signed payload also matches the certificate.
func TestObservedXMRPayloadLookupRejectsSameKeyDifferentPayload(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	obsTx, _ := observerTestXMRPlan(t)
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, obsTx, secp256k1.GenPrivKey()))

	oldPayload, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	oldPayloadHash := sha256.Sum256(oldPayload)
	k := observedTxKey(obsTx, false, false)

	// Memo is signed but omitted from txKey, so the key lookup alone is not
	// sufficient to bind the certificate to the live observation state.
	replacement := obsTx
	replacement.Tx.Memo += ":replacement"
	require.Equal(t, k, observedTxKey(replacement, false, false))
	replacementPayload, err := replacement.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	replacementPayloadHash := sha256.Sum256(replacementPayload)
	require.NotEqual(t, oldPayloadHash, replacementPayloadHash)

	assert.Empty(t, ag.observedXMRPayloadAttestations(replacement, replacementPayloadHash))
	ag.markXMRSpentRefsAppliedByPayload(replacement, replacementPayloadHash)
	assert.False(t, observedXMRPayloadApplied(t, ag, k))
	require.Len(t, ag.observedXMRPayloadAttestations(obsTx, oldPayloadHash), 1)
}

// TestObservedXMRPayloadLookupDoesNotScanUnrelatedStates verifies that lookup
// touches only the certificate's two candidate buckets, rather than blocking
// on an unrelated observation as the previous global scan did.
func TestObservedXMRPayloadLookupDoesNotScanUnrelatedStates(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	obsTx, _ := observerTestXMRPlan(t)
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, obsTx, secp256k1.GenPrivKey()))
	payload, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	payloadHash := sha256.Sum256(payload)

	decoy := obsTx
	decoy.Tx.ID = common.TxID(observerTestHash("xmr-payload-lookup-decoy"))
	decoy.SpentOutputRefs = []common.OutputRef{
		observerTestOutputRef("xmr-payload-lookup-decoy-ref", decoy.Tx.ID.String()),
	}
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, decoy, secp256k1.GenPrivKey()))

	ag.mu.Lock()
	decoyState := ag.observedTxs[observedTxKey(decoy, false, false)]
	require.NotNil(t, decoyState)
	decoyState.mu.Lock()
	ag.mu.Unlock()

	lookupDone := make(chan int, 1)
	go func() {
		lookupDone <- len(ag.observedXMRPayloadAttestations(obsTx, payloadHash))
	}()
	select {
	case count := <-lookupDone:
		require.Equal(t, 1, count)
	case <-time.After(time.Second):
		decoyState.mu.Unlock()
		t.Fatal("payload lookup blocked on an unrelated observation state")
	}
	decoyState.mu.Unlock()
}

func observedXMRPayloadApplied(t *testing.T, ag *AttestationGossip, k txKey) bool {
	t.Helper()
	ag.mu.Lock()
	state, ok := ag.observedTxs[k]
	if ok {
		state.mu.Lock()
	}
	ag.mu.Unlock()
	require.True(t, ok)
	defer state.mu.Unlock()
	return state.spentRefsApplied
}

func TestHistoricalXMRSpentRefCertificateInactiveQuorumIsCertificateOnly(t *testing.T) {
	for _, status := range []thortypes.VaultStatus{thortypes.VaultStatus_ActiveVault, thortypes.VaultStatus_RetiringVault, thortypes.VaultStatus_InactiveVault} {
		t.Run(status.String(), func(t *testing.T) {
			ag, _, keys, _, bridge, _ := setupTestGossip(t)
			obsTx, planned := observerTestXMRPlan(t)
			second := secp256k1.GenPrivKey()
			third := secp256k1.GenPrivKey()
			members := common.PubKeys{
				observerTestPubKey(t, keys.privKey),
				observerTestPubKey(t, second),
				observerTestPubKey(t, third),
			}
			ag.setActiveValidators(nil)
			vaultLookup := observerTestRetiringVaultFunc(obsTx.ObservedPubKey, members...)
			bridge.getVaultFunc = func(key string) (thortypes.Vault, error) {
				vault, err := vaultLookup(key)
				vault.Status = status
				return vault, err
			}
			bridge.getKeysignFunc = observerTestKeysignFunc(planned)
			if status == thortypes.VaultStatus_ActiveVault {
				inboundID := common.TxID(observerTestHash("stale-active-outbound"))
				obsTx.Tx.Memo = "OUT:" + inboundID.String()
				planned.memo, planned.wireMemo = obsTx.Tx.Memo, obsTx.Tx.Memo
				bridge.getObservedTxVoterFunc = observerTestObservedTxVoterFunc(inboundID, planned)
			}

			applied := make(chan common.ObservedTx, 1)
			wireMemos := make(chan string, 1)
			ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
				if onOutcome != nil {
					onOutcome(true)
				}
				applied <- request.Tx
				wireMemos <- request.WireMemo
			})

			// A departed producer transfers ownership durably before any local import.
			producer := wireHistoricalXMRCertificateProducer(t, ag, bridge)
			require.NoError(t, ag.AttestObservedTx(context.Background(), &obsTx, false, false))
			require.Empty(t, ag.historicalXMRSpentRefCertificates)
			producer.retryPendingXMRCertificates(context.Background())
			assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
			ag.mu.Lock()
			observedCount := len(ag.observedTxs)
			certificateCount := len(ag.historicalXMRSpentRefCertificates)
			ag.mu.Unlock()
			assert.Zero(t, observedCount)
			require.Equal(t, 1, certificateCount)

			// A second historical member reaches the two-of-three source-vault quorum.
			secondCertificate := observerTestSignedHistoricalXMRCertificate(t, obsTx, second)
			secondPeer := observerTestPeerID(t, second)
			require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(secondCertificate, secondPeer))
			select {
			case got := <-applied:
				assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
				assert.Equal(t, obsTx.SpentOutputRefs, got.SpentOutputRefs)
				assert.Equal(t, obsTx.Tx.Memo, got.Tx.Memo)
				assert.Equal(t, obsTx.Tx.Memo, <-wireMemos)
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for historical certificate quorum import")
			}

			ag.mu.Lock()
			assert.Empty(t, ag.observedTxs, "inactive certificate shares must never become observation votes")
			for _, bucket := range ag.historicalXMRSpentRefCertificates {
				assert.True(t, bucket.applied)
				assert.Len(t, bucket.attestations, 2)
			}
			ag.mu.Unlock()
		})
	}
}

// Both recovery entry points must follow the actual retry schedule before
// importing a Ragnarok spend whose original queue entry was superseded.
func TestRescheduledRagnarokSpentRefsRequireSourceQuorum(t *testing.T) {
	for _, historical := range []bool{false, true} {
		name := map[bool]string{false: "ordinary gossip", true: "inactive historical certificates"}[historical]
		t.Run(name, func(t *testing.T) {
			ag, _, keys, _, bridge, _ := setupTestGossip(t)
			obsTx, planned := observerTestXMRPlan(t)
			obsTx.Tx.Memo = "RAGNAROK:123"
			planned.memo, planned.wireMemo = obsTx.Tx.Memo, obsTx.Tx.Memo
			original := planned
			original.outHash = common.BlankTxID

			first, second, third := secp256k1.GenPrivKey(), secp256k1.GenPrivKey(), secp256k1.GenPrivKey()
			members := common.PubKeys{
				observerTestPubKey(t, first), observerTestPubKey(t, second), observerTestPubKey(t, third),
			}
			localMember := observerTestPubKey(t, keys.privKey)
			if historical {
				ag.setActiveValidators(common.PubKeys{localMember})
			} else {
				// Two source members authorize recovery, but do not establish
				// the current four-validator observation quorum.
				ag.setActiveValidators(append(common.PubKeys{localMember}, members...))
			}
			bridge.getVaultFunc = observerTestRetiringVaultFunc(obsTx.ObservedPubKey, members...)

			var originalQueries, retryQueries, queueQueries atomic.Int32
			bridge.getKeysignFunc = func(height int64, pubKey string) (tctypes.TxOut, error) {
				if pubKey != "" {
					return tctypes.TxOut{}, fmt.Errorf("expected public keysign lookup, got %q", pubKey)
				}
				switch height {
				case 123:
					originalQueries.Add(1)
					return observerTestKeysignFunc(original)(height, pubKey)
				case 1040:
					retryQueries.Add(1)
					return observerTestKeysignFunc(planned)(height, pubKey)
				default:
					return tctypes.TxOut{}, fmt.Errorf("unexpected retry height %d", height)
				}
			}
			bridge.getPendingOutboundsFunc = func(height int64) ([]thorclient.PendingOutbound, error) {
				if height != 0 {
					return nil, fmt.Errorf("expected current pending queue, got height %d", height)
				}
				queueQueries.Add(1)
				// Coalescing need not preserve the original height's phase.
				return []thorclient.PendingOutbound{{Chain: "XMR", Memo: obsTx.Tx.Memo, Height: 1040}}, nil
			}

			applied := make(chan spentOutputRefsImport, 2)
			ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
				onOutcome(true)
				applied <- request
			})
			submit := func(signer *secp256k1.PrivKey) {
				attestation := observerTestSignedHistoricalXMRCertificate(t, obsTx, signer)
				if historical {
					require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(attestation, observerTestPeerID(t, signer)))
				} else {
					ag.handleObservedTxAttestation(context.Background(), attestation)
				}
			}

			submit(first)
			select {
			case <-applied:
				t.Fatal("one source member authorized the retry import")
			case <-time.After(100 * time.Millisecond):
			}
			submit(second)
			select {
			case got := <-applied:
				wantPayload, err := obsTx.GetSignablePayloadWithInbound(false)
				require.NoError(t, err)
				gotPayload, err := got.Tx.GetSignablePayloadWithInbound(false)
				require.NoError(t, err)
				require.Equal(t, wantPayload, gotPayload)
				require.Equal(t, obsTx.SpentOutputRefs, got.Tx.SpentOutputRefs)
				require.Equal(t, obsTx.Tx.Memo, got.WireMemo)
				require.False(t, got.ChainCommitted)
			case <-time.After(time.Second):
				t.Fatal("source quorum did not import the rescheduled Ragnarok spend")
			}
			require.Positive(t, originalQueries.Load())
			require.Positive(t, retryQueries.Load())
			require.Positive(t, queueQueries.Load())
			require.Eventually(t, func() bool { return len(ag.applySpentRefsSem) == 0 }, time.Second, time.Millisecond)
			require.Empty(t, applied, "the quorum must authorize exactly one import")

			if historical {
				ag.mu.Lock()
				observedCount := len(ag.observedTxs)
				ag.mu.Unlock()
				require.Zero(t, observedCount, "historical shares must not fabricate observation votes")
				require.True(t, ag.historicalXMRSpentRefCertificateApplied(obsTx))
			} else {
				payload, err := obsTx.GetSignablePayloadWithInbound(false)
				require.NoError(t, err)
				attestations := ag.observedXMRPayloadAttestations(obsTx, sha256.Sum256(payload))
				require.Len(t, attestations, 2)
				for _, attestation := range attestations {
					require.NotEqual(t, keys.privKey.PubKey().Bytes(), attestation.PubKey,
						"import must not create a local observation")
				}
				require.True(t, observedXMRPayloadApplied(t, ag, observedTxKey(obsTx, false, false)))
			}
		})
	}
}

func TestHistoricalXMRCertificateTransfersScannerReceiptToDurableOutbox(t *testing.T) {
	ag, _, keys, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	// The scanner enters this path through common.NewTxID, which canonicalizes
	// 64-byte hashes. Keep the directly constructed remote certificate fixture
	// on that same representation so both signatures address one payload bucket.
	canonicalTxID, canonicalErr := common.NewTxID(obsTx.Tx.ID.String())
	require.NoError(t, canonicalErr)
	obsTx.Tx.ID = canonicalTxID
	second := secp256k1.GenPrivKey()
	third := secp256k1.GenPrivKey()
	members := common.PubKeys{
		observerTestPubKey(t, keys.privKey),
		observerTestPubKey(t, second),
		observerTestPubKey(t, third),
	}
	ag.setActiveValidators(nil)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(obsTx.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	ag.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, onOutcome func(bool)) {
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	newTxIn := func() (tctypes.TxIn, *bool, *error) {
		spentRefs := make([]*common.OutputRef, 0, len(obsTx.SpentOutputRefs))
		for i := range obsTx.SpentOutputRefs {
			ref := obsTx.SpentOutputRefs[i]
			spentRefs = append(spentRefs, &ref)
		}
		stored := false
		var rejected error
		return tctypes.TxIn{
			Chain:                common.XMRChain,
			Filtered:             true,
			ConfirmationRequired: obsTx.FinaliseHeight - obsTx.BlockHeight,
			TxArray: []*tctypes.TxInItem{{
				BlockHeight:         obsTx.BlockHeight,
				Tx:                  obsTx.Tx.ID.String(),
				Memo:                obsTx.Tx.Memo,
				Sender:              obsTx.Tx.FromAddress.String(),
				To:                  obsTx.Tx.ToAddress.String(),
				Coins:               obsTx.Tx.Coins,
				Gas:                 obsTx.Tx.Gas,
				ObservedVaultPubKey: obsTx.ObservedPubKey,
				SpentOutputRefs:     spentRefs,
				OnObservedTxStored:  func() { stored = true },
				OnObservedTxRejected: func(err error) {
					rejected = err
				},
			}},
		}, &stored, &rejected
	}

	observer := &Observer{
		logger: ag.logger,
		chains: map[common.Chain]chainclients.ChainClient{
			common.XMRChain: &historicalXMRConfirmationChain{
				confirmations: obsTx.FinaliseHeight - obsTx.BlockHeight,
			},
		},
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
		attestationGossip:   ag,
	}
	store, path := setupTestDB(t)
	defer cleanupTestDB(t, store, path)
	observer.storage = store
	ag.certificateObserver = observer
	first, stored, rejected := newTxIn()
	store.beforeCertificateWrite = func() error { return fmt.Errorf("disk unavailable") }
	require.Error(t, observer.attestHistoricalXMRSpentRefCertificates(first))
	require.False(t, *stored)
	require.Error(t, *rejected)
	store.beforeCertificateWrite = nil
	first, stored, rejected = newTxIn()
	require.NoError(t, observer.attestHistoricalXMRSpentRefCertificates(first))
	require.True(t, *stored)
	require.NoError(t, *rejected)
	records, _, err := store.pendingXMRCertificates(time.Now().Add(time.Second), 16)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.False(t, ag.historicalXMRSpentRefCertificateApplied(obsTx), "local application is not the handoff boundary")
	replay, stored, rejected := newTxIn()
	require.NoError(t, observer.attestHistoricalXMRSpentRefCertificates(replay))
	require.True(t, *stored)
	require.NoError(t, *rejected)
}

func TestHistoricalXMRCertificateMixedBatchHandlesEachItemIndependently(t *testing.T) {
	ag, _, keys, _, bridge, _ := setupTestGossip(t)
	memberObs, memberPlan := observerTestXMRPlan(t)
	canonicalID, err := common.NewTxID(memberObs.Tx.ID.String())
	require.NoError(t, err)
	memberObs.Tx.ID = canonicalID
	memberObs.SpentOutputRefs[0].SpendTxHash = canonicalID.String()

	nonmemberObs := memberObs
	nonmemberID, err := common.NewTxID(observerTestHash("xmr-mixed-nonmember"))
	require.NoError(t, err)
	nonmemberObs.Tx.ID = nonmemberID
	nonmemberObs.ObservedPubKey = randomXMRObservedPubKey(t)
	nonmemberAddress, err := nonmemberObs.ObservedPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	nonmemberObs.Tx.FromAddress = nonmemberAddress
	nonmemberObs.SpentOutputRefs = []common.OutputRef{
		observerTestOutputRef("xmr-mixed-nonmember-ref", nonmemberObs.Tx.ID.String()),
	}
	nonmemberPlan := memberPlan
	nonmemberPlan.vaultPubKeyFrost = nonmemberObs.ObservedPubKey

	localMember := observerTestPubKey(t, keys.privKey)
	otherMember := observerTestPubKey(t, secp256k1.GenPrivKey())
	memberVault := observerTestRetiringVaultFunc(memberObs.ObservedPubKey, localMember)
	nonmemberVault := observerTestRetiringVaultFunc(nonmemberObs.ObservedPubKey, otherMember)
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		switch pubKey {
		case memberObs.ObservedPubKey.String():
			return memberVault(pubKey)
		case nonmemberObs.ObservedPubKey.String():
			return nonmemberVault(pubKey)
		default:
			return thortypes.Vault{}, nil
		}
	}
	bridge.getKeysignFunc = func(height int64, _ string) (tctypes.TxOut, error) {
		toItem := func(planned plannedXMROutbound) tctypes.TxArrayItem {
			return tctypes.TxArrayItem{
				Chain:            planned.chain,
				ToAddress:        planned.toAddress,
				VaultPubKey:      planned.vaultPubKey,
				VaultPubKeyFrost: planned.vaultPubKeyFrost,
				Coin:             planned.coins.GetCoin(common.XMRAsset),
				Memo:             planned.memo,
				MaxGas:           planned.maxGas,
			}
		}
		return tctypes.TxOut{Height: height, TxArray: []tctypes.TxArrayItem{
			toItem(memberPlan),
			toItem(nonmemberPlan),
		}}, nil
	}
	ag.setActiveValidators(nil)
	ag.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, onOutcome func(bool)) {
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	toItem := func(obs common.ObservedTx, stored *bool, rejected *error) *tctypes.TxInItem {
		spentRefs := make([]*common.OutputRef, 0, len(obs.SpentOutputRefs))
		for i := range obs.SpentOutputRefs {
			ref := obs.SpentOutputRefs[i]
			spentRefs = append(spentRefs, &ref)
		}
		return &tctypes.TxInItem{
			BlockHeight:         obs.BlockHeight,
			Tx:                  obs.Tx.ID.String(),
			Memo:                obs.Tx.Memo,
			Sender:              obs.Tx.FromAddress.String(),
			To:                  obs.Tx.ToAddress.String(),
			Coins:               obs.Tx.Coins,
			Gas:                 obs.Tx.Gas,
			ObservedVaultPubKey: obs.ObservedPubKey,
			SpentOutputRefs:     spentRefs,
			OnObservedTxStored:  func() { *stored = true },
			OnObservedTxRejected: func(err error) {
				*rejected = err
			},
		}
	}
	memberStored, nonmemberStored := false, false
	var memberRejected, nonmemberRejected error
	txIn := tctypes.TxIn{
		Chain:                common.XMRChain,
		Filtered:             true,
		ConfirmationRequired: memberObs.FinaliseHeight - memberObs.BlockHeight,
		TxArray: []*tctypes.TxInItem{
			toItem(memberObs, &memberStored, &memberRejected),
			toItem(nonmemberObs, &nonmemberStored, &nonmemberRejected),
		},
	}
	observer := wireHistoricalXMRCertificateProducer(t, ag, bridge)
	observer.chains[common.XMRChain] = &historicalXMRConfirmationChain{
		confirmations: memberObs.FinaliseHeight - memberObs.BlockHeight,
	}

	require.NoError(t, observer.attestHistoricalXMRSpentRefCertificates(txIn))
	require.True(t, memberStored)
	require.NoError(t, memberRejected)
	require.False(t, nonmemberStored)
	require.ErrorIs(t, nonmemberRejected, tctypes.ErrObservedTxNotApplicable)
}

func TestHistoricalXMRSpentRefCertificateRejectsUnauthorizedVariants(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*AttestationGossip, *MockThorchainBridge, common.ObservedTx, plannedXMROutbound, *secp256k1.PrivKey) (common.AttestTx, peer.ID)
		contains  string
	}{
		{
			name: "inbound classification",
			configure: func(_ *AttestationGossip, _ *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				certificate := observerTestSignedHistoricalXMRCertificate(t, obs, signer)
				certificate.Inbound = true
				return certificate, observerTestPeerID(t, signer)
			},
			contains: "restricted to XMR outbounds",
		},
		{
			name: "missing spent refs",
			configure: func(_ *AttestationGossip, _ *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				obs.SpentOutputRefs = nil
				return observerTestSignedHistoricalXMRCertificate(t, obs, signer), observerTestPeerID(t, signer)
			},
			contains: "no spent refs",
		},
		{
			name: "signed payload mutation",
			configure: func(_ *AttestationGossip, _ *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				certificate := observerTestSignedHistoricalXMRCertificate(t, obs, signer)
				certificate.ObsTx.Tx.Memo = "MIGRATE:999"
				return certificate, observerTestPeerID(t, signer)
			},
			contains: "signature",
		},
		{
			name: "transport signer mismatch",
			configure: func(_ *AttestationGossip, _ *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				return observerTestSignedHistoricalXMRCertificate(t, obs, signer), observerTestPeerID(t, secp256k1.GenPrivKey())
			},
			contains: "does not match transport peer",
		},
		{
			name: "active transport",
			configure: func(ag *AttestationGossip, _ *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				ag.setActiveValidators(common.PubKeys{observerTestPubKey(t, signer)})
				return observerTestSignedHistoricalXMRCertificate(t, obs, signer), observerTestPeerID(t, signer)
			},
			contains: "ordinary observation gossip",
		},
		{
			name: "nonmember",
			configure: func(_ *AttestationGossip, bridge *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				bridge.getVaultFunc = observerTestRetiringVaultFunc(obs.ObservedPubKey, observerTestPubKey(t, secp256k1.GenPrivKey()))
				return observerTestSignedHistoricalXMRCertificate(t, obs, signer), observerTestPeerID(t, signer)
			},
			contains: "not a source-vault member",
		},
		{
			name: "missing source vault",
			configure: func(_ *AttestationGossip, bridge *MockThorchainBridge, obs common.ObservedTx, _ plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				bridge.getVaultFunc = func(string) (thortypes.Vault, error) { return thortypes.Vault{}, nil }
				return observerTestSignedHistoricalXMRCertificate(t, obs, signer), observerTestPeerID(t, signer)
			},
			contains: "source vault is empty",
		},
		{
			name: "unplanned outbound",
			configure: func(_ *AttestationGossip, bridge *MockThorchainBridge, obs common.ObservedTx, planned plannedXMROutbound, signer *secp256k1.PrivKey) (common.AttestTx, peer.ID) {
				planned.memo = "MIGRATE:999"
				bridge.getKeysignFunc = observerTestKeysignFunc(planned)
				return observerTestSignedHistoricalXMRCertificate(t, obs, signer), observerTestPeerID(t, signer)
			},
			contains: "does not match a planned outbound",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ag, _, _, _, bridge, _ := setupTestGossip(t)
			obsTx, planned := observerTestXMRPlan(t)
			signer := secp256k1.GenPrivKey()
			otherMembers := common.PubKeys{
				observerTestPubKey(t, signer),
				observerTestPubKey(t, secp256k1.GenPrivKey()),
				observerTestPubKey(t, secp256k1.GenPrivKey()),
			}
			ag.setActiveValidators(nil)
			bridge.getVaultFunc = observerTestRetiringVaultFunc(obsTx.ObservedPubKey, otherMembers...)
			bridge.getKeysignFunc = observerTestKeysignFunc(planned)
			certificate, transportPeer := tc.configure(ag, bridge, obsTx, planned, signer)

			err := ag.acceptHistoricalXMRSpentRefCertificate(certificate, transportPeer)
			require.ErrorContains(t, err, tc.contains)
			if tc.name == "nonmember" {
				require.ErrorIs(t, err, tctypes.ErrObservedTxNotApplicable)
			}
			ag.mu.Lock()
			assert.Empty(t, ag.historicalXMRSpentRefCertificates)
			assert.Empty(t, ag.observedTxs)
			ag.mu.Unlock()
		})
	}
}

func TestHistoricalXMRCertificatePerSignerLimitAppliesToExistingBucket(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	signerPubKey := observerTestPubKey(t, signer)
	ag.setActiveValidators(nil)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(obsTx.ObservedPubKey, signerPubKey)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	certificate := observerTestSignedHistoricalXMRCertificate(t, obsTx, signer)
	payload, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	payloadHash := sha256.Sum256(payload)

	ag.mu.Lock()
	ag.historicalXMRSpentRefCertificates = map[[32]byte]*historicalXMRSpentRefCertificateBucket{
		payloadHash: {
			observedTx:   obsTx,
			attestations: make(map[string]*common.Attestation),
			updatedAt:    time.Now(),
		},
	}
	ag.historicalXMRCertificateSignerUse = map[string]int{
		string(signer.PubKey().Bytes()): maxHistoricalXMRCertificateBucketsPerSigner,
	}
	ag.mu.Unlock()

	err = ag.acceptHistoricalXMRSpentRefCertificate(certificate, observerTestPeerID(t, signer))
	require.ErrorContains(t, err, "signer capacity exhausted")
	ag.mu.Lock()
	assert.Empty(t, ag.historicalXMRSpentRefCertificates[payloadHash].attestations)
	ag.mu.Unlock()
}

// TestHistoricalXMRCertificatePerSignerLimitDoesNotAllocateNewBuckets verifies
// rejected over-quota payloads cannot consume global certificate bucket capacity.
func TestHistoricalXMRCertificatePerSignerLimitDoesNotAllocateNewBuckets(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	signerKey := string(signer.PubKey().Bytes())
	ag.setActiveValidators(nil)
	vaultCalls := 0
	keysignCalls := 0
	loadVault := observerTestRetiringVaultFunc(obsTx.ObservedPubKey, observerTestPubKey(t, signer))
	loadKeysign := observerTestKeysignFunc(planned)
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls++
		return loadVault(pubKey)
	}
	bridge.getKeysignFunc = func(height int64, pubKey string) (tctypes.TxOut, error) {
		keysignCalls++
		return loadKeysign(height, pubKey)
	}

	ag.mu.Lock()
	ag.historicalXMRCertificateSignerUse = map[string]int{
		signerKey: maxHistoricalXMRCertificateBucketsPerSigner,
	}
	ag.mu.Unlock()

	for _, seed := range []string{"over-quota-a", "over-quota-b"} {
		candidate := obsTx
		candidate.SpentOutputRefs = []common.OutputRef{
			observerTestOutputRef(seed, candidate.Tx.ID.String()),
		}
		certificate := observerTestSignedHistoricalXMRCertificate(t, candidate, signer)
		err := ag.acceptHistoricalXMRSpentRefCertificate(certificate, observerTestPeerID(t, signer))
		require.ErrorContains(t, err, "signer capacity exhausted")
	}

	ag.mu.Lock()
	assert.Empty(t, ag.historicalXMRSpentRefCertificates)
	assert.Equal(t, maxHistoricalXMRCertificateBucketsPerSigner, ag.historicalXMRCertificateSignerUse[signerKey])
	ag.mu.Unlock()
	assert.Zero(t, vaultCalls, "over-quota certificates must be rejected before GetVault")
	assert.Zero(t, keysignCalls, "over-quota certificates must be rejected before planned-outbound lookup")
}

// TestHistoricalXMRCertificateDuplicateAtSignerLimitRemainsAdmissible verifies
// retained certificates remain retryable after their signer reaches its quota.
func TestHistoricalXMRCertificateDuplicateAtSignerLimitRemainsAdmissible(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	signerKey := string(signer.PubKey().Bytes())
	ag.setActiveValidators(nil)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(obsTx.ObservedPubKey, observerTestPubKey(t, signer))
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	certificate := observerTestSignedHistoricalXMRCertificate(t, obsTx, signer)
	transportPeer := observerTestPeerID(t, signer)

	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(certificate, transportPeer))
	ag.mu.Lock()
	ag.historicalXMRCertificateSignerUse[signerKey] = maxHistoricalXMRCertificateBucketsPerSigner
	ag.mu.Unlock()

	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(certificate, transportPeer))
	ag.mu.Lock()
	bucketCount := len(ag.historicalXMRSpentRefCertificates)
	attestationCount := 0
	for _, bucket := range ag.historicalXMRSpentRefCertificates {
		attestationCount = len(bucket.attestations)
	}
	signerUse := ag.historicalXMRCertificateSignerUse[signerKey]
	ag.mu.Unlock()
	require.Equal(t, 1, bucketCount)
	assert.Equal(t, 1, attestationCount)
	assert.Equal(t, maxHistoricalXMRCertificateBucketsPerSigner, signerUse)
}

// TestHistoricalXMRCertificateCachesValidationRPCs verifies duplicate frames
// reuse the short-lived vault and planned-outbound results instead of issuing
// another pair of bridge queries.
func TestHistoricalXMRCertificateCachesValidationRPCs(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	ag.setActiveValidators(nil)
	vaultCalls := 0
	keysignCalls := 0
	loadVault := observerTestRetiringVaultFunc(obsTx.ObservedPubKey, observerTestPubKey(t, signer))
	loadKeysign := observerTestKeysignFunc(planned)
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls++
		return loadVault(pubKey)
	}
	bridge.getKeysignFunc = func(height int64, pubKey string) (tctypes.TxOut, error) {
		keysignCalls++
		return loadKeysign(height, pubKey)
	}
	certificate := observerTestSignedHistoricalXMRCertificate(t, obsTx, signer)
	transportPeer := observerTestPeerID(t, signer)

	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(certificate, transportPeer))
	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(certificate, transportPeer))
	assert.Equal(t, 1, vaultCalls)
	assert.Equal(t, 1, keysignCalls)
}

// TestHistoricalXMRCertificatePeerRateLimitPrecedesRPC verifies an exhausted
// peer admission budget rejects the next frame before either bridge query.
func TestHistoricalXMRCertificatePeerRateLimitPrecedesRPC(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	transportPeer := observerTestPeerID(t, signer)
	ag.setActiveValidators(nil)
	vaultCalls := 0
	keysignCalls := 0
	loadVault := observerTestRetiringVaultFunc(obsTx.ObservedPubKey, observerTestPubKey(t, signer))
	loadKeysign := observerTestKeysignFunc(planned)
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls++
		return loadVault(pubKey)
	}
	bridge.getKeysignFunc = func(height int64, pubKey string) (tctypes.TxOut, error) {
		keysignCalls++
		return loadKeysign(height, pubKey)
	}

	sem, err := ag.peerMgr.acquire(transportPeer)
	require.NoError(t, err)
	sem.mu.Lock()
	sem.historicalXMRCertificateLimiter = rate.NewLimiter(0, 0)
	sem.mu.Unlock()
	ag.peerMgr.release(sem)

	certificate := observerTestSignedHistoricalXMRCertificate(t, obsTx, signer)
	encoded, err := certificate.Marshal()
	require.NoError(t, err)
	ag.handleStreamXMRSpentRefCertificate(NewMockStreamWithData(encoded, transportPeer))
	assert.Zero(t, vaultCalls)
	assert.Zero(t, keysignCalls)
}

// TestHistoricalXMRCertificateRPCConcurrencyRejectsBeforeBridge verifies a
// saturated process-wide RPC boundary rejects rather than blocking another
// stream handler or starting additional Thornode work.
func TestHistoricalXMRCertificateRPCConcurrencyRejectsBeforeBridge(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	ag.setActiveValidators(nil)
	vaultCalls := 0
	keysignCalls := 0
	loadVault := observerTestRetiringVaultFunc(obsTx.ObservedPubKey, observerTestPubKey(t, signer))
	loadKeysign := observerTestKeysignFunc(planned)
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls++
		return loadVault(pubKey)
	}
	bridge.getKeysignFunc = func(height int64, pubKey string) (tctypes.TxOut, error) {
		keysignCalls++
		return loadKeysign(height, pubKey)
	}

	ag.mu.Lock()
	sem := ag.historicalXMRCertificateRPCControlLocked().rpcSem
	for range cap(sem) {
		sem <- struct{}{}
	}
	ag.mu.Unlock()
	defer func() {
		for range cap(sem) {
			<-sem
		}
	}()

	err := ag.acceptHistoricalXMRSpentRefCertificate(
		observerTestSignedHistoricalXMRCertificate(t, obsTx, signer),
		observerTestPeerID(t, signer),
	)
	require.ErrorContains(t, err, "RPC capacity exhausted")
	assert.Zero(t, vaultCalls)
	assert.Zero(t, keysignCalls)
}

func TestHistoricalXMRCertificateFrameRejectsOversizeBeforeAllocation(t *testing.T) {
	var frame bytes.Buffer
	require.NoError(t, binary.Write(&frame, binary.LittleEndian, uint32(maxHistoricalXMRCertificateBytes+1)))
	stream := NewMockStream(bytes.NewReader(frame.Bytes()), io.Discard, "")

	data, err := readBoundedCertificateFrame(stream, maxHistoricalXMRCertificateBytes, time.Second)
	require.ErrorContains(t, err, "exceeds limit")
	assert.Nil(t, data)
}

func TestXMRSpentRefImportRetriesAfterBoundedSaturation(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.applySpentRefsSem = make(chan struct{}, 1)

	first, _ := observerTestXMRPlan(t)
	second := first
	second.Tx.ID = common.TxID(observerTestHash("xmr-saturated-second"))

	started := make(chan common.TxID, 2)
	releases := map[common.TxID]chan struct{}{
		first.Tx.ID:  make(chan struct{}),
		second.Tx.ID: make(chan struct{}),
	}
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		tx := request.Tx
		started <- tx.Tx.ID
		<-releases[tx.Tx.ID]
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	ag.applySpentRefsAsync(spentOutputRefsImport{Tx: first}, nil)
	require.Equal(t, first.Tx.ID, <-started)
	rejected := make(chan bool, 1)
	ag.applySpentRefsAsync(spentOutputRefsImport{Tx: second}, func(applied bool) { rejected <- applied })
	require.False(t, <-rejected, "saturation must resolve the callback without leaking a waiter")

	close(releases[first.Tx.ID])
	require.Eventually(t, func() bool { return len(ag.applySpentRefsSem) == 0 }, time.Second, 10*time.Millisecond)

	// The same exact work can be resubmitted after the overload callback. This
	// is the path used by a duplicate attestation/reconciliation tick.
	retried := make(chan bool, 1)
	ag.applySpentRefsAsync(spentOutputRefsImport{Tx: second}, func(applied bool) { retried <- applied })
	require.Equal(t, second.Tx.ID, <-started)
	close(releases[second.Tx.ID])
	require.True(t, <-retried)
	require.Eventually(t, func() bool { return len(ag.applySpentRefsSem) == 0 }, time.Second, 10*time.Millisecond)
}

func TestObservedXMRSpentRefsReconcileAfterCapacityRejection(t *testing.T) {
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
	ag.setActiveValidators(members)
	bridge.getVaultFunc = observerTestVaultFunc(obsTx.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	// Occupy the only executor slot, matching an exhausted admission boundary.
	// The outcome releases the conflict latch.
	ag.applySpentRefsSem = make(chan struct{}, 1)
	ag.applySpentRefsSem <- struct{}{}
	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, obsTx, first))
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, obsTx, second))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)

	<-ag.applySpentRefsSem
	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for periodic spent-ref reconciliation")
	}
}

// TestCommittedXMRSpentRefsLatchExactPayload verifies committed catch-up marks
// only the live state whose signed payload the sidecar accepted. A conflicting
// live ref set remains unlatched but is filtered by the confirmed conflict
// entry before reconciliation can issue Thornode RPCs.
func TestCommittedXMRSpentRefsLatchExactPayload(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	exact, _ := observerTestXMRPlan(t)
	differing := exact
	differing.SpentOutputRefs = []common.OutputRef{
		observerTestOutputRef("committed-differing", exact.Tx.ID.String()),
	}

	signer := secp256k1.GenPrivKey()
	exactState := addPendingObservedXMRSpentRefsTestState(ag, exact, signer, time.Now().Add(-2*time.Minute))
	differingState := addPendingObservedXMRSpentRefsTestState(ag, differing, signer, time.Now().Add(-time.Minute))
	exactPayload, err := exact.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	exactPayloadHash := sha256.Sum256(exactPayload)
	ag.mu.Lock()
	if ag.historicalXMRSpentRefCertificates == nil {
		ag.historicalXMRSpentRefCertificates = make(map[[sha256.Size]byte]*historicalXMRSpentRefCertificateBucket)
	}
	ag.historicalXMRSpentRefCertificates[exactPayloadHash] = &historicalXMRSpentRefCertificateBucket{
		observedTx:   exact,
		attestations: make(map[string]*common.Attestation),
	}
	ag.mu.Unlock()

	ag.SetObserverApplyCommittedSpentRefs(func(context.Context, common.PubKey, common.TxID) (common.ObservedTx, bool) {
		return exact, true
	})
	committed := common.QuorumTx{
		ObsTx:        exact,
		Inbound:      false,
		Attestations: []*common.Attestation{{PubKey: signer.PubKey().Bytes(), Signature: []byte("committed")}},
	}
	payload, err := committed.Marshal()
	require.NoError(t, err)
	ag.handleQuorumTxCommitted(&ebifrost.EventNotification{Payload: payload})

	require.Eventually(t, func() bool {
		exactState.mu.Lock()
		defer exactState.mu.Unlock()
		return exactState.spentRefsApplied
	}, time.Second, 10*time.Millisecond)
	differingState.mu.Lock()
	assert.False(t, differingState.spentRefsApplied)
	differingState.mu.Unlock()
	require.Eventually(t, func() bool {
		state, ok := observerTestConflictState(ag, exact)
		return ok && state == xmrSpentRefsImportConfirmed
	}, time.Second, 10*time.Millisecond)
	// The committed callback marks the historical bucket after the live state
	// and conflict entry, so wait for that final write rather than racing it.
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return ag.historicalXMRSpentRefCertificates[exactPayloadHash].applied
	}, time.Second, 10*time.Millisecond)

	var vaultCalls atomic.Int32
	bridge.getVaultFunc = func(string) (thortypes.Vault, error) {
		vaultCalls.Add(1)
		return thortypes.Vault{}, nil
	}
	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	assert.Zero(t, vaultCalls.Load(), "confirmed conflict must stop differing state before RPC")
}

// TestObservedXMRSpentRefsReconcileBacksOff verifies a sub-quorum observation
// is retried exponentially by the sweep while a genuinely new attestation can
// still trigger an immediate authorization attempt.
func TestObservedXMRSpentRefsReconcileBacksOff(t *testing.T) {
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
	loadVault := observerTestVaultFunc(obsTx.ObservedPubKey, members...)
	vaultCalls := 0
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls++
		return loadVault(pubKey)
	}
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)

	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, obsTx, first))
	require.Equal(t, 1, vaultCalls)

	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	require.Equal(t, 2, vaultCalls)
	k := observedTxKey(obsTx, false, false)
	ag.mu.Lock()
	state := ag.observedTxs[k]
	require.NotNil(t, state)
	state.mu.Lock()
	ag.mu.Unlock()
	initialDelay := ag.xmrSpentRefsInitialRetryDelay()
	assert.Equal(t, initialDelay*2, state.spentRefsRetryDelay)
	require.True(t, time.Now().Before(state.spentRefsNextRetryAt))
	state.mu.Unlock()

	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	assert.Equal(t, 2, vaultCalls, "not-yet-due retry must not issue another RPC")

	state.mu.Lock()
	state.spentRefsNextRetryAt = time.Time{}
	state.mu.Unlock()
	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	require.Equal(t, 3, vaultCalls)
	state.mu.Lock()
	assert.Equal(t, initialDelay*4, state.spentRefsRetryDelay)
	state.mu.Unlock()

	// The normal gossip path does not consult the sweep schedule. A new source
	// vault signature can therefore complete quorum immediately.
	ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, obsTx, second))
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
	case <-time.After(time.Second):
		t.Fatal("fresh attestation did not bypass reconciliation backoff")
	}
}

// TestObservedXMRSpentRefsReconcileCapsPostFilterWork verifies blocked and
// not-yet-due states do not consume the per-pass budget, and that the oldest
// eligible states are claimed first.
func TestObservedXMRSpentRefsReconcileCapsPostFilterWork(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	ag.SetObserverApplySpentOutputRefs(func(spentOutputRefsImport, func(bool)) {})
	base, _ := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	members := common.PubKeys{
		observerTestPubKey(t, signer),
		observerTestPubKey(t, secp256k1.GenPrivKey()),
		observerTestPubKey(t, secp256k1.GenPrivKey()),
	}
	loadVault := observerTestVaultFunc(base.ObservedPubKey, members...)
	var vaultCalls atomic.Int32
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls.Add(1)
		return loadVault(pubKey)
	}

	makeObservation := func(seed string) common.ObservedTx {
		obsTx := base
		obsTx.Tx.ID = common.TxID(observerTestHash(seed + "-spend"))
		obsTx.SpentOutputRefs = []common.OutputRef{observerTestOutputRef(seed, obsTx.Tx.ID.String())}
		return obsTx
	}
	now := time.Now()
	for i, importState := range []xmrSpentRefsImportState{xmrSpentRefsImportPending, xmrSpentRefsImportConfirmed} {
		obsTx := makeObservation(fmt.Sprintf("blocked-%d", i))
		addPendingObservedXMRSpentRefsTestState(ag, obsTx, signer, now.Add(-3*time.Hour+time.Duration(i)*time.Second))
		ag.authorizedXMRSpentRefsMu.Lock()
		ag.authorizedXMRSpentRefs[xmrSpentRefsConflictKeyFor(obsTx)] = &xmrSpentRefsConflictEntry{
			identity: xmrSpentRefsIdentity(obsTx.SpentOutputRefs),
			state:    importState,
		}
		ag.authorizedXMRSpentRefsMu.Unlock()
	}

	notDue := makeObservation("not-due")
	notDueState := addPendingObservedXMRSpentRefsTestState(ag, notDue, signer, now.Add(-2*time.Hour))
	notDueState.mu.Lock()
	notDueState.spentRefsNextRetryAt = now.Add(time.Hour)
	notDueState.mu.Unlock()

	eligible := make([]*AttestationState[*attestableObservedTx], 0, maxPendingObservedXMRSpentRefsPerReconcile+1)
	for i := 0; i < maxPendingObservedXMRSpentRefsPerReconcile+1; i++ {
		obsTx := makeObservation(fmt.Sprintf("eligible-%02d", i))
		eligible = append(eligible, addPendingObservedXMRSpentRefsTestState(
			ag,
			obsTx,
			signer,
			now.Add(-time.Hour+time.Duration(i)*time.Second),
		))
	}

	ag.reconcilePendingObservedXMRSpentRefs(context.Background())
	assert.EqualValues(t, maxPendingObservedXMRSpentRefsPerReconcile, vaultCalls.Load())
	for i, state := range eligible {
		state.mu.Lock()
		claimed := !state.spentRefsNextRetryAt.IsZero()
		state.mu.Unlock()
		if i < maxPendingObservedXMRSpentRefsPerReconcile {
			assert.True(t, claimed, "eligible state %d should be inside the oldest-due cap", i)
		} else {
			assert.False(t, claimed, "newest eligible state must remain for a later pass")
		}
	}
}

// TestObservedXMRSpentRefsReconcileSingleFlight verifies a slow bridge call
// cannot accumulate another reconciliation goroutine on the next trigger.
func TestObservedXMRSpentRefsReconcileSingleFlight(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	ag.SetObserverApplySpentOutputRefs(func(spentOutputRefsImport, func(bool)) {})
	base, _ := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	members := common.PubKeys{
		observerTestPubKey(t, signer),
		observerTestPubKey(t, secp256k1.GenPrivKey()),
		observerTestPubKey(t, secp256k1.GenPrivKey()),
	}
	for i := 0; i < maxPendingObservedXMRSpentRefsPerReconcile+1; i++ {
		obsTx := base
		seed := fmt.Sprintf("single-flight-%02d", i)
		obsTx.Tx.ID = common.TxID(observerTestHash(seed + "-spend"))
		obsTx.SpentOutputRefs = []common.OutputRef{observerTestOutputRef(seed, obsTx.Tx.ID.String())}
		addPendingObservedXMRSpentRefsTestState(ag, obsTx, signer, time.Now().Add(time.Duration(i)*time.Second))
	}

	loadVault := observerTestVaultFunc(base.ObservedPubKey, members...)
	started := make(chan struct{})
	release := make(chan struct{})
	var vaultCalls atomic.Int32
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		if vaultCalls.Add(1) == 1 {
			close(started)
		}
		<-release
		return loadVault(pubKey)
	}

	ag.triggerPendingObservedXMRSpentRefsReconcile(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not reach the bridge")
	}
	ag.triggerPendingObservedXMRSpentRefsReconcile(context.Background())
	time.Sleep(50 * time.Millisecond)
	assert.EqualValues(t, 1, vaultCalls.Load())
	close(release)
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.pendingXMRSpentRefsReconcileRunning
	}, time.Second, 10*time.Millisecond)
}

// TestHistoricalXMRCertificateBroadcastAttemptsAllActivePeers verifies that a
// full send semaphore delays later peers instead of truncating the broadcast.
func TestHistoricalXMRCertificateBroadcastAttemptsAllActivePeers(t *testing.T) {
	ag, host, _, _, _, _ := setupTestGossip(t)
	remotePeers := configureHistoricalXMRCertificateBroadcastTestPeers(
		t,
		ag,
		host,
		historicalXMRCertificateSendConcurrency+8,
	)
	certificate := observerTestHistoricalXMRBroadcastCertificate(t)

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	started := make(chan peer.ID, len(remotePeers))
	attempted := make(map[peer.ID]struct{}, len(remotePeers))
	var attemptedMu sync.Mutex
	host.streamFunc = func(ctx context.Context, remotePeer peer.ID, _ ...protocol.ID) (network.Stream, error) {
		attemptedMu.Lock()
		attempted[remotePeer] = struct{}{}
		attemptedMu.Unlock()
		started <- remotePeer
		select {
		case <-release:
			return nil, fmt.Errorf("test stream released")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	done := make(chan struct{})
	go func() {
		ag.broadcastHistoricalXMRSpentRefCertificate(context.Background(), certificate)
		close(done)
	}()
	for range historicalXMRCertificateSendConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("broadcast did not fill its send capacity")
		}
	}
	select {
	case <-done:
		t.Fatal("broadcast returned before later active peers received an attempt")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast did not finish after send capacity was released")
	}
	require.Eventually(t, func() bool {
		attemptedMu.Lock()
		defer attemptedMu.Unlock()
		return len(attempted) == len(remotePeers)
	}, time.Second, 10*time.Millisecond)
	attemptedMu.Lock()
	defer attemptedMu.Unlock()
	for _, remotePeer := range remotePeers {
		assert.Contains(t, attempted, remotePeer)
	}
}

// TestHistoricalXMRCertificateBroadcastBoundsConcurrentSends verifies that
// waiting for full-peer coverage never exceeds the process-wide send limit.
func TestHistoricalXMRCertificateBroadcastBoundsConcurrentSends(t *testing.T) {
	ag, host, _, _, _, _ := setupTestGossip(t)
	remotePeers := configureHistoricalXMRCertificateBroadcastTestPeers(
		t,
		ag,
		host,
		historicalXMRCertificateSendConcurrency*3,
	)
	certificate := observerTestHistoricalXMRBroadcastCertificate(t)

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var attempted atomic.Int32
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	host.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
		attempted.Add(1)
		current := inFlight.Add(1)
		for previous := maxInFlight.Load(); current > previous; previous = maxInFlight.Load() {
			if maxInFlight.CompareAndSwap(previous, current) {
				break
			}
		}
		defer inFlight.Add(-1)
		select {
		case <-release:
			return nil, fmt.Errorf("test stream released")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	done := make(chan struct{})
	go func() {
		ag.broadcastHistoricalXMRSpentRefCertificate(context.Background(), certificate)
		close(done)
	}()
	require.Eventually(t, func() bool {
		return inFlight.Load() == historicalXMRCertificateSendConcurrency
	}, time.Second, 10*time.Millisecond)
	assert.EqualValues(t, historicalXMRCertificateSendConcurrency, maxInFlight.Load())

	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("broadcast did not schedule every peer after capacity was released")
	}
	require.Eventually(t, func() bool {
		return attempted.Load() == int32(len(remotePeers)) && inFlight.Load() == 0
	}, time.Second, 10*time.Millisecond)
	assert.LessOrEqual(t, maxInFlight.Load(), int32(historicalXMRCertificateSendConcurrency))
}

// TestHistoricalXMRCertificateBroadcastCancellation verifies that a broadcast
// waiting for send capacity exits promptly when its controlling context ends.
func TestHistoricalXMRCertificateBroadcastCancellation(t *testing.T) {
	ag, host, _, _, _, _ := setupTestGossip(t)
	configureHistoricalXMRCertificateBroadcastTestPeers(
		t,
		ag,
		host,
		historicalXMRCertificateSendConcurrency+1,
	)
	certificate := observerTestHistoricalXMRBroadcastCertificate(t)

	var inFlight atomic.Int32
	started := make(chan struct{}, historicalXMRCertificateSendConcurrency+1)
	host.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		ag.broadcastHistoricalXMRSpentRefCertificate(ctx, certificate)
		close(done)
	}()
	for range historicalXMRCertificateSendConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("broadcast did not fill its send capacity")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled broadcast remained blocked on send capacity")
	}
	require.Eventually(t, func() bool { return inFlight.Load() == 0 }, time.Second, 10*time.Millisecond)
}

// TestHistoricalXMRCertificateReconcileSingleFlight verifies that a full-peer
// fanout cannot accumulate overlapping reconciliation passes on later ticks.
func TestHistoricalXMRCertificateReconcileSingleFlight(t *testing.T) {
	ag, host, keys, _, bridge, _ := setupTestGossip(t)
	remotePeers := configureHistoricalXMRCertificateBroadcastTestPeers(
		t,
		ag,
		host,
		historicalXMRCertificateSendConcurrency+1,
	)
	firstObsTx, plan := observerTestXMRPlan(t)
	producer := wireHistoricalXMRCertificateProducer(t, ag, bridge)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(firstObsTx.ObservedPubKey, observerTestPubKey(t, keys.privKey))
	bridge.getKeysignFunc = observerTestKeysignFunc(plan)
	require.NoError(t, ag.AttestObservedTx(context.Background(), &firstObsTx, false, false))

	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var attempts atomic.Int32
	started := make(chan struct{}, len(remotePeers)*2)
	host.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
		attempts.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return nil, fmt.Errorf("test stream released")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	ag.triggerHistoricalXMRSpentRefCertificateReconcile(context.Background())
	for range historicalXMRCertificateSendConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("historical reconciliation did not fill send capacity")
		}
	}

	secondObsTx := firstObsTx
	secondObsTx.Tx.ID = common.TxID(observerTestHash("historical-reconcile-single-flight-spend"))
	secondObsTx.SpentOutputRefs = []common.OutputRef{
		observerTestOutputRef("historical-reconcile-single-flight-ref", secondObsTx.Tx.ID.String()),
	}
	require.NoError(t, ag.AttestObservedTx(context.Background(), &secondObsTx, false, false))
	ag.triggerHistoricalXMRSpentRefCertificateReconcile(context.Background())
	time.Sleep(50 * time.Millisecond)
	ag.mu.Lock()
	reconcileRunning := ag.historicalXMRCertificateReconcileRunning
	ag.mu.Unlock()
	assert.True(t, reconcileRunning)
	pending, _, err := producer.storage.pendingXMRCertificates(time.Now(), 16)
	require.NoError(t, err)
	require.Len(t, pending, 1, "overlapping trigger must not claim newly queued work")
	require.Zero(t, pending[0].RetryDelay)
	assert.EqualValues(t, historicalXMRCertificateSendConcurrency, attempts.Load())

	releaseOnce.Do(func() { close(release) })
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.historicalXMRCertificateReconcileRunning
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		return attempts.Load() == int32(len(remotePeers))
	}, time.Second, 10*time.Millisecond)

	ag.triggerHistoricalXMRSpentRefCertificateReconcile(context.Background())
	require.Eventually(t, func() bool {
		return attempts.Load() == int32(len(remotePeers)*2)
	}, time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		ag.mu.Lock()
		defer ag.mu.Unlock()
		return !ag.historicalXMRCertificateReconcileRunning
	}, time.Second, 10*time.Millisecond)
}

// TestHistoricalXMRCertificateReconcileBacksOffRPCs verifies a retained,
// sub-quorum bucket does not reload its source vault on every reconciliation
// tick, and that a due retry advances the exponential schedule before RPC.
func TestHistoricalXMRCertificateReconcileBacksOffRPCs(t *testing.T) {
	ag, _, _, _, bridge, _ := setupTestGossip(t)
	obsTx, planned := observerTestXMRPlan(t)
	signer := secp256k1.GenPrivKey()
	members := common.PubKeys{
		observerTestPubKey(t, signer),
		observerTestPubKey(t, secp256k1.GenPrivKey()),
		observerTestPubKey(t, secp256k1.GenPrivKey()),
	}
	vaultCalls := 0
	loadVault := observerTestRetiringVaultFunc(obsTx.ObservedPubKey, members...)
	bridge.getVaultFunc = func(pubKey string) (thortypes.Vault, error) {
		vaultCalls++
		return loadVault(pubKey)
	}
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	ag.SetObserverApplySpentOutputRefs(func(spentOutputRefsImport, func(bool)) {})

	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(
		observerTestSignedHistoricalXMRCertificate(t, obsTx, signer),
		observerTestPeerID(t, signer),
	))
	require.Equal(t, 1, vaultCalls)

	ag.mu.Lock()
	require.Len(t, ag.historicalXMRSpentRefCertificates, 1)
	var bucket *historicalXMRSpentRefCertificateBucket
	for _, candidate := range ag.historicalXMRSpentRefCertificates {
		bucket = candidate
	}
	require.NotNil(t, bucket)
	initialDelay := bucket.retryDelay
	require.Positive(t, initialDelay)
	require.True(t, time.Now().Before(bucket.nextRetryAt))
	for pubKey, entry := range ag.historicalXMRCertificateRPC.vaultCache {
		entry.expiresAt = time.Time{}
		ag.historicalXMRCertificateRPC.vaultCache[pubKey] = entry
	}
	ag.mu.Unlock()

	vaultCalls = 0
	ag.reconcileHistoricalXMRSpentRefCertificates(context.Background())
	assert.Zero(t, vaultCalls, "a bucket still in backoff must not issue RPCs")

	ag.mu.Lock()
	bucket.nextRetryAt = time.Time{}
	ag.mu.Unlock()
	ag.reconcileHistoricalXMRSpentRefCertificates(context.Background())
	require.Equal(t, 1, vaultCalls)
	ag.reconcileHistoricalXMRSpentRefCertificates(context.Background())
	assert.Equal(t, 1, vaultCalls, "the advanced backoff must suppress an immediate second retry")

	ag.mu.Lock()
	assert.Equal(t, initialDelay*2, bucket.retryDelay)
	require.True(t, time.Now().Before(bucket.nextRetryAt))
	ag.mu.Unlock()
}

func TestHistoricalXMRCertificateActiveReceiverReconcilesAfterCapacityRejection(t *testing.T) {
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
	bridge.getVaultFunc = observerTestRetiringVaultFunc(obsTx.ObservedPubKey, members...)
	bridge.getKeysignFunc = observerTestKeysignFunc(planned)
	ag.applySpentRefsSem = make(chan struct{}, 1)
	ag.applySpentRefsSem <- struct{}{}
	applied := make(chan common.ObservedTx, 1)
	ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, onOutcome func(bool)) {
		tx := request.Tx
		applied <- tx
		if onOutcome != nil {
			onOutcome(true)
		}
	})

	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(
		observerTestSignedHistoricalXMRCertificate(t, obsTx, first), observerTestPeerID(t, first),
	))
	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(
		observerTestSignedHistoricalXMRCertificate(t, obsTx, second), observerTestPeerID(t, second),
	))
	assertNoSpentRefImportDuring(t, applied, 100*time.Millisecond)
	<-ag.applySpentRefsSem

	// The ordinary ticker must respect the certificate's retry schedule.
	ag.reconcileHistoricalXMRSpentRefCertificates(context.Background())
	assertNoSpentRefImportDuring(t, applied, 50*time.Millisecond)

	// setupTestGossip leaves this process active. Once due, active receivers still
	// retry locally rather than depending on another inactive peer duplicate.
	ag.mu.Lock()
	for _, bucket := range ag.historicalXMRSpentRefCertificates {
		bucket.nextRetryAt = time.Time{}
	}
	ag.mu.Unlock()
	ag.reconcileHistoricalXMRSpentRefCertificates(context.Background())
	select {
	case got := <-applied:
		assert.Equal(t, obsTx.Tx.ID, got.Tx.ID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for historical certificate reconciliation")
	}
}

func observerTestSignedHistoricalXMRCertificate(t *testing.T, obsTx common.ObservedTx, signer *secp256k1.PrivKey) common.AttestTx {
	t.Helper()
	payload, err := obsTx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	signature, err := signer.Sign(payload)
	require.NoError(t, err)
	return common.AttestTx{
		ObsTx:   obsTx,
		Inbound: false,
		Attestation: &common.Attestation{
			PubKey:    signer.PubKey().Bytes(),
			Signature: signature,
		},
	}
}

func observerTestPeerID(t *testing.T, signer *secp256k1.PrivKey) peer.ID {
	t.Helper()
	peerID, err := conversion.GetPeerIDFromSecp256PubKey(signer.PubKey().Bytes())
	require.NoError(t, err)
	return peerID
}

func observerTestHistoricalXMRBroadcastCertificate(t *testing.T) common.AttestTx {
	t.Helper()
	obsTx, _ := observerTestXMRPlan(t)
	return observerTestSignedHistoricalXMRCertificate(t, obsTx, secp256k1.GenPrivKey())
}

func configureHistoricalXMRCertificateBroadcastTestPeers(
	t *testing.T,
	ag *AttestationGossip,
	host *MockHost,
	remoteCount int,
) []peer.ID {
	t.Helper()
	require.Positive(t, remoteCount)
	self := host.ID()
	allPeers := make([]peer.ID, 1, remoteCount+1)
	allPeers[0] = self
	remotePeers := make([]peer.ID, 0, remoteCount)
	active := make(map[peer.ID]bool, remoteCount)
	for i := range remoteCount {
		remotePeer := peer.ID(fmt.Sprintf("historical-certificate-peer-%03d", i))
		allPeers = append(allPeers, remotePeer)
		remotePeers = append(remotePeers, remotePeer)
		active[remotePeer] = true
	}
	host.peers = allPeers
	ag.avMu.Lock()
	ag.activeVals = active
	ag.avMu.Unlock()
	return remotePeers
}

func wireHistoricalXMRCertificateProducer(t *testing.T, ag *AttestationGossip, bridge *MockThorchainBridge) *Observer {
	t.Helper()
	store, path := setupTestDB(t)
	t.Cleanup(func() { cleanupTestDB(t, store, path) })
	o := &Observer{
		logger: ag.logger, storage: store, attestationGossip: ag, thorchainBridge: bridge,
		lastNodeStatus:      thortypes.NodeStatus_Standby,
		chains:              map[common.Chain]chainclients.ChainClient{common.XMRChain: &historicalXMRConfirmationChain{}},
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}
	ag.certificateObserver = o
	return o
}

func addPendingObservedXMRSpentRefsTestState(
	ag *AttestationGossip,
	obsTx common.ObservedTx,
	signer *secp256k1.PrivKey,
	observedAt time.Time,
) *AttestationState[*attestableObservedTx] {
	state := ag.observedTxsPool.NewAttestationState(&attestableObservedTx{ObservedTx: &obsTx})
	state.firstAttestationObserved = observedAt
	state.attestations = append(state.attestations, attestationSentState{
		attestation: &common.Attestation{
			PubKey:    signer.PubKey().Bytes(),
			Signature: []byte("reconcile-" + obsTx.Tx.ID.String()),
		},
	})
	ag.mu.Lock()
	ag.observedTxs[observedTxKey(obsTx, false, false)] = state
	ag.mu.Unlock()
	return state
}

func observerTestRetiringVaultFunc(vaultPubKey common.PubKey, members ...common.PubKey) func(string) (thortypes.Vault, error) {
	base := observerTestVaultFunc(vaultPubKey, members...)
	return func(pubKey string) (thortypes.Vault, error) {
		vault, err := base(pubKey)
		vault.Status = thortypes.VaultStatus_RetiringVault
		return vault, err
	}
}

// A signing committee can contain two current validators and one churned-out
// member. Its certificate must bind exactly the same observation bytes as the
// current members, including the finalization heights and instant-signing path.
func TestHistoricalXMRCertificateMatchesActiveObservationPhase(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		filtered, mempool, ready bool
		confirmations            int64
	}{
		{name: "instant signing", filtered: true, mempool: true, ready: true},
		{name: "canonical finalized", ready: true, confirmations: 10},
		{name: "canonical awaiting confirmations", confirmations: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag, _, keys, _, bridge, _ := setupTestGossip(t)
			obs, planned := observerTestXMRPlan(t)
			var err error
			obs.Tx.ID, err = common.NewTxID(obs.Tx.ID.String())
			require.NoError(t, err)
			obs.SpentOutputRefs[0].SpendTxHash = obs.Tx.ID.String()
			obs.BlockHeight = 1732
			obs.XmrMinedHeight = obs.BlockHeight
			if tc.mempool {
				obs.XmrMinedHeight = 0
			}
			obs.FinaliseHeight = obs.BlockHeight + tc.confirmations
			second, third, fourth := secp256k1.GenPrivKey(), secp256k1.GenPrivKey(), secp256k1.GenPrivKey()
			ag.setActiveValidators(nil)
			bridge.getVaultFunc = observerTestRetiringVaultFunc(obs.ObservedPubKey,
				observerTestPubKey(t, keys.privKey), observerTestPubKey(t, second),
				observerTestPubKey(t, third), observerTestPubKey(t, fourth))
			bridge.getKeysignFunc = observerTestKeysignFunc(planned)
			item := &tctypes.TxInItem{
				BlockHeight: obs.BlockHeight, Tx: obs.Tx.ID.String(), Memo: obs.Tx.Memo,
				Sender: obs.Tx.FromAddress.String(), To: obs.Tx.ToAddress.String(),
				Coins: obs.Tx.Coins, Gas: obs.Tx.Gas, ObservedVaultPubKey: obs.ObservedPubKey,
				SpentOutputRefs: []*common.OutputRef{&obs.SpentOutputRefs[0]},
			}
			expected := obs
			if tc.mempool {
				expected.XmrMinedHeight = 0
			}
			if tc.ready && !tc.mempool {
				expected.BlockHeight = expected.FinaliseHeight
			}
			// Two ordinary source-member signatures are insufficient by themselves.
			ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, expected, second))
			ag.handleObservedTxAttestation(context.Background(), observerTestSignedHistoricalXMRCertificate(t, expected, third))
			applied := make(chan spentOutputRefsImport, 1)
			ag.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, outcome func(bool)) {
				applied <- request
				outcome(true)
			})
			observer := wireHistoricalXMRCertificateProducer(t, ag, bridge)
			observer.chains[common.XMRChain] = &historicalXMRConfirmationChain{confirmations: 10, ready: tc.ready}
			txIn := tctypes.TxIn{
				Chain: common.XMRChain, Filtered: tc.filtered, MemPool: tc.mempool,
				ConfirmationRequired: tc.confirmations, TxArray: []*tctypes.TxInItem{item},
			}
			require.NoError(t, observer.attestHistoricalXMRSpentRefCertificates(txIn))
			observer.retryPendingXMRCertificates(context.Background())
			select {
			case got := <-applied:
				wantBytes, payloadErr := expected.GetSignablePayloadWithInbound(false)
				require.NoError(t, payloadErr)
				gotBytes, payloadErr := got.Tx.GetSignablePayloadWithInbound(false)
				require.NoError(t, payloadErr)
				require.Equal(t, wantBytes, gotBytes)
				require.False(t, got.ChainCommitted)
			case <-time.After(time.Second):
				t.Fatal("historical member did not complete the exact source-vault certificate")
			}
			payload, err := expected.GetSignablePayloadWithInbound(false)
			require.NoError(t, err)
			require.Len(t, ag.observedXMRPayloadAttestations(expected, sha256.Sum256(payload)), 2,
				"the inactive member must not become an ordinary observation vote")
		})
	}
}

func TestHistoricalXMRCertificateIgnoresUnfilteredMempool(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	ag.setActiveValidators(nil)
	observer := &Observer{attestationGossip: ag}
	require.NoError(t, observer.attestHistoricalXMRSpentRefCertificates(tctypes.TxIn{
		Chain: common.XMRChain, MemPool: true, TxArray: []*tctypes.TxInItem{{}},
	}))
	ag.mu.Lock()
	defer ag.mu.Unlock()
	require.Empty(t, ag.historicalXMRSpentRefCertificates)
	require.Empty(t, ag.observedTxs)
}

func TestHistoricalXMRCertificateVaultStatusDoesNotGrantAuthority(t *testing.T) {
	obs, _ := observerTestXMRPlan(t)
	for _, status := range []thortypes.VaultStatus{thortypes.VaultStatus_ActiveVault, thortypes.VaultStatus_RetiringVault, thortypes.VaultStatus_InactiveVault} {
		vault := thortypes.Vault{PubKey: obs.ObservedPubKey, Status: status}
		require.NoError(t, validateHistoricalXMRCertificateVault(obs.ObservedPubKey, vault))
		require.Error(t, validateHistoricalXMRCertificateVault(common.PubKey("wrong-vault"), vault))
	}
	require.Error(t, validateHistoricalXMRCertificateVault(obs.ObservedPubKey, thortypes.Vault{}))
}

func (c *historicalXMRConfirmationChain) GetHeight() (int64, error) { return c.height, nil }

func TestHistoricalXMRCertificateUsesFullBatchAndSurvivesProducerRestart(t *testing.T) {
	for _, prefix := range []string{"MIGRATE:", "OUT:", "REFUND:"} {
		for _, finalized := range []bool{false, true} {
			t.Run(prefix+fmt.Sprint(finalized), func(t *testing.T) {
				ag, _, keys, _, bridge, _ := setupTestGossip(t)
				obs, plan := observerTestXMRPlan(t)
				obs.Tx.ID, _ = common.NewTxID(obs.Tx.ID.String())
				obs.SpentOutputRefs[0].SpendTxHash = obs.Tx.ID.String()
				ag.setActiveValidators(nil)
				bridge.getVaultFunc = observerTestRetiringVaultFunc(obs.ObservedPubKey, observerTestPubKey(t, keys.privKey))
				bridge.getKeysignFunc = observerTestKeysignFunc(plan)
				if prefix != "MIGRATE:" {
					inboundID := common.TxID(observerTestHash("restart-inbound"))
					obs.Tx.Memo = prefix + inboundID.String()
					plan.memo, plan.wireMemo = obs.Tx.Memo, obs.Tx.Memo
					bridge.getObservedTxVoterFunc = observerTestObservedTxVoterFunc(inboundID, plan)
				}
				store, path := setupTestDB(t)
				chain := &historicalXMRConfirmationChain{ready: finalized, height: 237}
				countCalls := 0
				chain.confirmationCount = func(batch tctypes.TxIn) int64 {
					countCalls++
					require.Len(t, batch.TxArray, 2, "confirmation calculation must include the deposit")
					require.Equal(t, int64(200), batch.TxArray[0].BlockHeight, "keep the active path's first-item anchor")
					return 37
				}
				o := &Observer{
					logger: ag.logger, storage: store, attestationGossip: ag, thorchainBridge: bridge,
					lastNodeStatus: thortypes.NodeStatus_Standby,
					chains:         map[common.Chain]chainclients.ChainClient{common.XMRChain: chain}, tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
				}
				ag.certificateObserver = o
				payout := &tctypes.TxInItem{
					BlockHeight: 190, Tx: obs.Tx.ID.String(), Memo: obs.Tx.Memo,
					Sender: obs.Tx.FromAddress.String(), To: obs.Tx.ToAddress.String(), Coins: obs.Tx.Coins, Gas: obs.Tx.Gas,
					ObservedVaultPubKey: obs.ObservedPubKey, SpentOutputRefs: []*common.OutputRef{&obs.SpentOutputRefs[0]}, TxKey: obs.XmrTxKey,
				}
				deposit := payout.Copy()
				deposit.BlockHeight, deposit.Tx = 200, observerTestHash("large-deposit")
				deposit.Sender, deposit.To = "unknown-sender", obs.Tx.FromAddress.String()
				deposit.Memo, deposit.SpentOutputRefs, deposit.TxKey = "SWAP:BTC.BTC", nil, ""
				deposit.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2220000000)))
				batch := tctypes.TxIn{Chain: common.XMRChain, TxArray: []*tctypes.TxInItem{deposit, payout}}
				ag.setActiveValidators(common.PubKeys{observerTestPubKey(t, keys.privKey)})
				require.ErrorContains(t, o.attestHistoricalXMRSpentRefCertificates(batch), "waiting for node status")
				before, _, err := store.pendingXMRCertificates(time.Now().Add(time.Second), 16)
				require.NoError(t, err)
				require.Empty(t, before)
				ag.setActiveValidators(nil)
				countCalls = 0
				require.NoError(t, o.attestHistoricalXMRSpentRefCertificates(batch))
				require.Equal(t, 1, countCalls)
				records, _, err := store.pendingXMRCertificates(time.Now().Add(time.Second), 16)
				require.NoError(t, err)
				require.Len(t, records, 1)
				var got common.AttestTx
				require.NoError(t, got.Unmarshal(records[0].Payload))
				expected := obs
				expected.BlockHeight, expected.FinaliseHeight = 190, 237
				expected.XmrMinedHeight = payout.BlockHeight
				if finalized {
					expected.BlockHeight = 237
				}
				wantBytes, err := expected.GetSignablePayloadWithInbound(false)
				require.NoError(t, err)
				gotBytes, err := got.ObsTx.GetSignablePayloadWithInbound(false)
				require.NoError(t, err)
				require.Equal(t, wantBytes, gotBytes)
				require.NoError(t, verifySignature(gotBytes, got.Attestation.Signature, got.Attestation.PubKey))
				// Crash after durable handoff, before sending. Opening only the persisted
				// store is sufficient; no scanner receipt or in-memory certificate exists.
				require.NoError(t, store.Close())
				store, err = NewObserverStorage(path, config.LevelDBOptions{})
				require.NoError(t, err)
				defer cleanupTestDB(t, store, path)
				o.storage = store
				o.retryPendingXMRCertificates(context.Background())
				records, _, err = store.pendingXMRCertificates(time.Now().Add(time.Hour), 16)
				require.NoError(t, err)
				require.Len(t, records, 1, "local import must not complete durable recovery")
				got = common.AttestTx{}
				require.NoError(t, got.Unmarshal(records[0].Payload))
				require.Equal(t, int64(237), got.ObsTx.BlockHeight, "saved full-batch finalization threshold survives restart")
				require.Equal(t, int64(237), got.ObsTx.FinaliseHeight)
				require.Equal(t, payout.BlockHeight, got.ObsTx.XmrMinedHeight, "original mined height survives restart")
				finalBytes, err := got.ObsTx.GetSignablePayloadWithInbound(false)
				require.NoError(t, err)
				require.NoError(t, verifySignature(finalBytes, got.Attestation.Signature, got.Attestation.PubKey))
			})
		}
	}
}

func TestPendingXMRCertificateCompletionRequiresCanonicalFinality(t *testing.T) {
	obs, _ := observerTestXMRPlan(t)
	voter := &thortypes.QueryObservedTxVoter{TxID: obs.Tx.ID, Height: 123}
	require.False(t, pendingXMRCertificateComplete(obs, nil))
	require.False(t, pendingXMRCertificateComplete(obs, voter))
	voter.FinalisedHeight = 124
	voter.Tx = thortypes.QueryObservedTx{Tx: obs.Tx, ObservedPubKey: obs.ObservedPubKey, BlockHeight: 10, FinaliseHeight: 20, XmrTxKey: obs.XmrTxKey}
	require.False(t, pendingXMRCertificateComplete(obs, voter))
	voter.Tx.BlockHeight = 20
	require.False(t, pendingXMRCertificateComplete(obs, voter), "missing spend proof cannot complete recovery")
	voter.Tx.SpentOutputRefs = []*common.OutputRef{&obs.SpentOutputRefs[0]}
	require.True(t, pendingXMRCertificateComplete(obs, voter))
	voter.Tx.Tx.ID = common.TxID(observerTestHash("different"))
	require.False(t, pendingXMRCertificateComplete(obs, voter))
	voter.Reverted = true
	require.True(t, pendingXMRCertificateComplete(obs, voter))
	voter.TxID = common.TxID(observerTestHash("different"))
	require.False(t, pendingXMRCertificateComplete(obs, voter))
}

func TestHistoricalXMRCertificateCapacityReclaimsOnlyAppliedReceipts(t *testing.T) {
	ag, _, _, _, _, _ := setupTestGossip(t)
	signer := "historical-member"
	ag.historicalXMRSpentRefCertificates = make(map[[sha256.Size]byte]*historicalXMRSpentRefCertificateBucket)
	ag.historicalXMRCertificateSignerUse = map[string]int{signer: maxHistoricalXMRCertificateBucketsPerSigner}
	for i := 0; i < maxHistoricalXMRCertificateBucketsPerSigner; i++ {
		hash := sha256.Sum256([]byte(fmt.Sprint(i)))
		ag.historicalXMRSpentRefCertificates[hash] = &historicalXMRSpentRefCertificateBucket{attestations: map[string]*common.Attestation{signer: {}}, updatedAt: time.Now()}
	}
	incoming := sha256.Sum256([]byte("new-payout"))
	require.Error(t, ag.checkHistoricalXMRCertificateCapacity(incoming, signer), "unapplied signatures must not be evicted")
	completed := sha256.Sum256([]byte("0"))
	ag.historicalXMRSpentRefCertificates[completed].applied = true
	require.NoError(t, ag.checkHistoricalXMRCertificateCapacity(incoming, signer))
	require.NotContains(t, ag.historicalXMRSpentRefCertificates, completed)
	require.Equal(t, maxHistoricalXMRCertificateBucketsPerSigner-1, ag.historicalXMRCertificateSignerUse[signer])
}

func TestHistoricalXMRCertificateRequiresDurableOwner(t *testing.T) {
	ag, _, keys, _, bridge, _ := setupTestGossip(t)
	obs, plan := observerTestXMRPlan(t)
	ag.setActiveValidators(nil)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(obs.ObservedPubKey, observerTestPubKey(t, keys.privKey))
	bridge.getKeysignFunc = observerTestKeysignFunc(plan)
	require.ErrorContains(t, ag.AttestObservedTx(context.Background(), &obs, false, false), "durable storage is unavailable")
	require.Empty(t, ag.historicalXMRSpentRefCertificates)
	ag.certificateObserver = &Observer{}
	require.ErrorContains(t, ag.AttestObservedTx(context.Background(), &obs, false, false), "durable storage is unavailable")
	producer := wireHistoricalXMRCertificateProducer(t, ag, bridge)
	require.NoError(t, ag.AttestObservedTx(context.Background(), &obs, false, false))
	records, _, err := producer.storage.pendingXMRCertificates(time.Now().Add(time.Second), 16)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Empty(t, ag.historicalXMRSpentRefCertificates, "producer admission must not create non-durable delivery state")
}

func TestHistoricalXMRCertificateReceiverNeverOwnsProducerDelivery(t *testing.T) {
	ag, host, keys, _, bridge, _ := setupTestGossip(t)
	obs, plan := observerTestXMRPlan(t)
	configureHistoricalXMRCertificateBroadcastTestPeers(t, ag, host, 1)
	bridge.getVaultFunc = observerTestRetiringVaultFunc(obs.ObservedPubKey, observerTestPubKey(t, keys.privKey))
	bridge.getKeysignFunc = observerTestKeysignFunc(plan)
	var sends atomic.Int32
	host.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) {
		sends.Add(1)
		return nil, fmt.Errorf("unexpected receiver broadcast")
	}
	require.NoError(t, ag.acceptHistoricalXMRSpentRefCertificate(observerTestSignedHistoricalXMRCertificate(t, obs, keys.privKey), ""))
	ag.mu.Lock()
	for _, bucket := range ag.historicalXMRSpentRefCertificates {
		bucket.nextRetryAt = time.Time{}
	}
	ag.mu.Unlock()
	ag.reconcileHistoricalXMRSpentRefCertificates(context.Background())
	// A removed fallback used asynchronous sends. Wait for any claimed send
	// slots to drain so this assertion also catches that implementation.
	ag.mu.Lock()
	sendSem := ag.historicalXMRCertificateSendSem
	ag.mu.Unlock()
	require.Eventually(t, func() bool { return len(sendSem) == 0 }, time.Second, time.Millisecond)
	require.Zero(t, sends.Load(), "only the durable producer may retransmit")
	require.NotEmpty(t, ag.historicalXMRSpentRefCertificates, "receiver proof and local retries are retained")
}
