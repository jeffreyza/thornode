package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestLateMigrationReplayIncludesOnlyEligibleSourceProof(t *testing.T) {
	for _, tc := range []struct {
		name       string
		origin     string
		eligible   bool
		wantSource bool
	}{
		{"certified source", sidecar.OutgoingOriginVaultCertified, true, true},
		{"local source", sidecar.OutgoingOriginLocalCombined, true, true},
		{"ineligible source", sidecar.OutgoingOriginVaultCertified, false, false},
		{"committed pairing", sidecar.OutgoingOriginChainCommitted, false, false},
		{"committed cannot promote itself", sidecar.OutgoingOriginChainCommitted, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, destination, txID, voter, refs := testXMRCatchupMigration(t, tc.name)
			blockHash := testXMRHash(tc.name + "-block")
			parentHash := testXMRHash(tc.name + "-parent")
			proof := sidecar.Transfer{
				TxID: txID, TxKey: voter.Tx.XmrTxKey,
				VaultPubKey: source.String(), FromAddress: voter.Tx.Tx.FromAddress.String(),
				Address: voter.Tx.Tx.ToAddress.String(), Amount: 2 * moneroAtomicToThorchain,
				Fee: moneroAtomicToThorchain, Height: 100, Note: voter.Tx.Tx.Memo,
				Origin: tc.origin, ObservationEligible: tc.eligible,
				SpentOutputRefs: []sidecar.SpentRef{{
					TxHash:      refs[0].TxHash,
					OutputIndex: refs[0].OutputIndex, KeyImage: refs[0].KeyImage, SpendTxHash: txID,
				}},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/monero/vaults/register":
					_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(201))
				case "/v1/monero/chain/tx/" + txID + "/location":
					_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
						TxHash: txID, Height: 100, BlockHash: blockHash, PreviousHash: parentHash,
					})
				case "/v1/monero/outputs/mark-observed":
					_ = json.NewEncoder(w).Encode(proof)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			scanner, store, _ := newXMRCatchupReplayScanner(t, signer, source, destination)
			// Authentication arrived after scanning the mined block. No scanner rewind
			// or synthetic vote may be required to recover its source observation.
			scanner.setCurrentHeight(200)
			observed := common.NewObservedTx(voter.Tx.Tx, 100, source, 100)
			observed.XmrTxKey = voter.Tx.XmrTxKey
			observed.SpentOutputRefs = []common.OutputRef{*refs[0]}
			queued, err := scanner.EnqueueAuthenticatedInternalInboundReplay(observed)
			require.NoError(t, err)
			require.True(t, queued)
			items, claims, err := scanner.replayAuthenticatedInternalInbounds(201, 201)
			require.NoError(t, err)
			require.Len(t, claims, 1)
			wantCount := 1
			if tc.wantSource {
				wantCount = 2
			}
			require.Len(t, items, wantCount)
			require.True(t, items[0].ObservedVaultPubKey.Equals(destination))
			require.Empty(t, items[0].TxKey)
			require.Empty(t, items[0].SpentOutputRefs)
			if !tc.wantSource {
				items[0].OnObservedTxStored()
				count, countErr := store.Count()
				require.NoError(t, countErr)
				require.Zero(t, count)
				return
			}
			require.True(t, items[1].ObservedVaultPubKey.Equals(source))
			require.Equal(t, int64(100), items[1].BlockHeight)
			require.Equal(t, voter.Tx.XmrTxKey, items[1].TxKey)
			require.Equal(t, refs[0].KeyImage, items[1].SpentOutputRefs[0].KeyImage)
			require.Equal(t, items[0].Coins, items[1].Coins)
			require.Equal(t, items[0].Gas, items[1].Gas)

			items[0].OnObservedTxStored()
			items[0].OnObservedTxStored() // duplicate callbacks cannot acknowledge the pair
			count, err := store.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count)
			items[1].OnObservedTxRejected(errors.New("observer persistence failed"))
			items, _, err = scanner.replayAuthenticatedInternalInbounds(202, 202)
			require.NoError(t, err)
			require.Len(t, items, 2, "a partial rejection must replay both observations")
			items[1].OnObservedTxStored()
			count, err = store.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count)
			// A process crash after storing one side must retain the durable record.
			require.NoError(t, store.ResetClaims())
			items, _, err = scanner.replayAuthenticatedInternalInbounds(203, 203)
			require.NoError(t, err)
			require.Len(t, items, 2)
			var wg sync.WaitGroup
			for _, item := range items {
				wg.Add(1)
				go func() { defer wg.Done(); item.OnObservedTxStored() }()
			}
			wg.Wait()
			count, err = store.Count()
			require.NoError(t, err)
			require.Zero(t, count, "only the complete durable pair may clear the replay")
		})
	}
}

func TestCommittedPayoutReconciliationRequiresCanonicalProofAndJournal(t *testing.T) {
	for _, scenario := range []string{"pruned", "admission-rejected", "journal-conflict", "journal-write-failure", "not-mined", "wrong-payment", "moved-during-import", "local-proof", "certified-proof"} {
		t.Run(scenario, func(t *testing.T) {
			source, destination, txID, voter, refs := testXMRCatchupMigration(t, scenario)
			voter.Tx.Tx.Memo = "OUT:" + testXMRHash("committed-inbound")
			blockHash, parentHash := testXMRHash("committed-block"), testXMRHash("committed-parent")
			proof := sidecar.Transfer{
				TxID: txID, TxKey: voter.Tx.XmrTxKey, VaultPubKey: source.String(), FromAddress: voter.Tx.Tx.FromAddress.String(),
				Address: voter.Tx.Tx.ToAddress.String(), Amount: 2 * moneroAtomicToThorchain, Fee: moneroAtomicToThorchain,
				Height: 100, Note: voter.Tx.Tx.Memo, Origin: sidecar.OutgoingOriginChainCommitted,
				SpentOutputRefs: []sidecar.SpentRef{{TxHash: refs[0].TxHash, OutputIndex: refs[0].OutputIndex, KeyImage: refs[0].KeyImage, SpendTxHash: txID}},
			}
			switch scenario {
			case "wrong-payment":
				proof.Amount += moneroAtomicToThorchain
			case "local-proof":
				proof.Origin, proof.ObservationEligible = sidecar.OutgoingOriginLocalCombined, true
			case "certified-proof":
				proof.Origin, proof.ObservationEligible = sidecar.OutgoingOriginVaultCertified, true
			}
			var requestMu sync.Mutex
			locationCalls, markCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestMu.Lock()
				defer requestMu.Unlock()
				switch r.URL.Path {
				case "/v1/monero/chain/tx/" + txID + "/location":
					locationCalls++
					if scenario == "not-mined" {
						http.NotFound(w, r)
						return
					}
					location := sidecar.CanonicalTxLocationResp{TxHash: txID, Height: 100, BlockHash: blockHash, PreviousHash: parentHash}
					if scenario == "moved-during-import" && locationCalls > 1 {
						location.BlockHash = testXMRHash("new-canonical-block")
					}
					_ = json.NewEncoder(w).Encode(location)
				case "/v1/monero/outputs/mark-observed":
					markCalls++
					_ = json.NewEncoder(w).Encode(proof)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			scanner, _, store := newXMRCatchupReplayScanner(t, signer, source, destination)
			scanner.SetManualVaultAddress(source, voter.Tx.Tx.FromAddress.String())
			scanner.setCurrentHeight(200)
			observed := common.NewObservedTx(voter.Tx.Tx, 110, source, 110)
			observed.XmrTxKey, observed.SpentOutputRefs = voter.Tx.XmrTxKey, []common.OutputRef{*refs[0]}
			switch scenario {
			case "journal-conflict":
				require.NoError(t, store.Set(&reorgBlockMeta{Height: 100, BlockHash: testXMRHash("journal-old-fork"), PreviousHash: parentHash, ProvenanceComplete: true}))
			case "journal-write-failure":
				require.NoError(t, store.db.Put([]byte(scannerRelocationWatchPrefix+normalizeTxID(txID)), []byte("malformed"), syncScannerWrite))
			}
			err = scanner.reconcileCommittedOutboundGuarded(observed, func() (func(), bool) {
				if scanner.scanMu.TryLock() {
					scanner.scanMu.Unlock()
					t.Error("admission must run under the scanner lock")
				}
				return func() {}, scenario != "admission-rejected"
			})
			switch scenario {
			case "admission-rejected":
				require.ErrorIs(t, err, errXMRSpentRefsAdmissionRejected)
			case "pruned", "local-proof", "certified-proof":
				require.NoError(t, err)
			case "journal-conflict":
				require.ErrorContains(t, err, "conflicts with canonical journal")
			case "journal-write-failure":
				require.ErrorContains(t, err, "malformed")
			case "wrong-payment":
				require.ErrorContains(t, err, "payment mismatch")
			case "moved-during-import":
				require.ErrorContains(t, err, "moved during mined reconciliation")
			default:
				require.Error(t, err)
			}
			requestMu.Lock()
			calls := markCalls
			requestMu.Unlock()
			meta, metaErr := store.Get(100)
			require.NoError(t, metaErr)
			switch scenario {
			case "admission-rejected", "not-mined", "journal-write-failure":
				require.Zero(t, calls)
				require.Nil(t, meta, "a failed atomic binding must not clear pending accounting")
			case "journal-conflict":
				require.Zero(t, calls)
				require.Equal(t, testXMRHash("journal-old-fork"), meta.BlockHash)
			default:
				require.Equal(t, 1, calls)
				require.True(t, reorgMetaHasVaultOrigin(meta, txID))
			}
			receipts, err := store.ListObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, receipts, "accounting reconciliation must not manufacture a source vote")
		})
	}
}

func TestLatePayoutReplayRecreatesPrunedJournalAndPersistsSource(t *testing.T) {
	for _, prefix := range []string{"OUT:", "REFUND:"} {
		t.Run(prefix, func(t *testing.T) {
			source, destination, txID, voter, refs := testXMRCatchupMigration(t, prefix)
			voter.Tx.Tx.Memo = prefix + testXMRHash("inbound")
			blockHash, parentHash := testXMRHash("late-block"), testXMRHash("late-parent")
			proof := sidecar.Transfer{
				TxID: txID, TxKey: voter.Tx.XmrTxKey, VaultPubKey: source.String(), FromAddress: voter.Tx.Tx.FromAddress.String(),
				Address: voter.Tx.Tx.ToAddress.String(), Amount: 2 * moneroAtomicToThorchain, Fee: moneroAtomicToThorchain,
				Height: 100, Note: voter.Tx.Tx.Memo, Origin: sidecar.OutgoingOriginVaultCertified, ObservationEligible: true,
				SpentOutputRefs: []sidecar.SpentRef{{TxHash: refs[0].TxHash, OutputIndex: refs[0].OutputIndex, KeyImage: refs[0].KeyImage, SpendTxHash: txID}},
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/monero/chain/tx/" + txID + "/location":
					_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{TxHash: txID, Height: 100, BlockHash: blockHash, PreviousHash: parentHash})
				case "/v1/monero/outputs/mark-observed":
					_ = json.NewEncoder(w).Encode(proof)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			scanner, _, _ := newXMRCatchupReplayScanner(t, signer, source, destination)
			scanner.setCurrentHeight(200)
			observed := common.NewObservedTx(voter.Tx.Tx, 110, source, 110)
			observed.XmrTxKey, observed.SpentOutputRefs = voter.Tx.XmrTxKey, []common.OutputRef{*refs[0]}
			// Final certificate height is not the mining height, and the real journal
			// has already expired under the 72-block retention window.
			_, err = scanner.reorgStore.PruneBefore(128)
			require.NoError(t, err)
			guardCalled := false
			queued, guardErr := scanner.enqueueAuthenticatedOutboundReplayGuarded(observed, func() (func(), bool) {
				guardCalled = true
				require.False(t, scanner.scanMu.TryLock(), "scanner lock must precede admission")
				return nil, false
			})
			require.True(t, guardCalled)
			require.ErrorIs(t, guardErr, errXMRSpentRefsAdmissionRejected)
			require.False(t, queued)
			absentBefore, readErr := scanner.reorgStore.Get(100)
			require.NoError(t, readErr)
			require.Nil(t, absentBefore)
			claimsBefore, readErr := scanner.canonicalObservationReceipts()
			require.NoError(t, readErr)
			require.Empty(t, claimsBefore)
			// A failure while preparing any part of the atomic write must leave
			// neither a receipt nor a partial journal behind.
			watchKey := []byte(scannerRelocationWatchPrefix + normalizeTxID(txID))
			require.NoError(t, scanner.reorgStore.db.Put(watchKey, []byte("malformed"), syncScannerWrite))
			_, err = scanner.EnqueueAuthenticatedOutboundReplay(observed)
			require.NoError(t, err)
			_, _, err = scanner.replayAuthenticatedInternalInbounds(201, 201)
			require.ErrorContains(t, err, "malformed")
			absent, err := scanner.reorgStore.Get(100)
			require.NoError(t, err)
			require.Nil(t, absent)
			emptyClaims, err := scanner.canonicalObservationReceipts()
			require.NoError(t, err)
			require.Empty(t, emptyClaims)
			require.NoError(t, scanner.reorgStore.db.Delete(watchKey, syncScannerWrite))
			require.NoError(t, scanner.reorgStore.WatchRelocation(scannerRelocationWatch{
				TxID: txID, OriginalHeight: 99, LocationHeight: 100, BlockHash: blockHash, PreviousHash: parentHash,
			}))
			queued, err = scanner.EnqueueAuthenticatedOutboundReplay(observed)
			require.NoError(t, err)
			require.True(t, queued)
			_, _, err = scanner.replayAuthenticatedInternalInbounds(201, 201)
			require.NoError(t, err)
			watches, err := scanner.reorgStore.ListRelocationWatches(1)
			require.NoError(t, err)
			require.Len(t, watches, 1)
			require.True(t, watches[0].VaultOrigin, "journal, receipt and relocation provenance commit together")
			meta, err := scanner.reorgStore.Get(100)
			require.NoError(t, err)
			require.Contains(t, meta.CustomerTransactions, normalizeTxID(txID))
			require.Contains(t, meta.VaultOriginTransactions, normalizeTxID(txID))
			_, err = scanner.reorgStore.PruneBefore(128)
			require.NoError(t, err)
			claims, err := scanner.canonicalObservationReceipts()
			require.NoError(t, err)
			require.Len(t, claims, 1)
			require.Len(t, claims[0].entry.Items, 1)
			item := claims[0].entry.Items[0]
			require.Equal(t, int64(100), item.BlockHeight)
			require.Equal(t, observed.Tx.Memo, item.Memo)
			require.Equal(t, source, item.ObservedVaultPubKey)
			require.NotEmpty(t, item.SpentOutputRefs)
			// Query replay never creates a fresh vote from a consensus-only proof.
			proof.Origin = sidecar.OutgoingOriginChainCommitted
			_, err = scanner.EnqueueAuthenticatedOutboundReplay(observed)
			require.NoError(t, err)
			_, _, err = scanner.replayAuthenticatedInternalInbounds(202, 202)
			require.NoError(t, err)
			count, err := scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Equal(t, 1, count, "ineligible proof must retain the pending replay")
			retainedReceipts, err := scanner.canonicalObservationReceipts()
			require.NoError(t, err)
			require.Equal(t, claims, retainedReceipts, "failed replay must not change the existing receipt")
		})
	}
}
