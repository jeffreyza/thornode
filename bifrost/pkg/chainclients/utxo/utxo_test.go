package utxo

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/hashicorp/go-multierror"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/zec"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

const (
	bob      = "bob"
	password = "password"
)

var m *metrics.Metrics

func TestPackage(t *testing.T) {
	// Load the RPC fixtures here, not in init(): go test records the files a
	// test opens only once tests start running, and CI reuses cached results
	// when no recorded input changed (scripts/ci-stable-mtimes.sh). A fixture
	// read in init() would not invalidate the cached result when it changed.
	loadBTCFixtures()
	loadBCHFixtures()
	loadDOGEFixtures()
	loadLTCFixtures()
	TestingT(t)
}

func TestIsUTXOTxNotFoundErr(t *testing.T) {
	// bitcoind/litecoind/dogecoind return RPC error -5 (ErrRPCInvalidAddressOrKey)
	// for a genuinely-absent tx; that is the ONLY shape treated as "not found" —
	// the linchpin of BTC dropped-broadcast recovery. Everything else must fail
	// closed so a still-live broadcast is never mistaken for absent and re-signed.
	if !isUTXOTxNotFoundErr(&btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey}) {
		t.Fatal("code -5 RPCError should be recognized as not-found")
	}
	// zebrad (ZEC) surfaces the same not-found as a *zec.Error with code -5, not a
	// *btcjson.RPCError — this is the shape that previously slipped through.
	if !isUTXOTxNotFoundErr(&zec.Error{Code: -5, Message: "No such mempool or blockchain transaction"}) {
		t.Fatal("zec.Error code -5 should be recognized as not-found")
	}
	for _, e := range []error{
		nil,
		errors.New("connection refused"),
		&btcjson.RPCError{Code: btcjson.RPCErrorCode(-99)},
		&zec.Error{Code: -8, Message: "parameter must be hexadecimal"},
	} {
		if isUTXOTxNotFoundErr(e) {
			t.Fatalf("error %v should NOT be recognized as not-found", e)
		}
	}
}

func TestUTXOVerifyPresence(t *testing.T) {
	notFoundErr := &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey}
	for _, tc := range []struct {
		name    string
		verbose *rpc.TxResult
		err     error
		want    signercache.LookupResult
	}{
		// Canonical (mined) tx -> present, respect the cache.
		{"confirmed canonical", &rpc.TxResult{TxRawResult: btcjson.TxRawResult{Confirmations: 6, BlockHash: "blockhash"}}, nil, signercache.LookupFound},
		// Still pending in mempool (no block yet) -> present, could still land.
		{"mempool pending", &rpc.TxResult{TxRawResult: btcjson.TxRawResult{Confirmations: 0, BlockHash: ""}}, nil, signercache.LookupFound},
		// Retained by txindex but its block was orphaned (0 confs, block hash set) ->
		// not canonical, surface as absent so mimir+expiry can re-sign. (Confirmations
		// is uint64, so a non-canonical tx reports 0 rather than a negative value.)
		{"orphaned block zero conf", &rpc.TxResult{TxRawResult: btcjson.TxRawResult{Confirmations: 0, BlockHash: "orphanhash"}}, nil, signercache.LookupNotFound},
		// Defensive: nil result with no error -> respect the cache (fail closed).
		{"nil verbose no error", nil, nil, signercache.LookupFound},
		// Definitive not-found error -> absent.
		{"not found error", nil, notFoundErr, signercache.LookupNotFound},
		// Any other error -> ambiguous, respect the cache.
		{"transport error", nil, errors.New("connection refused"), signercache.LookupAmbiguous},
	} {
		if got := utxoVerifyPresence(tc.verbose, tc.err); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpdateCurrentBlockHeightMonotonic(t *testing.T) {
	client := &Client{}

	client.updateCurrentBlockHeight(100)
	client.updateCurrentBlockHeight(90)
	if got := client.getCurrentBlockHeight(); got != 100 {
		t.Fatalf("expected current block height to remain 100, got %d", got)
	}

	client.updateCurrentBlockHeight(110)
	if got := client.getCurrentBlockHeight(); got != 110 {
		t.Fatalf("expected current block height to advance to 110, got %d", got)
	}
}

// TestAllNotSelectedForKeysign covers the signer stand-down helper with plain
// and aggregated errors from the UTXO signing path.
func (s *HelpersSuite) TestAllNotSelectedForKeysign(c *C) {
	c.Assert(allNotSelectedForKeysign(nil), Equals, false)
	c.Assert(allNotSelectedForKeysign(tss.ErrNotSelectedForKeysign), Equals, true)

	allNotSelected := multierror.Append(nil, tss.ErrNotSelectedForKeysign, tss.ErrNotSelectedForKeysign)
	c.Assert(allNotSelectedForKeysign(allNotSelected), Equals, true)

	mixed := multierror.Append(nil, tss.ErrNotSelectedForKeysign, errors.New("signature failed"))
	c.Assert(allNotSelectedForKeysign(mixed), Equals, false)
}

// TestAllJoinPartyFailures covers the helper that decides whether a failed
// UTXO keysign exposed no signature and may be rebuilt on the next attempt.
func (s *HelpersSuite) TestAllJoinPartyFailures(c *C) {
	syncFail := tss.NewKeysignError(ttypes.Blame{FailReason: blame.TssSyncFail})
	roundFail := tss.NewKeysignError(ttypes.Blame{FailReason: blame.TssTimeout, Round: tssMessages.KEYSIGN2Unicast})
	wrapped := fmt.Errorf("fail to get witness: %w", fmt.Errorf("cannot sign tx input: %w", syncFail))

	c.Assert(allJoinPartyFailures(nil, 1), Equals, false)
	c.Assert(allJoinPartyFailures(syncFail, 1), Equals, false)
	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped), 0), Equals, false)

	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped), 1), Equals, true)
	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped, syncFail), 2), Equals, true)

	// one input signed or was never attempted
	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped), 2), Equals, false)
	// the party formed for one input
	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped, roundFail), 2), Equals, false)
	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped, tss.ErrNotSelectedForKeysign), 2), Equals, false)
	c.Assert(allJoinPartyFailures(multierror.Append(nil, wrapped, errors.New("rpc failed")), 2), Equals, false)
}

func GetMetricForTest(c *C, chain common.Chain) *metrics.Metrics {
	if m != nil {
		return m
	}
	var err error
	m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
		Enabled:      false,
		ListenPort:   9000,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		Chains:       common.Chains{common.DOGEChain, common.BCHChain, common.LTCChain, common.BTCChain, common.DASHChain},
	})
	c.Assert(m, NotNil)
	c.Assert(err, IsNil)
	return m
}

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	content, err := os.ReadFile(fixture)
	if err != nil {
		c.Fatal(err)
	}
	rw.Header().Set("Content-Type", "application/json")
	if _, err = rw.Write(content); err != nil {
		c.Fatal(err)
	}
}

// assertSenderUTXOValidation verifies that Client.getSender rejects bare
// multisig sender UTXOs. This guards against the sender-spoofing attack where
// an attacker crafts a bare multisig [victim_pubkey, attacker_pubkey] and signs
// with their own key, causing getSender to attribute the tx to the victim
// (first pubkey's address). P2SH/P2WSH-wrapped multisig is not affected since
// those scriptPubKeys return a single 3xxx/bc1q address. The same check applies
// uniformly to BTC, LTC, DOGE, BCH, and ZEC — each chain's isValidUTXO rejects
// MultiSigTy and accepts single-address/single-sig types.
func assertSenderUTXOValidation(c *C, client *Client) {
	const multisigTxid = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// 1-of-1 bare multisig: OP_1 <pubkey> OP_1 OP_CHECKMULTISIG
	const bareMultisigScriptHex = "51210281feb90c058c3436f8bc361930ae99fcfb530a699cdad141d7244bfcad521a1f51ae"

	tx := btcjson.TxRawResult{
		Vin: []btcjson.Vin{{Txid: multisigTxid, Vout: 0}},
	}
	vinZeroTxs := map[string]*btcjson.TxRawResult{
		multisigTxid: {
			Vout: []btcjson.Vout{
				{
					ScriptPubKey: btcjson.ScriptPubKeyResult{
						Hex:  bareMultisigScriptHex,
						Type: "multisig",
					},
				},
			},
		},
	}

	sender, err := client.getSender(&tx, vinZeroTxs)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "sender utxo must be single-sig")
	c.Assert(sender, Equals, "")
}
