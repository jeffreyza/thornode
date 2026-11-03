package utxo

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/txscript"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	dashchaincfg "github.com/dashpay/dashd-go/chaincfg"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"

	dogeec "github.com/eager7/dogd/btcec"
	dogechaincfg "github.com/eager7/dogd/chaincfg"
	dogewire "github.com/eager7/dogd/wire"
	"github.com/eager7/dogutil"
	dogetxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/dogd-txscript"

	"github.com/gcash/bchd/bchec"
	bchchaincfg "github.com/gcash/bchd/chaincfg"
	bchwire "github.com/gcash/bchd/wire"
	"github.com/gcash/bchutil"
	bchtxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/bchd-txscript"

	ltcec "github.com/ltcsuite/ltcd/btcec"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"
	ltcwire "github.com/ltcsuite/ltcd/wire"
	"github.com/ltcsuite/ltcutil"
	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Client
////////////////////////////////////////////////////////////////////////////////////////

//go:generate go run utxo_generate.go

type Client struct {
	sync.Mutex
	chain common.Chain
	rpc   *rpc.Client

	keys    *thorclient.Keys
	privKey *btcec.PrivateKey
	pubKey  common.PubKey
	address common.Address

	// chain specific
	mtx               sync.Mutex
	spentUtxos        map[string]bool
	pendingSpentUtxos map[string][]string
}

var _ LiteChainClient = &Client{}

const utxoStartupRPCTimeout = 5 * time.Second

var (
	utxoBroadcastAttempts   = 3
	utxoBroadcastRetryDelay = 2 * time.Second
)

var utxoStartupImportLocks sync.Map

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	// create rpc client
	retries := 5
	rpc, err := rpc.NewClient(host, "thorchain", "password", retries, utxoStartupRPCTimeout, chain, zerolog.Nop())
	if err != nil {
		return nil, fmt.Errorf("fail to create rpc client: %w", err)
	}

	// extract the private key
	privateKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}
	privKey, _ := btcec.PrivKeyFromBytes(btcec.S256(), privateKey.Bytes())

	// derive the public key
	buf := privKey.PubKey().SerializeCompressed()
	pk := secp256k1.PubKey(buf)
	pubkey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to create pubkey: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubkey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	// import address to wallet, rescan if master account
	if !chain.Equals(common.ZECChain) {
		importLock := getUTXOStartupImportLock(chain)
		importLock.Lock()
		imported, importedErr := rpc.IsAddressImported(address.String())
		if importedErr != nil {
			importLock.Unlock()
			return nil, fmt.Errorf("fail to query address info(%s): %w", address, importedErr)
		}
		if !imported {
			if keys.GetSignerInfo().Name == "master" {
				err = rpc.ImportAddressRescan(address.String())
			} else {
				err = rpc.ImportAddress(address.String())
			}
		}
		importLock.Unlock()

		if err != nil {
			return nil, fmt.Errorf("fail to import address(%s): %w", address, err)
		}
	}

	return &Client{
		chain:             chain,
		rpc:               rpc,
		keys:              keys,
		privKey:           privKey,
		pubKey:            pubkey,
		address:           address,
		spentUtxos:        make(map[string]bool),
		pendingSpentUtxos: make(map[string][]string),
		mtx:               sync.Mutex{},
	}, nil
}

func getUTXOStartupImportLock(chain common.Chain) *sync.Mutex {
	if lock, ok := utxoStartupImportLocks.Load(chain); ok {
		return lock.(*sync.Mutex)
	}

	lock := &sync.Mutex{}
	actual, _ := utxoStartupImportLocks.LoadOrStore(chain, lock)
	return actual.(*sync.Mutex)
}

////////////////////////////////////////////////////////////////////////////////////////
// GetAccount
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	// default to the client key address
	var err error
	addr := c.address
	if pk != nil {
		addr, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	// get unspent utxos
	utxos, err := c.rpc.ListUnspent(addr.String())
	if err != nil {
		return nil, fmt.Errorf("fail to get UTXOs: %w", err)
	}

	// sum balance
	total := 0.0
	for _, item := range utxos {
		total += item.Amount
	}
	totalAmt, err := btcutil.NewAmount(total)
	if err != nil {
		return nil, fmt.Errorf("fail to convert total amount: %w", err)
	}

	// create account
	coin := common.NewCoin(c.chain.GetGasAsset(), cosmos.NewUint(uint64(totalAmt)))
	a := common.NewAccount(0, 0, common.NewCoins(coin), false)

	return &a, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// SignTx
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) SignContractTx(SimContractTx) ([]byte, error) {
	return nil, fmt.Errorf("not implemented")
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// never send amount less than dust threshold
	if tx.Chain.DustThreshold().GTE(tx.Coin.Amount) {
		return nil, fmt.Errorf("amount %s is below dust threshold %s", tx.Coin.Amount.String(), tx.Chain.DustThreshold().String())
	}

	sourceScript, err := c.getSourceScript(tx)
	if err != nil {
		return nil, fmt.Errorf("fail to get source pay to address script: %w", err)
	}

	// build transaction
	redeemTx, amounts, err := c.buildTx(tx, sourceScript)
	if err != nil {
		return nil, err
	}

	if len(redeemTx.TxIn) == 0 {
		return nil, fmt.Errorf("tx has no vin")
	}

	// create the list of signing requests
	signings := []struct{ idx, amount int64 }{}
	totalAmount := int64(0)
	for idx, txIn := range redeemTx.TxIn {
		key := fmt.Sprintf("%s-%d", txIn.PreviousOutPoint.Hash, txIn.PreviousOutPoint.Index)
		outputAmount := amounts[key]
		totalAmount += outputAmount
		signings = append(signings, struct{ idx, amount int64 }{int64(idx), outputAmount})
	}

	// convert the wire tx to the chain specific tx for signing
	var stx interface{}
	switch c.chain {
	case common.DOGEChain:
		stx = wireToDOGE(redeemTx)
	case common.BCHChain:
		stx = wireToBCH(redeemTx)
	case common.LTCChain:
		stx = wireToLTC(redeemTx)
	case common.BTCChain:
		stx = wireToBTC(redeemTx)
	case common.DASHChain:
		// Dash tx format is Bitcoin-compatible (legacy, non-SegWit), so we
		// sign the btcwire.MsgTx directly without a separate wire conversion.
		stx = redeemTx
	case common.ZECChain:
		// signing is slightly different, using zecTx, no need to wire
	default:
		log.Fatal().Msg("unsupported chain")
	}

	// only used for zcash
	var height int64
	var zecTx *zcash.Tx
	var zecHashes *zcash.SigHashes
	if c.chain.Equals(common.ZECChain) {
		height, err = c.rpc.GetBlockCount()
		if err != nil {
			return nil, fmt.Errorf("fail to get block count: %w", err)
		}

		var consensus zcash.ConsensusState
		if err := c.rpc.Call(&consensus, "getblockchaininfo"); err != nil {
			return nil, err
		}
		if height < 0 || height >= 5e8 {
			return nil, fmt.Errorf("invalid ZEC height")
		}
		branch, expiry, err := consensus.PaymentState(uint32(height), 20)
		if err != nil {
			return nil, err
		}
		redeemTx.Version = 6
		zecTx = &zcash.Tx{MsgTx: redeemTx, ExpiryHeight: expiry, ConsensusBranchID: branch}
		inputs := make([]*btcwire.TxOut, len(redeemTx.TxIn))
		for i, input := range redeemTx.TxIn {
			key := fmt.Sprintf("%s-%d", input.PreviousOutPoint.Hash, input.PreviousOutPoint.Index)
			amount, exists := amounts[key]
			if !exists || amount <= 0 {
				return nil, fmt.Errorf("invalid ZEC input amount: %s", key)
			}
			inputs[i] = btcwire.NewTxOut(amount, sourceScript)
		}
		zecHashes, err = zcash.NewSigHashes(zecTx, inputs)
		if err != nil {
			return nil, err
		}
	}

	// sign the tx
	wg := &sync.WaitGroup{}
	wg.Add(len(signings))
	mu := &sync.Mutex{}
	var utxoErr error
	for _, signing := range signings {
		go func(i int, amount int64) {
			defer wg.Done()

			var err error
			switch c.chain {
			case common.DOGEChain:
				err = c.signUTXODOGE(stx.(*dogewire.MsgTx), amount, sourceScript, i)
			case common.BCHChain:
				err = c.signUTXOBCH(stx.(*bchwire.MsgTx), amount, sourceScript, i)
			case common.LTCChain:
				err = c.signUTXOLTC(stx.(*ltcwire.MsgTx), amount, sourceScript, i)
			case common.BTCChain:
				err = c.signUTXOBTC(stx.(*btcwire.MsgTx), amount, sourceScript, i)
			case common.DASHChain:
				err = c.signUTXODASH(stx.(*btcwire.MsgTx), amount, sourceScript, i)
			case common.ZECChain:
				err = c.signUTXOZEC(zecTx, zecHashes, i)
			default:
				log.Fatal().Msg("unsupported chain")
			}

			if err != nil {
				mu.Lock()
				utxoErr = multierror.Append(utxoErr, err)
				mu.Unlock()
			}
		}(int(signing.idx), signing.amount)
	}
	wg.Wait()
	if utxoErr != nil {
		return nil, fmt.Errorf("fail to sign the message: %w", err)
	}

	// convert back to wire tx
	switch c.chain {
	case common.DOGEChain:
		redeemTx = dogeToWire(stx.(*dogewire.MsgTx))
	case common.BCHChain:
		redeemTx = bchToWire(stx.(*bchwire.MsgTx))
	case common.LTCChain:
		redeemTx = ltcToWire(stx.(*ltcwire.MsgTx))
	case common.BTCChain:
		redeemTx = btcToWire(stx.(*btcwire.MsgTx))
	case common.DASHChain:
		// stx is already the same *btcwire.MsgTx as redeemTx; nothing to convert.
		redeemTx = stx.(*btcwire.MsgTx)
	case common.ZECChain:
		// using zecTx directly, no need to convert back
	default:
		log.Fatal().Msg("unsupported chain")
	}

	// calculate the final transaction size
	var signedTx bytes.Buffer
	switch c.chain {
	case common.ZECChain:
		err = zecTx.Serialize(&signedTx)
	default:
		err = redeemTx.Serialize(&signedTx)
	}

	if err != nil {
		return nil, fmt.Errorf("fail to serialize tx to bytes: %w", err)
	}

	c.rememberPendingSpentUtxos(signedTx.Bytes(), amounts)
	return signedTx.Bytes(), nil
}

////////////////////////////////////////////////////////////////////////////////////////
// BroadcastTx
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) BroadcastTx(payload []byte) (string, error) {
	if c.chain.Equals(common.ZECChain) {
		var hash string
		payloadHex := hex.EncodeToString(payload)
		tx, err := zcash.ParseTx(payload)
		if err != nil {
			return "", err
		}
		id, err := tx.ID()
		if err != nil {
			return "", err
		}
		txID := id.String()
		args := []any{payloadHex}
		err = c.rpc.Call(&hash, "sendrawtransaction", args...)
		if err == nil && !strings.EqualFold(hash, txID) {
			err = fmt.Errorf("unexpected ZEC broadcast txid: got %q, expected %s", hash, txID)
		}
		if err != nil {
			// Zebra may requeue verification after cancelling the RPC. Check
			// the original payload in the mempool/chain before retrying it.
			if accepted, _ := c.payloadAccepted(txID, payloadHex); accepted {
				c.finalizePendingSpentUtxos(payloadHex)
				return txID, nil
			}
			// Keep these inputs reserved on an ambiguous result, including
			// exhausted retries. They become spent only after acceptance; a
			// failed simulation must not reuse possibly consumed inputs.
			return "", err
		}
		c.finalizePendingSpentUtxos(payloadHex)
		return hash, nil
	}

	redeemTx := btcwire.NewMsgTx(btcwire.TxVersion)
	buf := bytes.NewBuffer(payload)
	if err := redeemTx.Deserialize(buf); err != nil {
		return "", fmt.Errorf("fail to deserialize payload: %w", err)
	}

	var maxFee any
	switch c.chain {
	case common.DOGEChain, common.BCHChain:
		maxFee = true // "allowHighFees"
	case common.LTCChain, common.BTCChain, common.DASHChain:
		maxFee = 10_000_000
	}

	// Rebroadcasting identical bytes is idempotent, so transport failures reuse
	// this payload. Signing again after a timed-out broadcast that the node
	// accepted anyway spends other inputs and sends a duplicate inbound.
	txID := redeemTx.TxHash().String()
	payloadHex := hex.EncodeToString(payload)
	var err error
	for attempt := 1; attempt <= utxoBroadcastAttempts; attempt++ {
		var hash string
		hash, err = c.rpc.SendRawTransaction(redeemTx, maxFee)
		if err == nil {
			return hash, nil
		}
		accepted, absent := c.payloadAccepted(txID, payloadHex)
		if accepted {
			return txID, nil
		}

		// the node answered: an earlier attempt was mined, or the payload was
		// rejected. Rejection is final only when the node also denies holding
		// the txid, since an earlier attempt or a transport retry inside
		// rpc.Call may have been accepted without an answer. Otherwise resend.
		var rpcErr *btcjson.RPCError
		if errors.As(err, &rpcErr) {
			if rpcErr.Code == btcjson.ErrRPCTxAlreadyInChain {
				return txID, nil
			}
			if absent {
				return "", fmt.Errorf("fail to broadcast %s tx %s: %w", c.chain, txID, err)
			}
		}
		if attempt < utxoBroadcastAttempts {
			time.Sleep(utxoBroadcastRetryDelay)
		}
	}
	return txID, fmt.Errorf("%w: %s tx %s not found after %d broadcast attempts: %w",
		ErrSimTxUncertain, c.chain, txID, utxoBroadcastAttempts, err)
}

// payloadAccepted reports whether the node already holds the exact payload in
// its mempool or chain, and whether the node answered that it has no such txid.
// absent also rules out mined transactions only with -txindex, which the
// mocknet DOGE and LTC daemons enable.
func (c *Client) payloadAccepted(txid, payloadHex string) (accepted, absent bool) {
	raw, err := c.rpc.GetRawTransaction(txid)
	if err == nil {
		return strings.EqualFold(raw, payloadHex), false
	}
	var rpcErr *btcjson.RPCError
	return false, errors.As(err, &rpcErr) && rpcErr.Code == btcjson.ErrRPCNoTxInfo
}

////////////////////////////////////////////////////////////////////////////////////////
// Internal
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) getSourceScript(tx SimTx) ([]byte, error) {
	switch c.chain {
	case common.DOGEChain:
		var addr dogutil.Address
		addr, err := dogutil.DecodeAddress(c.address.String(), c.getChainCfgDOGE())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return dogetxscript.PayToAddrScript(addr)
	case common.BCHChain:
		var addr bchutil.Address
		addr, err := bchutil.DecodeAddress(c.address.String(), c.getChainCfgBCH())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return bchtxscript.PayToAddrScript(addr)
	case common.LTCChain:
		var addr ltcutil.Address
		addr, err := ltcutil.DecodeAddress(c.address.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return ltctxscript.PayToAddrScript(addr)
	case common.BTCChain:
		var addr btcutil.Address
		addr, err := btcutil.DecodeAddress(c.address.String(), c.getChainCfgBTC())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return btctxscript.PayToAddrScript(addr)
	case common.DASHChain:
		addr, err := dashutil.DecodeAddress(c.address.String(), c.getChainCfgDASH())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return dashtxscript.PayToAddrScript(addr)
	case common.ZECChain:
		params := c.getChainCfgZEC()
		addr, err := zcash.DecodeAddress(c.address.String(), params.Name)
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", c.address, err)
		}
		return addr.Script()
	default:
		log.Fatal().Msg("unsupported chain")
		return nil, nil
	}
}

func (c *Client) getUtxoToSpend(total float64) ([]btcjson.ListUnspentResult, error) {
	utxos, err := c.rpc.ListUnspent(c.address.String())
	if err != nil {
		return nil, fmt.Errorf("fail to get UTXOs: %w", err)
	}

	// spend UTXOs older to younger
	sort.SliceStable(utxos, func(i, j int) bool {
		if utxos[i].Confirmations > utxos[j].Confirmations {
			return true
		} else if utxos[i].Confirmations < utxos[j].Confirmations {
			return false
		}
		return utxos[i].TxID < utxos[j].TxID
	})

	// collect UTXOs to spend
	var result []btcjson.ListUnspentResult
	var toSpend float64

	if c.chain.Equals(common.ZECChain) {
		c.mtx.Lock()
		defer c.mtx.Unlock()
	}

	for _, item := range utxos {
		if c.chain.Equals(common.ZECChain) {
			key := fmt.Sprintf("%s-%d", item.TxID, item.Vout)

			if c.spentUtxos[key] || c.pendingSpentUtxoLocked(key) {
				continue
			}

		}

		result = append(result, item)
		toSpend += item.Amount
		if toSpend >= total {
			break
		}
	}

	// error if there is insufficient balance
	if toSpend < total {
		return nil, fmt.Errorf("insufficient balance: %f < %f", toSpend, total)
	}

	return result, nil
}

// GetGasSats returns the default fixed fee, in satoshis / smallest-unit base
// units, used by simulation-generated UTXO transactions when the scenario does
// not provide an explicit SimTx.FeeSats override. These are intentionally
// simple per-chain constants rather than live RPC fee estimates, so test
// scenarios stay deterministic.
func (c *Client) GetGasSats() uint64 {
	switch c.chain {
	case common.DOGEChain:
		return 10_000_000
	case common.BCHChain:
		return 10_000
	case common.LTCChain:
		return 10_000
	case common.BTCChain:
		return 10_000
	case common.ZECChain:
		return 100_000
	case common.DASHChain:
		// Fixed fee for ordinary Dash simulation transactions; InstantSend has no extra fee gate.
		// Scenarios that need a lower fee override FeeSats directly.
		return 15_000
	default:
		log.Fatal().Msg("unsupported chain")
		return 0
	}
}

// getPaymentAmount returns the total output-side payment value in whole-coin
// units for RPC calls that expect float amounts. It adds the transfer amount
// and the selected fixed fee (default or SimTx.FeeSats override), both in base
// units, then converts the result by dividing through common.One.
func (c *Client) getPaymentAmount(tx SimTx) float64 {
	feeSats := c.GetGasSats()
	if tx.FeeSats > 0 {
		feeSats = tx.FeeSats
	}

	totalSats := tx.Coin.Amount.Uint64() + feeSats
	if memoScripts, err := c.getMemoScripts(tx.Memo); err == nil && len(memoScripts) > 1 {
		totalSats += uint64(len(memoScripts)-1) * uint64(c.chain.P2WPKHOutputValue())
	}
	return float64(totalSats) / float64(common.One)
}

func (c *Client) getMemoScripts(memo string) ([][]byte, error) {
	switch c.chain {
	case common.DOGEChain:
		return memoToScripts(memo, dogetxscript.MaxDataCarrierSize, dogetxscript.NullDataScript, dogetxscript.PayToWitnessScript)
	case common.BCHChain:
		return memoToScripts(memo, bchtxscript.MaxDataCarrierSize, bchtxscript.NullDataScript, bchtxscript.PayToWitnessScript)
	case common.LTCChain:
		return memoToScripts(memo, ltctxscript.MaxDataCarrierSize, ltctxscript.NullDataScript, ltctxscript.PayToWitnessScript)
	case common.BTCChain:
		return memoToScripts(memo, btctxscript.MaxDataCarrierSize, btctxscript.NullDataScript, btctxscript.PayToWitnessScript)
	case common.DASHChain:
		return memoToScripts(memo, dashtxscript.MaxDataCarrierSize, dashtxscript.NullDataScript, zcash.PayToPubKeyHashScript)
	case common.ZECChain:
		return memoToScripts(memo, btctxscript.MaxDataCarrierSize, btctxscript.NullDataScript, zcash.PayToPubKeyHashScript)
	default:
		log.Fatal().Msg("unsupported chain")
		return nil, nil
	}
}

func memoToScripts(memo string, maxDataCarrierSize int, nullDataScript, payToContinuationScript func([]byte) ([]byte, error)) ([][]byte, error) {
	if len(memo) == 0 {
		return nil, nil
	}

	if len(memo) > constants.MaxMemoSize {
		return nil, fmt.Errorf("memo size %d exceeds maximum size of %d bytes", len(memo), constants.MaxMemoSize)
	}

	data := []byte(memo)
	remainingDataSize := len(data)
	if remainingDataSize > maxDataCarrierSize {
		remainingDataSize -= (maxDataCarrierSize - 1)
	} else {
		remainingDataSize = 0
	}

	numScripts := 1 + (remainingDataSize+19)/20
	scripts := make([][]byte, 0, numScripts)

	firstChunkSize := len(data)
	continuation := false
	if firstChunkSize > maxDataCarrierSize {
		firstChunkSize = maxDataCarrierSize - 1
		continuation = true
	}

	firstChunk := make([]byte, 0, maxDataCarrierSize)
	firstChunk = append(firstChunk, data[:firstChunkSize]...)
	if continuation {
		firstChunk = append(firstChunk, '^')
	}

	script, err := nullDataScript(firstChunk)
	if err != nil {
		return nil, fmt.Errorf("fail to create OP_RETURN script: %w", err)
	}
	scripts = append(scripts, script)

	if continuation {
		remainingData := data[firstChunkSize:]
		for i := 0; len(remainingData) > 0; i++ {
			chunkSize := len(remainingData)
			if chunkSize > 20 {
				chunkSize = 20
			}

			hash := make([]byte, 20)
			copy(hash, remainingData[:chunkSize])

			continuationScript, err := payToContinuationScript(hash)
			if err != nil {
				return nil, fmt.Errorf("fail to create memo continuation script at index %d: %w", i, err)
			}
			scripts = append(scripts, continuationScript)
			remainingData = remainingData[chunkSize:]
		}
	}

	return scripts, nil
}

func (c *Client) rememberPendingSpentUtxos(payload []byte, amounts map[string]int64) {
	if !c.chain.Equals(common.ZECChain) || len(amounts) == 0 {
		return
	}

	keys := make([]string, 0, len(amounts))
	for key := range amounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	c.mtx.Lock()
	defer c.mtx.Unlock()
	if c.pendingSpentUtxos == nil {
		c.pendingSpentUtxos = make(map[string][]string)
	}
	c.pendingSpentUtxos[hex.EncodeToString(payload)] = keys
}

func (c *Client) finalizePendingSpentUtxos(payloadHex string) {
	if !c.chain.Equals(common.ZECChain) {
		return
	}

	c.mtx.Lock()
	defer c.mtx.Unlock()
	keys := c.pendingSpentUtxos[payloadHex]
	delete(c.pendingSpentUtxos, payloadHex)

	for _, key := range keys {
		if c.spentUtxos == nil {
			c.spentUtxos = make(map[string]bool)
		}
		c.spentUtxos[key] = true
	}
}

func (c *Client) pendingSpentUtxoLocked(key string) bool {
	for _, keys := range c.pendingSpentUtxos {
		for _, pendingKey := range keys {
			if pendingKey == key {
				return true
			}
		}
	}
	return false
}

func (c *Client) buildTx(tx SimTx, sourceScript []byte) (
	*btcwire.MsgTx, map[string]int64, error,
) {
	txes, err := c.getUtxoToSpend(c.getPaymentAmount(tx))
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get unspent UTXO: %w", err)
	}
	redeemTx := wire.NewMsgTx(wire.TxVersion)
	totalAmt := int64(0)
	individualAmounts := make(map[string]int64, len(txes))
	for _, item := range txes {
		var txID *chainhash.Hash
		txID, err = chainhash.NewHashFromStr(item.TxID)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to parse txID(%s): %w", item.TxID, err)
		}
		// double check that the utxo is still valid
		outputPoint := wire.NewOutPoint(txID, item.Vout)
		sourceTxIn := wire.NewTxIn(outputPoint, nil, nil)
		redeemTx.AddTxIn(sourceTxIn)
		var amt btcutil.Amount
		amt, err = btcutil.NewAmount(item.Amount)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to parse amount(%f): %w", item.Amount, err)
		}
		individualAmounts[fmt.Sprintf("%s-%d", txID, item.Vout)] = int64(amt)
		totalAmt += int64(amt)
	}

	var buf []byte
	switch c.chain {
	case common.DOGEChain:
		var outputAddr dogutil.Address
		outputAddr, err = dogutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgDOGE())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = dogetxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.BCHChain:
		var outputAddr bchutil.Address
		outputAddr, err = bchutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgBCH())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = bchtxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.LTCChain:
		var outputAddr ltcutil.Address
		outputAddr, err = ltcutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = ltctxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.BTCChain:
		var outputAddr btcutil.Address
		outputAddr, err = btcutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgBTC())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = btctxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.DASHChain:
		outputAddr, err := dashutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgDASH())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = dashtxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	case common.ZECChain:
		var outputAddr *zcash.Address
		outputAddr, err = zcash.DecodeAddress(tx.ToAddress.String(), c.getChainCfgZEC().Name)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = outputAddr.Script()
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
	default:
		log.Fatal().Msg("unsupported chain")
	}

	// pay to customer
	redeemTxOut := wire.NewTxOut(int64(tx.Coin.Amount.Uint64()), buf)
	redeemTx.AddTxOut(redeemTxOut)

	memoScripts, err := c.getMemoScripts(tx.Memo)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to generate memo scripts: %w", err)
	}

	p2wpkhOutputsCost := int64(0)
	if len(memoScripts) > 1 {
		p2wpkhOutputsCost = int64(len(memoScripts)-1) * c.chain.P2WPKHOutputValue()
	}

	// add output to pay the balance back ourselves
	feeSats := int64(c.GetGasSats())
	if tx.FeeSats > 0 {
		feeSats = int64(tx.FeeSats)
	}
	balance := totalAmt - redeemTxOut.Value - feeSats - p2wpkhOutputsCost
	if balance < 0 {
		return nil, nil, fmt.Errorf("not enough balance to pay customer: %d", balance)
	}
	if balance > 0 {
		redeemTx.AddTxOut(wire.NewTxOut(balance, sourceScript))
	}

	if len(memoScripts) > 0 {
		redeemTx.AddTxOut(wire.NewTxOut(0, memoScripts[0]))
		for _, script := range memoScripts[1:] {
			redeemTx.AddTxOut(wire.NewTxOut(c.chain.P2WPKHOutputValue(), script))
		}
	}

	return redeemTx, individualAmounts, nil
}

// ------------------------------ chain config ------------------------------

func (c *Client) getChainCfgBTC() *btcchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &btcchaincfg.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &btcchaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

func (c *Client) getChainCfgLTC() *ltcchaincfg.Params {
	cn := common.CurrentChainNetwork
	switch cn {
	case common.MockNet:
		return &ltcchaincfg.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &ltcchaincfg.MainNetParams
	}
	return nil
}

func (c *Client) getChainCfgBCH() *bchchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &bchchaincfg.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &bchchaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

func (c *Client) getChainCfgDOGE() *dogechaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &dogechaincfg.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &dogechaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

func (c *Client) getChainCfgZEC() *btcchaincfg.Params {
	return &btcchaincfg.Params{Name: common.ZECNetworkName()}
}

// getChainCfgDASH selects Dash parameters for the configured simulation network.
func (c *Client) getChainCfgDASH() *dashchaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return &dashchaincfg.RegressionNetParams
	case common.MainNet, common.StageNet, common.ChainNet:
		return &dashchaincfg.MainNetParams
	default:
		log.Fatal().Msg("unsupported network")
		return nil
	}
}

// ------------------------------ signing ------------------------------

func (c *Client) signUTXODOGE(redeemTx *dogewire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	signable := dogetxscript.NewPrivateKeySignable((*dogeec.PrivateKey)(c.privKey))
	sig, err := dogetxscript.RawTxInSignature(redeemTx, idx, sourceScript, dogetxscript.SigHashAll, signable)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	pkData := signable.GetPubKey().SerializeCompressed()
	sigscript, err := dogetxscript.NewScriptBuilder().AddData(sig).AddData(pkData).Script()
	if err != nil {
		return fmt.Errorf("fail to build signature script: %w", err)
	}
	redeemTx.TxIn[idx].SignatureScript = sigscript
	flag := dogetxscript.StandardVerifyFlags
	engine, err := dogetxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOBCH(redeemTx *bchwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	signable := bchtxscript.NewPrivateKeySignable((*bchec.PrivateKey)(c.privKey))
	sig, err := bchtxscript.RawTxInECDSASignature(redeemTx, idx, sourceScript, bchtxscript.SigHashAll, signable, amount)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	pkData := signable.GetPubKey().SerializeCompressed()
	sigscript, err := bchtxscript.NewScriptBuilder().AddData(sig).AddData(pkData).Script()
	if err != nil {
		return fmt.Errorf("fail to build signature script: %w", err)
	}
	redeemTx.TxIn[idx].SignatureScript = sigscript
	flag := bchtxscript.StandardVerifyFlags
	engine, err := bchtxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOLTC(redeemTx *ltcwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	sigHashes := ltctxscript.NewTxSigHashes(redeemTx)
	signable := ltctxscript.NewPrivateKeySignable((*ltcec.PrivateKey)(c.privKey))
	witness, err := ltctxscript.WitnessSignature(redeemTx, sigHashes, idx, amount, sourceScript, ltctxscript.SigHashAll, signable, true)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	redeemTx.TxIn[idx].Witness = witness
	flag := ltctxscript.StandardVerifyFlags
	engine, err := ltctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOBTC(redeemTx *btcwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	sigHashes := btctxscript.NewTxSigHashes(redeemTx)
	signable := btctxscript.NewPrivateKeySignable(c.privKey)
	witness, err := btctxscript.WitnessSignature(redeemTx, sigHashes, idx, amount, sourceScript, btctxscript.SigHashAll, signable, true)
	if err != nil {
		return fmt.Errorf("fail to get witness: %w", err)
	}

	redeemTx.TxIn[idx].Witness = witness
	flag := btctxscript.StandardVerifyFlags
	engine, err := btctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create engine: %w", err)
	}
	if err = engine.Execute(); err != nil {
		return fmt.Errorf("fail to execute the script: %w", err)
	}
	return nil
}

// signUTXODASH signs one legacy Dash input and verifies its signature script locally.
func (c *Client) signUTXODASH(redeemTx *btcwire.MsgTx, amount int64, sourceScript []byte, idx int) error {
	// Dash uses Bitcoin-compatible legacy ECDSA signing (no SegWit).
	signable := btctxscript.NewPrivateKeySignable(c.privKey)
	sig, err := btctxscript.RawTxInSignature(redeemTx, idx, sourceScript, btctxscript.SigHashAll, signable)
	if err != nil {
		return fmt.Errorf("fail to get signature: %w", err)
	}

	pkData := signable.GetPubKey().SerializeCompressed()
	sigScript, err := btctxscript.NewScriptBuilder().AddData(sig).AddData(pkData).Script()
	if err != nil {
		return fmt.Errorf("fail to build signature script: %w", err)
	}
	redeemTx.TxIn[idx].SignatureScript = sigScript

	flag := btctxscript.StandardVerifyFlags
	engine, err := btctxscript.NewEngine(sourceScript, redeemTx, idx, flag, nil, nil, amount)
	if err != nil {
		return fmt.Errorf("fail to create script engine: %w", err)
	}
	err = engine.Execute()
	if err != nil {
		return fmt.Errorf("fail to verify signature script: %w", err)
	}
	return nil
}

func (c *Client) signUTXOZEC(zecTx *zcash.Tx, hashes *zcash.SigHashes, idx int) (err error) {
	signable := btctxscript.NewPrivateKeySignable(c.privKey)

	blakeHash, err := hashes.SignatureHash(idx)
	if err != nil {
		return err
	}

	signature, err := signable.Sign(blakeHash)
	if err != nil {
		return err
	}

	pkData := signable.GetPubKey().SerializeCompressed()

	sigScript, err := btctxscript.NewScriptBuilder().
		AddData(append(signature.Serialize(), byte(txscript.SigHashAll))).
		AddData(pkData).
		Script()
	if err != nil {
		return err
	}

	zecTx.TxIn[idx].SignatureScript = sigScript

	return nil
}
