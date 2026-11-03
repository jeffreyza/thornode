package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/mempool"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// feeCapTestMaxSatsPerVByte matches max_sats_per_vbyte for LTC in config/default.yaml.
const feeCapTestMaxSatsPerVByte = 9765

// LitecoinFeeCapSuite signs LTC outbounds whose estimated fee exceeds MaxSatsPerVByte
// and checks the capped fee against the final signed size, the way Litecoin Core's
// sendrawtransaction "maxfeerate" check does.
type LitecoinFeeCapSuite struct {
	client *Client
	server *httptest.Server
	db     *leveldb.DB
	keys   *thorclient.Keys

	mu    sync.Mutex
	utxos []btcjson.ListUnspentResult
}

var _ = Suite(&LitecoinFeeCapSuite{})

func (s *LitecoinFeeCapSuite) SetUpSuite(c *C) {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(bob, cKeys.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.keys = thorclient.NewKeysWithKeybase(kb, bob, password)
}

func (s *LitecoinFeeCapSuite) SetUpTest(c *C) {
	s.utxos = nil
	types2.SetupConfigForTest()

	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		if req.RequestURI == thorclient.ChainVersionEndpoint {
			_, err := rw.Write([]byte(`{"current":"` + types2.GetCurrentVersion().String() + `"}`))
			c.Assert(err, IsNil)
			return
		}
		buf, err := io.ReadAll(req.Body)
		c.Assert(err, IsNil)
		c.Assert(req.Body.Close(), IsNil)
		if len(buf) == 0 {
			return
		}
		r := struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}{}
		c.Assert(json.Unmarshal(buf, &r), IsNil)
		switch r.Method {
		case "getnetworkinfo":
			httpTestHandler(c, rw, "../../../../test/fixtures/ltc/getnetworkinfo.json")
		case "getblockcount":
			httpTestHandler(c, rw, "../../../../test/fixtures/ltc/blockcount.json")
		case "importaddress":
			httpTestHandler(c, rw, "../../../../test/fixtures/ltc/importaddress.json")
		case "createwallet":
			_, err = rw.Write([]byte(`{"result":null,"error":null,"id":1}`))
			c.Assert(err, IsNil)
		case "listunspent":
			s.mu.Lock()
			var result []byte
			result, err = json.Marshal(s.utxos)
			s.mu.Unlock()
			c.Assert(err, IsNil)
			_, err = rw.Write([]byte(`{"result":` + string(result) + `,"error":null,"id":1}`))
			c.Assert(err, IsNil)
		case "sendrawtransaction":
			s.sendRawTransaction(c, rw, r.Params)
		}
	}))

	cfg := config.BifrostChainConfiguration{
		ChainID:     "LTC",
		UserName:    bob,
		Password:    password,
		DisableTLS:  true,
		HTTPostMode: true,
		RPCHost:     s.server.Listener.Addr().String(),
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1, // avoids querying thorchain for block height
		},
	}
	cfg.UTXO.DefaultSatsPerVByte = 25
	cfg.UTXO.MaxSatsPerVByte = feeCapTestMaxSatsPerVByte

	ns := strconv.Itoa(time.Now().Nanosecond())
	bridgeCfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       s.server.Listener.Addr().String(),
		SignerName:      bob,
		SignerPasswd:    password,
		ChainHomeFolder: filepath.Join(os.TempDir(), ns, ".thorcli"),
	}
	m := GetMetricForTest(c, common.LTCChain)
	bridge, err := thorclient.NewThorchainBridge(bridgeCfg, m, s.keys)
	c.Assert(err, IsNil)
	s.client, err = NewClient(s.keys, cfg, nil, bridge, m)
	c.Assert(err, IsNil)
	s.db, err = leveldb.Open(storage.NewMemStorage(), nil)
	c.Assert(err, IsNil)
	s.client.temporalStorage, err = utxo.NewTemporalStorage(s.db, 0)
	c.Assert(err, IsNil)

	priKeyBuf, err := hex.DecodeString("b404c5ec58116b5f0fe13464a92e46626fc5db130e418cbce98df86ffe9317c5")
	c.Assert(err, IsNil)
	pkey, _ := btcec.PrivKeyFromBytes(btcec.S256(), priKeyBuf)
	s.client.nodePrivKey = pkey
	s.client.nodePubKey, err = bech32AccountPubKey(pkey)
	c.Assert(err, IsNil)
}

func (s *LitecoinFeeCapSuite) TearDownTest(c *C) {
	s.server.Close()
	c.Assert(s.db.Close(), IsNil)
}

// sendRawTransaction mimics Litecoin Core: reject the transaction when its fee exceeds
// maxfeerate (LTC/kvB) applied to the transaction's virtual size.
func (s *LitecoinFeeCapSuite) sendRawTransaction(c *C, rw http.ResponseWriter, params []json.RawMessage) {
	c.Assert(params, HasLen, 2)
	var txHex string
	c.Assert(json.Unmarshal(params[0], &txHex), IsNil)
	var maxFeeRate float64
	c.Assert(json.Unmarshal(params[1], &maxFeeRate), IsNil)
	c.Assert(maxFeeRate, Equals, broadcastMaxFeeRate)
	raw, err := hex.DecodeString(txHex)
	c.Assert(err, IsNil)
	tx := &btcwire.MsgTx{}
	c.Assert(tx.Deserialize(bytes.NewReader(raw)), IsNil)

	fee := s.fee(c, tx)
	satsPerKvB := int64(maxFeeRate * 1e8)
	maxFee := satsPerKvB * mempool.GetTxVirtualSize(btcutil.NewTx(tx)) / 1000
	if fee > maxFee {
		_, err = fmt.Fprintf(rw, `{"result":null,"error":{"code":-25,"message":"Fee exceeds maximum configured by user (e.g. -maxtxfee, maxfeerate) (%d > %d)"},"id":1}`, fee, maxFee)
		c.Assert(err, IsNil)
		return
	}
	_, err = fmt.Fprintf(rw, `{"result":%q,"error":null,"id":1}`, tx.TxHash().String())
	c.Assert(err, IsNil)
}

// fee returns the input value minus the output value of a transaction that spends the
// suite's UTXOs.
func (s *LitecoinFeeCapSuite) fee(c *C, tx *btcwire.MsgTx) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	fee := int64(0)
	for _, in := range tx.TxIn {
		found := false
		for _, u := range s.utxos {
			if u.TxID == in.PreviousOutPoint.Hash.String() && u.Vout == in.PreviousOutPoint.Index {
				amt, err := btcutil.NewAmount(u.Amount)
				c.Assert(err, IsNil)
				fee += int64(amt)
				found = true
			}
		}
		c.Assert(found, Equals, true, Commentf("unknown input %s", in.PreviousOutPoint))
	}
	for _, out := range tx.TxOut {
		fee -= out.Value
	}
	return fee
}

// setUTXOs gives the vault n confirmed UTXOs of amountSats each.
func (s *LitecoinFeeCapSuite) setUTXOs(c *C, n int, amountSats int64) {
	sourceScript, err := s.client.getSourceScript(stypes.TxOutItem{Chain: common.LTCChain, VaultPubKey: s.client.nodePubKey})
	c.Assert(err, IsNil)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.utxos = make([]btcjson.ListUnspentResult, n)
	for i := range s.utxos {
		s.utxos[i] = btcjson.ListUnspentResult{
			TxID:          strings.ToLower(thorchain.GetRandomTxHash().String()),
			Vout:          uint32(i),
			ScriptPubKey:  hex.EncodeToString(sourceScript),
			Amount:        btcutil.Amount(amountSats).ToBTC(),
			Confirmations: 10,
			Spendable:     true,
		}
	}
}

func (s *LitecoinFeeCapSuite) TestFeeCapHoldsForFinalSignedSize(c *C) {
	vaultAddr, err := s.client.nodePubKey.GetAddress(common.LTCChain)
	c.Assert(err, IsNil)
	customerAddr, err := types2.GetRandomPubKey().GetAddress(common.LTCChain)
	c.Assert(err, IsNil)

	const utxoSats = int64(100_000_000)
	testCases := []struct {
		name       string
		inputs     int
		memo       string
		toAddress  common.Address
		amount     uint64
		maxGas     uint64
		wantChange bool
	}{
		{
			// job 16335048318: a migration sending the whole vault has no change output,
			// so a cap sized with change let 1,601,460 sats through at 133 vbytes.
			name:      "migration without change, 1 input",
			inputs:    1,
			memo:      "MIGRATE:1024",
			toAddress: customerAddr,
			amount:    uint64(utxoSats) - 2_000_000,
			maxGas:    2_000_000,
		},
		{
			name:      "migration without change, 3 inputs",
			inputs:    3,
			memo:      "MIGRATE:1024",
			toAddress: customerAddr,
			amount:    3*uint64(utxoSats) - 5_000_000,
			maxGas:    5_000_000,
		},
		{
			name:      "migration without change, 20 inputs",
			inputs:    20,
			memo:      "MIGRATE:1024",
			toAddress: customerAddr,
			amount:    20*uint64(utxoSats) - 20_000_000,
			maxGas:    20_000_000,
		},
		{
			name:       "outbound with change, 1 input",
			inputs:     1,
			memo:       "OUT:" + thorchain.GetRandomTxHash().String(),
			toAddress:  customerAddr,
			amount:     50_000_000,
			maxGas:     5_000_000,
			wantChange: true,
		},
		{
			name:       "outbound with change, 5 inputs",
			inputs:     5,
			memo:       "OUT:" + thorchain.GetRandomTxHash().String(),
			toAddress:  customerAddr,
			amount:     4*uint64(utxoSats) + 50_000_000,
			maxGas:     20_000_000,
			wantChange: true,
		},
		{
			name:      "consolidation without change, 10 inputs",
			inputs:    10,
			memo:      "consolidate",
			toAddress: vaultAddr,
			amount:    10 * uint64(utxoSats),
		},
	}

	for _, tc := range testCases {
		comment := Commentf(tc.name)
		s.setUTXOs(c, tc.inputs, utxoSats)
		txOut := stypes.TxOutItem{
			Chain:       common.LTCChain,
			ToAddress:   tc.toAddress,
			VaultPubKey: s.client.nodePubKey,
			Coins:       common.Coins{common.NewCoin(common.LTCAsset, cosmos.NewUint(tc.amount))},
			Memo:        tc.memo,
			// well above MaxSatsPerVByte, so the fee cap decides the fee
			GasRate: 20_000,
		}
		if tc.maxGas > 0 {
			txOut.MaxGas = common.Gas{common.NewCoin(common.LTCAsset, cosmos.NewUint(tc.maxGas))}
		}

		payload, _, obs, err := s.client.SignTx(txOut, 1)
		c.Assert(err, IsNil, comment)
		c.Assert(payload, NotNil, comment)

		signed := &btcwire.MsgTx{}
		c.Assert(signed.Deserialize(bytes.NewReader(payload)), IsNil, comment)
		c.Assert(signed.TxIn, HasLen, tc.inputs, comment)
		// customer + memo, plus change when there is a balance left
		wantOutputs := 2
		if tc.wantChange {
			wantOutputs++
		}
		c.Assert(signed.TxOut, HasLen, wantOutputs, comment)

		fee := s.fee(c, signed)
		vsize := mempool.GetTxVirtualSize(btcutil.NewTx(signed))
		c.Assert(fee > 0, Equals, true, comment)
		// the instant observation reports the fee the signed transaction pays
		c.Assert(obs, NotNil, comment)
		c.Check(obs.Gas[0].Amount.Uint64(), Equals, uint64(fee), comment)
		c.Check(fee <= feeCapTestMaxSatsPerVByte*vsize, Equals, true,
			Commentf("%s: fee %d exceeds %d sat/vB at final size %d vB", tc.name, fee, feeCapTestMaxSatsPerVByte, vsize))
		if tc.maxGas > 0 {
			c.Check(fee <= int64(tc.maxGas), Equals, true, Commentf("%s: fee %d exceeds max gas %d", tc.name, fee, tc.maxGas))
		}

		// Litecoin Core accepts the signed transaction under the broadcast fee-rate limit.
		txid, err := s.client.BroadcastTx(txOut, payload)
		c.Assert(err, IsNil, comment)
		c.Check(txid, Equals, signed.TxHash().String(), comment)
	}
}

// TestMaxFeeSatsSignatureSizes checks the fee cap against transactions whose final
// signatures are shorter than the estimate, with and without change. Segwit chains
// carry the signature in the discounted witness; DOGE, BCH and DASH carry it in the
// scriptSig, where every byte counts in full.
func (s *LitecoinFeeCapSuite) TestMaxFeeSatsSignatureSizes(c *C) {
	// output script contents only affect the size, so LTC scripts stand in for every chain
	customerScript, err := s.client.getSourceScript(stypes.TxOutItem{Chain: common.LTCChain, VaultPubKey: types2.GetRandomPubKey()})
	c.Assert(err, IsNil)
	changeScript, err := s.client.getSourceScript(stypes.TxOutItem{Chain: common.LTCChain, VaultPubKey: s.client.nodePubKey})
	c.Assert(err, IsNil)
	memoScripts, err := MemoToScripts("MIGRATE:1024", ltctxscript.MaxDataCarrierSize, ltctxscript.NullDataScript, ltctxscript.PayToWitnessScript)
	c.Assert(err, IsNil)

	for _, chain := range []common.Chain{common.LTCChain, common.BTCChain, common.DOGEChain, common.BCHChain, common.DASHChain} {
		s.client.cfg.ChainID = chain
		segwit := s.client.isSegwitChain()
		for _, inputs := range []int{1, 2, 5, 20, 100} {
			utxos := make([]btcjson.ListUnspentResult, inputs)
			for i := range utxos {
				utxos[i] = btcjson.ListUnspentResult{TxID: thorchain.GetRandomTxHash().String(), Vout: uint32(i)}
			}
			for _, withChange := range []bool{true, false} {
				var change []byte
				if withChange {
					change = changeScript
				}
				maxFee := s.client.maxFeeSats(utxos, memoScripts, customerScript, change)

				// feeCapSigLen..72 covers low-S DER signatures, including a 33-byte r
				for sigLen := feeCapSigLen; sigLen <= estimateSigLen; sigLen++ {
					tx := btcwire.NewMsgTx(btcwire.TxVersion)
					for _, u := range utxos {
						hash, hashErr := chainhash.NewHashFromStr(u.TxID)
						c.Assert(hashErr, IsNil)
						in := btcwire.NewTxIn(btcwire.NewOutPoint(hash, u.Vout), nil, nil)
						if segwit {
							in.Witness = btcwire.TxWitness{make([]byte, sigLen), make([]byte, 33)}
						} else {
							// <push sig> <sig> <push pubkey> <pubkey>
							in.SignatureScript = make([]byte, 1+sigLen+1+33)
						}
						tx.AddTxIn(in)
					}
					tx.AddTxOut(btcwire.NewTxOut(1, customerScript))
					if withChange {
						tx.AddTxOut(btcwire.NewTxOut(1, changeScript))
					}
					tx.AddTxOut(btcwire.NewTxOut(0, memoScripts[0]))

					vsize := mempool.GetTxVirtualSize(btcutil.NewTx(tx))
					comment := Commentf("chain=%s inputs=%d change=%t sigLen=%d", chain, inputs, withChange, sigLen)
					c.Check(maxFee <= feeCapTestMaxSatsPerVByte*vsize, Equals, true, comment)
					if sigLen == estimateSigLen {
						// the fee estimate sizes the same transaction exactly
						c.Check(s.client.estimateTxSize(utxos, memoScripts, customerScript, change), Equals, vsize, comment)
					}
				}
			}
		}
	}
}

// TestMaxFeeSatsClampedToBroadcastLimit checks that a MaxSatsPerVByte above the
// broadcast maxfeerate cannot produce a fee BroadcastTx's daemon would reject.
func (s *LitecoinFeeCapSuite) TestMaxFeeSatsClampedToBroadcastLimit(c *C) {
	script, err := s.client.getSourceScript(stypes.TxOutItem{Chain: common.LTCChain, VaultPubKey: s.client.nodePubKey})
	c.Assert(err, IsNil)
	utxos := []btcjson.ListUnspentResult{{TxID: thorchain.GetRandomTxHash().String()}}
	for _, tc := range []struct {
		chain common.Chain
		rates []int64
	}{
		{common.LTCChain, []int64{9765, 10_000, 10_000}},
		{common.BTCChain, []int64{9765, 10_000, 10_000}},
		{common.DASHChain, []int64{9765, 10_000, 10_000}},
		{common.DOGEChain, []int64{9765, 10_000, 20_000}},
		{common.BCHChain, []int64{9765, 10_000, 20_000}},
	} {
		s.client.cfg.ChainID = tc.chain
		for i, rate := range []int64{9765, 10_000, 20_000} {
			s.client.cfg.UTXO.MaxSatsPerVByte = rate
			size := s.client.estimateTxSizeWithSigLen(utxos, nil, script, nil, feeCapSigLen)
			c.Check(s.client.maxFeeSats(utxos, nil, script, nil), Equals, size*tc.rates[i],
				Commentf("chain=%s configured rate=%d", tc.chain, rate))
		}
	}
	c.Check(broadcastMaxSatsPerVByte, Equals, int64(10_000))
}
