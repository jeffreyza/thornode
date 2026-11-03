package ebifrost

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestErrataInjectionRemainsRetryableUntilTerminal(t *testing.T) {
	for _, terminalOutcome := range []common.ErrataTxOutcome{common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION} {
		for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
			t.Run(chain.String()+"/"+terminalOutcome.String(), func(t *testing.T) {
				eb, _ := newTestEBS()
				events := make(chan *EventNotification, 4)
				eb.subscribers[EventQuorumErrataTxCommitted] = []chan *EventNotification{events}
				att := createTestAttestation("pk", "sig")
				makeQuorum := func() *common.QuorumErrataTx {
					return createTestErrataTx(chain, "errata-retry", []*common.Attestation{att})
				}
				_, err := eb.SendQuorumErrataTx(context.Background(), makeQuorum())
				require.NoError(t, err)
				ctx := context.WithValue(context.Background(), sdk.SdkContextKey, createTestSDKContext(200))
				pending := makeQuorum()
				eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, pending)
				require.Empty(t, eb.errataCache.Get(), "delivered items do not remain in every proposal")
				select {
				case event := <-events:
					var result common.QuorumErrataTx
					require.NoError(t, result.Unmarshal(event.Payload))
					require.Equal(t, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED, result.Outcome)
				case <-time.After(time.Second):
					t.Fatal("missing committed event")
				}
				// A durable retry must get through the real injection cache without
				// relying on some other errata expiring recentBlockItems.
				_, err = eb.SendQuorumErrataTx(context.Background(), makeQuorum())
				require.NoError(t, err)
				if chain.Equals(common.BTCChain) {
					require.Empty(t, eb.errataCache.Get(), "legacy deliveries remain deduplicated")
					return
				}
				require.Len(t, eb.errataCache.Get(), 1)
				require.Empty(t, eb.errataCache.recentBlockItems)
				terminal := makeQuorum()
				terminal.Outcome = terminalOutcome
				eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, terminal)
				select {
				case event := <-events:
					var result common.QuorumErrataTx
					require.NoError(t, result.Unmarshal(event.Payload))
					require.Equal(t, terminal.Outcome, result.Outcome)
				case <-time.After(time.Second):
					t.Fatal("missing terminal event")
				}
				require.Empty(t, eb.errataCache.Get())
				_, err = eb.SendQuorumErrataTx(context.Background(), makeQuorum())
				require.NoError(t, err)
				require.Empty(t, eb.errataCache.Get(), "terminal attestations are still deduplicated")
			})
		}
	}
}

func TestPendingErrataConfirmationConcurrentRetry(t *testing.T) {
	eb, _ := newTestEBS()
	ctx := context.WithValue(context.Background(), sdk.SdkContextKey, createTestSDKContext(200))
	makeQuorum := func() *common.QuorumErrataTx {
		return createTestErrataTx(common.XMRChain, "errata-concurrent", []*common.Attestation{createTestAttestation("pk", "sig")})
	}
	for i := 0; i < 20; i++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = eb.SendQuorumErrataTx(context.Background(), makeQuorum()) }()
		go func() { defer wg.Done(); eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, makeQuorum()) }()
		wg.Wait()
		// Either ordering must permit a later durable retry and retain at most
		// one merged item, without a recent-terminal suppression entry.
		_, err := eb.SendQuorumErrataTx(context.Background(), makeQuorum())
		require.NoError(t, err)
		items := eb.errataCache.Get()
		require.Len(t, items, 1)
		require.Len(t, items[0].Attestations, 1)
		require.Empty(t, eb.errataCache.recentBlockItems)
	}
}

// Bifrost can miss the one-shot event or fail its receipt fsync while THORNode
// remains alive. The existing terminal cache window must expire with chain
// progress even if no other errata is confirmed, so a retry can recover the result.
func TestErrataLostTerminalReceiptRetriesAfterCacheWindow(t *testing.T) {
	for _, outcome := range []common.ErrataTxOutcome{common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION} {
		for _, chain := range []common.Chain{common.XMRChain, common.BTCChain} {
			t.Run(chain.String()+"/"+outcome.String(), func(t *testing.T) {
				eb, _ := newTestEBS()
				makeQuorum := func() *common.QuorumErrataTx {
					return createTestErrataTx(chain, "errata-lost-receipt", []*common.Attestation{createTestAttestation("pk", "sig")})
				}
				terminal := makeQuorum()
				terminal.Outcome = outcome
				ctx := context.WithValue(context.Background(), sdk.SdkContextKey, createTestSDKContext(200))
				eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, terminal)
				// Confirmation just before a proposal must still deduplicate.
				eb.ProposalInjectTxs(createTestSDKContext(200+cachedBlocks), 1<<20)
				_, err := eb.SendQuorumErrataTx(context.Background(), makeQuorum())
				require.NoError(t, err)
				require.Empty(t, eb.errataCache.Get())
				// This node skips several windows of proposer turns. Without a
				// local proposal callback the receipt stays cached: elapsed chain
				// height alone does not run local cache maintenance.
				_, err = eb.SendQuorumErrataTx(context.Background(), makeQuorum())
				require.NoError(t, err)
				require.Empty(t, eb.errataCache.Get())
				// The next local proposal ages history; a subsequent retry recovers.
				eb.ProposalInjectTxs(createTestSDKContext(200+5*cachedBlocks), 1<<20)
				_, err = eb.SendQuorumErrataTx(context.Background(), makeQuorum())
				require.NoError(t, err)
				if chain.Equals(common.XMRChain) {
					require.Len(t, eb.errataCache.Get(), 1, "durable retry can recover a lost terminal receipt")
				} else {
					require.Empty(t, eb.errataCache.Get(), "non-XMR cache behavior is unchanged")
				}
			})
		}
	}
}

func TestErrataCommittedEventPreservesConfirmedSnapshot(t *testing.T) {
	// Keep the queued publisher off another processor until confirmation and
	// caller reuse finish. The event must describe the confirmed message.
	previousProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(previousProcs) })
	for _, tc := range []struct {
		name    string
		chain   common.Chain
		outcome common.ErrataTxOutcome
	}{
		{"pending", common.XMRChain, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED},
		{"reverted", common.XMRChain, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED},
		{"no-observation", common.XMRChain, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_NO_APPLICABLE_OBSERVATION},
		{"legacy", common.BTCChain, common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eb, _ := newTestEBS()
			events := make(chan *EventNotification, 1)
			eb.subscribers[EventQuorumErrataTxCommitted] = []chan *EventNotification{events}
			att := createTestAttestation("pk", "sig")
			message := createTestErrataTx(tc.chain, "confirmed-errata", []*common.Attestation{att})
			message.Outcome = tc.outcome
			expected, err := message.Marshal()
			require.NoError(t, err)
			_, err = eb.SendQuorumErrataTx(context.Background(), message)
			require.NoError(t, err)
			require.NotSame(t, message, eb.errataCache.Get()[0])

			// Publication may block on subscribers, but confirmation must not.
			eb.subscribersMu.Lock()
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(eb.subscribersMu.Unlock) }
			t.Cleanup(release)
			done := make(chan struct{})
			ctx := context.WithValue(context.Background(), sdk.SdkContextKey, createTestSDKContext(200))
			go func() {
				defer close(done)
				eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, message)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("errata confirmation waited for event delivery")
			}
			require.Empty(t, eb.errataCache.Get())

			// Cache cleanup must leave the independently owned receipt intact.
			// A later handler attempt can reuse and rewrite the caller's message.
			message.Outcome = common.ErrataTxOutcome_ERRATA_TX_OUTCOME_REVERTED
			message.ErrataTx.Id = "reused-errata"
			att.PubKey[0], att.Signature[0] = 'x', 'x'
			message.Attestations = []*common.Attestation{att}
			if tc.chain.RequiresTerminalErrataOutcome() && tc.outcome == common.ErrataTxOutcome_ERRATA_TX_OUTCOME_UNSPECIFIED {
				require.Empty(t, eb.errataCache.recentBlockItems)
			} else {
				require.Len(t, eb.errataCache.recentBlockItems[200], 1)
				receipt, marshalErr := eb.errataCache.recentBlockItems[200][0].Marshal()
				require.NoError(t, marshalErr)
				require.Equal(t, expected, receipt, "retry suppression must retain the confirmed identity and attestations")
			}
			release()
			select {
			case event := <-events:
				require.Equal(t, EventQuorumErrataTxCommitted, event.EventType)
				require.Equal(t, expected, event.Payload, "committed event must retain its confirmed outcome, identity and attestations")
			case <-time.After(time.Second):
				t.Fatal("missing committed errata event")
			}
		})
	}
}

func TestErrataCommittedEventPreservesSnapshotDuringCacheMerges(t *testing.T) {
	eb, _ := newTestEBS()
	events := make(chan *EventNotification, 1)
	eb.subscribers[EventQuorumErrataTxCommitted] = []chan *EventNotification{events}
	message := createTestErrataTx(common.XMRChain, "merging-errata", []*common.Attestation{createTestAttestation("pk-original", "sig")})
	_, err := eb.SendQuorumErrataTx(context.Background(), message)
	require.NoError(t, err)
	require.NotSame(t, message, eb.errataCache.Get()[0])
	expected, err := message.Marshal()
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), sdk.SdkContextKey, createTestSDKContext(200))
	for i := 0; i < 32; i++ {
		incoming := createTestErrataTx(common.XMRChain, "merging-errata", []*common.Attestation{createTestAttestation(fmt.Sprintf("pk-%d", i), "sig")})
		start := make(chan struct{})
		merged := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			eb.MarkQuorumErrataTxAttestationsConfirmed(ctx, message)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, mergeErr := eb.SendQuorumErrataTx(context.Background(), incoming)
			merged <- mergeErr
		}()
		close(start)
		wg.Wait()
		require.NoError(t, <-merged)
		select {
		case event := <-events:
			require.Equal(t, EventQuorumErrataTxCommitted, event.EventType)
			require.Equal(t, expected, event.Payload, "cache merges must not change the independently owned confirmation")
		case <-time.After(time.Second):
			t.Fatal("missing event during errata cache merge")
		}
	}
}
