package monero

import (
	"errors"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
)

func newStandaloneSignedStore(t *testing.T) *signedTxPayloadStore {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatalf("open standalone store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newSignedTxPayloadStore(db)
}

func standaloneClaimPlan(logicalHash string) signedTxPayloadClaimPlan {
	return signedTxPayloadClaimPlan{
		CacheVault: "cache-vault", CacheHash: "cache-hash", LogicalHash: logicalHash,
		OwnerVault: "owner-vault", MsgID: "msg-id", PlanHash: "plan-hash",
		ObserveKeys: []string{"observe-key"}, ObserveEpoch: map[string]uint64{"observe-key": 0},
		TxOutJSON: []byte(`{"standalone":true}`),
	}
}

func standalonePayload(txHash string) []byte {
	return []byte(`{"tx_hash":"` + txHash + `"}`)
}

func standaloneSignedClaim(t *testing.T, logicalHash, txHash string) (*signedTxPayloadStore, signedTxPayloadClaimPlan) {
	t.Helper()
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan(logicalHash)
	if err := store.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(plan, txHash, standalonePayload(txHash)); err != nil {
		t.Fatal(err)
	}
	return store, plan
}

func TestStandaloneReleaseReservedRequiresExactUnsignedPlan(t *testing.T) {
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan("logical-release")
	if err := store.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	different := plan
	different.MsgID = "other-msg"
	different.PlanHash = "other-plan"
	if err := store.ReleaseReserved(different); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("different plan release did not fail closed: %v", err)
	}
	if err := store.ReleaseReserved(plan); err != nil {
		t.Fatalf("release exact plan: %v", err)
	}
	if _, found, err := store.Claim(plan.LogicalHash); err != nil || found {
		t.Fatalf("released claim remains present: found=%t err=%v", found, err)
	}
	if err := store.Reserve(different); err != nil {
		t.Fatalf("released observation alias remains locked: %v", err)
	}
}

func TestStandaloneReleaseReservedNeverUnlocksSignedClaim(t *testing.T) {
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan("logical-signed-release")
	if err := store.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(plan, "signed-tx", standalonePayload("signed-tx")); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseReserved(plan); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("signed claim release did not fail closed: %v", err)
	}
	claim, found, err := store.Claim(plan.LogicalHash)
	if err != nil || !found || claim.State != signedTxClaimSigned {
		t.Fatalf("signed claim changed after release: found=%t state=%s err=%v", found, claim.State, err)
	}
}

func TestStandaloneVisibilityImportPreservesExactClaimAndRejectsConflicts(t *testing.T) {
	for _, exactAlias := range []bool{false, true} {
		name := "logical"
		if exactAlias {
			name = "exact-alias"
		}
		t.Run(name, func(t *testing.T) {
			store, plan := standaloneSignedClaim(t, "visibility-only-outbound", "signed-tx")
			importVerified := func(txHash string) error {
				if exactAlias {
					_, _, err := store.ImportVerifiedExact(plan.ObserveKeys[0], plan.OwnerVault, txHash)
					return err
				}
				_, _, err := store.ImportVerifiedLogical(plan.LogicalHash, plan.ObserveKeys, txHash)
				return err
			}
			if err := importVerified("signed-tx"); err != nil {
				t.Fatal(err)
			}
			if _, found, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); err != nil || !found {
				t.Fatalf("exact imported claim is not recoverable: found=%t err=%v", found, err)
			}
			if err := store.ReleaseReserved(plan); !errors.Is(err, errXMRSigningClaimConflict) {
				t.Fatalf("visibility import released signature-exposed ownership: %v", err)
			}
			// A competing verified result must retain the conservative guard.
			// Reimport of the old hash cannot silently erase that conflict.
			if err := importVerified("competing-tx"); err != nil {
				t.Fatal(err)
			}
			if err := importVerified("signed-tx"); err == nil {
				t.Fatal("exact reimport erased a competing verified fulfillment")
			}
			called := false
			_, err := store.WithBroadcastAuthorization(plan.CacheVault, plan.CacheHash, plan.LogicalHash,
				plan.OwnerVault, "signed-tx", plan.MsgID, plan.PlanHash,
				func() (string, error) { called = true; return "signed-tx", nil })
			if err == nil || called {
				t.Fatalf("competing import failed to block dispatch: called=%t err=%v", called, err)
			}
		})
	}
}

func TestStandaloneVisibilityImportBeforeSigningKeepsOwnershipClosed(t *testing.T) {
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan("visibility-before-signing")
	if _, _, err := store.ImportVerifiedLogical(plan.LogicalHash, plan.ObserveKeys, "peer-tx"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reserve(plan); err == nil {
		t.Fatal("verified peer fulfillment allowed a new independently signed payment")
	}
}

func TestStandaloneReleaseReservedHonorsObservationBarrier(t *testing.T) {
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan("logical-observed-release")
	if err := store.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	if _, committed, err := store.CommitUnfinalizedLogical(plan.LogicalHash, plan.ObserveKeys, "observed-tx"); err != nil || !committed {
		t.Fatalf("commit observation barrier: committed=%t err=%v", committed, err)
	}
	if err := store.ReleaseReserved(plan); !errors.Is(err, errXMRSigningClaimCommitted) {
		t.Fatalf("observation barrier did not block release: %v", err)
	}
}

func TestStandaloneDifferentAliasTxBlocksLocalThroughFinal(t *testing.T) {
	store := newStandaloneSignedStore(t)
	plan := standaloneClaimPlan("logical-alias-conflict")
	if err := store.Reserve(plan); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(plan, "local-b", standalonePayload("local-b")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CommitUnfinalizedExact("observe-key", "owner-vault", "peer-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimAliasCommitted) {
		t.Fatalf("peer alias did not block local recovery: %v", err)
	}
	called := false
	if _, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"local-b", plan.MsgID, plan.PlanHash,
		func() (string, error) { called = true; return "local-b", nil },
	); !errors.Is(err, errXMRSigningClaimAliasCommitted) || called {
		t.Fatalf("peer alias did not block broadcast: called=%t err=%v", called, err)
	}
	if _, _, err := store.CompleteObservedExact("observe-key", "owner-vault", "peer-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimAliasCommitted) {
		t.Fatalf("exact mismatch finality released local payload: %v", err)
	}
}

func TestStandaloneBroadcastDoesNotHoldStoreMutex(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-nonblocking-broadcast", "exact-tx")
	started := make(chan struct{})
	release := make(chan struct{})
	broadcastDone := make(chan error, 1)
	go func() {
		_, err := store.WithBroadcastAuthorization(
			plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
			"exact-tx", plan.MsgID, plan.PlanHash,
			func() (string, error) {
				close(started)
				<-release
				return "exact-tx", nil
			},
		)
		broadcastDone <- err
	}()
	<-started

	storeOpDone := make(chan error, 1)
	go func() {
		_, _, err := store.Claim(plan.LogicalHash)
		storeOpDone <- err
	}()
	select {
	case err := <-storeOpDone:
		if err != nil {
			t.Fatalf("unrelated store operation failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked broadcast held the global store mutex")
	}
	close(release)
	if err := <-broadcastDone; err != nil {
		t.Fatalf("finish broadcast: %v", err)
	}
}

func TestStandaloneBroadcastObservationRace(t *testing.T) {
	t.Run("same hash completes idempotently", func(t *testing.T) {
		store, plan := standaloneSignedClaim(t, "logical-same-observation", "exact-tx")
		started := make(chan struct{})
		release := make(chan struct{})
		done := make(chan struct {
			hash string
			err  error
		}, 1)
		go func() {
			hash, err := store.WithBroadcastAuthorization(
				plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
				"exact-tx", plan.MsgID, plan.PlanHash,
				func() (string, error) {
					close(started)
					<-release
					return "", errors.New("ambiguous HTTP timeout")
				},
			)
			done <- struct {
				hash string
				err  error
			}{hash, err}
		}()
		<-started
		if _, completed, err := store.CompleteObservedLogical(plan.LogicalHash, plan.ObserveKeys, "exact-tx"); err != nil || !completed {
			t.Fatalf("complete exact observation during broadcast: completed=%t err=%v", completed, err)
		}
		store.mu.Lock()
		activeToken := store.activeBroadcasts[plan.LogicalHash]
		store.mu.Unlock()
		if activeToken == "" {
			t.Fatal("observation cleared the live token before its durable batch and callback finished")
		}
		close(release)
		result := <-done
		if result.err != nil || result.hash != "exact-tx" {
			t.Fatalf("exact observation did not resolve ambiguous broadcast: hash=%s err=%v", result.hash, result.err)
		}
	})

	t.Run("different hash fails closed", func(t *testing.T) {
		store, plan := standaloneSignedClaim(t, "logical-different-observation", "exact-tx")
		started := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, err := store.WithBroadcastAuthorization(
				plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
				"exact-tx", plan.MsgID, plan.PlanHash,
				func() (string, error) {
					close(started)
					<-release
					return "exact-tx", nil
				},
			)
			done <- err
		}()
		<-started
		if _, _, err := store.CompleteObservedLogical(plan.LogicalHash, plan.ObserveKeys, "different-tx"); !errors.Is(err, errXMRSigningClaimConflict) {
			t.Fatalf("different observation did not fail closed: %v", err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("authorized exact broadcast failed: %v", err)
		}
	})
}

func TestStandaloneBroadcastAttemptRestartAndStaleToken(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-restart-broadcast", "exact-tx")
	started := make(chan struct{})
	release := make(chan struct{})
	staleDone := make(chan error, 1)
	go func() {
		_, err := store.WithBroadcastAuthorization(
			plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
			"exact-tx", plan.MsgID, plan.PlanHash,
			func() (string, error) {
				close(started)
				<-release
				return "exact-tx", nil
			},
		)
		staleDone <- err
	}()
	<-started

	restarted := newSignedTxPayloadStore(store.db)
	called := false
	if _, err := restarted.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", "different-msg", plan.PlanHash,
		func() (string, error) { called = true; return "exact-tx", nil },
	); !errors.Is(err, errXMRSigningClaimConflict) || called {
		t.Fatalf("restart accepted a different broadcast identity: called=%t err=%v", called, err)
	}
	if hash, err := restarted.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", plan.MsgID, plan.PlanHash,
		func() (string, error) { return "exact-tx", nil },
	); err != nil || hash != "exact-tx" {
		t.Fatalf("restart did not retry exact payload: hash=%s err=%v", hash, err)
	}
	close(release)
	if err := <-staleDone; !errors.Is(err, errXMRBroadcastAttemptStale) {
		t.Fatalf("superseded callback did not fail stale: %v", err)
	}
}

func TestStandaloneSuccessfulBroadcastAttemptSurvivesRestartAndRejectsDifferentObservation(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-success-restart-conflict", "exact-tx")
	if hash, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", plan.MsgID, plan.PlanHash,
		func() (string, error) { return "exact-tx", nil },
	); err != nil || hash != "exact-tx" {
		t.Fatalf("broadcast exact payload: hash=%s err=%v", hash, err)
	}

	restarted := newSignedTxPayloadStore(store.db)
	if _, _, err := restarted.CompleteObservedLogical(
		plan.LogicalHash, plan.ObserveKeys, "different-tx",
	); !errors.Is(err, errXMRSigningClaimConflict) {
		t.Fatalf("restart accepted a different transaction after successful broadcast: %v", err)
	}
}

func TestStandaloneSuccessfulBroadcastAttemptSurvivesRestartUntilExactObservation(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-success-restart-exact", "exact-tx")
	if hash, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", plan.MsgID, plan.PlanHash,
		func() (string, error) { return "exact-tx", nil },
	); err != nil || hash != "exact-tx" {
		t.Fatalf("broadcast exact payload: hash=%s err=%v", hash, err)
	}

	restarted := newSignedTxPayloadStore(store.db)
	restarted.mu.Lock()
	_, found, err := restarted.broadcastAttemptLocked(plan.LogicalHash)
	restarted.mu.Unlock()
	if err != nil || !found {
		t.Fatalf("successful broadcast authorization did not survive restart: found=%t err=%v", found, err)
	}
	if _, completed, completeErr := restarted.CompleteObservedLogical(
		plan.LogicalHash, plan.ObserveKeys, "exact-tx",
	); completeErr != nil || !completed {
		t.Fatalf("complete exact observation after restart: completed=%t err=%v", completed, completeErr)
	}
	restarted.mu.Lock()
	_, found, err = restarted.broadcastAttemptLocked(plan.LogicalHash)
	restarted.mu.Unlock()
	if err != nil || found {
		t.Fatalf("exact observation did not clear broadcast authorization: found=%t err=%v", found, err)
	}
}

func TestStandaloneDifferentReusableAliasDoesNotConsumePersistedBroadcastAttempt(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-reusable-alias-attempt", "exact-tx")
	broadcastErr := errors.New("ambiguous broadcast timeout")
	if _, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", plan.MsgID, plan.PlanHash,
		func() (string, error) { return "", broadcastErr },
	); !errors.Is(err, broadcastErr) {
		t.Fatalf("persist ambiguous exact broadcast attempt: %v", err)
	}

	if _, matched, err := store.CommitUnfinalizedExact("observe-key", "owner-vault", "peer-tx"); err != nil || matched {
		t.Fatalf("different reusable-alias commit wedged scanner: matched=%t err=%v", matched, err)
	}
	if _, matched, err := store.CompleteObservedExact("observe-key", "owner-vault", "peer-tx"); err != nil || matched {
		t.Fatalf("different reusable-alias finalization wedged scanner: matched=%t err=%v", matched, err)
	}

	store.mu.Lock()
	attempt, found, err := store.broadcastAttemptLocked(plan.LogicalHash)
	store.mu.Unlock()
	if err != nil || !found || attempt.TxHash != "exact-tx" {
		t.Fatalf("different alias transaction consumed exact broadcast attempt: found=%t attempt=%+v err=%v", found, attempt, err)
	}
	claim, found, err := store.Claim(plan.LogicalHash)
	if err != nil || !found || claim.State != signedTxClaimSigned || claim.ObservedTxHash != "" {
		t.Fatalf("different alias transaction terminalized exact claim: found=%t claim=%+v err=%v", found, claim, err)
	}
}

func TestStandaloneBroadcastTokenGenerationFailureUnlocksStore(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-token-error", "exact-tx")
	tokenErr := errors.New("entropy unavailable")
	store.newBroadcastToken = func() (string, error) { return "", tokenErr }
	called := false
	if _, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"exact-tx", plan.MsgID, plan.PlanHash,
		func() (string, error) { called = true; return "exact-tx", nil },
	); !errors.Is(err, tokenErr) || called {
		t.Fatalf("token generation failure dispatched callback: called=%t err=%v", called, err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := store.Claim(plan.LogicalHash)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("store unusable after token generation failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("token generation failure left the store mutex locked")
	}
}

func TestStandaloneRevertedClaimIsPermanentlyQuarantinedAcrossRestart(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-quarantine", "reverted-tx")
	if _, committed, err := store.CommitUnfinalizedLogical(
		plan.LogicalHash, plan.ObserveKeys, "reverted-tx",
	); err != nil || !committed {
		t.Fatalf("commit pre-errata observation: committed=%t err=%v", committed, err)
	}
	if err := store.QuarantineReverted("reverted-tx"); err != nil {
		t.Fatal(err)
	}

	// The bytes stay available only to the diagnostic accessor for incident
	// response. Every operational path remains terminally blocked.
	if _, found, err := store.Get(plan.CacheVault, plan.CacheHash); err != nil || !found {
		t.Fatalf("quarantine deleted forensic payload: found=%t err=%v", found, err)
	}
	if _, found, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimQuarantined) || found {
		t.Fatalf("owned recovery bypassed quarantine: found=%t err=%v", found, err)
	}
	if _, found, err := store.ActiveClaim(plan.ObserveKeys, plan.OwnerVault); !errors.Is(err, errXMRSigningClaimQuarantined) || found {
		t.Fatalf("active recovery bypassed quarantine: found=%t err=%v", found, err)
	}
	if err := store.Reserve(plan); !errors.Is(err, errXMRSigningClaimQuarantined) {
		t.Fatalf("new ceremony bypassed quarantine: %v", err)
	}
	if err := store.Set(plan, "reverted-tx", standalonePayload("reverted-tx")); !errors.Is(err, errXMRSigningClaimQuarantined) {
		t.Fatalf("signed result bypassed quarantine: %v", err)
	}
	called := false
	if _, err := store.WithBroadcastAuthorization(
		plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
		"reverted-tx", plan.MsgID, plan.PlanHash,
		func() (string, error) { called = true; return "reverted-tx", nil },
	); !errors.Is(err, errXMRSigningClaimQuarantined) || called {
		t.Fatalf("broadcast bypassed quarantine: called=%t err=%v", called, err)
	}
	if _, _, err := store.CompleteObservedLogical(
		plan.LogicalHash, plan.ObserveKeys, "reverted-tx",
	); !errors.Is(err, errXMRSigningClaimQuarantined) {
		t.Fatalf("delayed Observer callback bypassed quarantine: %v", err)
	}

	restarted := newSignedTxPayloadStore(store.db)
	if _, found, err := restarted.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimQuarantined) || found {
		t.Fatalf("restart lost quarantine: found=%t err=%v", found, err)
	}
	if local, err := restarted.IsLocallySignedTx("reverted-tx"); err != nil || !local {
		t.Fatalf("restart lost local signed provenance: local=%t err=%v", local, err)
	}
}

func TestStandaloneQuarantineWinsInFlightBroadcastRace(t *testing.T) {
	store, plan := standaloneSignedClaim(t, "logical-quarantine-race", "race-tx")
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.WithBroadcastAuthorization(
			plan.CacheVault, plan.CacheHash, plan.LogicalHash, plan.OwnerVault,
			"race-tx", plan.MsgID, plan.PlanHash,
			func() (string, error) {
				close(started)
				<-release
				return "race-tx", nil
			},
		)
		done <- err
	}()
	<-started
	if err := store.QuarantineReverted("race-tx"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, errXMRSigningClaimQuarantined) {
		t.Fatalf("in-flight callback reported authorization after quarantine: %v", err)
	}
	if _, found, err := store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash); !errors.Is(err, errXMRSigningClaimQuarantined) || found {
		t.Fatalf("in-flight race re-enabled payload: found=%t err=%v", found, err)
	}
}
