package solana

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mr-tron/base58"
	"github.com/stretchr/testify/require"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
)

// countingSOLRPC is a param-aware SOL RPC mock for the consensus-blockhash cache
// tests. It serves the same scenario shape as solRPCScenario but also counts calls
// per method, can be flipped into an all-errors mode, and can hold getSlot on a
// gate so a test can prove concurrent lookups collapse into ONE derivation.
type countingSOLRPC struct {
	mu       sync.Mutex
	scenario solRPCScenario
	calls    map[string]int

	failAll atomic.Bool

	// failBroadcastBlockhash makes sendTransaction answer with the preflight
	// "Blockhash not found" rejection that gates BroadcastTx's expiry check.
	failBroadcastBlockhash atomic.Bool

	// gateGetSlot, when non-nil, blocks every getSlot until closed; arrivals
	// counts the callers that reached the gate.
	gateGetSlot chan struct{}
	arrivals    atomic.Int32

	// lastGetBlockConfig is the config object of the most recent getBlock request,
	// so a test can assert what the sign path asks the RPC to send back.
	lastGetBlockConfig map[string]interface{}
}

func (m *countingSOLRPC) getBlockConfig() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastGetBlockConfig
}

func newCountingSOLRPC(t *testing.T, sc solRPCScenario) (*countingSOLRPC, *rpc.SolRPC) {
	m, server := newCountingSOLRPCServer(sc)
	t.Cleanup(server.Close)
	return m, newSolRPCForServer(server)
}

// newCountingSOLRPCServer exposes the mock's httptest server so a test can hand the
// SAME view to a full SOLClient built by newSolCacheTestClient (which takes a server,
// not an RPC client) and drive SignTx/BroadcastTx against it. The caller owns Close.
func newCountingSOLRPCServer(sc solRPCScenario) (*countingSOLRPC, *httptest.Server) {
	m := &countingSOLRPC{scenario: sc, calls: map[string]int{}}
	return m, httptest.NewServer(http.HandlerFunc(m.serve))
}

func (m *countingSOLRPC) count(method string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[method]
}

func (m *countingSOLRPC) setScenario(sc solRPCScenario) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scenario = sc
}

func (m *countingSOLRPC) serve(rw http.ResponseWriter, req *http.Request) {
	var rpcReq struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(req.Body).Decode(&rpcReq); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.calls[rpcReq.Method]++
	sc := m.scenario
	m.mu.Unlock()

	if m.failAll.Load() {
		http.Error(rw, "rpc down", http.StatusServiceUnavailable)
		return
	}
	write := func(result string) {
		_, _ = fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":1,"result":%s}`, result)
	}
	var p []json.RawMessage
	_ = json.Unmarshal(rpcReq.Params, &p)
	slotArg := func(i int) uint64 {
		var s uint64
		if len(p) > i {
			_ = json.Unmarshal(p[i], &s)
		}
		return s
	}
	switch rpcReq.Method {
	case "getSlot":
		if m.gateGetSlot != nil {
			m.arrivals.Add(1)
			<-m.gateGetSlot
		}
		write(fmt.Sprintf("%d", sc.tip))
	case "getFirstAvailableBlock":
		if sc.noHistoryFloor {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
			break
		}
		write(fmt.Sprintf("%d", sc.floorSlot))
	case "getBlocks":
		b, _ := json.Marshal(sc.presentSlots(slotArg(0), slotArg(1)))
		write(string(b))
	case "getBlockTime":
		// Match solRPCScenario.server's null response so this counting mock can
		// verify how the search handles unavailable newest and interior times.
		blockTime, ok := sc.blockTimeAt(slotArg(0))
		if !ok {
			write("null")
			break
		}
		write(fmt.Sprintf("%d", blockTime))
	case "getBlock":
		slot := slotArg(0)
		var cfg map[string]interface{}
		if len(p) > 1 {
			_ = json.Unmarshal(p[1], &cfg)
		}
		m.mu.Lock()
		m.lastGetBlockConfig = cfg
		m.mu.Unlock()
		write(fmt.Sprintf(`{"blockhash":%q,"previousBlockhash":"","parentSlot":%d}`, mockBlockhashForSlot(slot), slot-1))
	case "isBlockhashValid":
		result, valid := sc.blockhashValidResponse()
		if !valid {
			_, _ = rw.Write([]byte(result))
			break
		}
		write(result)
	case "sendTransaction":
		if m.failBroadcastBlockhash.Load() {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Transaction simulation failed: Blockhash not found"}}`))
			break
		}
		write(`"broadcast-sig"`)
	case "getLatestBlockhash":
		write(fmt.Sprintf(`{"context":{"slot":%d},"value":{"blockhash":%q,"lastValidBlockHeight":%d}}`, sc.tip, mockBlockhashForSlot(sc.tip), sc.tip))
	default:
		write("null")
	}
}

// happyConsensusScenario is a node finalized well past target: S* == slot 950.
func happyConsensusScenario() solRPCScenario {
	return solRPCScenario{
		tip:       1000,
		floorSlot: 1,
		alignSlot: 950,
		target:    testConsensusBlockTimeUnix - solFinalizationMarginSeconds,
	}
}

func newConsensusCacheClient(t *testing.T, sc solRPCScenario) (*SOLClient, *countingSOLRPC) {
	m, rpcClient := newCountingSOLRPC(t, sc)
	return &SOLClient{bridge: &mockSOLThorchainBridge{}, rpcClient: rpcClient}, m
}

func tiedConsensusBlockTimes(target, alignSlot int64) func(uint64) (int64, bool) {
	return func(slot uint64) (int64, bool) {
		delta := int64(slot) - alignSlot
		seconds := delta / 3
		if delta < 0 && delta%3 != 0 {
			seconds-- // floor division for slots before alignSlot
		}
		return target + seconds, true
	}
}

// TestConsensusBlockHashAcrossRPCViews protects the signer determinism invariant:
// RPC nodes can expose different finalized tips and history windows, but every view
// that brackets the consensus target must choose the same rightmost slot and hash.
// Views that cannot prove that bracket must abstain or return an error, never guess.
func TestConsensusBlockHashAcrossRPCViews(t *testing.T) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	tiedAt950 := tiedConsensusBlockTimes(target, 950)
	tiedAt100 := tiedConsensusBlockTimes(target, 100)

	tests := []struct {
		name      string
		scenario  solRPCScenario
		wantSlot  int64 // zero means the node should abstain
		wantErr   bool
		wantErrIs error // when set, the specific sentinel the caller branches on
	}{
		// A node finalized far past the target must still select the global
		// rightmost slot at the target, not a slot relative to its local tip.
		{
			name:     "far-ahead node",
			scenario: solRPCScenario{tip: 1000, floorSlot: 1, target: target, blockTime: tiedAt950},
			wantSlot: 952,
		},
		// The smallest valid upper witness, one slot past the selected timestamp
		// group, must produce the same result as the far-ahead node.
		{
			name:     "tip at successor of selected slot",
			scenario: solRPCScenario{tip: 953, floorSlot: 1, target: target, blockTime: tiedAt950},
			wantSlot: 952,
		},
		// Pruned history is sufficient when its floor still supplies a lower
		// witness at or before the target.
		{
			name:     "snapshot history starts shortly before target",
			scenario: solRPCScenario{tip: 5000, floorSlot: 940, target: target, blockTime: tiedAt950},
			wantSlot: 952,
		},
		// A newest timestamp equal to the target is not a strict upper witness,
		// so the node must abstain instead of choosing from an incomplete view.
		{
			name:     "not finalized past target",
			scenario: solRPCScenario{tip: 952, floorSlot: 1, target: target, blockTime: tiedAt950},
		},
		// When the target is outside the initial window, the widening search must
		// still find the rightmost slot sharing the target timestamp.
		{
			name:     "gallops beyond initial lookback",
			scenario: solRPCScenario{tip: 1000, floorSlot: 0, target: target, blockTime: tiedAt100},
			wantSlot: 102,
		},
		// A snapshot whose retained history begins after the target cannot provide a
		// lower witness. The 1001 finalized blocks it DOES serve all postdate the
		// target, which is more than MAX_PROCESSING_AGE, so the anchor is provably
		// dead and the caller is told to reschedule rather than keep retrying.
		{
			name:      "snapshot history starts after target",
			scenario:  solRPCScenario{tip: 10000, floorSlot: 9000, target: target, blockTime: tiedAt950},
			wantErr:   true,
			wantErrIs: errAnchorExpired,
		},
		// Same verdict when the search runs out of lookback rather than history: the
		// capped window still proves thousands of blocks postdate the target.
		{
			name: "target is beyond maximum lookback",
			scenario: solRPCScenario{
				tip:       10000,
				floorSlot: 1,
				target:    target,
				blockTime: tiedConsensusBlockTimes(target, 1000),
			},
			wantErr:   true,
			wantErrIs: errAnchorExpired,
		},
		// ... but only when the block count proves it. A cluster producing blocks
		// slowly can leave the target unreachable with FEWER than MAX_PROCESSING_AGE
		// blocks above it, and then expiry is unproven: abstain and retry, never drop.
		{
			name:     "unreachable target with too few blocks to prove expiry",
			scenario: solRPCScenario{tip: 10000, floorSlot: 9950, target: target, blockTime: tiedAt950},
		},
		// A null timestamp at the newest slot removes the upper witness, so the
		// node must abstain until its RPC can provide a usable finalized view.
		{
			name: "null newest block time abstains",
			scenario: solRPCScenario{
				tip:       1000,
				floorSlot: 1,
				target:    target,
				blockTime: func(slot uint64) (int64, bool) {
					if slot == 1000 {
						return 0, false
					}
					return tiedAt950(slot)
				},
			},
		},
		// A null timestamp inside an otherwise bracketed finalized range prevents
		// deterministic ordering, so the search must return an error for retry.
		{
			name: "null interior block time errors",
			scenario: solRPCScenario{
				tip:       1000,
				floorSlot: 1,
				target:    target,
				blockTime: func(slot uint64) (int64, bool) {
					if slot == 800 { // first midpoint of [600, 1000]
						return 0, false
					}
					return tiedAt950(slot)
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newConsensusCacheClient(t, tt.scenario)
			hash, slot, err := client.consensusBlockHash(100)

			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrIs != nil {
					require.ErrorIs(t, err, tt.wantErrIs)
				}
				require.Empty(t, hash)
				return
			}

			require.NoError(t, err)
			if tt.wantSlot == 0 {
				require.Empty(t, hash)
				require.Zero(t, slot)
				return
			}

			require.Equal(t, tt.wantSlot, slot)
			require.Equal(t, mockBlockhashForSlot(uint64(tt.wantSlot)), hash)
		})
	}
}

func waitUntil(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// A successful height -> (blockhash, slot) derivation is immutable (CometBFT
// timestamp + finalized SOL slots never change), so a second lookup for the same
// height must be served from memory with ZERO RPC traffic.
func TestCachedConsensusBlockHashDerivesOnceForAHeight(t *testing.T) {
	client, m := newConsensusCacheClient(t, happyConsensusScenario())

	hash, slot, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.Equal(t, int64(950), slot)
	firstSlots, firstTimes, firstBlocks := m.count("getSlot"), m.count("getBlockTime"), m.count("getBlock")
	require.Equal(t, 1, firstSlots)
	require.Equal(t, 1, firstBlocks)
	require.Greater(t, firstTimes, 0)

	hash2, slot2, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, hash, hash2)
	require.Equal(t, slot, slot2)
	require.Equal(t, firstSlots, m.count("getSlot"), "cache hit must not call getSlot")
	require.Equal(t, firstTimes, m.count("getBlockTime"), "cache hit must not probe getBlockTime")
	require.Equal(t, firstBlocks, m.count("getBlock"), "cache hit must not fetch getBlock")

	// A different height is a different key and derives afresh.
	_, _, err = client.cachedConsensusBlockHash(101)
	require.NoError(t, err)
	require.Equal(t, 2, m.count("getSlot"))
}

// The derivation reads exactly one field of the chosen block. On mainnet a full
// getBlock is megabytes of transaction JSON, spent inside the ~60s blockhash
// liveness budget and then discarded, so the sign path must request only the
// blockhash: no transaction list, no rewards, finalized commitment.
func TestConsensusBlockHashFetchesBlockhashWithoutTransactions(t *testing.T) {
	client, m := newConsensusCacheClient(t, happyConsensusScenario())

	hash, _, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.Equal(t, 1, m.count("getBlock"))

	cfg := m.getBlockConfig()
	require.NotNil(t, cfg)
	require.Equal(t, "none", cfg["transactionDetails"])
	require.Equal(t, false, cfg["rewards"])
	require.Equal(t, "finalized", cfg["commitment"])
}

// The empty-hash abstention ("not finalized past target yet") is node-local and
// time-varying: it must never be remembered, or a transient "wait" becomes a
// permanent skip for that height.
func TestCachedConsensusBlockHashDoesNotCacheAbstention(t *testing.T) {
	behind := happyConsensusScenario()
	behind.tip = 940 // newest finalized blockTime <= target -> abstain
	client, m := newConsensusCacheClient(t, behind)

	hash, _, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, "", hash)
	require.Equal(t, 1, m.count("getSlot"))

	// The node catches up: the SAME height must now derive and return the hash.
	m.setScenario(happyConsensusScenario())
	hash, slot, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.Equal(t, int64(950), slot)
	require.Equal(t, 2, m.count("getSlot"), "abstention must not be served from cache")
}

// RPC errors are transient and must not poison the cache.
func TestCachedConsensusBlockHashDoesNotCacheErrors(t *testing.T) {
	client, m := newConsensusCacheClient(t, happyConsensusScenario())
	m.failAll.Store(true)

	_, _, err := client.cachedConsensusBlockHash(100)
	require.Error(t, err)

	m.failAll.Store(false)
	hash, _, err := client.cachedConsensusBlockHash(100)
	require.NoError(t, err)
	require.Equal(t, mockBlockhashForSlot(950), hash)
	require.Equal(t, 2, m.count("getSlot"), "error must not be served from cache")
}

// Concurrent callers for the same height (a second vault's outbound, or the
// keep-alive goroutine) must share ONE in-flight derivation rather than each
// walking the RPC search. getSlot is gated so every caller has time to arrive:
// without coalescing all of them block inside getSlot; with it only the leader.
func TestCachedConsensusBlockHashCoalescesConcurrentLookups(t *testing.T) {
	client, m := newConsensusCacheClient(t, happyConsensusScenario())
	m.gateGetSlot = make(chan struct{})

	const callers = 8
	results := make(chan string, callers)
	for i := 0; i < callers; i++ {
		go func() {
			hash, _, err := client.cachedConsensusBlockHash(100)
			if err != nil {
				results <- "err: " + err.Error()
				return
			}
			results <- hash
		}()
	}
	// Either every caller reached the RPC (no coalescing) or only the leader did
	// and the rest are parked on it; either way, release after a grace period.
	_ = waitUntil(func() bool { return m.arrivals.Load() >= callers }, 300*time.Millisecond)
	close(m.gateGetSlot)

	for i := 0; i < callers; i++ {
		require.Equal(t, mockBlockhashForSlot(950), <-results)
	}
	require.Equal(t, int32(1), m.arrivals.Load(), "only one caller may reach the RPC")
	require.Equal(t, 1, m.count("getSlot"))
	require.Equal(t, 1, m.count("getBlock"))
}

// The cache must be bounded: heights only ever move forward, so entries older
// than the retention window behind the newest stored height are dropped.
func TestConsensusBlockHashCacheIsBounded(t *testing.T) {
	client := &SOLClient{}
	const extra = int64(50)
	newest := solConsensusCacheWindow + extra
	for h := int64(1); h <= newest; h++ {
		client.storeConsensusBlockHash(h, consensusBlockHashEntry{hash: mockBlockhashForSlot(uint64(h)), slot: h})
	}

	client.consensusHashMu.Lock()
	size := len(client.consensusHashes)
	client.consensusHashMu.Unlock()
	require.LessOrEqual(t, size, int(solConsensusCacheWindow)+1)

	_, ok := client.lookupConsensusBlockHash(1)
	require.False(t, ok, "oldest height must be evicted")
	e, ok := client.lookupConsensusBlockHash(newest)
	require.True(t, ok, "newest height must be retained")
	require.Equal(t, mockBlockhashForSlot(uint64(newest)), e.hash)
	require.Equal(t, newest, e.slot)
}

// signedTxRecentBlockhash parses the legacy Solana wire format just far enough to
// read the message's recent blockhash: varlen sig count, 64-byte sigs, 3 header
// bytes, varlen account count, 32-byte accounts, then the 32-byte blockhash.
func signedTxRecentBlockhash(c *C, raw []byte) string {
	r := bytes.NewReader(raw)
	nSigs, err := binary.ReadUvarint(r)
	c.Assert(err, IsNil)
	_, err = r.Seek(int64(nSigs)*64+3, io.SeekCurrent)
	c.Assert(err, IsNil)
	nAccounts, err := binary.ReadUvarint(r)
	c.Assert(err, IsNil)
	_, err = r.Seek(int64(nAccounts)*32, io.SeekCurrent)
	c.Assert(err, IsNil)
	hash := make([]byte, 32)
	_, err = io.ReadFull(r, hash)
	c.Assert(err, IsNil)
	return base58.Encode(hash)
}

// SignTx must go through the cache: a seeded entry for the keysign height is
// what ends up in the signed message (the mock RPC would derive slot 950's hash
// instead), and a fresh height populates the cache on the way through.
func (s *SolTestSuite) TestSignTxUsesCachedConsensusBlockHash(c *C) {
	server := newSolCacheRPCServer(c, &solCacheRPCServer{})
	defer server.Close()
	client, pub, pubkeyMgr := s.newSolCacheTestClient(c, server)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()

	seeded := mockBlockhashForSlot(123)
	client.storeConsensusBlockHash(7, consensusBlockHashEntry{hash: seeded, slot: 123})

	signed, _, _, err := client.SignTx(newSolCacheTestTxOut(pub), 7)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)
	c.Assert(signedTxRecentBlockhash(c, signed), Equals, seeded)

	_, ok := client.lookupConsensusBlockHash(8)
	c.Assert(ok, Equals, false)
	signed, _, _, err = client.SignTx(newSolCacheTestTxOut(pub), 8)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)
	e, ok := client.lookupConsensusBlockHash(8)
	c.Assert(ok, Equals, true)
	c.Assert(e.hash, Equals, mockBlockhashForSlot(950))
	c.Assert(signedTxRecentBlockhash(c, signed), Equals, mockBlockhashForSlot(950))
}
