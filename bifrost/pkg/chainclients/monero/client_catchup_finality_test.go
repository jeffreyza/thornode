package monero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Exercise peer import -> non-final catch-up -> final catch-up through the
// production HTTP import paths and durable claim store. Both reusable aliases
// must remain closed until final catch-up, without losing exact local bytes.
func TestCatchupReusableAliasFinalityTransitions(t *testing.T) {
	for _, memoText := range []string{"CONSOLIDATE", ""} {
		for _, heights := range [][2]int64{{100, 110}, {0, 0}, {100, 0}, {0, 100}} {
			for _, localClaim := range []bool{false, true} {
				t.Run(fmt.Sprintf("memo=%q/heights=%v/local=%t", memoText, heights, localClaim), func(t *testing.T) {
					vault, vaultAddress := testVaultPubKeyAndAddress(t)
					txHash := testXMRHash("catchup-transition-spend")
					ref := testXMROutputRef("catchup-transition-input", 0, txHash)
					tx := common.Tx{
						ID: common.TxID(txHash), Chain: common.XMRChain,
						FromAddress: vaultAddress, ToAddress: testOutboundXMRDestination(),
						Coins: common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2))),
						Gas:   common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}, Memo: memoText,
					}
					if memoText == "CONSOLIDATE" {
						tx.ToAddress = vaultAddress
					}
					voter := &ttypes.QueryObservedTxVoter{
						TxID: tx.ID, Height: 90,
						Tx: ttypes.QueryObservedTx{
							Tx: tx, BlockHeight: heights[0], FinaliseHeight: heights[1],
							ObservedPubKey: vault, XmrTxKey: strings.Repeat("7", 64),
						},
					}
					imports := make(chan sidecar.MarkSpentReq, 4)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var req sidecar.MarkSpentReq
						if r.URL.Path != "/v1/monero/outputs/mark-spent" || json.NewDecoder(r.Body).Decode(&req) != nil {
							http.Error(w, "unexpected request", http.StatusBadRequest)
							return
						}
						imports <- req
						_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
					}))
					defer server.Close()
					signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
					require.NoError(t, err)
					store := newTestSignedTxPayloadStore(t)
					client := &Client{
						chain: common.XMRChain, sidecar: signer, logger: testLogger(),
						signedTxPayloadStore: store, signerCacheManager: newTestSignerCacheManager(t),
						thorchainBridge: &mockMoneroBridge{
							spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): {ref}},
							observedByTx:        map[string]*ttypes.QueryObservedTxVoter{txHash: voter},
							pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
							mimirValues:         map[string]int64{"HaltScannerXMR": 0},
						},
					}
					alias := xmrScheduledObserveKey(vault, tx.ToAddress.String(), tx.Coins, memoText)
					if memoText == "CONSOLIDATE" {
						alias = xmrConsolidationObserveKey(vault)
					}
					plan := testSignedTxClaimPlan("catchup-cache-vault", testXMRHash("catchup-cache"),
						testXMRHash("catchup-logical"), vault.String(), "catchup-msg", testXMRHash("catchup-plan"), alias)
					payload := testStoredSignedTxPayload(t, txHash, "deadbeef")
					if localClaim {
						require.NoError(t, store.Reserve(plan))
						require.NoError(t, store.Set(plan, txHash, payload))
					}
					next := testSignedTxClaimPlan("catchup-cache-vault", testXMRHash("catchup-next-cache"),
						testXMRHash("catchup-next-logical"), vault.String(), "catchup-next-msg", testXMRHash("catchup-next-plan"), alias)
					assertBlocked := func() {
						t.Helper()
						next.ObserveEpoch, err = store.SnapshotObserveEpochs(next.ObserveKeys)
						require.NoError(t, err)
						require.Error(t, store.Reserve(next), "non-final evidence must not release reusable ownership")
						if localClaim {
							got, found, getErr := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
							require.NoError(t, getErr)
							require.True(t, found)
							require.Equal(t, payload, got, "non-final import must retain exact rebroadcast bytes")
						}
					}

					// Even final-looking peer heights are not authoritative finality.
					peer := common.NewObservedTx(tx, 100, vault, 100)
					peer.SpentOutputRefs = []common.OutputRef{*ref}
					peer.XmrTxKey = voter.Tx.XmrTxKey
					allowSpentOutputRefsImports(client)
					applied, retry := client.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: peer, WireMemo: peer.Tx.Memo})
					require.True(t, applied)
					require.False(t, retry)
					assertBlocked()

					allowSpentOutputRefsImports(client)
					client.catchupSpentOutputRefs()
					importCount := 2
					if heights[0] <= 0 || heights[1] <= 0 {
						// Invalid heights are rejected before reaching the sidecar.
						importCount = 1
					}
					require.Len(t, imports, importCount)
					assertBlocked()

					voter.Tx.BlockHeight = 100
					voter.Tx.FinaliseHeight = 100
					voter.FinalisedHeight = 110
					allowSpentOutputRefsImports(client)
					client.catchupSpentOutputRefs()
					importCount++
					require.Len(t, imports, importCount)
					for range importCount {
						req := <-imports
						require.False(t, req.SkipVerification)
						require.Equal(t, txHash, req.TxHash)
					}
					if localClaim {
						_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
						require.ErrorIs(t, err, errXMRSigningClaimObserved)
					}
					next.ObserveEpoch, err = store.SnapshotObserveEpochs(next.ObserveKeys)
					require.NoError(t, err)
					require.NoError(t, store.Reserve(next), "final catch-up must release the reusable alias")
					// Periodic replay of the old final result must not poison its successor.
					allowSpentOutputRefsImports(client)
					client.catchupSpentOutputRefs()
					require.Len(t, imports, 1)
					require.NoError(t, store.Reserve(next))
				})
			}
		}
	}
}
