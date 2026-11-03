package utxo

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	crypto "github.com/cometbft/cometbft/crypto"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/rs/zerolog"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	txscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type BitcoinSignerSuite struct {
	client *Client
	server *httptest.Server
	bridge thorclient.ThorchainBridge
	cfg    config.BifrostChainConfiguration
	m      *metrics.Metrics
	db     *leveldb.DB
	keys   *thorclient.Keys

	vinsUnspentFixtureAddress string
}

var _ = Suite(&BitcoinSignerSuite{})

func (s *BitcoinSignerSuite) SetUpSuite(c *C) {
	types2.SetupConfigForTest()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(bob, cKeys.English, cmd.THORChainHDPath, password, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.keys = thorclient.NewKeysWithKeybase(kb, bob, password)
}

func (s *BitcoinSignerSuite) SetUpTest(c *C) {
	s.m = GetMetricForTest(c, common.BTCChain)
	s.cfg = config.BifrostChainConfiguration{
		ChainID:     "BTC",
		UserName:    bob,
		Password:    password,
		DisableTLS:  true,
		HTTPostMode: true,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			StartBlockHeight: 1, // avoids querying thorchain for block height
		},
	}

	ns := strconv.Itoa(time.Now().Nanosecond())
	thordir := filepath.Join(os.TempDir(), ns, ".thorcli")
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      bob,
		SignerPasswd:    password,
		ChainHomeFolder: thordir,
	}

	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case req.RequestURI == thorclient.LastBlockEndpoint:
			httpTestHandler(c, rw, "../../../../test/fixtures/endpoints/lastblock/btc.json")
		case req.RequestURI == thorclient.ChainVersionEndpoint:
			_, err := rw.Write([]byte(`{"current":"` + types2.GetCurrentVersion().String() + `"}`))
			c.Assert(err, IsNil)
		default:
			r := struct {
				Method string `json:"method"`
			}{}
			buf, err := io.ReadAll(req.Body)
			c.Assert(err, IsNil)
			if len(buf) == 0 {
				return
			}
			c.Assert(json.Unmarshal(buf, &r), IsNil)
			defer func() {
				c.Assert(req.Body.Close(), IsNil)
			}()
			switch r.Method {
			case "getnetworkinfo":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/getnetworkinfo.json")
			case "getbestblockhash":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/getbestblockhash.json")
			case "getblockcount":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/blockcount.json")
			case "getblock":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/block.json")
			case "getrawtransaction":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/tx.json")
			case "getinfo":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/getinfo.json")
			case "sendrawtransaction":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/sendrawtransaction.json")
			case "importaddress":
				httpTestHandler(c, rw, "../../../../test/fixtures/btc/importaddress.json")
			case "createwallet":
				_, err = rw.Write([]byte(`{ "result": null, "error": null, "id": 1 }`))
				c.Assert(err, IsNil)
			case "listunspent":
				body := string(buf)
				switch {
				case s.vinsUnspentFixtureAddress != "" && strings.Contains(body, s.vinsUnspentFixtureAddress):
					httpTestHandler(c, rw, "../../../../test/fixtures/btc/listunspent-vins-unspent.json")
				case strings.Contains(body, "tb1qleqepvj0d9n7899qj3skd8tw7c7jvh3zlxul70"):
					httpTestHandler(c, rw, "../../../../test/fixtures/btc/listunspent-tss.json")
				default:
					httpTestHandler(c, rw, "../../../../test/fixtures/btc/listunspent.json")
				}
			}
		}
	}))
	var err error
	s.cfg.RPCHost = s.server.Listener.Addr().String()
	cfg.ChainHost = s.server.Listener.Addr().String()
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, s.keys)
	c.Assert(err, IsNil)
	s.client, err = NewClient(s.keys, s.cfg, nil, s.bridge, s.m)
	c.Assert(err, IsNil)
	storage := storage.NewMemStorage()
	db, err := leveldb.Open(storage, nil)
	c.Assert(err, IsNil)
	s.db = db
	s.client.temporalStorage, err = utxo.NewTemporalStorage(db, 0)
	c.Assert(err, IsNil)
	c.Assert(s.client, NotNil)
}

func (s *BitcoinSignerSuite) TearDownTest(c *C) {
	s.server.Close()
	c.Assert(s.db.Close(), IsNil)
}

func (s *BitcoinSignerSuite) setNodeSignerForTest(c *C) {
	priKeyBuf, err := hex.DecodeString("b404c5ec58116b5f0fe13464a92e46626fc5db130e418cbce98df86ffe9317c5")
	c.Assert(err, IsNil)
	pkey, _ := btcec.PrivKeyFromBytes(btcec.S256(), priKeyBuf)
	c.Assert(pkey, NotNil)
	s.client.nodePrivKey = pkey
	s.client.nodePubKey, err = bech32AccountPubKey(pkey)
	c.Assert(err, IsNil)
}

func newBTCConsolidateRPCServer(c *C, sendRawTxBody string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		c.Assert(err, IsNil)
		c.Assert(req.Body.Close(), IsNil)

		var rpcReq struct {
			Method string `json:"method"`
		}
		c.Assert(json.Unmarshal(body, &rpcReq), IsNil)

		switch rpcReq.Method {
		case "listunspent":
			httpTestHandler(c, rw, "../../../../test/fixtures/btc/listunspent.json")
		case "getblockcount":
			httpTestHandler(c, rw, "../../../../test/fixtures/btc/blockcount.json")
		case "sendrawtransaction":
			_, err = rw.Write([]byte(sendRawTxBody))
			c.Assert(err, IsNil)
		default:
			c.Fatalf("unexpected RPC method: %s", rpcReq.Method)
		}
	}))
}

func (s *BitcoinSignerSuite) TestGetBTCPrivateKey(c *C) {
	input := "YjQwNGM1ZWM1ODExNmI1ZjBmZTEzNDY0YTkyZTQ2NjI2ZmM1ZGIxMzBlNDE4Y2JjZTk4ZGY4NmZmZTkzMTdjNQ=="
	buf, err := base64.StdEncoding.DecodeString(input)
	c.Assert(err, IsNil)
	c.Assert(buf, NotNil)
	prikeyByte, err := hex.DecodeString(string(buf))
	c.Assert(err, IsNil)
	pk := secp256k1.GenPrivKeyFromSecret(prikeyByte)
	btcPrivateKey, _ := btcec.PrivKeyFromBytes(btcec.S256(), pk.Bytes())
	c.Assert(btcPrivateKey, NotNil)
}

func (s *BitcoinSignerSuite) TestSignTx(c *C) {
	txOutItem := stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   types2.GetRandomETHAddress(),
		VaultPubKey: types2.GetRandomPubKey(),
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10000000000)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1001)),
		},
		InHash:  "",
		OutHash: "",
	}
	// incorrect chain should return an error
	result, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// invalid pubkey should return an error
	txOutItem.Chain = common.BTCChain
	txOutItem.VaultPubKey = common.PubKey("helloworld")
	result, _, _, err = s.client.SignTx(txOutItem, 2)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// invalid to address should return an error
	txOutItem.VaultPubKey = types2.GetRandomPubKey()
	result, _, _, err = s.client.SignTx(txOutItem, 3)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	addr, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	txOutItem.ToAddress = addr

	// nothing to sign , because there is not enough UTXO
	result, _, _, err = s.client.SignTx(txOutItem, 4)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *BitcoinSignerSuite) TestVinsUnspentUsesTxIDAndVout(c *C) {
	const txID = "1111111111111111111111111111111111111111111111111111111111111111"
	vaultPubKey := types2.GetRandomPubKey()
	vaultAddr, err := vaultPubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	s.vinsUnspentFixtureAddress = vaultAddr.String()
	defer func() {
		s.vinsUnspentFixtureAddress = ""
	}()

	hash, err := chainhash.NewHashFromStr(txID)
	c.Assert(err, IsNil)

	tx := stypes.TxOutItem{
		Chain:       common.BTCChain,
		VaultPubKey: vaultPubKey,
	}

	matchingVin := wire.NewTxIn(wire.NewOutPoint(hash, 1), nil, nil)
	unspent, err := s.client.vinsUnspent(tx, []*wire.TxIn{matchingVin})
	c.Assert(err, IsNil)
	c.Assert(unspent, Equals, true)

	spentSiblingVin := wire.NewTxIn(wire.NewOutPoint(hash, 0), nil, nil)
	unspent, err = s.client.vinsUnspent(tx, []*wire.TxIn{spentSiblingVin})
	c.Assert(err, IsNil)
	c.Assert(unspent, Equals, false)
}

func (s *BitcoinSignerSuite) TestSignTxHappyPathWithPrivateKey(c *C) {
	addr, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	inHash := thorchain.GetRandomTxHash()
	memo := "OUT:" + inHash.String() // Memo must be parsable or ParseMemo will error.

	txOutItem := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   addr,
		VaultPubKey: "tthorpub1addwnpepqw2k68efthm08f0f5akhjs6fk5j2pze4wkwt4fmnymf9yd463puruhh0lyz",
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
		},
		InHash:  inHash,
		OutHash: "",
		Memo:    memo,
	}
	txHash := "256222fb25a9950479bb26049a2c00e75b89abbb7f0cf646c623b93e942c4c34"
	c.Assert(err, IsNil)
	blockMeta := utxo.NewBlockMeta("000000000000008a0da55afa8432af3b15c225cc7e04d32f0de912702dd9e2ae",
		100,
		"0000000000000068f0710c510e94bd29aa624745da43e32a1de887387306bfda")
	blockMeta.AddCustomerTransaction(txHash)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	priKeyBuf, err := hex.DecodeString("b404c5ec58116b5f0fe13464a92e46626fc5db130e418cbce98df86ffe9317c5")
	c.Assert(err, IsNil)
	pkey, _ := btcec.PrivKeyFromBytes(btcec.S256(), priKeyBuf)
	c.Assert(pkey, NotNil)
	s.client.nodePrivKey = pkey
	s.client.nodePubKey, err = bech32AccountPubKey(pkey)
	c.Assert(err, IsNil)
	txOutItem.VaultPubKey = s.client.nodePubKey
	buf, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(buf, NotNil)
}

// keysignFailKeyManager fails every remote sign with the configured blame.
type keysignFailKeyManager struct {
	errorThorchainKeyManager
	blame types2.Blame
}

func (m *keysignFailKeyManager) RemoteSign(msg []byte, algo common.SigningAlgo, poolPubKey string, thorchainHeight int64) ([]byte, []byte, error) {
	return nil, nil, tss.NewKeysignError(m.blame)
}

// TestSignTxCheckpointAfterKeysignFailure verifies an unsigned tx is only
// checkpointed once a keysign got past forming its party. A tx whose party
// never formed exposed no signature and must be rebuilt on the next attempt.
func (s *BitcoinSignerSuite) TestSignTxCheckpointAfterKeysignFailure(c *C) {
	s.client.cfg.UTXO.MaxSatsPerVByte = 100
	addr, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	inHash := thorchain.GetRandomTxHash()
	txOutItem := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   addr,
		VaultPubKey: types2.GetRandomPubKey(),
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
		},
		InHash: inHash,
		Memo:   "OUT:" + inHash.String(),
	}
	blockMeta := utxo.NewBlockMeta("000000000000008a0da55afa8432af3b15c225cc7e04d32f0de912702dd9e2ae",
		100,
		"0000000000000068f0710c510e94bd29aa624745da43e32a1de887387306bfda")
	blockMeta.AddCustomerTransaction("256222fb25a9950479bb26049a2c00e75b89abbb7f0cf646c623b93e942c4c34")
	c.Assert(s.client.temporalStorage.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)

	syncFail := &keysignFailKeyManager{blame: types2.Blame{FailReason: blame.TssSyncFail}}
	roundFail := &keysignFailKeyManager{blame: types2.Blame{FailReason: blame.TssTimeout, Round: messages.KEYSIGN2Unicast}}

	partyNotFormed := s.m.GetCounterVec(metrics.UTXOKeysignPartyNotFormed).WithLabelValues(common.BTCChain.String())
	before := testutil.ToFloat64(partyNotFormed)

	// party never formed: no checkpoint, the next attempt rebuilds the tx
	s.client.tssKeySigner = syncFail
	signed, checkpoint, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, ErrorMatches, "(?s).*signers fail to sync.*")
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(testutil.ToFloat64(partyNotFormed), Equals, before+1)

	// party formed and failed in a round: the tx is checkpointed
	s.client.tssKeySigner = roundFail
	signed, checkpoint, _, err = s.client.SignTx(txOutItem, 1)
	c.Assert(err, NotNil)
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, NotNil)

	// a checkpointed tx stays pinned even if its party later fails to form
	txOutItem.Checkpoint = checkpoint
	s.client.tssKeySigner = syncFail
	signed, retained, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, NotNil)
	c.Assert(signed, IsNil)
	c.Assert(retained, DeepEquals, checkpoint)

	// only the discard path counts
	c.Assert(testutil.ToFloat64(partyNotFormed), Equals, before+1)
}

func (s *BitcoinSignerSuite) TestSignTxWithoutPredefinedMaxGas(c *C) {
	s.client.cfg.UTXO.MaxSatsPerVByte = 100
	addr, err := types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	txOutItem := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   addr,
		VaultPubKey: "tthorpub1addwnpepqw2k68efthm08f0f5akhjs6fk5j2pze4wkwt4fmnymf9yd463puruhh0lyz",
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		},
		Memo:    "MIGRATE:101",
		GasRate: 25,
		InHash:  "",
		OutHash: "",
	}
	txHash := "256222fb25a9950479bb26049a2c00e75b89abbb7f0cf646c623b93e942c4c34"
	c.Assert(err, IsNil)
	blockMeta := utxo.NewBlockMeta("000000000000008a0da55afa8432af3b15c225cc7e04d32f0de912702dd9e2ae",
		100,
		"0000000000000068f0710c510e94bd29aa624745da43e32a1de887387306bfda")
	blockMeta.AddCustomerTransaction(txHash)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	priKeyBuf, err := hex.DecodeString("b404c5ec58116b5f0fe13464a92e46626fc5db130e418cbce98df86ffe9317c5")
	c.Assert(err, IsNil)
	pkey, _ := btcec.PrivKeyFromBytes(btcec.S256(), priKeyBuf)
	c.Assert(pkey, NotNil)
	s.client.nodePrivKey = pkey
	s.client.nodePubKey, err = bech32AccountPubKey(pkey)
	c.Assert(err, IsNil)
	txOutItem.VaultPubKey = s.client.nodePubKey
	buf, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(buf, NotNil)

	c.Assert(s.client.temporalStorage.UpsertTransactionFee(0.001, 10), IsNil)
	buf, _, _, err = s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(buf, NotNil)
}

func (s *BitcoinSignerSuite) TestBroadcastTx(c *C) {
	txOutItem := stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   types2.GetRandomETHAddress(),
		VaultPubKey: types2.GetRandomPubKey(),
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1)),
		},
		InHash:  "",
		OutHash: "",
	}
	input := []byte("hello world")
	_, err := s.client.BroadcastTx(txOutItem, input)
	c.Assert(err, NotNil)
	input1, err := hex.DecodeString("01000000000103c7d45551ff54354be6711396560348ebbf273b989b542be36645568ed1dbecf10000000000ffffffff951ed70edc0bf2a4b3e1cbfe55d191a72850c5595c381309f69fc084c9af0b540100000000ffffffffc5db14c562b96bfd95f97d74a558a3e3b91841a96e1b09546208c9fb67424f420000000000ffffffff02231710000000000016001417acb08a31369e7666d94664d7e64f0e048220900000000000000000176a1574686f72636861696e3a636f6e736f6c6964617465024730440220756d15a363b78b070b583dfc1a6aba0dd605550407d5d3d92f5e785ef7e42aca02200db19dab144033c9c353481be30469da42c0c0a7580a513f49282bea77d7a29301210223da2ff73fa9b2258d335a4e63a4e7ef88211b8e800588280ed8b51e285ec0ff02483045022100a695f0fece36de02212b10bf6aa2f06dc6ef84ba30cae0c78749deddba1574530220315b490111c830c27e6cb810559c2a37cd00b123de82df79e061df26c8deb14301210223da2ff73fa9b2258d335a4e63a4e7ef88211b8e800588280ed8b51e285ec0ff0247304402207e586439b04985a90a53cf9fc511a53d86acece57b3e5571118562449d4f27ac02206d84f0fba1a68cf55efc8a1c2ec768924479b97ceaf2029ed6941176f004bf8101210223da2ff73fa9b2258d335a4e63a4e7ef88211b8e800588280ed8b51e285ec0ff00000000")
	c.Assert(err, IsNil)
	_, err = s.client.BroadcastTx(txOutItem, input1)
	c.Assert(err, IsNil)
}

// TestBuildConsolidateTxOutItemSumsUtxosInSats verifies consolidation sums each UTXO
// in sats so the resulting amount is not affected by float accumulation drift.
func (s *BitcoinSignerSuite) TestBuildConsolidateTxOutItemSumsUtxosInSats(c *C) {
	s.client.lastFeeRate.Store(10)
	vault := types2.Vault{PubKey: types2.GetRandomPubKey()}

	txOutItem, err := s.client.buildConsolidateTxOutItem(vault, []btcjson.ListUnspentResult{
		{Amount: 0.1},
		{Amount: 0.2},
		{Amount: 0.3},
	})
	c.Assert(err, IsNil)
	c.Assert(txOutItem.Memo, Equals, "consolidate")
	c.Assert(txOutItem.GasRate, Equals, int64(15))
	c.Assert(txOutItem.Coins.GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(60000000))
}

// TestConsolidateVaultUTXOsSkipsBelowThreshold verifies consolidation exits early
// when the number of available UTXOs is below the configured trigger threshold.
func (s *BitcoinSignerSuite) TestConsolidateVaultUTXOsSkipsBelowThreshold(c *C) {
	s.client.cfg.UTXO.MaxUTXOsToSpend = 10
	vault := types2.Vault{PubKey: s.client.nodePubKey}
	cacheKey := stypes.BroadcastCacheKey(vault.PubKey.String(), s.client.GetChain().String())

	err := s.client.consolidateVaultUTXOs(vault, 3, 0)
	c.Assert(err, IsNil)

	latestTx, err := s.client.signerCacheManager.GetLatestRecordedTx(cacheKey)
	c.Assert(latestTx, Equals, "")
	c.Assert(errors.Is(err, leveldb.ErrNotFound), Equals, true)
}

type errorThorchainKeyManager struct{}

func (m *errorThorchainKeyManager) GetPrivKey() crypto.PrivKey {
	return nil
}

func (m *errorThorchainKeyManager) GetAddr() sdktypes.AccAddress {
	return nil
}

func (m *errorThorchainKeyManager) ExportAsMnemonic() (string, error) {
	return "", nil
}

func (m *errorThorchainKeyManager) ExportAsPrivateKey() (string, error) {
	return "", nil
}

func (m *errorThorchainKeyManager) ExportAsKeyStore(password string) (*tss.EncryptedKeyJSON, error) {
	return nil, nil
}

func (m *errorThorchainKeyManager) RemoteSign(msg []byte, algo common.SigningAlgo, poolPubKey string, thorchainHeight int64) ([]byte, []byte, error) {
	return nil, nil, errors.New("mock remote sign failure")
}

func (m *errorThorchainKeyManager) Start() {}
func (m *errorThorchainKeyManager) Stop()  {}

// TestConsolidateVaultUTXOsReturnsSignError verifies consolidate wraps and returns
// signing failures before any broadcast attempt is made.
func (s *BitcoinSignerSuite) TestConsolidateVaultUTXOsReturnsSignError(c *C) {
	s.client.cfg.UTXO.MaxUTXOsToSpend = 10
	s.client.tssKeySigner = &errorThorchainKeyManager{}
	vault := types2.Vault{PubKey: types2.GetRandomPubKey()}

	err := s.client.consolidateVaultUTXOs(vault, 2, 0)
	c.Assert(err, ErrorMatches, "(?s)sign consolidate txout item: .*mock remote sign failure.*")
}

// TestConsolidateVaultUTXOsReturnsBroadcastError verifies consolidate wraps and returns
// broadcast failures after a successful sign.
func (s *BitcoinSignerSuite) TestConsolidateVaultUTXOsReturnsBroadcastError(c *C) {
	s.client.cfg.UTXO.MaxUTXOsToSpend = 10
	s.setNodeSignerForTest(c)
	vault := types2.Vault{PubKey: s.client.nodePubKey}

	rpcServer := newBTCConsolidateRPCServer(
		c,
		`{"result":null,"error":{"code":-25,"message":"broadcast failed"},"id":1}`,
	)
	defer rpcServer.Close()

	testRPC, err := rpc.NewClient(
		rpcServer.Listener.Addr().String(),
		s.cfg.UserName,
		s.cfg.Password,
		0,
		time.Second,
		common.BTCChain,
		s.client.log,
	)
	c.Assert(err, IsNil)

	originalRPC := s.client.rpc
	s.client.rpc = testRPC
	defer func() {
		s.client.rpc = originalRPC
	}()

	err = s.client.consolidateVaultUTXOs(vault, 2, 0)
	c.Assert(err, ErrorMatches, "broadcast consolidate tx: fail to broadcast transaction to chain: .*broadcast failed.*")
}

func (s *BitcoinSignerSuite) TestIsSelfTransaction(c *C) {
	c.Check(s.client.isSelfTransaction("66d2d6b5eb564972c59e4797683a1225a02515a41119f0a8919381236b63e948"), Equals, false)
	bm := utxo.NewBlockMeta("", 1024, "")
	hash := "66d2d6b5eb564972c59e4797683a1225a02515a41119f0a8919381236b63e948"
	bm.AddSelfTransaction(hash)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(1024, bm), IsNil)
	c.Check(s.client.isSelfTransaction("66d2d6b5eb564972c59e4797683a1225a02515a41119f0a8919381236b63e948"), Equals, true)
}

func (s *BitcoinSignerSuite) TestEstimateTxSize(c *C) {
	txs := []btcjson.ListUnspentResult{
		{
			TxID:      "66d2d6b5eb564972c59e4797683a1225a02515a41119f0a8919381236b63e948",
			Vout:      0,
			Spendable: true,
		},
		{
			TxID:      "c5946215d82d5870ba2b1e8f245d8aa1446783975aa3a592cf55589fccbf285f",
			Vout:      0,
			Spendable: true,
		},
	}
	const memo = "OUT:2180B871F2DEA2546E1403DBFE9C26B062ABAFFD979CF3A65F2B4D2230105CF1"
	nullDataScripts, err := MemoToScripts(memo, txscript.MaxDataCarrierSize, txscript.NullDataScript, txscript.PayToWitnessScript)
	c.Assert(err, IsNil)
	addr, err := btcutil.DecodeAddress("tb1qleqepvj0d9n7899qj3skd8tw7c7jvh3zlxul70", &btcchaincfg.MainNetParams)
	c.Assert(err, IsNil)
	buf, err := txscript.PayToAddrScript(addr)
	c.Assert(err, IsNil)
	size := s.client.estimateTxSize(txs, nullDataScripts, buf, buf)
	c.Assert(size, Equals, int64(288))
	const longMemo = "OUT:2180B871F2DEA2546E1403DBFE9C26B062ABAFFD979CF3A65F2B4D2230105CF12180B871F2DEA2546E1403DBFE9C26B062ABAFFD979CF3A65F2B4D2230105CF12180B871F2DEA2546E1403DBFE9C26B062ABAFFD979CF3A65F2B4D2230105CF1"
	nullDataScripts, err = MemoToScripts(longMemo, txscript.MaxDataCarrierSize, txscript.NullDataScript, txscript.PayToWitnessScript)
	c.Assert(err, IsNil)
	size = s.client.estimateTxSize(txs, nullDataScripts, buf, buf)
	c.Assert(size, Equals, int64(487))
}

func (s *BitcoinSignerSuite) TestSignTxWithAddressPubkey(c *C) {
	txOutItem := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   "04ae1a62fe09c5f51b13905f07f06b99a2f7159b2225f374cd378d71302fa28414e7aab37397f554a7df5f142c21c1b7303b8a0626f1baded5c72a704f7e6cd84c",
		VaultPubKey: "tthorpub1addwnpepqw2k68efthm08f0f5akhjs6fk5j2pze4wkwt4fmnymf9yd463puruhh0lyz",
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
		},
		InHash:  "",
		OutHash: "",
	}
	txHash := "256222fb25a9950479bb26049a2c00e75b89abbb7f0cf646c623b93e942c4c34"
	blockMeta := utxo.NewBlockMeta("000000000000008a0da55afa8432af3b15c225cc7e04d32f0de912702dd9e2ae",
		100,
		"0000000000000068f0710c510e94bd29aa624745da43e32a1de887387306bfda")
	blockMeta.AddCustomerTransaction(txHash)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	priKeyBuf, err := hex.DecodeString("b404c5ec58116b5f0fe13464a92e46626fc5db130e418cbce98df86ffe9317c5")
	c.Assert(err, IsNil)
	pkey, _ := btcec.PrivKeyFromBytes(btcec.S256(), priKeyBuf)
	c.Assert(pkey, NotNil)
	s.client.nodePrivKey = pkey
	s.client.nodePubKey, err = bech32AccountPubKey(pkey)
	c.Assert(err, IsNil)
	txOutItem.VaultPubKey = s.client.nodePubKey
	// The transaction will not signed, but ignored instead
	buf, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(buf, IsNil)
}

func (s *BitcoinSignerSuite) TestToAddressCanNotRoundTripShouldBlock(c *C) {
	txOutItem := stypes.TxOutItem{
		Chain:       common.BTCChain,
		ToAddress:   "05ae1a62fe09c5f51b13905f07f06b99a2f7159b2225f374cd378d71302fa28414e7aab37397f554a7df5f142c21c1b7303b8a0626f1baded5c72a704f7e6cd84c",
		VaultPubKey: "tthorpub1addwnpepqw2k68efthm08f0f5akhjs6fk5j2pze4wkwt4fmnymf9yd463puruhh0lyz",
		Coins: common.Coins{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(10)),
		},
		MaxGas: common.Gas{
			common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
		},
		InHash:  "",
		OutHash: "",
	}
	txHash := "256222fb25a9950479bb26049a2c00e75b89abbb7f0cf646c623b93e942c4c34"
	blockMeta := utxo.NewBlockMeta("000000000000008a0da55afa8432af3b15c225cc7e04d32f0de912702dd9e2ae",
		100,
		"0000000000000068f0710c510e94bd29aa624745da43e32a1de887387306bfda")
	blockMeta.AddCustomerTransaction(txHash)
	c.Assert(s.client.temporalStorage.SaveBlockMeta(blockMeta.Height, blockMeta), IsNil)
	priKeyBuf, err := hex.DecodeString("b404c5ec58116b5f0fe13464a92e46626fc5db130e418cbce98df86ffe9317c5")
	c.Assert(err, IsNil)
	pkey, _ := btcec.PrivKeyFromBytes(btcec.S256(), priKeyBuf)
	c.Assert(pkey, NotNil)
	s.client.nodePrivKey = pkey
	s.client.nodePubKey, err = bech32AccountPubKey(pkey)
	c.Assert(err, IsNil)
	txOutItem.VaultPubKey = s.client.nodePubKey
	// The transaction will not signed, but ignored instead
	buf, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(buf, IsNil)
}

// A negative relayFee makes getnetworkinfo fail, including on a fresh client.
func feeFloorTestRPC(c *C, chain common.Chain, relayFee float64, utxos ...btcjson.ListUnspentResult) (*rpc.Client, func()) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		c.Assert(json.NewDecoder(req.Body).Decode(&request), IsNil)
		var result any
		var rpcErr *btcjson.RPCError
		switch request.Method {
		case "getnetworkinfo":
			if relayFee < 0 {
				rpcErr = &btcjson.RPCError{Code: -28, Message: "daemon warming up"}
			} else {
				result = btcjson.GetNetworkInfoResult{RelayFee: relayFee}
			}
		case "listunspent":
			result = utxos
		case "getblockhash":
			result = fakeTxID(2)
		case "getblockstats":
			result = btcjson.GetBlockStatsResult{AverageFeeRate: 1}
		case "getblockcount":
			result = 100
		default:
			rpcErr = &btcjson.RPCError{Code: -32601, Message: "unexpected method: " + request.Method}
		}
		rw.Header().Set("Content-Type", "application/json")
		c.Check(json.NewEncoder(rw).Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID, "result": result, "error": rpcErr,
		}), IsNil)
	}))
	client, err := rpc.NewClient(server.URL, bob, password, 0, 0, chain, zerolog.Nop())
	c.Assert(err, IsNil)
	return client, server.Close
}

func (s *BitcoinSignerSuite) btcFeeFloorFixture(c *C) (stypes.TxOutItem, btcjson.ListUnspentResult, []byte) {
	s.setNodeSignerForTest(c)
	s.client.cfg.UTXO.MaxSatsPerVByte = 9765
	vault := s.client.nodePubKey
	tx := stypes.TxOutItem{
		Chain:       common.BTCChain,
		VaultPubKey: vault,
		Coins:       common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10_000))),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(2_000))},
		GasRate:     3,
		InHash:      common.TxID(strings.Repeat("ab", 32)),
	}
	tx.Memo = "OUT:" + tx.InHash.String()
	var err error
	tx.ToAddress, err = types2.GetRandomPubKey().GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	sourceScript, err := s.client.getSourceScript(tx)
	c.Assert(err, IsNil)
	vaultAddress, err := vault.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	input := btcjson.ListUnspentResult{
		TxID: fakeTxID(1), Address: vaultAddress.String(), ScriptPubKey: hex.EncodeToString(sourceScript),
		Amount: btcutil.Amount(100_000).ToBTC(), Confirmations: 10, Spendable: true,
	}
	return tx, input, sourceScript
}

func (s *BitcoinSignerSuite) TestBTCFeeFloorTransactionAndSighash(c *C) {
	tx, input, script := s.btcFeeFloorFixture(c)
	for _, tc := range []struct {
		name    string
		gasRate int64
		maxGas  uint64
	}{
		{"below floor", 3, 2_000},
		{"above floor", 10, 10_000},
		{"MaxGas below floor", 3, 600},
		{"no MaxGas", 3, 0},
	} {
		tx.GasRate = tc.gasRate
		tx.MaxGas = nil
		if tc.maxGas > 0 {
			tx.MaxGas = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(tc.maxGas))}
		}
		var legacyBytes, legacySighash []byte
		// Old Core default, 29.1+ default, custom policy, zero and RPC failure.
		for _, relayFee := range []float64{0.00001, 0.000001, 0.00002, 0, -1} {
			var closeRPC func()
			s.client.rpc, closeRPC = feeFloorTestRPC(c, common.BTCChain, relayFee, input)
			defer closeRPC()
			s.client.minRelayFeeSats.Store(0)
			s.client.updateNetworkInfo()
			redeemTx, _, err := s.client.buildTx(tx, script, 0)
			c.Assert(err, IsNil, Commentf("%s, relay fee %g", tc.name, relayFee))
			var serialized bytes.Buffer
			c.Assert(redeemTx.Serialize(&serialized), IsNil)
			sighash, err := txscript.CalcWitnessSigHash(script, txscript.NewTxSigHashes(redeemTx),
				txscript.SigHashAll, redeemTx, 0, 100_000)
			c.Assert(err, IsNil)
			c.Assert(sighash, HasLen, 32)
			if legacyBytes == nil {
				legacyBytes, legacySighash = serialized.Bytes(), sighash
			}
			c.Check(serialized.Bytes(), DeepEquals, legacyBytes, Commentf("%s, relay fee %g", tc.name, relayFee))
			c.Check(sighash, DeepEquals, legacySighash, Commentf("%s, relay fee %g", tc.name, relayFee))
			fee := checkSpendsAmountPlusFee(c, redeemTx, 100_000, 10_000, script)
			switch tc.name {
			case "above floor":
				memoScripts, memoErr := MemoToScripts(tx.Memo, txscript.MaxDataCarrierSize, txscript.NullDataScript, txscript.PayToWitnessScript)
				c.Assert(memoErr, IsNil)
				size := s.client.estimateTxSize([]btcjson.ListUnspentResult{input}, memoScripts, redeemTx.TxOut[0].PkScript, script)
				c.Check(fee, Equals, tc.gasRate*size)
				c.Assert(fee > 1_000, Equals, true)
			case "MaxGas below floor":
				c.Check(fee, Equals, int64(600))
			default:
				c.Check(fee, Equals, int64(1_000))
			}
		}
	}
}

func (s *BitcoinSignerSuite) TestBTCFeeFloorNetworkFeeReport(c *C) {
	s.client.cfg.UTXO.EstimatedAverageTxSize = 250
	s.client.cfg.UTXO.MinSatsPerVByte = 1
	s.client.cfg.BlockScanner.GasCacheBlocks = 0
	for _, relayFee := range []float64{0.00001, 0.000001, 0.00002, 0, -1} {
		var closeRPC func()
		s.client.rpc, closeRPC = feeFloorTestRPC(c, common.BTCChain, relayFee)
		defer closeRPC()
		s.client.minRelayFeeSats.Store(0)
		s.client.lastFeeRate.Store(0)
		reports := make(chan common.NetworkFee, 1)
		s.client.globalNetworkFeeQueue = reports
		s.client.updateNetworkInfo()
		c.Assert(s.client.sendNetworkFee(100), IsNil)
		select {
		case report := <-reports:
			c.Check(report, DeepEquals, common.NetworkFee{
				Chain: common.BTCChain, Height: 100, TransactionSize: 250, TransactionRate: 4,
			}, Commentf("relay fee %g", relayFee))
		default:
			c.Fatal("network fee report was not queued")
		}
	}
}

func (s *BitcoinSignerSuite) TestBTCFeeFloorBoundaries(c *C) {
	s.client.minRelayFeeSats.Store(0)
	s.client.updateNetworkInfo()
	tx := stypes.TxOutItem{Chain: common.BTCChain}
	memo := mem.NewOutboundMemo(common.TxID(strings.Repeat("ab", 32)))
	for _, tc := range []struct{ fee, want uint64 }{
		{0, 1_000}, {100, 1_000}, {999, 1_000}, {1_000, 1_000}, {1_001, 1_001},
	} {
		fee, customer := s.client.applyFeeLimits(tx, memo, tc.fee, 10_000, 10_000)
		c.Check(fee, Equals, tc.want)
		c.Check(customer, Equals, uint64(10_000))
	}
}

func (s *BitcoinSignerSuite) TestBTCFeeFloorKeepsOtherChainPolicy(c *C) {
	for _, chain := range []common.Chain{common.LTCChain, common.BCHChain, common.DOGEChain, common.ZECChain} {
		s.client.cfg.ChainID = chain
		for _, tc := range []struct {
			relay float64
			want  uint64
		}{
			{0.00001, 1_000}, {0.000001, 100}, {0.00002, 2_000}, {-1, 2_000},
		} {
			var closeRPC func()
			s.client.rpc, closeRPC = feeFloorTestRPC(c, chain, tc.relay)
			defer closeRPC()
			s.client.updateNetworkInfo()
			c.Check(s.client.minRelayFeeSats.Load(), Equals, tc.want, Commentf("%s relay %g", chain, tc.relay))
		}
	}
}

func (s *BitcoinSignerSuite) TestBTCFeeFloorPreservesCheckpoint(c *C) {
	tx, input, script := s.btcFeeFloorFixture(c)
	var closeRPC func()
	s.client.rpc, closeRPC = feeFloorTestRPC(c, common.BTCChain, 0.000001, input)
	defer closeRPC()

	// Construct a checkpoint at the old, daemon-dependent 100-sat floor.
	s.client.minRelayFeeSats.Store(100)
	unsigned, amounts, err := s.client.buildTx(tx, script, 0)
	c.Assert(err, IsNil)
	c.Assert(checkSpendsAmountPlusFee(c, unsigned, 100_000, 10_000, script) < 1_000, Equals, true)
	var serialized bytes.Buffer
	c.Assert(unsigned.Serialize(&serialized), IsNil)
	tx.Checkpoint, err = json.Marshal(utxo.SignCheckpoint{
		UnsignedTx: serialized.Bytes(), IndividualAmounts: amounts,
	})
	c.Assert(err, IsNil)

	// Refresh the fixed policy, then retry through SignTx. A possibly signed
	// checkpoint must keep its original bytes rather than adopt the new floor.
	s.client.updateNetworkInfo()
	c.Assert(s.client.minRelayFeeSats.Load(), Equals, uint64(1_000))
	s.client.nodePubKey = types2.GetRandomPubKey()
	s.client.tssKeySigner = &keysignFailKeyManager{
		blame: types2.Blame{FailReason: blame.TssTimeout, Round: messages.KEYSIGN2Unicast},
	}
	signed, retained, _, err := s.client.SignTx(tx, 100)
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), blame.TssTimeout), Equals, true)
	c.Assert(signed, IsNil)
	c.Assert(retained, DeepEquals, tx.Checkpoint)
}
