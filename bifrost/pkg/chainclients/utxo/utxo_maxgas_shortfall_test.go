package utxo

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// These tests cover #3383: an outbound's MaxGas only bounds its fee, so a vault
// holding less than amount + MaxGas must still sign when it can pay amount + the
// actual fee, and must still refuse when it cannot.

// btcRefundFromSingleUTXO builds a BTC refund out of a vault whose only UTXO is worth
// inbound sats. It returns the built transaction, the UTXO and the vault script.
func (s *BitcoinSignerSuite) btcRefundFromSingleUTXO(c *C, inbound int64, refund, maxGas uint64, gasRate int64) (*wire.MsgTx, btcjson.ListUnspentResult, []byte, error) {
	s.setNodeSignerForTest(c)
	s.client.cfg.UTXO.MaxSatsPerVByte = 9765 // config/default.yaml, the suite leaves it zero
	vault := s.client.nodePubKey
	addr, err := vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	script, err := s.client.getSourceScript(stypes.TxOutItem{VaultPubKey: vault, Chain: common.BTCChain})
	c.Assert(err, IsNil)
	utxo := btcjson.ListUnspentResult{
		TxID: fakeTxID(1), Address: addr.String(), ScriptPubKey: hex.EncodeToString(script),
		Amount: btcutil.Amount(inbound).ToBTC(), Confirmations: 10, Spendable: true,
	}

	// answer listunspent with the single UTXO and fail every other method
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var r struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&r), IsNil)
		rw.Header().Set("Content-Type", "application/json")
		reply := fmt.Sprintf(`"result":[{"txid":"%s","vout":0,"address":"%s","scriptPubKey":"%s","amount":%.8f,"confirmations":10,"spendable":true}]`,
			utxo.TxID, utxo.Address, utxo.ScriptPubKey, utxo.Amount)
		if r.Method != "listunspent" {
			reply = `"error":{"code":-5,"message":"No such mempool or blockchain transaction"}`
		}
		_, writeErr := fmt.Fprintf(rw, `{"jsonrpc":"2.0","id":%s,%s}`, r.ID, reply)
		c.Assert(writeErr, IsNil)
	}))
	defer server.Close()
	s.client.rpc, err = rpc.NewClient(server.URL, bob, password, 0, 0, common.BTCChain, zerolog.Nop())
	c.Assert(err, IsNil)

	to, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	inHash := strings.Repeat("ab", 32)
	tx := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   to,
		VaultPubKey: vault,
		Coins:       common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(refund))),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(maxGas))},
		GasRate:     gasRate,
		InHash:      common.TxID(inHash),
		Memo:        "REFUND:" + inHash,
	}
	redeemTx, _, err := s.client.buildTx(tx, script, 0)
	return redeemTx, utxo, script, err
}

// checkSpendsAmountPlusFee asserts the transaction pays the customer exactly refund,
// returns the remainder to the vault, and returns the fee it pays.
func checkSpendsAmountPlusFee(c *C, redeemTx *wire.MsgTx, inbound int64, refund uint64, script []byte) int64 {
	c.Assert(redeemTx.TxIn, HasLen, 1)
	// customer, change back to the vault, memo
	c.Assert(redeemTx.TxOut, HasLen, 3)
	c.Check(redeemTx.TxOut[0].Value, Equals, int64(refund))
	c.Check(redeemTx.TxOut[1].PkScript, DeepEquals, script)
	c.Check(redeemTx.TxOut[2].Value, Equals, int64(0))
	var out int64
	for _, o := range redeemTx.TxOut {
		out += o.Value
	}
	fee := inbound - out
	c.Check(redeemTx.TxOut[1].Value, Equals, inbound-int64(refund)-fee)
	return fee
}

// Reproduces the test-simulation failure: THORChain scheduled a refund out of an
// inactive vault after deducting the MaxGas of the time (49,000 sats), then re-priced
// MaxGas to 84,000 sats when the delayed outbound was released. The vault's only UTXO,
// the refunded inbound, covers the refund and the actual fee but not refund + MaxGas,
// which bifrost used to require: "insufficient available UTXOs: need 2616478, only
// have 2581478 available from 1 UTXOs".
func (s *BitcoinSignerSuite) TestBuildTxRefundShortOfMaxGasPaysActualFee(c *C) {
	const (
		inbound = int64(2_581_478)
		refund  = uint64(2_532_478)
		maxGas  = uint64(84_000)
		gasRate = int64(84)
	)
	c.Assert(int64(refund+maxGas) > inbound, Equals, true)

	redeemTx, utxo, script, err := s.btcRefundFromSingleUTXO(c, inbound, refund, maxGas, gasRate)
	c.Assert(err, IsNil)
	fee := checkSpendsAmountPlusFee(c, redeemTx, inbound, refund, script)

	// the fee is the estimate at the outbound's gas rate, well within MaxGas
	memoScripts, err := MemoToScripts("REFUND:"+strings.Repeat("ab", 32), btctxscript.MaxDataCarrierSize, btctxscript.NullDataScript, btctxscript.PayToWitnessScript)
	c.Assert(err, IsNil)
	size := s.client.estimateTxSize([]btcjson.ListUnspentResult{utxo}, memoScripts, redeemTx.TxOut[0].PkScript, script)
	c.Check(fee, Equals, gasRate*size)
	c.Check(uint64(fee) < maxGas, Equals, true)
}

// When the estimate at the gas rate exceeds MaxGas the fee is capped at MaxGas, so a
// vault short of amount + MaxGas, even by one sat, cannot pay and must not sign.
func (s *BitcoinSignerSuite) TestBuildTxShortOfMaxGasKeepsFeeWithinMaxGas(c *C) {
	const (
		refund  = uint64(2_532_478)
		maxGas  = uint64(84_000)
		gasRate = int64(1_000) // estimate is far above MaxGas
	)
	inbound := int64(refund) + int64(maxGas) - 1
	redeemTx, _, _, err := s.btcRefundFromSingleUTXO(c, inbound, refund, maxGas, gasRate)
	c.Assert(err, ErrorMatches, `not enough balance to pay customer: -1 .*`)
	c.Assert(redeemTx, IsNil)

	// with MaxGas available the cap applies exactly as before
	inbound = int64(refund) + int64(maxGas) + 6_000
	redeemTx, _, script, err := s.btcRefundFromSingleUTXO(c, inbound, refund, maxGas, gasRate)
	c.Assert(err, IsNil)
	c.Check(checkSpendsAmountPlusFee(c, redeemTx, inbound, refund, script), Equals, int64(maxGas))
}

// A vault that covers the amount but not the actual fee must not sign: the fee is
// never taken out of the customer's amount nor beyond the vault's inputs.
func (s *BitcoinSignerSuite) TestBuildTxShortOfActualFeeFails(c *C) {
	const (
		refund  = uint64(2_532_478)
		maxGas  = uint64(84_000)
		gasRate = int64(84)
	)
	redeemTx, _, _, err := s.btcRefundFromSingleUTXO(c, int64(refund)+5_000, refund, maxGas, gasRate)
	c.Assert(err, ErrorMatches, `not enough balance to pay customer: -\d+ \(inputs: 2537478, to customer: 2532478, gas: \d+\)`)
	c.Assert(redeemTx, IsNil)
}

// A vault that cannot even cover the amount fails selection, reporting the amount.
func (s *BitcoinSignerSuite) TestBuildTxShortOfAmountFailsSelection(c *C) {
	const (
		refund  = uint64(2_532_478)
		maxGas  = uint64(84_000)
		gasRate = int64(84)
	)
	_, _, _, err := s.btcRefundFromSingleUTXO(c, int64(refund)-1, refund, maxGas, gasRate)
	c.Assert(err, ErrorMatches, `fail to get unspent UTXO: insufficient available UTXOs: need 2532478, only have 2532477 available from 1 UTXOs; .*`)
}

// The floor only applies once all eligible UTXOs fall short of the total: a vault
// that can cover the total keeps selecting the oldest covering prefix.
func (s *DogecoinSignerSuite) TestUTXOSelectionFloorSpendsAllEligibleWhenShortOfTotal(c *C) {
	vault, addr, script := s.vaultScript(c)
	s.client.cfg.UTXO.MinUTXOConfirmations = 1
	defer s.useListUnspent(c, `[
		{"txid":"`+fakeTxID(1)+`","vout":0,"address":"`+addr.String()+`","scriptPubKey":"`+script+`","amount":3,"confirmations":5,"spendable":true},
		{"txid":"`+fakeTxID(2)+`","vout":0,"address":"`+addr.String()+`","scriptPubKey":"`+script+`","amount":2,"confirmations":4,"spendable":true}
	]`).Close()

	// strict selection of more than the vault holds fails as before
	_, err := s.client.getUtxoToSpend(vault, btcutil.Amount(600_000_000), 0)
	c.Check(err, ErrorMatches, `insufficient available UTXOs: need 600000000, only have 500000000 available from 2 UTXOs; .*`)

	// a floor within reach returns every eligible UTXO
	utxos, err := s.client.getUtxoToSpend(vault, btcutil.Amount(600_000_000), btcutil.Amount(450_000_000))
	c.Assert(err, IsNil)
	c.Assert(utxos, HasLen, 2)

	// a floor out of reach fails, reporting the floor
	_, err = s.client.getUtxoToSpend(vault, btcutil.Amount(600_000_000), btcutil.Amount(500_000_001))
	c.Check(err, ErrorMatches, `insufficient available UTXOs: need 500000001, only have 500000000 available from 2 UTXOs; .*`)

	// a reachable total ignores the floor and keeps the covering prefix
	s.client.cfg.UTXO.MaxUTXOsToSpend = 1
	utxos, err = s.client.getUtxoToSpend(vault, btcutil.Amount(250_000_000), btcutil.Amount(100_000_000))
	c.Assert(err, IsNil)
	c.Assert(utxos, HasLen, 1)
	c.Check(utxos[0].TxID, Equals, fakeTxID(1))
}

// ZEC selects its own confirmed inputs but required the same amount + MaxGas, so a
// vault holding less than that is affected the same way.
func (s *ZcashSignerSuite) TestZECRefundShortOfMaxGasPaysActualFee(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	makeCandidate, view, restore := s.zecInputSelectionCandidates(c, fixture)
	defer restore()
	reference := fixture.bridge.observedHeight

	const refund = uint64(1_000_000)
	inHash := strings.Repeat("ab", 32)
	to, err := types2.GetRandomPubKey().GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	tx := fixture.tx
	tx.ToAddress = to
	tx.Coins = common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(refund)))
	tx.InHash = common.TxID(inHash)
	tx.Memo = "REFUND:" + inHash
	maxGas := int64(tx.MaxGas.ToCoins().GetCoin(common.ZECAsset).Amount.Uint64())
	fee := int64(zecFeeForInputCount(1, tx.Memo))
	c.Assert(fee < maxGas, Equals, true)
	script, err := s.client.getSourceScript(tx)
	c.Assert(err, IsNil)

	// the only input covers the refund and its actual fee, but not refund + MaxGas
	inbound := int64(refund) + fee + 20_000
	c.Assert(inbound < int64(refund)+maxGas, Equals, true)
	view.Store([]zecInputSelectionCandidate{makeCandidate(strings.Repeat("aa", 32), reference-10, 0, uint64(inbound))})
	redeemTx, _, err := s.client.buildTx(tx, script, reference)
	c.Assert(err, IsNil)
	c.Assert(redeemTx.TxIn, HasLen, 1)
	c.Assert(redeemTx.TxOut, HasLen, 3)
	c.Check(redeemTx.TxOut[0].Value, Equals, int64(refund))
	c.Check(redeemTx.TxOut[1].PkScript, DeepEquals, script)
	c.Check(redeemTx.TxOut[1].Value, Equals, int64(20_000))
	c.Check(redeemTx.TxOut[2].Value, Equals, int64(0))

	// one zat short of the actual fee must not sign
	view.Store([]zecInputSelectionCandidate{makeCandidate(strings.Repeat("aa", 32), reference-10, 0, refund+uint64(fee)-1)})
	_, _, err = s.client.buildTx(tx, script, reference)
	c.Check(err, ErrorMatches, `not enough balance to pay customer: -1 .*`)

	// short of the refund itself fails selection, reporting the refund
	view.Store([]zecInputSelectionCandidate{makeCandidate(strings.Repeat("aa", 32), reference-10, 0, refund-1)})
	_, _, err = s.client.buildTx(tx, script, reference)
	c.Check(err, ErrorMatches, `fail to get confirmed ZEC inputs: insufficient confirmed ZEC inputs at tip \d+: have 999999, need 1000000`)
}
