package monero

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
)

func TestSignedTxPayloadStoreFinalAliasReplayPreservesSuccessor(t *testing.T) {
	for _, priorLocalClaim := range []bool{false, true} {
		for _, successorSigned := range []bool{false, true} {
			t.Run(fmt.Sprintf("prior-local=%t/successor-signed=%t", priorLocalClaim, successorSigned), func(t *testing.T) {
				store := newTestSignedTxPayloadStore(t)
				const alias, vault, oldHash, nextHash = "OBSERVE", "vault", "OLD-TX", "NEXT-TX"
				if priorLocalClaim {
					old := testSignedTxClaimPlan("cache-vault", "OLD-CACHE", "OLD-LOGICAL", vault, "old-msg", "old-plan", alias)
					require.NoError(t, store.Reserve(old))
					require.NoError(t, store.Set(old, oldHash, testStoredSignedTxPayload(t, oldHash, "old")))
				}
				_, _, err := store.CompleteObservedExact(alias, vault, oldHash)
				require.NoError(t, err)
				next := testSignedTxClaimPlan("cache-vault", "NEXT-CACHE", "NEXT-LOGICAL", vault, "next-msg", "next-plan", alias)
				next.ObserveEpoch, err = store.SnapshotObserveEpochs(next.ObserveKeys)
				require.NoError(t, err)
				require.NoError(t, store.Reserve(next))
				payload := testStoredSignedTxPayload(t, nextHash, "next")
				if successorSigned {
					require.NoError(t, store.Set(next, nextHash, payload))
				}

				_, completed, err := store.CompleteObservedExact(alias, vault, oldHash)
				require.NoError(t, err)
				require.False(t, completed, "old completion cannot complete its successor")
				if !successorSigned {
					require.NoError(t, store.Reserve(next), "old completion must not reacquire the reusable alias")
					require.NoError(t, store.Set(next, nextHash, payload))
				}
				got, found, err := store.GetOwned(next.CacheVault, next.CacheHash, next.LogicalHash)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, payload, got)

				// A genuinely new, conflicting result must still quarantine the
				// successor; replaying an older final result must not remove it.
				_, completed, err = store.CompleteObservedExact(alias, vault, "DIFFERENT-TX")
				require.NoError(t, err)
				require.False(t, completed)
				_, _, err = store.GetOwned(next.CacheVault, next.CacheHash, next.LogicalHash)
				require.ErrorIs(t, err, errXMRSigningClaimAliasCommitted)
				_, _, err = store.CompleteObservedExact(alias, vault, oldHash)
				require.NoError(t, err)
				_, _, err = store.GetOwned(next.CacheVault, next.CacheHash, next.LogicalHash)
				require.ErrorIs(t, err, errXMRSigningClaimAliasCommitted)
			})
		}
	}
}

func TestSignedTxPayloadStoreFinalAliasReplayCompletesPendingSameHash(t *testing.T) {
	store := newTestSignedTxPayloadStore(t)
	// Another alias may already index the final transaction while this alias
	// still has an unfinalized barrier. Its first final transition must run.
	_, _, err := store.CommitUnfinalizedExact("PENDING-ALIAS", "vault", "TX")
	require.NoError(t, err)
	_, _, err = store.CompleteObservedExact("OTHER-ALIAS", "vault", "TX")
	require.NoError(t, err)
	_, _, err = store.CompleteObservedExact("PENDING-ALIAS", "vault", "TX")
	require.NoError(t, err)
	plan := testSignedTxClaimPlan("cache-vault", "CACHE", "LOGICAL", "vault", "msg", "plan", "PENDING-ALIAS")
	plan.ObserveEpoch, err = store.SnapshotObserveEpochs(plan.ObserveKeys)
	require.NoError(t, err)
	require.NoError(t, store.Reserve(plan))
}

func TestSignedTxPayloadStoreFinalAliasReplayStillCompletesMatchingClaim(t *testing.T) {
	store := newTestSignedTxPayloadStore(t)
	plan := testSignedTxClaimPlan("cache-vault", "CACHE", "LOGICAL", "vault", "msg", "plan", "MATCHING-ALIAS")
	require.NoError(t, store.Reserve(plan))
	require.NoError(t, store.Set(plan, "TX", testStoredSignedTxPayload(t, "TX", "payload")))
	_, _, err := store.CompleteObservedExact("OTHER-ALIAS", "vault", "TX")
	require.NoError(t, err)
	// An existing alias event plus the global final index does not prove that
	// this matching local claim has itself completed. It must still finalize,
	// even without a committed-alias barrier.
	batch := new(leveldb.Batch)
	_, err = store.recordObservationLocked(batch, "MATCHING-ALIAS", "TX")
	require.NoError(t, err)
	require.NoError(t, store.db.Write(batch, signedTxPayloadWriteOptions))
	_, completed, err := store.CompleteObservedExact("MATCHING-ALIAS", "vault", "TX")
	require.NoError(t, err)
	require.True(t, completed)
	_, _, err = store.GetOwned(plan.CacheVault, plan.CacheHash, plan.LogicalHash)
	require.ErrorIs(t, err, errXMRSigningClaimObserved)
}
