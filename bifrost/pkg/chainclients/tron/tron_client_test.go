package tron

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/syndtr/goleveldb/leveldb"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"

	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TronTestSuite struct {
	tmpdir  string
	metrics *metrics.Metrics
	client  *TronClient
	api     *httptest.Server
	rpc     *httptest.Server
}

// mockBridge is a mock implementation of ThorchainBridge for testing
type mockBridge struct {
	thorclient.ThorchainBridge
	// skipCacheMimir is returned by GetMimir; set >0 to open the SkipSignerCache gate.
	skipCacheMimir int64
}

func (m *mockBridge) GetMimir(_ string) (int64, error) {
	return m.skipCacheMimir, nil
}

func (m *mockBridge) GetBlockTimestamp(height int64) (time.Time, error) {
	// Return a fixed timestamp that matches the test's refBlock
	return time.Unix(0, 1749121581000*int64(time.Millisecond)), nil
}

var _ = Suite(&TronTestSuite{})

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.TRONChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *TronTestSuite) SetUpSuite(c *C) {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount("sthor", "sthorpub")

	s.api = api.NewMockServer()
	c.Assert(s.api, NotNil)

	s.rpc = rpc.NewMockServer()
	c.Assert(s.rpc, NotNil)

	s.metrics = GetMetricForTest(c)
	c.Assert(s.metrics, NotNil)
	ns := strconv.Itoa(time.Now().Nanosecond())
	c.Assert(os.Setenv("NET", "stagenet"), IsNil)

	s.tmpdir = filepath.Join(os.TempDir(), ns, ".thorcli")

	clientConfig := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: s.tmpdir,
	}

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.TRONChain,
		APIHost: s.api.URL,
		RPCHost: s.rpc.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			HTTPRequestTimeout: time.Second * 2,
			WhitelistTokens: []string{
				"TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			},
		},
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)

	kb := keyring.NewInMemory(codec.NewProtoCodec(registry))
	_, _, err := kb.NewMnemonic(
		clientConfig.SignerName,
		keyring.English,
		cmd.THORChainHDPath,
		clientConfig.SignerPasswd,
		hd.Secp256k1,
	)
	c.Assert(err, IsNil)

	keys := thorclient.NewKeysWithKeybase(
		kb, clientConfig.SignerName, clientConfig.SignerPasswd,
	)
	c.Assert(err, IsNil)

	localKeyManager, err := NewLocalKeyManager(keys)
	c.Assert(err, IsNil)

	// Use mock bridge for tests to avoid needing a real THORChain node
	bridge := &mockBridge{}

	whitelist := map[string]tokenlist.ERC20Token{
		"TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs": {
			Name:     "USDT",
			Symbol:   "USDT",
			Address:  "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			Decimals: 6,
		},
	}

	pubkeyMgr := &pubkeymanager.MockPoolAddressValidator{}

	scanner, err := NewTronBlockScanner(
		chainConfig, bridge, pubkeyMgr, func(int64) error { return nil },
	)
	c.Assert(err, IsNil)

	// the mock block from getnowblock.json
	scanner.refBlocks = []RefBlock{{
		Timestamp: 1749121581000,
		Height:    55088560,
		Id:        "00000000034895b0e5029588a71f9689fb5c31e2c66989389d6ff09548726f6e",
	}}

	storage, err := blockscanner.NewBlockScannerStorage(s.tmpdir, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	c.Assert(scanner, NotNil)

	signerCacheManager, err := signercache.NewSignerCacheManager(
		storage.GetInternalDb(),
	)
	c.Assert(err, IsNil)

	s.client = &TronClient{
		chainId:            common.TRONChain.String(),
		config:             chainConfig,
		bridge:             bridge,
		whitelist:          whitelist,
		api:                api.NewTronApi(chainConfig.APIHost, chainConfig.BlockScanner.HTTPRequestTimeout),
		rpc:                rpc.NewTronRpc(chainConfig.RPCHost, chainConfig.BlockScanner.HTTPRequestTimeout),
		tronScanner:        scanner,
		localKeyManager:    localKeyManager,
		signerCacheManager: signerCacheManager,
	}

	s.client.abi, err = abi.JSON(bytes.NewReader(trc20ContractABI))
	c.Assert(err, IsNil)
}

func (s *TronTestSuite) TearDownSuite(c *C) {
	c.Assert(os.Unsetenv("NET"), IsNil)
	if err := os.RemoveAll(s.tmpdir); err != nil {
		c.Error(err)
	}
}

// TestGetGasEstimateForSolvency verifies the TRON scanner fee is converted
// from sun into THORChain 1e8 units.
func (s *TronTestSuite) TestGetGasEstimateForSolvency(c *C) {
	client := &TronClient{
		config: config.BifrostChainConfiguration{
			ChainID: common.TRONChain,
		},
		tronScanner: &TronBlockScanner{},
	}
	client.tronScanner.currentFee.Store(42_000)

	got := client.GetGasEstimateForSolvency()
	c.Assert(got.Equal(cosmos.NewUint(4_200_000)), Equals, true)
}

func (s *TronTestSuite) TestGetAddress(c *C) {
	pubKey := common.PubKey("sthorpub1addwnpepqf72ur2e8zk8r5augtrly40cuy94f7e663zh798tyms6pu2k8qdswf4es66")
	address := s.client.GetAddress(pubKey)
	c.Assert(address, Equals, "TJWs2NWfr4y61S6FbUYhUBJmMhK1SYaEFi")

	address = s.client.GetAddress("nonsense")
	c.Assert(address, Equals, "")
}

func (s *TronTestSuite) TestGetAccountByAddress(c *C) {
	address := "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM"

	account, err := s.client.GetAccountByAddress(address, nil)
	c.Assert(err, IsNil)
	c.Assert(len(account.Coins), Equals, 2)
	missing := 0
	for _, coin := range account.Coins {
		switch coin.Asset.Symbol.String() {
		case "TRX":
			c.Assert(coin.Amount.String(), Equals, "212331370000")
		case "USDT-TG3XXYEXBKPP9NZDAJDZSOZEU4BKASJOZS":
			c.Assert(coin.Amount.String(), Equals, "351560000000")
		default:
			missing++
		}
	}
	c.Assert(missing, Equals, 0)
}

func (s *TronTestSuite) TestGetConfirmationCountAndBlocks(c *C) {
	c.Assert(s.client.GetConfirmationCount(types.TxIn{}), Equals, int64(0))
	c.Assert(ConfirmationBlocks, Equals, int64(1))
}

func (s *TronTestSuite) TestGetConfig(c *C) {
	cfg := s.client.GetConfig()
	c.Assert(cfg, NotNil)
}

func (s *TronTestSuite) TestSignAndBroadcastTx(c *C) {
	coins := []common.Coin{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(150_000_000_000),
		Decimals: 6,
	}}

	gas := common.Gas{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(1_100_000),
		Decimals: 6,
	}}

	txOutItem := types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM",
		VaultPubKey: s.client.localKeyManager.Pubkey(),
		Coins:       coins,
		MaxGas:      gas,
		GasRate:     1,
	}

	//
	signed, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)

	var tx api.Transaction
	err = json.Unmarshal(signed, &tx)
	c.Assert(err, IsNil)

	// values used from scanner.refBlocks (api/test-tron/getnowblock.json)
	c.Assert(tx.RawData.RefBlockBytes, Equals, "95b0")
	c.Assert(tx.RawData.RefBlockHash, Equals, "e5029588a71f9689")
	c.Assert(tx.RawData.Timestamp, Equals, int64(1749121581000))
	c.Assert(tx.RawData.Expiration, Equals, 1749121581000+TimestampValidity.Milliseconds())
	c.Assert(tx.RawData.Contract[0].Type, Equals, "TransferContract")
	c.Assert(len(tx.Signature), Equals, 1)
	c.Assert(len(tx.Signature[0]), Equals, 130)

	txId, err := s.client.BroadcastTx(txOutItem, signed)
	c.Assert(err, IsNil)
	// tx id returned by api/test-tron/broadcasttransaction.json
	c.Assert(txId, Equals, "77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299")
}

func (s *TronTestSuite) TestSignTxReSignsWhenCachedBroadcastIsMissingFromChain(c *C) {
	coins := []common.Coin{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(150_000_000_000),
		Decimals: 6,
	}}

	gas := common.Gas{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(1_100_000),
		Decimals: 6,
	}}

	txOutItem := types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM",
		VaultPubKey: s.client.localKeyManager.Pubkey(),
		Coins:       coins,
		MaxGas:      gas,
		GasRate:     1,
	}

	// Expired cache entry + operational mimir set: verifyFn checks the recorded
	// broadcast on chain; "missing-txid" is absent (gettransactionbyid returns {}),
	// so the cache is cleared and the tx re-signs.
	mb, ok := s.client.bridge.(*mockBridge)
	c.Assert(ok, Equals, true)
	mb.skipCacheMimir = 1
	defer func() { mb.skipCacheMimir = 0 }()
	err := s.client.signerCacheManager.SetSignedWithExpiry(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(s.client.GetChain()),
		"missing-txid",
		time.Now().Add(-time.Hour).UnixMilli(),
	)
	c.Assert(err, IsNil)

	signed, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)
}

func (s *TronTestSuite) TestSignTxSkipsWhenCachedBroadcastStillExistsOnChain(c *C) {
	coins := []common.Coin{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(150_000_000_000),
		Decimals: 6,
	}}

	gas := common.Gas{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(1_100_000),
		Decimals: 6,
	}}

	txOutItem := types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM",
		VaultPubKey: s.client.localKeyManager.Pubkey(),
		Coins:       coins,
		MaxGas:      gas,
		GasRate:     1,
	}

	err := s.client.signerCacheManager.SetSigned(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(s.client.GetChain()),
		"77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299",
	)
	c.Assert(err, IsNil)

	signed, checkpoint, txIn, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(txIn, IsNil)
}

func (s *TronTestSuite) TestSignTxRespectsExpiredCacheWhenBroadcastPresent(c *C) {
	coins := []common.Coin{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(150_000_000_000),
		Decimals: 6,
	}}

	gas := common.Gas{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(1_100_000),
		Decimals: 6,
	}}

	txOutItem := types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM",
		VaultPubKey: s.client.localKeyManager.Pubkey(),
		Coins:       coins,
		MaxGas:      gas,
		GasRate:     1,
	}

	// Expired cache entry + mimir set, but the recorded broadcast IS on chain
	// (gettransactionbyid returns a populated tx) → verifyFn LookupFound → respect
	// the cache and skip, avoiding a double-spend.
	mb, ok := s.client.bridge.(*mockBridge)
	c.Assert(ok, Equals, true)
	mb.skipCacheMimir = 1
	defer func() { mb.skipCacheMimir = 0 }()
	err := s.client.signerCacheManager.SetSignedWithExpiry(
		txOutItem.CacheHash(),
		txOutItem.CacheVault(s.client.GetChain()),
		"77ddfa7093cc5f745c0d3a54abb89ef070f983343c05e0f89e5a52f3e5401299",
		time.Now().Add(-time.Hour).UnixMilli(),
	)
	c.Assert(err, IsNil)

	signed, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, IsNil)
}

func (s *TronTestSuite) TestSignTxUsesOriginalMemoForMemolessOutbounds(c *C) {
	coins := []common.Coin{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(150_000_000_100),
		Decimals: 6,
	}}

	gas := common.Gas{{
		Asset:    common.TRXAsset,
		Amount:   cosmos.NewUint(1_100_000),
		Decimals: 6,
	}}

	outboundMemo := "OUT:" + strings.Repeat("A", 64)
	txOutItem := types.TxOutItem{
		Chain:        common.TRONChain,
		ToAddress:    "TU6nEM4GTca2L5AuDTnY1qp1rkQ2t8NxvM",
		VaultPubKey:  s.client.localKeyManager.Pubkey(),
		Coins:        coins,
		Memo:         "",
		OriginalMemo: outboundMemo,
		MaxGas:       gas,
		GasRate:      1,
	}

	signed, _, obs, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)
	c.Assert(obs, NotNil)
	c.Assert(obs.Memo, Equals, outboundMemo)
	c.Assert(obs.Gas, HasLen, 1)
	c.Assert(obs.Gas[0].Amount.Uint64(), Equals, uint64(100))
	c.Assert(obs.Gas[0].Decimals, Equals, int64(6))

	var tx api.Transaction
	err = json.Unmarshal(signed, &tx)
	c.Assert(err, IsNil)
	c.Assert(tx.RawData.Data, Equals, hex.EncodeToString([]byte(outboundMemo)))
}

func (s *TronTestSuite) checkpointTestTxOut(amount uint64) types.TxOutItem {
	return types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
		VaultPubKey: s.client.localKeyManager.Pubkey(),
		Coins: common.Coins{{
			Asset: common.Asset{
				Chain:  common.TRONChain,
				Symbol: "USDT-TG3XXYEXBKPP9NZDAJDZSOZEU4BKASJOZS",
				Ticker: "USDT",
			},
			Amount:   cosmos.NewUint(amount),
			Decimals: 6,
		}},
		Memo: "OUT:tron-checkpoint-retry",
		MaxGas: common.Gas{{
			Asset:    common.TRXAsset,
			Amount:   cosmos.NewUint(4_000_000),
			Decimals: 6,
		}},
		GasRate: 1,
		Height:  1,
	}
}

func (s *TronTestSuite) TestSignTxReusesExactCheckpoint(c *C) {
	txOutItem := s.checkpointTestTxOut(123_456_789)

	signed, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)

	var original api.Transaction
	c.Assert(json.Unmarshal(signed, &original), IsNil)
	original.Signature = nil
	checkpoint, err := encodeTronSignCheckpoint(original, txOutItem)
	c.Assert(err, IsNil)
	c.Assert(checkpoint, NotNil)

	// A checkpoint retry must not depend on the live reference-block window or
	// call the transaction-construction API again.
	savedRefBlocks := s.client.tronScanner.refBlocks
	savedAPI := s.client.api
	s.client.tronScanner.refBlocks = nil
	s.client.api = nil
	defer func() {
		s.client.tronScanner.refBlocks = savedRefBlocks
		s.client.api = savedAPI
	}()

	txOutItem.Checkpoint = checkpoint
	retried, _, _, err := s.client.SignTx(txOutItem, 2)
	c.Assert(err, IsNil)
	c.Assert(retried, NotNil)

	var retry api.Transaction
	c.Assert(json.Unmarshal(retried, &retry), IsNil)
	c.Assert(retry.TxId, Equals, original.TxId)
	c.Assert(retry.RawDataHex, Equals, original.RawDataHex)
	c.Assert(retry.RawData.FeeLimit, Equals, original.RawData.FeeLimit)
}

func (s *TronTestSuite) TestSignCheckpointRejectsChangedOrCorruptState(c *C) {
	txOutItem := s.checkpointTestTxOut(123_456_790)
	signed, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)

	var unsigned api.Transaction
	c.Assert(json.Unmarshal(signed, &unsigned), IsNil)
	unsigned.Signature = nil
	checkpoint, err := encodeTronSignCheckpoint(unsigned, txOutItem)
	c.Assert(err, IsNil)

	changedTxOut := txOutItem
	changedTxOut.MaxGas = common.Gas{common.NewCoin(common.TRXAsset, cosmos.NewUint(5_000_000))}
	_, err = decodeTronSignCheckpoint(checkpoint, changedTxOut)
	c.Assert(err, NotNil)

	var envelope tronSignCheckpoint
	c.Assert(json.Unmarshal(checkpoint, &envelope), IsNil)
	envelope.UnsignedTx.TxId = strings.Repeat("0", 64)
	envelope.Checksum, err = tronJSONDigest(envelope.UnsignedTx)
	c.Assert(err, IsNil)
	tampered, err := json.Marshal(envelope)
	c.Assert(err, IsNil)
	_, err = decodeTronSignCheckpoint(tampered, txOutItem)
	c.Assert(err, NotNil)

	c.Assert(json.Unmarshal(checkpoint, &envelope), IsNil)
	envelope.UnsignedTx.Signature = []string{"unexpected-signature"}
	envelope.Checksum, err = tronJSONDigest(envelope.UnsignedTx)
	c.Assert(err, IsNil)
	tampered, err = json.Marshal(envelope)
	c.Assert(err, IsNil)
	_, err = decodeTronSignCheckpoint(tampered, txOutItem)
	c.Assert(err, NotNil)
}

func (s *TronTestSuite) TestSignFailureReturnsExactCheckpoint(c *C) {
	txOutItem := s.checkpointTestTxOut(123_456_791)
	signed, _, _, err := s.client.SignTx(txOutItem, 1)
	c.Assert(err, IsNil)

	var unsigned api.Transaction
	c.Assert(json.Unmarshal(signed, &unsigned), IsNil)
	unsigned.Signature = nil
	checkpoint, err := encodeTronSignCheckpoint(unsigned, txOutItem)
	c.Assert(err, IsNil)

	signErr := errors.New("terminal signing failure")
	signed, returnedCheckpoint, observation, err := s.client.signTronTransaction(
		txOutItem,
		2,
		unsigned,
		checkpoint,
		func([]byte, types.TxOutItem, int64) ([]byte, error) {
			return nil, signErr
		},
	)
	c.Assert(errors.Is(err, signErr), Equals, true)
	c.Assert(signed, IsNil)
	c.Assert(returnedCheckpoint, DeepEquals, checkpoint)
	c.Assert(observation, IsNil)

	signed, returnedCheckpoint, observation, err = s.client.signTronTransaction(
		txOutItem,
		2,
		unsigned,
		checkpoint,
		func([]byte, types.TxOutItem, int64) ([]byte, error) {
			return nil, tss.ErrNotSelectedForKeysign
		},
	)
	c.Assert(err, IsNil)
	c.Assert(signed, IsNil)
	c.Assert(returnedCheckpoint, IsNil)
	c.Assert(observation, IsNil)
}

func (s *TronTestSuite) TestShouldReportSolvency(c *C) {
	c.Assert(s.client.ShouldReportSolvency(1), Equals, false)
	c.Assert(s.client.ShouldReportSolvency(9570), Equals, true)
}

func (s *TronTestSuite) TestCreateTrc20Transaction(c *C) {
	testCases := []struct {
		From     string
		To       string
		Contract string
		Fail     bool
	}{
		{
			From:     "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
			To:       "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
			Contract: "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			Fail:     false,
		},
		{
			From:     "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
			To:       "bc1qelysxslyl86yq57yx9lzhl50pfayj9wng54p3g",
			Contract: "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs",
			Fail:     true,
		},
	}

	amount := big.NewInt(234_560_000_000)
	gas := big.NewInt(4_000_000)

	for _, tc := range testCases {
		tx, err := s.client.createTrc20Transaction(
			tc.From, tc.To, tc.Contract, *amount, *gas,
		)
		if tc.Fail {
			c.Assert(err, NotNil)
			continue
		}
		// returns mock data from api/test-tron/triggersmartcontract.json
		c.Assert(err, IsNil)
		c.Assert(tx.TxId, Equals, "482b1a3b61894f75ea25bd10b14335a4db86c7e2c642ae07abc5a8ae45fb0027")
	}
}

type failedRetryBridge struct {
	mockBridge
	base      time.Time
	gateErr   error
	gateCalls int
}

func (b *failedRetryBridge) GetMimir(_ string) (int64, error) {
	b.gateCalls++
	return b.skipCacheMimir, b.gateErr
}

func (b *failedRetryBridge) GetBlockTimestamp(height int64) (time.Time, error) {
	return b.base.Add(time.Duration(height) * time.Second), nil
}

// Keep HTTP state synchronized so the same fixture can cross a restart or a
// changed chain response without races between the test and server goroutines.
type failedRetryRPC struct {
	mu                 sync.Mutex
	transaction        string
	node               string
	receipt            string
	statusPath         string
	failCreate         bool
	broadcastDuplicate bool
	calls              []string
	lookupIDs          []string
	fallback           http.Handler
}

func (r *failedRetryRPC) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req.URL.Path)
	if req.URL.Path == r.statusPath || (r.failCreate && req.URL.Path == "/wallet/createtransaction") {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	var response string
	switch req.URL.Path {
	case "/wallet/gettransactionbyid", "/wallet/gettransactioninfobyid":
		var params struct {
			Value string `json:"value"`
		}
		if err := json.NewDecoder(req.Body).Decode(&params); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.lookupIDs = append(r.lookupIDs, params.Value)
		response = r.transaction
		if req.URL.Path == "/wallet/gettransactioninfobyid" {
			response = r.receipt
		}
	case "/wallet/getnodeinfo":
		response = r.node
	case "/wallet/broadcasttransaction":
		var tx api.Transaction
		if err := json.NewDecoder(req.Body).Decode(&tx); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		response = fmt.Sprintf(`{"result":true,"txid":%q}`, tx.TxId)
		if r.broadcastDuplicate {
			response = `{"result":false,"code":"DUP_TRANSACTION_ERROR"}`
		}
	default:
		r.fallback.ServeHTTP(w, req)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(response))
}

func (r *failedRetryRPC) evidence(id, result, execution string, height int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.transaction = fmt.Sprintf(`{"txID":%q,"ret":[{"contractRet":%q}]}`, id, execution)
	r.node = `{"solidityBlock":"Num:100,ID:0000000000000064` + strings.Repeat("a", 48) + `"}`
	r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":%d,"result":%q,"receipt":{"result":%q,"energy_usage_total":100}}`, id, height, result, execution)
}

func (r *failedRetryRPC) recoveryCalls() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var calls []string
	for _, path := range r.calls {
		if path == "/wallet/gettransactionbyid" || path == "/wallet/getnodeinfo" || path == "/wallet/gettransactioninfobyid" {
			calls = append(calls, path)
		}
	}
	return calls, append([]string(nil), r.lookupIDs...)
}

type failedRetryFixture struct {
	client *TronClient
	bridge *failedRetryBridge
	rpc    *failedRetryRPC
	db     *leveldb.DB
	path   string
	server *httptest.Server
}

func (s *TronTestSuite) newFailedRetryFixture(c *C) *failedRetryFixture {
	bridge := &failedRetryBridge{
		mockBridge: mockBridge{skipCacheMimir: 1},
		base:       time.Now().Add(-5 * time.Minute).Truncate(time.Millisecond),
	}
	rpc := &failedRetryRPC{fallback: s.api.Config.Handler}
	rpc.evidence(strings.Repeat("a", 64), "FAILED", "OUT_OF_ENERGY", 100)
	server := httptest.NewServer(rpc)
	tronAPI := api.NewTronApi(server.URL, 2*time.Second)
	path := filepath.Join(c.MkDir(), "cache")
	db, err := leveldb.OpenFile(path, nil)
	c.Assert(err, IsNil)
	manager, err := signercache.NewSignerCacheManager(db)
	c.Assert(err, IsNil)
	client := &TronClient{
		chainId:            s.client.chainId,
		config:             s.client.config,
		bridge:             bridge,
		api:                tronAPI,
		localKeyManager:    s.client.localKeyManager,
		signerCacheManager: manager,
		tronScanner: &TronBlockScanner{
			api: tronAPI,
			refBlocks: []RefBlock{{
				Timestamp: bridge.base.Add(-3 * time.Minute).UnixMilli(),
				Height:    55088560,
				Id:        "00000000034895b0e5029588a71f9689fb5c31e2c66989389d6ff09548726f6e",
			}},
		},
	}
	client.config.SignerCacheExpiry = time.Hour
	fixture := &failedRetryFixture{client: client, bridge: bridge, rpc: rpc, db: db, path: path, server: server}
	return fixture
}

func (s *TronTestSuite) failedRetryTxOut(amount uint64) types.TxOutItem {
	tx := s.checkpointTestTxOut(amount)
	tx.Coins[0].Asset = common.TRXAsset
	return tx
}

func (f *failedRetryFixture) close(c *C) {
	f.server.Close()
	c.Check(f.db.Close(), IsNil)
}

func (f *failedRetryFixture) reopen(c *C) {
	c.Assert(f.db.Close(), IsNil)
	var err error
	f.db, err = leveldb.OpenFile(f.path, nil)
	c.Assert(err, IsNil)
	f.client.signerCacheManager, err = signercache.NewSignerCacheManager(f.db)
	c.Assert(err, IsNil)
}

func (f *failedRetryFixture) cache(c *C, tx types.TxOutItem, id string, expiry int64) {
	c.Assert(f.client.signerCacheManager.SetSignedWithExpiry(tx.CacheHash(), tx.CacheVault(tx.Chain), id, expiry), IsNil)
}

func (f *failedRetryFixture) assertCached(c *C, tx types.TxOutItem, id string) {
	got, ok := f.client.signerCacheManager.GetSignedTxHash(tx.CacheHash())
	c.Assert(ok, Equals, true)
	c.Assert(got, Equals, id)
}

func (f *failedRetryFixture) signedTransaction(c *C, tx types.TxOutItem) (api.Transaction, []byte) {
	signed, checkpoint, observation, err := f.client.SignTx(tx, tx.Height)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, NotNil)
	var transaction api.Transaction
	c.Assert(json.Unmarshal(signed, &transaction), IsNil)
	c.Assert(observation.Tx, Equals, transaction.TxId)
	c.Assert(transaction.Signature, HasLen, 1)
	return transaction, signed
}

func (s *TronTestSuite) TestFailedPaymentRecoveryNewScheduleAndRestart(c *C) {
	f := s.newFailedRetryFixture(c)
	defer f.close(c)
	original := s.failedRetryTxOut(11300)
	original.Height = 100
	failed, signed := f.signedTransaction(c, original)
	id, err := f.client.BroadcastTx(original, signed)
	c.Assert(err, IsNil)
	c.Assert(id, Equals, failed.TxId)
	f.cache(c, original, failed.TxId, time.Now().Add(-time.Minute).UnixMilli())
	f.rpc.evidence(failed.TxId, "FAILED", "OUT_OF_ENERGY", 100)
	f.reopen(c)

	retry := original
	retry.Height++
	c.Assert(retry.CacheHash(), Equals, original.CacheHash())
	replacement, replacementBytes := f.signedTransaction(c, retry)
	c.Assert(replacement.TxId == failed.TxId, Equals, false)
	c.Assert(replacement.RawData.Timestamp, Equals, failed.RawData.Timestamp+1000)
	c.Assert(replacement.RawData.Expiration > time.Now().UnixMilli(), Equals, true)
	// Signing alone must leave the failed attempt durable until broadcast.
	f.assertCached(c, retry, failed.TxId)
	calls, ids := f.rpc.recoveryCalls()
	c.Assert(calls, DeepEquals, []string{"/wallet/gettransactionbyid", "/wallet/getnodeinfo", "/wallet/gettransactioninfobyid"})
	c.Assert(ids, DeepEquals, []string{failed.TxId, failed.TxId})

	id, err = f.client.BroadcastTx(retry, replacementBytes)
	c.Assert(err, IsNil)
	c.Assert(id, Equals, replacement.TxId)
	f.reopen(c)
	f.assertCached(c, retry, replacement.TxId)
	signed, checkpoint, observation, err := f.client.SignTx(retry, retry.Height)
	c.Assert(err, IsNil)
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, IsNil)
	after, _ := f.rpc.recoveryCalls()
	c.Assert(after, DeepEquals, calls)
}

func (s *TronTestSuite) TestFailedPaymentRecoveryUncertainEvidenceRetainsCache(c *C) {
	id := strings.Repeat("a", 64)
	cases := []struct {
		name   string
		modify func(*failedRetryRPC)
	}{
		{"successful payment", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"receipt":{"result":"SUCCESS"}}`, id)
		}},
		{"not solidified", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":101,"result":"FAILED","receipt":{"result":"REVERT"}}`, id)
		}},
		{"pending", func(r *failedRetryRPC) { r.receipt = `{}` }},
		{"missing receipt", func(r *failedRetryRPC) { r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"result":"FAILED"}`, id) }},
		{"missing height", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"result":"FAILED","receipt":{"result":"REVERT"}}`, id)
		}},
		{"zero height", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":0,"result":"FAILED","receipt":{"result":"REVERT"}}`, id)
		}},
		{"missing top-level failure", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"receipt":{"result":"REVERT"}}`, id)
		}},
		{"unknown execution", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"result":"FAILED","receipt":{"result":"FUTURE_FAILURE"}}`, id)
		}},
		{"default execution", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"result":"FAILED","receipt":{"result":"DEFAULT"}}`, id)
		}},
		{"unknown execution enum", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"result":"FAILED","receipt":{"result":"UNKNOWN"}}`, id)
		}},
		{"wrong receipt id", func(r *failedRetryRPC) {
			r.receipt = fmt.Sprintf(`{"id":%q,"blockNumber":100,"result":"FAILED","receipt":{"result":"REVERT"}}`, strings.Repeat("b", 64))
		}},
		{"wrong transaction id", func(r *failedRetryRPC) { r.transaction = fmt.Sprintf(`{"txID":%q}`, strings.Repeat("b", 64)) }},
		{"transaction error envelope", func(r *failedRetryRPC) { r.transaction = `{"Error":"lookup unavailable"}` }},
		{"null transaction", func(r *failedRetryRPC) { r.transaction = `null` }},
		{"malformed transaction", func(r *failedRetryRPC) { r.transaction = `{` }},
		{"receipt error envelope", func(r *failedRetryRPC) { r.receipt = `{"Error":"lookup unavailable"}` }},
		{"malformed receipt", func(r *failedRetryRPC) { r.receipt = `{` }},
		{"null receipt", func(r *failedRetryRPC) { r.receipt = `null` }},
		{"missing solid floor", func(r *failedRetryRPC) { r.node = `{}` }},
		{"malformed solid floor", func(r *failedRetryRPC) { r.node = `{"solidityBlock":"Num:100,ID:not-a-block-id"}` }},
		{"transaction RPC error", func(r *failedRetryRPC) { r.statusPath = "/wallet/gettransactionbyid" }},
		{"finality RPC error", func(r *failedRetryRPC) { r.statusPath = "/wallet/getnodeinfo" }},
		{"receipt RPC error", func(r *failedRetryRPC) { r.statusPath = "/wallet/gettransactioninfobyid" }},
	}
	for _, tc := range cases {
		c.Log(tc.name)
		f := s.newFailedRetryFixture(c)
		defer f.close(c)
		tx := s.failedRetryTxOut(11301)
		tx.Height = 100
		f.cache(c, tx, id, time.Now().Add(-time.Minute).UnixMilli())
		f.rpc.mu.Lock()
		tc.modify(f.rpc)
		f.rpc.mu.Unlock()
		signed, checkpoint, observation, err := f.client.SignTx(tx, tx.Height)
		c.Assert(err, IsNil, Commentf("%s", tc.name))
		c.Assert(signed, IsNil, Commentf("%s", tc.name))
		c.Assert(checkpoint, IsNil)
		c.Assert(observation, IsNil)
		f.assertCached(c, tx, id)
	}
}

func (s *TronTestSuite) TestFailedPaymentRecoveryGateBoundaries(c *C) {
	id := strings.Repeat("a", 64)
	for _, name := range []string{"fresh", "gate off", "gate error", "legacy", "missing hash", "expired"} {
		c.Log(name)
		f := s.newFailedRetryFixture(c)
		defer f.close(c)
		tx := s.failedRetryTxOut(11302)
		tx.Height = 100
		expiry := time.Now().Add(-time.Minute).UnixMilli()
		if name == "fresh" {
			expiry = time.Now().Add(time.Hour).UnixMilli()
		}
		if name == "gate off" {
			f.bridge.skipCacheMimir = 0
		}
		if name == "gate error" {
			f.bridge.gateErr = errors.New("mimir unavailable")
		}
		switch name {
		case "legacy":
			c.Assert(f.client.signerCacheManager.SetSigned(tx.CacheHash(), tx.CacheVault(tx.Chain), id), IsNil)
		case "missing hash":
			f.cache(c, tx, "", expiry)
		default:
			f.cache(c, tx, id, expiry)
		}
		signed, _, _, err := f.client.SignTx(tx, tx.Height)
		c.Assert(err, IsNil, Commentf("%s", name))
		calls, _ := f.rpc.recoveryCalls()
		if name == "expired" {
			c.Assert(signed, NotNil)
			c.Assert(calls, HasLen, 3)
		} else {
			c.Assert(signed, IsNil)
			c.Assert(calls, HasLen, 0)
		}
		wantGateCalls := 1
		if name == "fresh" || name == "legacy" {
			wantGateCalls = 0
		}
		c.Assert(f.bridge.gateCalls, Equals, wantGateCalls)
		c.Assert(f.client.signerCacheManager.HasSigned(tx.CacheHash()), Equals, true)
	}
}

func (s *TronTestSuite) TestFailedPaymentRecoveryCheckpointsAndConstruction(c *C) {
	f := s.newFailedRetryFixture(c)
	defer f.close(c)
	original := s.failedRetryTxOut(11303)
	original.Height = 100
	failed, _ := f.signedTransaction(c, original)
	failed.Signature = nil
	oldCheckpoint, err := encodeTronSignCheckpoint(failed, original)
	c.Assert(err, IsNil)
	f.cache(c, original, failed.TxId, time.Now().Add(-time.Minute).UnixMilli())
	f.rpc.evidence(failed.TxId, "FAILED", "REVERT", 100)

	// The exact failed attempt must never be signed again, with or without a
	// retained checkpoint, including after an ordinary database restart.
	for _, checkpoint := range [][]byte{oldCheckpoint, nil} {
		f.reopen(c)
		original.Checkpoint = checkpoint
		signed, returned, observation, signErr := f.client.SignTx(original, original.Height)
		c.Assert(errors.Is(signErr, chainclienttypes.ErrTxExpired), Equals, true)
		c.Assert(signed, IsNil)
		c.Assert(returned, IsNil)
		c.Assert(observation, IsNil)
		f.assertCached(c, original, failed.TxId)
	}

	retry := original
	retry.Height++
	retry.Checkpoint = nil
	f.rpc.mu.Lock()
	f.rpc.failCreate = true
	f.rpc.mu.Unlock()
	signed, checkpoint, observation, err := f.client.SignTx(retry, retry.Height)
	c.Assert(err, NotNil)
	c.Assert(signed, IsNil)
	c.Assert(checkpoint, IsNil)
	c.Assert(observation, IsNil)
	f.reopen(c)
	f.assertCached(c, retry, failed.TxId)
	f.rpc.mu.Lock()
	f.rpc.failCreate = false
	f.rpc.mu.Unlock()
	replacement, _ := f.signedTransaction(c, retry)
	replacement.Signature = nil
	retry.Checkpoint, err = encodeTronSignCheckpoint(replacement, retry)
	c.Assert(err, IsNil)
	f.reopen(c)
	// A different pinned attempt may already have exposed a signature. Keep
	// its exact preimage and avoid transaction construction on retry.
	f.rpc.mu.Lock()
	f.rpc.failCreate = true
	f.rpc.mu.Unlock()
	restored, _ := f.signedTransaction(c, retry)
	c.Assert(restored.TxId, Equals, replacement.TxId)
	c.Assert(restored.RawDataHex, Equals, replacement.RawDataHex)
	f.assertCached(c, retry, failed.TxId)
}

func (s *TronTestSuite) TestFailedPaymentRecoveryExpiredReplacementCheckpoint(c *C) {
	f := s.newFailedRetryFixture(c)
	defer f.close(c)
	f.bridge.base = time.Now().Add(-time.Hour)
	tx := s.failedRetryTxOut(11304)
	tx.Height = 100
	// A peer may have broadcast replacement B after a terminal keysign error,
	// leaving this node with B's checkpoint but only failed attempt A cached.
	expired, _ := f.signedTransaction(c, tx)
	expired.Signature = nil
	checkpoint, err := encodeTronSignCheckpoint(expired, tx)
	c.Assert(err, IsNil)
	failedID := strings.Repeat("a", 64)
	f.cache(c, tx, failedID, time.Now().Add(-time.Minute).UnixMilli())

	// A fresh construction at a stale schedule is retryable, never evidence
	// that an already exposed replacement checkpoint can be retired.
	signed, returned, observation, err := f.client.SignTx(tx, tx.Height)
	c.Assert(err, NotNil)
	c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, false)
	c.Assert(signed, IsNil)
	c.Assert(returned, IsNil)
	c.Assert(observation, IsNil)
	f.assertCached(c, tx, failedID)

	f.reopen(c)
	tx.Checkpoint = checkpoint
	restored, signed := f.signedTransaction(c, tx)
	c.Assert(restored.TxId, Equals, expired.TxId)
	c.Assert(restored.RawDataHex, Equals, expired.RawDataHex)
	f.assertCached(c, tx, failedID)
	f.rpc.mu.Lock()
	f.rpc.broadcastDuplicate = true
	f.rpc.mu.Unlock()
	id, err := f.client.BroadcastTx(tx, signed)
	c.Assert(err, IsNil)
	c.Assert(id, Equals, expired.TxId)
	f.reopen(c)
	f.assertCached(c, tx, expired.TxId)
	// Even if the replacement's cache later expires, its successful payment
	// must stop another recovery attempt.
	f.cache(c, tx, expired.TxId, time.Now().Add(-time.Minute).UnixMilli())
	f.rpc.evidence(expired.TxId, "", "SUCCESS", 100)
	signed, returned, observation, err = f.client.SignTx(tx, tx.Height)
	c.Assert(err, IsNil)
	c.Assert(signed, IsNil)
	c.Assert(returned, IsNil)
	c.Assert(observation, IsNil)
	f.assertCached(c, tx, expired.TxId)
}
