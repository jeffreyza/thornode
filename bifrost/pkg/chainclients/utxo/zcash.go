package utxo

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/btcsuite/btcd/btcjson"
	btcchaincfg "github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcutil"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
)

func (c *Client) getChainCfgZEC() *btcchaincfg.Params {
	return &btcchaincfg.Params{Name: common.ZECNetworkName()}
}

// getZecExpiryHeight uses the outbound's THORChain height, not a validator's
// moving ZEC tip. All signers must construct the same digest on their first try.
func (c *Client) getZecExpiryHeight(thorchainHeight int64) (uint32, error) {
	height, err := c.bridge.GetLastObservedInHeightAtHeight(common.ZECChain, thorchainHeight)
	if err != nil {
		return 0, fmt.Errorf("get agreed ZEC height for expiry: %w", err)
	}
	if height <= 0 || height >= 5e8-int64(ZecTxExpiry) {
		return 0, fmt.Errorf("invalid agreed ZEC height for expiry: %d", height)
	}
	return uint32(height) + ZecTxExpiry, nil
}

func checkZecExpiry(expiry uint32, chainHeight int64) error {
	if chainHeight <= 0 {
		return fmt.Errorf("invalid ZEC tip height: %d", chainHeight)
	}
	// ZIP-203 permits inclusion at expiry, but the next block after a tip at
	// expiry is already too late. Zero denotes a legacy non-expiring payload.
	if expiry != 0 && chainHeight >= int64(expiry) {
		return fmt.Errorf("%w: ZEC expiry height %d, current height %d", chainclienttypes.ErrTxExpired, expiry, chainHeight)
	}
	return nil
}

// checkZecBroadcastExpiry returns a confirmed txid when another signer already
// mined the payload. An ambiguous lookup remains retryable; an expired payload
// that is absent, orphaned, or still in the mempool must leave the retry queue.
func (c *Client) checkZecBroadcastExpiry(expiry uint32, txid string, chainHeight int64) (string, error) {
	expiryErr := checkZecExpiry(expiry, chainHeight)
	if !errors.Is(expiryErr, chainclienttypes.ErrTxExpired) {
		return "", expiryErr
	}
	confirmed, err := c.rpc.GetRawTransactionVerbose(txid)
	if err != nil {
		if isUTXOTxNotFoundErr(err) {
			return "", expiryErr
		}
		return "", fmt.Errorf("check expired ZEC transaction %s: %w", txid, err)
	}
	if confirmed == nil || confirmed.Txid != txid || (confirmed.Confirmations > 0 && confirmed.BlockHash == "") {
		return "", fmt.Errorf("invalid lookup response for expired ZEC transaction %s", txid)
	}
	if confirmed.Confirmations > 0 && confirmed.BlockHash != "" {
		return txid, nil
	}
	return "", expiryErr
}

// getZecUtxoToSpend selects a confirmed input prefix from Zebra's live UTXO set.
// Ordinary targets include the committed gas allowance and stop when funded;
// consolidation deliberately fills its configured input target. Confirmed change
// remains eligible even when mined after the reference used for outbound expiry.
//
// Reference is a catch-up boundary, not a historical input cutoff. A common
// funded prefix is unaffected by later deposits, but newly confirmed inputs that
// are needed, or older inputs spent between reads, can still change the plan.
// This is not a historical snapshot or shared allocation across outbounds.
func (c *Client) getZecUtxoToSpend(pubkey common.PubKey, target btcutil.Amount, reference int64, consolidate bool) ([]btcjson.ListUnspentResult, error) {
	return c.selectZecUtxos(pubkey, target, 0, reference, consolidate, nil)
}

// extraFee handles ordinary payments without MaxGas. Its only variable input is
// the selected input count; local fee history must not affect the chosen prefix.
//
// A positive floor (at most target) lets a payment whose eligible inputs all
// together fall short of target proceed with every one of them, as long as they
// cover floor. It is for targets that include MaxGas, which only bounds the fee;
// buildTx checks the actual fee fits. A floor of zero requires the full target.
func (c *Client) selectZecUtxos(pubkey common.PubKey, target, floor btcutil.Amount, reference int64, consolidate bool, extraFee func(int) btcutil.Amount) ([]btcjson.ListUnspentResult, error) {
	if reference <= 0 || reference > int64(math.MaxUint32-ZecTxExpiry) {
		return nil, fmt.Errorf("invalid ZEC input reference: %d", reference)
	}
	if target < 0 || (!consolidate && target == 0) {
		return nil, fmt.Errorf("invalid ZEC input target: %d", target)
	}
	tip, err := c.rpc.GetBlockCount()
	if err != nil {
		return nil, fmt.Errorf("get ZEC tip for input selection: %w", err)
	}
	if tip < reference {
		return nil, fmt.Errorf("local ZEC tip %d is behind input reference %d", tip, reference)
	}
	address, err := pubkey.GetAddress(common.ZECChain)
	if err != nil {
		return nil, fmt.Errorf("get ZEC vault address: %w", err)
	}
	candidates, err := c.rpc.GetZecAddressUTXOs(address.String())
	if err != nil {
		return nil, fmt.Errorf("get confirmed ZEC input candidates: %w", err)
	}

	eligible := make([]rpc.ZecAddressUTXO, 0, len(candidates))
	seen := make(map[string]rpc.ZecAddressUTXO, len(candidates))
	minimumConfirmations := max(int64(1), c.cfg.UTXO.MinUTXOConfirmations)
	for _, candidate := range candidates {
		if candidate.Height <= 0 || candidate.Height > tip || tip-candidate.Height+1 < minimumConfirmations {
			continue
		}
		if candidate.Satoshis == 0 || !c.isValidUTXO(candidate.Script) {
			continue
		}
		// Zcash and Bitcoin share the 21 million coin supply bound. Values above
		// it are invalid and need not pass through a floating point RPC adapter.
		if candidate.Satoshis > uint64(btcutil.MaxSatoshi) {
			return nil, fmt.Errorf("invalid ZEC input amount: %d", candidate.Satoshis)
		}
		candidate.Txid = strings.ToLower(candidate.Txid)
		candidate.Script = strings.ToLower(candidate.Script)
		key := formatUtxoKey(candidate.Txid, candidate.Index)
		if previous, ok := seen[key]; ok {
			if previous.Height != candidate.Height || previous.Satoshis != candidate.Satoshis || previous.Script != candidate.Script {
				return nil, fmt.Errorf("conflicting ZEC input metadata for %s", key)
			}
			continue
		}
		seen[key] = candidate
		eligible = append(eligible, candidate)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].Height != eligible[j].Height {
			return eligible[i].Height < eligible[j].Height
		}
		if eligible[i].Txid != eligible[j].Txid {
			return eligible[i].Txid < eligible[j].Txid
		}
		return eligible[i].Index < eligible[j].Index
	})

	consolidationTarget := int64(0)
	if consolidate {
		consolidationTarget = c.getMaximumUtxosToSpend()
	}
	selected := make([]btcjson.ListUnspentResult, 0, len(eligible))
	dustTransactions := make(map[string]*btcjson.TxRawResult)
	var vaultScript []byte
	var asgardScripts map[string]bool
	isOtherAsgardScript := func(script []byte) (bool, error) {
		if asgardScripts == nil {
			pubkeys, queryErr := c.bridge.GetAsgardPubKeys()
			if queryErr != nil {
				return false, fmt.Errorf("get Asgard keys for confirmed ZEC dust: %w", queryErr)
			}
			asgardScripts = make(map[string]bool, len(pubkeys))
			for _, pair := range pubkeys {
				if pair.Algo != common.SigningAlgoSecp256k1 || pair.PubKey.IsEmpty() {
					continue
				}
				if _, keyErr := pair.PubKey.Secp256K1(); keyErr != nil {
					return false, fmt.Errorf("invalid Asgard key in confirmed ZEC dust proof: %w", keyErr)
				}
				sourceScript, scriptErr := c.getSourceScript(stypes.TxOutItem{VaultPubKey: pair.PubKey})
				if scriptErr != nil {
					return false, fmt.Errorf("derive Asgard script for confirmed ZEC dust: %w", scriptErr)
				}
				asgardScripts[hex.EncodeToString(sourceScript)] = true
			}
		}
		return asgardScripts[hex.EncodeToString(script)], nil
	}
	total, required := btcutil.Amount(0), target
	for _, candidate := range eligible {
		if candidate.Satoshis < common.ZECChain.DustThreshold().Uint64() {
			if vaultScript == nil {
				vaultScript, err = c.getSourceScript(stypes.TxOutItem{VaultPubKey: pubkey})
				if err != nil {
					return nil, fmt.Errorf("derive ZEC vault script for confirmed change: %w", err)
				}
			}
			vaultChange, proofErr := c.isZecVaultChange(candidate, vaultScript, dustTransactions, isOtherAsgardScript)
			if proofErr != nil {
				return nil, proofErr
			}
			if !vaultChange {
				continue
			}
		}
		amount := btcutil.Amount(candidate.Satoshis)
		if total > btcutil.Amount(math.MaxInt64)-amount {
			return nil, fmt.Errorf("ZEC selected input total overflows")
		}
		total += amount
		selected = append(selected, btcjson.ListUnspentResult{
			TxID: candidate.Txid, Vout: candidate.Index, Address: candidate.Address,
			ScriptPubKey: candidate.Script, Amount: amount.ToBTC(),
			Confirmations: tip - candidate.Height + 1, Spendable: true,
		})
		if extraFee != nil {
			fee := extraFee(len(selected))
			if fee < 0 || target > btcutil.Amount(math.MaxInt64)-fee {
				return nil, fmt.Errorf("ZEC input target including fee overflows")
			}
			required = target + fee
		}
		if total >= required && (!consolidate || int64(len(selected)) >= consolidationTarget) {
			break
		}
	}
	// An empty zero-target consolidation probe is a no-op, as on other chains.
	if len(selected) == 0 && consolidate && target == 0 && extraFee == nil {
		return selected, nil
	}
	if len(selected) == 0 || total < required {
		if floor <= 0 || len(selected) == 0 || total < floor {
			if floor > 0 {
				required = floor
			}
			return nil, fmt.Errorf("insufficient confirmed ZEC inputs at tip %d: have %d, need %d", tip, total, required)
		}
		c.log.Info().
			Int64("available", int64(total)).
			Int64("target", int64(target)).
			Int64("floor", int64(floor)).
			Int("inputs", len(selected)).
			Msg("confirmed ZEC inputs cover the payment but not its full max gas, spending all of them")
	}

	// Local spent markers may veto the selected plan, never replace an input.
	// Doing this after selection prevents a successful broadcaster from choosing
	// a different fresh digest than peers which have not stored that spend yet.
	for _, input := range selected {
		key := formatUtxoKey(input.TxID, input.Vout)
		spent, spentErr := c.temporalStorage.HasSpentUtxo(key)
		if spentErr != nil {
			return nil, fmt.Errorf("read ZEC input spend %s: %w", key, spentErr)
		}
		if spent {
			return nil, fmt.Errorf("selected ZEC input %s is locally spent; wait for reconciliation", key)
		}
	}
	return selected, nil
}

// isZecVaultChange preserves small confirmed self-change needed by full-vault
// migration. Own change uses this vault's fixed script, never local self
// records. Other Asgard senders retain the existing live vault-list policy;
// membership changes between reads remain a limitation of fresh selection.
// Only candidates reached before funding
// the payment incur these reads; shared creating/previous transactions are cached
// within this selection. Unknown provenance defers the plan instead of silently
// changing its inputs.
func (c *Client) isZecVaultChange(candidate rpc.ZecAddressUTXO, vaultScript []byte, cache map[string]*btcjson.TxRawResult, isOtherAsgardScript func([]byte) (bool, error)) (bool, error) {
	lookup := func(txID string) (*btcjson.TxRawResult, error) {
		txID = strings.ToLower(txID)
		if len(txID) != chainhash.MaxHashStringSize {
			return nil, fmt.Errorf("invalid ZEC change proof transaction ID %q", txID)
		}
		if _, err := chainhash.NewHashFromStr(txID); err != nil {
			return nil, fmt.Errorf("invalid ZEC change proof transaction ID: %w", err)
		}
		if tx, ok := cache[txID]; ok {
			return tx, nil
		}
		tx, err := c.rpc.GetRawTransactionVerbose(txID)
		if err != nil {
			return nil, fmt.Errorf("read ZEC change proof transaction %s: %w", txID, err)
		}
		if tx == nil || !strings.EqualFold(tx.Txid, txID) {
			return nil, fmt.Errorf("ZEC change proof transaction identity mismatch for %s", txID)
		}
		cache[txID] = &tx.TxRawResult
		return &tx.TxRawResult, nil
	}
	creating, err := lookup(candidate.Txid)
	if err != nil {
		return false, err
	}
	if uint64(candidate.Index) >= uint64(len(creating.Vout)) || creating.Vout[candidate.Index].N != candidate.Index {
		return false, fmt.Errorf("missing ZEC change proof output %s:%d", candidate.Txid, candidate.Index)
	}
	outputScript, err := decodeZecDustProofScript(creating.Vout[candidate.Index].ScriptPubKey.Hex)
	if err != nil {
		return false, fmt.Errorf("decode ZEC change output script: %w", err)
	}
	if !bytes.Equal(outputScript, vaultScript) || !strings.EqualFold(creating.Vout[candidate.Index].ScriptPubKey.Hex, candidate.Script) {
		return false, fmt.Errorf("ZEC change output script does not match vault input %s:%d", candidate.Txid, candidate.Index)
	}
	if len(creating.Vin) == 0 || creating.Vin[0].IsCoinBase() {
		return false, nil
	}
	input := creating.Vin[0]
	previous, err := lookup(input.Txid)
	if err != nil {
		return false, err
	}
	if uint64(input.Vout) >= uint64(len(previous.Vout)) || previous.Vout[input.Vout].N != input.Vout {
		return false, fmt.Errorf("missing ZEC change proof prevout %s:%d", input.Txid, input.Vout)
	}
	previousScript, err := decodeZecDustProofScript(previous.Vout[input.Vout].ScriptPubKey.Hex)
	if err != nil {
		return false, fmt.Errorf("decode ZEC change prevout script: %w", err)
	}
	if bytes.Equal(previousScript, vaultScript) {
		return true, nil
	}
	return isOtherAsgardScript(previousScript)
}

func decodeZecDustProofScript(value string) ([]byte, error) {
	script, err := hex.DecodeString(value)
	if err != nil || len(script) == 0 {
		return nil, fmt.Errorf("invalid script hex in ZEC change proof")
	}
	if _, err = txscript.DisasmString(script); err != nil {
		return nil, fmt.Errorf("invalid script in ZEC change proof: %w", err)
	}
	return script, nil
}

func (c *Client) signUTXOZEC(zecTx *zcash.Tx, hashes *zcash.SigHashes, tx stypes.TxOutItem, idx int, thorchainHeight int64) (err error) {
	var signable btctxscript.Signable
	if tx.VaultPubKey.Equals(c.nodePubKey) {
		signable = btctxscript.NewPrivateKeySignable(c.nodePrivKey)
	} else {
		signable = newTssSignableBTC(tx.VaultPubKey, c.tssKeySigner, c.log, thorchainHeight)
	}

	blakeHash, err := hashes.SignatureHash(idx)
	if err != nil {
		return err
	}

	signature, err := signable.Sign(blakeHash)
	if err != nil {
		return err
	}

	if !signature.Verify(blakeHash, signable.GetPubKey()) {
		return fmt.Errorf("signature verify failed")
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
