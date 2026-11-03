package solana

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/mr-tron/base58"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
)

// mockBlockhashForSlot is a per-slot, base58-VALID, 32-byte blockhash so the mock
// survives CreateTx's base58 decode when reused by the SignTx-path tests and the
// serialized message carries a full-width hash a test can parse back out.
func mockBlockhashForSlot(slot uint64) string {
	b := make([]byte, 32)
	copy(b, fmt.Sprintf("slot%d", slot))
	return base58.Encode(b)
}

// consensusBlockhashRPCResponse returns the JSON-RPC *result* payload for the SOL
// RPC methods the consensus-blockhash signing path + participation gate call
// (getSlot/getBlocks/getBlockTime/getBlock/isBlockhashValid). It is deterministic
// and concurrency-safe (no *C) so the shared signer-test server can serve it while
// the background scanner hits the same server from another goroutine. handled is
// false for any other method. The canned values bracket the consensus target
// (t - margin) at slot 950 and report the derived hash still valid.
func consensusBlockhashRPCResponse(method string, params json.RawMessage) (result string, handled bool) {
	const tip = uint64(1000)
	const alignSlot = int64(950)
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	switch method {
	case "getSlot":
		return fmt.Sprintf("%d", tip), true
	case "getBlocks":
		// finalized slot list [900..1000] brackets target at slot 950
		out := make([]string, 0, 101)
		for s := uint64(900); s <= tip; s++ {
			out = append(out, fmt.Sprintf("%d", s))
		}
		return "[" + strings.Join(out, ",") + "]", true
	case "getBlockTime":
		slot := alignSlot
		var p []json.RawMessage
		if json.Unmarshal(params, &p) == nil && len(p) > 0 {
			_ = json.Unmarshal(p[0], &slot)
		}
		return fmt.Sprintf("%d", target+(slot-alignSlot)), true
	case "getBlock":
		slot := uint64(alignSlot)
		var p []json.RawMessage
		if json.Unmarshal(params, &p) == nil && len(p) > 0 {
			_ = json.Unmarshal(p[0], &slot)
		}
		return fmt.Sprintf(`{"blockhash":%q,"previousBlockhash":"","parentSlot":%d,"transactions":[]}`, mockBlockhashForSlot(slot), slot-1), true
	case "isBlockhashValid":
		// context.slot at the tip so a minContextSlot == the ~alignSlot anchor passes.
		return fmt.Sprintf(`{"context":{"slot":%d},"value":true}`, tip), true
	}
	return "", false
}

// cometBFTBlockResponse returns a minimal CometBFT ResultBlock JSON-RPC response
// whose header.time is the fixed consensus block time, so the REAL thorchain
// bridge's GetBlockTimestamp resolves in the signer tests. id must echo the
// request id (the CometBFT client validates it).
func cometBFTBlockResponse(id json.RawMessage) string {
	ts := time.Unix(testConsensusBlockTimeUnix, 0).UTC().Format(time.RFC3339Nano)
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"block_id":{"hash":"","parts":{"total":0,"hash":""}},"block":{"header":{"height":"1","time":%q}}}}`, string(id), ts)
}

// solRPCScenario is a param-aware mock of the SOL RPC methods consensusBlockHash
// depends on (getSlot/getBlocks/getBlockTime/getBlock). Different scenarios model
// the RPC views seen by individual signers so unit tests can prove they derive the
// same blockhash whenever their finalized histories bracket the consensus target.
// By default block time is a strictly increasing function of slot; blockTime can
// override it to model coarser timestamps or unavailable block times.
// blockhashValidity selects the mock's isBlockhashValid response so SignTx abstain
// branches can be exercised without magic strings (a typo is a compile error, not a
// silent happy-path run).
type blockhashValidity int

const (
	blockhashValid    blockhashValidity = iota // value:true at a caught-up bank (happy path)
	blockhashInvalid                           // value:false at a caught-up bank -> genuine expiry -> SignTx drops
	blockhashRPCError                          // RPC error -> SignTx retains
	blockhashLagging                           // value:false but bank below minContextSlot -> defensive error -> SignTx retains
)

type solRPCScenario struct {
	tip       uint64 // finalized tip this node's RPC reports (getSlot)
	floorSlot uint64 // oldest slot the ledger retains (older slots are PRUNED away)
	alignSlot int64  // slot whose blockTime == target
	target    int64  // t - margin (from the mock bridge time)
	blockTime func(slot uint64) (value int64, ok bool)
	// skipFrom/skipTo (inclusive, ignored when skipTo == 0) model a run of SKIPPED
	// slots inside RETAINED history: no leader produced a block, so getBlocks omits
	// them, but every block BELOW the run is still served and
	// getFirstAvailableBlock still reports floorSlot. Pruning and a skipped run look
	// identical in a getBlocks listing, which is why the consensus search may only
	// conclude "pruned" from the reported floor.
	skipFrom, skipTo uint64
	// noHistoryFloor makes getFirstAvailableBlock ERROR, modelling a provider that
	// does not serve it. Pruning is then unproven and the search must gallop on.
	noHistoryFloor bool
	// isBlockhashValidResp drives the per-node participation gate so SignTx abstain
	// branches can be exercised (see blockhashValidity).
	isBlockhashValidResp blockhashValidity
	// Pre-sign filter reads. accounts maps an address to its getAccountInfo value
	// JSON ("null" = missing); an unlisted address is a funded plain System account.
	// simErr is the simulateTransaction err JSON ("" = success).
	accounts map[string]string
	simErr   string
}

func (sc solRPCScenario) blockTimeOf(slot uint64) int64 {
	return sc.target + (int64(slot) - sc.alignSlot)
}

func (sc solRPCScenario) blockTimeAt(slot uint64) (int64, bool) {
	if sc.blockTime != nil {
		return sc.blockTime(slot)
	}
	if !sc.hasBlock(slot) {
		return 0, false // pruned or skipped -> getBlockTime reports null
	}
	return sc.blockTimeOf(slot), true
}

// skipped reports whether slot falls in this scenario's skipped-slot run.
func (sc solRPCScenario) skipped(slot uint64) bool {
	return sc.skipTo != 0 && slot >= sc.skipFrom && slot <= sc.skipTo
}

// hasBlock reports whether the node can serve a block at slot: it must be retained
// (>= floorSlot), at or below the finalized tip, and not a skipped slot.
func (sc solRPCScenario) hasBlock(slot uint64) bool {
	return slot >= sc.floorSlot && slot <= sc.tip && !sc.skipped(slot)
}

// presentSlots returns the finalized slots in [start,end] this node can see.
func (sc solRPCScenario) presentSlots(start, end uint64) []uint64 {
	if end > sc.tip {
		end = sc.tip
	}
	if start < sc.floorSlot {
		start = sc.floorSlot
	}
	out := []uint64{}
	for s := start; s <= end; s++ {
		if !sc.hasBlock(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// blockhashValidResponse renders the isBlockhashValid reply this scenario's
// participation-gate setting implies: the per-node gate checks the derived hash is
// still usable at the processed tip, and context.slot reports the answering bank so a
// below-min-context (lagging) backend is detectable. ok is false when the scenario
// wants a JSON-RPC ERROR envelope rather than a result. Shared by both mock servers so
// they cannot drift apart on what a scenario means.
func (sc solRPCScenario) blockhashValidResponse() (body string, ok bool) {
	switch sc.isBlockhashValidResp {
	case blockhashInvalid:
		return fmt.Sprintf(`{"context":{"slot":%d},"value":false}`, sc.tip), true
	case blockhashRPCError:
		return `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"node unavailable"}}`, false
	case blockhashLagging:
		// answers from a bank far below the anchor -> defensive guard errors
		return `{"context":{"slot":1},"value":false}`, true
	default:
		return fmt.Sprintf(`{"context":{"slot":%d},"value":true}`, sc.tip), true
	}
}

func (sc solRPCScenario) server(c *C) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var rpcReq struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&rpcReq), IsNil)

		write := func(result string) {
			_, err := rw.Write([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%s}`, result)))
			c.Assert(err, IsNil)
		}

		switch rpcReq.Method {
		case "getSlot":
			write(fmt.Sprintf("%d", sc.tip))
		case "getFirstAvailableBlock":
			if sc.noHistoryFloor {
				_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
				c.Assert(err, IsNil)
				break
			}
			write(fmt.Sprintf("%d", sc.floorSlot))
		case "getBlocks":
			var p []json.RawMessage
			c.Assert(json.Unmarshal(rpcReq.Params, &p), IsNil)
			var start, end uint64
			c.Assert(json.Unmarshal(p[0], &start), IsNil)
			c.Assert(json.Unmarshal(p[1], &end), IsNil)
			b, err := json.Marshal(sc.presentSlots(start, end))
			c.Assert(err, IsNil)
			write(string(b))
		case "getBlockTime":
			var p []json.RawMessage
			c.Assert(json.Unmarshal(rpcReq.Params, &p), IsNil)
			var slot uint64
			c.Assert(json.Unmarshal(p[0], &slot), IsNil)
			// Solana can return a null block time even for a listed finalized slot.
			// The scenario override lets tests exercise that response; GetBlockTime
			// decodes the JSON null into a nil timestamp.
			blockTime, ok := sc.blockTimeAt(slot)
			if !ok {
				write("null")
				break
			}
			write(fmt.Sprintf("%d", blockTime))
		case "getBlock":
			var p []json.RawMessage
			c.Assert(json.Unmarshal(rpcReq.Params, &p), IsNil)
			var slot uint64
			c.Assert(json.Unmarshal(p[0], &slot), IsNil)
			// A per-slot blockhash so a test can prove WHICH slot was chosen.
			write(fmt.Sprintf(`{"blockhash":%q,"previousBlockhash":"","parentSlot":%d}`, mockBlockhashForSlot(slot), slot-1))
		case "isBlockhashValid":
			result, ok := sc.blockhashValidResponse()
			if !ok {
				_, err := rw.Write([]byte(result))
				c.Assert(err, IsNil)
				break
			}
			write(result)
		case "getMinimumBalanceForRentExemption":
			write("890880")
		case "getAccountInfo":
			var p []json.RawMessage
			c.Assert(json.Unmarshal(rpcReq.Params, &p), IsNil)
			var addr string
			c.Assert(json.Unmarshal(p[0], &addr), IsNil)
			value, ok := sc.accounts[addr]
			if value == "rpcerror" {
				_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
				c.Assert(err, IsNil)
				break
			}
			if !ok {
				value = `{"owner":"11111111111111111111111111111111","executable":false,"lamports":100000000000,"data":["","base64"]}`
			}
			write(fmt.Sprintf(`{"context":{"slot":1},"value":%s}`, value))
		case "simulateTransaction":
			simErr := sc.simErr
			if simErr == "rpcerror" {
				_, err := rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind"}}`))
				c.Assert(err, IsNil)
				break
			}
			if simErr == "" {
				simErr = "null"
			}
			write(fmt.Sprintf(`{"context":{"slot":1},"value":{"err":%s,"logs":[]}}`, simErr))
		default:
			c.Fatalf("unexpected RPC method %q", rpcReq.Method)
		}
	}))
}

// newSolRPCForServer wires a SOL RPC client to an already-started scenario server.
func newSolRPCForServer(server *httptest.Server) *rpc.SolRPC {
	return rpc.NewSolRPC("http://"+server.Listener.Addr().String(), time.Second)
}

// newDeterministicSOLRPC returns a SOL RPC client backed by a happy-path
// deterministic-blockhash mock (finalized tip well past target, S* == slot 950
// comfortably inside the search window). Directly-constructed SignTx-path tests
// wire this so consensusBlockHash + the per-node staleness gate resolve instead
// of nil-deref'ing on a missing rpcClient / scanner RPC.
func newDeterministicSOLRPC(c *C) (*httptest.Server, *rpc.SolRPC) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	sc := solRPCScenario{tip: 1000, floorSlot: 1, alignSlot: 950, target: target}
	server := sc.server(c)
	return server, newSolRPCForServer(server)
}

// newSOLRPCBlockhashInvalid brackets + derives a hash like the happy path, but the
// participation gate reports it NO LONGER valid at the processed tip. Drives SignTx's
// permanent-abstain branch (drop for reschedule).
func newSOLRPCBlockhashInvalid(c *C) (*httptest.Server, *rpc.SolRPC) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	sc := solRPCScenario{tip: 1000, floorSlot: 1, alignSlot: 950, target: target, isBlockhashValidResp: blockhashInvalid}
	server := sc.server(c)
	return server, newSolRPCForServer(server)
}

// newSOLRPCBlockhashValidError brackets + derives a hash like the happy path, but the
// participation gate ERRORS. Drives SignTx's transient-abstain branch (fail closed ->
// return an error so the signer retains + retries).
func newSOLRPCBlockhashValidError(c *C) (*httptest.Server, *rpc.SolRPC) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	sc := solRPCScenario{tip: 1000, floorSlot: 1, alignSlot: 950, target: target, isBlockhashValidResp: blockhashRPCError}
	server := sc.server(c)
	return server, newSolRPCForServer(server)
}

// newSOLRPCBlockhashLagging brackets + derives a hash, but the participation gate
// answers from a bank BELOW the anchor (lagging). The minContextSlot guard turns that
// into an error, so SignTx retains rather than dropping a possibly-still-valid hash.
func newSOLRPCBlockhashLagging(c *C) (*httptest.Server, *rpc.SolRPC) {
	target := testConsensusBlockTimeUnix - solFinalizationMarginSeconds
	sc := solRPCScenario{tip: 1000, floorSlot: 1, alignSlot: 950, target: target, isBlockhashValidResp: blockhashLagging}
	server := sc.server(c)
	return server, newSolRPCForServer(server)
}
