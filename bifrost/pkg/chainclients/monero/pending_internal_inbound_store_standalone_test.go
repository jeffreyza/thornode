package monero

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"filippo.io/edwards25519"
	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func standaloneReplayVault(t *testing.T, seed string) (common.PubKey, common.Address) {
	t.Helper()
	config := cosmos.GetConfig()
	config.SetBech32PrefixForAccount("tthor", "tthorpub")
	config.SetBech32PrefixForValidator("tthorv", "tthorvpub")
	config.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
	digest := sha512.Sum512([]byte("authenticated-internal-replay:" + seed))
	scalar, err := new(edwards25519.Scalar).SetUniformBytes(digest[:])
	if err != nil {
		t.Fatal(err)
	}
	point := new(edwards25519.Point).ScalarBaseMult(scalar)
	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(point.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	address, err := pubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatal(err)
	}
	return pubKey, address
}

func standaloneReplayHash(label string) string {
	digest := sha256.Sum256([]byte(label))
	return hex.EncodeToString(digest[:])
}

func standaloneReplayRecord(t *testing.T, label string, height int64) authenticatedInternalInboundReplay {
	t.Helper()
	sourceVault, sourceAddress := standaloneReplayVault(t, "source-"+label)
	destinationVault, destinationAddress := standaloneReplayVault(t, "destination-"+label)
	return authenticatedInternalInboundReplay{
		TxID:                   standaloneReplayHash("tx-" + label),
		TxKey:                  standaloneReplayHash("tx-key-" + label),
		BlockHeight:            height,
		BlockHash:              standaloneReplayHash("block-" + label),
		PreviousHash:           standaloneReplayHash("parent-" + label),
		SourceAddress:          sourceAddress.String(),
		SourceVaultPubKey:      sourceVault.String(),
		ToAddress:              destinationAddress.String(),
		DestinationVaultPubKey: destinationVault.String(),
		AmountThorchain:        2,
		FeeThorchain:           1,
		Memo:                   fmt.Sprintf("MIGRATE:%d", height),
		SpentOutputRefs: []authenticatedInternalSpentRef{{
			TxHash:      standaloneReplayHash("input-" + label),
			OutputIndex: 0,
			KeyImage:    standaloneReplayHash("key-image-" + label),
		}},
	}
}

func TestAuthenticatedInternalInboundReplayClaimsOnceUntilTerminalCallback(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newAuthenticatedInternalInboundReplayStore(db)
	record := standaloneReplayRecord(t, "once", 100)
	if err = store.Enqueue(record); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan []authenticatedInternalInboundClaim, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claims, claimErr := store.Claim(1)
			results <- claims
			errs <- claimErr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for claimErr := range errs {
		if claimErr != nil {
			t.Fatal(claimErr)
		}
	}
	var claim authenticatedInternalInboundClaim
	total := 0
	for claims := range results {
		total += len(claims)
		if len(claims) == 1 {
			claim = claims[0]
		}
	}
	if total != 1 {
		t.Fatalf("expected one exclusive prefetch claim, got %d", total)
	}
	if err = store.Release(claim); err != nil {
		t.Fatal(err)
	}
	claims, err := store.Claim(1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("expected rejected claim to be retryable, claims=%d err=%v", len(claims), err)
	}
	if err = store.Acknowledge(claims[0]); err != nil {
		t.Fatal(err)
	}
	count, err := store.Count()
	if err != nil || count != 0 {
		t.Fatalf("expected acknowledged replay deletion, count=%d err=%v", count, err)
	}
}

func TestAuthenticatedInternalInboundRemoteReconciliationClaimsAtMostOnePerBlock(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newAuthenticatedInternalInboundReplayStore(db)
	for i := 0; i < 2; i++ {
		if err = store.Enqueue(standaloneReplayRecord(t, fmt.Sprintf("remote-cap-%d", i), int64(200+i))); err != nil {
			t.Fatal(err)
		}
	}

	claims, err := store.Claim(maxAuthenticatedInternalInboundClaims)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("remote replay reconciliation must claim one record per scanned block, got %d", len(claims))
	}
	count, err := store.Count()
	if err != nil || count != 2 {
		t.Fatalf("claiming one replay must preserve the durable remainder: count=%d err=%v", count, err)
	}
}

func TestAuthenticatedInternalInboundReplayResetClaimsAfterRestart(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newAuthenticatedInternalInboundReplayStore(db)
	if err = store.Enqueue(standaloneReplayRecord(t, "restart", 101)); err != nil {
		t.Fatal(err)
	}
	claims, err := store.Claim(1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("initial claim: claims=%d err=%v", len(claims), err)
	}
	restarted := newAuthenticatedInternalInboundReplayStore(db)
	if err = restarted.ResetClaims(); err != nil {
		t.Fatal(err)
	}
	claims, err = restarted.Claim(1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("expected startup claim reset, claims=%d err=%v", len(claims), err)
	}
}

func TestAuthenticatedInternalInboundReplayReclaimsExpiredLease(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newAuthenticatedInternalInboundReplayStore(db)
	now := time.Unix(1_000_000, 0)
	store.now = func() time.Time { return now }
	if err = store.Enqueue(standaloneReplayRecord(t, "lease", 102)); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(1)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: claims=%d err=%v", len(first), err)
	}
	now = now.Add(authenticatedInternalInboundClaimLease + time.Second)
	second, err := store.Claim(1)
	if err != nil || len(second) != 1 {
		t.Fatalf("expected stale claim lease to be reclaimed, claims=%d err=%v", len(second), err)
	}
	if second[0].Token == first[0].Token {
		t.Fatal("expected reclaimed lease to receive a new token")
	}
}

func TestAuthenticatedInternalInboundReplayRejectsConflictAndBoundsOutbox(t *testing.T) {
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := newAuthenticatedInternalInboundReplayStore(db)
	record := standaloneReplayRecord(t, "conflict", 100)
	if err = store.Enqueue(record); err != nil {
		t.Fatal(err)
	}
	conflict := record
	conflict.AmountThorchain++
	if err = store.Enqueue(conflict); err == nil {
		t.Fatal("expected conflicting exact identity to be rejected")
	}
	for i := 1; i < maxAuthenticatedInternalInboundReplays; i++ {
		if err = store.Enqueue(standaloneReplayRecord(t, fmt.Sprintf("cap-%d", i), int64(100+i))); err != nil {
			t.Fatalf("fill replay %d: %v", i, err)
		}
	}
	if err = store.Enqueue(standaloneReplayRecord(t, "overflow", 999)); err == nil {
		t.Fatal("expected bounded replay outbox to reject overflow")
	}
}
