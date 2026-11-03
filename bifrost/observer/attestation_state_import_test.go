package observer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// WithTimeout computes its child deadline before consulting parent.Deadline.
// Pause that consultation so the test can expire exactly that child budget
// while keeping the original transfer live, without a production test hook.
type pausedImportDeadline struct {
	context.Context
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (c *pausedImportDeadline) Deadline() (time.Time, bool) {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Context.Deadline()
}

func stateImportFrames(t *testing.T, batches ...common.QuorumState) [][]byte {
	t.Helper()
	var frames [][]byte
	for i, batch := range batches {
		data, err := batch.Marshal()
		require.NoError(t, err)
		frames = append(frames, stateBoundsHeader(uint32(i), uint32(len(data))), append([]byte{prefixBatchData[0]}, data...))
	}
	return append(frames, prefixBatchEnd)
}

type stateImportCompletionStream struct {
	*stateImportRetentionStream
	doneWriteErr error
}

func (s *stateImportCompletionStream) Write(p []byte) (int, error) {
	if s.doneWriteErr != nil && bytes.Equal(p, encodeStreamMsg([]byte(p2p.StreamMsgDone))) {
		return 0, s.doneWriteErr
	}
	return s.stateBoundsStream.Write(p)
}

func TestStateImportIncompleteClosesFinishedExchange(t *testing.T) {
	for _, tc := range []struct {
		name         string
		retainFee    bool
		invalidEnd   bool
		failFinalACK bool
	}{
		{name: "no retained evidence"},
		{name: "retained evidence", retainFee: true},
		{name: "invalid final frame", retainFee: true, invalidEnd: true},
		{name: "failed final acknowledgement", retainFee: true, failFinalACK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _, _, _, _, _ := setupTestGossip(t)
			tx, _ := observerTestXMRPlan(t)
			batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
			fee, atts := stateSyncSignedFee(t, 785, 1)
			if tc.retainFee {
				batch.QuoNetworkFees = []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}
			}
			g.observerStorage = nil // Admission cannot decide until storage is available.
			frames := append([][]byte{append([]byte{prefixBatchBegin[0]}, stateBoundsCount(1)...)}, stateImportFrames(t, batch)...)
			if tc.invalidEnd {
				frames[len(frames)-1] = []byte{0xff}
			}
			base := newStateBoundsStream("peer", frames...)
			var retained *common.NetworkFee
			if tc.retainFee {
				retained = fee
			}
			stream := &stateImportCompletionStream{stateImportRetentionStream: stateImportAfterRetention(g, retained, base)}
			writeErr := errors.New("final acknowledgement write failed")
			if tc.failFinalACK {
				stream.doneWriteErr = writeErr
			}

			result := g.askStreamForState(stream)
			require.False(t, result.completed, "missing evidence must remain retryable")
			require.NotContains(t, g.observedTxs, observedTxKey(tx, false, false))
			if tc.retainFee {
				require.Equal(t, 1, result.items)
				require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
			} else {
				require.Zero(t, result.items)
			}
			if tc.invalidEnd || tc.failFinalACK {
				require.EqualValues(t, 1, stream.resets.Load())
				require.Zero(t, stream.closes.Load())
				require.NotContains(t, ParseMessages(stream.output.Bytes()), []byte(p2p.StreamMsgDone))
				if tc.failFinalACK {
					require.ErrorIs(t, result.err, writeErr)
				} else {
					require.ErrorContains(t, result.err, "expected batch end")
				}
			} else {
				require.EqualValues(t, 1, stream.closes.Load(), "a finished exchange must close gracefully despite the import miss")
				require.Zero(t, stream.resets.Load())
				require.Contains(t, ParseMessages(stream.output.Bytes()), []byte(p2p.StreamMsgDone))
				require.ErrorContains(t, result.err, "attestation state import incomplete:")
			}
		})
	}
}

func TestStateImportExpiredChildKeepsLaterItems(t *testing.T) {
	g, _, _, _, _, _ := setupTestGossip(t)
	g.config.PeerTimeout = 30 * time.Millisecond
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	fee, atts := stateSyncSignedFee(t, 780, 1)
	batch.QuoNetworkFees = []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}
	later, laterAtts := stateSyncSignedFee(t, 781, 1)
	batches := []common.QuorumState{batch, {QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: later, Attestations: laterAtts}}}}
	frames := stateImportFrames(t, batches...)
	transfer, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	ctx := &pausedImportDeadline{Context: transfer, entered: make(chan struct{}), release: release}
	type received struct {
		imports attestationStateImportResult
		err     error
	}
	done := make(chan received, 1)
	go func() {
		// Pause the per-item child budget directly; the receiver's own parent
		// deadline is exercised separately by the asynchronous stream tests.
		batch.QuoNetworkFees = append(batch.QuoNetworkFees, batches[1].QuoNetworkFees...)
		imported, err := g.importAttestationStateBatch(ctx, g.logger, batch)
		done <- received{imported, err}
	}()
	waitStateBounds(t, ctx.entered)
	timer := time.NewTimer(g.config.PeerTimeout)
	defer timer.Stop()
	<-timer.C
	unblock()
	var result received
	select {
	case result = <-done:
	case <-time.After(time.Second):
		t.Fatal("transfer failed to continue after the child deadline")
	}
	require.NoError(t, result.err, "the parent import remains live")
	require.ErrorIs(t, result.imports.incomplete, context.DeadlineExceeded)
	require.ErrorContains(t, result.imports.incomplete, "attestation state import incomplete:")
	require.NoError(t, transfer.Err())
	require.Equal(t, 2, result.imports.items)
	require.NotContains(t, g.observedTxs, observedTxKey(tx, false, false))
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
	require.Equal(t, 1, g.networkFees[*later].AttestationCount())

	// Replaying the same snapshot can recover the skipped item. The other
	// records remain valid non-empty duplicates rather than new signatures.
	g.config.PeerTimeout = time.Second
	imported, err := g.receiveAttestationStateTransfer(transfer, newStateBoundsStream("peer", frames...), stateBoundsCount(2))
	require.NoError(t, err)
	require.NoError(t, imported.incomplete)
	require.Equal(t, 3, imported.items)
	require.Equal(t, 1, g.observedTxs[observedTxKey(tx, false, false)].AttestationCount())
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
}

func TestStartupStateSyncAdmissionReadErrorRetriesRetainedEvidence(t *testing.T) {
	g, h := startupRetryGossip(t, "peer")
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	fee, atts := stateSyncSignedFee(t, 782, 1)
	batch.QuoNetworkFees = []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}
	data, err := batch.Marshal()
	require.NoError(t, err)
	h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		return stateSyncImportStream(t, id, data), nil
	}
	storage := g.observerStorage
	g.observerStorage = nil // The real admission API returns a storage-readiness error.
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	settleStartupRetry(t, g, now)
	require.False(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{failed: 1, partial: 1}, g.startupState.result)
	require.NotContains(t, g.observedTxs, observedTxKey(tx, false, false))
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())

	g.observerStorage = storage
	g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
	settleStartupRetry(t, g, g.startupState.next)
	require.True(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{failed: 1, partial: 1, nonEmpty: 1}, g.startupState.result)
	require.Equal(t, 1, g.observedTxs[observedTxKey(tx, false, false)].AttestationCount())
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
}

func TestStateImportDefinitiveAdmissionRejectionCompletesEmpty(t *testing.T) {
	g, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	errata := common.ErrataTx{Chain: common.XMRChain, Id: tx.Tx.ID}
	require.NoError(t, g.observerStorage.applyCommittedErrata(errata, nil))
	require.NoError(t, g.observerStorage.recordErrataVoterResult(errata, true, nil))
	data, err := batch.Marshal()
	require.NoError(t, err)
	result := g.askStreamForState(stateSyncImportStream(t, "peer", data))
	require.True(t, result.completed)
	require.NoError(t, result.err)
	require.Zero(t, result.items)
	require.NotContains(t, g.observedTxs, observedTxKey(tx, false, false))
}

func TestStateImportAcceptanceAcrossCategories(t *testing.T) {
	for _, category := range []string{"transaction", "network fee", "solvency", "errata", "price feed"} {
		for _, mode := range []string{"empty", "bad signature", "accepted and duplicate"} {
			t.Run(category+"/"+mode, func(t *testing.T) {
				g, _, _, _, _, _ := setupTestGossip(t)
				g.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
				g.priceFeedsPool = NewAttestationStatePool[*common.PriceFeed]()
				g.priceFeedsDelay = NewDelay()
				g.priceFeedsDelay.Start() // This fixture only checks retention, not delayed sends.
				key := secp256k1.GenPrivKey()
				var item AttestableItem
				var batch common.QuorumState
				switch category {
				case "transaction":
					tx, _ := observerTestXMRPlan(t)
					item = &attestableObservedTx{ObservedTx: &tx}
					batch.QuoTxs = []*common.QuorumTx{{ObsTx: tx}}
				case "network fee":
					fee, _ := stateSyncSignedFee(t, 783, 1)
					item = fee
					batch.QuoNetworkFees = []*common.QuorumNetworkFee{{NetworkFee: fee}}
				case "solvency":
					sol := &common.Solvency{Chain: common.BTCChain, Height: 783}
					item = sol
					batch.QuoSolvencies = []*common.QuorumSolvency{{Solvency: sol}}
				case "errata":
					errata := &common.ErrataTx{Chain: common.BTCChain, Id: "retained-errata"}
					item = errata
					batch.QuoErrataTxs = []*common.QuorumErrataTx{{ErrataTx: errata}}
				case "price feed":
					feed := &common.PriceFeed{Time: 783, Rates: []*common.OraclePrice{{Amount: 1, Decimals: 8}}, Version: make([]byte, 8)}
					item = feed
					batch.QuoPriceFeeds = []*common.QuorumPriceFeed{{PriceFeed: feed}}
				}
				payload, err := item.GetSignablePayload()
				require.NoError(t, err)
				signature, err := key.Sign(payload)
				require.NoError(t, err)
				if mode == "bad signature" {
					signature[0] ^= 0xff
				}
				var atts []*common.Attestation
				if mode != "empty" {
					atts = []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: signature}}
				}
				switch category {
				case "transaction":
					batch.QuoTxs[0].Attestations = atts
				case "network fee":
					batch.QuoNetworkFees[0].Attestations = atts
				case "solvency":
					batch.QuoSolvencies[0].Attestations = atts
				case "errata":
					batch.QuoErrataTxs[0].Attestations = atts
				case "price feed":
					batch.QuoPriceFeeds[0].Attestations = atts
				}
				data, err := batch.Marshal()
				require.NoError(t, err)
				for range 2 {
					result := g.askStreamForState(stateSyncImportStream(t, "peer", data))
					require.NoError(t, result.err)
					require.True(t, result.completed)
					if mode == "accepted and duplicate" {
						require.Equal(t, 1, result.items)
					} else {
						require.Zero(t, result.items)
					}
				}
			})
		}
	}
}

func TestStartupStateSyncRejectedOnlySnapshotRetriesAsEmpty(t *testing.T) {
	for _, mode := range []string{"invalid fields", "invalid signature", "no attestations"} {
		t.Run(mode, func(t *testing.T) {
			g, h := startupRetryGossip(t, "peer")
			fee, atts := stateSyncSignedFee(t, 784, 1)
			switch mode {
			case "invalid fields":
				fee.Height = -1
				payload, err := fee.GetSignablePayload()
				require.NoError(t, err)
				key := secp256k1.GenPrivKey()
				signature, err := key.Sign(payload)
				require.NoError(t, err)
				atts = []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: signature}}
			case "invalid signature":
				atts[0].Signature[0] ^= 0xff
			case "no attestations":
				atts = nil
			}
			data, err := (&common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}}).Marshal()
			require.NoError(t, err)
			h.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
				return stateSyncImportStream(t, id, data), nil
			}
			now := time.Now()
			g.startupState = newAttestationStateCollection(context.Background(), now, time.Hour, 2)
			for range 2 {
				g.tickAttestationStateSync(context.Background(), true, false, g.startupState.next)
				settleStartupRetry(t, g, g.startupState.next)
			}
			require.True(t, g.startupState.finished)
			require.Equal(t, 2, g.startupState.requests)
			require.Equal(t, attestationStateGroupResult{empty: 2}, g.startupState.result)
		})
	}
}

func TestStateImportDeliveryTimeoutKeepsAcceptedEvidence(t *testing.T) {
	g, _, _, client, _, _ := setupTestGossip(t)
	g.config.PeerTimeout = 20 * time.Millisecond
	tx, _ := observerTestXMRPlan(t)
	key := secp256k1.GenPrivKey()
	batch := signedCancellationBatch(t, tx, key)
	g.activeVals = map[peer.ID]bool{"one-validator": true}
	var submissionErr error
	client.sendQuorumTxFunc = func(ctx context.Context, _ *common.QuorumTx, _ ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
		<-ctx.Done()
		submissionErr = ctx.Err()
		return nil, submissionErr
	}
	data, err := batch.Marshal()
	require.NoError(t, err)
	result := g.askStreamForState(stateSyncImportStream(t, "peer", data))
	require.ErrorIs(t, submissionErr, context.DeadlineExceeded)
	require.True(t, result.completed)
	require.NoError(t, result.err)
	require.Equal(t, 1, result.items)
	state := g.observedTxs[observedTxKey(tx, false, false)]
	require.Len(t, state.UnsentAttestations(), 1)
	require.False(t, state.spentRefsApplied)
}

func TestStateImportSpentRefQueryFailureKeepsAcceptedEvidence(t *testing.T) {
	g, _, _, _, legacy, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	queried := false
	g.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(ctx context.Context, _ string) (thortypes.Vault, error) {
		require.NoError(t, ctx.Err(), "the enclosing transfer is still live")
		queried = true
		return thortypes.Vault{}, context.DeadlineExceeded
	}}
	g.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, _ func(bool)) { t.Error("failed authorization must not launch an import") })
	data, err := batch.Marshal()
	require.NoError(t, err)
	result := g.askStreamForState(stateSyncImportStream(t, "peer", data))
	require.True(t, queried)
	require.True(t, result.completed)
	require.NoError(t, result.err)
	require.Equal(t, 1, result.items)
	state := g.observedTxs[observedTxKey(tx, false, false)]
	require.Equal(t, 1, state.AttestationCount())
	require.False(t, state.spentRefsApplied)
}

// Delay the terminal/error frame until the first async batch has retained its
// evidence. Otherwise a buffered fixture can cancel it before it ever starts.
type stateImportRetentionStream struct {
	*stateBoundsStream
	gossip  *AttestationGossip
	fee     *common.NetworkFee
	after   int64
	once    sync.Once
	waitErr error
}

func stateImportAfterRetention(g *AttestationGossip, fee *common.NetworkFee, stream *stateBoundsStream) *stateImportRetentionStream {
	return &stateImportRetentionStream{stateBoundsStream: stream, gossip: g, fee: fee, after: stream.frameEnds[len(stream.frameEnds)-2]}
}

func (s *stateImportRetentionStream) Read(p []byte) (int, error) {
	if s.fee != nil && s.readBytes.Load() >= s.after {
		s.once.Do(func() {
			timeout := time.NewTimer(time.Second)
			defer timeout.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				s.gossip.mu.Lock()
				state := s.gossip.networkFees[*s.fee]
				retained := false
				if state != nil {
					state.mu.Lock()
					retained = state.AttestationCount() > 0
					state.mu.Unlock()
				}
				s.gossip.mu.Unlock()
				if retained {
					return
				}
				select {
				case <-s.stopped:
					s.waitErr = fmt.Errorf("stream stopped before retention")
					return
				case <-timeout.C:
					s.waitErr = fmt.Errorf("batch did not retain evidence")
					return
				case <-tick.C:
				}
			}
		})
	}
	if s.waitErr != nil {
		return 0, s.waitErr
	}
	return s.stateBoundsStream.Read(p)
}

func TestStateImportAsyncWorkerFailureKeepsOtherBatches(t *testing.T) {
	g, _, _, _, _, _ := setupTestGossip(t)
	fee, atts := stateSyncSignedFee(t, 790, 1)
	valid := common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}}
	malformed := common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{}}}
	for _, batches := range [][]common.QuorumState{{malformed, valid}, {valid, malformed}} {
		frames := append([][]byte{append([]byte{prefixBatchBegin[0]}, stateBoundsCount(2)...)}, stateImportFrames(t, batches...)...)
		stream := newStateBoundsStream("peer", frames...)
		result := g.askStreamForState(stream)
		require.False(t, result.completed, "worker failure must survive a complete wire exchange")
		require.ErrorContains(t, result.err, "network fee[0] or payload is nil")
		require.Equal(t, 1, result.items, "other workers still retain evidence, including duplicates")
		require.EqualValues(t, 1, stream.closes.Load())
		require.Zero(t, stream.resets.Load())
		require.Contains(t, ParseMessages(stream.output.Bytes()), []byte(p2p.StreamMsgDone))
	}
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
}

func TestStateImportIncompleteBatchSequenceRetries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames [][]byte
	}{
		{"early end", [][]byte{prefixBatchEnd}},
		{"missing batch", [][]byte{stateBoundsHeader(0, 0), prefixBatchData, prefixBatchEnd}},
		{"out of order", [][]byte{stateBoundsHeader(1, 0)}},
		{"duplicate batch", [][]byte{stateBoundsHeader(0, 0), prefixBatchData, stateBoundsHeader(0, 0)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := startupRetryGossip(t, "peer")
			frames := append([][]byte{append([]byte{prefixBatchBegin[0]}, stateBoundsCount(2)...)}, tc.frames...)
			stream := newStateBoundsStream("peer", frames...)
			result := g.askStreamForState(stream)
			require.False(t, result.completed)
			require.Error(t, result.err)
			require.EqualValues(t, 1, stream.resets.Load())
			require.NotContains(t, ParseMessages(stream.output.Bytes()), []byte(p2p.StreamMsgDone))
		})
	}
}

func TestStateImportRetainsLegacySignatureCounts(t *testing.T) {
	g, _ := startupRetryGossip(t, "peer")
	fee, atts := stateSyncSignedFee(t, 791, 129)
	stateSyncTrustAttestors(t, g, atts)
	data, err := (&common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: atts}}}).Marshal()
	require.NoError(t, err)
	result := g.askStreamForState(stateSyncImportStream(t, "peer", data))
	require.NoError(t, result.err)
	require.True(t, result.completed)
	require.Equal(t, 1, result.items)
	require.Equal(t, 129, g.networkFees[*fee].AttestationCount(), "do not pull the later receiver's 128-share limit into this release")
}
