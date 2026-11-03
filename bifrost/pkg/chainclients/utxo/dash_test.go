package utxo

import (
	"bytes"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	dashchaincfg "github.com/dashpay/dashd-go/chaincfg"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

type DashSuite struct{}

var _ = Suite(&DashSuite{})

// newDashClientForUnitTest returns a minimally-initialised *Client suitable
// for exercising Dash-specific helper methods that only read cfg/log — no
// RPC, signer, or block scanner involvement. Use the full Client constructor
// for anything that talks to the chain.
func newDashClientForUnitTest() *Client {
	client := &Client{
		cfg:                       config.BifrostChainConfiguration{ChainID: common.DASHChain},
		log:                       zerolog.Nop(),
		regexpRemoveTrailingZeros: regexp.MustCompile(`(00)+$`),
	}
	client.instantSend = instantSendHandler{client: client, enabled: client.cfg.InstantSend.Enabled}
	return client
}

// TestPayToPubKeyHashDash verifies the script builder produces a
// standard P2PKH scriptPubKey (OP_DUP OP_HASH160 <20-byte hash> OP_EQUALVERIFY
// OP_CHECKSIG). This is the key divergence from DOGE/BCH, which use the
// P2WPKH shape (OP_0 <20-byte hash>) — dashd 23 has no WITNESS_V0_KEYHASH
// variant and would reject that shape as NONSTANDARD. A regression here
// causes extended-memo outbounds to be rejected on broadcast.
func (s *DashSuite) TestPayToPubKeyHashDash(c *C) {
	hash := make([]byte, 20)
	for i := range hash {
		hash[i] = byte(i)
	}

	script, err := payToPubKeyHashMemoScript(hash)
	c.Assert(err, IsNil)

	// Expected: 0x76 (OP_DUP) 0xa9 (OP_HASH160) 0x14 (push 20) <20 bytes> 0x88 (OP_EQUALVERIFY) 0xac (OP_CHECKSIG)
	c.Assert(len(script), Equals, 25, Commentf("P2PKH scriptPubKey is always 25 bytes; got %d", len(script)))
	c.Assert(script[0], Equals, byte(dashtxscript.OP_DUP))
	c.Assert(script[1], Equals, byte(dashtxscript.OP_HASH160))
	c.Assert(script[2], Equals, byte(0x14)) // push 20 bytes
	c.Assert(bytes.Equal(script[3:23], hash), Equals, true, Commentf("hash bytes must match input"))
	c.Assert(script[23], Equals, byte(dashtxscript.OP_EQUALVERIFY))
	c.Assert(script[24], Equals, byte(dashtxscript.OP_CHECKSIG))

	// Round-trip: the script must parse back as a pubkeyhash script under
	// Dash's chain params, not as witness_v0_keyhash.
	class, _, _, err := dashtxscript.ExtractPkScriptAddrs(script, &dashchaincfg.MainNetParams)
	c.Assert(err, IsNil)
	c.Assert(class, Equals, dashtxscript.PubKeyHashTy,
		Commentf("must classify as PubKeyHashTy, not WitnessV0KeyHashTy; got %s", class))
}

// TestMemoToScripts verifies the full MemoToScripts pipeline produces
// P2PKH-shaped overflow scripts when called with Dash's
// payToPubKeyHashMemoScript callback. This is the integration between the
// shape-agnostic MemoToScripts and the Dash-specific script builder.
func (s *DashSuite) TestMemoToScripts(c *C) {
	// Memo > MaxDataCarrierSize (80) forces a continuation, which creates
	// overflow outputs via the payToKeyHashScript callback.
	longMemo := strings.Repeat("a", 100) // 100 > 80 triggers overflow

	scripts, err := MemoToScripts(longMemo, dashtxscript.MaxDataCarrierSize,
		dashtxscript.NullDataScript, payToPubKeyHashMemoScript)
	c.Assert(err, IsNil)
	c.Assert(len(scripts) >= 2, Equals, true,
		Commentf("expected at least one OP_RETURN + one overflow; got %d", len(scripts)))

	// First script is OP_RETURN (starts with 0x6a).
	c.Assert(scripts[0][0], Equals, byte(0x6a))

	// Every overflow script must be P2PKH-shaped, not P2WPKH-shaped.
	for i, overflow := range scripts[1:] {
		c.Assert(len(overflow), Equals, 25,
			Commentf("overflow script %d: expected 25-byte P2PKH; got %d bytes", i, len(overflow)))
		c.Assert(overflow[0], Equals, byte(dashtxscript.OP_DUP),
			Commentf("overflow script %d: first byte is not OP_DUP (would be OP_0=0x00 for P2WPKH shape)", i))
	}
}

func (s *DashSuite) TestGetAddressesFromScriptPubKeyDASH(c *C) {
	hash := make([]byte, 20)
	for i := range hash {
		hash[i] = byte(i + 1)
	}
	script, err := dashtxscript.NewScriptBuilder().
		AddOp(dashtxscript.OP_DUP).
		AddOp(dashtxscript.OP_HASH160).
		AddData(hash).
		AddOp(dashtxscript.OP_EQUALVERIFY).
		AddOp(dashtxscript.OP_CHECKSIG).
		Script()
	c.Assert(err, IsNil)

	expectedAddress := "XanAvE5GMB8CsPH78B9moJq9viEVKvCS4f"
	if common.CurrentChainNetwork == common.MockNet {
		expectedAddress = "yLQmwB9hninHD8Ceh2UAqLFWCzirppNLik"
	}
	testCases := []struct {
		ScriptPubKeyResult btcjson.ScriptPubKeyResult
		ShouldReturnNil    bool
		Expected           []string
	}{
		{
			ScriptPubKeyResult: btcjson.ScriptPubKeyResult{
				Addresses: []string{"XhkcLQT9RKiRyTDpecLtPDL6z6M4v1ZiE3"},
			},
			Expected: []string{"XhkcLQT9RKiRyTDpecLtPDL6z6M4v1ZiE3"},
		},
		{
			ScriptPubKeyResult: btcjson.ScriptPubKeyResult{
				Hex: hex.EncodeToString(script),
			},
			Expected: []string{expectedAddress},
		},
		{
			ShouldReturnNil:    true,
			ScriptPubKeyResult: btcjson.ScriptPubKeyResult{},
		},
		{
			ShouldReturnNil: true,
			ScriptPubKeyResult: btcjson.ScriptPubKeyResult{
				Hex: "invalidhex",
			},
		},
	}

	for _, tc := range testCases {
		client := newDashClientForUnitTest()

		addresses := client.getAddressesFromScriptPubKeyDASH(tc.ScriptPubKeyResult)
		if tc.ShouldReturnNil {
			c.Assert(addresses, IsNil)
		} else {
			c.Assert(addresses, DeepEquals, tc.Expected)
			for _, address := range addresses {
				c.Assert(common.IsValidDASHAddress(address), Equals, true)
			}
		}
	}
}

// TestGetChainCfgDASH verifies the chain-params selector uses
// Dash's params (not BTC's) for the active network. A silent fall-through to
// chaincfg.MainNetParams would cause address decoding to fail on real Dash
// addresses while falsely accepting BTC ones.
func (s *DashSuite) TestGetChainCfgDASH(c *C) {
	client := newDashClientForUnitTest()
	params := client.getChainCfgDASH()
	want := &dashchaincfg.MainNetParams
	if common.CurrentChainNetwork == common.MockNet {
		want = &dashchaincfg.RegressionNetParams
	}
	c.Assert(params, Equals, want)
}

// TestGetMemoDASHUsesCanonicalScriptHex verifies Dash memo parsing ignores the
// potentially lossy asm rendering and reconstructs the memo from the canonical
// script bytes instead. Dash Core can render "NOOP" as the decimal script
// number "1347374926" in asm, which the old generic parser would not recover
// back into the original memo text.
func (s *DashSuite) TestGetMemoDASHUsesCanonicalScriptHex(c *C) {
	client := newDashClientForUnitTest()

	script, err := dashtxscript.NullDataScript([]byte("NOOP"))
	c.Assert(err, IsNil)

	tx := &btcjson.TxRawResult{
		Vout: []btcjson.Vout{
			{
				ScriptPubKey: btcjson.ScriptPubKeyResult{
					Type: "nulldata",
					Hex:  hex.EncodeToString(script),
					Asm:  "OP_RETURN 1347374926",
				},
			},
		},
	}

	memo, err := client.getMemo(tx)
	c.Assert(err, IsNil)
	c.Assert(memo, Equals, "NOOP")
}

// TestGetMemoDASHConcatenatesNulldataOutputs verifies Dash memo parsing
// preserves output order across multiple nulldata fragments, which is required
// for extended THORChain memos split across several outputs.
func (s *DashSuite) TestGetMemoDASHConcatenatesNulldataOutputs(c *C) {
	client := newDashClientForUnitTest()

	script1, err := dashtxscript.NullDataScript([]byte("SWAP:"))
	c.Assert(err, IsNil)
	script2, err := dashtxscript.NullDataScript([]byte("DASH.DASH:"))
	c.Assert(err, IsNil)
	script3, err := dashtxscript.NullDataScript([]byte("Xabc"))
	c.Assert(err, IsNil)

	tx := &btcjson.TxRawResult{
		Vout: []btcjson.Vout{
			{
				ScriptPubKey: btcjson.ScriptPubKeyResult{
					Type: "nulldata",
					Hex:  hex.EncodeToString(script1),
				},
			},
			{
				ScriptPubKey: btcjson.ScriptPubKeyResult{
					Type: "pubkeyhash",
					Hex:  "76a914000000000000000000000000000000000000000088ac",
				},
			},
			{
				ScriptPubKey: btcjson.ScriptPubKeyResult{
					Type: "nulldata",
					Hex:  hex.EncodeToString(script2),
				},
			},
			{
				ScriptPubKey: btcjson.ScriptPubKeyResult{
					Type: "nulldata",
					Hex:  hex.EncodeToString(script3),
				},
			},
		},
	}

	memo, err := client.getMemo(tx)
	c.Assert(err, IsNil)
	c.Assert(memo, Equals, "SWAP:DASH.DASH:Xabc")
}

// The shared builder and native Dash signer must agree on legacy wire bytes
// and transaction IDs, including signatures returned to shared accounting.
func (s *DashSuite) TestDashWireConversion(c *C) {
	tx := wire.NewMsgTx(wire.TxVersion)
	tx.LockTime = 12345
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{1, 2, 3}, 4), []byte{0x51}, nil))
	tx.AddTxIn(wire.NewTxIn(wire.NewOutPoint(&chainhash.Hash{5, 6, 7}, 8), []byte{0x52}, nil))
	tx.TxIn[1].Sequence = 123
	nullDataScript, err := dashtxscript.NullDataScript([]byte("dash"))
	c.Assert(err, IsNil)
	tx.AddTxOut(wire.NewTxOut(1000, nullDataScript))

	var original, native, roundTrip bytes.Buffer
	c.Assert(tx.Serialize(&original), IsNil)
	dashTx := wireToDASH(tx)
	c.Assert(dashTx.Serialize(&native), IsNil)
	c.Assert(native.Bytes(), DeepEquals, original.Bytes())
	c.Assert(dashTx.TxHash().String(), Equals, tx.TxHash().String())
	out := dashToWire(dashTx)
	c.Assert(out.Serialize(&roundTrip), IsNil)
	c.Assert(roundTrip.Bytes(), DeepEquals, original.Bytes())

	// Attaching the native signer's result must not mutate the builder's inputs.
	dashTx.TxIn[1].SignatureScript = []byte{0x53}
	c.Assert(tx.TxIn[1].SignatureScript, DeepEquals, []byte{0x52})
	out = dashToWire(dashTx)
	c.Assert(out.TxIn[1].SignatureScript, DeepEquals, []byte{0x53})
	c.Assert(out.TxHash().String(), Equals, dashTx.TxHash().String())
}
