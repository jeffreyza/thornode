package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	. "gopkg.in/check.v1"

	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	utxorpcc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type zecExpiryBridge struct {
	thorclient.ThorchainBridge
	observedHeight   int64
	requestedHeights []int64
	err              error
}

func (b *zecExpiryBridge) GetLastObservedInHeightAtHeight(chain common.Chain, height int64) (int64, error) {
	if chain != common.ZECChain {
		return 0, fmt.Errorf("unexpected chain %s", chain)
	}
	b.requestedHeights = append(b.requestedHeights, height)
	return b.observedHeight, b.err
}

func (b *zecExpiryBridge) GetMimir(string) (int64, error) { return 1, nil }

// Record the actual digest handed to TSS, then simulate a failed signing round.
// It can also return a valid signature to check the serialized ZEC transaction.
type zecDigestSigner struct {
	tss.ThorchainKeyManager
	mu         sync.Mutex
	digests    [][]byte
	heights    []int64
	privateKey *btcec.PrivateKey
	fail       bool
	afterSign  func()
}

func (s *zecDigestSigner) RemoteSign(msg []byte, _ common.SigningAlgo, _ string, height int64) ([]byte, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.digests = append(s.digests, append([]byte(nil), msg...))
	s.heights = append(s.heights, height)
	if s.fail {
		return nil, nil, errors.New("injected signing failure")
	}
	signature, err := s.privateKey.Sign(msg)
	if err != nil {
		return nil, nil, err
	}
	raw := make([]byte, 64)
	signature.R.FillBytes(raw[:32])
	signature.S.FillBytes(raw[32:])
	if s.afterSign != nil {
		s.afterSign()
	}
	return raw, nil, nil
}

type zecExpiryFixture struct {
	tx          stypes.TxOutItem
	bridge      *zecExpiryBridge
	signer      *zecDigestSigner
	localHeight atomic.Int64
	heightErr   atomic.Bool
	inputsSpent atomic.Bool
	upgrades    string
	upgradesErr atomic.Bool
	afterUTXOs  func()
	rpcServer   *httptest.Server
}

func (s *ZcashSignerSuite) newZecExpiryFixture(c *C) *zecExpiryFixture {
	const agreedHeight = int64(3500000)
	bridge := &zecExpiryBridge{ThorchainBridge: s.bridge, observedHeight: agreedHeight}
	s.client.bridge = bridge
	privateKey, _ := btcec.PrivKeyFromBytes(btcec.S256(), bytes.Repeat([]byte{1}, 32))
	vault, err := bech32AccountPubKey(privateKey)
	c.Assert(err, IsNil)
	address, err := vault.GetAddress(common.ZECChain)
	c.Assert(err, IsNil)
	tx := stypes.TxOutItem{
		Chain: common.ZECChain, VaultPubKey: vault, ToAddress: address,
		Coins:  common.NewCoins(common.NewCoin(common.ZECAsset, cosmos.NewUint(100000000))),
		MaxGas: common.Gas{common.NewCoin(common.ZECAsset, cosmos.NewUint(100000))},
		Memo:   "MIGRATE:1237",
	}
	sourceScript, err := s.client.getSourceScript(tx)
	c.Assert(err, IsNil)
	signer := &zecDigestSigner{privateKey: privateKey, fail: true}
	s.client.tssKeySigner = signer
	s.client.cfg.UTXO.MaxSatsPerVByte = 1000
	fixture := &zecExpiryFixture{
		tx: tx, bridge: bridge, signer: signer,
		upgrades: `{"37a5165b":{"activationheight":1}}`,
	}
	fixture.localHeight.Store(agreedHeight + 1)
	rpcServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
			ID     int    `json:"id"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil {
			http.Error(w, decodeErr.Error(), 400)
			return
		}
		var result any
		switch request.Method {
		case "getblockcount":
			if fixture.heightErr.Load() {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "error": map[string]any{"code": -1, "message": "tip unavailable"}})
				return
			}
			result = fixture.localHeight.Load()
		case "getblockchaininfo":
			if fixture.upgradesErr.Load() {
				http.Error(w, "upgrade schedule unavailable", 503)
				return
			}
			result = map[string]any{"upgrades": json.RawMessage(fixture.upgrades)}
		case "getaddressutxos":
			if fixture.afterUTXOs != nil {
				fixture.afterUTXOs()
			}
			if fixture.inputsSpent.Load() {
				result = []map[string]any{}
				break
			}
			result = []map[string]any{{
				"txid":        "500468e8f504eaeee3c53f826dac132dbff2abf824b5361d10d712a199917d65",
				"outputIndex": 0, "address": address.String(), "script": hex.EncodeToString(sourceScript),
				"satoshis": 150000000, "height": agreedHeight - 100,
			}}
		default:
			http.Error(w, "unexpected RPC: "+request.Method, 400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": result, "error": nil})
	}))
	fixture.rpcServer = rpcServer
	s.client.rpc, err = utxorpcc.NewClient(rpcServer.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
	c.Assert(err, IsNil)

	return fixture
}

func (s *ZcashSignerSuite) TestZECExpiryAcrossSignersAndCheckpointRetry(c *C) {
	const signingHeight = int64(1237)
	const agreedHeight = int64(3500000)
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	tx, bridge, signer := fixture.tx, fixture.bridge, fixture.signer
	localHeight := &fixture.localHeight
	privateKey := signer.privateKey
	var err error

	// Independent first attempts must agree even across a local block boundary.
	raw, checkpoint1, _, err := s.client.SignTx(tx, signingHeight)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(raw, IsNil)
	c.Assert(len(checkpoint1) > 0, Equals, true)
	localHeight.Add(1)
	_, checkpoint2, _, err := s.client.SignTx(tx, signingHeight)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(checkpoint2, DeepEquals, checkpoint1)
	c.Assert(bridge.requestedHeights, DeepEquals, []int64{signingHeight, signingHeight})
	c.Assert(len(signer.digests), Equals, 2)
	c.Assert(signer.digests[1], DeepEquals, signer.digests[0])

	var checkpoint sharedutxo.SignCheckpoint
	c.Assert(json.Unmarshal(checkpoint1, &checkpoint), IsNil)
	c.Assert(checkpoint.ZecExpiryHeight, Equals, uint32(agreedHeight)+ZecTxExpiry)
	c.Assert(checkpoint.ZecConsensusBranchID, Equals, uint32(0x37a5165b))
	var unsigned wire.MsgTx
	c.Assert(unsigned.Deserialize(bytes.NewReader(checkpoint.UnsignedTx)), IsNil)
	c.Assert(unsigned.Version, Equals, int32(6))

	// A fresh signer after restart must use the saved expiry even if the
	// historical state has since been pruned and the local tip has advanced.
	restartedSigner := &zecDigestSigner{privateKey: privateKey, fail: true}
	restarted, err := NewClient(s.keys, s.client.cfg, nil, s.bridge, s.m)
	c.Assert(err, IsNil)
	restarted.rpc = s.client.rpc
	restarted.bridge = bridge
	restarted.tssKeySigner = restartedSigner
	restarted.temporalStorage = s.client.temporalStorage
	restarted.signerCacheManager = s.client.signerCacheManager
	s.client = restarted
	bridge.err = errors.New("historical state pruned")
	_, _, _, err = s.client.SignTx(tx, signingHeight)
	c.Assert(err, ErrorMatches, ".*historical state pruned")
	c.Assert(len(restartedSigner.digests), Equals, 0)
	localHeight.Add(10)
	fixture.upgradesErr.Store(true) // Retries must not fetch a new branch either.
	tx.Checkpoint = checkpoint1
	_, retriedCheckpoint, _, err := s.client.SignTx(tx, signingHeight)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	c.Assert(retriedCheckpoint, DeepEquals, checkpoint1)
	c.Assert(restartedSigner.digests[0], DeepEquals, signer.digests[0])
	c.Assert(len(bridge.requestedHeights), Equals, 3)

	// Expiry-less checkpoints predate mainnet activation. Drop the attempt
	// without inventing a new digest or querying historical state to fill it in.
	checkpoint.ZecExpiryHeight = 0
	tx.Checkpoint, err = json.Marshal(checkpoint)
	c.Assert(err, IsNil)
	fixture.heightErr.Store(true) // Discard does not require a functioning ZEC RPC.
	raw, retiredCheckpoint, retiredObservation, err := s.client.SignTx(tx, signingHeight)
	c.Assert(err, IsNil)
	c.Assert(raw, IsNil)
	c.Assert(retiredCheckpoint, IsNil)
	c.Assert(retiredObservation, IsNil)
	c.Assert(len(restartedSigner.digests), Equals, 1)
	c.Assert(len(bridge.requestedHeights), Equals, 3)
	fixture.heightErr.Store(false)

	// The pinned expiry must also reach the successfully serialized payload.
	tx.Checkpoint = checkpoint1
	restartedSigner.fail = false
	raw, _, observation, err := s.client.SignTx(tx, signingHeight)
	c.Assert(err, IsNil)
	c.Assert(observation, NotNil)
	c.Assert(restartedSigner.digests[1], DeepEquals, signer.digests[0])
	signed, err := zcash.ParseTx(raw)
	c.Assert(err, IsNil)
	c.Assert(signed.ExpiryHeight, Equals, uint32(agreedHeight)+ZecTxExpiry)
	c.Assert(signed.Version, Equals, int32(6))
	c.Assert(signed.ConsensusBranchID, Equals, uint32(0x37a5165b))
	c.Assert(observation.Tx, Equals, zecPayloadTxID(raw))
}

// TestZECRejectsUnsupportedCheckpoint prevents changing the digest of an unfinished signing round.
func (s *ZcashSignerSuite) TestZECRejectsUnsupportedCheckpoint(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	_, saved, _, signErr := s.client.SignTx(fixture.tx, 1237)
	c.Assert(signErr, ErrorMatches, "(?s).*injected signing failure.*")
	fixture.upgradesErr.Store(true)
	// Non-signable versions (1, 2, 4) are rejected by the version check; the signable
	// versions (5, 6) with a zeroed branch exercise the other rejection clause.
	for _, version := range []int32{1, 2, 4, 5, 6} {
		var checkpoint sharedutxo.SignCheckpoint
		c.Assert(json.Unmarshal(saved, &checkpoint), IsNil)
		var tx wire.MsgTx
		c.Assert(tx.Deserialize(bytes.NewReader(checkpoint.UnsignedTx)), IsNil)
		tx.Version = version
		var encoded bytes.Buffer
		c.Assert(tx.Serialize(&encoded), IsNil)
		checkpoint.UnsignedTx = encoded.Bytes()
		if version == 5 || version == 6 {
			checkpoint.ZecConsensusBranchID = 0
		}
		var marshalErr error
		fixture.tx.Checkpoint, marshalErr = json.Marshal(checkpoint)
		c.Assert(marshalErr, IsNil)
		raw, kept, observation, err := s.client.SignTx(fixture.tx, 1237)
		c.Assert(err, ErrorMatches, "unsupported Zcash checkpoint")
		c.Assert(raw, IsNil)
		c.Assert(kept, DeepEquals, fixture.tx.Checkpoint)
		c.Assert(observation, IsNil)

		// Expiry retires even unsupported signing formats, at the boundary too.
		for _, tip := range []int64{
			int64(checkpoint.ZecExpiryHeight),
			int64(checkpoint.ZecExpiryHeight) + 1,
		} {
			fixture.localHeight.Store(tip)
			raw, _, observation, err = s.client.SignTx(fixture.tx, 1237)
			c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, true)
			c.Assert(raw, IsNil)
			c.Assert(observation, IsNil)
		}

		// Lose the tip after input verification; retain the checkpoint.
		fixture.afterUTXOs = func() { fixture.heightErr.Store(true) }
		raw, kept, observation, err = s.client.SignTx(fixture.tx, 1237)
		c.Assert(err, ErrorMatches, "get ZEC tip for expiry check: .*")
		c.Assert(kept, DeepEquals, fixture.tx.Checkpoint)
		c.Assert(raw, IsNil)
		c.Assert(observation, IsNil)

		// Spent inputs retire without another tip lookup or a v5 digest.
		fixture.heightErr.Store(false)
		fixture.inputsSpent.Store(true)
		raw, kept, observation, err = s.client.SignTx(fixture.tx, 1237)
		c.Assert(err, IsNil)
		c.Assert(fixture.heightErr.Load(), Equals, true)
		c.Assert(kept, IsNil)
		c.Assert(raw, IsNil)
		c.Assert(observation, IsNil)

		fixture.inputsSpent.Store(false)
		fixture.heightErr.Store(false)
		fixture.afterUTXOs = nil
		fixture.localHeight.Store(int64(checkpoint.ZecExpiryHeight) - 1)
	}
	c.Assert(fixture.signer.digests, HasLen, 1)
}

func (s *ZcashSignerSuite) TestZECV6DaemonBranchCheckpoint(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	fixture.upgrades = `{"37a5165b":{"activationheight":1},"01020304":{"activationheight":3500000}}`
	_, saved, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, ErrorMatches, "(?s).*injected signing failure.*")
	var checkpoint sharedutxo.SignCheckpoint
	c.Assert(json.Unmarshal(saved, &checkpoint), IsNil)
	c.Assert(checkpoint.ZecConsensusBranchID, Equals, uint32(0x01020304))

	fixture.tx.Checkpoint = saved
	fixture.upgradesErr.Store(true)
	fixture.signer.fail = false
	raw, _, observation, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, IsNil)
	c.Assert(observation, NotNil)
	tx, err := zcash.ParseTx(raw)
	c.Assert(err, IsNil)
	c.Assert(tx.ConsensusBranchID, Equals, checkpoint.ZecConsensusBranchID)
	c.Assert(tx.ExpiryHeight, Equals, checkpoint.ZecExpiryHeight)
	c.Assert(fixture.signer.digests, HasLen, 2)
	c.Assert(fixture.signer.digests[1], DeepEquals, fixture.signer.digests[0])
}

// TestZECV6UpgradeExpiry prevents a payment from crossing the next upgrade.
func (s *ZcashSignerSuite) TestZECV6UpgradeExpiry(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	fixture.upgrades = `{"37a5165b":{"activationheight":1},"01020304":{"activationheight":3500020}}`
	// Shortening expiry must not lower the input-selection catch-up boundary.
	fixture.localHeight.Store(fixture.bridge.observedHeight - 1)
	_, _, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, ErrorMatches, ".*local ZEC tip 3499999 is behind input reference 3500000")
	c.Assert(fixture.signer.digests, HasLen, 0)
	fixture.localHeight.Store(fixture.bridge.observedHeight + 1)
	fixture.signer.fail = false
	raw, _, _, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, IsNil)
	tx, err := zcash.ParseTx(raw)
	c.Assert(err, IsNil)
	c.Assert(tx.ConsensusBranchID, Equals, uint32(0x37a5165b))
	c.Assert(tx.ExpiryHeight, Equals, uint32(3500019))
	input := tx.TxIn[0].PreviousOutPoint
	id := formatUtxoKey(input.Hash.String(), input.Index)
	c.Assert(s.client.temporalStorage.PruneSpentUtxos(3500018-int64(ZecTxExpiry)), IsNil)
	spent, err := s.client.temporalStorage.HasSpentUtxo(id)
	c.Assert(err, IsNil)
	c.Assert(spent, Equals, true)
	c.Assert(s.client.temporalStorage.PruneSpentUtxos(3500019-int64(ZecTxExpiry)), IsNil)
	spent, err = s.client.temporalStorage.HasSpentUtxo(id)
	c.Assert(err, IsNil)
	c.Assert(spent, Equals, false)
}

func (s *ZcashSignerSuite) TestZECExpiryRejectsUnavailableOrInvalidHeight(c *C) {
	bridge := &zecExpiryBridge{ThorchainBridge: s.bridge, observedHeight: 3500000}
	s.client.bridge = bridge
	bridge.err = errors.New("historical state unavailable")
	_, heightErr := s.client.getZecExpiryHeight(1237)
	c.Assert(heightErr, ErrorMatches, ".*historical state unavailable")
	bridge.err = nil
	invalidHeights := []int64{
		-1, 0,
		5e8 - int64(ZecTxExpiry),
		int64(math.MaxUint32-ZecTxExpiry) + 1,
		math.MaxInt64,
	}
	for _, height := range invalidHeights {
		bridge.observedHeight = height
		expiry, err := s.client.getZecExpiryHeight(1237)
		c.Assert(err, ErrorMatches, "invalid agreed ZEC height for expiry: .*", Commentf("height %d", height))
		c.Assert(expiry, Equals, uint32(0))
	}
}

func (s *ZcashSignerSuite) TestZECStaticFeeRefreshesAgreedHeight(c *C) {
	bridge := &zecFeeBridge{ThorchainBridge: s.bridge, maxUTXOs: 6}
	s.client.bridge = bridge
	s.client.cfg.BlockScanner.GasPriceResolution = 1
	queue := make(chan common.NetworkFee, 4)
	s.client.globalNetworkFeeQueue = queue
	// Empty blocks still need height reports: ZEC's ZIP-317 fees are static.
	c.Assert(s.client.sendNetworkFeeFromBlock(&btcjson.GetBlockVerboseTxResult{Height: 1200}), IsNil)
	c.Assert(len(queue), Equals, 1)
	first := <-queue
	c.Assert(s.client.sendNetworkFeeFromBlock(&btcjson.GetBlockVerboseTxResult{Height: 1201}), IsNil)
	c.Assert(len(queue), Equals, 0)
	c.Assert(s.client.sendNetworkFeeFromBlock(&btcjson.GetBlockVerboseTxResult{Height: 1210}), IsNil)
	c.Assert(len(queue), Equals, 1)
	refresh := <-queue
	c.Assert(refresh.Height, Equals, int64(1210))
	c.Assert(refresh.TransactionRate, Equals, first.TransactionRate)
	c.Assert(refresh.TransactionSize, Equals, first.TransactionSize)

	// More inputs increase the actual ZIP-317 fee. A changed fee must still
	// report between periodic height refreshes.
	bridge.maxUTXOs = 10
	c.Assert(s.client.sendNetworkFeeFromBlock(&btcjson.GetBlockVerboseTxResult{Height: 1211}), IsNil)
	c.Assert(len(queue), Equals, 1)
	changed := <-queue
	c.Assert(changed.Height, Equals, int64(1211))
	c.Assert(changed.TransactionRate > refresh.TransactionRate, Equals, true)
	c.Assert(changed.TransactionRate-refresh.TransactionRate, Equals, uint64(20000))
	c.Assert(changed.TransactionSize, Equals, first.TransactionSize)
}

type zecFeeBridge struct {
	thorclient.ThorchainBridge
	maxUTXOs int64
}

func (b *zecFeeBridge) GetMimir(key string) (int64, error) {
	if key == "MaxUTXOsToSpend" {
		return b.maxUTXOs, nil
	}
	return b.ThorchainBridge.GetMimir(key)
}

func (s *ZcashSignerSuite) TestZECExpiryRejectsStaleAnchorAndCheckpoint(c *C) {
	for _, restored := range []bool{false, true} {
		for _, tip := range []int64{3500040, 3500041} {
			fixture := s.newZecExpiryFixture(c)
			tx := fixture.tx
			if restored {
				_, checkpoint, _, signErr := s.client.SignTx(tx, 1237)
				c.Assert(signErr, ErrorMatches, "(?s).*injected signing failure.*")
				tx.Checkpoint = checkpoint
				fixture.bridge.err = errors.New("historical state pruned")
			}
			fixture.signer.digests = nil
			fixture.signer.fail = false
			fixture.localHeight.Store(tip)
			raw, _, observation, err := s.client.SignTx(tx, 1237)
			c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, true, Commentf("restored=%v tip=%d: %v", restored, tip, err))
			c.Assert(raw, IsNil)
			c.Assert(observation, IsNil)
			c.Assert(fixture.signer.digests, HasLen, 0)
			spent, spentErr := s.client.temporalStorage.HasSpentUtxo(formatUtxoKey("500468e8f504eaeee3c53f826dac132dbff2abf824b5361d10d712a199917d65", 0))
			c.Assert(spentErr, IsNil)
			c.Assert(spent, Equals, false)
			fixture.rpcServer.Close()
		}
	}
}

func (s *ZcashSignerSuite) TestZECExpiryDuringSigning(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	fixture.signer.fail = false
	fixture.signer.afterSign = func() { fixture.localHeight.Store(3500040) }
	raw, _, observation, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, true)
	c.Assert(raw, IsNil)
	c.Assert(observation, IsNil)
	c.Assert(fixture.signer.digests, HasLen, 1)
	spent, err := s.client.temporalStorage.HasSpentUtxo(formatUtxoKey("500468e8f504eaeee3c53f826dac132dbff2abf824b5361d10d712a199917d65", 0))
	c.Assert(err, IsNil)
	c.Assert(spent, Equals, false)
}

func (s *ZcashSignerSuite) TestZECExpiryRequiresLiveTip(c *C) {
	for _, afterSigning := range []bool{false, true} {
		fixture := s.newZecExpiryFixture(c)
		fixture.signer.fail = false
		if afterSigning {
			fixture.signer.afterSign = func() { fixture.heightErr.Store(true) }
		} else {
			fixture.afterUTXOs = func() { fixture.heightErr.Store(true) }
		}
		raw, checkpoint, observation, err := s.client.SignTx(fixture.tx, 1237)
		if afterSigning {
			// Keep completed signatures and reserve their inputs when the tip
			// becomes unavailable. Broadcast remains blocked until it recovers.
			c.Assert(err, IsNil)
			c.Assert(len(raw) > 0, Equals, true)
			c.Assert(observation, NotNil)
			spent, spentErr := s.client.temporalStorage.GetSpentUtxosByHeight(3500000)
			c.Assert(spentErr, IsNil)
			c.Assert(spent, HasLen, 1)
			_, err = s.client.BroadcastTx(fixture.tx, raw)
			c.Assert(err, ErrorMatches, ".*tip unavailable.*")
			c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, false)
		} else {
			c.Assert(err, ErrorMatches, ".*tip unavailable.*")
			c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, false)
			c.Assert(raw, IsNil)
			c.Assert(observation, IsNil)
			c.Assert(len(checkpoint) > 0, Equals, true)
		}
		c.Assert(len(fixture.signer.digests) > 0, Equals, afterSigning)
		fixture.rpcServer.Close()
	}
}

func (s *ZcashSignerSuite) TestZECExpiryLastValidBlockAndCachePruning(c *C) {
	fixture := s.newZecExpiryFixture(c)
	defer fixture.rpcServer.Close()
	fixture.signer.fail = false
	fixture.localHeight.Store(3500039)
	raw, _, observation, err := s.client.SignTx(fixture.tx, 1237)
	c.Assert(err, IsNil)
	c.Assert(observation, NotNil)
	signed, err := zcash.ParseTx(raw)
	c.Assert(err, IsNil)
	c.Assert(signed.ExpiryHeight, Equals, uint32(3500040))
	id := formatUtxoKey(signed.TxIn[0].PreviousOutPoint.Hash.String(), signed.TxIn[0].PreviousOutPoint.Index)
	// FetchTxs uses scanned height minus ZecTxExpiry as the pruning cutoff.
	c.Assert(s.client.temporalStorage.PruneSpentUtxos(3500039-int64(ZecTxExpiry)), IsNil)
	spent, err := s.client.temporalStorage.HasSpentUtxo(id)
	c.Assert(err, IsNil)
	c.Assert(spent, Equals, true)
	c.Assert(s.client.temporalStorage.PruneSpentUtxos(3500040-int64(ZecTxExpiry)), IsNil)
	spent, err = s.client.temporalStorage.HasSpentUtxo(id)
	c.Assert(err, IsNil)
	c.Assert(spent, Equals, false)
}

func zecExpiryPayload(c *C, version int32, expiry uint32) []byte {
	tx := &zcash.Tx{MsgTx: wire.NewMsgTx(version), ExpiryHeight: expiry}
	if version == 5 || version == 6 {
		tx.ConsensusBranchID = 0x37a5165b
	}
	tx.AddTxIn(wire.NewTxIn(&wire.OutPoint{}, []byte{1}, nil))
	tx.AddTxOut(wire.NewTxOut(1, []byte{0x51}))
	var payload bytes.Buffer
	c.Assert(tx.Serialize(&payload), IsNil)
	return payload.Bytes()
}

func (s *ZcashSignerSuite) TestZECExpiryBroadcast(c *C) {
	for _, tc := range []struct {
		name                string
		version             int32
		tip                 int64
		expiry              uint32
		lookup              string
		expired, wantErr    bool
		broadcasts, lookups int32
	}{
		{"last valid block", 5, 39, 40, "", false, false, 1, 0},
		{"v6 last valid block", 6, 39, 40, "", false, false, 1, 0},
		{"v6 at expiry", 6, 40, 40, "missing", true, true, 0, 1},
		{"v6 past expiry", 6, 41, 40, "missing", true, true, 0, 1},
		{"at expiry", 5, 40, 40, "missing", true, true, 0, 1},
		{"past expiry", 5, 41, 40, "missing", true, true, 0, 1},
		{"already mined", 5, 41, 40, "confirmed", false, false, 0, 1},
		{"orphaned", 5, 41, 40, "orphaned", true, true, 0, 1},
		{"still in mempool", 5, 40, 40, "mempool", true, true, 0, 1},
		{"lookup unavailable", 5, 41, 40, "unavailable", false, true, 0, 1},
		{"lookup mismatch", 5, 41, 40, "mismatch", false, true, 0, 1},
		{"incomplete confirmation", 5, 41, 40, "incomplete", false, true, 0, 1},
		{"invalid tip", 5, 0, 40, "", false, true, 0, 0},
		{"v5 no expiry", 5, 41, 0, "", false, false, 1, 0},
		{"v3 no expiry", 3, 41, 0, "", false, false, 1, 0},
		{"v4 no expiry", 4, 41, 0, "", false, false, 1, 0},
		{"v3 at expiry", 3, 40, 40, "missing", true, true, 0, 1},
		{"v4 at expiry", 4, 40, 40, "missing", true, true, 0, 1},
		{"v3 already mined", 3, 41, 40, "confirmed", false, false, 0, 1},
		{"v4 already mined", 4, 41, 40, "confirmed", false, false, 0, 1},
	} {
		payload := zecExpiryPayload(c, tc.version, tc.expiry)
		var txid string
		if tc.version == 5 || tc.version == 6 {
			txid = zecPayloadTxID(payload)
		} else {
			txid = chainhash.DoubleHashH(payload).String()
		}
		var broadcasts, lookups atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Method string            `json:"method"`
				ID     int               `json:"id"`
				Params []json.RawMessage `json:"params"`
			}
			if decodeErr := json.NewDecoder(r.Body).Decode(&request); decodeErr != nil {
				http.Error(w, decodeErr.Error(), 400)
				return
			}
			var result, rpcErr any
			switch request.Method {
			case "getblockcount":
				result = tc.tip
			case "sendrawtransaction":
				broadcasts.Add(1)
				var submitted string
				if len(request.Params) != 1 || json.Unmarshal(request.Params[0], &submitted) != nil || submitted != hex.EncodeToString(payload) {
					http.Error(w, "unexpected transaction payload", 400)
					return
				}
				result = txid
			case "getrawtransaction":
				lookups.Add(1)
				var requestedID string
				if len(request.Params) == 0 || json.Unmarshal(request.Params[0], &requestedID) != nil || requestedID != txid {
					http.Error(w, "unexpected transaction lookup", 400)
					return
				}
				switch tc.lookup {
				case "missing":
					rpcErr = map[string]any{"code": -5, "message": "No such mempool or blockchain transaction"}
				case "unavailable":
					rpcErr = map[string]any{"code": -1, "message": "lookup unavailable"}
				case "confirmed":
					result = btcjson.TxRawResult{Txid: txid, Confirmations: 2, BlockHash: "block"}
				case "orphaned":
					result = btcjson.TxRawResult{Txid: txid, BlockHash: "orphan"}
				case "mempool":
					result = btcjson.TxRawResult{Txid: txid}
				case "mismatch":
					result = btcjson.TxRawResult{Txid: "different", Confirmations: 2, BlockHash: "block"}
				case "incomplete":
					result = btcjson.TxRawResult{Txid: txid, Confirmations: 2}
				}
			default:
				http.Error(w, "unexpected RPC: "+request.Method, 400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": request.ID, "result": result, "error": rpcErr})
		}))
		var err error
		s.client.rpc, err = utxorpcc.NewClient(server.Listener.Addr().String(), s.cfg.UserName, s.cfg.Password, 0, time.Second, common.ZECChain, s.client.log)
		c.Assert(err, IsNil)
		txOut := stypes.TxOutItem{Chain: common.ZECChain, Memo: tc.name}
		got, err := s.client.BroadcastTx(txOut, payload)
		c.Assert(err != nil, Equals, tc.wantErr, Commentf("%s: %v", tc.name, err))
		c.Assert(errors.Is(err, chainclienttypes.ErrTxExpired), Equals, tc.expired, Commentf(tc.name))
		if !tc.wantErr {
			c.Assert(got, Equals, txid)
		}
		c.Assert(s.client.signerCacheManager.HasSigned(txOut.CacheHash()), Equals, !tc.wantErr, Commentf(tc.name))
		c.Assert(broadcasts.Load(), Equals, tc.broadcasts, Commentf(tc.name))
		c.Assert(lookups.Load(), Equals, tc.lookups, Commentf(tc.name))
		server.Close()
	}
}
