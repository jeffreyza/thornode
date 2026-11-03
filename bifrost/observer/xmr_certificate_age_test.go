package observer

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/config"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestPendingXMRCertificateAgeSurvivesLifecycle(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "known age"
		if legacy {
			name = "missing timestamp"
		}
		t.Run(name, func(t *testing.T) {
			store, path := setupTestDB(t)
			defer func() { cleanupTestDB(t, store, path) }()
			obs, _ := observerTestXMRPlan(t)
			obs.BlockHeight, obs.FinaliseHeight = 100, 137
			key := secp256k1.GenPrivKey()
			nonfinal := observerTestSignedHistoricalXMRCertificate(t, obs, key)
			started := time.Now()
			require.NoError(t, store.savePendingXMRCertificate(nonfinal))
			records, stats, err := store.pendingXMRCertificates(time.Now().Add(time.Hour), 16)
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.Equal(t, 1, stats.count)
			require.False(t, records[0].FirstEnqueuedAt.Before(started))
			require.False(t, records[0].FirstEnqueuedAt.After(time.Now()))

			// Model an aged record and the exact old JSON schema, without guessing an
			// enqueue time for existing local databases. Backoff must not hide occupancy.
			record := records[0]
			firstEnqueued := started.Add(-time.Hour)
			record.FirstEnqueuedAt = firstEnqueued
			record.NextAttempt = started.Add(24 * time.Hour)
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			if legacy {
				var old map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(raw, &old))
				delete(old, "first_enqueued_at")
				raw, err = json.Marshal(old)
				require.NoError(t, err)
				firstEnqueued = time.Time{}
			}
			require.NoError(t, store.db.Put([]byte(record.key), raw, nil))
			m := &metrics.Metrics{}
			o := &Observer{storage: store, m: m, lastNodeStatus: thortypes.NodeStatus_Standby}
			o.retryPendingXMRCertificates(context.Background()) // no due record, no RPC
			require.Equal(t, float64(1), testutil.ToFloat64(m.GetGauge(metrics.XMRPendingCertificates)))
			age := testutil.ToFloat64(m.GetGauge(metrics.XMROldestPendingCertificateAge))
			if legacy {
				require.Equal(t, float64(-1), age)
			} else {
				require.GreaterOrEqual(t, age, float64(3600))
			}
			records, stats, err = store.pendingXMRCertificates(time.Now(), 16)
			require.NoError(t, err)
			require.Empty(t, records)
			require.Equal(t, 1, stats.count)

			require.NoError(t, store.savePendingXMRCertificate(nonfinal))
			duplicate, err := store.db.Get([]byte(record.key), nil)
			require.NoError(t, err)
			require.Equal(t, raw, duplicate, "duplicate must preserve age and backoff")
			records, _, err = store.pendingXMRCertificates(started.Add(48*time.Hour), 16)
			require.NoError(t, err)
			oldRecord := records[0]
			obs.BlockHeight = obs.FinaliseHeight
			final := observerTestSignedHistoricalXMRCertificate(t, obs, key)
			require.NoError(t, store.savePendingXMRCertificate(final))
			require.NoError(t, store.finishPendingXMRCertificate(oldRecord, true, time.Now()))
			require.NoError(t, store.savePendingXMRCertificate(nonfinal))
			records, _, err = store.pendingXMRCertificates(started.Add(48*time.Hour), 16)
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.True(t, firstEnqueued.Equal(records[0].FirstEnqueuedAt))
			require.NoError(t, store.finishPendingXMRCertificate(records[0], false, started.Add(24*time.Hour)))
			require.NoError(t, store.Close())
			store, err = NewObserverStorage(path, config.LevelDBOptions{})
			require.NoError(t, err)
			records, stats, err = store.pendingXMRCertificates(started.Add(48*time.Hour), 16)
			require.NoError(t, err)
			require.Len(t, records, 1)
			require.True(t, firstEnqueued.Equal(records[0].FirstEnqueuedAt), "retry and restart preserve the original or unknown age")
			require.Equal(t, legacy, stats.unknownAge)
			want, err := final.Marshal()
			require.NoError(t, err)
			require.Equal(t, want, records[0].Payload)
			require.NoError(t, store.completePendingXMRCertificate(final))
			o.storage = store
			o.retryPendingXMRCertificates(context.Background())
			require.Zero(t, testutil.ToFloat64(m.GetGauge(metrics.XMRPendingCertificates)))
			require.Zero(t, testutil.ToFloat64(m.GetGauge(metrics.XMROldestPendingCertificateAge)))
		})
	}
}
