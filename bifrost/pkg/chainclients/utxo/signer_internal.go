package utxo

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
	dashbtcutil "github.com/dashpay/dashd-go/btcutil"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/pivxutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"

	"github.com/eager7/dogutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	dogetxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/dogd-txscript"
	"gitlab.com/thorchain/thornode/v3/constants"

	"github.com/gcash/bchutil"
	bchtxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/bchd-txscript"

	"github.com/ltcsuite/ltcutil"
	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"

	"github.com/btcsuite/btcutil"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// UTXO Selection
////////////////////////////////////////////////////////////////////////////////////////

func (c *Client) getMaximumUtxosToSpend() int64 {
	const mimirMaxUTXOsToSpend = `MaxUTXOsToSpend`
	utxosToSpend, err := c.bridge.GetMimir(mimirMaxUTXOsToSpend)
	if err != nil {
		c.log.Err(err).Msg("fail to get MaxUTXOsToSpend")
	}
	if utxosToSpend <= 0 {
		utxosToSpend = c.cfg.UTXO.MaxUTXOsToSpend
	}
	return utxosToSpend
}

// getUtxoToSpend will iterate unspend utxos for the given address and return the oldest
// set of utxos that can cover the total.
//
// A positive floor (at most total) relaxes that requirement: when every eligible UTXO
// together falls short of total but still covers floor, all of them are returned. An
// outbound's total includes its MaxGas, which only bounds the fee, so a vault holding
// less than amount + MaxGas may still afford amount + the actual fee; buildTx checks
// that once the fee is known. A floor of zero requires the full total.
func (c *Client) getUtxoToSpend(pubKey common.PubKey, total, floor btcutil.Amount) ([]btcjson.ListUnspentResult, error) {
	// get all unspent utxos
	addr, err := pubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pubKey, err)
	}
	utxos, err := c.rpc.ListUnspent(addr.String())
	if err != nil {
		return nil, fmt.Errorf("fail to get UTXOs: %w", err)
	}

	// spend UTXO older to younger
	sort.SliceStable(utxos, func(i, j int) bool {
		if utxos[i].Confirmations > utxos[j].Confirmations {
			return true
		} else if utxos[i].Confirmations < utxos[j].Confirmations {
			return false
		}
		return utxos[i].TxID < utxos[j].TxID
	})

	var result []btcjson.ListUnspentResult
	var toSpend btcutil.Amount
	var excluded utxoExclusions
	minUTXOAmt := btcutil.Amount(c.cfg.ChainID.DustThreshold().Uint64()).ToBTC()
	utxosToSpend := c.getMaximumUtxosToSpend() // can be set by mimir

	for _, item := range utxos {
		if !c.isValidUTXO(item.ScriptPubKey) {
			c.log.Warn().Str("script", item.ScriptPubKey).Msgf("invalid utxo, unable to spend")
			excluded.invalidScript++
			continue
		}

		// analyze-ignore(float-comparison)
		belowMinimum := item.Confirmations < c.cfg.UTXO.MinUTXOConfirmations || item.Amount < minUTXOAmt
		if belowMinimum {
			// Local Dash lock visibility must not change signer inputs. Preserve
			// the existing confirmation/dust exceptions for vault transactions.
			if !c.isSelfTransaction(item.TxID) && !c.isFromAsgard(item.TxID) {
				excluded.unconfirmedOrDust++
				continue
			}
		}

		// For unconfirmed UTXOs (even self-transactions), check ancestor, descendant, and combined
		// chain count to avoid exceeding the chain's mempool limits. Set to 0 to disable.
		checkMempoolLimits := item.Confirmations == 0 && c.cfg.UTXO.MaxMempoolAncestors > 0
		if checkMempoolLimits && (belowMinimum || c.cfg.ChainID == common.DASHChain) {
			var entry *btcjson.GetMempoolEntryResult
			entry, err = c.rpc.GetMempoolEntry(item.TxID)
			if err != nil {
				if c.cfg.ChainID == common.DASHChain {
					return nil, fmt.Errorf("check Dash input mempool limits: %w", err)
				}
				// If we cannot get the mempool entry, the tx is likely confirmed.
				c.log.Debug().Err(err).Str("txid", item.TxID).Msg("fail to get mempool entry")
			}

			// Check the combined ancestor and descendant counts to avoid exceeding mempool
			// limits and receiving "-26: too-long-mempool-chain" errors on broadcast.
			if err == nil && entry.AncestorCount+entry.DescendantCount >= c.cfg.UTXO.MaxMempoolAncestors {
				c.log.Warn().
					Str("txid", item.TxID).
					Int64("ancestor_count", entry.AncestorCount).
					Int64("descendant_count", entry.DescendantCount).
					Int64("max_allowed", c.cfg.UTXO.MaxMempoolAncestors).
					Msg("skipping UTXO with too many ancestors/descendants to avoid mempool chain limit")
				excluded.mempoolChainLimit++
				continue
			}
		}

		if c.cfg.ChainID == common.ZECChain {
			id := formatUtxoKey(item.TxID, item.Vout)

			var found bool
			found, err = c.temporalStorage.HasSpentUtxo(id)
			if err != nil {
				c.log.Err(err).Msg("failed to check spent utxo")
				excluded.spentCheckFailed++
				continue
			}

			if found {
				excluded.alreadySpent++
				continue
			}
		}

		result = append(result, item)
		amt, err := btcutil.NewAmount(item.Amount)
		if err != nil {
			return nil, fmt.Errorf("fail to convert to btcutil amount: %w", err)
		}
		toSpend += amt

		// in the scenario that there are too many unspent utxos available, make sure it
		// doesn't spend too much as too much UTXO will cause huge pressure on TSS, also
		// make sure it will spend at least maxUTXOsToSpend so the UTXOs will be
		// consolidated
		if int64(len(result)) >= utxosToSpend && toSpend >= total {
			break
		}
	}

	// If we couldn't collect enough UTXOs to cover the required amount, return an error
	// to avoid confusing downstream errors about negative balance
	if toSpend < total {
		if floor <= 0 || toSpend < floor {
			need := total
			if floor > 0 {
				need = floor
			}
			return nil, insufficientUTXOsError(addr, need, toSpend, len(result), utxos, excluded)
		}
		c.log.Info().
			Int64("available", int64(toSpend)).
			Int64("total", int64(total)).
			Int64("floor", int64(floor)).
			Int("utxos", len(result)).
			Msg("eligible UTXOs cover the payment but not its full max gas, spending all of them")
	}

	return result, nil
}

// utxoExclusions counts listed UTXOs that getUtxoToSpend skipped, by reason.
type utxoExclusions struct {
	invalidScript     int
	unconfirmedOrDust int
	mempoolChainLimit int
	spentCheckFailed  int
	alreadySpent      int
}

// insufficientUTXOsError reports whether the daemon listed no UTXOs for the
// vault, meaning the coins are absent from the node's view, or listed UTXOs that
// selection excluded. The two cases need different investigations.
func insufficientUTXOsError(
	addr common.Address,
	need, selectedAmount btcutil.Amount,
	selectedCount int,
	listed []btcjson.ListUnspentResult,
	excluded utxoExclusions,
) error {
	if len(listed) == 0 {
		return fmt.Errorf("insufficient available UTXOs: need %d, node lists no UTXOs for %s "+
			"(coins absent from the node view: unfunded, not imported, or removed by a reorg)", need, addr)
	}

	var listedAmount btcutil.Amount
	for _, item := range listed {
		// best effort for diagnostics, an unparsable amount is left out of the total
		if amt, err := btcutil.NewAmount(item.Amount); err == nil {
			listedAmount += amt
		}
	}
	return fmt.Errorf("insufficient available UTXOs: need %d, only have %d available from %d UTXOs; "+
		"node lists %d UTXOs worth %d for %s, excluded: invalid script=%d, "+
		"unconfirmed or dust not from asgard=%d, mempool chain limit=%d, spent check failed=%d, already spent=%d",
		need, selectedAmount, selectedCount, len(listed), listedAmount, addr,
		excluded.invalidScript, excluded.unconfirmedOrDust, excluded.mempoolChainLimit,
		excluded.spentCheckFailed, excluded.alreadySpent)
}

func formatUtxoKey(txID string, vout uint32) string {
	return fmt.Sprintf("%s-%d", txID, vout)
}

// vinsUnspent will return true if all the vins are unspent.
func (c *Client) vinsUnspent(tx stypes.TxOutItem, vins []*wire.TxIn) (bool, error) {
	// get all unspent utxos
	addr, err := tx.VaultPubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		return false, fmt.Errorf("fail to get address from pubkey(%s): %w", tx.VaultPubKey, err)
	}
	utxos, err := c.rpc.ListUnspent(addr.String())
	if err != nil {
		return false, fmt.Errorf("fail to get UTXOs: %w", err)
	}
	unspent := make(map[string]bool, len(utxos))
	for _, utxo := range utxos {
		unspent[formatUtxoKey(utxo.TxID, utxo.Vout)] = true
	}

	// return false if any vin is spent
	allUnspent := true
	for _, vin := range vins {
		key := formatUtxoKey(vin.PreviousOutPoint.Hash.String(), vin.PreviousOutPoint.Index)
		if c.cfg.ChainID == common.ZECChain {
			var found bool
			found, err = c.temporalStorage.HasSpentUtxo(key)
			if err != nil {
				return false, fmt.Errorf("fail to check spent utxo(%s): %w", key, err)
			}

			if found {
				c.log.Warn().
					Stringer("in_hash", tx.InHash).
					Str("txid", vin.PreviousOutPoint.Hash.String()).
					Uint32("vout", vin.PreviousOutPoint.Index).
					Msg("vin is marked spent in local cache")
				allUnspent = false
				continue
			}
		}

		if !unspent[key] {
			c.log.Warn().
				Stringer("in_hash", tx.InHash).
				Str("txid", vin.PreviousOutPoint.Hash.String()).
				Uint32("vout", vin.PreviousOutPoint.Index).
				Msg("vin is spent")
			allUnspent = false
		}
	}

	return allUnspent, nil
}

// isSelfTransaction check the block meta to see whether the transactions is broadcast
// by ourselves if the transaction is broadcast by ourselves, then we should be able to
// spend the UTXO even it is still in mempool as such we could daisy chain the outbound
// transaction
func (c *Client) isSelfTransaction(txID string) bool {
	bms, err := c.temporalStorage.GetBlockMetas()
	if err != nil {
		c.log.Err(err).Msg("fail to get block metas")
		return false
	}
	for _, item := range bms {
		for _, tx := range item.SelfTransactions {
			if strings.EqualFold(tx, txID) {
				c.log.Debug().Msgf("%s is self transaction", txID)
				return true
			}
		}
	}
	return false
}

func (c *Client) getPaymentAmount(tx stypes.TxOutItem) btcutil.Amount {
	amtToPay := tx.Coins.GetCoin(c.cfg.ChainID.GetGasAsset()).Amount.Uint64()
	// ZEC gas is calculated differently, see getUtxoToSpend()
	if !tx.MaxGas.IsEmpty() && c.cfg.ChainID != common.ZECChain {
		gasAmt := tx.MaxGas.ToCoins().GetCoin(c.cfg.ChainID.GetGasAsset()).Amount
		amtToPay += gasAmt.Uint64()
	}
	return btcutil.Amount(amtToPay)
}

// getSourceScript retrieve pay to addr script from tx source
func (c *Client) getSourceScript(tx stypes.TxOutItem) ([]byte, error) {
	sourceAddr, err := tx.VaultPubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		return nil, fmt.Errorf("fail to get source address: %w", err)
	}

	switch c.cfg.ChainID {
	case common.DOGEChain:
		var addr dogutil.Address
		addr, err = dogutil.DecodeAddress(sourceAddr.String(), c.getChainCfgDOGE())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return dogetxscript.PayToAddrScript(addr)
	case common.BCHChain:
		var addr bchutil.Address
		addr, err = bchutil.DecodeAddress(sourceAddr.String(), c.getChainCfgBCH())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return bchtxscript.PayToAddrScript(addr)
	case common.LTCChain:
		var addr ltcutil.Address
		addr, err = ltctxscript.DecodeAddress(sourceAddr.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return ltctxscript.PayToAddrScript(addr)
	case common.BTCChain:
		var addr btcutil.Address
		addr, err = btcutil.DecodeAddress(sourceAddr.String(), c.getChainCfgBTC())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return btctxscript.PayToAddrScript(addr)
	case common.DASHChain:
		addr, err := dashutil.DecodeAddress(sourceAddr.String(), c.getChainCfgDASH())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return dashtxscript.PayToAddrScript(addr)
	case common.PIVXChain:
		addr, err := pivxutil.DecodeAddress(sourceAddr.String(), c.getChainCfgPIVX())
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return btctxscript.PayToAddrScript(addr)
	case common.ZECChain:
		params := c.getChainCfgZEC()
		addr, err := zcash.DecodeAddress(sourceAddr.String(), params.Name)
		if err != nil {
			return nil, fmt.Errorf("fail to decode source address(%s): %w", sourceAddr.String(), err)
		}
		return addr.Script()
	default:
		c.log.Fatal().Msg("unsupported chain")
		return nil, nil
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Build Transaction
////////////////////////////////////////////////////////////////////////////////////////

const (
	// estimateSigLen is the signature length (DER encoding plus the sighash type byte)
	// assumed when estimating the fee. A low-S secp256k1 signature is at most 72 bytes.
	estimateSigLen = 72

	// feeCapSigLen is the shortest signature length assumed when capping the fee rate.
	// DER drops leading zero bytes, so small r or s values shorten a signature by a byte
	// each (probability ~1/256 per byte). 67 bytes allows r and s to lose five bytes
	// between them, which leaves a negligible (~2^-38) chance per signature that the
	// signed transaction is smaller than the capped estimate.
	feeCapSigLen = 67
)

// estimateTxSize builds a dummy transaction with the given inputs and outputs and
// returns the exact virtual size (vbytes) according to BIP141.
// For non-segwit chains, it returns the actual serialized size.
// A nil changeScript estimates a transaction without a change output.
func (c *Client) estimateTxSize(txes []btcjson.ListUnspentResult, memoScripts [][]byte, customerScript, changeScript []byte) int64 {
	return c.estimateTxSizeWithSigLen(txes, memoScripts, customerScript, changeScript, estimateSigLen)
}

// maxFeeSats returns the largest fee that keeps the signed transaction within
// MaxSatsPerVByte. It sizes the transaction with the outputs it will contain and the
// shortest expected signatures, so the cap still holds against the final signed size.
func (c *Client) maxFeeSats(txes []btcjson.ListUnspentResult, memoScripts [][]byte, customerScript, changeScript []byte) int64 {
	satsPerVByte := c.cfg.UTXO.MaxSatsPerVByte
	switch c.cfg.ChainID {
	case common.BTCChain, common.LTCChain, common.DASHChain:
		// never exceed the maxfeerate BroadcastTx asks the daemon to enforce
		satsPerVByte = min(satsPerVByte, broadcastMaxSatsPerVByte)
	}
	return c.estimateTxSizeWithSigLen(txes, memoScripts, customerScript, changeScript, feeCapSigLen) * satsPerVByte
}

func (c *Client) estimateTxSizeWithSigLen(txes []btcjson.ListUnspentResult, memoScripts [][]byte, customerScript, changeScript []byte, sigLen int) int64 {
	tx := wire.NewMsgTx(wire.TxVersion)

	// Add inputs with realistic witness/scriptSig data for size estimation
	for _, utxo := range txes {
		hash, err := chainhash.NewHashFromStr(utxo.TxID)
		if err != nil {
			c.log.Error().Err(err).Msg("failed to parse txid for size estimation")
			continue
		}
		outpoint := wire.NewOutPoint(hash, utxo.Vout)
		txIn := wire.NewTxIn(outpoint, nil, nil)

		// Add realistic scriptSig/witness data for accurate size estimation
		if c.isSegwitChain() {
			// For segwit chains (BTC, LTC), inputs have empty scriptSig but witness data
			// P2WPKH witness: [signature, compressed pubkey (33 bytes)]
			txIn.Witness = make([][]byte, 2)
			txIn.Witness[0] = make([]byte, sigLen) // signature
			txIn.Witness[1] = make([]byte, 33)     // pubkey
		} else {
			// For non-segwit chains (DOGE, BCH), inputs have scriptSig
			// P2PKH scriptSig: <sig> <pubkey>, each with a one byte push opcode
			txIn.SignatureScript = make([]byte, 1+sigLen+1+33)
		}

		tx.AddTxIn(txIn)
	}

	// Add customer output
	tx.AddTxOut(wire.NewTxOut(0, customerScript))

	// Add change output (will be added if balance > 0)
	if changeScript != nil {
		tx.AddTxOut(wire.NewTxOut(0, changeScript))
	}

	// Add memo outputs
	if len(memoScripts) > 0 {
		// First script is OP_RETURN (value = 0)
		tx.AddTxOut(wire.NewTxOut(0, memoScripts[0]))

		// Additional scripts are P2WPKH/P2PKH outputs with dust value
		for _, script := range memoScripts[1:] {
			tx.AddTxOut(wire.NewTxOut(0, script)) // value doesn't affect size
		}
	}

	// Calculate size based on chain type
	if c.isSegwitChain() {
		// For segwit chains, calculate virtual size (weight/4)
		strippedSize := tx.SerializeSizeStripped()
		totalSize := tx.SerializeSize()
		// Virtual size = (base_size * 3 + total_size) / 4
		return int64((strippedSize*3 + totalSize + 3) / 4) // +3 for proper rounding
	}

	// For non-segwit chains, return actual serialized size
	return int64(tx.SerializeSize())
}

// isSegwitChain returns true if the chain supports segwit transactions
func (c *Client) isSegwitChain() bool {
	switch c.cfg.ChainID {
	case common.BTCChain, common.LTCChain:
		return true
	case common.DOGEChain, common.BCHChain, common.ZECChain, common.DASHChain, common.PIVXChain:
		return false
	default:
		c.log.Fatal().Msgf("unsupported chain: %s", c.cfg.ChainID)
		return false
	}
}

func (c *Client) getGasCoin(tx stypes.TxOutItem, vSize int64) common.Coin {
	gasRate := tx.GasRate

	// if the gas rate is zero, try to get from last transaction fee
	if gasRate == 0 {
		fee, vBytes, err := c.temporalStorage.GetTransactionFee()
		if err != nil {
			c.log.Error().Err(err).Msg("fail to get previous transaction fee from local storage")
			return common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(uint64(vSize*gasRate)))
		}
		// analyze-ignore(float-comparison)
		if fee > 0 && vBytes != 0 {
			var amt btcutil.Amount
			amt, err = btcutil.NewAmount(fee)
			if err != nil {
				c.log.Err(err).Msg("fail to convert amount from float64 to int64")
			} else {
				gasRate = int64(amt) / int64(vBytes) // sats per vbyte
			}
		}
	}

	// default to configured value
	if gasRate == 0 {
		gasRate = c.cfg.UTXO.DefaultSatsPerVByte
	}

	return common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(uint64(gasRate*vSize)))
}

func (c *Client) getGasCoinZEC(tx *wire.MsgTx, memo string) common.Coin {
	return common.NewCoin(common.ZECAsset, cosmos.NewUint(uint64(zecFeeForInputCount(len(tx.TxIn), memo))))
}

func zecFeeForInputCount(inputCount int, memo string) btcutil.Amount {
	// https://zips.z.cash/zip-0317
	// 8 bytes value + 1 byte length + OP_RETURN + OP_PUSHBYTES/DATA
	// -> 8 + 1 + 1 + 2 (using two bytes for OP_PUSHDATA2 for simplicity)
	var actionsMemo int
	if len(memo) > 0 {
		bytesOpReturn := 12 + len(memo)
		actionsMemo = (bytesOpReturn + 34 - 1) / 34
	}

	// assume 2 tx out: one to customer + one back to self
	amount := 5000*max(inputCount, 2+actionsMemo) + ZecExtraFee

	return btcutil.Amount(amount)
}

func (c *Client) buildTx(tx stypes.TxOutItem, sourceScript []byte, zecReference int64) (*wire.MsgTx, map[string]int64, error) {
	var txes []btcjson.ListUnspentResult
	var err error
	if c.cfg.ChainID != common.ZECChain {
		// MaxGas is only a fee ceiling, so a vault short of amount + MaxGas may still pay
		// the actual fee; the balance check below rejects it if not.
		floor := btcutil.Amount(tx.Coins.GetCoin(c.cfg.ChainID.GetGasAsset()).Amount.Uint64())
		txes, err = c.getUtxoToSpend(tx.VaultPubKey, c.getPaymentAmount(tx), floor)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get unspent UTXO: %w", err)
		}
	}
	var buf []byte
	var nullDataScripts [][]byte
	switch c.cfg.ChainID {
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
		nullDataScripts, err = MemoToScripts(tx.Memo, dogetxscript.MaxDataCarrierSize, dogetxscript.NullDataScript, dogetxscript.PayToWitnessScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to generate null data script: %w", err)
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
		nullDataScripts, err = MemoToScripts(tx.Memo, bchtxscript.MaxDataCarrierSize, bchtxscript.NullDataScript, bchtxscript.PayToWitnessScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to generate null data script: %w", err)
		}
	case common.LTCChain:
		var outputAddr ltcutil.Address
		outputAddr, err = ltctxscript.DecodeAddress(tx.ToAddress.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		buf, err = ltctxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get pay to address script: %w", err)
		}
		nullDataScripts, err = MemoToScripts(tx.Memo, ltctxscript.MaxDataCarrierSize, ltctxscript.NullDataScript, ltctxscript.PayToWitnessScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to generate null data script: %w", err)
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
		nullDataScripts, err = MemoToScripts(tx.Memo, btctxscript.MaxDataCarrierSize, btctxscript.NullDataScript, btctxscript.PayToWitnessScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to generate null data script: %w", err)
		}
	case common.DASHChain:
		var outputAddr dashbtcutil.Address
		outputAddr, err = dashutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgDASH())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode destination address: %w", err)
		}
		buf, err = dashtxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to build payment script: %w", err)
		}
		// Dash has no SegWit. Extended memos >80 bytes use the fake-address-dust
		// pattern with P2PKH-shaped outputs (payToPubKeyHashMemoScript); DOGE/BCH
		// use the P2WPKH shape, but dashd 23's TxoutType enum has no
		// WITNESS_V0_KEYHASH variant so those outputs would be rejected as
		// NONSTANDARD. getMemo on the observer side already handles the P2PKH
		// overflow shape (client_internal.go: pubkeyhash scriptType branch).
		nullDataScripts, err = MemoToScripts(tx.Memo, dashtxscript.MaxDataCarrierSize, dashtxscript.NullDataScript, payToPubKeyHashMemoScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to build memo scripts: %w", err)
		}
	case common.PIVXChain:
		var outputAddr btcutil.Address
		outputAddr, err = pivxutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgPIVX())
		if err != nil {
			return nil, nil, fmt.Errorf("fail to decode destination address: %w", err)
		}
		buf, err = btctxscript.PayToAddrScript(outputAddr)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to build payment script: %w", err)
		}
		// PIVX has no segwit. Memos longer than 80 bytes continue in P2PKH-shaped
		// outputs, the same shape getMemo already accepts.
		nullDataScripts, err = MemoToScripts(tx.Memo, pivxutil.MaxDataCarrierBytes, btctxscript.NullDataScript, payToPIVXPubKeyHashMemoScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to build memo scripts: %w", err)
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
		nullDataScripts, err = MemoToScripts(tx.Memo, btctxscript.MaxDataCarrierSize, btctxscript.NullDataScript, zcash.PayToPubKeyHashScript)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to generate null data script: %w", err)
		}
	default:
		c.log.Fatal().Msg("unsupported chain")
	}

	// For memoless outbounds, allow empty nullDataScripts
	if len(nullDataScripts) == 0 && len(tx.Memo) != 0 {
		return nil, nil, fmt.Errorf("no null data scripts generated, memo will not be included in the transaction")
	}

	// Parse the memo to be able to identify Migrate or Consolidate outbounds.
	// Use GetMemo() which returns OriginalMemo for memoless outbounds
	memoForParsing := tx.GetMemo()
	var memo mem.Memo
	if memoForParsing == "" {
		memo = mem.NewOutboundMemo(tx.InHash)
	} else {
		memo, err = mem.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return nil, nil, fmt.Errorf("fail to parse memo: %w", err)
		}
	}

	if c.cfg.ChainID == common.ZECChain {
		consolidate := memo.GetType() == mem.TxConsolidate
		target := tx.Coins.GetCoin(common.ZECAsset).Amount
		if !tx.MaxGas.IsEmpty() {
			target = target.Add(tx.MaxGas.ToCoins().GetCoin(common.ZECAsset).Amount)
		}
		if len(nullDataScripts) > 1 {
			target = target.AddUint64(uint64(len(nullDataScripts)-1) * uint64(tx.Chain.P2WPKHOutputValue()))
		}
		if !target.BigInt().IsInt64() {
			return nil, nil, fmt.Errorf("ZEC input target exceeds the supported amount range")
		}
		amount := btcutil.Amount(target.BigInt().Int64())
		switch {
		case !consolidate && tx.MaxGas.IsEmpty():
			txes, err = c.selectZecUtxos(tx.VaultPubKey, amount, 0, zecReference, false, func(inputCount int) btcutil.Amount {
				return zecFeeForInputCount(inputCount, memo.String())
			})
		case !consolidate:
			// as for other chains, MaxGas only bounds the fee: inputs covering the payment
			// without it may still pay the actual fee, which the balance check verifies
			floor := amount - btcutil.Amount(tx.MaxGas.ToCoins().GetCoin(common.ZECAsset).Amount.Uint64())
			txes, err = c.selectZecUtxos(tx.VaultPubKey, amount, floor, zecReference, false, nil)
		default:
			txes, err = c.getZecUtxoToSpend(tx.VaultPubKey, amount, zecReference, consolidate)
		}
		if err != nil {
			return nil, nil, fmt.Errorf("fail to get confirmed ZEC inputs: %w", err)
		}
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
		individualAmounts[formatUtxoKey(txID.String(), item.Vout)] = int64(amt)
		totalAmt += int64(amt)
	}

	totalSize := c.estimateTxSize(txes, nullDataScripts, buf, sourceScript)

	coinToCustomer := tx.Coins.GetCoin(c.cfg.ChainID.GetGasAsset())

	var gasCoin common.Coin
	switch c.cfg.ChainID {
	case common.ZECChain:
		gasCoin = c.getGasCoinZEC(redeemTx, memo.String())
	default:
		gasCoin = c.getGasCoin(tx, totalSize)
	}

	// Calculate the total cost of P2WPKH outputs for extended memos
	p2wpkhOutputsCost := int64(0)
	if len(nullDataScripts) > 1 {
		// Each P2WPKH output (nullDataScripts[1:]) costs P2WPKHOutputValue()
		p2wpkhOutputsCost = int64(len(nullDataScripts)-1) * tx.Chain.P2WPKHOutputValue()
	}

	var gasAmtSats, toCustomer uint64
	var balance int64
	if c.cfg.ChainID == common.ZECChain && tx.MaxGas.IsEmpty() && memo.GetType() != mem.TxConsolidate {
		// There is no committed fee allowance. Use the same ZIP-317 fee as input
		// selection; local relay policy can veto it but cannot change the digest.
		gasAmtSats, toCustomer = gasCoin.Amount.Uint64(), coinToCustomer.Amount.Uint64()
		balance = totalAmt - int64(toCustomer) - int64(gasAmtSats) - p2wpkhOutputsCost
		changeScript := sourceScript
		if balance == 0 {
			changeScript = nil
		}
		maxFeeSats := c.maxFeeSats(txes, nullDataScripts, buf, changeScript)
		if maxFeeSats < 0 || gasAmtSats > uint64(maxFeeSats) {
			return nil, nil, fmt.Errorf("ZEC conventional fee %d exceeds configured cap %d", gasAmtSats, maxFeeSats)
		}
		if relay := c.minRelayFeeSats.Load(); gasAmtSats < relay {
			return nil, nil, fmt.Errorf("ZEC conventional fee %d is below local relay minimum %d", gasAmtSats, relay)
		}
	} else {
		settleFee := func(changeScript []byte) (gasAmtSats, toCustomer uint64, balance int64) {
			gasAmtSats, toCustomer = c.applyFeeLimits(tx, memo, gasCoin.Amount.Uint64(), coinToCustomer.Amount.Uint64(),
				c.maxFeeSats(txes, nullDataScripts, buf, changeScript))
			return gasAmtSats, toCustomer, totalAmt - int64(toCustomer) - int64(gasAmtSats) - p2wpkhOutputsCost
		}

		// Cap the fee assuming a change output first. When no balance is left for change
		// (e.g. a migration that sends everything), the signed transaction is smaller, so
		// re-cap against the size without the change output to stay within the fee rate.
		gasAmtSats, toCustomer, balance = settleFee(sourceScript)
		if balance == 0 {
			c.log.Info().Msg("no change output, capping fee against the transaction without change")
			gasAmtSats, toCustomer, balance = settleFee(nil)
		}
	}

	// Check the fee after all caps, using the outputs the transaction will
	// actually contain. An insufficient budget must fail before keysign rather
	// than leave the signer retrying signed bytes that cannot satisfy relay policy.
	changeScript := sourceScript
	if balance == 0 {
		changeScript = nil
	}
	finalSize := c.estimateTxSize(txes, nullDataScripts, buf, changeScript)
	if err = c.validateRelayFee(gasAmtSats, finalSize); err != nil {
		return nil, nil, err
	}

	gasAmt := btcutil.Amount(gasAmtSats)
	if err = c.temporalStorage.UpsertTransactionFee(gasAmt.ToBTC(), int32(totalSize)); err != nil {
		c.log.Err(err).Msg("fail to save gas info to UTXO storage")
	}

	// pay to customer
	redeemTxOut := wire.NewTxOut(int64(toCustomer), buf)
	redeemTx.AddTxOut(redeemTxOut)

	// balance to ourselves
	// add output to pay the balance back ourselves
	// Now properly account for P2WPKH outputs cost
	c.log.Info().Msgf("total: %d, to customer: %d, gas: %d, p2wpkh_outputs_cost: %d", totalAmt, redeemTxOut.Value, int64(gasAmt), p2wpkhOutputsCost)
	if balance < 0 {
		return nil, nil, fmt.Errorf("not enough balance to pay customer: %d (inputs: %d, to customer: %d, gas: %d)",
			balance, totalAmt, toCustomer, gasAmtSats)
	}
	if balance > 0 {
		c.log.Info().Msgf("send %d back to self", balance)
		redeemTx.AddTxOut(wire.NewTxOut(balance, sourceScript))
	}

	// memo
	if len(tx.Memo) != 0 {
		redeemTx.AddTxOut(wire.NewTxOut(0, nullDataScripts[0]))
		for _, script := range nullDataScripts[1:] {
			redeemTx.AddTxOut(wire.NewTxOut(tx.Chain.P2WPKHOutputValue(), script))
		}
	}

	return redeemTx, individualAmounts, nil
}

// validateRelayFee checks that the final fee covers the size-scaled relay minimum.
// It only vetoes the transaction; it never raises a fee beyond its assigned budget.
func (c *Client) validateRelayFee(fee uint64, vSize int64) error {
	switch c.cfg.ChainID {
	case common.BTCChain, common.LTCChain, common.BCHChain, common.DOGEChain, common.DASHChain, common.PIVXChain:
		// These chains express the daemon's relay fee per 1,000 bytes/vbytes.
	default:
		// ZEC has separate ZIP-317 fee handling.
		return nil
	}
	if vSize <= 0 {
		return fmt.Errorf("invalid estimated transaction size: %d", vSize)
	}
	// Round up without overflowing a uint64 multiplication.
	minimum := cosmos.NewUint(c.minRelayFeeSats.Load()).
		MulUint64(uint64(vSize)).AddUint64(999).QuoUint64(1000)
	if cosmos.NewUint(fee).LT(minimum) {
		return fmt.Errorf("%s fee %d is below relay minimum %s for %d bytes/vbytes",
			c.cfg.ChainID, fee, minimum.String(), vSize)
	}
	return nil
}

// applyFeeLimits bounds the estimated gas by maxFeeSats, the min relay fee and the
// outbound's MaxGas, and returns the gas and the amount to pay the customer.
func (c *Client) applyFeeLimits(tx stypes.TxOutItem, memo mem.Memo, gasAmtSats, toCustomer uint64, maxFeeSats int64) (uint64, uint64) {
	// make sure the transaction fee is not more than the max, otherwise it might reject the transaction
	if gasAmtSats > uint64(maxFeeSats) {
		diffSats := gasAmtSats - uint64(maxFeeSats) // in sats
		c.log.Info().Msgf("gas amount: %d is larger than maximum fee: %d, diff: %d", gasAmtSats, uint64(maxFeeSats), diffSats)
		gasAmtSats = uint64(maxFeeSats)
	} else {
		minRelayFeeSats := c.minRelayFeeSats.Load()
		if gasAmtSats < minRelayFeeSats {
			diffStats := minRelayFeeSats - gasAmtSats
			c.log.Info().Msgf("gas amount: %d is less than min relay fee: %d, diff remove from customer: %d", gasAmtSats, minRelayFeeSats, diffStats)
			gasAmtSats = minRelayFeeSats
		}
	}

	// if the total gas spend is more than max gas , then we have to take away some from the amount pay to customer
	if !tx.MaxGas.IsEmpty() {
		maxGasCoin := tx.MaxGas.ToCoins().GetCoin(c.cfg.ChainID.GetGasAsset())
		if gasAmtSats > maxGasCoin.Amount.Uint64() {
			c.log.Info().Msgf("max gas: %s, however estimated gas need %d", tx.MaxGas, gasAmtSats)
			gasAmtSats = maxGasCoin.Amount.Uint64()
		} else if gasAmtSats < maxGasCoin.Amount.Uint64() && memo.GetType() == mem.TxMigrate {
			// if the tx spend less gas then the estimated MaxGas , then the extra can be added to the coinToCustomer
			gap := maxGasCoin.Amount.Uint64() - gasAmtSats
			c.log.Info().Msgf("max gas is: %s, however only: %d is required, gap: %d goes to the vault migrated to", tx.MaxGas, gasAmtSats, gap)
			toCustomer += gap
		}
	} else if memo.GetType() == mem.TxConsolidate {
		gap := gasAmtSats
		c.log.Info().Msgf("consolidate tx, need gas: %d", gap)
		toCustomer -= min(gap, toCustomer)
	}

	return gasAmtSats, toCustomer
}

// MemoToScripts converts a memo to UTXO scripts.
// Up to 80 bytes in a single OP_RETURN output; for longer memos, 79 bytes plus '^' marker in OP_RETURN,
// with remaining data in chain-specific continuation outputs (20 bytes each).
func MemoToScripts(memo string, maxDataCarrierSize int, nullDataScript, payToContinuationScript func([]byte) ([]byte, error)) ([][]byte, error) {
	if len(memo) == 0 {
		return nil, nil
	}

	if len(memo) > constants.MaxMemoSize {
		return nil, fmt.Errorf("memo size %d exceeds maximum size of %d bytes", len(memo), constants.MaxMemoSize)
	}

	data := []byte(memo)

	// Calculate number of scripts: 1 OP_RETURN + ceil(remaining_data / 20) continuation outputs
	remainingDataSize := len(data)
	if remainingDataSize > maxDataCarrierSize {
		remainingDataSize -= (maxDataCarrierSize - 1) // Reserve 1 byte for '^'
	} else {
		remainingDataSize = 0
	}
	numScripts := 1 + (remainingDataSize+19)/20 // 1 for OP_RETURN, plus continuation outputs (20 bytes each)
	scripts := make([][]byte, 0, numScripts)

	// First chunk OP_RETURN: up to 80 bytes; if > 80 bytes, 79 bytes + '^' marker
	firstChunkSize := len(data)
	continuation := false
	if firstChunkSize > maxDataCarrierSize { // Reserve 1 byte for '^' if needed
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

	// Remaining data (if any) goes into continuation outputs, 20 bytes each
	if continuation {
		remainingData := data[firstChunkSize:]
		for i := 0; len(remainingData) > 0; i++ {
			// Take up to 20 bytes for this continuation output
			chunkSize := len(remainingData)
			if chunkSize > 20 {
				chunkSize = 20
			}
			hash := make([]byte, 20)
			copy(hash, remainingData[:chunkSize])
			// Remaining bytes (if < 20) are padded with zeros, signaling the end
			// (getMemo stops at a hash ending with "00")
			continuationScript, err := payToContinuationScript(hash)
			if err != nil {
				return nil, fmt.Errorf("fail to create memo continuation script at index %d: %w", i, err)
			}
			scripts = append(scripts, continuationScript)
			// Move to the next chunk
			remainingData = remainingData[chunkSize:]
		}
	}

	return scripts, nil
}

////////////////////////////////////////////////////////////////////////////////////////
// UTXO Consolidation
////////////////////////////////////////////////////////////////////////////////////////

// consolidateKeysignBlockCadence is the spacing, in THORChain blocks, of the
// shared boundaries that UTXO-consolidation keysigns anchor on (~5 min at
// 6s/block). A consolidation signed anywhere within one cadence window hands
// go-tss boundary×cadence as its signing height, so every vault member converges
// on one leader and one keysign session instead of splitting across per-node
// tips. Matches keepAliveThorBlockCadence used by the SOL keep-alive anchor.
const consolidateKeysignBlockCadence = 50

// anchorConsolidateHeight quantizes a raw THORChain tip to the shared cadence
// boundary that every vault member converges on for a consolidation keysign. The
// result is ≤ height (so it stays a valid signing_height the keysign-party query
// accepts — future heights are rejected) and equal for any two tips in the same
// cadence window. Below one cadence it returns the height unchanged so an early
// chain cannot anchor to 0.
func anchorConsolidateHeight(height int64) int64 {
	if height < consolidateKeysignBlockCadence {
		return height
	}
	return height / consolidateKeysignBlockCadence * consolidateKeysignBlockCadence
}

func (c *Client) consolidateVaultUTXOs(vault types.Vault, utxosToSpend, scannedHeight int64) error {
	lock := c.GetVaultLock(vault.PubKey.String())

	// Hold the vault lock through broadcast to avoid local double-spend on failure/timeout.
	lock.Lock()
	defer lock.Unlock()

	var height int64
	var utxos []btcjson.ListUnspentResult
	var err error
	if c.GetChain() == common.ZECChain {
		height, err = c.bridge.GetBlockHeight()
		if err != nil {
			return fmt.Errorf("get THORChain block height: %w", err)
		}
		height, err = c.getZecConsolidationHeight(scannedHeight, height, vault.BlockHeight)
		if err != nil {
			return fmt.Errorf("get ZEC consolidation signing height: %w", err)
		}
		expiry, expiryErr := c.getZecExpiryHeight(height)
		if expiryErr != nil {
			return fmt.Errorf("get ZEC consolidation expiry: %w", expiryErr)
		}
		utxos, err = c.getZecUtxoToSpend(vault.PubKey, 0, int64(expiry)-int64(ZecTxExpiry), true)
	} else {
		// Probe the existing consolidation input target on other UTXO chains.
		utxos, err = c.getUtxoToSpend(vault.PubKey, 0, 0)
	}
	if err != nil {
		return fmt.Errorf("get utxos to spend: %w", err)
	}

	// Doesn't have enough UTXOs, no consolidation needed.
	if int64(len(utxos)) < utxosToSpend {
		return nil
	}

	txOutItem, err := c.buildConsolidateTxOutItem(vault, utxos)
	if err != nil {
		return err
	}

	if c.GetChain() != common.ZECChain {
		height, err = c.bridge.GetBlockHeight()
		if err != nil {
			return fmt.Errorf("get THORChain block height: %w", err)
		}
		// A consolidation is not a consensus txOut, so unlike a scheduled outbound
		// it carries no agreed signing height — every vault member reads its own
		// tip here, which differs by a few blocks across the fleet. The keysign
		// leader is derived from this height (sha256(msgID‖height‖peer)) and go-tss
		// batches keysign tasks by {algo, height}, so a per-node tip splits members
		// across different leaders and keysign sessions: the consolidate
		// leader-blame race that jails whichever node a sub-party elects. Anchoring
		// to the shared cadence boundary gives every member the same, valid height,
		// exactly the convergence a scheduled outbound gets from its consensus
		// txOut.Height. (ZEC derives its height from the expiry window above.)
		height = anchorConsolidateHeight(height)
	}

	rawTx, _, _, err := c.SignTx(txOutItem, height)
	if err != nil {
		return fmt.Errorf("sign consolidate txout item: %w", err)
	}
	if len(rawTx) == 0 {
		c.log.Warn().Str("vault_pubkey", vault.PubKey.String()).Msg("signed consolidate transaction is empty, skipping broadcast")
		return nil
	}

	txID, err := c.BroadcastTx(txOutItem, rawTx)
	if err != nil {
		return fmt.Errorf("broadcast consolidate tx: %w", err)
	}
	c.log.Info().Str("vault_pubkey", vault.PubKey.String()).Msgf("broadcast consolidate tx successfully, hash:%s", txID)
	return nil
}

func (c *Client) buildConsolidateTxOutItem(vault types.Vault, utxos []btcjson.ListUnspentResult) (stypes.TxOutItem, error) {
	// Convert each UTXO amount to sats before summing to avoid float accumulation drift.
	total := btcutil.Amount(0)
	for _, item := range utxos {
		amt, err := btcutil.NewAmount(item.Amount)
		if err != nil {
			return stypes.TxOutItem{}, fmt.Errorf("convert utxo amount %f: %w", item.Amount, err)
		}
		total += amt
	}

	addr, err := vault.PubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		return stypes.TxOutItem{}, fmt.Errorf("get address for pubkey %s: %w", vault.PubKey, err)
	}

	// THORChain usually pays 1.5 of the last observed fee rate.
	feeRate := math.Ceil(float64(c.lastFeeRate.Load()) * 3 / 2)

	return stypes.TxOutItem{
		Chain:            c.cfg.ChainID,
		ToAddress:        addr,
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: vault.PubKeyEddsa,
		Coins: common.Coins{
			common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(uint64(total))),
		},
		Memo:    mem.NewConsolidateMemo().String(),
		MaxGas:  nil,
		GasRate: int64(feeRate),
	}, nil
}

// consolidateUTXOs only required when there is a new block
func (c *Client) consolidateUTXOs(scannedHeight int64) {
	defer func() {
		c.wg.Done()
		c.consolidateInProgress.Store(false)
	}()

	nodeStatus, err := c.bridge.FetchNodeStatus()
	if err != nil {
		c.log.Err(err).Msg("fail to get node status")
		return
	}
	if nodeStatus != types.NodeStatus_Active {
		c.log.Info().Msgf("node is not active , doesn't need to consolidate utxos")
		return
	}
	if signing.IsSigningHalted(c.bridge, c.cfg.ChainID, c.log) {
		return
	}
	vaults, err := c.bridge.GetAsgards()
	if err != nil {
		c.log.Err(err).Msg("fail to get current asgards")
		return
	}
	utxosToSpend := c.getMaximumUtxosToSpend()
	for _, vault := range vaults {
		if !vault.Contains(c.nodePubKey) {
			// Not part of this vault , don't need to consolidate UTXOs for this Vault
			continue
		}
		if err = c.consolidateVaultUTXOs(vault, utxosToSpend, scannedHeight); err != nil {
			c.log.Err(err).Str("vault_pubkey", vault.PubKey.String()).Msg("fail to consolidate utxos for vault")
		}
	}
}
