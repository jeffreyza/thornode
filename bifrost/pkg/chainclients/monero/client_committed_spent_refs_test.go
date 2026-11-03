package monero

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Migration observations share a tx ID but belong to different vault voters.
// Model the legacy endpoint's inbound preference separately from explicit out.
type migrationVoterBridge struct {
	*mockMoneroBridge
	inbound *ttypes.QueryObservedTxVoter
}

func (b *migrationVoterBridge) GetObservedTxVoter(string) (*ttypes.QueryObservedTxVoter, error) {
	return b.inbound, nil
}

func (b *migrationVoterBridge) GetObservedTxOutVoter(txID string) (*ttypes.QueryObservedTxVoter, error) {
	return b.mockMoneroBridge.GetObservedTxVoter(txID)
}

func (b *migrationVoterBridge) GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*ttypes.QueryObservedTxVoter, bool, error) {
	return b.mockMoneroBridge.GetObservedTxOutVoterUncached(ctx, txID)
}

func TestCommittedMigrationImportsUseOutboundVoter(t *testing.T) {
	for _, route := range []string{"live", "catchup"} {
		for _, state := range []string{"pending inbound", "consensus inbound"} {
			t.Run(route+"/"+state, func(t *testing.T) {
				vault, destination, txHash, outbound, refs := testXMRCatchupMigration(t, route+state)
				inbound := *outbound
				inbound.Tx.ObservedPubKey = destination
				if state == "pending inbound" {
					inbound.Height, inbound.FinalisedHeight = 0, 0
				}
				var marked atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request sidecar.MarkSpentReq
					if r.URL.Path != "/v1/monero/outputs/mark-spent" || json.NewDecoder(r.Body).Decode(&request) != nil {
						http.Error(w, "unexpected request", http.StatusBadRequest)
						return
					}
					assert.Equal(t, vault.String(), request.VaultPubKey)
					assert.Equal(t, outbound.Tx.Tx.Memo, request.Memo)
					assert.Equal(t, outbound.Tx.XmrTxKey, request.TxKey)
					marked.Add(1)
					_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(request.SpentRefs)})
				}))
				defer server.Close()
				signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
				require.NoError(t, err)
				c := &Client{
					chain:                  common.XMRChain,
					sidecar:                signer,
					logger:                 testLogger(),
					signerCacheManager:     newTestSignerCacheManager(t),
					signedTxPayloadStore:   newTestSignedTxPayloadStore(t),
					spentRefsRetryInterval: time.Millisecond,
					thorchainBridge: &migrationVoterBridge{
						mockMoneroBridge: &mockMoneroBridge{
							observedByTx:        map[string]*ttypes.QueryObservedTxVoter{txHash: outbound},
							spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
							pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
							mimirValues:         map[string]int64{"HaltScannerXMR": 0},
						},
						inbound: &inbound,
					},
				}
				if route == "live" {
					allowSpentOutputRefsImports(c)
					imported, applied := c.ApplyCommittedSpentOutputRefs(context.Background(), vault, common.TxID(txHash))
					require.True(t, applied, "destination voter must not hide source consensus")
					assert.Equal(t, vault, imported.ObservedPubKey)
				} else {
					allowSpentOutputRefsImports(c)
					c.catchupSpentOutputRefs()
				}
				assert.EqualValues(t, 1, marked.Load())
				assert.True(t, c.signerCacheManager.HasSigned(xmrMigrationObserveKey(vault, outbound.Tx.Tx.ToAddress.String(), outbound.Tx.Tx.Memo)))
			})
		}
	}
}

func TestQueryConsensusObservedXMRTxRequiresCanonicalCommittedMatch(t *testing.T) {
	t.Parallel()

	txID := common.TxID(testXMRHash("consensus-selector"))
	candidate := ttypes.QueryObservedTx{Tx: common.Tx{ID: txID, Chain: common.XMRChain}}
	nonXMRCandidate := candidate
	nonXMRCandidate.Tx.Chain = common.BTCChain
	tests := []struct {
		name      string
		voter     *ttypes.QueryObservedTxVoter
		wantRetry bool
		wantOK    bool
	}{
		{
			name: "non-consensus observation is not a canonical fallback",
			voter: &ttypes.QueryObservedTxVoter{
				TxID: txID, Height: 100, Txs: []ttypes.QueryObservedTx{candidate},
			},
		},
		{
			name: "canonical transaction before consensus is retryable",
			voter: &ttypes.QueryObservedTxVoter{
				TxID: txID, Tx: candidate,
			},
			wantRetry: true,
		},
		{
			name: "reverted voter is terminal",
			voter: &ttypes.QueryObservedTxVoter{
				TxID: txID, Tx: candidate, Height: 100, Reverted: true,
			},
		},
		{
			name: "mismatched voter transaction ID is terminal",
			voter: &ttypes.QueryObservedTxVoter{
				TxID:   common.TxID(testXMRHash("consensus-selector-other-voter")),
				Tx:     candidate,
				Height: 100,
			},
		},
		{
			name: "non-xmr canonical transaction is terminal",
			voter: &ttypes.QueryObservedTxVoter{
				TxID: txID, Tx: nonXMRCandidate, Height: 100,
			},
		},
		{
			name: "finalised canonical transaction is accepted",
			voter: &ttypes.QueryObservedTxVoter{
				TxID: txID, Tx: candidate, FinalisedHeight: 100,
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, retry, ok := queryConsensusObservedXMRTx(tt.voter, txID)
			assert.Equal(t, tt.wantRetry, retry)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestCommittedXMROutboundRejectsHintVaultMismatch(t *testing.T) {
	t.Parallel()

	vault, _, spendTxHash, voter, pointerRefs := testXMRCatchupMigration(t, "committed-hint-vault-mismatch")
	refs := make([]common.OutputRef, len(pointerRefs))
	for i := range pointerRefs {
		refs[i] = *pointerRefs[i]
	}
	c := &Client{
		logger: testLogger(),
		thorchainBridge: &mockMoneroBridge{
			observedByTx: map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
		},
	}
	wrongVault := testVaultPubKey(t, "committed-hint-wrong-vault")
	require.False(t, wrongVault.Equals(vault))

	_, retry, ok := c.committedXMROutboundForSpentRefs(context.Background(), common.TxID(spendTxHash), wrongVault, refs, true)
	assert.False(t, retry)
	assert.False(t, ok)
}

func TestApplyCommittedSpentOutputRefsUsesConsensusMetadata(t *testing.T) {
	t.Parallel()

	vault, _, spendTxHash, voter, refs := testXMRCatchupMigration(t, "committed-hint-metadata")
	var captureMu sync.Mutex
	var captured sidecar.MarkSpentReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/mark-spent" {
			http.NotFound(w, r)
			return
		}
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		captureMu.Lock()
		captured = req
		captureMu.Unlock()
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	signerCache := newTestSignerCacheManager(t)
	payloadStore := newTestSignedTxPayloadStore(t)
	c := &Client{
		chain:                common.XMRChain,
		sidecar:              signer,
		logger:               testLogger(),
		signerCacheManager:   signerCache,
		signedTxPayloadStore: payloadStore,
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
		},
	}

	allowSpentOutputRefsImports(c)
	imported, applied := c.ApplyCommittedSpentOutputRefs(context.Background(), vault, common.TxID(spendTxHash))
	require.True(t, applied)
	captureMu.Lock()
	capturedCopy := captured
	captureMu.Unlock()
	assert.Equal(t, voter.Tx.Tx.ToAddress.String(), capturedCopy.ToAddress)
	assert.Equal(t, voter.Tx.Tx.FromAddress.String(), capturedCopy.FromAddress)
	assert.Equal(t, voter.Tx.Tx.Memo, capturedCopy.Memo)
	assert.Equal(t, voter.Tx.XmrTxKey, capturedCopy.TxKey)
	require.Len(t, capturedCopy.SpentRefs, len(refs))
	assert.Equal(t, refs[0].TxHash, capturedCopy.SpentRefs[0].TxHash)
	assert.True(t, imported.Tx.ID.Equals(voter.Tx.Tx.ID))
	assert.Equal(t, voter.Tx.Tx.Memo, imported.Tx.Memo)
	consensusHash := xmrMigrationObserveKey(vault, voter.Tx.Tx.ToAddress.String(), voter.Tx.Tx.Memo)
	maliciousHash := xmrMigrationObserveKey(vault, "malicious-pending-destination", "MIGRATE:999")
	assert.True(t, signerCache.HasSigned(consensusHash))
	assert.False(t, signerCache.HasSigned(maliciousHash), "event metadata must not suppress an unrelated pending outbound")
}

func TestApplyCommittedSpentOutputRefsRetriesDeliverTxCommitRace(t *testing.T) {
	t.Parallel()

	vault, _, spendTxHash, voter, refs := testXMRCatchupMigration(t, "committed-hint-race")
	pending := *voter
	pending.Height = 0
	pending.FinalisedHeight = 0
	var voterCalls atomic.Int32
	var markSpentCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markSpentCalls.Add(1)
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:                  common.XMRChain,
		sidecar:                signer,
		logger:                 testLogger(),
		spentRefsRetryInterval: 5 * time.Millisecond,
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedFreshFunc: func(_ context.Context, txID string) (*ttypes.QueryObservedTxVoter, bool, error) {
				if voterCalls.Add(1) == 1 {
					return &pending, true, nil
				}
				return voter, true, nil
			},
			pubKeys: []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
		},
	}

	allowSpentOutputRefsImports(c)
	imported, applied := c.ApplyCommittedSpentOutputRefs(context.Background(), vault, common.TxID(spendTxHash))
	require.True(t, applied)
	assert.True(t, imported.Tx.ID.Equals(common.TxID(spendTxHash)))
	assert.Equal(t, int32(2), voterCalls.Load())
	assert.Equal(t, int32(1), markSpentCalls.Load())
}

func TestApplyCommittedSpentOutputRefsRetriesUntilRefsAreVisible(t *testing.T) {
	t.Parallel()

	vault, _, spendTxHash, voter, refs := testXMRCatchupMigration(t, "committed-hint-refs-race")
	var refsCalls atomic.Int32
	var voterCalls atomic.Int32
	var markSpentCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markSpentCalls.Add(1)
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:                  common.XMRChain,
		sidecar:                signer,
		logger:                 testLogger(),
		spentRefsRetryInterval: 5 * time.Millisecond,
		thorchainBridge: &mockMoneroBridge{
			spentRefsFunc: func(common.PubKey) ([]*common.OutputRef, error) {
				if refsCalls.Add(1) == 1 {
					return nil, nil
				}
				return refs, nil
			},
			observedFreshFunc: func(context.Context, string) (*ttypes.QueryObservedTxVoter, bool, error) {
				voterCalls.Add(1)
				return voter, true, nil
			},
			pubKeys: []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
		},
	}

	allowSpentOutputRefsImports(c)
	imported, applied := c.ApplyCommittedSpentOutputRefs(context.Background(), vault, common.TxID(spendTxHash))
	require.True(t, applied)
	assert.True(t, imported.Tx.ID.Equals(common.TxID(spendTxHash)))
	assert.Equal(t, int32(2), refsCalls.Load())
	assert.Equal(t, int32(1), voterCalls.Load())
	assert.Equal(t, int32(1), markSpentCalls.Load())
}
