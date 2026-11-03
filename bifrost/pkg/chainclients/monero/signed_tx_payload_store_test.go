package monero

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func testStoredSignedTxPayload(t *testing.T, txHash, txMetadata string) []byte {
	t.Helper()
	payload, err := json.Marshal(signedTxPayload{TxHash: txHash, TxMetadata: txMetadata})
	if err != nil {
		t.Fatalf("marshal stored signed tx payload: %v", err)
	}
	return payload
}

func equalFoldStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[strings.ToLower(value)]++
	}
	for _, value := range right {
		key := strings.ToLower(value)
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func testSignedTxClaimPlan(
	cacheVault,
	cacheHash,
	logicalHash,
	ownerVault,
	msgID,
	planHash string,
	observeKeys ...string,
) signedTxPayloadClaimPlan {
	epochs := make(map[string]uint64, len(observeKeys))
	for _, key := range observeKeys {
		epochs[key] = 0
	}
	return signedTxPayloadClaimPlan{
		CacheVault:   cacheVault,
		CacheHash:    cacheHash,
		LogicalHash:  logicalHash,
		OwnerVault:   ownerVault,
		MsgID:        msgID,
		PlanHash:     planHash,
		ObserveKeys:  observeKeys,
		ObserveEpoch: epochs,
		TxOutJSON:    []byte(`{"test_txout":true}`),
	}
}

func TestSignedTxPayloadStoreEnforcesLogicalOwnershipAndTombstone(t *testing.T) {
	t.Parallel()

	store := newTestSignedTxPayloadStore(t)
	firstPlan := testSignedTxClaimPlan(
		"broadcast-vault-a-XMR",
		"CACHE-A",
		"LOGICAL-OUTBOUND",
		"vault-a",
		"MSG-A",
		"PLAN-A",
		"OBSERVE-A",
	)
	firstPayload := testStoredSignedTxPayload(t, "TX-A", "aa")
	if err := store.Reserve(firstPlan); err != nil {
		t.Fatalf("reserve first payload: %v", err)
	}
	if err := store.Set(firstPlan, "TX-A", firstPayload); err != nil {
		t.Fatalf("store first payload: %v", err)
	}
	if err := store.Set(firstPlan, "TX-A", firstPayload); err != nil {
		t.Fatalf("idempotently store first payload: %v", err)
	}

	secondPlan := testSignedTxClaimPlan(
		"broadcast-vault-b-XMR",
		"CACHE-B",
		firstPlan.LogicalHash,
		"vault-b",
		"MSG-B",
		"PLAN-B",
		"OBSERVE-B",
	)
	if err := store.Reserve(secondPlan); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("expected cross-vault ownership rejection, got %v", err)
	}
	if _, _, err := store.GetOwned(secondPlan.CacheVault, secondPlan.CacheHash, secondPlan.LogicalHash); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("expected cross-vault recovery to fail closed, got %v", err)
	}

	claim, completed, err := store.CompleteObserved("observe-a", "vault-a", "tx-a")
	if err != nil || !completed || claim.State != signedTxClaimObserved {
		t.Fatalf("complete first observed claim: completed=%t claim=%+v err=%v", completed, claim, err)
	}
	if _, ok, err := store.Get(firstPlan.CacheVault, firstPlan.CacheHash); err != nil || !ok {
		t.Fatalf("final observation must retain exact payload for deep-reorg recovery: ok=%t err=%v", ok, err)
	}
	if _, _, err := store.GetOwned(firstPlan.CacheVault, firstPlan.CacheHash, firstPlan.LogicalHash); !errors.Is(err, errXMRSigningClaimObserved) {
		t.Fatalf("expected terminal observed tombstone, got %v", err)
	}
	if err := store.Reserve(firstPlan); !errors.Is(err, errXMRSigningClaimObserved) {
		t.Fatalf("expected observed claim never to be recreated, got %v", err)
	}
}

func TestSignedTxPayloadStoreRefusesPayloadReplacement(t *testing.T) {
	t.Parallel()

	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan("broadcast-vault-XMR", "CACHE", "LOGICAL", "vault", "MSG", "PLAN", "OBSERVE")
	if err := store.Reserve(plan); err != nil {
		t.Fatalf("reserve first payload: %v", err)
	}
	if err := store.Set(plan, "TX-A", testStoredSignedTxPayload(t, "TX-A", "first")); err != nil {
		t.Fatalf("store first payload: %v", err)
	}
	if err := store.Set(plan, "TX-B", testStoredSignedTxPayload(t, "TX-B", "second")); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("expected payload replacement rejection, got %v", err)
	}
}

func TestSignedTxPayloadStoreReservesExactPlanAndRejectsLateResult(t *testing.T) {
	t.Parallel()

	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan("broadcast-vault-a-XMR", "CACHE-A", "LOGICAL", "vault-a", "MSG-A", "PLAN-A", "OBSERVE-A")
	if err := store.Reserve(plan); err != nil {
		t.Fatalf("reserve first plan: %v", err)
	}
	if _, ok, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); err != nil || ok {
		t.Fatalf("reservation without payload should reach exact-plan retry: ok=%t err=%v", ok, err)
	}
	if err := store.Reserve(plan); err != nil {
		t.Fatalf("reserve exact retry plan: %v", err)
	}
	changedPlan := plan
	changedPlan.PlanHash = "PLAN-B"
	if err := store.Reserve(changedPlan); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("expected same-vault changed plan to remain blocked by the durable reservation, got %v", err)
	}
	changedIntent := plan
	changedIntent.TxOutJSON = []byte(`{"original_memo":"OUT:DIFFERENT"}`)
	if err := store.Reserve(changedIntent); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("expected changed semantic txout under the same FROST plan to be rejected, got %v", err)
	}

	claim, completed, err := store.CompleteObserved("OBSERVE-A", "vault-a", "PEER-TX")
	if err != nil || !completed || claim.State != signedTxClaimObserved {
		t.Fatalf("complete peer-observed reservation: completed=%t claim=%+v err=%v", completed, claim, err)
	}
	if err := store.Set(plan, "TX-A", testStoredSignedTxPayload(t, "TX-A", "aa")); !errors.Is(err, errXMRSigningClaimObserved) {
		t.Fatalf("expected late FROST result to be rejected by tombstone, got %v", err)
	}
}

func TestSignedTxPayloadStoreRejectsObservationLostWakeup(t *testing.T) {
	t.Parallel()

	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan("broadcast-vault-XMR", "CACHE", "LOGICAL", "vault", "MSG", "PLAN", "OBSERVE")
	var err error
	plan.ObserveEpoch, err = store.SnapshotObserveEpochs(plan.ObserveKeys)
	if err != nil {
		t.Fatalf("snapshot observation epochs: %v", err)
	}
	if _, _, err = store.CompleteObservedExact("OBSERVE", "vault", testXMRHash("peer-before-reserve")); err != nil {
		t.Fatalf("record observation between snapshot and reserve: %v", err)
	}
	if err = store.Reserve(plan); !errors.Is(err, errXMRSigningClaimStale) {
		t.Fatalf("expected stale snapshot to block reservation, got %v", err)
	}
}

func TestSignedTxPayloadStoreLogicalObservationBeforeClaimCreatesTombstone(t *testing.T) {
	t.Parallel()

	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan("broadcast-vault-XMR", "CACHE", "LOGICAL", "vault", "MSG", "PLAN", "OBSERVE")
	if _, completed, err := store.CompleteObservedLogical(plan.LogicalHash, plan.ObserveKeys, testXMRHash("peer-fulfillment")); err != nil || !completed {
		t.Fatalf("record logical fulfillment before claim: completed=%t err=%v", completed, err)
	}
	plan.ObserveEpoch, _ = store.SnapshotObserveEpochs(plan.ObserveKeys)
	if err := store.Reserve(plan); !errors.Is(err, errXMRSigningClaimObserved) {
		t.Fatalf("expected logical tombstone to block later reservation, got %v", err)
	}
}

func TestSignedTxPayloadStoreDifferentExactAliasTxQuarantinesLocalPayload(t *testing.T) {
	t.Parallel()

	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan("broadcast-vault-XMR", "CACHE", "LOGICAL", "vault", "MSG", "PLAN", "OBSERVE")
	localTxHash := testXMRHash("local-exact-alias-payload")
	peerTxHash := testXMRHash("peer-exact-alias-fulfillment")
	if err := store.Reserve(plan); err != nil {
		t.Fatalf("reserve local payload: %v", err)
	}
	if err := store.Set(plan, localTxHash, testStoredSignedTxPayload(t, localTxHash, "aa")); err != nil {
		t.Fatalf("set local payload: %v", err)
	}
	if _, matched, err := store.CommitUnfinalizedExact("OBSERVE", "vault", peerTxHash); err != nil || matched {
		t.Fatalf("record peer alias tx: matched=%t err=%v", matched, err)
	}
	if err := store.Set(plan, localTxHash, testStoredSignedTxPayload(t, localTxHash, "aa")); !errors.Is(err, errXMRSigningClaimAliasCommitted) {
		t.Fatalf("different peer alias did not block late Set: %v", err)
	}
	if _, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimAliasCommitted) {
		t.Fatalf("different peer alias did not block local recovery: %v", err)
	}
	called := false
	if _, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		localTxHash, plan.MsgID, plan.PlanHash,
		func() (string, error) { called = true; return localTxHash, nil },
	); !errors.Is(err, errXMRSigningClaimAliasCommitted) {
		t.Fatalf("different peer alias did not block local broadcast: %v", err)
	}
	if called {
		t.Fatal("local broadcast ran while a different alias transaction was committed")
	}
	if _, matched, err := store.CompleteObservedExact("OBSERVE", "vault", peerTxHash); err != nil || matched {
		t.Fatalf("finalize different peer alias: matched=%t err=%v", matched, err)
	}
	if _, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimAliasCommitted) {
		t.Fatalf("final exact mismatch released local payload without semantic proof: %v", err)
	}
	if _, completed, err := store.CompleteObservedLogical(plan.LogicalHash, plan.ObserveKeys, peerTxHash); err != nil || !completed {
		t.Fatalf("semantic resolution did not terminalize peer fulfillment: completed=%t err=%v", completed, err)
	}
	if _, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimObserved) {
		t.Fatalf("semantic peer fulfillment did not terminalize local claim: %v", err)
	}
}

func TestXMRMemolessLogicalIdentitySurvivesVaultReschedule(t *testing.T) {
	t.Parallel()

	oldVault := testVaultPubKey(t, "memoless-old-vault")
	newVault := testVaultPubKey(t, "memoless-new-vault")
	originalMemo := "OUT:" + testXMRHash("memoless-inbound")
	coins := common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(25)))
	base := stypes.TxOutItem{
		Chain:        common.XMRChain,
		ToAddress:    testOutboundXMRDestination(),
		Coins:        coins,
		Memo:         "",
		OriginalMemo: originalMemo,
	}
	oldTx := base
	oldTx.VaultPubKeyFrost = oldVault
	newTx := base
	newTx.VaultPubKeyFrost = newVault
	oldIdentity, err := xmrSigningCacheIdentity(oldTx)
	if err != nil {
		t.Fatalf("derive old memoless identity: %v", err)
	}
	newIdentity, err := xmrSigningCacheIdentity(newTx)
	if err != nil {
		t.Fatalf("derive new memoless identity: %v", err)
	}
	if oldIdentity.LogicalHash != newIdentity.LogicalHash {
		t.Fatalf("memoless vault reschedule changed semantic logical identity: old=%s new=%s", oldIdentity.LogicalHash, newIdentity.LogicalHash)
	}
	if slicesEqualStrings(oldIdentity.ObserveKeys, newIdentity.ObserveKeys) {
		t.Fatal("wire observation aliases should remain source-vault scoped")
	}

	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan(
		txCacheVault(oldTx, common.XMRChain),
		oldIdentity.CacheHash,
		oldIdentity.LogicalHash,
		oldVault.String(),
		"MSG-OLD",
		"PLAN-OLD",
		oldIdentity.ObserveKeys...,
	)
	plan.TxOutJSON, err = encodeDurableXMRClaimTxOut(oldTx)
	if err != nil {
		t.Fatalf("encode durable old txout: %v", err)
	}
	if err = store.Reserve(plan); err != nil {
		t.Fatalf("reserve old memoless claim: %v", err)
	}
	oldTxHash := testXMRHash("old-memoless-signed")
	if err = store.Set(plan, oldTxHash, testStoredSignedTxPayload(t, oldTxHash, "aa")); err != nil {
		t.Fatalf("store old memoless payload: %v", err)
	}

	client := &Client{
		chain:                common.XMRChain,
		signedTxPayloadStore: store,
		signerCacheManager:   newTestSignerCacheManager(t),
		logger:               testLogger(),
	}
	winnerHash := testXMRHash("new-vault-memoless-winner")
	newVaultAddress, err := newVault.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive new vault address: %v", err)
	}
	winner := common.NewObservedTx(
		common.NewTx(
			common.TxID(winnerHash),
			newVaultAddress,
			base.ToAddress,
			coins,
			nil,
			originalMemo,
		),
		100,
		newVault,
		100, // Peer-provided final-looking heights are not authoritative finality.
	)
	if err = client.markImportedXMROutboundSigned(winner, false); err != nil {
		t.Fatalf("record verified new-vault fulfillment: %v", err)
	}
	if _, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimCommitted) {
		t.Fatalf("expected pre-final new-vault fulfillment to suspend the old claim, got %v", err)
	}
	called := false
	if _, err = store.WithBroadcastAuthorization(
		plan.CacheVault,
		plan.CacheHash,
		plan.LogicalHash,
		plan.OwnerVault,
		oldTxHash,
		plan.MsgID,
		plan.PlanHash,
		func() (string, error) { called = true; return oldTxHash, nil },
	); !errors.Is(err, errXMRSigningClaimCommitted) {
		t.Fatalf("expected old payload broadcast to be blocked by different fulfillment, got %v", err)
	}
	if called {
		t.Fatal("broadcast callback ran after different memoless fulfillment")
	}
}

func TestXMRRagnarokSpentRefImportRetainsExactClaimWithoutInboundTxID(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKey:      vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins:            common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(25))),
		Memo:             "RAGNAROK:123",
	}
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatalf("derive RAGNAROK identity: %v", err)
	}
	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan(
		txCacheVault(tx, common.XMRChain),
		identity.CacheHash,
		identity.LogicalHash,
		vaultPubKey.String(),
		"MSG-RAGNAROK",
		"PLAN-RAGNAROK",
		identity.ObserveKeys...,
	)
	plan.TxOutJSON, err = encodeDurableXMRClaimTxOut(tx)
	if err != nil {
		t.Fatalf("encode durable RAGNAROK txout: %v", err)
	}
	if err = store.Reserve(plan); err != nil {
		t.Fatalf("reserve RAGNAROK claim: %v", err)
	}
	txHash := testXMRHash("ragnarok-fulfillment")
	if err = store.Set(plan, txHash, testStoredSignedTxPayload(t, txHash, "aa")); err != nil {
		t.Fatalf("store RAGNAROK payload: %v", err)
	}

	client := &Client{
		chain:                common.XMRChain,
		signedTxPayloadStore: store,
		logger:               testLogger(),
	}
	observed := common.NewObservedTx(
		common.NewTx(
			common.TxID(txHash),
			vaultAddress,
			tx.ToAddress,
			tx.Coins,
			nil,
			tx.Memo,
		),
		100,
		vaultPubKey,
		110,
	)
	if err = client.markImportedXMROutboundSigned(observed, false); err != nil {
		t.Fatalf("record verified RAGNAROK fulfillment: %v", err)
	}
	if _, found, getErr := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); getErr != nil || !found {
		t.Fatalf("expected imported RAGNAROK fulfillment without an inbound tx id to retain exact recovery, found=%t err=%v", found, getErr)
	}
}

func TestXMRSigningCacheIdentityKeepsScheduledMigrationGlobal(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		Memo:             "MIGRATE:123",
		VaultPubKeyFrost: testVaultPubKey(t, "scheduled-migration-vault"),
		ToAddress:        testOutboundXMRDestination(),
	}
	tx.SigningID = "scheduled-item-a"
	first, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatalf("first scheduled identity: %v", err)
	}
	tx.SigningID = "scheduled-item-b"
	second, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatalf("second scheduled identity: %v", err)
	}
	if first.CacheHash != tx.CacheHash() || first.LogicalHash != first.ObserveKeys[0] ||
		first.CacheHash != second.CacheHash || first.LogicalHash != second.LogicalHash ||
		!slicesEqualStrings(first.ObserveKeys, second.ObserveKeys) {
		t.Fatalf("scheduled migration must retain one vault-independent identity: first=%+v second=%+v", first, second)
	}
}

func TestXMRMemolessPlanUsesOnlySemanticObservationKey(t *testing.T) {
	t.Parallel()

	vault := testVaultPubKey(t, "memoless-recovery-alias-vault")
	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        testOutboundXMRDestination(),
		VaultPubKeyFrost: vault,
		Coins: common.NewCoins(
			common.NewCoin(common.XMRAsset, cosmos.NewUint(10)),
		),
		Memo:         "",
		OriginalMemo: "OUT:" + testXMRHash("memoless-recovery-alias-inbound"),
	}
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		t.Fatalf("derive memoless recovery identity: %v", err)
	}
	if len(identity.ObserveKeys) != 1 {
		t.Fatalf("expected only the on-chain semantic memo alias, got %d", len(identity.ObserveKeys))
	}

	other := tx
	other.OriginalMemo = "OUT:" + testXMRHash("another-inbound")
	otherIdentity, err := xmrSigningCacheIdentity(other)
	if err != nil {
		t.Fatal(err)
	}
	if slicesEqualStrings(identity.ObserveKeys, otherIdentity.ObserveKeys) {
		t.Fatal("distinct memoless planned payouts must not share an empty-memo claim")
	}
	// Reserve canonicalization sorts and lower-cases aliases. Reverse that
	// canonical order to model the construction order recovered from TxOutJSON;
	// recovery must compare alias sets, not slice positions.
	canonical, err := normalizeObserveKeys(identity.ObserveKeys)
	if err != nil {
		t.Fatalf("canonicalize memoless aliases: %v", err)
	}
	reversed := append([]string(nil), canonical...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	for i := range reversed {
		reversed[i] = strings.ToUpper(reversed[i])
	}
	if !equalFoldStringSets(reversed, canonical) {
		t.Fatal("memoless recovery rejected the same aliases in construction order")
	}
}

func TestXMRConsolidationObservationReleasesOnlyActiveAlias(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	store := newTestSignedTxPayloadStore(t)
	signerCache := newTestSignerCacheManager(t)
	client := &Client{
		chain:                common.XMRChain,
		signerCacheManager:   signerCache,
		signedTxPayloadStore: store,
		logger:               testLogger(),
	}
	baseTx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		ToAddress:        vaultAddress,
		VaultPubKey:      vaultPubKey,
		VaultPubKeyFrost: vaultPubKey,
		Coins: common.NewCoins(
			common.NewCoin(common.XMRAsset, cosmos.NewUint(10)),
		),
		Memo: "CONSOLIDATE",
	}
	if _, err := xmrSigningCacheIdentity(baseTx); err == nil {
		t.Fatal("expected consolidation without signing id to fail closed")
	}

	firstTx := baseTx
	firstTx.SigningID = "xmr-consolidation:first"
	firstIdentity, err := xmrSigningCacheIdentity(firstTx)
	if err != nil {
		t.Fatalf("derive first consolidation identity: %v", err)
	}
	secondTx := baseTx
	secondTx.SigningID = "xmr-consolidation:second"
	secondIdentity, err := xmrSigningCacheIdentity(secondTx)
	if err != nil {
		t.Fatalf("derive second consolidation identity: %v", err)
	}
	if firstIdentity.CacheHash == secondIdentity.CacheHash || firstIdentity.LogicalHash == secondIdentity.LogicalHash {
		t.Fatal("separate consolidation operations must have unique primary identities")
	}
	if !slicesEqualStrings(firstIdentity.ObserveKeys, secondIdentity.ObserveKeys) {
		t.Fatal("one vault must retain one active consolidation observation alias")
	}

	cacheVault := txCacheVault(firstTx, common.XMRChain)
	firstPlan := testSignedTxClaimPlan(
		cacheVault,
		firstIdentity.CacheHash,
		firstIdentity.LogicalHash,
		vaultPubKey.String(),
		"MSG-CONSOLIDATE-1",
		"PLAN-CONSOLIDATE-1",
		firstIdentity.ObserveKeys...,
	)
	if err = store.Reserve(firstPlan); err != nil {
		t.Fatalf("reserve first consolidation: %v", err)
	}
	txHash := testXMRHash("consolidation-observed-cleanup")
	if err = store.Set(firstPlan, txHash, testStoredSignedTxPayload(t, txHash, "aa")); err != nil {
		t.Fatalf("store first consolidation payload: %v", err)
	}
	if err = signerCache.SetSigned(firstIdentity.CacheHash, cacheVault, txHash); err != nil {
		t.Fatalf("set first consolidation signer cache: %v", err)
	}
	client.OnObservedTxIn(stypes.TxInItem{
		Tx:                   txHash,
		Sender:               vaultAddress.String(),
		To:                   vaultAddress.String(),
		Coins:                firstTx.Coins,
		Memo:                 firstTx.Memo,
		ObservedVaultPubKey:  vaultPubKey,
		CommittedUnFinalised: true,
	}, 100)
	if _, ok, getErr := store.Get(cacheVault, firstIdentity.CacheHash); getErr != nil || !ok {
		t.Fatalf("unfinalized observation removed recoverable payload: ok=%t err=%v", ok, getErr)
	}

	client.OnObservedTxIn(stypes.TxInItem{
		Tx:                  txHash,
		Sender:              vaultAddress.String(),
		To:                  vaultAddress.String(),
		Coins:               firstTx.Coins,
		Memo:                firstTx.Memo,
		ObservedVaultPubKey: vaultPubKey,
	}, 100)
	if _, ok, getErr := store.Get(cacheVault, firstIdentity.CacheHash); getErr != nil || !ok {
		t.Fatalf("expected committed consolidation to retain deep-reorg payload: ok=%t err=%v", ok, getErr)
	}
	if signerCache.HasSigned(firstIdentity.CacheHash) {
		t.Fatal("expected committed consolidation to clear its signer-cache entry")
	}
	if _, _, getErr := store.GetOwned(cacheVault, firstIdentity.CacheHash, firstIdentity.LogicalHash); !errors.Is(getErr, errXMRSigningClaimObserved) {
		t.Fatalf("expected first consolidation tombstone, got %v", getErr)
	}

	secondPlan := testSignedTxClaimPlan(
		cacheVault,
		secondIdentity.CacheHash,
		secondIdentity.LogicalHash,
		vaultPubKey.String(),
		"MSG-CONSOLIDATE-2",
		"PLAN-CONSOLIDATE-2",
		secondIdentity.ObserveKeys...,
	)
	secondPlan.ObserveEpoch, err = store.SnapshotObserveEpochs(secondIdentity.ObserveKeys)
	if err != nil {
		t.Fatalf("snapshot second consolidation observation epoch: %v", err)
	}
	if err = store.Reserve(secondPlan); err != nil {
		t.Fatalf("expected observed consolidation to release only its active alias: %v", err)
	}
}

func slicesEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
