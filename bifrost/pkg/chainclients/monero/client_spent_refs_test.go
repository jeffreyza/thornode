package monero

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func testLogger() zerolog.Logger {
	return zerolog.Nop()
}

// Test 2: buildOutboundObservation includes SpentOutputRefs in the TxInItem.
func TestBuildOutboundObservationIncludesSpentRefs(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-spent-refs-obs")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)

	c := &Client{
		chain: common.XMRChain,
	}
	c.vaultAddressMap = map[string]common.Address{
		vaultPubKey.String(): vaultAddr,
	}
	c.vaultAddrToKey = map[string]string{}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKeyFrost: vaultPubKey,
		Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Memo:             "OUT:inhash",
	}

	spentRefs := []sidecar.SpentRef{
		{TxHash: "aaa111", OutputIndex: 0, KeyImage: "ki_aaa"},
		{TxHash: "bbb222", OutputIndex: 1, KeyImage: "ki_bbb"},
		{TxHash: "ccc333", OutputIndex: 2, KeyImage: "ki_ccc"},
	}

	txKey := strings.Repeat("1", 64)
	obs := c.buildOutboundObservation(tx, "txhash123", txKey, 5000000, spentRefs)
	require.NotNil(t, obs, "observation should not be nil")
	require.Equal(t, 3, len(obs.SpentOutputRefs), "observation should carry all spent refs")
	require.Equal(t, txKey, obs.TxKey, "observation should carry tx key")

	assert.Equal(t, "aaa111", obs.SpentOutputRefs[0].TxHash)
	assert.Equal(t, uint32(0), obs.SpentOutputRefs[0].OutputIndex)
	assert.Equal(t, "ki_aaa", obs.SpentOutputRefs[0].KeyImage)
	assert.Equal(t, "txhash123", obs.SpentOutputRefs[0].SpendTxHash)
	assert.Equal(t, "bbb222", obs.SpentOutputRefs[1].TxHash)
	assert.Equal(t, uint32(1), obs.SpentOutputRefs[1].OutputIndex)
	assert.Equal(t, "ki_bbb", obs.SpentOutputRefs[1].KeyImage)
	assert.Equal(t, "ccc333", obs.SpentOutputRefs[2].TxHash)
	assert.Equal(t, uint32(2), obs.SpentOutputRefs[2].OutputIndex)
	assert.Equal(t, "ki_ccc", obs.SpentOutputRefs[2].KeyImage)

	assert.Equal(t, vaultPubKey, obs.ObservedVaultPubKey, "observation vault pubkey should be the frost key")
}

// Test 2b: buildOutboundObservation with empty spent refs produces no refs.
func TestBuildOutboundObservationNoSpentRefs(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-no-spent-refs")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)

	c := &Client{
		chain: common.XMRChain,
	}
	c.vaultAddressMap = map[string]common.Address{
		vaultPubKey.String(): vaultAddr,
	}
	c.vaultAddrToKey = map[string]string{}

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKeyFrost: vaultPubKey,
		Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(100))),
		Memo:             "OUT:inhash",
	}

	obs := c.buildOutboundObservation(tx, "txhash456", strings.Repeat("2", 64), 5000000, nil)
	require.NotNil(t, obs)
	assert.Empty(t, obs.SpentOutputRefs)
}

// TestApplySpentOutputRefsSidecarCall verifies peer-imported spent
// refs are forwarded to the sidecar with the full outbound metadata needed for
// independent validation.
func TestApplySpentOutputRefsSidecarCall(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs")

	var mu sync.Mutex
	var capturedVault string
	var capturedTxHash string
	var capturedTxKey string
	var capturedRefs []sidecar.SpentRef
	var capturedSkipVerification bool
	var capturedFromAddress string
	var capturedToAddress string
	var capturedAmount uint64
	var capturedFee uint64
	var capturedMemo string
	var capturedOrigin sidecar.OutgoingOrigin

	// Capture the mark-spent request so the test can assert that THORChain's
	// ObservedTx fields were converted to the sidecar contract correctly.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/outputs/mark-spent" {
			var req sidecar.MarkSpentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			capturedVault = req.VaultPubKey
			capturedTxHash = req.TxHash
			capturedTxKey = req.TxKey
			capturedRefs = req.SpentRefs
			capturedSkipVerification = req.SkipVerification
			capturedFromAddress = req.FromAddress
			capturedToAddress = req.ToAddress
			capturedAmount = req.Amount
			capturedFee = req.Fee
			capturedMemo = req.Memo
			capturedOrigin = req.Origin
			mu.Unlock()
			// MarkSpent is idempotent: zero newly marked with zero missing means
			// every requested ref was already verified.
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 0, NotFound: 0})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
	}

	refs := []common.OutputRef{
		*testXMROutputRef("spent-a", 0, spendTxHash),
		*testXMROutputRef("spent-b", 3, spendTxHash),
	}
	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(500_000))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(10_000))},
			Memo:        "MIGRATE:100",
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = refs
	obsTx.XmrTxKey = strings.Repeat("3", 64)

	allowSpentOutputRefsImports(c)
	applied, retry := c.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: obsTx, WireMemo: obsTx.Tx.Memo})
	assert.True(t, applied, "idempotent Marked=0 response must count as verified")
	assert.False(t, retry)
	allowSpentOutputRefsImports(c)
	applied, retry = c.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: obsTx, WireMemo: obsTx.Tx.Memo, ReplaySource: true})
	assert.False(t, applied, "certificate import cannot succeed without a scanner to own replay")
	assert.True(t, retry)

	mu.Lock()
	defer mu.Unlock()

	// The sidecar receives atomic-unit outbound metadata. This is separate from
	// the consensus ObservedTx shape and is what lets the scanner later
	// attribute the verified XMR spend back to the THORChain outbound.
	expectedAmount, err := thorchainAmountToAtomic(obsTx.Tx.Coins.GetCoin(common.XMRAsset).Amount)
	require.NoError(t, err)
	expectedFee, err := thorchainAmountToAtomic(obsTx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset).Amount)
	require.NoError(t, err)

	assert.Equal(t, vaultPubKey.String(), capturedVault, "sidecar should receive the vault pubkey from ObservedPubKey")
	assert.Equal(t, spendTxHash, capturedTxHash)
	assert.Equal(t, obsTx.XmrTxKey, capturedTxKey)
	// Peer-imported refs must still be verified by the sidecar. This explicitly
	// guards against accidentally switching the import path to trusted refs.
	assert.False(t, capturedSkipVerification)
	assert.Equal(t, vaultAddr.String(), capturedFromAddress)
	assert.Equal(t, obsTx.Tx.ToAddress.String(), capturedToAddress)
	assert.Equal(t, expectedAmount, capturedAmount)
	assert.Equal(t, expectedFee, capturedFee)
	assert.Equal(t, "MIGRATE:100", capturedMemo)
	// applySpentOutputRefsOnce is the peer pre-quorum import path. 4cac9e1187
	// deliberately marks it VaultCertified and attaches the source-vault
	// certificate hash, rather than ChainCommitted, because the refs are not yet
	// committed by Thornode. ChainCommitted is reserved for the catchup path
	// that reads them back from a committed voter.
	// The origin constants are untyped strings, so compare as string to avoid a
	// spurious string vs sidecar.OutgoingOrigin type mismatch.
	assert.Equal(t, sidecar.OutgoingOriginVaultCertified, string(capturedOrigin))
	require.Equal(t, 2, len(capturedRefs))
	assert.Equal(t, refs[0].TxHash, capturedRefs[0].TxHash)
	assert.Equal(t, uint32(0), capturedRefs[0].OutputIndex)
	assert.Equal(t, refs[0].KeyImage, capturedRefs[0].KeyImage)
	assert.Equal(t, spendTxHash, capturedRefs[0].SpendTxHash)
	assert.Equal(t, refs[1].TxHash, capturedRefs[1].TxHash)
	assert.Equal(t, uint32(3), capturedRefs[1].OutputIndex)
	assert.Equal(t, refs[1].KeyImage, capturedRefs[1].KeyImage)
	assert.Equal(t, spendTxHash, capturedRefs[1].SpendTxHash)
}

// TestValidateXMRSpentRefsForSidecarRejectsDuplicateKeyImage protects the
// sidecar binding invariant that one Monero key image can only spend one output.
func TestValidateXMRSpentRefsForSidecarRejectsDuplicateKeyImage(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-duplicate-key-image")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("duplicate-key-image")

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	first := *testXMROutputRef("duplicate-key-image-a", 0, spendTxHash)
	second := *testXMROutputRef("duplicate-key-image-b", 1, spendTxHash)
	second.KeyImage = strings.ToUpper(first.KeyImage)
	obsTx.SpentOutputRefs = []common.OutputRef{first, second}
	obsTx.XmrTxKey = strings.Repeat("7", 64)

	err = validateXMRSpentRefsForSidecar(obsTx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate xmr spent ref key image")
}

func TestApplySpentOutputRefsAuthenticatesExactOriginBeforeMutation(t *testing.T) {
	vaultPubKey := testVaultPubKey(t, "test-vault-exact-origin-before-mark-spent")
	vaultAddress, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	toAddress := testOutboundXMRDestination()
	coins := common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50)))
	memoText := "OUT:" + testXMRHash("exact-origin-inbound")

	cases := []struct {
		name string
		from func(string) common.Address
	}{
		{
			name: "case variant",
			from: func(string) common.Address {
				return common.Address(strings.ToUpper(vaultAddress.String()))
			},
		},
		{
			name: "unknown sender marker",
			from: func(txHash string) common.Address {
				return common.Address(common.XMRUnknownSenderAddress(txHash, toAddress.String()))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var markMu sync.Mutex
			markCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/monero/outputs/mark-spent" {
					markMu.Lock()
					markCalls++
					markMu.Unlock()
					_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 1})
					return
				}
				http.Error(w, "not found", http.StatusNotFound)
			}))
			defer server.Close()

			signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
			require.NoError(t, err)
			store := newTestSignedTxPayloadStore(t)
			client := &Client{
				chain: common.XMRChain, sidecar: signer, signedTxPayloadStore: store, logger: testLogger(),
			}
			txHash := testXMRHash("exact-origin-before-mark-spent-" + tc.name)
			obsTx := common.NewObservedTx(common.Tx{
				ID:          common.TxID(txHash),
				Chain:       common.XMRChain,
				FromAddress: tc.from(txHash),
				ToAddress:   toAddress,
				Coins:       coins,
				Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
				Memo:        memoText,
			}, 100, vaultPubKey, 100)
			obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("exact-origin-ref-"+tc.name, 0, txHash)}
			obsTx.XmrTxKey = strings.Repeat("d", 64)

			logicalHash := xmrScheduledLogicalHash(common.XMRChain, toAddress.String(), coins, memoText)
			plan := testSignedTxClaimPlan(
				"cache-vault", "cache-"+tc.name, logicalHash, vaultPubKey.String(), "msg-"+tc.name, "plan-"+tc.name,
				xmrScheduledObserveKey(vaultPubKey, toAddress.String(), coins, memoText),
			)
			require.NoError(t, store.Reserve(plan))
			payload := testStoredSignedTxPayload(t, txHash, "deadbeef")
			require.NoError(t, store.Set(plan, txHash, payload))

			allowSpentOutputRefsImports(client)
			applied, retry := client.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: obsTx, WireMemo: obsTx.Tx.Memo})
			assert.False(t, applied)
			assert.False(t, retry)
			markMu.Lock()
			gotMarkCalls := markCalls
			markMu.Unlock()
			assert.Zero(t, gotMarkCalls, "unauthenticated origin reached sidecar MarkSpent")
			claim, found, claimErr := store.Claim(logicalHash)
			require.NoError(t, claimErr)
			require.True(t, found)
			assert.Equal(t, signedTxClaimSigned, claim.State)
			assert.Empty(t, claim.ObservedTxHash, "unauthenticated origin transitioned durable claim")
			gotPayload, ok, getErr := store.GetOwned(plan.CacheVault, plan.CacheHash, logicalHash)
			require.NoError(t, getErr)
			require.True(t, ok)
			assert.Equal(t, payload, gotPayload)
		})
	}
}

// TestApplySpentOutputRefsMarksImportedOutboundSignedOnlyAfterFullImport checks
// the handoff from verified spent-ref import to signer-cache suppression. Even
// a full import retains the cross-vault proof bundle until THORChain commits the
// outbound observation.
func TestApplySpentOutputRefsMarksImportedOutboundSignedOnlyAfterFullImport(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-signer-cache")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-signer-cache")
	inboundHash := testXMRHash("apply-spent-output-refs-signer-cache-inbound")

	var markMu sync.Mutex
	markCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/outputs/mark-spent" {
			var req sidecar.MarkSpentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			markMu.Lock()
			markCalls++
			call := markCalls
			markMu.Unlock()
			if call == 1 {
				_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs) - 1, NotFound: 1})
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
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
	}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			Memo:        "OUT:" + inboundHash,
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("spent-signer-cache-a", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("8", 64)

	cacheItem := &stypes.TxInItem{
		To:                  obsTx.Tx.ToAddress.String(),
		Coins:               obsTx.Tx.Coins,
		Memo:                obsTx.Tx.Memo,
		ObservedVaultPubKey: vaultPubKey,
	}
	expectedInboundHash, err := common.NewTxID(inboundHash)
	require.NoError(t, err)
	cacheHash := cacheItem.CacheHash(common.XMRChain, expectedInboundHash.String())
	signerCacheVault := cacheItem.CacheVault(common.XMRChain)
	payloadCacheVault := stypes.BroadcastCacheKey(vaultPubKey.String(), common.XMRChain.String())
	require.False(t, signerCache.HasSigned(cacheHash))
	// Seed the production broadcast namespace, not TxInItem's distinct inbound
	// signer-cache namespace. The logical claim uses the semantic scheduled
	// identity, which intentionally differs from the legacy cache hash.
	payloadBytes, err := json.Marshal(signedTxPayload{TxHash: spendTxHash, TxMetadata: "deadbeef"})
	require.NoError(t, err)
	logicalHash := xmrScheduledLogicalHash(common.XMRChain, obsTx.Tx.ToAddress.String(), obsTx.Tx.Coins, obsTx.Tx.Memo)
	claimPlan := testSignedTxClaimPlan(
		payloadCacheVault,
		cacheHash,
		logicalHash,
		vaultPubKey.String(),
		"TEST-MSG",
		"TEST-PLAN",
		xmrScheduledObserveKey(vaultPubKey, obsTx.Tx.ToAddress.String(), obsTx.Tx.Coins, obsTx.Tx.Memo),
	)
	require.NoError(t, payloadStore.Reserve(claimPlan))
	require.NoError(t, payloadStore.Set(claimPlan, spendTxHash, payloadBytes))

	allowSpentOutputRefsImports(c)
	applied, retry := c.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: obsTx, WireMemo: obsTx.Tx.Memo})
	require.False(t, applied)
	require.True(t, retry)
	assert.False(t, signerCache.HasSigned(cacheHash), "partial import must not suppress a future exact retry")
	_, ok, err := payloadStore.Get(payloadCacheVault, cacheHash)
	require.NoError(t, err)
	assert.True(t, ok, "partial import must retain the recoverable payload")

	allowSpentOutputRefsImports(c)
	applied, retry = c.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: obsTx, WireMemo: obsTx.Tx.Memo})
	require.True(t, applied)
	require.False(t, retry)
	assert.True(t, signerCache.HasSigned(cacheHash))
	cachedTxHash, err := signerCache.GetTransactionHash(cacheHash)
	require.NoError(t, err)
	assert.Equal(t, spendTxHash, cachedTxHash)
	_, ok, err = payloadStore.Get(payloadCacheVault, cacheHash)
	require.NoError(t, err)
	assert.True(t, ok, "verified full spent-ref import must retain exact bytes for rebroadcast")
	recovered, ok, err := payloadStore.GetOwned(payloadCacheVault, cacheHash, logicalHash)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, payloadBytes, recovered, "visibility-only import must not terminalize exact recovery")

	c.OnObservedTxIn(stypes.TxInItem{
		Tx:                  spendTxHash,
		Sender:              vaultAddr.String(),
		To:                  obsTx.Tx.ToAddress.String(),
		Coins:               obsTx.Tx.Coins,
		Memo:                obsTx.Tx.Memo,
		ObservedVaultPubKey: vaultPubKey,
	}, 100)
	_, ok, err = payloadStore.Get(payloadCacheVault, cacheHash)
	require.NoError(t, err)
	assert.True(t, ok, "final observation should retain exact bytes behind the tombstone for deep reorgs")
	latestTx, err := signerCache.GetLatestRecordedTx(signerCacheVault)
	require.NoError(t, err)
	assert.Equal(t, spendTxHash, latestTx)
}

func TestValidateXMRSpentRefsForSidecarRequiresTxKey(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-missing-tx-key")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("missing-tx-key")

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{
		*testXMROutputRef("missing-key-spent", 0, spendTxHash),
	}

	err = validateXMRSpentRefsForSidecar(obsTx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbound tx key")
}

// TestApplySpentOutputRefsRetriesTransientNotFound covers the race where peer
// refs arrive before the Monero daemon/sidecar can query the spend tx. The
// import should retry boundedly instead of relying only on committed catchup.
func TestApplySpentOutputRefsRetriesTransientNotFound(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-retry")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-retry")

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	var closeDone sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/mark-spent" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		mu.Lock()
		calls++
		call := calls
		mu.Unlock()

		if call == 1 {
			// First attempt simulates the sidecar get_transactions race window.
			// The second attempt proves ApplySpentOutputRefs queues a retry.
			http.Error(w, "tx not mined yet", http.StatusNotFound)
			return
		}

		var req sidecar.MarkSpentReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
		closeDone.Do(func() { close(done) })
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:                     common.XMRChain,
		sidecar:                   signer,
		logger:                    testLogger(),
		wg:                        &sync.WaitGroup{},
		stopchan:                  make(chan struct{}),
		spentRefsRetryInterval:    10 * time.Millisecond,
		spentRefsRetryMaxAttempts: 3,
	}
	defer func() {
		close(c.stopchan)
		c.wg.Wait()
	}()

	refs := []common.OutputRef{
		*testXMROutputRef("spent-retry-a", 0, spendTxHash),
	}
	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = refs
	obsTx.XmrTxKey = strings.Repeat("5", 64)

	c.ApplySpentOutputRefs(obsTx)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mark-spent retry")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, calls, 2)
}

// TestApplySpentOutputRefsRetriesTransientSidecarUnavailable covers the peer
// import case where the local sidecar is reachable but not ready yet. This must
// not permanently consume the observer bucket's spent-ref import opportunity.
func TestApplySpentOutputRefsRetriesTransientSidecarUnavailable(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-retry-503")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-retry-503")

	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	var closeDone sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/mark-spent" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		mu.Lock()
		calls++
		call := calls
		mu.Unlock()

		if call == 1 {
			// A 503 is a sidecar readiness/health failure, not a ref validation
			// failure. The import should retry just like the get_transactions
			// 404 race.
			http.Error(w, "sidecar not ready", http.StatusServiceUnavailable)
			return
		}

		var req sidecar.MarkSpentReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
		closeDone.Do(func() { close(done) })
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:                     common.XMRChain,
		sidecar:                   signer,
		logger:                    testLogger(),
		wg:                        &sync.WaitGroup{},
		stopchan:                  make(chan struct{}),
		spentRefsRetryInterval:    10 * time.Millisecond,
		spentRefsRetryMaxAttempts: 3,
	}
	defer func() {
		close(c.stopchan)
		c.wg.Wait()
	}()

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("spent-retry-503-a", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("6", 64)

	c.ApplySpentOutputRefs(obsTx)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mark-spent retry after 503")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, calls, 2)
}

// TestApplySpentOutputRefsRetryExhaustionIsBounded verifies that
// transient sidecar/daemon failures retry only up to the configured limit.
func TestApplySpentOutputRefsRetryExhaustionIsBounded(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-retry-exhausted")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-retry-exhausted")

	var mu sync.Mutex
	calls := 0
	// Always return a transient 404 so the retry loop has to stop by its
	// configured attempt bound instead of by success.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/mark-spent" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "tx still unavailable", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:                     common.XMRChain,
		sidecar:                   signer,
		logger:                    testLogger(),
		wg:                        &sync.WaitGroup{},
		stopchan:                  make(chan struct{}),
		spentRefsRetryInterval:    5 * time.Millisecond,
		spentRefsRetryMaxAttempts: 2,
	}
	defer close(c.stopchan)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("spent-retry-exhausted-a", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("a", 64)

	c.ApplySpentOutputRefs(obsTx)
	c.wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, calls, "initial attempt plus two configured retries")
}

// TestApplySpentOutputRefsDoesNotRetryTerminalSidecarError verifies that the
// broader transient retry policy does not retry sidecar validation failures.
// Retrying 400-class ref errors would only add load and hide bad inputs.
func TestApplySpentOutputRefsDoesNotRetryTerminalSidecarError(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-no-retry-400")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-no-retry-400")

	var mu sync.Mutex
	calls := 0

	// Bad-request responses represent validation failures, so retrying them
	// would only repeat the same rejected payload.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/mark-spent" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "invalid spent refs", http.StatusBadRequest)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:    common.XMRChain,
		sidecar:  signer,
		logger:   testLogger(),
		wg:       &sync.WaitGroup{},
		stopchan: make(chan struct{}),
	}
	defer close(c.stopchan)

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("spent-no-retry-400-a", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("7", 64)

	c.ApplySpentOutputRefs(obsTx)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls)
}

// TestApplySpentOutputRefsSkipsWhenSidecarMissing ensures the import
// hook is safe to call before the XMR sidecar client has been wired.
func TestApplySpentOutputRefsSkipsWhenSidecarMissing(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-no-sidecar")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-no-sidecar")

	c := &Client{
		chain:  common.XMRChain,
		logger: testLogger(),
	}

	// Leave c.sidecar nil to cover startup/shutdown edge cases where the hook
	// can receive an observation but has nowhere to import it yet.
	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("spent-no-sidecar-a", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("b", 64)

	require.NotPanics(t, func() {
		c.ApplySpentOutputRefs(obsTx)
	})
}

// TestApplySpentOutputRefsDoesNotQueueRetryAfterStop protects
// shutdown behavior: a stopped client should not create async retry work.
func TestApplySpentOutputRefsDoesNotQueueRetryAfterStop(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-stopped")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-stopped")

	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/mark-spent" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "tx not mined yet", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	stopchan := make(chan struct{})
	close(stopchan)
	// The closed stop channel should allow the synchronous attempt but prevent
	// queueing any follow-up retry goroutine.
	c := &Client{
		chain:                     common.XMRChain,
		sidecar:                   signer,
		logger:                    testLogger(),
		wg:                        &sync.WaitGroup{},
		stopchan:                  stopchan,
		spentRefsRetryInterval:    10 * time.Millisecond,
		spentRefsRetryMaxAttempts: 3,
	}

	obsTx := common.NewObservedTx(
		common.Tx{
			ID:          common.TxID(spendTxHash),
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   testOutboundXMRDestination(),
			Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
			Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		},
		100,
		vaultPubKey,
		100,
	)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef("spent-stopped-a", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("9", 64)

	c.ApplySpentOutputRefs(obsTx)
	c.wg.Wait()
	time.Sleep(25 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, calls, "closed stop channel must prevent every sidecar attempt")
}

// TestApplySpentOutputRefsFromTxInItemUsesObservationHeight verifies the
// observer-to-sidecar conversion preserves the observed tx hash/ref set when a
// TxInItem is reconstructed into an ObservedTx for spent-ref import.
func TestApplySpentOutputRefsFromTxInItemUsesObservationHeight(t *testing.T) {
	t.Parallel()

	vaultPubKey := testVaultPubKey(t, "test-vault-apply-spent-txin")
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("apply-spent-output-refs-txin")

	var mu sync.Mutex
	var capturedTxHash string
	var capturedTxKey string
	var capturedRefs []sidecar.SpentRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/outputs/mark-spent" {
			var req sidecar.MarkSpentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			capturedTxHash = req.TxHash
			capturedTxKey = req.TxKey
			capturedRefs = req.SpentRefs
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
	}
	refs := []*common.OutputRef{
		testXMROutputRef("spent-txin-a", 0, spendTxHash),
	}

	// Feed the signer-produced TxInItem shape into the helper and verify it is
	// reconstructed into the sidecar mark-spent request without losing refs.
	c.applySpentOutputRefsFromTxInItem(&stypes.TxInItem{
		BlockHeight:         123,
		Tx:                  spendTxHash,
		Sender:              vaultAddr.String(),
		To:                  testOutboundXMRDestination().String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		SpentOutputRefs:     refs,
		TxKey:               strings.Repeat("4", 64),
	})

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, spendTxHash, capturedTxHash)
	assert.Equal(t, strings.Repeat("4", 64), capturedTxKey)
	require.Equal(t, 1, len(capturedRefs))
	assert.Equal(t, refs[0].TxHash, capturedRefs[0].TxHash)
	assert.Equal(t, spendTxHash, capturedRefs[0].SpendTxHash)
}

// Test 2c: txVaultPubKey only accepts the frost vault key for XMR routing.
func TestTxVaultPubKeyPreference(t *testing.T) {
	t.Parallel()

	frost := testVaultPubKey(t, "frost-key")
	eddsa := testVaultPubKey(t, "eddsa-key")
	ecdsa := testVaultPubKey(t, "ecdsa-key")

	tx := stypes.TxOutItem{
		VaultPubKey:      ecdsa,
		VaultPubKeyEddsa: eddsa,
		VaultPubKeyFrost: frost,
	}
	assert.Equal(t, frost, txVaultPubKey(tx), "should prefer frost key")

	txNoFrost := stypes.TxOutItem{
		VaultPubKey:      ecdsa,
		VaultPubKeyEddsa: eddsa,
	}
	assert.True(t, txVaultPubKey(txNoFrost).IsEmpty(), "should reject eddsa-only xmr vault routing")

	txOnlyEcdsa := stypes.TxOutItem{
		VaultPubKey: ecdsa,
	}
	assert.True(t, txVaultPubKey(txOnlyEcdsa).IsEmpty(), "should be empty when only ecdsa set")
}

func testXMRSpentRefsObservedTx(t *testing.T, vaultLabel, txLabel string) common.ObservedTx {
	t.Helper()
	vaultPubKey := testVaultPubKey(t, vaultLabel)
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash(txLabel)
	obsTx := common.NewObservedTx(common.Tx{
		ID: common.TxID(spendTxHash), Chain: common.XMRChain, FromAddress: vaultAddr,
		ToAddress: testOutboundXMRDestination(),
		Coins:     common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
		Gas:       common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
	}, 100, vaultPubKey, 100)
	obsTx.SpentOutputRefs = []common.OutputRef{*testXMROutputRef(txLabel+"-ref", 0, spendTxHash)}
	obsTx.XmrTxKey = strings.Repeat("c", 64)
	return obsTx
}

func TestApplySpentOutputRefsWithWireMemoUsesWireMemoAcrossRetry(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var requests []sidecar.MarkSpentReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode mark-spent request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, req)
		call := len(requests)
		mu.Unlock()
		if call == 1 {
			http.Error(w, "tx not mined yet", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{
		chain:                     common.XMRChain,
		sidecar:                   signer,
		logger:                    testLogger(),
		wg:                        &sync.WaitGroup{},
		stopchan:                  make(chan struct{}),
		spentRefsRetryInterval:    10 * time.Millisecond,
		spentRefsRetryMaxAttempts: 2,
	}
	defer func() {
		close(c.stopchan)
		c.wg.Wait()
	}()

	obsTx := testXMRSpentRefsObservedTx(t, "test-vault-signed-wire-memo", "signed-wire-memo")
	obsTx.Tx.Memo = "OUT:" + strings.Repeat("A", 64)
	expectedCertificateHash, err := xmrSpentRefsCertificateHash(obsTx)
	require.NoError(t, err)
	outcome := make(chan bool, 1)
	c.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx:       obsTx,
		WireMemo: "",
	}, func(applied bool) { outcome <- applied })

	select {
	case applied := <-outcome:
		require.True(t, applied)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signed spent-ref import retry")
	}

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	for _, req := range requests {
		assert.Empty(t, req.Memo, "sidecar must authenticate the empty wire memo")
		assert.Equal(t, expectedCertificateHash, req.CertificateHash,
			"certificate must remain bound to the semantic observation")
	}
	assert.Equal(t, "OUT:"+strings.Repeat("A", 64), obsTx.Tx.Memo)
}

func TestApplySpentOutputRefsChainCommittedCarriesMetadataWithoutCertificate(t *testing.T) {
	t.Parallel()

	requests := make(chan sidecar.MarkSpentReq, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode mark-spent request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- req
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger()}
	obsTx := testXMRSpentRefsObservedTx(t, "test-vault-chain-committed", "chain-committed")
	obsTx.Tx.Memo = "OUT:" + strings.Repeat("B", 64)
	obsTx.XmrTxKey = strings.Repeat("d", 64)

	outcome := make(chan bool, 1)
	allowSpentOutputRefsImports(c)
	c.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx:             obsTx,
		ChainCommitted: true,
	}, func(applied bool) { outcome <- applied })
	require.True(t, <-outcome)

	req := <-requests
	assert.Equal(t, sidecar.OutgoingOrigin(sidecar.OutgoingOriginChainCommitted), req.Origin)
	assert.Equal(t, obsTx.Tx.Memo, req.Memo)
	assert.Equal(t, obsTx.XmrTxKey, req.TxKey)
	assert.False(t, req.SkipVerification)
	assert.Empty(t, req.CertificateHash)
	assert.Equal(t, "OUT:"+strings.Repeat("B", 64), obsTx.Tx.Memo)
}

func TestApplySpentOutputRefsChainCommittedInternalCarriesPairingProof(t *testing.T) {
	t.Parallel()

	requests := make(chan sidecar.MarkSpentReq, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode mark-spent request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests <- req
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger()}

	for _, tc := range []struct {
		name string
		memo string
	}{
		{name: "migrate", memo: "MIGRATE:100"},
		{name: "consolidate", memo: "consolidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obsTx := testXMRSpentRefsObservedTx(t, "test-vault-chain-committed-"+tc.name, "chain-committed-"+tc.name)
			obsTx.Tx.Memo = tc.memo

			allowSpentOutputRefsImports(c)
			applied, retry := c.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{
				Tx:             obsTx,
				ChainCommitted: true,
			})
			require.True(t, applied)
			require.False(t, retry)

			req := <-requests
			expectedAmount, err := thorchainAmountToAtomic(obsTx.Tx.Coins.GetCoin(common.XMRAsset).Amount)
			require.NoError(t, err)
			expectedFee, err := thorchainAmountToAtomic(obsTx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset).Amount)
			require.NoError(t, err)
			assert.Equal(t, obsTx.Tx.FromAddress.String(), req.FromAddress)
			assert.Equal(t, obsTx.Tx.ToAddress.String(), req.ToAddress)
			assert.Equal(t, obsTx.XmrTxKey, req.TxKey)
			assert.Equal(t, expectedAmount, req.Amount)
			assert.Equal(t, expectedFee, req.Fee)
			assert.Equal(t, obsTx.Tx.Memo, req.Memo)
			assert.Equal(t, sidecar.OutgoingOrigin(sidecar.OutgoingOriginChainCommitted), req.Origin)
			assert.Empty(t, req.CertificateHash)

			// Mirror the sidecar record produced from this request and pin the
			// complete proof contract for every internal memo, not just MIGRATE.
			proof := sidecar.Transfer{
				TxID:                req.TxHash,
				TxKey:               req.TxKey,
				Address:             req.ToAddress,
				FromAddress:         req.FromAddress,
				VaultPubKey:         req.VaultPubKey,
				Amount:              req.Amount,
				Fee:                 req.Fee,
				Height:              uint64(obsTx.BlockHeight),
				Note:                req.Memo,
				Origin:              string(req.Origin),
				ObservationEligible: false,
				SpentOutputRefs:     req.SpentRefs,
			}
			assert.True(t, hasCompleteChainCommittedPairingProof(proof))
		})
	}
}

func TestApplySpentOutputRefsOnceRetriesPartialNotFound(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 1, NotFound: 1})
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger()}
	obsTx := testXMRSpentRefsObservedTx(t, "test-vault-partial-not-found", "partial-not-found")
	obsTx.SpentOutputRefs = append(obsTx.SpentOutputRefs,
		*testXMROutputRef("partial-not-found-second", 1, obsTx.Tx.ID.String()))
	allowSpentOutputRefsImports(c)
	applied, retry := c.applySpentOutputRefsOnce(chainclienttypes.SpentOutputRefsImport{Tx: obsTx, WireMemo: obsTx.Tx.Memo})
	assert.False(t, applied)
	assert.True(t, retry)
}

func TestApplySpentOutputRefsWithOutcomeReportsRetrySuccess(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			http.Error(w, "tx not mined yet", http.StatusNotFound)
			return
		}
		var req sidecar.MarkSpentReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger(), wg: &sync.WaitGroup{}, stopchan: make(chan struct{}), spentRefsRetryInterval: 10 * time.Millisecond, spentRefsRetryMaxAttempts: 3}
	defer func() { close(c.stopchan); c.wg.Wait() }()
	outcome := make(chan bool, 1)
	c.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx: testXMRSpentRefsObservedTx(t, "test-vault-outcome-retry", "outcome-retry-success"),
	}, func(applied bool) { outcome <- applied })
	select {
	case applied := <-outcome:
		assert.True(t, applied, "outcome should report the retry success")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for import outcome")
	}
}

func TestApplySpentOutputRefsWithOutcomeReportsTerminalFailure(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "spent ref key image conflicts with tracked output", http.StatusBadRequest)
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger(), wg: &sync.WaitGroup{}, stopchan: make(chan struct{}), spentRefsRetryInterval: 10 * time.Millisecond, spentRefsRetryMaxAttempts: 3}
	defer func() { close(c.stopchan); c.wg.Wait() }()
	outcome := make(chan bool, 1)
	c.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx: testXMRSpentRefsObservedTx(t, "test-vault-outcome-terminal", "outcome-terminal"),
	}, func(applied bool) { outcome <- applied })
	select {
	case applied := <-outcome:
		assert.False(t, applied, "terminal sidecar failure must report a failed outcome")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for import outcome")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls, "terminal failure must not be retried")
}

func TestApplySpentOutputRefsWithOutcomeReportsExhaustion(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "tx not mined yet", http.StatusNotFound)
	}))
	defer server.Close()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger(), wg: &sync.WaitGroup{}, stopchan: make(chan struct{}), spentRefsRetryInterval: 5 * time.Millisecond, spentRefsRetryMaxAttempts: 2}
	defer func() { close(c.stopchan); c.wg.Wait() }()
	outcome := make(chan bool, 1)
	c.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx: testXMRSpentRefsObservedTx(t, "test-vault-outcome-exhausted", "outcome-exhausted"),
	}, func(applied bool) { outcome <- applied })
	select {
	case applied := <-outcome:
		assert.False(t, applied, "exhausted retries must report a failed outcome")
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for import outcome")
	}
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 3, calls, "expected the first attempt plus both retries")
}

func TestGuardedSpentOutputRefsRetryStopsBeforeSidecarMutation(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	sidecarCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		sidecarCalls++
		mu.Unlock()
		http.Error(w, "tx not mined yet", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	c := &Client{
		chain:                     common.XMRChain,
		sidecar:                   signer,
		logger:                    testLogger(),
		wg:                        &sync.WaitGroup{},
		stopchan:                  make(chan struct{}),
		spentRefsRetryInterval:    10 * time.Millisecond,
		spentRefsRetryMaxAttempts: 3,
	}
	defer func() {
		close(c.stopchan)
		c.wg.Wait()
	}()

	guardCalls := 0
	guard := func(common.ObservedTx) (func(), bool) {
		guardCalls++
		if guardCalls > 1 {
			return nil, false
		}
		return func() {}, true
	}
	outcome := make(chan bool, 1)
	c.SetSpentOutputRefsGuard(guard)
	tx := testXMRSpentRefsObservedTx(t, "test-vault-guarded-retry", "guarded-retry")
	c.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx: tx, WireMemo: tx.Tx.Memo, ReplaySource: true,
	}, func(applied bool) { outcome <- applied })

	select {
	case applied := <-outcome:
		require.False(t, applied)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for guarded retry outcome")
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, guardCalls)
	require.Equal(t, 1, sidecarCalls, "rejected retry reached the sidecar")
}

func allowSpentOutputRefsImports(c *Client) {
	c.SetSpentOutputRefsGuard(func(common.ObservedTx) (func(), bool) {
		return func() {}, true
	})
}

func TestSpentRefsImportRequiresAdmissionAndRechecksCancellation(t *testing.T) {
	for _, scenario := range []string{"committed without guard", "peer without guard", "canceled during admission"} {
		t.Run(scenario, func(t *testing.T) {
			vault := testVaultPubKey(t, "admission-vault")
			txHash := testXMRHash("admission-spend")
			refs := []*common.OutputRef{testXMROutputRef("admission-ref", 0, txHash)}
			voter := spentOutputRefsCatchupVoter(t, vault, txHash, 100)
			obs, _, found := queryConsensusObservedXMRTx(voter, common.TxID(txHash))
			require.True(t, found)
			obs.SpentOutputRefs = xmrOutputRefsFromPointers(refs)
			var sidecarCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				sidecarCalls.Add(1)
				_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 1})
			}))
			defer server.Close()
			client := spentOutputRefsCatchupClient(t, server.URL, vault, refs, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var releases atomic.Int32
			if scenario == "canceled during admission" {
				client.SetSpentOutputRefsGuard(func(common.ObservedTx) (func(), bool) {
					cancel()
					return func() { releases.Add(1) }, true
				})
			}
			peer := scenario == "peer without guard"
			applied, retry := client.applySpentOutputRefsOnceContext(ctx, chainclienttypes.SpentOutputRefsImport{
				Tx: obs, ChainCommitted: !peer, ReplaySource: peer,
			})
			require.False(t, applied)
			require.False(t, retry)
			require.Zero(t, sidecarCalls.Load(), "rejected work must not reach the sidecar")
			if scenario == "canceled during admission" {
				require.EqualValues(t, 1, releases.Load(), "cancellation must release the acquired reader")
			}
		})
	}
}

type spentRefsReplayFixture struct {
	client                  *Client
	tx                      common.ObservedTx
	db                      *leveldb.DB
	dbPath                  string
	source                  common.PubKey
	destination             common.PubKey
	markSpentCalls          atomic.Int32
	markObservedCalls       atomic.Int32
	beforeMarkSpent         func()
	beforeMarkObserved      func()
	beforeVaultRegistration func()
}

// The HTTP stub models authenticated sidecar responses, not FROST verification.
// Import, signed-claim storage, replay enrollment and acknowledgement use the
// production Go paths and a database that can actually be closed and reopened.
func newSpentRefsReplayFixture(t *testing.T, payout bool) *spentRefsReplayFixture {
	t.Helper()
	source, destination, txID, voter, refs := testXMRCatchupMigration(t, t.Name())
	if payout {
		voter.Tx.Tx.Memo = "OUT:" + testXMRHash(t.Name()+"-inbound")
	}
	tx := common.NewObservedTx(voter.Tx.Tx, 110, source, 110)
	tx.XmrMinedHeight, tx.XmrTxKey, tx.SpentOutputRefs = 100, voter.Tx.XmrTxKey, []common.OutputRef{*refs[0]}
	f := &spentRefsReplayFixture{
		tx: tx, source: source, destination: destination,
		dbPath: filepath.Join(t.TempDir(), "recovery"),
	}
	blockHash, parentHash := testXMRHash(t.Name()+"-block"), testXMRHash(t.Name()+"-parent")
	var imported atomic.Pointer[sidecar.MarkSpentReq]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/mark-spent":
			var request sidecar.MarkSpentReq
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if request.TxHash != txID || request.Memo != tx.Tx.Memo || len(request.SpentRefs) != 1 {
				http.Error(w, "import changed the exact transaction", http.StatusBadRequest)
				return
			}
			f.markSpentCalls.Add(1)
			imported.Store(&request)
			if f.beforeMarkSpent != nil {
				f.beforeMarkSpent()
			}
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(request.SpentRefs)})
		case "/v1/monero/chain/tx/" + txID + "/location":
			_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
				TxHash: txID, Height: 100, BlockHash: blockHash, PreviousHash: parentHash,
			})
		case "/v1/monero/vaults/register":
			if f.beforeVaultRegistration != nil {
				f.beforeVaultRegistration()
			}
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(201))
		case "/v1/monero/outputs/mark-observed":
			request := imported.Load()
			if request == nil {
				http.Error(w, "no imported spend", http.StatusNotFound)
				return
			}
			f.markObservedCalls.Add(1)
			if f.beforeMarkObserved != nil {
				f.beforeMarkObserved()
			}
			_ = json.NewEncoder(w).Encode(sidecar.Transfer{
				TxID: request.TxHash, TxKey: request.TxKey, VaultPubKey: request.VaultPubKey,
				FromAddress: request.FromAddress, Address: request.ToAddress,
				Amount: request.Amount, Fee: request.Fee, Height: 100, Note: request.Memo,
				Origin: string(request.Origin), ObservationEligible: request.Origin == sidecar.OutgoingOriginVaultCertified,
				SpentOutputRefs: request.SpentRefs,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	f.client = &Client{chain: common.XMRChain, sidecar: signer, logger: testLogger()}
	f.open(t)
	t.Cleanup(func() { require.NoError(t, f.db.Close()) })
	return f
}

func (f *spentRefsReplayFixture) open(t *testing.T) {
	t.Helper()
	db, err := leveldb.OpenFile(f.dbPath, nil)
	require.NoError(t, err)
	f.db = db
	scanner := NewScanner(testLogger(), f.client.sidecar, &scannerMemoBridge{asgards: ttypes.Vaults{
		{PubKey: f.source, PubKeyEddsa: f.source, PubKeyFrost: f.source},
		{PubKey: f.destination, PubKeyEddsa: f.destination, PubKeyFrost: f.destination},
	}}, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(NewReorgStore(db))
	scanner.SetAuthenticatedInternalInboundReplayStore(newAuthenticatedInternalInboundReplayStore(db))
	require.NoError(t, scanner.internalReplayStore.ResetClaims())
	scanner.setCurrentHeight(200)
	f.client.scanner = scanner
	f.client.signedTxPayloadStore = newSignedTxPayloadStore(db)
}

func TestSpentRefsImportCleanupBeforeMutationAndReplay(t *testing.T) {
	for _, payout := range []bool{false, true} {
		for _, committed := range []bool{false, true} {
			for _, beforeImport := range []bool{false, true} {
				name := map[bool]string{false: "migration", true: "payout"}[payout] + "/" +
					map[bool]string{false: "certified", true: "committed"}[committed] + "/" +
					map[bool]string{false: "during import", true: "before import"}[beforeImport]
				t.Run(name, func(t *testing.T) {
					f := newSpentRefsReplayFixture(t, payout)
					entered, resume := make(chan struct{}), make(chan struct{})
					var resumeOnce sync.Once
					unblock := func() { resumeOnce.Do(func() { close(resume) }) }
					var barrier sync.RWMutex
					var firstAdmission sync.Once
					f.client.SetSpentOutputRefsGuard(func(tx common.ObservedTx) (func(), bool) {
						if beforeImport {
							firstAdmission.Do(func() { close(entered); <-resume })
						}
						barrier.RLock()
						reverted, err := f.client.scanner.reorgStore.IsRevertedTx(tx.Tx.ID.String())
						if err != nil || reverted {
							barrier.RUnlock()
							return nil, false
						}
						return barrier.RUnlock, true
					})
					if !beforeImport {
						f.beforeMarkSpent = func() { close(entered); <-resume }
					}
					outcomes := make(chan bool, 2)
					done := make(chan struct{})
					go func() {
						defer close(done)
						f.client.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
							Tx: f.tx, WireMemo: f.tx.Tx.Memo, ChainCommitted: committed, ReplaySource: !committed,
						}, func(applied bool) { outcomes <- applied })
					}()
					t.Cleanup(func() {
						unblock()
						select {
						case <-done:
						case <-time.After(3 * time.Second):
							t.Error("import did not stop")
						}
					})
					select {
					case <-entered:
					case <-time.After(3 * time.Second):
						t.Fatal("import did not reach pause")
					}
					cleanupDone := make(chan error, 1)
					go func() {
						barrier.Lock()
						defer barrier.Unlock()
						cleanupDone <- f.client.QuarantinePermanentErrata(common.ErrataTx{
							Chain: common.XMRChain, Id: f.tx.Tx.ID,
						}, 100)
					}()
					if beforeImport {
						select {
						case cleanupErr := <-cleanupDone:
							require.NoError(t, cleanupErr)
						case <-time.After(3 * time.Second):
							t.Fatal("cleanup waited for an import that had not started")
						}
					} else {
						// A queued writer prevents the replay's next reader from
						// overtaking cleanup after MarkSpent releases its reader.
						require.Eventually(t, func() bool {
							if barrier.TryRLock() {
								barrier.RUnlock()
								return false
							}
							return true
						}, 3*time.Second, time.Millisecond)
						select {
						case <-cleanupDone:
							t.Fatal("cleanup crossed the in-flight MarkSpent mutation")
						default:
						}
					}
					unblock()
					if !beforeImport {
						select {
						case cleanupErr := <-cleanupDone:
							require.NoError(t, cleanupErr)
						case <-time.After(3 * time.Second):
							t.Fatal("cleanup did not finish after import")
						}
					}
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("import did not reach a terminal outcome")
					}
					require.Len(t, outcomes, 1, "one terminal callback, with no retry after quarantine")
					require.False(t, <-outcomes)
					require.EqualValues(t, map[bool]int{false: 1, true: 0}[beforeImport], f.markSpentCalls.Load())
					require.Zero(t, f.markObservedCalls.Load(), "replay must recheck admission after import")

					require.NoError(t, f.db.Close())
					f.open(t)
					reverted, err := f.client.scanner.reorgStore.IsRevertedTx(f.tx.Tx.ID.String())
					require.NoError(t, err)
					require.True(t, reverted)
					quarantined, err := f.client.signedTxPayloadStore.txQuarantinedLocked(f.tx.Tx.ID.String())
					require.NoError(t, err)
					require.True(t, quarantined, "cleanup must preserve the signing fence across reopen")
					count, err := f.client.scanner.internalReplayStore.Count()
					require.NoError(t, err)
					require.Zero(t, count)
					receipts, err := f.client.scanner.reorgStore.ListObservationReceipts()
					require.NoError(t, err)
					require.Empty(t, receipts, "rejected recovery must not create source observations")
				})
			}
		}
	}
}

func TestSpentRefsMigrationRefreshDoesNotBlockErrataCleanup(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "certified", true: "committed"}[committed], func(t *testing.T) {
			f := newSpentRefsReplayFixture(t, false)
			entered, resume := make(chan struct{}), make(chan struct{})
			var resumeOnce, registrationOnce sync.Once
			unblock := func() { resumeOnce.Do(func() { close(resume) }) }
			f.beforeVaultRegistration = func() {
				registrationOnce.Do(func() { close(entered); <-resume })
			}
			var barrier sync.RWMutex
			f.client.SetSpentOutputRefsGuard(func(tx common.ObservedTx) (func(), bool) {
				barrier.RLock()
				reverted, err := f.client.scanner.reorgStore.IsRevertedTx(tx.Tx.ID.String())
				if err != nil || reverted {
					barrier.RUnlock()
					return nil, false
				}
				return barrier.RUnlock, true
			})
			outcomes, done := make(chan bool, 2), make(chan struct{})
			go func() {
				defer close(done)
				f.client.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
					Tx: f.tx, WireMemo: f.tx.Tx.Memo, ChainCommitted: committed, ReplaySource: !committed,
				}, func(applied bool) { outcomes <- applied })
			}()
			t.Cleanup(func() {
				unblock()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("import did not stop")
				}
			})
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("vault registration did not start")
			}
			require.EqualValues(t, 1, f.markSpentCalls.Load())
			// Registration can rewind the scanner, so it must retain scanMu.
			if f.client.scanner.scanMu.TryLock() {
				f.client.scanner.scanMu.Unlock()
				t.Fatal("vault registration released the scanner lock")
			}
			if !barrier.TryLock() {
				t.Fatal("vault refresh holds errata admission while waiting for HTTP")
			}
			cleanupErr := f.client.QuarantinePermanentErrata(common.ErrataTx{
				Chain: common.XMRChain, Id: f.tx.Tx.ID,
			}, 100)
			barrier.Unlock()
			require.NoError(t, cleanupErr)
			unblock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("import did not finish after vault refresh")
			}
			require.Len(t, outcomes, 1)
			require.False(t, <-outcomes, "replay must recheck admission after refresh")
			require.Zero(t, f.markObservedCalls.Load())
			count, err := f.client.scanner.internalReplayStore.Count()
			require.NoError(t, err)
			require.Zero(t, count, "cleanup must prevent replay enrollment")
		})
	}
}

func TestSpentRefsMigrationReplayRetainsObligationAcrossReopen(t *testing.T) {
	f := newSpentRefsReplayFixture(t, false)
	allowSpentOutputRefsImports(f.client)
	entered, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	unblock := func() { resumeOnce.Do(func() { close(resume) }) }
	var firstObserved sync.Once
	f.beforeMarkObserved = func() { firstObserved.Do(func() { close(entered); <-resume }) }
	outcome, done := make(chan bool, 1), make(chan struct{})
	go func() {
		defer close(done)
		f.client.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
			Tx: f.tx, WireMemo: f.tx.Tx.Memo, ReplaySource: true,
		}, func(applied bool) { outcome <- applied })
	}()
	t.Cleanup(func() {
		unblock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("import did not stop")
		}
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("import did not reach replay reconciliation")
	}
	count, err := f.client.scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count, "replay must be durable before sidecar reconciliation completes")
	select {
	case <-outcome:
		t.Fatal("success was reported before replay reconciliation")
	default:
	}
	unblock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("import did not finish replay reconciliation")
	}
	require.True(t, <-outcome)
	require.EqualValues(t, 1, f.markSpentCalls.Load())

	items, _, err := f.client.scanner.replayAuthenticatedInternalInbounds(201, 201)
	require.NoError(t, err)
	require.Len(t, items, 2)
	items[0].OnObservedTxStored()
	items[1].OnObservedTxRejected(errors.New("observer storage unavailable"))
	count, err = f.client.scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count, "partial acceptance cannot retire the durable obligation")

	require.NoError(t, f.db.Close())
	f.open(t)
	items, _, err = f.client.scanner.replayAuthenticatedInternalInbounds(202, 202)
	require.NoError(t, err)
	require.Len(t, items, 2, "reopen must recover both sides of the migration")
	require.True(t, items[0].ObservedVaultPubKey.Equals(f.destination))
	require.True(t, items[1].ObservedVaultPubKey.Equals(f.source))
	for _, item := range items {
		require.Equal(t, f.tx.Tx.ID.String(), item.Tx)
		require.Equal(t, int64(100), item.BlockHeight)
		require.Equal(t, f.tx.Tx.Memo, item.Memo)
	}
	require.Equal(t, f.tx.XmrTxKey, items[1].TxKey)
	require.Equal(t, f.tx.SpentOutputRefs[0].KeyImage, items[1].SpentOutputRefs[0].KeyImage)
	items[1].OnObservedTxStored()
	count, err = f.client.scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Equal(t, 1, count)
	items[0].OnObservedTxStored()
	count, err = f.client.scanner.internalReplayStore.Count()
	require.NoError(t, err)
	require.Zero(t, count, "only both durable acceptances retire the replay")
}
