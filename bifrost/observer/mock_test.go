package observer

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	ic "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/network"
	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-core/peerstore"
	"github.com/libp2p/go-libp2p-core/protocol"
	"github.com/multiformats/go-multiaddr"
	"google.golang.org/grpc"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Mock implementations of external dependencies
type MockHost struct {
	host.Host
	peers         []peer.ID
	streamHandler map[protocol.ID]network.StreamHandler
	streamFunc    func(context.Context, peer.ID, ...protocol.ID) (network.Stream, error)
}

func NewMockHost(peers []peer.ID) *MockHost {
	return &MockHost{
		peers:         peers,
		streamHandler: make(map[protocol.ID]network.StreamHandler),
		streamFunc: func(ctx context.Context, p peer.ID, pids ...protocol.ID) (network.Stream, error) {
			return &MockStream{mu: new(sync.Mutex)}, nil
		},
	}
}

func (m *MockHost) ID() peer.ID {
	return m.peers[0]
}

func (m *MockHost) NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (network.Stream, error) {
	return m.streamFunc(ctx, p, pids...)
}

func (m *MockHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	m.streamHandler[pid] = handler
}

func (m *MockHost) Peerstore() peerstore.Peerstore {
	return &MockPeerstore{peers: m.peers}
}

type MockPeerstore struct {
	peers []peer.ID
}

func (m *MockPeerstore) Peers() peer.IDSlice {
	return m.peers
}

func (m *MockPeerstore) PeerInfo(peer.ID) peer.AddrInfo {
	return peer.AddrInfo{}
}

func (m *MockPeerstore) AddAddr(p peer.ID, addr multiaddr.Multiaddr, ttl time.Duration) {
	// Mock implementation, do nothing
}

func (m *MockPeerstore) AddAddrs(id peer.ID, addrs []multiaddr.Multiaddr, ttl time.Duration) {
	// Mock implementation, do nothing
}

func (m *MockPeerstore) AddPrivKey(peer.ID, ic.PrivKey) error {
	// Mock implementation, do nothing
	return nil
}

func (m *MockPeerstore) AddProtocols(peer.ID, ...string) error {
	// Mock implementation, do nothing
	return nil
}

func (m *MockPeerstore) AddPubKey(peer.ID, ic.PubKey) error {
	// Mock implementation, do nothing
	return nil
}

func (m *MockPeerstore) AddrStream(context.Context, peer.ID) <-chan multiaddr.Multiaddr {
	return nil
}

func (m *MockPeerstore) Addrs(p peer.ID) []multiaddr.Multiaddr {
	return nil
}
func (m *MockPeerstore) ClearAddrs(p peer.ID) {}
func (m *MockPeerstore) Close() error {
	return nil
}

func (m *MockPeerstore) FirstSupportedProtocol(peer.ID, ...string) (string, error) {
	return "", nil
}

func (m *MockPeerstore) Get(p peer.ID, key string) (interface{}, error) {
	return nil, nil
}

func (m *MockPeerstore) GetProtocols(peer.ID) ([]string, error) {
	return nil, nil
}

func (m *MockPeerstore) LatencyEWMA(peer.ID) time.Duration {
	return 0
}

func (m *MockPeerstore) PeersWithAddrs() peer.IDSlice {
	return m.peers
}

func (m *MockPeerstore) PeersWithKeys() peer.IDSlice {
	return m.peers
}

func (m *MockPeerstore) PrivKey(peer.ID) ic.PrivKey {
	return nil
}

func (m *MockPeerstore) PubKey(peer.ID) ic.PubKey {
	return nil
}

func (m *MockPeerstore) Put(p peer.ID, key string, val interface{}) error {
	return nil
}

func (m *MockPeerstore) RecordLatency(peer.ID, time.Duration) {
	// Mock implementation, do nothing
}

func (m *MockPeerstore) RemoveProtocols(peer.ID, ...string) error {
	return nil
}

func (m *MockPeerstore) SetAddr(p peer.ID, addr multiaddr.Multiaddr, ttl time.Duration) {
	// Mock implementation, do nothing
}

func (m *MockPeerstore) SetAddrs(p peer.ID, addrs []multiaddr.Multiaddr, ttl time.Duration) {
	// Mock implementation, do nothing
}

func (m *MockPeerstore) SetProtocols(peer.ID, ...string) error {
	return nil
}

func (m *MockPeerstore) SupportsProtocols(peer.ID, ...string) ([]string, error) {
	return nil, nil
}

func (m *MockPeerstore) UpdateAddrs(p peer.ID, oldTTL, newTTL time.Duration) {
	// Mock implementation, do nothing
}

type MockKeys struct {
	thorclient.Keys
	privKey *secp256k1.PrivKey
}

func (m *MockKeys) GetPrivateKey() (*secp256k1.PrivKey, error) {
	return m.privKey, nil
}

type MockPrivKey struct {
	cryptotypes.PrivKey
	signFunc func(msg []byte) ([]byte, error)
}

func (m *MockPrivKey) Sign(msg []byte) ([]byte, error) {
	if m.signFunc != nil {
		return m.signFunc(msg)
	}
	return m.PrivKey.Sign(msg)
}

type MockGRPCClient struct {
	ebifrost.LocalhostBifrostClient
	sendQuorumTxFunc             func(ctx context.Context, quorumTx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error)
	sendQuorumNetworkFeeFunc     func(ctx context.Context, quorumNetworkFee *common.QuorumNetworkFee, opts ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error)
	sendQuorumSolvencyFunc       func(ctx context.Context, quorumSolvency *common.QuorumSolvency, opts ...grpc.CallOption) (*ebifrost.SendQuorumSolvencyResult, error)
	sendQuorumErrataFunc         func(ctx context.Context, quorumErrata *common.QuorumErrataTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumErrataTxResult, error)
	sendQuorumPriceFeedBatchFunc func(ctx context.Context, batch *common.QuorumPriceFeedBatch, opts ...grpc.CallOption) (*ebifrost.SendQuorumPriceFeedBatchResult, error)
}

func (m *MockGRPCClient) SendQuorumTx(ctx context.Context, tx *common.QuorumTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumTxResult, error) {
	return m.sendQuorumTxFunc(ctx, tx, opts...)
}

func (m *MockGRPCClient) SendQuorumNetworkFee(ctx context.Context, quorumNetworkFee *common.QuorumNetworkFee, opts ...grpc.CallOption) (*ebifrost.SendQuorumNetworkFeeResult, error) {
	return m.sendQuorumNetworkFeeFunc(ctx, quorumNetworkFee, opts...)
}

func (m *MockGRPCClient) SendQuorumSolvency(ctx context.Context, quorumSolvency *common.QuorumSolvency, opts ...grpc.CallOption) (*ebifrost.SendQuorumSolvencyResult, error) {
	return m.sendQuorumSolvencyFunc(ctx, quorumSolvency, opts...)
}

func (m *MockGRPCClient) SendQuorumErrataTx(ctx context.Context, quorumErrata *common.QuorumErrataTx, opts ...grpc.CallOption) (*ebifrost.SendQuorumErrataTxResult, error) {
	return m.sendQuorumErrataFunc(ctx, quorumErrata, opts...)
}

func (m *MockGRPCClient) SendQuorumPriceFeedBatch(ctx context.Context, batch *common.QuorumPriceFeedBatch, opts ...grpc.CallOption) (*ebifrost.SendQuorumPriceFeedBatchResult, error) {
	if m.sendQuorumPriceFeedBatchFunc != nil {
		return m.sendQuorumPriceFeedBatchFunc(ctx, batch, opts...)
	}
	return &ebifrost.SendQuorumPriceFeedBatchResult{}, nil
}

type MockThorchainBridge struct {
	thorclient.ThorchainBridge
	getKeysignPartyFunc         func(common.PubKey, int64) (common.PubKeys, error)
	getKeysignPolicyFunc        func(common.PubKey, int64) (thorclient.KeysignPolicy, error)
	getVaultFunc                func(string) (stypes.Vault, error)
	getKeysignFunc              func(int64, string) (tctypes.TxOut, error)
	getPendingOutboundsFunc     func(int64) ([]thorclient.PendingOutbound, error)
	getBlockHeightFunc          func() (int64, error)
	getBlockHeightFreshFunc     func() (int64, error)
	getObservedTxVoterFunc      func(string) (*stypes.QueryObservedTxVoter, error)
	getObservedTxVoterFreshFunc func(string) (*stypes.QueryObservedTxVoter, bool, error)
}

func (m *MockThorchainBridge) GetMimir(key string) (int64, error) {
	return 0, nil
}

// GetBlockHeight returns a stable height for observer tests that need a
// ThorchainBridge implementation but do not care about chain progress.
func (m *MockThorchainBridge) GetBlockHeight() (int64, error) {
	if m.getBlockHeightFunc != nil {
		return m.getBlockHeightFunc()
	}
	return 100, nil
}

func (m *MockThorchainBridge) GetBlockHeightUncached() (int64, error) {
	if m.getBlockHeightFreshFunc != nil {
		return m.getBlockHeightFreshFunc()
	}
	return m.GetBlockHeight()
}

// GetKeysignParty lets observer tests inject the legacy signer-only response.
func (m *MockThorchainBridge) GetKeysignParty(pubKey common.PubKey, blockHeight int64) (common.PubKeys, error) {
	if m.getKeysignPartyFunc != nil {
		return m.getKeysignPartyFunc(pubKey, blockHeight)
	}
	return nil, nil
}

// GetKeysignPolicy lets observer tests inject a full policy while preserving a
// compatibility fallback to the legacy signer-only hook.
func (m *MockThorchainBridge) GetKeysignPolicy(pubKey common.PubKey, blockHeight int64) (thorclient.KeysignPolicy, error) {
	if m.getKeysignPolicyFunc != nil {
		return m.getKeysignPolicyFunc(pubKey, blockHeight)
	}
	// Older observer tests only set getKeysignPartyFunc; synthesize the minimal
	// policy so those tests do not need to know about the new endpoint shape.
	signers, err := m.GetKeysignParty(pubKey, blockHeight)
	if err != nil {
		return thorclient.KeysignPolicy{}, err
	}
	return thorclient.KeysignPolicy{PubKey: pubKey, Signers: signers, ActiveSignerCount: len(signers)}, nil
}

// GetVault allows tests to opt into vault lookups without making every mock
// bridge setup construct a real thorchain bridge.
func (m *MockThorchainBridge) GetVault(pubkey string) (stypes.Vault, error) {
	if m.getVaultFunc != nil {
		return m.getVaultFunc(pubkey)
	}
	return stypes.Vault{}, nil
}

func (m *MockThorchainBridge) GetKeysign(blockHeight int64, pk string) (tctypes.TxOut, error) {
	if m.getKeysignFunc != nil {
		return m.getKeysignFunc(blockHeight, pk)
	}
	return tctypes.TxOut{}, nil
}

func (m *MockThorchainBridge) GetPendingOutbounds(height int64) ([]thorclient.PendingOutbound, error) {
	if m.getPendingOutboundsFunc != nil {
		return m.getPendingOutboundsFunc(height)
	}
	return nil, nil
}

func (m *MockThorchainBridge) GetObservedTxVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	if m.getObservedTxVoterFunc != nil {
		return m.getObservedTxVoterFunc(txID)
	}
	return nil, nil
}

func (m *MockThorchainBridge) GetObservedTxOutVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	return m.GetObservedTxVoter(txID)
}

func (m *MockThorchainBridge) GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return m.GetObservedTxVoterUncached(txID)
}

func (m *MockThorchainBridge) GetObservedTxVoterUncached(txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	if m.getObservedTxVoterFreshFunc != nil {
		return m.getObservedTxVoterFreshFunc(txID)
	}
	voter, err := m.GetObservedTxVoter(txID)
	return voter, voter != nil, err
}

type MockStream struct {
	network.Stream
	reader io.Reader
	writer io.Writer
	peer   peer.ID

	mu *sync.Mutex
	// remoteClosed marks a reader that already holds everything the remote
	// will send, so its EOF is final rather than "not written yet".
	remoteClosed bool
}

func NewMockStream(reader io.Reader, writer io.Writer, peer peer.ID) *MockStream {
	mu := &sync.Mutex{}
	return &MockStream{
		reader: reader,
		writer: writer,
		peer:   peer,
		mu:     mu,
	}
}

func (m *MockStream) Read(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Perform the read. A bytes.Buffer returns EOF whenever it is momentarily
	// empty, so poll for up to 5s: a loaded CI runner can starve the peer
	// goroutine past a few hundred ms and turn "not written yet" into a fake EOF.
	for range 50 {
		n, err = m.reader.Read(p)
		if err == nil {
			break
		}
		if err != io.EOF || m.remoteClosed {
			return n, err
		}
		m.mu.Unlock()
		<-time.After(100 * time.Millisecond)
		m.mu.Lock()
	}

	return n, err
}

func (m *MockStream) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writer.Write(p)
}

func (m *MockStream) Close() error {
	return nil
}

func (m *MockStream) Conn() network.Conn {
	return &MockConn{peer: m.peer}
}

func (m *MockStream) SetWriteDeadline(t time.Time) error {
	return nil
}

func (m *MockStream) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *MockStream) Reset() error {
	return nil
}

type MockConn struct {
	network.Conn
	peer peer.ID
}

func (m *MockConn) RemotePeer() peer.ID {
	return m.peer
}

type MockEventClient struct {
	handlers map[string]func(*ebifrost.EventNotification)
}

func (m *MockEventClient) RegisterHandler(event string, handler func(*ebifrost.EventNotification)) {
	if m.handlers == nil {
		m.handlers = make(map[string]func(*ebifrost.EventNotification))
	}
	m.handlers[event] = handler
}

func (m *MockEventClient) Start() {}
func (m *MockEventClient) Stop()  {}

func NewMockEventClient() *MockEventClient {
	return &MockEventClient{
		handlers: make(map[string]func(*ebifrost.EventNotification)),
	}
}

// MockThorchainBridge2 implements a broader set of ThorchainBridge methods for observer tests
type MockThorchainBridge2 struct {
	thorclient.ThorchainBridge
	nodeStatus                  stypes.NodeStatus
	activeNodes                 common.PubKeys
	getMimirFunc                func(key string) (int64, error)
	getKeysignPartyFunc         func(common.PubKey, int64) (common.PubKeys, error)
	getKeysignPolicyFunc        func(common.PubKey, int64) (thorclient.KeysignPolicy, error)
	getVaultFunc                func(string) (stypes.Vault, error)
	getKeysignFunc              func(int64, string) (tctypes.TxOut, error)
	getBlockHeightFreshFunc     func() (int64, error)
	getObservedTxVoterFunc      func(string) (*stypes.QueryObservedTxVoter, error)
	getObservedTxVoterFreshFunc func(string) (*stypes.QueryObservedTxVoter, bool, error)
}

func (m *MockThorchainBridge2) FetchNodeStatus() (stypes.NodeStatus, error) {
	return m.nodeStatus, nil
}

func (m *MockThorchainBridge2) FetchActiveNodes() ([]common.PubKey, error) {
	return m.activeNodes, nil
}

func (m *MockThorchainBridge2) GetMimir(key string) (int64, error) {
	if m.getMimirFunc != nil {
		return m.getMimirFunc(key)
	}
	return 0, nil
}

// GetBlockHeight returns a stable height for broader observer bridge tests.
func (m *MockThorchainBridge2) GetBlockHeight() (int64, error) {
	return 100, nil
}

func (m *MockThorchainBridge2) GetBlockHeightUncached() (int64, error) {
	if m.getBlockHeightFreshFunc != nil {
		return m.getBlockHeightFreshFunc()
	}
	return m.GetBlockHeight()
}

// GetKeysignParty lets tests inject the legacy signer-only bridge response.
func (m *MockThorchainBridge2) GetKeysignParty(pubKey common.PubKey, blockHeight int64) (common.PubKeys, error) {
	if m.getKeysignPartyFunc != nil {
		return m.getKeysignPartyFunc(pubKey, blockHeight)
	}
	return nil, nil
}

// GetKeysignPolicy lets tests inject a full policy while keeping the legacy
// signer-only hook as a compatibility fallback.
func (m *MockThorchainBridge2) GetKeysignPolicy(pubKey common.PubKey, blockHeight int64) (thorclient.KeysignPolicy, error) {
	if m.getKeysignPolicyFunc != nil {
		return m.getKeysignPolicyFunc(pubKey, blockHeight)
	}
	// This mirrors MockThorchainBridge so tests can migrate hook-by-hook instead
	// of updating every call site at once.
	signers, err := m.GetKeysignParty(pubKey, blockHeight)
	if err != nil {
		return thorclient.KeysignPolicy{}, err
	}
	return thorclient.KeysignPolicy{PubKey: pubKey, Signers: signers, ActiveSignerCount: len(signers)}, nil
}

// GetVault allows bridge2 tests to inject vault data only when the path under
// test needs it.
func (m *MockThorchainBridge2) GetVault(pubkey string) (stypes.Vault, error) {
	if m.getVaultFunc != nil {
		return m.getVaultFunc(pubkey)
	}
	return stypes.Vault{}, nil
}

func (m *MockThorchainBridge2) GetKeysign(blockHeight int64, pk string) (tctypes.TxOut, error) {
	if m.getKeysignFunc != nil {
		return m.getKeysignFunc(blockHeight, pk)
	}
	return tctypes.TxOut{}, nil
}

func (m *MockThorchainBridge2) GetObservedTxVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	if m.getObservedTxVoterFunc != nil {
		return m.getObservedTxVoterFunc(txID)
	}
	return nil, nil
}

func (m *MockThorchainBridge2) GetObservedTxOutVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	return m.GetObservedTxVoter(txID)
}

func (m *MockThorchainBridge2) GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	return m.GetObservedTxVoterUncached(txID)
}

func (m *MockThorchainBridge2) GetObservedTxVoterUncached(txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	if m.getObservedTxVoterFreshFunc != nil {
		return m.getObservedTxVoterFreshFunc(txID)
	}
	voter, err := m.GetObservedTxVoter(txID)
	return voter, voter != nil, err
}

// Define a custom pipe that implements the stream interface
type streamPair struct {
	clientToServer *bytes.Buffer
	serverToClient *bytes.Buffer
	peer           peer.ID
}
