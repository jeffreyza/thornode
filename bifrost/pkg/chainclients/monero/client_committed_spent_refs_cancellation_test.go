package monero

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestApplyCommittedSpentOutputRefsCancelsBlockedRefsQuery(t *testing.T) {
	t.Parallel()

	vault, _, txHash, _, _ := testXMRCatchupMigration(t, "cancel-blocked-refs")
	entered := make(chan struct{})
	abort := make(chan struct{})
	defer close(abort)
	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sidecarCalls.Add(1)
		http.Error(w, "canceled lookup must not reach sidecar", http.StatusBadRequest)
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	var voterCalls atomic.Int32
	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsFreshFunc: func(ctx context.Context, _ common.PubKey) ([]*common.OutputRef, error) {
				close(entered)
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-abort:
					return nil, context.Canceled
				}
			},
			spentRefsFunc: func(common.PubKey) ([]*common.OutputRef, error) {
				t.Error("live import used the cached refs lookup")
				return nil, context.Canceled
			},
			observedFreshFunc: func(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error) {
				voterCalls.Add(1)
				return nil, false, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan bool, 1)
	go func() {
		allowSpentOutputRefsImports(c)
		_, applied := c.ApplyCommittedSpentOutputRefs(ctx, vault, common.TxID(txHash))
		outcome <- applied
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("fresh refs request did not start")
	}
	cancel()
	select {
	case applied := <-outcome:
		assert.False(t, applied)
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not release the blocked refs request")
	}
	assert.Zero(t, voterCalls.Load())
	assert.Zero(t, sidecarCalls.Load())
}

func TestApplyCommittedSpentOutputRefsCancellationDuringVoterLookupSkipsMarkSpent(t *testing.T) {
	t.Parallel()

	vault, _, txHash, voter, refs := testXMRCatchupMigration(t, "cancel-during-voter")
	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sidecarCalls.Add(1)
		http.Error(w, "canceled lookup must not reach sidecar", http.StatusBadRequest)
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var voterCalls atomic.Int32
	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedFreshFunc: func(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error) {
				voterCalls.Add(1)
				cancel()
				return voter, true, nil
			},
		},
	}

	allowSpentOutputRefsImports(c)
	_, applied := c.ApplyCommittedSpentOutputRefs(ctx, vault, common.TxID(txHash))
	assert.False(t, applied)
	assert.EqualValues(t, 1, voterCalls.Load())
	assert.Zero(t, sidecarCalls.Load(), "valid voter metadata returned after cancellation must not start MarkSpent")
}

func TestApplyCommittedSpentOutputRefsOnceCanceledContextSkipsVoterLookup(t *testing.T) {
	t.Parallel()

	vault, _, txHash, voter, pointerRefs := testXMRCatchupMigration(t, "cancel-before-voter")
	refs := xmrOutputRefsFromPointers(pointerRefs)
	var voterCalls int
	c := &Client{
		logger: testLogger(),
		thorchainBridge: &mockMoneroBridge{
			// The hook deliberately counts even a canceled context. Otherwise the
			// mock could hide an unnecessary voter lookup by rejecting it itself.
			observedFreshFunc: func(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error) {
				voterCalls++
				return voter, true, nil
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, applied, retry := c.applyCommittedSpentOutputRefsOnce(ctx, vault, common.TxID(txHash), refs, true)
	assert.False(t, applied)
	assert.False(t, retry)
	assert.Zero(t, voterCalls, "cancellation must prevent starting a new voter request")
}

func TestApplyCommittedSpentOutputRefsCancelsBlockedVoterQuery(t *testing.T) {
	t.Parallel()

	vault, _, txHash, _, refs := testXMRCatchupMigration(t, "cancel-blocked-voter")
	entered := make(chan struct{})
	abort := make(chan struct{})
	defer close(abort)
	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sidecarCalls.Add(1)
		http.Error(w, "canceled lookup must not reach sidecar", http.StatusBadRequest)
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedFreshFunc: func(ctx context.Context, _ string) (*ttypes.QueryObservedTxVoter, bool, error) {
				close(entered)
				select {
				case <-ctx.Done():
					return nil, false, ctx.Err()
				case <-abort:
					return nil, false, context.Canceled
				}
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan bool, 1)
	go func() {
		allowSpentOutputRefsImports(c)
		_, applied := c.ApplyCommittedSpentOutputRefs(ctx, vault, common.TxID(txHash))
		outcome <- applied
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("fresh voter request did not start")
	}
	cancel()
	select {
	case applied := <-outcome:
		assert.False(t, applied)
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not release the blocked voter request")
	}
	assert.Zero(t, sidecarCalls.Load())
}

func TestCommittedSpentOutputRefsLookupLogLevels(t *testing.T) {
	t.Parallel()

	vault, _, txHash, _, pointerRefs := testXMRCatchupMigration(t, "lookup-log-levels")
	refs := xmrOutputRefsFromPointers(pointerRefs)
	transportErr := errors.New("lookup transport failed")
	for _, lookup := range []string{"refs", "voter"} {
		for _, tc := range []struct {
			name           string
			cancelCaller   bool
			callerDeadline bool
			lookupErr      error
			wantLevel      zerolog.Level
		}{
			{"caller cancellation", true, false, errors.Join(transportErr, context.Canceled), zerolog.DebugLevel},
			{"request deadline", false, false, context.DeadlineExceeded, zerolog.WarnLevel},
			{"caller deadline", false, true, context.DeadlineExceeded, zerolog.WarnLevel},
			{"transport failure", false, false, transportErr, zerolog.WarnLevel},
			{"transport failure with canceled caller", true, false, transportErr, zerolog.WarnLevel},
		} {
			t.Run(lookup+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				if tc.callerDeadline {
					cancel()
					ctx, cancel = context.WithDeadline(context.Background(), time.Unix(0, 0))
				}
				defer cancel()
				if tc.cancelCaller {
					cancel()
				}
				var levels []zerolog.Level
				logger := zerolog.New(io.Discard).Level(zerolog.DebugLevel).Hook(zerolog.HookFunc(
					func(_ *zerolog.Event, level zerolog.Level, _ string) { levels = append(levels, level) },
				))
				c := &Client{
					logger:  logger,
					sidecar: &sidecar.Client{},
					thorchainBridge: &mockMoneroBridge{
						spentRefsFreshFunc: func(context.Context, common.PubKey) ([]*common.OutputRef, error) {
							return nil, tc.lookupErr
						},
						observedFreshFunc: func(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error) {
							return nil, false, tc.lookupErr
						},
					},
				}
				if lookup == "refs" {
					_, applied, retry := c.applyCommittedSpentOutputRefsHintOnce(ctx, vault, common.TxID(txHash))
					assert.False(t, applied)
					assert.True(t, retry)
				} else {
					_, retry, ok := c.committedXMROutboundForSpentRefs(ctx, common.TxID(txHash), vault, refs, true)
					assert.False(t, ok)
					assert.True(t, retry)
				}
				assert.Equal(t, []zerolog.Level{tc.wantLevel}, levels,
					"only a lookup canceled by its caller should be demoted from warning")
			})
		}
	}
}

func TestApplyCommittedSpentOutputRefsLastAttemptCancellationDoesNotWarn(t *testing.T) {
	t.Parallel()

	vault, _, txHash, _, _ := testXMRCatchupMigration(t, "cancel-last-attempt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var levels []zerolog.Level
	logger := zerolog.New(io.Discard).Level(zerolog.DebugLevel).Hook(zerolog.HookFunc(
		func(_ *zerolog.Event, level zerolog.Level, _ string) { levels = append(levels, level) },
	))
	var refsCalls int
	c := &Client{
		logger:                 logger,
		sidecar:                &sidecar.Client{},
		spentRefsRetryInterval: time.Millisecond,
		thorchainBridge: &mockMoneroBridge{
			spentRefsFreshFunc: func(requestCtx context.Context, _ common.PubKey) ([]*common.OutputRef, error) {
				refsCalls++
				if refsCalls < defaultXMRCommittedRefsImportMaxAttempts {
					return nil, nil
				}
				cancel()
				return nil, requestCtx.Err()
			},
		},
	}

	allowSpentOutputRefsImports(c)
	_, applied := c.ApplyCommittedSpentOutputRefs(ctx, vault, common.TxID(txHash))
	assert.False(t, applied)
	assert.Equal(t, defaultXMRCommittedRefsImportMaxAttempts, refsCalls)
	assert.Contains(t, levels, zerolog.DebugLevel)
	assert.NotContains(t, levels, zerolog.WarnLevel, "caller cancellation must not be reported as exhausted retries")
}

func TestApplyCommittedSpentOutputRefsCancellationAfterMarkSpentPreservesSignerState(t *testing.T) {
	t.Parallel()

	vault, destination, txHash, voter, refs := testXMRCatchupMigration(t, "cancel-after-mark-spent")
	blockHash := testXMRHash("cancel-after-mark-spent-block")
	parentHash := testXMRHash("cancel-after-mark-spent-parent")
	entered := make(chan struct{})
	release := make(chan struct{})
	requestCanceled := make(chan struct{})
	var releaseOnce sync.Once
	releaseMarkSpent := func() { releaseOnce.Do(func() { close(release) }) }
	var markSpentCalls, locationCalls atomic.Int32
	var lastRequest atomic.Pointer[sidecar.MarkSpentReq]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-spent":
			var request sidecar.MarkSpentReq
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, "invalid mark-spent request", http.StatusBadRequest)
				return
			}
			lastRequest.Store(&request)
			if markSpentCalls.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					close(requestCanceled)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(request.SpentRefs)})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/tx/"+txHash+"/location":
			locationCalls.Add(1)
			_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
				TxHash: txHash, Height: 100, BlockHash: blockHash, PreviousHash: parentHash,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(101))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-observed":
			request := lastRequest.Load()
			if request == nil {
				http.Error(w, "outgoing record not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.Transfer{
				TxID:            request.TxHash,
				TxKey:           request.TxKey,
				Address:         request.ToAddress,
				FromAddress:     request.FromAddress,
				VaultPubKey:     request.VaultPubKey,
				Amount:          request.Amount,
				Fee:             request.Fee,
				Height:          100,
				Note:            request.Memo,
				Origin:          string(request.Origin),
				SpentOutputRefs: request.SpentRefs,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer releaseMarkSpent()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	scanner, replayStore, _ := newXMRCatchupReplayScanner(t, signer, vault, destination)
	scanner.setCurrentHeight(100)
	signerCache := newTestSignerCacheManager(t)
	payloadStore := newTestSignedTxPayloadStore(t)
	c := &Client{
		chain:                common.XMRChain,
		sidecar:              signer,
		scanner:              scanner,
		logger:               testLogger(),
		signerCacheManager:   signerCache,
		signedTxPayloadStore: payloadStore,
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{txHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
			mimirValues:         map[string]int64{"HaltScannerXMR": 0},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outcome := make(chan bool, 1)
	go func() {
		allowSpentOutputRefsImports(c)
		_, applied := c.ApplyCommittedSpentOutputRefs(ctx, vault, common.TxID(txHash))
		outcome <- applied
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("MarkSpent request did not start")
	}
	cancel()
	// Give transport cancellation time to arrive while the server still owns
	// the mutation. The import must wait for its outcome before returning.
	select {
	case <-requestCanceled:
		t.Fatal("caller cancellation interrupted an in-flight MarkSpent mutation")
	case <-outcome:
		t.Fatal("import returned before the in-flight MarkSpent mutation completed")
	case <-time.After(100 * time.Millisecond):
	}
	releaseMarkSpent()
	select {
	case applied := <-outcome:
		assert.False(t, applied, "replay enrollment remains incomplete after cancellation")
	case <-time.After(2 * time.Second):
		t.Fatal("import did not finish after MarkSpent succeeded")
	}
	assert.EqualValues(t, 1, markSpentCalls.Load())
	assert.Zero(t, locationCalls.Load(), "cancellation must prevent replay enrollment from starting")
	count, err := replayStore.Count()
	require.NoError(t, err)
	assert.Zero(t, count)

	logicalHash := xmrMigrationObserveKey(vault, voter.Tx.Tx.ToAddress.String(), voter.Tx.Tx.Memo)
	assert.True(t, signerCache.HasSigned(logicalHash), "successful MarkSpent must finish signer bookkeeping")
	reloadedStore := newSignedTxPayloadStore(payloadStore.db)
	_, _, err = reloadedStore.GetOwned("recovery-vault", "recovery-cache", logicalHash)
	assert.ErrorIs(t, err, errXMRSigningClaimObserved, "the fulfillment barrier must be durable despite cancellation")

	// Catch-up owns the deferred work and can repeat the idempotent import.
	allowSpentOutputRefsImports(c)
	c.catchupSpentOutputRefs()
	assert.EqualValues(t, 2, markSpentCalls.Load())
	assert.EqualValues(t, 1, locationCalls.Load())
	count, err = replayStore.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count, "a later catch-up must enroll the deferred migration replay")
}
