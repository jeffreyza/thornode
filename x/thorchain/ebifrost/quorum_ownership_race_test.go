package ebifrost

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cosmos/gogoproto/proto"
	"github.com/stretchr/testify/require"
	"gitlab.com/thorchain/thornode/v3/common"
)

// Each case uses the public Send and confirmation boundaries. In particular,
// passing a cached pointer to confirmation recreates the alias in the review
// fixture; decoded production inject transactions normally have another owner.
func TestQuorumCacheAndEventOwnership(t *testing.T) {
	att := createTestAttestation("pk1", "sig1")
	for _, tc := range quorumOwnershipCases(att) {
		t.Run(tc.name, func(t *testing.T) {
			ebs := tc.newServer()
			original := tc.message()
			want := mustCopyQuorum(original)
			require.NoError(t, tc.send(ebs, original))
			require.Len(t, tc.cached(ebs), 1)
			require.True(t, ownershipWireEqual(want, tc.cached(ebs)[0]))
			require.NotSame(t, original, tc.cached(ebs)[0])

			// Mutating both a nested attestation and the outer message after Send
			// must leave the cache's complete protobuf payload unchanged.
			tc.mutate(original)
			original.Reset()
			require.True(t, ownershipWireEqual(want, tc.cached(ebs)[0]))

			events := make(chan *EventNotification, 1)
			ebs.subscribers[tc.event] = []chan *EventNotification{events}
			confirmed := tc.cached(ebs)[0]
			tc.confirm(ebs, confirmed)
			require.Empty(t, tc.cached(ebs))
			// The event goroutine may still be queued. Its bytes must no longer
			// refer to the confirmation caller's object.
			confirmed.Reset()
			if tc.name != "price-feed" {
				require.True(t, ownershipWireEqual(want, recentOwnershipItem(ebs, tc.name)))
			}
			select {
			case event := <-events:
				require.Equal(t, tc.event, event.EventType)
				decoded := mustCopyQuorum(want)
				decoded.Reset()
				require.NoError(t, proto.Unmarshal(event.Payload, decoded))
				require.True(t, ownershipWireEqual(want, decoded), "event lost confirmed attestations")
			case <-time.After(5 * time.Second):
				t.Fatal("confirmation event not delivered")
			}
		})
	}
}

type quorumOwnershipCase struct {
	name, event string
	newServer   func() *EnshrinedBifrost
	message     func() proto.Message
	send        func(*EnshrinedBifrost, proto.Message) error
	cached      func(*EnshrinedBifrost) []proto.Message
	confirm     func(*EnshrinedBifrost, proto.Message)
	mutate      func(proto.Message)
}

func quorumOwnershipCases(att *common.Attestation) []quorumOwnershipCase {
	server := func() *EnshrinedBifrost { ebs, _ := newTestEBS(); return ebs }
	ctx := createTestSDKContext(100)
	return []quorumOwnershipCase{
		{
			name: "tx", event: EventQuorumTxCommitted, newServer: server,
			message: func() proto.Message {
				return createTestQuorumTx(common.BTCChain, "ownership-tx", true, []*common.Attestation{mustOwnershipType[*common.Attestation](proto.Clone(att))})
			},
			send: func(b *EnshrinedBifrost, m proto.Message) error {
				_, err := b.SendQuorumTx(context.Background(), mustOwnershipType[*common.QuorumTx](m))
				return err
			},
			cached: func(b *EnshrinedBifrost) []proto.Message { return asMessages(b.quorumTxCache.Get()) },
			confirm: func(b *EnshrinedBifrost, m proto.Message) {
				b.MarkQuorumTxAttestationsConfirmed(ctx, mustOwnershipType[*common.QuorumTx](m))
			},
			mutate: func(m proto.Message) { mustOwnershipType[*common.QuorumTx](m).Attestations[0].Signature[0] = 'x' },
		},
		{
			name: "network-fee", event: EventQuorumNetworkFeeCommitted, newServer: server,
			message: func() proto.Message {
				return createTestNetworkFee(common.BTCChain, 100, []*common.Attestation{mustOwnershipType[*common.Attestation](proto.Clone(att))})
			},
			send: func(b *EnshrinedBifrost, m proto.Message) error {
				_, err := b.SendQuorumNetworkFee(context.Background(), mustOwnershipType[*common.QuorumNetworkFee](m))
				return err
			},
			cached: func(b *EnshrinedBifrost) []proto.Message { return asMessages(b.networkFeeCache.Get()) },
			confirm: func(b *EnshrinedBifrost, m proto.Message) {
				b.MarkQuorumNetworkFeeAttestationsConfirmed(ctx, mustOwnershipType[*common.QuorumNetworkFee](m))
			},
			mutate: func(m proto.Message) {
				mustOwnershipType[*common.QuorumNetworkFee](m).Attestations[0].Signature[0] = 'x'
			},
		},
		{
			name: "solvency", event: EventQuorumSolvencyCommitted, newServer: server,
			message: func() proto.Message {
				return createTestSolvency(common.BTCChain, 100, []*common.Attestation{mustOwnershipType[*common.Attestation](proto.Clone(att))})
			},
			send: func(b *EnshrinedBifrost, m proto.Message) error {
				_, err := b.SendQuorumSolvency(context.Background(), mustOwnershipType[*common.QuorumSolvency](m))
				return err
			},
			cached: func(b *EnshrinedBifrost) []proto.Message { return asMessages(b.solvencyCache.Get()) },
			confirm: func(b *EnshrinedBifrost, m proto.Message) {
				b.MarkQuorumSolvencyAttestationsConfirmed(ctx, mustOwnershipType[*common.QuorumSolvency](m))
			},
			mutate: func(m proto.Message) { mustOwnershipType[*common.QuorumSolvency](m).Attestations[0].Signature[0] = 'x' },
		},
		{
			name: "errata", event: EventQuorumErrataTxCommitted, newServer: server,
			message: func() proto.Message {
				return createTestErrataTx(common.BTCChain, "ownership-errata", []*common.Attestation{mustOwnershipType[*common.Attestation](proto.Clone(att))})
			},
			send: func(b *EnshrinedBifrost, m proto.Message) error {
				_, err := b.SendQuorumErrataTx(context.Background(), mustOwnershipType[*common.QuorumErrataTx](m))
				return err
			},
			cached: func(b *EnshrinedBifrost) []proto.Message { return asMessages(b.errataCache.Get()) },
			confirm: func(b *EnshrinedBifrost, m proto.Message) {
				b.MarkQuorumErrataTxAttestationsConfirmed(ctx, mustOwnershipType[*common.QuorumErrataTx](m))
			},
			mutate: func(m proto.Message) { mustOwnershipType[*common.QuorumErrataTx](m).Attestations[0].Signature[0] = 'x' },
		},
		{
			name: "price-feed", event: EventQuorumPriceFeedBatchCommitted, newServer: server,
			message: func() proto.Message {
				return createTestPriceFeedBatch([]*common.QuorumPriceFeed{{Attestations: []*common.Attestation{mustOwnershipType[*common.Attestation](proto.Clone(att))}}})
			},
			send: func(b *EnshrinedBifrost, m proto.Message) error {
				_, err := b.SendQuorumPriceFeedBatch(context.Background(), mustOwnershipType[*common.QuorumPriceFeedBatch](m))
				return err
			},
			cached: func(b *EnshrinedBifrost) []proto.Message { return asMessages(b.priceFeedCache.Get()) },
			confirm: func(b *EnshrinedBifrost, m proto.Message) {
				b.MarkQuorumPriceFeedBatchAttestationsConfirmed(ctx, mustOwnershipType[*common.QuorumPriceFeedBatch](m))
			},
			mutate: func(m proto.Message) {
				mustOwnershipType[*common.QuorumPriceFeedBatch](m).QuorumPriceFeeds[0].Attestations[0].Signature[0] = 'x'
			},
		},
	}
}

func mustOwnershipType[T proto.Message](message proto.Message) T {
	typed, ok := message.(T)
	if !ok {
		panic("unexpected quorum fixture type")
	}
	return typed
}

func asMessages[T proto.Message](items []T) []proto.Message {
	result := make([]proto.Message, len(items))
	for i, item := range items {
		result[i] = item
	}
	return result
}

func TestQuorumRepeatedConfirmationsKeepEventAttestations(t *testing.T) {
	att := createTestAttestation("pk1", "sig1")
	for _, tc := range quorumOwnershipCases(att)[:4] {
		t.Run(tc.name, func(t *testing.T) {
			ebs := tc.newServer()
			first := tc.message()
			second := createTestAttestation("pk2", "sig2")
			full := mustCopyQuorum(first)
			setOwnershipAttestations(full, append(append([]*common.Attestation(nil), ownershipAttestations(full)...), second))
			require.NoError(t, tc.send(ebs, full))
			events := make(chan *EventNotification, 2)
			ebs.subscribers[tc.event] = []chan *EventNotification{events}

			for i, confirmed := range []proto.Message{first, mustCopyQuorum(first)} {
				if i == 1 {
					setOwnershipAttestations(confirmed, []*common.Attestation{second})
				}
				want := mustCopyQuorum(confirmed)
				tc.confirm(ebs, confirmed)
				if i == 0 {
					require.Len(t, tc.cached(ebs), 1)
					require.Len(t, ownershipAttestations(tc.cached(ebs)[0]), 1)
				} else {
					require.Empty(t, tc.cached(ebs))
				}
				select {
				case event := <-events:
					decoded := mustCopyQuorum(want)
					decoded.Reset()
					require.NoError(t, proto.Unmarshal(event.Payload, decoded))
					require.True(t, ownershipWireEqual(want, decoded))
				case <-time.After(5 * time.Second):
					t.Fatal("confirmation event not delivered")
				}
			}
		})
	}
}

func ownershipAttestations(m proto.Message) []*common.Attestation {
	switch x := m.(type) {
	case *common.QuorumTx:
		return x.Attestations
	case *common.QuorumNetworkFee:
		return x.Attestations
	case *common.QuorumSolvency:
		return x.Attestations
	case *common.QuorumErrataTx:
		return x.Attestations
	default:
		panic("unsupported quorum type")
	}
}

func setOwnershipAttestations(m proto.Message, atts []*common.Attestation) {
	switch x := m.(type) {
	case *common.QuorumTx:
		x.Attestations = atts
	case *common.QuorumNetworkFee:
		x.Attestations = atts
	case *common.QuorumSolvency:
		x.Attestations = atts
	case *common.QuorumErrataTx:
		x.Attestations = atts
	default:
		panic("unsupported quorum type")
	}
}

func mustCopyQuorum[T proto.Message](item T) T {
	bz, err := proto.Marshal(item)
	if err != nil {
		panic(err)
	}
	copy, ok := reflect.New(reflect.TypeOf(item).Elem()).Interface().(T)
	if !ok {
		panic("unexpected quorum copy type")
	}
	if err := proto.Unmarshal(bz, copy); err != nil {
		panic(err)
	}
	return copy
}

func recentOwnershipItem(b *EnshrinedBifrost, name string) proto.Message {
	switch name {
	case "tx":
		return b.quorumTxCache.recentBlockItems[100][0]
	case "network-fee":
		return b.networkFeeCache.recentBlockItems[100][0]
	case "solvency":
		return b.solvencyCache.recentBlockItems[100][0]
	case "errata":
		return b.errataCache.recentBlockItems[100][0]
	default:
		panic("unsupported quorum type")
	}
}

func ownershipWireEqual(a, b proto.Message) bool {
	ab, ae := proto.Marshal(a)
	bb, be := proto.Marshal(b)
	return ae == nil && be == nil && string(ab) == string(bb)
}
