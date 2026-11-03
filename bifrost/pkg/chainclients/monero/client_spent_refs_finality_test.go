package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

// Exercise the production HTTP import and broadcast paths: mark-spent verifies
// transaction visibility, including mempool visibility, not canonical finality.
func TestSpentRefImportKeepsExactBroadcastLiveUntilAuthoritativeObservation(t *testing.T) {
	for _, memoText := range []string{"OUT:" + testXMRHash("import-finality-inbound"), "MIGRATE:100", "CONSOLIDATE"} {
		for _, finaliseHeight := range []int64{100, 110} {
			t.Run(memoText+"/"+strconv.FormatInt(finaliseHeight, 10), func(t *testing.T) {
				vault, vaultAddress := testVaultPubKeyAndAddress(t)
				tx := stypes.TxOutItem{
					Chain:            common.XMRChain,
					ToAddress:        testOutboundXMRDestination(),
					VaultPubKey:      vault,
					VaultPubKeyFrost: vault,
					Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
					Memo:             memoText,
					SigningID:        "import-finality-consolidation",
				}
				if memoText == "CONSOLIDATE" {
					tx.ToAddress = vaultAddress
				}
				identity, err := xmrSigningCacheIdentity(tx)
				require.NoError(t, err)
				payload := testBroadcastPayload(t, "import-finality-spend", "deadbeef", "import-finality-message")
				payload.FeeAtomic = moneroAtomicToThorchain
				payload.Eventuality, err = tssp.BuildFrostEventuality(payload.Eventuality.PlanHash,
					payload.TxHash, payload.TxMetadata, payload.TxKey, payload.FeeAtomic)
				require.NoError(t, err)
				ref := testXMROutputRef("import-finality-input", 0, payload.TxHash)
				payload.SpentOutputRefs = xmrSpentRefsToSidecar([]common.OutputRef{*ref})
				payload.ClaimCacheVault = txCacheVault(tx, common.XMRChain)
				payload.ClaimCacheHash = identity.CacheHash
				payload.ClaimLogicalHash = identity.LogicalHash
				payload.ClaimOwnerVault = vault.String()
				payloadBytes, err := json.Marshal(payload)
				require.NoError(t, err)
				plan := testSignedTxClaimPlan(payload.ClaimCacheVault, identity.CacheHash, identity.LogicalHash,
					vault.String(), payload.MsgID, payload.Eventuality.PlanHash, identity.ObserveKeys...)
				plan.TxOutJSON, err = encodeDurableXMRClaimTxOut(tx)
				require.NoError(t, err)

				storeDir := filepath.Join(t.TempDir(), "claims")
				db, err := leveldb.OpenFile(storeDir, nil)
				require.NoError(t, err)
				t.Cleanup(func() { _ = db.Close() })
				store := newSignedTxPayloadStore(db)
				require.NoError(t, store.Reserve(plan))
				require.NoError(t, store.Set(plan, payload.TxHash, payloadBytes))

				broadcasts := make(chan sidecar.BroadcastReq, 4)
				imports := make(chan sidecar.MarkSpentReq, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v1/monero/outputs/mark-spent":
						var req sidecar.MarkSpentReq
						if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
							http.Error(w, decodeErr.Error(), http.StatusBadRequest)
							return
						}
						imports <- req
						_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
					case "/v1/monero/broadcast":
						var req sidecar.BroadcastReq
						if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
							http.Error(w, decodeErr.Error(), http.StatusBadRequest)
							return
						}
						broadcasts <- req
						_ = json.NewEncoder(w).Encode(sidecar.BroadcastResp{TxHash: req.TxHash, TxKey: req.TxKey})
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
				require.NoError(t, err)
				client := &Client{
					chain: common.XMRChain, sidecar: signer, logger: testLogger(),
					signedTxPayloadStore: store, signerCacheManager: newTestSignerCacheManager(t),
				}
				observation := client.buildOutboundObservation(tx, payload.TxHash, payload.TxKey, payload.FeeAtomic, payload.SpentOutputRefs)
				require.NotNil(t, observation)
				observation.BlockHeight = 100 // The signing-time scanner cursor is not inclusion evidence.
				imported := common.NewObservedTx(common.NewTx(common.TxID(observation.Tx), vaultAddress,
					tx.ToAddress, observation.Coins, observation.Gas, observation.Memo), 100, vault, finaliseHeight)
				imported.XmrTxKey = observation.TxKey
				imported.SpentOutputRefs = []common.OutputRef{*ref}

				applied, retry := client.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: imported, WireMemo: imported.Tx.Memo})
				require.True(t, applied)
				require.False(t, retry)
				require.Len(t, imports, 1)
				request := <-imports
				require.False(t, request.SkipVerification)
				require.Equal(t, payload.TxHash, request.TxHash)

				assertExactBroadcast := func() {
					t.Helper()
					recovered, _, _, found, recoverErr := client.recoverSignedXMRTxPayload(tx, identity)
					require.NoError(t, recoverErr)
					require.True(t, found)
					require.Equal(t, payloadBytes, recovered, "visibility-only import must retain the exact recoverable payload")
					txHash, broadcastErr := client.BroadcastTx(tx, recovered)
					require.NoError(t, broadcastErr)
					require.Equal(t, payload.TxHash, txHash)
					select {
					case req := <-broadcasts:
						require.Equal(t, payload.TxMetadata, req.TxHex)
						require.Equal(t, payload.MsgID, req.MsgID)
						require.Equal(t, payload.TxKey, req.TxKey)
					default:
						t.Fatal("visibility-only spent-ref import reported broadcast success without dispatching the exact signed bytes")
					}
				}
				assertExactBroadcast()
				// Reopen the actual LevelDB, not only the client wrapper, to prove
				// imports neither terminalize nor release the durable signing claim.
				require.NoError(t, db.Close())
				db, err = leveldb.OpenFile(storeDir, nil)
				require.NoError(t, err)
				client.signedTxPayloadStore = newSignedTxPayloadStore(db)
				assertExactBroadcast()
				conflict := plan
				conflict.MsgID = "independent-second-ceremony"
				conflict.PlanHash = testXMRHash("independent-second-plan")
				require.Error(t, client.signedTxPayloadStore.Reserve(conflict), "ShareIssued ownership must never reopen")

				// Only the observer's authoritative chain-commit callback can
				// suppress exact broadcast. Reimport must not undo that decision.
				observation.CommittedUnFinalised = true
				require.NoError(t, client.OnObservedTxInDurable(*observation, 100))
				applied, retry = client.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: imported, WireMemo: imported.Tx.Memo})
				require.True(t, applied)
				require.False(t, retry)
				_, err = client.BroadcastTx(tx, payloadBytes)
				require.NoError(t, err)
				require.Empty(t, broadcasts, "committed observation must retain its reversible broadcast barrier")
				observation.CommittedUnFinalised = false
				require.NoError(t, client.OnObservedTxInDurable(*observation, 100))
				applied, retry = client.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: imported, WireMemo: imported.Tx.Memo})
				require.True(t, applied)
				require.False(t, retry)
				_, err = client.BroadcastTx(tx, payloadBytes)
				require.NoError(t, err)
				require.Empty(t, broadcasts, "authoritative final observation must suppress exact rebroadcast")
				_, _, err = client.signedTxPayloadStore.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
				require.ErrorIs(t, err, errXMRSigningClaimObserved)
				if memoText == "CONSOLIDATE" {
					next := plan
					next.CacheHash = testXMRHash("next-consolidation-cache")
					next.LogicalHash = testXMRHash("next-consolidation-logical")
					next.MsgID = "next-consolidation-message"
					next.ObserveEpoch, err = client.signedTxPayloadStore.SnapshotObserveEpochs(next.ObserveKeys)
					require.NoError(t, err)
					require.NoError(t, client.signedTxPayloadStore.Reserve(next), "old finalized imports must not reacquire a reusable alias")
				}
			})
		}
	}
}
