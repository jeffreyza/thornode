package observer

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"

	"github.com/rs/zerolog"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Only these tests need the optional query capability. Legacy mocks retain
// their original blocking API and cannot accidentally mask a missing adapter.
type cancellationQueryBridge struct {
	*MockThorchainBridge
	vault   func(context.Context, string) (thortypes.Vault, error)
	keysign func(context.Context, int64, string) (tctypes.TxOut, error)
	voter   func(context.Context, string) (*thortypes.QueryObservedTxVoter, error)
	pending func(context.Context, int64) ([]thorclient.PendingOutbound, error)
}

// Cancel at the first context check after authorization reserves an identity.
// Err runs on the processing goroutine; no concurrent map reader or timer is
// needed to place cancellation precisely before the launch boundary.
type cancelReservedContext struct {
	context.Context
	cancel       context.CancelFunc
	gossip       *AttestationGossip
	key          xmrSpentRefsConflictKey
	beforeCancel func()
}

func (c cancelReservedContext) Err() error {
	c.gossip.authorizedXMRSpentRefsMu.Lock()
	reserved := c.gossip.authorizedXMRSpentRefs[c.key] != nil
	c.gossip.authorizedXMRSpentRefsMu.Unlock()
	if reserved {
		if c.beforeCancel != nil {
			c.beforeCancel()
		}
		c.cancel()
	}
	return c.Context.Err()
}

func cancellationTestReservationCount(s *AttestationGossip) int {
	s.authorizedXMRSpentRefsMu.Lock()
	defer s.authorizedXMRSpentRefsMu.Unlock()
	return len(s.authorizedXMRSpentRefs)
}

func TestStateCancellationReleasesPreLaunchReservation(t *testing.T) {
	s, _, _, _, legacy, _ := setupTestGossip(t)
	tx, plan := observerTestXMRPlan(t)
	key := secp256k1.GenPrivKey()
	legacy.getVaultFunc = observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
	legacy.getKeysignFunc = observerTestKeysignFunc(plan)
	s.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy}
	var calls atomic.Int32
	applied := make(chan struct{}, 1)
	s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) { calls.Add(1); outcome(true); applied <- struct{}{} })
	batch := signedCancellationBatch(t, tx, key)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reservedCtx := cancelReservedContext{Context: ctx, cancel: cancel, gossip: s, key: xmrSpentRefsConflictKeyFor(tx)}
	require.ErrorIs(t, s.processAttestationStateBatchWithContext(reservedCtx, s.logger, batch), context.Canceled)
	require.Zero(t, calls.Load())
	require.Zero(t, cancellationTestReservationCount(s), "pre-launch cancellation is not an import failure")
	require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
	waitStateBounds(t, applied)
	require.Equal(t, int32(1), calls.Load())
}

func TestStateCancellationReleasesReservationWhileGlobalLocked(t *testing.T) {
	s, _, _, _, legacy, _ := setupTestGossip(t)
	tx, plan := observerTestXMRPlan(t)
	key := secp256k1.GenPrivKey()
	legacy.getVaultFunc = observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
	legacy.getKeysignFunc = observerTestKeysignFunc(plan)
	s.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy}
	var calls atomic.Int32
	applied := make(chan struct{}, 1)
	s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) {
		calls.Add(1)
		outcome(true)
		applied <- struct{}{}
	})
	batch := signedCancellationBatch(t, tx, key)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var lockOnce, unlockOnce sync.Once
	reservedCtx := cancelReservedContext{
		Context: ctx, cancel: cancel, gossip: s, key: xmrSpentRefsConflictKeyFor(tx),
		// Model maintenance acquiring the global mutex after authorization
		// reserved the import, before the handler rechecks the live state.
		beforeCancel: func() { lockOnce.Do(s.mu.Lock) },
	}
	done := make(chan error, 1)
	go func() { done <- s.processAttestationStateBatchWithContext(reservedCtx, s.logger, batch) }()
	waitStateBounds(t, ctx.Done()) // Cancellation follows global lock acquisition.
	unlock := func() { unlockOnce.Do(s.mu.Unlock) }
	defer unlock()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		// Release the simulated holder and join the worker even when the
		// regression fails, so the test does not leave blocked work behind.
		unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("state processing did not return after unlocking")
		}
		t.Fatal("cancellation cleanup waited for the global mutex")
	}
	require.Zero(t, calls.Load(), "cancellation must not launch an import")
	require.Zero(t, cancellationTestReservationCount(s), "cleanup must finish while the global mutex is held")
	unlock()
	require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
	waitStateBounds(t, applied)
	require.Equal(t, int32(1), calls.Load(), "the released reservation must allow a later retry")
}

func TestStateCancellationInterruptsStreamReadAndWrite(t *testing.T) {
	for _, phase := range []string{"read", "write"} {
		t.Run(phase, func(t *testing.T) {
			s := newStateBoundsGossip()
			local, remote := net.Pipe()
			defer remote.Close()
			defer local.Close()
			stream := &stateBoundsPipeStream{MockStream: MockStream{peer: "remote", mu: new(sync.Mutex)}, pipe: local}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { s.askStreamForStateWithContext(ctx, stream); close(done) }()
			if phase == "read" {
				// Completing the request write places the owner in its response read.
				buf, err := p2p.ReadStreamWithBufferLimit(&stateBoundsPipeStream{pipe: remote}, 1)
				require.NoError(t, err)
				require.Equal(t, prefixSendState, buf)
			}
			cancel()
			waitStateBounds(t, done)
		})
	}
}

func TestStateCancellationGRPCThenRetry(t *testing.T) {
	s, _, _, client, _, _ := setupTestGossip(t)
	fee := &common.NetworkFee{Chain: common.BTCChain, Height: 100, TransactionSize: 1, TransactionRate: 1}
	key := secp256k1.GenPrivKey()
	s.setActiveValidators(common.PubKeys{observerTestPubKey(t, key)})
	payload, err := fee.GetSignablePayload()
	require.NoError(t, err)
	sig, err := key.Sign(payload)
	require.NoError(t, err)
	batch := common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: sig}}}}}
	started := make(chan struct{})
	client.sendQuorumNetworkFeeFunc = func(ctx context.Context, _ *common.QuorumNetworkFee, _ ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.processAttestationStateBatchWithContext(ctx, s.logger, batch) }()
	waitStateBounds(t, started)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("gRPC ignored cancellation")
	}
	var sent int
	client.sendQuorumNetworkFeeFunc = func(context.Context, *common.QuorumNetworkFee, ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
		sent++
		return &ebifrost.SendQuorumNetworkFeeResult{}, nil
	}
	require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
	require.Equal(t, 1, sent, "failed submission must remain unsent")
}

func TestStateCancellationSuccessfulTransferKeepsDeferredPriceFeed(t *testing.T) {
	s, _, _, client, _, _ := setupTestGossip(t)
	s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
	s.priceFeedsPool = NewAttestationStatePool[*common.PriceFeed]()
	s.priceFeedsDelay = NewDelay()
	price := &common.PriceFeed{Time: 100, Rates: []*common.OraclePrice{{Amount: 1, Decimals: 8}}, Version: make([]byte, 8)}
	key := secp256k1.GenPrivKey()
	payload, err := price.GetSignablePayload()
	require.NoError(t, err)
	sig, err := key.Sign(payload)
	require.NoError(t, err)
	batch := common.QuorumState{QuoPriceFeeds: []*common.QuorumPriceFeed{{PriceFeed: price, Attestations: []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: sig}}}}}
	sent := make(chan struct{}, 1)
	client.sendQuorumPriceFeedBatchFunc = func(ctx context.Context, batch *common.QuorumPriceFeedBatch, _ ...grpc.CallOption) (*ebifrost.SendQuorumPriceFeedBatchResult, error) {
		require.NoError(t, ctx.Err())
		require.Len(t, batch.QuorumPriceFeeds, 1)
		sent <- struct{}{}
		return &ebifrost.SendQuorumPriceFeedBatchResult{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.processAttestationStateBatchWithContext(ctx, s.logger, batch))
	cancel()
	waitStateBounds(t, sent)
	require.Eventually(t, func() bool { return !s.priceFeedsDelay.IsRunning() }, time.Second, time.Millisecond)
}

func (b *cancellationQueryBridge) GetVaultWithContext(ctx context.Context, key string) (thortypes.Vault, error) {
	if b.vault != nil {
		return b.vault(ctx, key)
	}
	return b.GetVault(key)
}

func (b *cancellationQueryBridge) GetKeysignWithContext(ctx context.Context, height int64, key string) (tctypes.TxOut, error) {
	if b.keysign != nil {
		return b.keysign(ctx, height, key)
	}
	return b.GetKeysign(height, key)
}

func (b *cancellationQueryBridge) GetPendingOutboundsWithContext(ctx context.Context, height int64) ([]thorclient.PendingOutbound, error) {
	if b.pending != nil {
		return b.pending(ctx, height)
	}
	return b.GetPendingOutbounds(height)
}

func (b *cancellationQueryBridge) GetObservedTxVoterWithContext(ctx context.Context, id string) (*thortypes.QueryObservedTxVoter, error) {
	if b.voter != nil {
		return b.voter(ctx, id)
	}
	return b.GetObservedTxVoter(id)
}

func signedCancellationBatch(t *testing.T, tx common.ObservedTx, keys ...*secp256k1.PrivKey) common.QuorumState {
	t.Helper()
	payload, err := tx.GetSignablePayloadWithInbound(false)
	require.NoError(t, err)
	q := &common.QuorumTx{ObsTx: tx}
	for _, key := range keys {
		sig, err := key.Sign(payload)
		require.NoError(t, err)
		q.Attestations = append(q.Attestations, &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: sig})
	}
	return common.QuorumState{QuoTxs: []*common.QuorumTx{q}}
}

func TestStateCancellationStopsVaultQueryAndStream(t *testing.T) {
	s, host, _, _, legacy, _ := setupTestGossip(t)
	s.config.AskPeers = 1
	s.config.StateSyncTimeout = 100 * time.Millisecond
	s.avMu.Lock()
	s.activeVals = map[peer.ID]bool{host.ID(): true, host.peers[1]: true}
	s.avMu.Unlock()
	s.SetObserverApplySpentOutputRefs(func(spentOutputRefsImport, func(bool)) { t.Error("canceled query launched import") })
	started, exited := make(chan struct{}), make(chan struct{})
	releaseLegacy := make(chan struct{})
	t.Cleanup(func() { close(releaseLegacy) })
	legacy.getVaultFunc = func(string) (thortypes.Vault, error) {
		close(started)
		<-releaseLegacy
		return thortypes.Vault{}, nil
	}
	s.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(ctx context.Context, _ string) (thortypes.Vault, error) {
		close(started)
		<-ctx.Done()
		close(exited)
		return thortypes.Vault{}, ctx.Err()
	}}
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	data, err := batch.Marshal()
	require.NoError(t, err)
	stream := newStateBoundsStream(host.peers[1], append([]byte{prefixBatchBegin[0]}, stateBoundsCount(1)...), stateBoundsHeader(0, uint32(len(data))), append([]byte{prefixBatchData[0]}, data...), prefixBatchEnd)
	host.streamFunc = func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error) { return stream, nil }
	done := make(chan struct{})
	go func() { s.askForAttestationState(context.Background(), host.peers[1]); close(done) }()
	waitStateBounds(t, started)
	waitStateBounds(t, done)
	waitStateBounds(t, exited)
	require.Positive(t, stream.resets.Load())
	require.Zero(t, stream.closes.Load())
	require.Zero(t, cancellationTestReservationCount(s))
}

func TestStateCancellationIncludesStreamCreation(t *testing.T) {
	s, host, _, _, _, _ := setupTestGossip(t)
	s.config.StateSyncTimeout = 30 * time.Millisecond
	exited := make(chan struct{})
	host.streamFunc = func(ctx context.Context, _ peer.ID, _ ...protocol.ID) (network.Stream, error) {
		<-ctx.Done()
		close(exited)
		return nil, ctx.Err()
	}
	done := make(chan struct{})
	go func() { s.askPeerForState(context.Background(), host.peers[1]); close(done) }()
	waitStateBounds(t, done)
	waitStateBounds(t, exited)
}

func TestStateCancellationReleasesExportAdmissionWhileStateLocked(t *testing.T) {
	s, host, _, _, _, _ := setupTestGossip(t)
	s.config.StateSyncTimeout = 30 * time.Millisecond
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := newStateBoundsStream(host.peers[1], prefixSendState)
	done := make(chan struct{})
	go func() { s.handleStreamAttestationState(stream); close(done) }()
	waitStateBounds(t, done)
	require.Zero(t, stateSyncExportCount(s))
	// A cancelled lock waiter can exit and disarm the asynchronous reset
	// before it runs. Both cleanup paths must close the stream and free the slot.
	require.Positive(t, stream.resets.Load()+stream.closes.Load())
}

func TestStateCancellationLatePlanThenSuccessfulCaller(t *testing.T) {
	for _, kind := range []string{"keysign", "voter", "ragnarok queue", "ragnarok retry"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _, legacy, _ := setupTestGossip(t)
			tx, plan := observerTestXMRPlan(t)
			key := secp256k1.GenPrivKey()
			legacy.getVaultFunc = observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
			started, exited := make(chan struct{}), make(chan struct{})
			bridge := &cancellationQueryBridge{MockThorchainBridge: legacy}
			wait := func(ctx context.Context) { close(started); <-ctx.Done(); close(exited) }
			switch kind {
			case "keysign":
				legacy.getKeysignFunc = observerTestKeysignFunc(plan)
				bridge.keysign = func(ctx context.Context, h int64, k string) (tctypes.TxOut, error) {
					wait(ctx)
					return legacy.GetKeysign(h, k)
				}
			case "voter":
				inbound := common.TxID(observerTestHash("cancellation-inbound"))
				tx.Tx.Memo = "OUT:" + inbound.String()
				plan.memo = tx.Tx.Memo
				legacy.getObservedTxVoterFunc = observerTestObservedTxVoterFunc(inbound, plan)
				bridge.voter = func(ctx context.Context, id string) (*thortypes.QueryObservedTxVoter, error) {
					wait(ctx)
					return legacy.GetObservedTxVoter(id)
				}
			case "ragnarok queue", "ragnarok retry":
				const originalHeight, retryHeight = int64(123), int64(1040)
				tx.Tx.Memo = "RAGNAROK:123"
				plan.memo, plan.wireMemo = tx.Tx.Memo, tx.Tx.Memo
				original := plan
				original.outHash = common.BlankTxID
				legacy.getKeysignFunc = func(height int64, key string) (tctypes.TxOut, error) {
					if height == originalHeight {
						return observerTestKeysignFunc(original)(height, key)
					}
					require.Equal(t, retryHeight, height)
					return observerTestKeysignFunc(plan)(height, key)
				}
				legacy.getPendingOutboundsFunc = func(height int64) ([]thorclient.PendingOutbound, error) {
					require.Zero(t, height)
					return []thorclient.PendingOutbound{{Chain: "XMR", Memo: tx.Tx.Memo, Height: retryHeight}}, nil
				}
				if kind == "ragnarok queue" {
					bridge.pending = func(ctx context.Context, height int64) ([]thorclient.PendingOutbound, error) {
						wait(ctx)
						return legacy.GetPendingOutbounds(height)
					}
				} else {
					bridge.keysign = func(ctx context.Context, height int64, key string) (tctypes.TxOut, error) {
						if height == retryHeight {
							wait(ctx)
						}
						return legacy.GetKeysign(height, key)
					}
				}
			}
			s.bridge = bridge
			var calls atomic.Int32
			applied := make(chan spentOutputRefsImport, 1)
			s.SetObserverApplySpentOutputRefs(func(request spentOutputRefsImport, outcome func(bool)) {
				calls.Add(1)
				outcome(true)
				applied <- request
			})
			batch := signedCancellationBatch(t, tx, key)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- s.processAttestationStateBatchWithContext(ctx, s.logger, batch) }()
			waitStateBounds(t, started)
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(time.Second):
				t.Fatal("query ignored cancellation")
			}
			waitStateBounds(t, exited)
			require.Zero(t, calls.Load())
			require.Zero(t, cancellationTestReservationCount(s))
			bridge.keysign, bridge.voter, bridge.pending = nil, nil, nil
			require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
			select {
			case request := <-applied:
				// Context query paths must preserve the wire memo derived
				// from OriginalMemo when the planned Memo is empty.
				require.Equal(t, tx.Tx.Memo, request.Tx.Tx.Memo)
				require.Equal(t, tx.Tx.Memo, request.WireMemo)
				require.True(t, request.ReplaySource)
				require.False(t, request.ChainCommitted)
			case <-time.After(time.Second):
				t.Fatal("retry did not import spent refs")
			}
			require.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestStateCancellationRecountsGrowingCertificate(t *testing.T) {
	s, _, _, _, legacy, _ := setupTestGossip(t)
	tx, plan := observerTestXMRPlan(t)
	keys := []*secp256k1.PrivKey{secp256k1.GenPrivKey(), secp256k1.GenPrivKey(), secp256k1.GenPrivKey()}
	members := common.PubKeys{observerTestPubKey(t, keys[0]), observerTestPubKey(t, keys[1]), observerTestPubKey(t, keys[2])}
	// The same raw vault response is reused for each accumulated certificate.
	vault, err := observerTestVaultFunc(tx.ObservedPubKey, members...)(tx.ObservedPubKey.String())
	require.NoError(t, err)
	var queries int
	s.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(context.Context, string) (thortypes.Vault, error) { queries++; return vault, nil }}
	legacy.getKeysignFunc = observerTestKeysignFunc(plan)
	applied := make(chan struct{}, 1)
	s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) { outcome(true); applied <- struct{}{} })
	batch := signedCancellationBatch(t, tx, keys...)
	require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
	waitStateBounds(t, applied)
	require.Equal(t, 2, queries, "second attestation must recompute and reach 2-of-3 quorum; subsequent import is already reserved")
}

func TestStateCancellationBeforeWorkAndWhileLocked(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, s.processAttestationStateBatchWithContext(ctx, s.logger, batch), context.Canceled)
	require.Empty(t, s.observedTxs)
	for _, lock := range []string{"global", "state"} {
		t.Run(lock, func(t *testing.T) {
			mu := &s.mu
			if lock == "state" {
				s.handleObservedTxAttestation(context.Background(), common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]})
				mu = &s.observedTxs[observedTxKey(tx, false, false)].mu
			}
			mu.Lock()
			defer mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, s.processAttestationStateBatchWithContext(ctx, s.logger, batch), context.DeadlineExceeded)
			if lock == "state" {
				require.True(t, s.mu.TryLock(), "canceled state waiter retained global lock")
				s.mu.Unlock()
			}
		})
	}
}

func TestStateCancellationReservationReleaseIsRetryable(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	key, identity := xmrSpentRefsConflictKeyFor(tx), xmrSpentRefsIdentity(tx.SpentOutputRefs)
	require.True(t, s.rememberAuthorizedXMRSpentRefs(tx))
	s.releaseAuthorizedXMRSpentRefs(key, identity+"different")
	require.Equal(t, 1, cancellationTestReservationCount(s))
	s.releaseAuthorizedXMRSpentRefs(key, identity)
	require.Zero(t, cancellationTestReservationCount(s))
	require.True(t, s.rememberAuthorizedXMRSpentRefs(tx))
	s.resolveAuthorizedXMRSpentRefs(key, identity, true)
	s.releaseAuthorizedXMRSpentRefs(key, identity)
	state, exists := observerTestConflictState(s, tx)
	require.True(t, exists)
	require.Equal(t, xmrSpentRefsImportConfirmed, state)
}

func TestStateCancellationLockLeavesNoWorkerAndLegacyBlocks(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, lockAttestationContext(ctx, &mu), context.Canceled)
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		_ = lockAttestationContext(context.Background(), &mu)
		mu.Unlock()
		close(done)
	}()
	waitStateBounds(t, started)
	select {
	case <-done:
		t.Fatal("legacy waiter did not block")
	default:
	}
	mu.Unlock()
	waitStateBounds(t, done)
	require.True(t, mu.TryLock(), "returned canceled caller later acquired lock")
	mu.Unlock()
}

// This stream preserves unread bytes and exposes the precise preflight read
// boundary. A blocked read is released only by reset, like a stopped transport.
type stateBoundsStream struct {
	MockStream
	input     *bytes.Reader
	frameEnds []int64
	output    bytes.Buffer
	readBytes atomic.Int64
	resets    atomic.Int32
	closes    atomic.Int32
	block     bool
	blocked   chan struct{}
	stopped   chan struct{}
	blockOnce sync.Once
	stopOnce  sync.Once
}

func newStateBoundsStream(remote peer.ID, frames ...[]byte) *stateBoundsStream {
	var input []byte
	var ends []int64
	for _, frame := range frames {
		input = append(input, encodeStreamMsg(frame)...)
		ends = append(ends, int64(len(input)))
	}
	return &stateBoundsStream{
		MockStream: MockStream{peer: remote, mu: new(sync.Mutex)},
		input:      bytes.NewReader(input), frameEnds: ends, blocked: make(chan struct{}), stopped: make(chan struct{}),
	}
}

func (s *stateBoundsStream) Read(p []byte) (int, error) {
	if s.block && s.input.Len() == 0 {
		s.blockOnce.Do(func() { close(s.blocked) })
		<-s.stopped
		return 0, io.ErrClosedPipe
	}
	// Older readers use a fresh bufio.Reader per frame. Model the handshake
	// by exposing one frame at a time, as a sender waiting for an ACK does.
	read := s.readBytes.Load()
	for len(s.frameEnds) > 0 && s.frameEnds[0] <= read {
		s.frameEnds = s.frameEnds[1:]
	}
	if len(s.frameEnds) > 0 {
		p = p[:min(len(p), int(s.frameEnds[0]-read))]
	}
	n, err := s.input.Read(p)
	s.readBytes.Add(int64(n))
	return n, err
}

func (s *stateBoundsStream) Write(p []byte) (int, error) { return s.output.Write(p) }
func (s *stateBoundsStream) Reset() error {
	s.resets.Add(1)
	s.stopOnce.Do(func() { close(s.stopped) })
	return nil
}
func (s *stateBoundsStream) Close() error                     { s.closes.Add(1); return nil }
func (s *stateBoundsStream) SetReadDeadline(time.Time) error  { return nil }
func (s *stateBoundsStream) SetWriteDeadline(time.Time) error { return nil }

func stateBoundsCount(n uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, n)
	return b
}

func stateBoundsHeader(n, size uint32) []byte {
	return append(append([]byte{prefixBatchHeader[0]}, stateBoundsCount(n)...), stateBoundsCount(size)...)
}

func newStateBoundsGossip(peers ...peer.ID) *AttestationGossip {
	s := &AttestationGossip{logger: zerolog.Nop(), activeVals: make(map[peer.ID]bool)}
	for _, p := range peers {
		s.activeVals[p] = true
	}
	return s
}

type stateBoundsPipeStream struct {
	MockStream
	pipe net.Conn
}

func (s *stateBoundsPipeStream) Read(p []byte) (int, error)  { return s.pipe.Read(p) }
func (s *stateBoundsPipeStream) Write(p []byte) (int, error) { return s.pipe.Write(p) }
func (s *stateBoundsPipeStream) Reset() error                { return s.pipe.Close() }
func (s *stateBoundsPipeStream) Close() error                { return s.pipe.Close() }

func waitStateBounds(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("state handler did not finish")
	}
}

func stateSyncSignedFee(t *testing.T, height int64, count int) (*common.NetworkFee, []*common.Attestation) {
	t.Helper()
	fee := &common.NetworkFee{Chain: common.BTCChain, Height: height, TransactionSize: 100, TransactionRate: 2}
	payload, err := fee.GetSignablePayload()
	require.NoError(t, err)
	atts := make([]*common.Attestation, count)
	for i := range atts {
		key := secp256k1.GenPrivKey()
		sig, err := key.Sign(payload)
		require.NoError(t, err)
		atts[i] = &common.Attestation{PubKey: key.PubKey().Bytes(), Signature: sig}
	}
	return fee, atts
}

// Include BatchBegin so imports exercise the requester's reset/close decision.

func TestStateCancellationVersionedPriceFeedRoundTrip(t *testing.T) {
	sender, _, _, _, _, _ := setupTestGossip(t)
	receiver, _, _, _, _, _ := setupTestGossip(t)
	for _, s := range []*AttestationGossip{sender, receiver} {
		s.logger = zerolog.Nop()
		s.config.StateSyncTimeout = 2 * time.Second
		s.priceFeeds = make(map[string]*AttestationState[*common.PriceFeed])
		s.priceFeedsPool = NewAttestationStatePool[*common.PriceFeed]()
		s.priceFeedsDelay = NewDelay()
		// Keep unrelated deferred THORNode delivery out of the transfer test.
		s.priceFeedsDelay.Start()
		defer s.priceFeedsDelay.Done()
	}
	sender.activeVals["receiver"] = true
	feed := &common.PriceFeed{
		Time:    123,
		Rates:   []*common.OraclePrice{{Amount: 1, Decimals: 8}},
		Version: []byte("12345678"),
	}
	require.NoError(t, feed.Valid())
	payload, err := feed.GetSignablePayload()
	require.NoError(t, err)
	signature, err := sender.privKey.Sign(payload)
	require.NoError(t, err)
	att := &common.Attestation{PubKey: sender.pubKey, Signature: signature}
	require.NoError(t, verifySignature(payload, att.Signature, att.PubKey))
	stateSyncTrustAttestors(t, receiver, []*common.Attestation{att})
	sender.handlePriceFeedAttestation(context.Background(), common.AttestPriceFeed{PriceFeed: feed, Attestation: att})
	require.Len(t, sender.priceFeeds, 1, "the sender must retain the signed versioned feed before exporting")

	server, client := stateSyncDeadlinePair(t)
	done := make(chan struct{})
	go func() { sender.handleStreamAttestationState(server); close(done) }()
	require.True(t, receiver.askStreamForState(client).completed)
	waitStateBounds(t, done)
	require.Zero(t, server.resets.Load(), "the exporter must complete its handshake")
	require.Zero(t, client.resets.Load(), "the requester must complete its handshake")
	require.EqualValues(t, 1, server.closes.Load())
	require.EqualValues(t, 1, client.closes.Load())
	require.Zero(t, stateSyncExportCount(sender))

	require.Len(t, receiver.priceFeeds, 1, "state sync must retain a correctly signed versioned price feed")
	retained := receiver.priceFeeds[fmt.Sprintf("%x", att.PubKey)]
	require.NotNil(t, retained)
	require.NoError(t, retained.Item.Valid())
	require.Equal(t, feed.Version, retained.Item.Version)
	importedPayload, err := retained.Item.GetSignablePayload()
	require.NoError(t, err)
	require.Equal(t, payload, importedPayload, "export must preserve every signed field")
	require.Equal(t, []*common.Attestation{att}, retained.AttestationsCopy())
	require.NoError(t, verifySignature(importedPayload, att.Signature, att.PubKey))
}

func stateSyncTrustAttestors(t *testing.T, s *AttestationGossip, atts []*common.Attestation) {
	t.Helper()
	for _, att := range atts {
		id, err := conversion.GetPeerIDFromSecp256PubKey(att.PubKey)
		require.NoError(t, err)
		s.activeVals[id] = true
	}
}

func stateBoundsPipe(t *testing.T, first, second peer.ID) (*stateBoundsPipeStream, *stateBoundsPipeStream) {
	t.Helper()
	a, b := net.Pipe()
	require.NoError(t, a.SetDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, b.SetDeadline(time.Now().Add(5*time.Second)))
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return &stateBoundsPipeStream{MockStream: MockStream{peer: first}, pipe: a}, &stateBoundsPipeStream{MockStream: MockStream{peer: second}, pipe: b}
}

func stateSyncImportStream(t *testing.T, remote peer.ID, payloads ...[]byte) *stateBoundsStream {
	t.Helper()
	frames := [][]byte{append([]byte{prefixBatchBegin[0]}, stateBoundsCount(uint32(len(payloads)))...)}
	for i, data := range payloads {
		frames = append(frames, stateBoundsHeader(uint32(i), uint32(len(data))), append([]byte{prefixBatchData[0]}, data...))
	}
	return newStateBoundsStream(remote, append(frames, prefixBatchEnd)...)
}
