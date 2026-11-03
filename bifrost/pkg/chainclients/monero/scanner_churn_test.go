package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Model the selected signer's same-birthday registration and exact-last-block
// replay contract. The actual Client, BlockScanner loop and Scanner drive it.
type churnSigner struct {
	mu            sync.Mutex
	next          uint64
	epoch         uint64
	registrations int
	stateReads    int
	rewinds       []uint64
	heights       []int64
	vault         common.PubKey
	address       common.Address
	failRewind    bool
	loseRewind    bool
	loseSync      bool
	scanStarted   chan struct{}
	releaseScan   chan struct{}
}

func (s *churnSigner) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer churn-test" {
		http.Error(w, "missing signer authentication", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/v1/monero/vaults/register":
		var req sidecar.RegisterVaultReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.VaultCreationHeight != 990 {
			http.Error(w, "unexpected birthday", http.StatusBadRequest)
			return
		}
		s.registrations++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"registered": false, "effective_birthday": 990,
			"reset_epoch": s.epoch, "expected_next_height": s.next,
		})
	case r.URL.Path == "/v1/monero/scanner/state":
		s.stateReads++
		last := s.next - 1
		_ = json.NewEncoder(w).Encode(sidecar.ScannerState{
			NextHeight: &s.next, ResetEpoch: s.epoch, LastHeight: &last,
			LastBlockHash: testBlockHash(int64(last)), LastParentHash: testBlockHash(int64(last) - 1),
			StrictAfterReset: s.epoch > 0,
		})
	case r.URL.Path == "/v1/monero/scanner/rewind":
		var req sidecar.ScannerRewindReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Network != "mocknet" ||
			req.ExpectedNextHeight != s.next || req.ExpectedResetEpoch != s.epoch ||
			req.TargetNextHeight >= s.next || req.AnchorHeight+1 != req.TargetNextHeight ||
			req.ExpectedNextHeight-req.TargetNextHeight > 730 ||
			req.AnchorHash != testBlockHash(int64(req.AnchorHeight)) {
			http.Error(w, "invalid rewind", http.StatusConflict)
			return
		}
		if s.failRewind {
			http.Error(w, "rewind unavailable", http.StatusServiceUnavailable)
			return
		}
		previous := s.next
		s.next = req.TargetNextHeight
		s.epoch++
		s.rewinds = append(s.rewinds, s.next)
		if s.loseRewind {
			s.loseRewind = false
			http.Error(w, "lost committed rewind response", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(sidecar.ScannerRewindResp{
			Rewound: true, PreviousNextHeight: previous, NextHeight: s.next, ResetEpoch: s.epoch,
		})
	case r.URL.Path == "/v1/monero/scanner/sync":
		var req sidecar.SyncReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ResetEpoch == nil ||
			*req.ResetEpoch != s.epoch || req.ExpectedNextHeight == nil ||
			*req.ExpectedNextHeight != uint64(req.Height) ||
			(uint64(req.Height) != s.next && uint64(req.Height)+1 != s.next) ||
			req.BlockHash != testBlockHash(req.Height) || req.ParentHash != testBlockHash(req.Height-1) {
			http.Error(w, "out of order sync", http.StatusConflict)
			return
		}
		s.heights = append(s.heights, req.Height)
		if req.Height == 1002 && s.epoch == 0 && s.scanStarted != nil {
			close(s.scanStarted)
			<-s.releaseScan
		}
		s.next = uint64(req.Height + 1)
		if s.loseSync {
			s.loseSync = false
			http.Error(w, "lost committed sync response", http.StatusInternalServerError)
			return
		}
		transfers := sidecar.Transfers{}
		if req.Height == 1001 {
			transfers.Outgoing = []sidecar.Transfer{{
				TxID: testXMRHash("churn-observation"), Height: 1001,
				Address: testOutboundXMRDestination().String(), FromAddress: s.address.String(),
				VaultPubKey: s.vault.String(), Amount: 2 * moneroAtomicToThorchain, Fee: moneroAtomicToThorchain,
				Note: "OUT:BTC:bc1qchurn", Origin: sidecar.OutgoingOriginLocalCombined, ObservationEligible: true,
			}}
		}
		_ = json.NewEncoder(w).Encode(transfers)
	case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
		height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
			Height: height, Hash: testBlockHash(height), PreviousHash: testBlockHash(height - 1),
		})
	default:
		http.NotFound(w, r)
	}
}

type churnBridge struct {
	*scannerMemoBridge
	lastObserved int64
}

func (b *churnBridge) WaitToCatchUp() error { return nil }
func (b *churnBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return b.lastObserved, nil
}
func (b *churnBridge) GetMimirWithRef(string, string) (int64, error) { return 0, nil }
func (b *churnBridge) GetMimir(string) (int64, error)                { return 0, nil }
func (b *churnBridge) GetBlockHeight() (int64, error)                { return 100, nil }
func (b *churnBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 1, 1, nil
}

func (b *churnBridge) GetConstants() (map[string]int64, error) {
	return map[string]int64{
		constants.ObservationDelayFlexibility.String(): 10,
		constants.ThorchainBlockTime.String():          int64(6 * time.Second),
	}, nil
}

type churnFetcher struct{ *Scanner }

func (f churnFetcher) GetHeight() (int64, error) { return 1002, nil }

type quietChurnFetcher struct {
	*Scanner
	tip atomic.Int64
}

func (f *quietChurnFetcher) GetHeight() (int64, error) { return f.tip.Load(), nil }

func TestClientChurnRollbackBeyondSignerLimitPreservesForwardScan(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1775, vault: vault, address: address}
	server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
	defer server.Close()
	storage, err := blockscanner.NewBlockScannerStorage(t.TempDir(), config.LevelDBOptions{})
	require.NoError(t, err)
	defer storage.Close()
	require.NoError(t, storage.SetScanPos(1774))
	store := NewReorgStore(storage.GetInternalDb())
	scanner, bridge := newChurnScanner(t, server.URL, store, vault)
	bridge.lastObserved = 815
	fetcher := &quietChurnFetcher{Scanner: scanner}
	fetcher.tip.Store(1774)
	loop, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.XMRChain, MaxResumeBlockLag: 72 * time.Hour,
		BlockHeightDiscoverBackoff: 10 * time.Millisecond,
	}, storage, failClosedTestMetrics(t), bridge, fetcher)
	require.NoError(t, err)
	client := &Client{blockScanner: loop, scanner: scanner}
	queue := make(chan stypes.TxIn)
	loop.Start(queue, make(chan common.NetworkFee, 10))
	defer loop.Stop()

	// Ready -> Active requests block 815 after a long period without deposits.
	// Rejecting the unsupported history must not persist that cursor or claim
	// the history was replayed. No key share is needed for forward view scanning.
	require.NoError(t, client.RollbackBlockScanner())
	fetcher.tip.Store(1775)
	require.Eventually(t, func() bool { return loop.PreviousHeight() == 1775 },
		3*time.Second, time.Millisecond, "rejected churn rollback must preserve forward scanning")
	position, err := storage.GetScanPos()
	require.NoError(t, err)
	require.Equal(t, int64(1775), position)
	marker, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, marker, "rejected history must not be marked caught up")
	signer.mu.Lock()
	defer signer.mu.Unlock()
	require.Empty(t, signer.rewinds)
	require.Equal(t, []int64{1775}, signer.heights)
	require.Equal(t, uint64(1776), signer.next)
}

func TestValidateScannerRollbackUsesSignerStateAndConfiguredLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		next      int64
		window    int64
		wantError bool
	}{
		{name: "at signer limit", next: 1045, window: 72},
		{name: "past signer limit", next: 1044, window: 72, wantError: true},
		{name: "reported quiet-chain gap", next: 815, window: 72, wantError: true},
		{name: "replay disabled", next: 1773, window: 0, wantError: true},
		{name: "repeat last block without rewind", next: 1774, window: 0},
		{name: "no signer rewind", next: 1775, window: 0},
		{name: "invalid target", next: 0, window: 72, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{next: 1730, vault: vault, address: address}
			server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
			defer server.Close()
			scanner, _ := newChurnScanner(t, server.URL, nil, vault)
			scanner.maxReorgRescan = tc.window
			_, err := scanner.sidecar.ScannerState()
			require.NoError(t, err)
			signer.mu.Lock()
			signer.next = 1775 // The cached state would incorrectly allow 1044.
			signer.mu.Unlock()
			err = scanner.ValidateScannerRollback(tc.next)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			signer.mu.Lock()
			defer signer.mu.Unlock()
			require.Equal(t, uint64(1775), signer.next)
			require.Empty(t, signer.rewinds)
			require.Empty(t, signer.heights)
		})
	}
}

func TestValidateScannerRollbackRejectsUnknownSignerState(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(strconv.FormatBool(unavailable), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if unavailable {
					http.Error(w, "signer unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(sidecar.ScannerState{})
			}))
			defer server.Close()
			scanner, _ := newChurnScanner(t, server.URL, nil, common.EmptyPubKey)
			err := scanner.ValidateScannerRollback(815)
			require.Error(t, err)
			if unavailable {
				require.ErrorIs(t, err, btypes.ErrRollbackValidationUnavailable)
			} else {
				// An uninitialized cursor needs registration, which is driven by
				// FetchTxs. Retrying only the state read would prevent bootstrap.
				require.NotErrorIs(t, err, btypes.ErrRollbackValidationUnavailable)
			}
		})
	}
}

func TestValidateScannerRollbackClassifiesHTTPFailures(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 409, 429, 500, 502, 503, 504} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "state unavailable", status)
			}))
			defer server.Close()
			scanner, _ := newChurnScanner(t, server.URL, nil, common.EmptyPubKey)
			err := scanner.ValidateScannerRollback(1001)
			require.Error(t, err)
			if status == 408 || status == 429 || status >= 500 {
				require.ErrorIs(t, err, btypes.ErrRollbackValidationUnavailable)
			} else {
				require.NotErrorIs(t, err, btypes.ErrRollbackValidationUnavailable)
			}
		})
	}
}

func TestValidateScannerRollbackRetriesTransportFailures(t *testing.T) {
	for _, mode := range []string{"timeout", "connection refused", "truncated response"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "timeout" {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Length", "100")
				_, _ = w.Write([]byte("{"))
			}))
			defer server.Close()
			if mode == "connection refused" {
				server.Close()
			}
			client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{
				BlockScanner: config.BifrostBlockScannerConfiguration{HTTPRequestTimeout: 50 * time.Millisecond},
			}, server.URL)
			require.NoError(t, err)
			scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
			require.ErrorIs(t, scanner.ValidateScannerRollback(1001), btypes.ErrRollbackValidationUnavailable)
		})
	}
}

func TestClientChurnRollbackRetriesValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		distance   int64
		window     int64
		primeCache bool
		repeated   bool
		reject     bool
	}{
		{name: "no cached cursor", distance: 2, window: 72},
		{name: "cached cursor", distance: 2, window: 72, primeCache: true},
		{name: "repeated failures", distance: 2, window: 72, repeated: true},
		{name: "exact signer limit", distance: 730, window: 72},
		{name: "beyond signer limit", distance: 731, window: 72, reject: true},
		{name: "last block without catch-up", distance: 1, window: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const target = int64(1001)
			previous := target + tc.distance - 1
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{next: uint64(previous + 1), vault: vault, address: address}
			var failuresLeft, failedReads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/monero/scanner/state" && failuresLeft.Load() != 0 {
					if failuresLeft.Load() > 0 {
						failuresLeft.Add(-1)
					}
					failedReads.Add(1)
					http.Error(w, "signer temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				signer.serveHTTP(w, r)
			}))
			defer server.Close()
			storage, err := blockscanner.NewBlockScannerStorage(t.TempDir(), config.LevelDBOptions{})
			require.NoError(t, err)
			defer storage.Close()
			require.NoError(t, storage.SetScanPos(previous))
			store := NewReorgStore(storage.GetInternalDb())
			scanner, bridge := newChurnScanner(t, server.URL, store, vault)
			scanner.maxReorgRescan = tc.window
			if tc.primeCache {
				_, err = scanner.sidecar.ScannerState()
				require.NoError(t, err)
			}
			fetcher := &quietChurnFetcher{Scanner: scanner}
			fetcher.tip.Store(previous)
			loop, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
				ChainID: common.XMRChain, MaxResumeBlockLag: 72 * time.Hour,
				BlockHeightDiscoverBackoff: 10 * time.Millisecond,
			}, storage, failClosedTestMetrics(t), bridge, fetcher)
			require.NoError(t, err)
			client := &Client{blockScanner: loop, scanner: scanner}
			queue := make(chan stypes.TxIn)
			stopConsumer, consumerDone := make(chan struct{}), make(chan struct{})
			var observations atomic.Int64
			go func() {
				defer close(consumerDone)
				for {
					select {
					case txIn := <-queue:
						for _, item := range txIn.TxArray {
							if item.Tx == testXMRHash("churn-observation") {
								observations.Add(1)
							}
							if item.OnObservedTxStored != nil {
								item.OnObservedTxStored()
							}
						}
						if txIn.Persisted != nil {
							txIn.Persisted <- nil
						}
					case <-stopConsumer:
						return
					}
				}
			}()
			loop.Start(queue, make(chan common.NetworkFee, 10))
			defer func() {
				loop.Stop()
				close(stopConsumer)
				<-consumerDone
			}()
			failuresLeft.Store(1)
			if tc.repeated {
				failuresLeft.Store(-1)
			}
			// This is the only churn request. A new block must not displace its
			// original replay height while validation is unavailable.
			require.NoError(t, client.RollbackBlockScanner())
			fetcher.tip.Store(previous + 1)
			if tc.repeated {
				require.Eventually(t, func() bool { return failedReads.Load() >= 3 },
					3*time.Second, time.Millisecond)
				require.False(t, loop.IsHealthy())
				require.Equal(t, previous, loop.PreviousHeight())
				position, posErr := storage.GetScanPos()
				require.NoError(t, posErr)
				require.Equal(t, previous, position)
				marker, markerErr := store.GetCatchupMarker()
				require.NoError(t, markerErr)
				require.Nil(t, marker)
				signer.mu.Lock()
				signerNext, scans, rewinds := signer.next, len(signer.heights), len(signer.rewinds)
				signer.mu.Unlock()
				require.Equal(t, uint64(previous+1), signerNext)
				require.Zero(t, scans)
				require.Zero(t, rewinds)
				failuresLeft.Store(0)
			}
			// The exact-limit case rescans about 800 blocks, which takes over 30s on
			// a small CI runner.
			require.Eventually(t, func() bool { return loop.PreviousHeight() == previous+1 },
				2*time.Minute, time.Millisecond, "scanning must resume after validation finishes")
			require.Positive(t, failedReads.Load())
			position, err := storage.GetScanPos()
			require.NoError(t, err)
			require.Equal(t, previous+1, position)
			if tc.reject {
				require.Zero(t, observations.Load(), "unsupported history must not be claimed as replayed")
			} else {
				require.Positive(t, observations.Load(), "the original historical payment must be replayed")
			}
			signer.mu.Lock()
			defer signer.mu.Unlock()
			if tc.reject || tc.distance == 1 {
				require.Empty(t, signer.rewinds)
			} else {
				require.Equal(t, []uint64{uint64(target)}, signer.rewinds)
			}
		})
	}
}

func newChurnScanner(t *testing.T, url string, store *ReorgStore, vault common.PubKey) (*Scanner, *churnBridge) {
	t.Helper()
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{
		ChainNetwork:          "mocknet",
		SignerExpectedNetwork: "mocknet", SignerAuthorizationBearer: "churn-test",
	}, url)
	require.NoError(t, err)
	bridge := &churnBridge{scannerMemoBridge: &scannerMemoBridge{
		asgards: ttypes.Vaults{{PubKeyFrost: vault, VaultCreationHeightXmr: 990}},
	}, lastObserved: 1001}
	scanner := NewScanner(log.Logger, client, bridge, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	return scanner, bridge
}

type changingChurnBridge struct {
	*churnBridge
	observedHeight atomic.Int64
}

func (b *changingChurnBridge) GetLastObservedInHeight(common.Chain) (int64, error) {
	return b.observedHeight.Load(), nil
}

func TestClientChurnRollbackCoalescesWhileValidationWaits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []int64
	}{
		{name: "duplicate and later targets", targets: []int64{1001, 1001, 1002}},
		{name: "earlier target", targets: []int64{1002, 1001, 1002}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{next: 1003, vault: vault, address: address}
			var stateDown atomic.Bool
			var failedReads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/monero/scanner/state" && stateDown.Load() {
					failedReads.Add(1)
					http.Error(w, "signer temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				signer.serveHTTP(w, r)
			}))
			defer server.Close()
			storage, err := blockscanner.NewBlockScannerStorage(t.TempDir(), config.LevelDBOptions{})
			require.NoError(t, err)
			defer storage.Close()
			require.NoError(t, storage.SetScanPos(1002))
			store := NewReorgStore(storage.GetInternalDb())
			scanner, bridge := newChurnScanner(t, server.URL, store, vault)
			changingBridge := &changingChurnBridge{churnBridge: bridge}
			changingBridge.observedHeight.Store(tc.targets[0])
			fetcher := &quietChurnFetcher{Scanner: scanner}
			fetcher.tip.Store(1002)
			loop, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
				ChainID: common.XMRChain, MaxResumeBlockLag: 72 * time.Hour,
				BlockHeightDiscoverBackoff: 10 * time.Millisecond,
			}, storage, failClosedTestMetrics(t), changingBridge, fetcher)
			require.NoError(t, err)
			client := &Client{blockScanner: loop, scanner: scanner}
			queue := make(chan stypes.TxIn)
			stopConsumer := make(chan struct{})
			consumerDone := make(chan struct{})
			go func() {
				// Acknowledge like Observer so replayed observations let the
				// scanner advance past their block.
				defer close(consumerDone)
				for {
					select {
					case txIn := <-queue:
						for _, item := range txIn.TxArray {
							if item.OnObservedTxStored != nil {
								item.OnObservedTxStored()
							}
						}
						if txIn.Persisted != nil {
							txIn.Persisted <- nil
						}
					case <-stopConsumer:
						return
					}
				}
			}()
			loop.Start(queue, make(chan common.NetworkFee, 10))
			defer func() {
				loop.Stop()
				close(stopConsumer)
				<-consumerDone
			}()

			stateDown.Store(true)
			require.NoError(t, client.RollbackBlockScanner())
			require.Eventually(t, func() bool { return failedReads.Load() >= 2 },
				5*time.Second, time.Millisecond)
			for _, target := range tc.targets[1:] {
				changingBridge.observedHeight.Store(target)
				returned := make(chan error, 1)
				// Observer calls this synchronously from its loop for all chains.
				// It must return while the XMR signer is still unavailable.
				go func() { returned <- client.RollbackBlockScanner() }()
				select {
				case err = <-returned:
					require.NoError(t, err)
				case <-time.After(2 * time.Second):
					t.Fatal("later rollback blocked Observer while validation was unavailable")
				}
			}
			require.Equal(t, int64(1002), loop.PreviousHeight())
			position, err := storage.GetScanPos()
			require.NoError(t, err)
			require.Equal(t, int64(1002), position)
			marker, err := store.GetCatchupMarker()
			require.NoError(t, err)
			require.Nil(t, marker)
			signer.mu.Lock()
			signerNext, scans, rewinds := signer.next, len(signer.heights), len(signer.rewinds)
			signer.mu.Unlock()
			require.Equal(t, uint64(1003), signerNext)
			require.Zero(t, scans)
			require.Zero(t, rewinds)

			stateDown.Store(false)
			fetcher.tip.Store(1003)
			require.Eventually(t, func() bool { return loop.PreviousHeight() == 1003 },
				10*time.Second, time.Millisecond)
			signer.mu.Lock()
			defer signer.mu.Unlock()
			require.Equal(t, []uint64{1001}, signer.rewinds)
			require.Equal(t, []int64{1001, 1002, 1003}, signer.heights,
				"merged requests must replay from the earliest height exactly once")
		})
	}
}

func TestClientChurnRollbackResumesScanner(t *testing.T) {
	for _, mode := range []string{"cached registration", "refreshed registration", "in-flight scan"} {
		t.Run(mode, func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{next: 990, vault: vault, address: address}
			var releaseOnce sync.Once
			releaseScan := func() { releaseOnce.Do(func() { close(signer.releaseScan) }) }
			if mode == "in-flight scan" {
				signer.scanStarted = make(chan struct{})
				signer.releaseScan = make(chan struct{})
			}
			server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
			defer server.Close()
			storage, err := blockscanner.NewBlockScannerStorage(t.TempDir(), config.LevelDBOptions{})
			require.NoError(t, err)
			defer storage.Close()
			require.NoError(t, storage.SetScanPos(989))
			scanner, bridge := newChurnScanner(t, server.URL, NewReorgStore(storage.GetInternalDb()), vault)
			loop, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
				ChainID: common.XMRChain, MaxResumeBlockLag: 72 * time.Hour,
				BlockHeightDiscoverBackoff: 10 * time.Millisecond,
			}, storage, failClosedTestMetrics(t), bridge, churnFetcher{scanner})
			require.NoError(t, err)
			client := &Client{blockScanner: loop, scanner: scanner}
			queue := make(chan stypes.TxIn)
			stopConsumer := make(chan struct{})
			consumerDone := make(chan struct{})
			var observations atomic.Int64
			go func() {
				defer close(consumerDone)
				for {
					select {
					case txIn := <-queue:
						for _, item := range txIn.TxArray {
							if item.Tx == testXMRHash("churn-observation") {
								observations.Add(1)
							}
							if item.OnObservedTxStored != nil {
								item.OnObservedTxStored()
							}
						}
						if txIn.Persisted != nil {
							txIn.Persisted <- nil
						}
					case <-stopConsumer:
						return
					}
				}
			}()
			loop.Start(queue, make(chan common.NetworkFee, 10))
			defer func() {
				if mode == "in-flight scan" {
					releaseScan()
				}
				loop.Stop()
				close(stopConsumer)
				<-consumerDone
			}()
			if mode == "in-flight scan" {
				select {
				case <-signer.scanStarted:
				case <-time.After(3 * time.Second):
					t.Fatal("final Ready scan did not start")
				}
			} else {
				require.Eventually(t, func() bool { return loop.PreviousHeight() == 1002 }, 10*time.Second, time.Millisecond)
			}
			require.Equal(t, int64(1), observations.Load())
			registrationsBefore := 0
			if mode != "in-flight scan" {
				signer.mu.Lock()
				registrationsBefore = signer.registrations
				signer.mu.Unlock()
				scanner.scanMu.Lock()
				scanner.asgardMu.Lock()
				// Select the cache branch independently of mocknet's short TTL
				// and race-detector/CI scheduling during the Ready scans.
				if mode == "refreshed registration" {
					scanner.asgardUpdated = time.Time{}
				} else {
					scanner.asgardUpdated = time.Now().Add(time.Hour)
				}
				scanner.asgardMu.Unlock()
				scanner.scanMu.Unlock()
			}
			// This is the production entrypoint called by Observer on Ready -> Active.
			if mode == "in-flight scan" {
				rollbackDone := make(chan error, 1)
				go func() { rollbackDone <- client.RollbackBlockScanner() }()
				select {
				case <-rollbackDone:
					t.Fatal("rollback must wait for the in-flight scan and its persistence")
				case <-time.After(50 * time.Millisecond):
				}
				require.Equal(t, int64(1001), loop.PreviousHeight())
				releaseScan()
				select {
				case err = <-rollbackDone:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("rollback did not resume after scan completed")
				}
			} else {
				require.NoError(t, client.RollbackBlockScanner())
			}
			require.Eventually(t, func() bool {
				// Receipts may be redelivered until Observer's stored callback
				// finishes; the required invariant is no missing churn replay.
				return observations.Load() >= 2 && loop.PreviousHeight() == 1002
			}, 10*time.Second, time.Millisecond, "churn rollback must replay 1001 and resume through 1002")
			signer.mu.Lock()
			defer signer.mu.Unlock()
			require.Equal(t, []uint64{1001}, signer.rewinds)
			require.Equal(t, []int64{1001, 1002}, signer.heights[len(signer.heights)-2:])
			require.Equal(t, uint64(1003), signer.next)
			if mode == "refreshed registration" {
				require.Greater(t, signer.registrations, registrationsBefore)
			} else if mode == "cached registration" {
				require.Equal(t, registrationsBefore, signer.registrations)
			}
		})
	}
}

func TestChurnRecoveryRetriesAcrossDurableRestart(t *testing.T) {
	for _, failure := range []string{"rewind rejected", "rewind response lost", "sync response lost"} {
		t.Run(failure, func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{
				next: 1003, vault: vault, address: address,
				failRewind: failure == "rewind rejected", loseRewind: failure == "rewind response lost",
				loseSync: failure == "sync response lost",
			}
			server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
			defer server.Close()
			dir := t.TempDir()
			storage, err := blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
			require.NoError(t, err)
			defer func() { _ = storage.Close() }()
			require.NoError(t, storage.SetScanPos(1000))
			store := NewReorgStore(storage.GetInternalDb())
			retained := scannerReceiptItem("churn-retained-receipt", 1000)
			retainedMeta := &reorgBlockMeta{
				Height: 1000, BlockHash: testBlockHash(1000), PreviousHash: testBlockHash(999),
			}
			require.NoError(t, store.Set(retainedMeta))
			require.NoError(t, store.PutObservationReceipt(retainedMeta, []*stypes.TxInItem{retained}))
			quarantined := common.TxID(testXMRHash("churn-quarantined"))
			require.NoError(t, store.QuarantineRevertedTxs(900, []stypes.ErrataTx{{TxID: quarantined, Chain: common.XMRChain}}))
			scanner, _ := newChurnScanner(t, server.URL, store, vault)
			_, err = scanner.FetchTxs(1001, 1002)
			require.Error(t, err)
			position, err := storage.GetScanPos()
			require.NoError(t, err)
			require.Equal(t, int64(1000), position)
			marker, err := store.GetCatchupMarker()
			require.NoError(t, err)
			require.NotNil(t, marker, "recovery intent must precede the sidecar mutation")
			require.Equal(t, int64(1001), marker.StartHeight)
			receipts, err := store.ListObservationReceipts()
			require.NoError(t, err)
			require.Len(t, receipts, 1, "failed recovery must retain previous receipts")
			signer.mu.Lock()
			if signer.failRewind {
				require.Equal(t, uint64(1003), signer.next)
				require.Zero(t, signer.epoch)
				signer.failRewind = false
			}
			signer.mu.Unlock()

			// Reopen the actual disk journal and create a fresh Go sidecar client.
			// The remote signer retains whichever mutation committed before failure.
			require.NoError(t, storage.Close())
			storage, err = blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
			require.NoError(t, err)
			store = NewReorgStore(storage.GetInternalDb())
			scanner, _ = newChurnScanner(t, server.URL, store, vault)
			txIn, err := scanner.FetchTxs(1001, 1002)
			require.NoError(t, err)
			require.Len(t, txIn.TxArray, 2)
			require.Equal(t, testXMRHash("churn-observation"), txIn.TxArray[0].Tx)
			require.Equal(t, retained.Tx, txIn.TxArray[1].Tx)
			stillQuarantined, err := isRevertedTxQuarantined(storage.GetInternalDb(), quarantined.String())
			require.NoError(t, err)
			require.True(t, stillQuarantined)
			for _, item := range txIn.TxArray {
				item.OnObservedTxStored()
			}
			marker, err = store.GetCatchupMarker()
			require.NoError(t, err)
			require.Nil(t, marker)
			receipts, err = store.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, receipts)
			_, err = scanner.FetchTxs(1002, 1002)
			require.NoError(t, err, "recovery must return to forward scanning")
			signer.mu.Lock()
			defer signer.mu.Unlock()
			require.Equal(t, uint64(1003), signer.next)
		})
	}
}

func TestChurnRecoveryPreservesEarlierCatchup(t *testing.T) {
	for _, start := range []int64{999, 1003} {
		t.Run(strconv.FormatInt(start, 10), func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{next: 1003, vault: vault, address: address}
			if start == 999 {
				// The first catch-up sync committed, but its response was lost
				// before checkpointing. Churn then lowered the requested target.
				signer.next = 1000
			}
			server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
			defer server.Close()
			store := newScannerReceiptStore(t)
			initialStart := min(start, 1002)
			require.NoError(t, store.BeginCatchup(scannerCatchupMarker{
				StartHeight: initialStart, TargetHeight: 1002,
				AnchorHeight: initialStart - 1, AnchorHash: testBlockHash(initialStart - 1),
			}))
			if start == 1003 {
				require.NoError(t, store.CheckpointCatchup(scannerCatchupMarker{
					StartHeight: start, TargetHeight: 1002, AnchorHeight: 1002, AnchorHash: testBlockHash(1002),
				}, &reorgBlockMeta{Height: 1002, BlockHash: testBlockHash(1002), PreviousHash: testBlockHash(1001)}, nil))
			}
			scanner, _ := newChurnScanner(t, server.URL, store, vault)
			txIn, err := scanner.FetchTxs(1001, 1002)
			require.NoError(t, err)
			require.Len(t, txIn.TxArray, 1)
			txIn.TxArray[0].OnObservedTxStored()
			_, err = scanner.FetchTxs(1002, 1002)
			require.NoError(t, err)
			signer.mu.Lock()
			defer signer.mu.Unlock()
			require.Equal(t, []uint64{uint64(min(start, 1001))}, signer.rewinds)
			if start == 999 {
				require.Equal(t, []int64{999, 1000, 1001, 1002}, signer.heights)
			} else {
				require.Equal(t, []int64{1001, 1002}, signer.heights)
			}
		})
	}
}

func TestChurnLoweredRegistrationUsesSignerRewindWindow(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1080, vault: vault, address: address}
	server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
	defer server.Close()
	store := newScannerReceiptStore(t)
	require.NoError(t, store.BeginCatchup(scannerCatchupMarker{
		StartHeight: 1080, TargetHeight: 1080,
		AnchorHeight: 1079, AnchorHash: testBlockHash(1079),
	}))
	scanner, _ := newChurnScanner(t, server.URL, store, vault)

	// Churn lowers an existing registration catch-up. Its 79-block rewind
	// needs the churn allowance even though the original marker did not.
	txIn, err := scanner.FetchTxs(1001, 1080)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	require.Equal(t, testXMRHash("churn-observation"), txIn.TxArray[0].Tx)
	marker, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.Equal(t, &scannerCatchupMarker{
		StartHeight: 1002, TargetHeight: 1001,
		AnchorHeight: 1001, AnchorHash: testBlockHash(1001), ChurnRollback: true,
	}, marker)
	txIn.TxArray[0].OnObservedTxStored()
	marker, err = store.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, marker)

	wantHeights := []int64{1001}
	for height := int64(1002); height <= 1080; height++ {
		_, err = scanner.FetchTxs(height, 1080)
		require.NoError(t, err, "forward scan at height %d", height)
		wantHeights = append(wantHeights, height)
	}
	signer.mu.Lock()
	defer signer.mu.Unlock()
	require.Equal(t, []uint64{1001}, signer.rewinds)
	require.Equal(t, wantHeights, signer.heights)
	require.Equal(t, uint64(1081), signer.next)
}

func TestChurnExactLastBlockReplayNeedsNoRewind(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1003, vault: vault, address: address}
	server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
	defer server.Close()
	scanner, _ := newChurnScanner(t, server.URL, newScannerReceiptStore(t), vault)
	_, err := scanner.FetchTxs(1002, 1003)
	require.NoError(t, err)
	_, err = scanner.FetchTxs(1003, 1003)
	require.NoError(t, err)
	signer.mu.Lock()
	defer signer.mu.Unlock()
	require.Empty(t, signer.rewinds)
	require.Zero(t, signer.stateReads, "exact replay and forward scans must retain the cached fast path")
	require.Equal(t, []int64{1002, 1003}, signer.heights)
}

func TestChurnRollbackUsesSignerRewindWindow(t *testing.T) {
	for _, distance := range []int64{72, 73, 99, 256, 257, 730} {
		t.Run(strconv.FormatInt(distance, 10), func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			nextHeight := int64(1001) + distance
			signer := &churnSigner{next: uint64(nextHeight), vault: vault, address: address}
			server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
			defer server.Close()
			store := newScannerReceiptStore(t)
			scanner, _ := newChurnScanner(t, server.URL, store, vault)
			txIn, err := scanner.FetchTxs(1001, nextHeight)
			require.NoError(t, err)
			require.Len(t, txIn.TxArray, 1)
			txIn.TxArray[0].OnObservedTxStored()
			for height := int64(1002); height <= nextHeight; height++ {
				_, err = scanner.FetchTxs(height, nextHeight)
				require.NoError(t, err)
			}
			signer.mu.Lock()
			defer signer.mu.Unlock()
			require.Equal(t, []uint64{1001}, signer.rewinds)
			require.Equal(t, uint64(nextHeight+1), signer.next)
			require.Len(t, signer.heights, int(distance)+1)
			for index, height := range signer.heights {
				require.Equal(t, int64(1001+index), height, "recovery must scan every height in order")
			}
		})
	}
}

func TestChurnRejectedRollbackPreservesRecoveryState(t *testing.T) {
	for _, replaySetting := range []int64{0, 72} {
		for _, completed := range []bool{false, true} {
			name := strconv.FormatInt(replaySetting, 10) + "/completed=" + strconv.FormatBool(completed)
			t.Run(name, func(t *testing.T) {
				vault, address := testVaultPubKeyAndAddress(t)
				nextHeight := int64(1732) // One beyond the pinned signer's 730-block bound.
				if replaySetting == 0 {
					nextHeight = 1100
				}
				signer := &churnSigner{next: uint64(nextHeight), vault: vault, address: address}
				server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
				defer server.Close()
				dir := t.TempDir()
				storage, err := blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
				require.NoError(t, err)
				defer func() { _ = storage.Close() }()
				store := NewReorgStore(storage.GetInternalDb())
				if completed {
					require.NoError(t, store.BeginCatchup(scannerCatchupMarker{
						StartHeight: nextHeight - 1, TargetHeight: nextHeight - 1,
						AnchorHeight: nextHeight - 2, AnchorHash: testBlockHash(nextHeight - 2),
					}))
					require.NoError(t, store.CheckpointCatchup(scannerCatchupMarker{
						StartHeight: nextHeight, TargetHeight: nextHeight - 1,
						AnchorHeight: nextHeight - 1, AnchorHash: testBlockHash(nextHeight - 1),
					}, &reorgBlockMeta{Height: nextHeight - 1, BlockHash: testBlockHash(nextHeight - 1), PreviousHash: testBlockHash(nextHeight - 2)}, nil))
				}
				before, err := store.GetCatchupMarker()
				require.NoError(t, err)
				scanner, _ := newChurnScanner(t, server.URL, store, vault)
				scanner.maxReorgRescan = replaySetting
				_, err = scanner.FetchTxs(1001, nextHeight)
				require.ErrorContains(t, err, "rewind limit")
				require.NoError(t, storage.Close())
				storage, err = blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
				require.NoError(t, err)
				store = NewReorgStore(storage.GetInternalDb())
				after, err := store.GetCatchupMarker()
				require.NoError(t, err)
				require.Equal(t, before, after, "rejected recovery must leave durable state unchanged")
				scanner, _ = newChurnScanner(t, server.URL, store, vault)
				scanner.maxReorgRescan = replaySetting
				for _, height := range []int64{nextHeight - 1, nextHeight} {
					_, err = scanner.FetchTxs(height, nextHeight)
					require.NoError(t, err, "a rejected rewind must not poison later forward recovery")
				}
				signer.mu.Lock()
				defer signer.mu.Unlock()
				require.Empty(t, signer.rewinds)
			})
		}
	}
}

func TestChurnRecoveryExtendsAfterForwardRestart(t *testing.T) {
	for _, failure := range []string{"rewind rejected", "rewind response lost", "sync response lost"} {
		t.Run(failure, func(t *testing.T) {
			vault, address := testVaultPubKeyAndAddress(t)
			signer := &churnSigner{
				next: 1100, vault: vault, address: address,
				failRewind: failure == "rewind rejected", loseRewind: failure == "rewind response lost",
				loseSync: failure == "sync response lost",
			}
			server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
			defer server.Close()
			dir := t.TempDir()
			storage, err := blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
			require.NoError(t, err)
			defer func() { _ = storage.Close() }()
			store := NewReorgStore(storage.GetInternalDb())
			retained := scannerReceiptItem("churn-forward-retained", 1000)
			meta := &reorgBlockMeta{Height: 1000, BlockHash: testBlockHash(1000), PreviousHash: testBlockHash(999)}
			require.NoError(t, store.Set(meta))
			require.NoError(t, store.PutObservationReceipt(meta, []*stypes.TxInItem{retained}))
			quarantined := common.TxID(testXMRHash("churn-forward-quarantine"))
			require.NoError(t, store.QuarantineRevertedTxs(900, []stypes.ErrataTx{{TxID: quarantined, Chain: common.XMRChain}}))
			scanner, _ := newChurnScanner(t, server.URL, store, vault)
			_, err = scanner.FetchTxs(1001, 1110)
			require.Error(t, err)
			signer.mu.Lock()
			signer.failRewind = false
			signer.mu.Unlock()
			for restart := 0; restart < 2; restart++ {
				require.NoError(t, storage.Close())
				storage, err = blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
				require.NoError(t, err)
				store = NewReorgStore(storage.GetInternalDb())
				scanner, _ = newChurnScanner(t, server.URL, store, vault)
				txIn, fetchErr := scanner.FetchTxs(1100, 1110)
				if restart == 0 {
					require.ErrorIs(t, fetchErr, btypes.ErrUnavailableBlock, "one call still replays at most 72 blocks")
					marker, markerErr := store.GetCatchupMarker()
					require.NoError(t, markerErr)
					require.NotNil(t, marker)
					require.Equal(t, int64(1073), marker.StartHeight)
					require.Equal(t, int64(1100), marker.TargetHeight)
					continue
				}
				require.NoError(t, fetchErr)
				require.Len(t, txIn.TxArray, 2)
				require.Equal(t, testXMRHash("churn-observation"), txIn.TxArray[0].Tx)
				require.Equal(t, retained.Tx, txIn.TxArray[1].Tx)
				for _, item := range txIn.TxArray {
					item.OnObservedTxStored()
				}
			}
			marker, err := store.GetCatchupMarker()
			require.NoError(t, err)
			require.Nil(t, marker)
			stillQuarantined, err := isRevertedTxQuarantined(storage.GetInternalDb(), quarantined.String())
			require.NoError(t, err)
			require.True(t, stillQuarantined)
			_, err = scanner.FetchTxs(1101, 1110)
			require.NoError(t, err)
		})
	}
}

func TestChurnOldAcknowledgementCannotClearExtendedRecovery(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1003, vault: vault, address: address}
	server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
	defer server.Close()
	store := newScannerReceiptStore(t)
	scanner, _ := newChurnScanner(t, server.URL, store, vault)
	txIn, err := scanner.FetchTxs(1001, 1110)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	_, err = scanner.FetchTxs(1100, 1110)
	require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
	txIn.TxArray[0].OnObservedTxStored()
	marker, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.NotNil(t, marker)
	require.Equal(t, int64(1074), marker.StartHeight)
	require.Equal(t, int64(1100), marker.TargetHeight)
	receipts, err := store.ListObservationReceipts()
	require.NoError(t, err)
	require.Len(t, receipts, 1, "a stale completed-job callback must retain unacknowledged observations")
	scanner, _ = newChurnScanner(t, server.URL, store, vault)
	txIn, err = scanner.FetchTxs(1100, 1110)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	txIn.TxArray[0].OnObservedTxStored()
	marker, err = store.GetCatchupMarker()
	require.NoError(t, err)
	require.Nil(t, marker)
}

func TestChurnRecoveryDoesNotExtendUnrelatedCatchup(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1001, vault: vault, address: address}
	server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
	defer server.Close()
	store := newScannerReceiptStore(t)
	marker := scannerCatchupMarker{StartHeight: 1001, TargetHeight: 1002, AnchorHeight: 1000, AnchorHash: testBlockHash(1000)}
	require.NoError(t, store.BeginCatchup(marker))
	scanner, _ := newChurnScanner(t, server.URL, store, vault)
	_, err := scanner.FetchTxs(1005, 1110)
	require.ErrorContains(t, err, "catch-up targets height 1002")
	after, err := store.GetCatchupMarker()
	require.NoError(t, err)
	require.Equal(t, &marker, after)
}

type churnRestartFetcher struct{ *Scanner }

func (f churnRestartFetcher) GetHeight() (int64, error) { return 1110, nil }

func TestChurnRecoveryResumesBlockScannerAfterStartupClamp(t *testing.T) {
	vault, address := testVaultPubKeyAndAddress(t)
	signer := &churnSigner{next: 1100, vault: vault, address: address, failRewind: true}
	server := httptest.NewServer(http.HandlerFunc(signer.serveHTTP))
	defer server.Close()
	dir := t.TempDir()
	storage, err := blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
	require.NoError(t, err)
	defer func() { _ = storage.Close() }()
	require.NoError(t, storage.SetScanPos(1000))
	store := NewReorgStore(storage.GetInternalDb())
	scanner, _ := newChurnScanner(t, server.URL, store, vault)
	_, err = scanner.FetchTxs(1001, 1110)
	require.Error(t, err)
	require.NoError(t, storage.Close())
	storage, err = blockscanner.NewBlockScannerStorage(dir, config.LevelDBOptions{})
	require.NoError(t, err)
	store = NewReorgStore(storage.GetInternalDb())
	signer.mu.Lock()
	signer.failRewind = false
	signer.mu.Unlock()
	scanner, bridge := newChurnScanner(t, server.URL, store, vault)
	bridge.lastObserved = 1129
	loop, err := blockscanner.NewBlockScanner(config.BifrostBlockScannerConfiguration{
		ChainID: common.XMRChain, MaxResumeBlockLag: time.Hour,
		BlockHeightDiscoverBackoff: 10 * time.Millisecond,
	}, storage, failClosedTestMetrics(t), bridge, churnRestartFetcher{scanner})
	require.NoError(t, err)
	start, err := loop.GetStartHeight()
	require.NoError(t, err)
	require.Equal(t, int64(1099), start, "default one-hour clamp advances Bifrost's first request to 1100")
	queue := make(chan stypes.TxIn)
	stopConsumer := make(chan struct{})
	consumerDone := make(chan struct{})
	var observations atomic.Int64
	go func() {
		defer close(consumerDone)
		for {
			select {
			case txIn := <-queue:
				for _, item := range txIn.TxArray {
					if item.Tx == testXMRHash("churn-observation") {
						observations.Add(1)
					}
					if item.OnObservedTxStored != nil {
						item.OnObservedTxStored()
					}
				}
				if txIn.Persisted != nil {
					txIn.Persisted <- nil
				}
			case <-stopConsumer:
				return
			}
		}
	}()
	loop.Start(queue, make(chan common.NetworkFee, 10))
	defer func() {
		loop.Stop()
		close(stopConsumer)
		<-consumerDone
	}()
	// Scanning 110 heights is CPU-bound under -race (~2s idle, >5s on a loaded
	// runner), so the deadline only bounds a hang rather than timing the scan.
	require.Eventually(t, func() bool {
		return observations.Load() >= 1 && loop.PreviousHeight() == 1110
	}, 30*time.Second, 10*time.Millisecond, "startup clamping must retain block1001 and eventually reach the tip")
	signer.mu.Lock()
	defer signer.mu.Unlock()
	require.Equal(t, []uint64{1001}, signer.rewinds)
	require.Equal(t, uint64(1111), signer.next)
	require.Len(t, signer.heights, 110)
	for index, height := range signer.heights {
		require.Equal(t, int64(1001+index), height, "startup recovery must not skip any pending block")
	}
}
