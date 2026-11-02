package utxo

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/pivxutil"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func (c *Client) getChainCfgPIVX() *chaincfg.Params {
	switch common.CurrentChainNetwork {
	case common.MockNet:
		return pivxutil.RegressionNetParams()
	case common.MainNet, common.StageNet, common.ChainNet:
		return pivxutil.MainNetParams()
	default:
		c.log.Fatal().Msg("unsupported network")
		return nil
	}
}

// payToPubKeyHashMemoScript is the non-segwit continuation output used when a
// memo exceeds 80 bytes. The 20-byte hash is memo data, not a real key.
func payToPIVXPubKeyHashMemoScript(hash []byte) ([]byte, error) {
	return pivxutil.PayToPubKeyHashScript(hash)
}

func (c *Client) getAddressesFromScriptPubKeyPIVX(scriptPubKey btcjson.ScriptPubKeyResult) []string {
	if pivxutil.IsRejectedOutputScript(scriptPubKey.Hex) {
		return nil
	}
	if len(scriptPubKey.Addresses) > 0 {
		return scriptPubKey.Addresses
	}
	if len(scriptPubKey.Hex) == 0 {
		return nil
	}
	buf, err := hex.DecodeString(scriptPubKey.Hex)
	if err != nil {
		c.log.Err(err).Msg("fail to decode PIVX output script")
		return nil
	}
	_, extracted, _, err := btctxscript.ExtractPkScriptAddrs(buf, c.getChainCfgPIVX())
	if err != nil {
		c.log.Err(err).Msg("fail to extract PIVX output addresses")
		return nil
	}
	addresses := make([]string, 0, len(extracted))
	for _, addr := range extracted {
		addresses = append(addresses, addr.String())
	}
	return addresses
}

func (c *Client) isValidUTXOPIVX(script []byte) bool {
	if pivxutil.IsRejectedOutputScript(hex.EncodeToString(script)) {
		return false
	}
	scriptType, addresses, requireSigs, err := btctxscript.ExtractPkScriptAddrs(script, c.getChainCfgPIVX())
	if err != nil {
		c.log.Err(err).Msg("fail to extract PIVX pub key script")
		return false
	}
	if scriptType == btctxscript.MultiSigTy {
		return false
	}
	return len(addresses) == 1 && requireSigs == 1
}

// pivxBlockSubsidySats is the height-scheduled block reward. Confirmation
// counting uses this instead of summing coinstake outputs, which include the
// staked principal and masternode payouts.
func (c *Client) pivxBlockSubsidySats(height int64) int64 {
	return pivxutil.BlockSubsidySats(common.CurrentChainNetwork == common.MockNet, height)
}

// sendNetworkFeePIVX reports a fee rate from getblockindexstats, which sums
// fees of ordinary transactions and skips the coinbase and coinstake. pivxd
// has no getblockstats. An empty block, a parse failure, or a non-positive
// rate falls back to the 10 sat/byte relay floor because estimatesmartfee is
// deprecated and returns -1 until it has samples.
func (c *Client) sendNetworkFeePIVX(height int64) error {
	stats, err := c.rpc.GetPIVXBlockIndexStats(height, 1)
	feeRate := pivxutil.FallbackFeeRateSatsPerByte
	if err != nil {
		c.log.Warn().Err(err).Int64("height", height).Msg("PIVX getblockindexstats failed, using relay fee floor")
	} else if rate, ok := pivxutil.FeeRateSatsPerByte(stats.FeePerKB, stats.TxBytes); ok {
		feeRate = rate
	}

	if feeRate < uint64(c.cfg.UTXO.MinSatsPerVByte) {
		feeRate = uint64(c.cfg.UTXO.MinSatsPerVByte)
	}

	c.networkFeeLock.Lock()
	defer c.networkFeeLock.Unlock()

	if c.cfg.BlockScanner.GasCacheBlocks > 0 {
		c.feeRateCache = append(c.feeRateCache, feeRate)
		if len(c.feeRateCache) > c.cfg.BlockScanner.GasCacheBlocks {
			c.feeRateCache = c.feeRateCache[len(c.feeRateCache)-c.cfg.BlockScanner.GasCacheBlocks:]
		}
		for _, rate := range c.feeRateCache {
			if rate > feeRate {
				feeRate = rate
			}
		}
	}

	if c.lastFeeRate.Load() == feeRate {
		return nil
	}
	c.lastFeeRate.Store(feeRate)
	c.m.GetGauge(metrics.GasPrice(c.cfg.ChainID)).Set(float64(feeRate))
	c.m.GetCounter(metrics.GasPriceChange(c.cfg.ChainID)).Inc()
	c.globalNetworkFeeQueue <- common.NetworkFee{
		Chain:           c.cfg.ChainID,
		Height:          height,
		TransactionSize: c.cfg.UTXO.EstimatedAverageTxSize,
		TransactionRate: feeRate,
	}
	return nil
}

// signUTXOPIVX signs one precomputed legacy digest. The transaction is not
// read or written here; the caller attaches every script after the workers
// finish and verifies them together.
func (c *Client) signUTXOPIVX(digest []byte, tx stypes.TxOutItem, thorchainHeight int64) ([]byte, error) {
	if len(digest) != 32 {
		return nil, fmt.Errorf("PIVX signing digest: got %d bytes, want 32", len(digest))
	}
	var (
		signature *btcec.Signature
		pubKey    *btcec.PublicKey
		err       error
	)
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		signature, err = c.nodePrivKey.Sign(digest)
		if err != nil {
			return nil, fmt.Errorf("fail to sign PIVX digest: %w", err)
		}
		pubKey = c.nodePrivKey.PubKey()
	} else {
		signable := newTssSignableBTC(tx.VaultPubKey, c.tssKeySigner, c.log, thorchainHeight)
		signature, err = signable.Sign(digest)
		if err != nil {
			return nil, err
		}
		pubKey = signable.GetPubKey()
	}
	return pivxutil.SignatureScript(digest, signature, pubKey)
}
