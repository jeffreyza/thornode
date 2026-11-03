package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A node that learns about a finalized consolidation only through periodic
// catch-up (no local claim, no own observation) must still be able to reserve
// the vault's next consolidation.
func TestCatchupConsolidationImportKeepsNextConsolidationReservable(t *testing.T) {
	vault := testVaultPubKey(t, "catchup-consolidation")
	vaultAddress, err := vault.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("catchup-consolidation-spend")
	refs := []*common.OutputRef{testXMROutputRef("catchup-consolidation-input", 0, spendTxHash)}
	voter := &ttypes.QueryObservedTxVoter{
		TxID:            common.TxID(spendTxHash),
		Height:          100,
		FinalisedHeight: 110,
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID:          common.TxID(spendTxHash),
				Chain:       common.XMRChain,
				FromAddress: vaultAddress,
				ToAddress:   vaultAddress,
				Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2))),
				Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
				Memo:        "CONSOLIDATE",
			},
			BlockHeight:    100,
			ObservedPubKey: vault,
			FinaliseHeight: 100,
			XmrTxKey:       strings.Repeat("7", 64),
		},
	}
	var marked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.MarkSpentReq
		if r.URL.Path != "/v1/monero/outputs/mark-spent" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		marked.Add(1)
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	store := newTestSignedTxPayloadStore(t)
	c := &Client{
		chain:                common.XMRChain,
		sidecar:              signer,
		logger:               testLogger(),
		signerCacheManager:   newTestSignerCacheManager(t),
		signedTxPayloadStore: store,
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
			mimirValues:         map[string]int64{"HaltScannerXMR": 0},
		},
	}

	allowSpentOutputRefsImports(c)
	c.catchupSpentOutputRefs()
	require.EqualValues(t, 1, marked.Load(), "catch-up must import the consolidation")

	alias := xmrConsolidationObserveKey(vault)
	epochs, err := store.SnapshotObserveEpochs([]string{alias})
	require.NoError(t, err)
	next := testSignedTxClaimPlan(
		"catchup-cache-vault-XMR",
		testXMRHash("catchup-next-cache"),
		testXMRHash("catchup-next-logical"),
		vault.String(),
		"catchup-next-msg",
		testXMRHash("catchup-next-plan"),
		alias,
	)
	next.ObserveEpoch = epochs
	require.NoError(t, store.Reserve(next), "a finalized catch-up import must not keep the reusable consolidation alias locked")
}
