package observer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestStateCancellationPreservesOrdinaryXMRSlowAuthorization(t *testing.T) {
	for _, delay := range []time.Duration{0, 150 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			s, _, _, _, legacy, _ := setupTestGossip(t)
			s.config.PeerTimeout = 50 * time.Millisecond
			tx, plan := observerTestXMRPlan(t)
			key := secp256k1.GenPrivKey()
			vault := observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
			legacy.getVaultFunc = func(pk string) (thortypes.Vault, error) {
				// Authorization runs after the delivery critical section. The
				// delivery deadline must not discard a later successful import.
				time.Sleep(delay)
				return vault(pk)
			}
			legacy.getKeysignFunc = observerTestKeysignFunc(plan)
			imported := make(chan struct{}, 1)
			s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) {
				outcome(true)
				imported <- struct{}{}
			})
			batch := signedCancellationBatch(t, tx, key)
			s.handleObservedTxAttestation(context.Background(), common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]})
			select {
			case <-imported:
			case <-time.After(time.Second):
				t.Fatal("ordinary delivery discarded a successful authorization after its delivery deadline")
			}
			state, exists := observerTestConflictState(s, tx)
			require.True(t, exists)
			require.Equal(t, xmrSpentRefsImportConfirmed, state)
		})
	}
}

func TestStateCancellationXMRSlowAuthorizationUsesTransferDeadline(t *testing.T) {
	for _, delay := range []time.Duration{0, 150 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			s, _, _, _, legacy, _ := setupTestGossip(t)
			s.config.PeerTimeout = 50 * time.Millisecond
			tx, plan := observerTestXMRPlan(t)
			key := secp256k1.GenPrivKey()
			vault := observerTestVaultFunc(tx.ObservedPubKey, observerTestPubKey(t, key))
			legacy.getKeysignFunc = observerTestKeysignFunc(plan)
			var queryErr error
			s.bridge = &cancellationQueryBridge{MockThorchainBridge: legacy, vault: func(ctx context.Context, pk string) (thortypes.Vault, error) {
				timer := time.NewTimer(delay)
				defer timer.Stop()
				select {
				case <-timer.C:
					return vault(pk)
				case <-ctx.Done():
					queryErr = ctx.Err()
					return thortypes.Vault{}, queryErr
				}
			}}
			imported := make(chan struct{}, 1)
			s.SetObserverApplySpentOutputRefs(func(_ spentOutputRefsImport, outcome func(bool)) {
				outcome(true)
				imported <- struct{}{}
			})
			transfer, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, s.processAttestationStateBatchWithContext(transfer, s.logger, signedCancellationBatch(t, tx, key)))
			require.NoError(t, transfer.Err())
			require.NoError(t, queryErr, "authorization must use the live transfer's budget")
			select {
			case <-imported:
			case <-time.After(time.Second):
				t.Fatal("live state transfer discarded a successful authorization after the delivery deadline")
			}
			state, exists := observerTestConflictState(s, tx)
			require.True(t, exists)
			require.Equal(t, xmrSpentRefsImportConfirmed, state)
		})
	}
}

func TestStateCancellationWhileErrataBarrierLocked(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	msg := common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]}
	s.xmrErrataBarrier.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(s.xmrErrataBarrier.Unlock) }
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.handleObservedTxAttestationFromStateSync(ctx, msg)
		close(done)
	}()
	select {
	case <-done:
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	case <-time.After(time.Second):
		unlock()
		waitStateBounds(t, done)
		t.Fatal("cancelled state import waited for errata cleanup")
	}
	require.Empty(t, s.observedTxs)
	unlock()
	// The cancelled reader must neither retain the barrier nor poison a retry.
	require.NoError(t, s.processAttestationStateBatchWithContext(context.Background(), s.logger, batch))
	require.Equal(t, 1, s.observedTxs[observedTxKey(tx, false, false)].AttestationCount())
}

func TestStateCancellationWhileFrameRevalidationLocked(t *testing.T) {
	s, _, _, _, _, _ := setupTestGossip(t)
	tx, _ := observerTestXMRPlan(t)
	batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
	stream := NewAutoResponderStream("state-peer")
	s.mu.Lock()
	var unlockOnce sync.Once
	unlock := func() { unlockOnce.Do(s.mu.Unlock) }
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	done := make(chan error, 1)
	go func() { done <- s.sendAttestationStateBatchWithContext(ctx, stream, 0, batch, deadline) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("frame export did not exit after unlocking")
		}
		t.Fatal("cancelled frame export waited for the global mutex")
	}
	unlock()
	require.NoError(t, s.sendAttestationStateBatch(NewAutoResponderStream("retry-peer"), 0, batch, time.Now().Add(time.Second)))
}

func TestStateCancellationKeepsOrdinaryLocksBlocking(t *testing.T) {
	for _, kind := range []string{"network fee", "XMR observation"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, _, _, _ := setupTestGossip(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			fee, atts := stateSyncSignedFee(t, 123, 1)
			tx, _ := observerTestXMRPlan(t)
			batch := signedCancellationBatch(t, tx, secp256k1.GenPrivKey())
			s.mu.Lock()
			var unlockOnce sync.Once
			unlock := func() { unlockOnce.Do(s.mu.Unlock) }
			defer unlock()
			started, done := make(chan struct{}), make(chan struct{})
			go func() {
				close(started)
				if kind == "network fee" {
					s.handleNetworkFeeAttestation(ctx, common.AttestNetworkFee{NetworkFee: fee, Attestation: atts[0]})
				} else {
					// The XMR handler adds a timeout, which must not select the polling path.
					s.handleObservedTxAttestation(ctx, common.AttestTx{ObsTx: tx, Attestation: batch.QuoTxs[0].Attestations[0]})
				}
				close(done)
			}()
			waitStateBounds(t, started)
			select {
			case <-done:
				t.Fatal("ordinary processing abandoned its blocking lock wait")
			case <-time.After(50 * time.Millisecond):
			}
			unlock()
			waitStateBounds(t, done)
			if kind == "network fee" {
				require.NotNil(t, s.networkFees[*fee])
				require.Equal(t, 1, s.networkFees[*fee].AttestationCount())
			} else {
				require.Empty(t, s.observedTxs, "cancelled XMR delivery must not add observations after acquiring the lock")
			}
		})
	}
}

// Model an old sender: every frame ACK uses the ordinary read deadline.
// The first import remains blocked longer than that deadline while the next
// batch arrives; only the final acknowledgement waits for processing.
func TestStateCancellationAsyncReceiverACKsWhileProcessing(t *testing.T) {
	for _, finish := range []string{"success", "cancel", "transport error"} {
		t.Run(finish, func(t *testing.T) {
			stateSyncDeadlineTestSettings(t)
			receiver, _, _, rpc, _, _ := setupTestGossip(t)
			// Delayed stream cleanup can log after this subtest has finished.
			receiver.logger = zerolog.Nop()
			receiver.config.StateSyncTimeout = 10 * time.Second
			key := secp256k1.GenPrivKey()
			receiver.setActiveValidators(common.PubKeys{observerTestPubKey(t, key)})
			started, exited, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			second := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			rpc.sendQuorumNetworkFeeFunc = func(ctx context.Context, fee *common.QuorumNetworkFee, _ ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
				if fee.NetworkFee.Height == 100 {
					close(started)
					defer close(exited)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				if fee.NetworkFee.Height == 101 {
					close(second)
				}
				return &ebifrost.SendQuorumNetworkFeeResult{}, nil
			}
			server, client := stateSyncDeadlinePair(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan attestationStateTransferResult, 1)
			returned := make(chan struct{})
			go func() { defer close(returned); done <- receiver.askStreamForStateWithContext(ctx, client) }()
			t.Cleanup(func() { cancel(); unblock(); _ = server.transport.Close(); waitStateBounds(t, returned) })
			read := func(expected []byte) {
				message, err := p2p.ReadStreamWithBuffer(server)
				require.NoError(t, err, "ordinary ACK must not wait for batch processing")
				require.Equal(t, expected, message)
			}
			write := func(data []byte) { require.NoError(t, p2p.WriteStreamWithBuffer(data, server)) }
			read(prefixSendState)
			write(append([]byte{prefixBatchBegin[0]}, stateBoundsCount(2)...))
			read([]byte(streamAckBegin))
			for i := range 2 {
				fee := &common.NetworkFee{Chain: common.BTCChain, Height: int64(100 + i), TransactionSize: 1, TransactionRate: 1}
				payload, err := fee.GetSignablePayload()
				require.NoError(t, err)
				signature, err := key.Sign(payload)
				require.NoError(t, err)
				batch := common.QuorumState{QuoNetworkFees: []*common.QuorumNetworkFee{{NetworkFee: fee, Attestations: []*common.Attestation{{PubKey: key.PubKey().Bytes(), Signature: signature}}}}}
				data, err := batch.Marshal()
				require.NoError(t, err)
				write(stateBoundsHeader(uint32(i), uint32(len(data))))
				read([]byte(streamAckHeader))
				write(append([]byte{prefixBatchData[0]}, data...))
				read([]byte(streamAckData))
				if i == 0 {
					waitStateBounds(t, started)
					time.Sleep(2 * stateSyncTestFrameTimeout)
				}
			}
			select {
			case <-done:
				t.Fatal("transfer returned before joining its blocked worker")
			default:
			}
			waitStateBounds(t, second)
			switch finish {
			case "success":
				write(prefixBatchEnd)
				select {
				case <-done:
					t.Fatal("transfer completed before final batch processing")
				case <-time.After(50 * time.Millisecond):
				}
				unblock()
				read([]byte(p2p.StreamMsgDone))
			case "cancel":
				write(prefixBatchEnd)
				cancel()
			case "transport error":
				require.NoError(t, server.transport.Close())
			}
			select {
			case success := <-done:
				require.Equal(t, finish == "success", success.completed)
				require.Equal(t, 2, success.items)
			case <-time.After(2 * time.Second):
				t.Fatal("async receive did not cancel and join workers")
			}
			waitStateBounds(t, exited)
			require.Len(t, receiver.networkFees, 2, "both batches must arrive before the blocked import finishes")
			for _, bucket := range receiver.networkFees {
				require.Equal(t, 1, bucket.AttestationCount())
			}
		})
	}
}
