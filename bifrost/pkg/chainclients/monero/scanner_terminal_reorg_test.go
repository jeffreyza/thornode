package monero

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestFetchTxsHeightMismatchRequiresRecovery(t *testing.T) {
	const fetchHeight = int64(90)
	for _, test := range []struct {
		name           string
		mismatchHeight int64
		cursor         uint64
		journalHeight  int64
		wantNext       int64
		unavailable    bool
	}{
		{name: "main fetch without cursor", mismatchHeight: 90, wantNext: 90},
		{name: "main fetch with cursor", mismatchHeight: 90, cursor: 80, wantNext: 80},
		{name: "journal tip", mismatchHeight: 100, cursor: 101, journalHeight: 100, wantNext: 101},
		{name: "catch-up anchor", mismatchHeight: 79, cursor: 80, wantNext: 80},
		{name: "unavailable catch-up anchor", mismatchHeight: 79, cursor: 80, unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mutations, mismatches, stateReads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutations.Add(1)
					http.Error(w, "unexpected scanner mutation", http.StatusMethodNotAllowed)
					return
				}
				const prefix = "/v1/monero/chain/block/"
				switch {
				case strings.HasPrefix(r.URL.Path, prefix):
					height, parseErr := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, prefix), 10, 64)
					if parseErr != nil {
						http.Error(w, parseErr.Error(), http.StatusBadRequest)
						return
					}
					block := sidecar.ChainBlockResp{
						Height: height, Hash: testBlockHash(height), PreviousHash: testBlockHash(height - 1),
					}
					if height == test.mismatchHeight {
						mismatches.Add(1)
						if test.unavailable {
							http.Error(w, "block unavailable", http.StatusConflict)
							return
						}
						block.Height++
					}
					_ = json.NewEncoder(w).Encode(block)
				case r.URL.Path == "/v1/monero/scanner/state":
					stateReads.Add(1)
					_ = json.NewEncoder(w).Encode(sidecar.ScannerState{NextHeight: &test.cursor})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			if test.cursor > 0 {
				_, err = client.ScannerState()
				require.NoError(t, err)
			}
			store := newScannerReceiptStore(t)
			var journal *reorgBlockMeta
			if test.journalHeight > 0 {
				journal = &reorgBlockMeta{Height: test.journalHeight, BlockHash: testBlockHash(test.journalHeight)}
				require.NoError(t, store.Set(journal))
			}
			scanner := NewScanner(testLogger(), client, nil, common.XMRChain, common.EmptyPubKey, 72)
			scanner.SetReorgStore(store)
			_, err = scanner.FetchTxs(fetchHeight, 110)

			var recovery *btypes.ScannerRecoveryRequiredError
			if test.unavailable {
				require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
				require.NotErrorAs(t, err, &recovery)
			} else {
				require.ErrorAs(t, err, &recovery)
				require.Equal(t, fetchHeight, recovery.CurrentHeight)
				require.Equal(t, test.wantNext, recovery.NextHeight)
				require.Contains(t, recovery.Reason, fmt.Sprintf("requested height %d", test.mismatchHeight))
				require.Contains(t, recovery.Reason, fmt.Sprintf("returned height %d", test.mismatchHeight+1))
				require.NotErrorIs(t, err, btypes.ErrUnavailableBlock)
			}
			require.Equal(t, int32(1), mismatches.Load(), "the selected failure path must be reached")
			require.Zero(t, mutations.Load(), "no rewind or sync may follow the mismatch")
			if test.cursor == 0 {
				require.Zero(t, stateReads.Load(), "error mapping must not make a new cursor RPC")
			}
			after, getErr := store.Get(test.journalHeight)
			require.NoError(t, getErr)
			require.Equal(t, journal, after)
			marker, markerErr := store.GetCatchupMarker()
			require.NoError(t, markerErr)
			require.Nil(t, marker)
			receipts, receiptErr := store.ListObservationReceipts()
			require.NoError(t, receiptErr)
			require.Empty(t, receipts)
		})
	}
}

func TestFetchTxsPreservesReorgRecoveryTarget(t *testing.T) {
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			http.Error(w, "unexpected mutation", http.StatusMethodNotAllowed)
			return
		}
		const prefix = "/v1/monero/chain/block/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		height, parseErr := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, prefix), 10, 64)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			return
		}
		returned := height
		if height == 100 {
			returned = 101
		}
		_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp{
			Height: returned, Hash: testBlockHash(height), PreviousHash: testBlockHash(height - 1),
		})
	}))
	defer server.Close()
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	store := newScannerReceiptStore(t)
	journal := &reorgBlockMeta{Height: 100, BlockHash: testXMRHash("orphaned-journal-tip")}
	require.NoError(t, store.Set(journal))
	scanner := NewScanner(testLogger(), client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	_, err = scanner.FetchTxs(101, 110)
	var recovery *btypes.ScannerRecoveryRequiredError
	require.ErrorAs(t, err, &recovery)
	require.Equal(t, int64(101), recovery.CurrentHeight)
	require.Equal(t, int64(100), recovery.NextHeight, "preserve the existing reorg recovery target")
	require.Zero(t, mutations.Load())
	after, getErr := store.Get(100)
	require.NoError(t, getErr)
	require.Equal(t, journal, after)
}

func TestDiscoverReorgReplacementRejectsWrongBlockHeight(t *testing.T) {
	tests := []struct {
		name                 string
		mismatchDuringReplay bool
		wantPhase            string
	}{
		{name: "common ancestor search", wantPhase: "reorg check"},
		{name: "replacement replay", mismatchDuringReplay: true, wantPhase: "reorg replay"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			old99 := testXMRHash("wrong-height-old-99")
			old100 := testXMRHash("wrong-height-old-100")
			new100 := testXMRHash("wrong-height-new-100")
			var height100Calls atomic.Int32
			var mutationCalls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutationCalls.Add(1)
					http.Error(w, "unexpected mutation", http.StatusMethodNotAllowed)
					return
				}
				const prefix = "/v1/monero/chain/block/"
				if !strings.HasPrefix(r.URL.Path, prefix) {
					http.NotFound(w, r)
					return
				}
				height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, prefix), 10, 64)
				if err != nil {
					http.Error(w, "invalid height", http.StatusBadRequest)
					return
				}

				block := sidecar.ChainBlock{Height: height}
				switch height {
				case 99:
					block.Hash = old99
				case 100:
					block.Hash = new100
					if call := height100Calls.Add(1); !test.mismatchDuringReplay || call > 1 {
						block.Height = 101
					}
				default:
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp(block))
			}))
			defer server.Close()

			client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			if err != nil {
				t.Fatalf("new sidecar client: %v", err)
			}
			store := newScannerReceiptStore(t)
			for _, meta := range []*reorgBlockMeta{
				{Height: 99, BlockHash: old99},
				{Height: 100, BlockHash: old100},
			} {
				if err = store.Set(meta); err != nil {
					t.Fatalf("seed reorg metadata: %v", err)
				}
			}

			scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
			scanner.SetReorgStore(store)
			_, _, _, err = scanner.discoverReorgReplacement(100)

			var recovery *btypes.ScannerRecoveryRequiredError
			if !errors.As(err, &recovery) {
				t.Fatalf("wrong block height must require operator recovery, got %v", err)
			}
			if recovery.CurrentHeight != 101 || recovery.NextHeight != 100 {
				t.Fatalf("unexpected recovery bounds: %+v", recovery)
			}
			if !strings.Contains(recovery.Reason, test.wantPhase) ||
				!strings.Contains(recovery.Reason, "requested height 100") ||
				!strings.Contains(recovery.Reason, "returned height 101") {
				t.Fatalf("unexpected recovery reason: %q", recovery.Reason)
			}
			if errors.Is(err, btypes.ErrUnavailableBlock) {
				t.Fatalf("structural height mismatch must not be transient: %v", err)
			}
			if mutationCalls.Load() != 0 {
				t.Fatalf("height mismatch reached scanner mutation: %d calls", mutationCalls.Load())
			}
		})
	}
}

func terminalReorgSidecar(t *testing.T, txID string, mined *atomic.Bool, height int64, blockHash, previousHash string) *sidecar.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/location") && strings.Contains(r.URL.Path, txID) && mined != nil && mined.Load():
			_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
				TxHash: txID, Height: height, BlockHash: blockHash, PreviousHash: previousHash,
			})
		case strings.HasSuffix(r.URL.Path, "/location"):
			http.Error(w, "not canonical", http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/exists"):
			_ = json.NewEncoder(w).Encode(sidecar.TxExistsResp{Exists: false})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func terminalReorgMeta(height int64, blockHash, previousHash, txID string, vaultOrigin bool) *reorgBlockMeta {
	meta := &reorgBlockMeta{
		Height: height, BlockHash: blockHash, PreviousHash: previousHash,
		CustomerTransactions: []string{normalizeTxID(txID)}, ProvenanceComplete: true,
	}
	if vaultOrigin {
		meta.VaultOriginTransactions = []string{normalizeTxID(txID)}
	}
	return meta
}

func TestVaultOriginAbsentReorgHoldsAcrossRestartAndBindsLaterCanonical(t *testing.T) {
	txID := testXMRHash("vault-origin-absent-hold")
	blockHash := testXMRHash("vault-origin-later-block")
	previousHash := testXMRHash("vault-origin-later-parent")
	var mined atomic.Bool
	client := terminalReorgSidecar(t, txID, &mined, 50, blockHash, previousHash)
	store := newScannerReceiptStore(t)
	original := terminalReorgMeta(40, "orphan", "parent", txID, true)
	if err := store.Set(original); err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	if err := scanner.emitReorgErrata(original, nil); err != nil {
		t.Fatal(err)
	}
	watches, err := store.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].VaultOrigin || !watches[0].HoldAbsent {
		t.Fatalf("absent vault outbound was not durably held: %#v err=%v", watches, err)
	}

	restarted := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	restarted.SetReorgStore(NewReorgStore(store.db))
	restarted.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	if err = restarted.reconcileRelocationWatches(); err != nil {
		t.Fatal(err)
	}
	watches, err = store.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].HoldAbsent {
		t.Fatalf("restart lost absent safety hold: %#v err=%v", watches, err)
	}

	if err = store.Set(&reorgBlockMeta{
		Height: 50, BlockHash: blockHash, PreviousHash: previousHash, ProvenanceComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	mined.Store(true)
	if err = restarted.reconcileRelocationWatches(); err != nil {
		t.Fatal(err)
	}
	watches, err = store.ListRelocationWatches(0)
	if err != nil || len(watches) != 0 {
		t.Fatalf("canonical remine did not clear hold: %#v err=%v", watches, err)
	}
	meta, err := store.Get(50)
	if err != nil || meta == nil || !reorgMetaHasVaultOrigin(meta, txID) ||
		!containsNormalizedTxID(meta.CustomerTransactions, txID) {
		t.Fatalf("canonical binding lost vault provenance: %#v err=%v", meta, err)
	}
}

func TestOrdinaryAbsentInboundErrataWaitsForConfirmedRevertBeforeQuarantine(t *testing.T) {
	txID := testXMRHash("ordinary-absent-inbound")
	client := terminalReorgSidecar(t, txID, nil, 0, "", "")
	store := newScannerReceiptStore(t)
	queue := make(chan stypes.ErrataBlock, 1)
	var quarantines atomic.Int32
	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	scanner.SetErrataQueue(queue)
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	scanner.SetClaimQuarantineHandler(func(got string) error {
		if normalizeTxID(got) != normalizeTxID(txID) {
			t.Fatalf("unexpected quarantined tx %s", got)
		}
		quarantines.Add(1)
		return nil
	})
	go func() {
		block := <-queue
		block.OnStored()
	}()
	meta := terminalReorgMeta(40, "orphan", "parent", txID, false)
	if err := scanner.emitReorgErrata(meta, nil); err != nil {
		t.Fatal(err)
	}
	if quarantines.Load() != 0 {
		t.Fatalf("pending ordinary inbound errata quarantined claim early: %d", quarantines.Load())
	}
	if reverted, err := store.IsRevertedTx(txID); err != nil || reverted {
		t.Fatalf("pending ordinary inbound errata reached permanent quarantine: %t %v", reverted, err)
	}
}

func TestLocallySignedAmbiguousTransactionIsHeldWithPersistedAttempt(t *testing.T) {
	txID := testXMRHash("local-signed-ambiguous-hold")
	client := terminalReorgSidecar(t, txID, nil, 0, "", "")
	reorgStore := newScannerReceiptStore(t)
	signedStore := newSignedTxPayloadStore(reorgStore.db)
	plan := standaloneClaimPlan(testXMRHash("local-signed-logical"))
	if err := signedStore.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	if err := signedStore.Set(plan, txID, standalonePayload(txID)); err != nil {
		t.Fatal(err)
	}
	dispatchErr := errors.New("ambiguous dispatch")
	if _, err := signedStore.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		txID, plan.MsgID, plan.PlanHash,
		func() (string, error) { return "", dispatchErr },
	); !errors.Is(err, dispatchErr) {
		t.Fatalf("persist broadcast attempt: %v", err)
	}
	signedStore.mu.Lock()
	_, attemptFound, attemptErr := signedStore.broadcastAttemptLocked(plan.LogicalHash)
	signedStore.mu.Unlock()
	if attemptErr != nil || !attemptFound {
		t.Fatalf("broadcast attempt not persisted: found=%t err=%v", attemptFound, attemptErr)
	}

	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(reorgStore)
	scanner.SetLocalSignedTxHandler(signedStore.IsLocallySignedTx)
	meta := terminalReorgMeta(40, "orphan", "parent", txID, false)
	if err := scanner.emitReorgErrata(meta, nil); err != nil {
		t.Fatal(err)
	}
	watches, err := reorgStore.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].VaultOrigin || !watches[0].HoldAbsent {
		t.Fatalf("locally signed ambiguous tx was not held: %#v err=%v", watches, err)
	}
}

func TestMigrationDestinationSenderAuthenticatesSourceAsgardOrigin(t *testing.T) {
	sourceVault, sourceAddress := testVaultPubKeyAndAddress(t)
	destinationVault := testVaultPubKey(t, "migration-destination-vault")
	txID := testXMRHash("migration-destination-origin")
	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.asgardLookup[sourceAddress.String()] = sourceVault
	item := &stypes.TxInItem{
		Tx: txID, Sender: sourceAddress.String(), To: testOutboundXMRDestination().String(),
		ObservedVaultPubKey: destinationVault, BlockHeight: 40, Memo: "MIGRATE:40",
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(5))),
	}
	if isAuthenticatedXMRVaultOrigin(item) {
		t.Fatal("destination view unexpectedly matched its observed destination vault")
	}
	meta := scanner.blockMetaForItems(sidecar.ChainBlock{Hash: "h40", PreviousHash: "h39"}, 40, []*stypes.TxInItem{item})
	if !meta.ProvenanceComplete || !reorgMetaHasVaultOrigin(meta, txID) {
		t.Fatalf("source Asgard sender was not persisted as vault origin: %#v", meta)
	}
}

func TestLateMigrationProvenanceUpgradeSurvivesRestartAndPreventsErrata(t *testing.T) {
	txID := testXMRHash("late-migration-watch-upgrade")
	client := terminalReorgSidecar(t, txID, nil, 0, "", "")
	store := newScannerReceiptStore(t)
	original := terminalReorgMeta(40, "orphan", "parent", txID, false)
	if err := store.Set(original); err != nil {
		t.Fatal(err)
	}
	if err := store.WatchRelocation(scannerRelocationWatch{TxID: txID, OriginalHeight: 40, InPool: true}); err != nil {
		t.Fatal(err)
	}
	addVaultOriginToMeta(original, txID)
	if err := store.Set(original); err != nil {
		t.Fatal(err)
	}

	restarted := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	restarted.SetReorgStore(NewReorgStore(store.db))
	restarted.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	if err := restarted.reconcileRelocationWatches(); err != nil {
		t.Fatal(err)
	}
	watches, err := store.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].VaultOrigin || !watches[0].HoldAbsent {
		t.Fatalf("late proof did not upgrade stale watch: %#v err=%v", watches, err)
	}
}

func TestRelocationWatchVaultProvenanceCannotBeDowngraded(t *testing.T) {
	store := newScannerReceiptStore(t)
	txID := testXMRHash("watch-downgrade")
	if err := store.WatchRelocation(scannerRelocationWatch{
		TxID: txID, OriginalHeight: 40, HoldAbsent: true, VaultOrigin: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.WatchRelocation(scannerRelocationWatch{
		TxID: txID, OriginalHeight: 40, InPool: true,
	}); err != nil {
		t.Fatal(err)
	}
	watches, err := store.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].VaultOrigin || !watches[0].InPool {
		t.Fatalf("stale writer downgraded vault provenance: %#v err=%v", watches, err)
	}
}

func TestLegacyReorgMetaWithoutProvenanceFailsClosed(t *testing.T) {
	txID := testXMRHash("legacy-journal-origin")
	client := terminalReorgSidecar(t, txID, nil, 0, "", "")
	store := newScannerReceiptStore(t)
	legacy := []byte(`{"height":40,"block_hash":"orphan","previous_hash":"parent","customer_transactions":["` + txID + `"]}`)
	if err := store.db.Put([]byte(reorgBlockMetaKey(40)), legacy, syncScannerWrite); err != nil {
		t.Fatal(err)
	}
	meta, err := store.Get(40)
	if err != nil || meta == nil || meta.ProvenanceComplete {
		t.Fatalf("legacy fixture did not decode as incomplete: %#v err=%v", meta, err)
	}
	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	if err = scanner.emitReorgErrata(meta, nil); err != nil {
		t.Fatal(err)
	}
	watches, err := store.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].VaultOrigin || !watches[0].HoldAbsent {
		t.Fatalf("legacy journal did not fail closed: %#v err=%v", watches, err)
	}
}

func TestRelocationWatchRoundRobinDoesNotStarveLaterEntry(t *testing.T) {
	resolvingTx := strings.Repeat("0", 63) + "5"
	blockHash := testXMRHash("round-robin-block")
	previousHash := testXMRHash("round-robin-parent")
	var mined atomic.Bool
	mined.Store(true)
	client := terminalReorgSidecar(t, resolvingTx, &mined, 90, blockHash, previousHash)
	store := newScannerReceiptStore(t)
	for i := byte('1'); i <= '4'; i++ {
		txID := strings.Repeat("0", 63) + string(i)
		if err := store.WatchRelocation(scannerRelocationWatch{
			TxID: txID, OriginalHeight: 40, HoldAbsent: true, VaultOrigin: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WatchRelocation(scannerRelocationWatch{
		TxID: resolvingTx, OriginalHeight: 40, VaultOrigin: true,
		LocationHeight: 90, BlockHash: blockHash, PreviousHash: previousHash,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(&reorgBlockMeta{
		Height: 90, BlockHash: blockHash, PreviousHash: previousHash, ProvenanceComplete: true,
	}); err != nil {
		t.Fatal(err)
	}
	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	if err := scanner.reconcileRelocationWatches(); err != nil {
		t.Fatal(err)
	}
	if err := scanner.reconcileRelocationWatches(); err != nil {
		t.Fatal(err)
	}
	watches, err := store.ListRelocationWatches(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, watch := range watches {
		if watch.TxID == resolvingTx {
			t.Fatalf("later resolvable watch was starved: %#v", watches)
		}
	}
}

func TestRelocationWatchQuotaReconcilesStaleEntriesBeforeRetry(t *testing.T) {
	newTx := strings.Repeat("f", 64)
	blockHash := testXMRHash("quota-new-location")
	previousHash := testXMRHash("quota-new-parent")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, newTx) && strings.HasSuffix(r.URL.Path, "/location"):
			_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
				TxHash: newTx, Height: 90, BlockHash: blockHash, PreviousHash: previousHash,
			})
		case strings.HasSuffix(r.URL.Path, "/location"):
			http.Error(w, "not canonical", http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/exists"):
			_ = json.NewEncoder(w).Encode(sidecar.TxExistsResp{Exists: false})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store := newScannerReceiptStore(t)
	for i := 0; i < maxOrdinaryScannerRelocationWatches; i++ {
		txID := strings.Repeat("0", 60) + string([]byte("0123456789abcdef")[(i>>4)&15]) +
			string([]byte("0123456789abcdef")[i&15]) + "aa"
		if err = store.WatchRelocation(scannerRelocationWatch{TxID: txID, OriginalHeight: 40, InPool: true}); err != nil {
			t.Fatalf("fill ordinary watch quota at %d: %v", i, err)
		}
	}
	queue := make(chan stypes.ErrataBlock, 8)
	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(store)
	scanner.SetErrataQueue(queue)
	scanner.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	scanner.SetClaimQuarantineHandler(func(string) error { return nil })
	done := make(chan struct{})
	go func() {
		defer close(done)
		for block := range queue {
			block.OnStored()
		}
	}()
	if err = scanner.emitReorgErrata(terminalReorgMeta(40, "orphan", "parent", newTx, false), nil); err != nil {
		t.Fatalf("quota recovery did not reconcile and retry: %v", err)
	}
	close(queue)
	<-done
	watches, err := store.ListRelocationWatches(0)
	if err != nil {
		t.Fatal(err)
	}
	if !containsWatchTxID(watches, newTx) {
		t.Fatalf("new relocation watch was not inserted after stale-page recovery: count=%d", len(watches))
	}
}

func TestRelocatedLateProvenanceMetaSurvivesPruneAndHoldsAfterRestart(t *testing.T) {
	txID := testXMRHash("relocated-late-provenance")
	client := terminalReorgSidecar(t, txID, nil, 0, "", "")
	store := newScannerReceiptStore(t)
	original := terminalReorgMeta(40, "old40", "h39", txID, false)
	location := terminalReorgMeta(50, "old50", "h49", txID, false)
	if err := store.Set(original); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(location); err != nil {
		t.Fatal(err)
	}
	if err := store.WatchRelocation(scannerRelocationWatch{
		TxID: txID, OriginalHeight: 40, LocationHeight: 50,
		BlockHash: location.BlockHash, PreviousHash: location.PreviousHash,
	}); err != nil {
		t.Fatal(err)
	}
	// A late authenticated migration proof was attached at the last known
	// location. Both referenced metas must survive ordinary journal pruning.
	addVaultOriginToMeta(location, txID)
	if err := store.Set(location); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneBefore(100); err != nil {
		t.Fatal(err)
	}
	for _, height := range []int64{40, 50} {
		if meta, err := store.Get(height); err != nil || meta == nil {
			t.Fatalf("watch-referenced provenance meta %d was pruned: %#v err=%v", height, meta, err)
		}
	}

	restarted := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)
	restarted.SetReorgStore(NewReorgStore(store.db))
	restarted.SetLocalSignedTxHandler(func(string) (bool, error) { return false, nil })
	if err := restarted.reconcileRelocationWatches(); err != nil {
		t.Fatal(err)
	}
	watches, err := store.ListRelocationWatches(0)
	if err != nil || len(watches) != 1 || !watches[0].VaultOrigin || !watches[0].HoldAbsent {
		t.Fatalf("relocated late provenance did not fail closed after restart: %#v err=%v", watches, err)
	}
}

func TestAuthenticatedHoldEntriesDoNotConsumeOrdinaryWatchQuota(t *testing.T) {
	store := newScannerReceiptStore(t)
	for i := 0; i < maxOrdinaryScannerRelocationWatches+16; i++ {
		txID := testXMRHash("unbounded-authenticated-hold-" + string(rune(i)))
		if err := store.WatchRelocation(scannerRelocationWatch{
			TxID: txID, OriginalHeight: 40, HoldAbsent: true, VaultOrigin: true,
		}); err != nil {
			t.Fatalf("authenticated hold %d consumed ordinary quota: %v", i, err)
		}
	}
}

func TestExactDedupeComposesEveryDurableCallbackOnce(t *testing.T) {
	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	first := scannerReceiptItem("callback-compose", 40)
	second := first.Copy()
	var firstStored, secondStored atomic.Int32
	first.OnObservedTxStored = func() { firstStored.Add(1) }
	second.OnObservedTxStored = func() { secondStored.Add(1) }
	items := scanner.dedupeTxInItemsByTxID([]*stypes.TxInItem{first, second})
	if len(items) != 1 || items[0].OnObservedTxStored == nil {
		t.Fatalf("exact duplicate callbacks were not composed: %#v", items)
	}
	items[0].OnObservedTxStored()
	items[0].OnObservedTxStored()
	if firstStored.Load() != 1 || secondStored.Load() != 1 {
		t.Fatalf("durable callbacks did not each fire once: first=%d second=%d", firstStored.Load(), secondStored.Load())
	}
}

func TestGenuineSameTxIDOutputsStillAggregate(t *testing.T) {
	scanner := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	first := scannerReceiptItem("genuine-two-output", 40)
	second := first.Copy()
	first.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(3)))
	second.Coins = common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(4)))
	items := scanner.dedupeTxInItemsByTxID([]*stypes.TxInItem{first, second})
	if len(items) != 1 || items[0].Coins.GetCoin(common.XMRAsset).Amount.Uint64() != 7 {
		t.Fatalf("genuine same-TxID outputs did not sum exactly once: %#v", items)
	}
}

func TestEventFirstPermanentObserverRejectionQuarantinesBeforeReceiptAckAcrossRestart(t *testing.T) {
	store := newScannerReceiptStore(t)
	txID := testXMRHash("event-first-permanent-rejection")
	item := scannerReceiptItem("event-first-permanent-rejection", 40)
	item.Tx = txID
	meta := terminalReorgMeta(40, "h40", "h39", txID, false)
	if err := store.Set(meta); err != nil {
		t.Fatal(err)
	}
	if err := store.PutObservationReceipt(meta, []*stypes.TxInItem{item}); err != nil {
		t.Fatal(err)
	}

	// First process models a disk failure after Observer has already reached its
	// authoritative tombstone. The receipt must remain retryable.
	first := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	first.SetReorgStore(store)
	first.SetClaimQuarantineHandler(func(string) error { return errors.New("disk unavailable") })
	claims, err := first.canonicalObservationReceipts()
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := first.prepareCanonicalObservationHandoff(claims[0].entry.Items, claims, nil)
	if err != nil || len(handoff) != 1 {
		t.Fatalf("prepare first handoff: items=%d err=%v", len(handoff), err)
	}
	handoff[0].OnObservedTxRejected(stypes.ErrObservedTxPermanentlyReverted)
	if remaining, listErr := store.ListObservationReceipts(); listErr != nil || len(remaining) != 1 {
		t.Fatalf("quarantine failure incorrectly acknowledged receipt: %#v err=%v", remaining, listErr)
	}

	// Restart retries the event, fsyncs signed-claim and scanner quarantine, and
	// only then removes the durable receipt.
	signedStore := newSignedTxPayloadStore(store.db)
	restarted := NewScanner(log.Logger, nil, nil, common.XMRChain, common.EmptyPubKey, 72)
	restarted.SetReorgStore(NewReorgStore(store.db))
	restarted.SetClaimQuarantineHandler(signedStore.QuarantineReverted)
	claims, err = restarted.canonicalObservationReceipts()
	if err != nil {
		t.Fatal(err)
	}
	handoff, err = restarted.prepareCanonicalObservationHandoff(claims[0].entry.Items, claims, nil)
	if err != nil || len(handoff) != 1 {
		t.Fatalf("prepare restarted handoff: items=%d err=%v", len(handoff), err)
	}
	handoff[0].OnObservedTxRejected(stypes.ErrObservedTxPermanentlyReverted)
	if remaining, listErr := store.ListObservationReceipts(); listErr != nil || len(remaining) != 0 {
		t.Fatalf("durably quarantined rejection did not acknowledge receipt: %#v err=%v", remaining, listErr)
	}
	if reverted, revertedErr := store.IsRevertedTx(txID); revertedErr != nil || !reverted {
		t.Fatalf("scanner quarantine missing before receipt ack: reverted=%t err=%v", reverted, revertedErr)
	}
	signedStore.mu.Lock()
	quarantined, quarantineErr := signedStore.txQuarantinedLocked(txID)
	signedStore.mu.Unlock()
	if quarantineErr != nil || !quarantined {
		t.Fatalf("signed-claim quarantine missing before receipt ack: quarantined=%t err=%v", quarantined, quarantineErr)
	}
}

func containsNormalizedTxID(values []string, txID string) bool {
	want := normalizeTxID(txID)
	for _, value := range values {
		if normalizeTxID(value) == want {
			return true
		}
	}
	return false
}

func containsWatchTxID(watches []scannerRelocationWatch, txID string) bool {
	want := normalizeTxID(txID)
	for _, watch := range watches {
		if normalizeTxID(watch.TxID) == want {
			return true
		}
	}
	return false
}
