package observer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type startupMembershipStream struct {
	*stateBoundsPipeStream
	readBytes atomic.Int64
	resets    atomic.Int32
}

func (s *startupMembershipStream) Read(p []byte) (int, error) {
	n, err := s.stateBoundsPipeStream.Read(p)
	s.readBytes.Add(int64(n))
	return n, err
}

func (s *startupMembershipStream) Reset() error {
	s.resets.Add(1)
	return s.stateBoundsPipeStream.Reset()
}

func TestStartupStateSyncServingMembershipRefresh(t *testing.T) {
	for _, membership := range []struct {
		name     string
		previous stypes.NodeStatus
		empty    bool
	}{
		{"restarted servers with empty membership", stypes.NodeStatus_Unknown, true},
		{"newly active requester missing from stale membership", stypes.NodeStatus_Ready, false},
	} {
		t.Run(membership.name, func(t *testing.T) {
			for _, limit := range []string{"recovery", "requests exhausted", "window exhausted"} {
				t.Run(limit, func(t *testing.T) {
					g, host, _, _, _, _ := setupTestGossip(t)
					g.logger = zerolog.Nop()
					t.Cleanup(g.stopStartupStateSync)
					requesterID := host.ID()
					peers := []peer.ID{requesterID}
					pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, g.privKey.PubKey())
					require.NoError(t, err)
					members := common.PubKeys{common.PubKey(pub)}
					servers := make(map[peer.ID]*Observer)
					bridges := make([]*MockThorchainBridge2, 0, 2)
					fee := &common.NetworkFee{Chain: common.BTCChain, Height: 150, TransactionSize: 100, TransactionRate: 2}
					payload, err := fee.GetSignablePayload()
					require.NoError(t, err)
					signature, err := g.privKey.Sign(payload)
					require.NoError(t, err)
					att := &common.Attestation{PubKey: g.pubKey, Signature: signature}
					for range 2 {
						sender, senderHost, _, _, _, _ := setupTestGossip(t)
						sender.logger = zerolog.Nop()
						senderPub, senderPubErr := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, sender.privKey.PubKey())
						require.NoError(t, senderPubErr)
						members = append(members, common.PubKey(senderPub))
						peers = append(peers, senderHost.ID())
						sender.networkFees[*fee] = &AttestationState[*common.NetworkFee]{
							Item: fee, attestations: []attestationSentState{{attestation: att}},
						}
						// A serving Bifrost may have retained evidence before its
						// first successful active-membership refresh after restart.
						sender.setActiveValidators(nil)
						bridge := &MockThorchainBridge2{nodeStatus: stypes.NodeStatus_Ready}
						bridges = append(bridges, bridge)
						servers[senderHost.ID()] = &Observer{
							logger: zerolog.Nop(), thorchainBridge: bridge,
							attestationGossip: sender, lock: &sync.Mutex{},
						}
					}
					if !membership.empty {
						for _, bridge := range bridges {
							bridge.activeNodes = members[1:]
						}
						for _, server := range servers {
							server.sendDeck(context.Background())
						}
					}
					host.peers = peers
					g.config.AskPeers = len(servers)
					bridge := &MockThorchainBridge2{nodeStatus: stypes.NodeStatus_Active, activeNodes: members}
					scanner := &churnRollbackClient{}
					var logs stateSyncLogBuffer
					o := &Observer{
						logger: zerolog.New(&logs), thorchainBridge: bridge,
						attestationGossip: g, lock: &sync.Mutex{}, lastNodeStatus: membership.previous,
						startedChains: map[common.Chain]chainclients.ChainClient{common.BTCChain: scanner},
					}
					type attempt struct {
						stream *startupMembershipStream
						done   chan struct{}
					}
					attempts := make(chan attempt, 4)
					host.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
						server, client := stateBoundsPipe(t, requesterID, id)
						stream := &startupMembershipStream{stateBoundsPipeStream: server}
						done := make(chan struct{})
						attempts <- attempt{stream, done}
						go func() {
							serveStateWithMembership(servers[id].attestationGossip, stream)
							close(done)
						}()
						return client, nil
					}
					joinGroup := func(rejected bool) {
						t.Helper()
						require.NotNil(t, g.startupState.group)
						waitStateBounds(t, g.startupState.group.done)
						require.Len(t, attempts, len(servers))
						for range servers {
							attempt := <-attempts
							waitStateBounds(t, attempt.done)
							if rejected {
								require.Zero(t, attempt.stream.readBytes.Load(), "membership must reject before reading the request")
								require.Positive(t, attempt.stream.resets.Load())
							} else {
								require.Positive(t, attempt.stream.readBytes.Load(), "refreshed membership must allow the real exchange")
							}
						}
						for _, server := range servers {
							require.Zero(t, stateSyncExportCount(server.attestationGossip))
							require.Zero(t, stateSyncExportCount(server.attestationGossip))
						}
					}

					o.sendDeck(context.Background())
					require.True(t, g.isActiveValidator(requesterID))
					collection := g.startupState
					require.NotNil(t, collection)
					require.Equal(t, startupStateSyncRequests, collection.requestLimit)
					for _, server := range servers {
						require.False(t, server.attestationGossip.isActiveValidator(requesterID))
					}
					joinGroup(true)
					switch limit {
					case "requests exhausted":
						collection.requestLimit = collection.requests
					case "window exhausted":
						g.tickAttestationStateSync(context.Background(), true, false, collection.deadline)
					}
					o.sendDeck(context.Background())
					require.Equal(t, attestationStateGroupResult{failed: 2}, collection.result)
					require.Equal(t, 2, collection.requests)
					require.Empty(t, g.networkFees)

					// Refresh through sendDeck, as production does. Virtual retry
					// ticks make no assumption about a cache TTL or refresh latency.
					for _, serverBridge := range bridges {
						serverBridge.activeNodes = members
					}
					for _, server := range servers {
						server.sendDeck(context.Background())
						require.True(t, server.attestationGossip.isActiveValidator(requesterID))
						inactive := newStateBoundsStream("inactive", prefixSendState)
						serveStateWithMembership(server.attestationGossip, inactive)
						require.Zero(t, inactive.readBytes.Load())
						require.EqualValues(t, 1, inactive.resets.Load(), "refresh must not admit genuinely inactive requesters")
					}
					if limit == "recovery" {
						require.False(t, collection.finished)
						g.tickAttestationStateSync(context.Background(), true, false, collection.next.Add(-time.Nanosecond))
						require.Nil(t, collection.group, "membership refresh does not bypass backoff")
						g.tickAttestationStateSync(context.Background(), true, false, collection.next)
						joinGroup(false)
						o.sendDeck(context.Background())
						require.Equal(t, attestationStateGroupResult{failed: 2, nonEmpty: 2}, collection.result)
						require.Equal(t, 4, collection.requests)
						require.Equal(t, 1, g.networkFees[*fee].AttestationCount(), "both snapshots import and deduplicate the retained evidence")
					} else {
						require.True(t, collection.finished)
						g.tickAttestationStateSync(context.Background(), true, false, collection.deadline.Add(time.Second))
						require.Empty(t, g.networkFees)
						require.Equal(t, 2, collection.requests, "a membership refresh must not re-arm exhausted budgets")
						require.Empty(t, attempts)
					}
					for range 3 {
						o.sendDeck(context.Background())
					}
					require.True(t, collection.finished)
					require.Same(t, collection, g.startupState)
					require.Nil(t, collection.group)
					require.Equal(t, stypes.NodeStatus_Active, o.lastNodeStatus)
					require.Equal(t, 1, strings.Count(logs.String(), "node is now active, will begin observation and gossip"))
					expectedRollbacks := 1
					if membership.previous == stypes.NodeStatus_Unknown {
						expectedRollbacks = 0
					}
					require.Equal(t, expectedRollbacks, scanner.rollbacks, "retries must not repeat churn rollback")
				})
			}
		})
	}
}

// Only the serving node's own status is stubbed. Active membership uses the
// real bridge's HTTP nodes response cache and FetchActiveNodes implementation.
type cachedStartupMembershipBridge struct {
	thorclient.ThorchainBridge
}

func (*cachedStartupMembershipBridge) FetchNodeStatus() (stypes.NodeStatus, error) {
	return stypes.NodeStatus_Ready, nil
}

func TestStartupStateSyncServingNodesResponseCacheRefresh(t *testing.T) {
	g, host, _, _, _, _ := setupTestGossip(t)
	g.logger = zerolog.Nop()
	t.Cleanup(g.stopStartupStateSync)
	requester := host.ID()
	pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, g.privKey.PubKey())
	require.NoError(t, err)
	response := func(status stypes.NodeStatus) []byte {
		t.Helper()
		data, marshalErr := json.Marshal([]*stypes.QueryNodeResponse{{
			Status: status.String(), PubKeySet: common.PubKeySet{Secp256k1: common.PubKey(pub)},
		}})
		require.NoError(t, marshalErr)
		return data
	}
	var reply atomic.Value
	reply.Store(response(stypes.NodeStatus_Ready))
	var requests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/thorchain/nodes" {
			http.NotFound(w, request)
			return
		}
		requests.Add(1)
		_, _ = w.Write(reply.Load().([]byte))
	}))
	defer endpoint.Close()
	bridge, err := thorclient.NewThorchainBridge(config.BifrostClientConfiguration{
		ChainID: "thorchain", ChainHost: strings.TrimPrefix(endpoint.URL, "http://"),
	}, getSharedTestMetrics(), nil)
	require.NoError(t, err)
	servingBridge := &cachedStartupMembershipBridge{ThorchainBridge: bridge}
	servers := make(map[peer.ID]*Observer)
	fee, atts := stateSyncSignedFee(t, 152, 1)
	stateSyncTrustAttestors(t, g, atts)
	for _, id := range []peer.ID{"one", "two"} {
		sender, _, _, _, _, _ := setupTestGossip(t)
		sender.logger = zerolog.Nop()
		sender.networkFees[*fee] = &AttestationState[*common.NetworkFee]{
			Item: fee, attestations: []attestationSentState{{attestation: atts[0]}},
		}
		servers[id] = &Observer{
			logger: zerolog.Nop(), thorchainBridge: servingBridge,
			attestationGossip: sender, lock: &sync.Mutex{},
		}
		g.activeVals[id] = true
	}
	host.peers = []peer.ID{requester, "one", "two"}
	g.config.AskPeers = 2
	exits := make(chan chan struct{}, 4)
	host.streamFunc = func(_ context.Context, id peer.ID, _ ...protocol.ID) (network.Stream, error) {
		server, client := stateBoundsPipe(t, requester, id)
		done := make(chan struct{})
		exits <- done
		go func() {
			serveStateWithMembership(servers[id].attestationGossip, server)
			close(done)
		}()
		return client, nil
	}
	refresh := func() {
		t.Helper()
		for _, server := range servers {
			server.sendDeck(context.Background())
		}
	}
	join := func() {
		t.Helper()
		require.NotNil(t, g.startupState.group)
		waitStateBounds(t, g.startupState.group.done)
		require.Len(t, exits, len(servers))
		for range servers {
			waitStateBounds(t, <-exits)
		}
	}
	// Prime the actual URL cache, then change the HTTP response. A status
	// refresh before cache expiry must still see the old membership.
	refresh()
	require.EqualValues(t, 1, requests.Load())
	reply.Store(response(stypes.NodeStatus_Active))
	refresh()
	require.EqualValues(t, 1, requests.Load(), "a cached nodes response must prevent a fresh HTTP query")
	for _, server := range servers {
		require.False(t, server.attestationGossip.isActiveValidator(requester))
	}
	now := time.Now()
	g.tickAttestationStateSync(context.Background(), true, true, now)
	join()
	settleStartupRetry(t, g, now)
	require.Equal(t, attestationStateGroupResult{failed: 2}, g.startupState.result)
	require.False(t, g.startupState.finished)
	require.EqualValues(t, 1, requests.Load(), "rejected streams do not bypass the membership cache")

	// Wait only for this fixture's successful cached response to expire.
	// This does not assume that real queries or deck work finish within a TTL.
	<-time.After(constants.ThorchainBlockTime + 20*time.Millisecond)
	refresh()
	require.EqualValues(t, 2, requests.Load())
	for _, server := range servers {
		require.True(t, server.attestationGossip.isActiveValidator(requester))
	}
	dispatch := g.startupState.next
	g.tickAttestationStateSync(context.Background(), true, false, dispatch)
	join()
	settleStartupRetry(t, g, dispatch)
	require.True(t, g.startupState.finished)
	require.Equal(t, attestationStateGroupResult{failed: 2, nonEmpty: 2}, g.startupState.result)
	require.Equal(t, 4, g.startupState.requests)
	require.Equal(t, 1, g.networkFees[*fee].AttestationCount())
}

// Model the membership gate of a later S14 serving peer. S14a/S15 deliberately
// retain their existing sender admission policy; retries treat either reset as
// a failed request without guessing its cause or fetching membership on demand.
func serveStateWithMembership(sender *AttestationGossip, stream network.Stream) {
	if !sender.isActiveValidator(stream.Conn().RemotePeer()) {
		_ = stream.Reset()
		return
	}
	sender.handleStreamAttestationState(stream)
}
