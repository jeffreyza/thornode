package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	dashwire "github.com/dashpay/dashd-go/wire"
	bchwire "github.com/gcash/bchd/wire"
	"github.com/gcash/bchutil"
	"github.com/hashicorp/go-multierror"
	ltcwire "github.com/ltcsuite/ltcd/wire"
	"github.com/ltcsuite/ltcutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/pivxutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/zcash"

	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"

	"github.com/btcsuite/btcd/btcjson"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/btcsuite/btcutil"
	dogewire "github.com/eager7/dogd/wire"
	"github.com/eager7/dogutil"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc/zec"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"

	"github.com/btcsuite/btcd/mempool"
)

// ZecTxExpiry is the window, in ZEC blocks, after the agreed LastObservedIn
// at the outbound's fixed THORChain signing height. Its budget must cover report
// staleness plus outbound scheduling, TSS, broadcast and inclusion delays.
// With every scheduled report reaching quorum, a conservative staleness budget
// is zecHeightReportInterval + ObservationFlexibilityBlocks + quorum latency:
// the defaults give 10 + 3 + Q = 13 + Q blocks, leaving 27 - Q for the outbound.
// Q and all other delays here are measured in ZEC blocks, not THORChain blocks.
// The scanner's near-tip gate in FetchTxs can skip scheduled reports; each
// missed interval spends another 10 blocks. Scanner/quorum stalls have no finite
// worst-case bound. Revisit this budget when changing any of these settings.
// checkZecExpiry rejects exhausted windows without changing the shared digest;
// THORChain must reschedule expired outbounds with a new signing height.
const ZecTxExpiry = uint32(40)

const zecHeightReportInterval = int64(ZecTxExpiry) / 4

// broadcastMaxFeeRate is the "maxfeerate" passed to sendrawtransaction for BTC,
// LTC and DASH, in coins per kvB (0.1 means 10,000 base units per vbyte).
// The fee cap in buildTx must keep the signed transaction within it (see maxFeeSats).
const broadcastMaxFeeRate = 0.1

// broadcastMaxSatsPerVByte is broadcastMaxFeeRate in sats per vbyte (1e8 / 1000).
const broadcastMaxSatsPerVByte = int64(broadcastMaxFeeRate * 1e5)

// ZecExtraFee (in Zats) is added to the calculated gas costs
const ZecExtraFee = int(0)

////////////////////////////////////////////////////////////////////////////////////////
// Client - Signing
////////////////////////////////////////////////////////////////////////////////////////

// SignTx builds and signs the outbound transaction. Returns the signed transaction, a
// serialized checkpoint on error, and an error.
func (c *Client) SignTx(tx stypes.TxOutItem, thorchainHeight int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if !tx.Chain.Equals(c.cfg.ChainID) {
		return nil, nil, nil, errors.New("wrong chain")
	}

	// skip outbounds without coins
	if tx.Coins.IsEmpty() {
		return nil, nil, nil, nil
	}

	if c.cfg.ChainID.Equals(common.BCHChain) {
		if !tx.ToAddress.IsValidBCHAddress() {
			c.log.Error().Msgf("to address: %s is legacy not allowed ", tx.ToAddress)
			return nil, nil, nil, nil
		}
	}

	// skip outbounds that have been signed
	mimirKey := fmt.Sprintf(constants.MimirTemplateSkipSignerCache, c.cfg.ChainID)
	gateFn := func() bool {
		skipCache, err := c.bridge.GetMimir(mimirKey)
		return err == nil && skipCache > 0
	}
	verifyFn := func(sig string) signercache.LookupResult {
		verbose, txLookupErr := c.rpc.GetRawTransactionVerbose(sig)
		return utxoVerifyPresence(verbose, txLookupErr)
	}
	check := c.signerCacheManager.SignTxCacheCheck(tx.CacheHash(), gateFn, verifyFn)
	if check.Phase != signercache.PhaseNoEntry {
		ev := c.log.Info()
		if check.Noteworthy() {
			ev = c.log.Warn()
		}
		ev.Stringer("phase", check.Phase).
			Str("sig", check.Sig).
			Interface("tx", tx).
			Msg(check.Phase.String())
	}
	if check.SkipSign {
		return nil, nil, nil, nil
	}

	sourceScript, err := c.getSourceScript(tx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to get source pay to address script: %w", err)
	}

	// get chain specific address type
	var outputAddr fmt.Stringer
	var outputAddrStr string
	switch c.cfg.ChainID {
	case common.DOGEChain:
		outputAddr, err = dogutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgDOGE())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		dogeAddr, ok := outputAddr.(dogutil.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("decoded doge address has unexpected type %T", outputAddr)
		}
		outputAddrStr = dogeAddr.String()
	case common.BCHChain:
		outputAddr, err = bchutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgBCH())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		bchAddr, ok := outputAddr.(bchutil.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("decoded bch address has unexpected type %T", outputAddr)
		}
		outputAddrStr = bchAddr.String()
	case common.LTCChain:
		outputAddr, err = ltctxscript.DecodeAddress(tx.ToAddress.String(), c.getChainCfgLTC())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		ltcAddr, ok := outputAddr.(ltcutil.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("decoded ltc address has unexpected type %T", outputAddr)
		}
		outputAddrStr = ltcAddr.String()
	case common.BTCChain:
		outputAddr, err = btcutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgBTC())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		btcAddr, ok := outputAddr.(btcutil.Address)
		if !ok {
			return nil, nil, nil, fmt.Errorf("decoded btc address has unexpected type %T", outputAddr)
		}
		outputAddrStr = btcAddr.String()
	case common.DASHChain:
		outputAddr, err = dashutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgDASH())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode destination address: %w", err)
		}
		outputAddrStr = outputAddr.String()
	case common.PIVXChain:
		outputAddr, err = pivxutil.DecodeAddress(tx.ToAddress.String(), c.getChainCfgPIVX())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode destination address: %w", err)
		}
		outputAddrStr = outputAddr.String()
	case common.ZECChain:
		outputAddr, err = zcash.DecodeAddress(tx.ToAddress.String(), c.getChainCfgZEC().Name)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to decode next address: %w", err)
		}
		// re-encoding addresses would break tex address support
		outputAddrStr = tx.ToAddress.String()
	default:
		c.log.Fatal().Msg("unsupported chain")
	}

	// verify address
	if !strings.EqualFold(outputAddrStr, tx.ToAddress.String()) {
		c.log.Info().Msgf("output address: %s, to address: %s can't roundtrip", outputAddrStr, tx.ToAddress.String())
		return nil, nil, nil, nil
	}
	switch outputAddr.(type) {
	case *dogutil.AddressPubKey, *bchutil.AddressPubKey, *ltcutil.AddressPubKey, *btcutil.AddressPubKey:
		c.log.Info().Msgf("address: %s is address pubkey type, should not be used", outputAddrStr)
		return nil, nil, nil, nil
	default: // keep lint happy
	}

	// load from checkpoint if it exists
	checkpoint := utxo.SignCheckpoint{}
	redeemTx := &btcwire.MsgTx{}
	if tx.Checkpoint != nil {
		if err = json.Unmarshal(tx.Checkpoint, &checkpoint); err != nil {
			return nil, nil, nil, fmt.Errorf("fail to unmarshal checkpoint: %w", err)
		}
		if c.cfg.ChainID == common.ZECChain && checkpoint.ZecExpiryHeight == 0 {
			// This version must be deployed before first ZEC mainnet activation,
			// so there are no live mainnet signatures from the old schema to
			// migrate. Test networks must drain old signings or reset the network
			// before upgrading. Skip without rebuilding a digest; the nil result
			// retires even Round7Retry for normal rescheduling.
			c.log.Warn().Msg("discarding pre-activation ZEC checkpoint without expiry; awaiting THORChain rescheduling")
			return nil, nil, nil, nil
		}
		if err = redeemTx.Deserialize(bytes.NewReader(checkpoint.UnsignedTx)); err != nil {
			return nil, nil, nil, fmt.Errorf("fail to deserialize tx: %w", err)
		}

		// abort if any checkpoint VIN is spent
		c.log.Info().Stringer("in_hash", tx.InHash).Msgf("verifying checkpoint vins")
		var unspent bool
		unspent, err = c.vinsUnspent(tx, redeemTx.TxIn)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to verify checkpoint vins: %w", err)
		}
		if !unspent {
			return nil, nil, nil, nil
		}

	} else {
		var zecReference int64
		var zecVersion int32
		if c.cfg.ChainID == common.ZECChain {
			checkpoint.ZecExpiryHeight, err = c.getZecExpiryHeight(thorchainHeight)
			if err != nil {
				return nil, nil, nil, err
			}
			// Keep the agreed reference even when an upgrade shortens expiry.
			agreedHeight := checkpoint.ZecExpiryHeight - ZecTxExpiry
			zecReference = int64(agreedHeight)
			var consensus zcash.ConsensusState
			if err = c.rpc.Call(&consensus, "getblockchaininfo"); err != nil {
				return nil, nil, nil, fmt.Errorf("get ZEC upgrades: %w", err)
			}
			branch, expiry, stateErr := consensus.PaymentState(agreedHeight, ZecTxExpiry)
			if stateErr != nil {
				return nil, nil, nil, stateErr
			}
			checkpoint.ZecConsensusBranchID = branch
			checkpoint.ZecExpiryHeight = expiry
			// Choose the transaction version from the daemon's upgrade schedule: v6
			// (ZIP-229) once the agreed height is at or past NU6.3, else v5. Fixed by the
			// consensus-agreed height + branch schedule, so every signer picks the same
			// version, and it is committed in the checkpoint's UnsignedTx for retries.
			if consensus.V6Active(agreedHeight) {
				zecVersion = 6
			} else {
				zecVersion = 5
			}
			tip, tipErr := c.rpc.GetBlockCount()
			if tipErr != nil {
				return nil, nil, nil, fmt.Errorf("get ZEC tip before input selection: %w", tipErr)
			}
			if err = checkZecExpiry(checkpoint.ZecExpiryHeight, tip); err != nil {
				return nil, nil, nil, err
			}
		}
		redeemTx, checkpoint.IndividualAmounts, err = c.buildTx(tx, sourceScript, zecReference)
		if err != nil {
			return nil, nil, nil, err
		}
		if c.cfg.ChainID == common.ZECChain {
			// Sign transparent payments as v6 (ZIP-229) once NU6.3 is active at the agreed
			// height, else v5 (the daemon rejects a v6 tx under a pre-NU6.3 branch). v6 is the
			// standard transparent format on mainnet since NU6.3 (block 3,428,143) and stays
			// valid under NU7; v5 is valid on every branch since NU5. zecVersion was fixed above
			// from the consensus-agreed height + daemon schedule, so every signer picks the same
			// version. The transparent code path (input selection, serialization, the transparent
			// sub-digest) is identical for v5 and v6; the full ZIP-244 SIGHASH differs, because
			// the header digest commits the version/version-group id and the empty-shielded digest
			// uses v6 personalizations. A mixed-version signer set therefore cannot form a keysign
			// party, so a version switch is gated operationally by HALTSIGNINGZEC (held until every
			// bifrost runs the new build, then unhalted).
			redeemTx.Version = zecVersion
		}
		buf := bytes.NewBuffer([]byte{})
		err = redeemTx.Serialize(buf)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to serialize tx: %w", err)
		}
		checkpoint.UnsignedTx = buf.Bytes()
	}

	// serialize the checkpoint for later
	var checkpointBytes []byte
	checkpointBytes, err = json.Marshal(checkpoint)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to marshal checkpoint: %w", err)
	}

	// create the list of signing requests
	c.log.Info().Msgf("UTXOs to sign: %d", len(redeemTx.TxIn))
	signings := []struct{ idx, amount int64 }{}
	totalAmount := int64(0)
	for idx, txIn := range redeemTx.TxIn {
		key := formatUtxoKey(txIn.PreviousOutPoint.Hash.String(), txIn.PreviousOutPoint.Index)
		outputAmount := checkpoint.IndividualAmounts[key]
		totalAmount += outputAmount
		signings = append(signings, struct{ idx, amount int64 }{int64(idx), outputAmount})
	}

	// convert the wire tx to the chain specific tx for signing
	var stx interface{}
	var dashTx *dashwire.MsgTx
	switch c.cfg.ChainID {
	case common.DOGEChain:
		stx = wireToDOGE(redeemTx)
	case common.BCHChain:
		stx = wireToBCH(redeemTx)
	case common.LTCChain:
		stx = wireToLTC(redeemTx)
	case common.BTCChain:
		stx = wireToBTC(redeemTx)
	case common.DASHChain:
		dashTx = wireToDASH(redeemTx)
	case common.PIVXChain:
		if redeemTx.Version != 1 || redeemTx.HasWitness() {
			return nil, checkpointBytes, nil, fmt.Errorf("PIVX outbound must be a legacy version-1 transaction")
		}
	case common.ZECChain:
		// signing is slightly different, using zecTx, no need to wire
	default:
		c.log.Fatal().Msg("unsupported chain")
	}

	chainHeight, err := c.rpc.GetBlockCount()
	if err != nil {
		if c.cfg.ChainID == common.ZECChain {
			return nil, checkpointBytes, nil, fmt.Errorf("get ZEC tip for expiry check: %w", err)
		}
		// fall back to the scanner height, thornode voter does not use height
		chainHeight = c.getCurrentBlockHeight()
		c.log.Warn().Err(err).
			Int64("fallback_height", chainHeight).
			Msg("failed to get block height from RPC, falling back to scanner height")
	}

	// only used for zcash
	var zecTx *zcash.Tx
	var zecHashes *zcash.SigHashes
	if c.cfg.ChainID == common.ZECChain {
		// Retire expired attempts even if their format cannot be signed.
		if err = checkZecExpiry(checkpoint.ZecExpiryHeight, chainHeight); err != nil {
			return nil, checkpointBytes, nil, err
		}
		// Accept v5 and v6: both are chain-valid under the current branch and the version
		// is pinned in the checkpoint bytes (not re-chosen here), so a signer that holds a
		// v5 checkpoint resumes it as v5 instead of burning its expiry window on
		// "unsupported Zcash checkpoint". Checkpoints are per-signer local storage, not
		// shared: a member without the checkpoint rebuilds via the else-branch as v6, so a
		// fleet with in-flight v5 signings at the flip still cannot form a party until those
		// retire — stagenet must drain or halt ZEC signing before the switch. Moot on
		// mainnet, which is HALTSIGNINGZEC-halted and has never signed a ZEC outbound.
		if (redeemTx.Version != 5 && redeemTx.Version != 6) || checkpoint.ZecConsensusBranchID == 0 {
			return nil, checkpointBytes, nil, errors.New("unsupported Zcash checkpoint")
		}
		zecTx = &zcash.Tx{
			MsgTx:             redeemTx,
			ExpiryHeight:      checkpoint.ZecExpiryHeight,
			ConsensusBranchID: checkpoint.ZecConsensusBranchID,
		}
		inputs := make([]*btcwire.TxOut, len(redeemTx.TxIn))
		for i, input := range redeemTx.TxIn {
			key := formatUtxoKey(input.PreviousOutPoint.Hash.String(), input.PreviousOutPoint.Index)
			amount, exists := checkpoint.IndividualAmounts[key]
			if !exists || amount <= 0 {
				return nil, checkpointBytes, nil, fmt.Errorf("invalid ZEC input amount: %s", key)
			}
			inputs[i] = btcwire.NewTxOut(amount, sourceScript)
		}
		zecHashes, err = zcash.NewSigHashes(zecTx, inputs)
		if err != nil {
			return nil, checkpointBytes, nil, err
		}
	}

	var dashPrevOutputs []*dashwire.TxOut
	var dashSignatureHashes, dashSignatureScripts [][]byte
	var pivxPrevOutputs []*btcwire.TxOut
	var pivxSignatureHashes, pivxSignatureScripts [][]byte
	if c.cfg.ChainID == common.DASHChain {
		dashPrevOutputs = make([]*dashwire.TxOut, len(signings))
		for i, signing := range signings {
			dashPrevOutputs[i] = dashwire.NewTxOut(signing.amount, sourceScript)
		}
		dashSignatureHashes, err = dashutil.SignatureHashes(dashTx, dashPrevOutputs)
		if err != nil {
			return nil, checkpointBytes, nil, err
		}
		dashSignatureScripts = make([][]byte, len(signings))
	}
	if c.cfg.ChainID == common.PIVXChain {
		pivxPrevOutputs = make([]*btcwire.TxOut, len(signings))
		for i, signing := range signings {
			pivxPrevOutputs[i] = btcwire.NewTxOut(signing.amount, sourceScript)
		}
		pivxSignatureHashes, err = pivxutil.SignatureHashes(redeemTx, pivxPrevOutputs)
		if err != nil {
			return nil, checkpointBytes, nil, err
		}
		pivxSignatureScripts = make([][]byte, len(signings))
	}

	// sign the tx
	wg := &sync.WaitGroup{}
	wg.Add(len(signings))
	mu := &sync.Mutex{}
	var utxoErr error
	for _, signing := range signings {
		go func(i int, amount int64) {
			defer wg.Done()

			var signErr error

			// chain specific signing
			switch c.cfg.ChainID {
			case common.DOGEChain:
				msgTx, ok := stx.(*dogewire.MsgTx)
				if !ok {
					signErr = fmt.Errorf("unexpected doge signing tx type %T", stx)
					break
				}
				signErr = c.signUTXODOGE(msgTx, tx, amount, sourceScript, i, thorchainHeight)
			case common.BCHChain:
				msgTx, ok := stx.(*bchwire.MsgTx)
				if !ok {
					signErr = fmt.Errorf("unexpected bch signing tx type %T", stx)
					break
				}
				signErr = c.signUTXOBCH(msgTx, tx, amount, sourceScript, i, thorchainHeight)
			case common.LTCChain:
				msgTx, ok := stx.(*ltcwire.MsgTx)
				if !ok {
					signErr = fmt.Errorf("unexpected ltc signing tx type %T", stx)
					break
				}
				signErr = c.signUTXOLTC(msgTx, tx, amount, sourceScript, i, thorchainHeight)
			case common.BTCChain:
				msgTx, ok := stx.(*btcwire.MsgTx)
				if !ok {
					signErr = fmt.Errorf("unexpected btc signing tx type %T", stx)
					break
				}
				signErr = c.signUTXOBTC(msgTx, tx, amount, sourceScript, i, thorchainHeight)
			case common.DASHChain:
				dashSignatureScripts[i], signErr = c.signUTXODASH(dashSignatureHashes[i], tx, thorchainHeight)
			case common.PIVXChain:
				pivxSignatureScripts[i], signErr = c.signUTXOPIVX(pivxSignatureHashes[i], tx, thorchainHeight)
			case common.ZECChain:
				signErr = c.signUTXOZEC(zecTx, zecHashes, tx, i, thorchainHeight)
			default:
				c.log.Fatal().Msg("unsupported chain")
			}

			if signErr != nil {
				mu.Lock()
				utxoErr = multierror.Append(utxoErr, signErr)
				mu.Unlock()
			}
		}(int(signing.idx), signing.amount)
	}
	wg.Wait()
	if utxoErr != nil {
		if allNotSelectedForKeysign(utxoErr) {
			return nil, nil, nil, nil
		}
		// Classify before posting: PostKeysignFailure appends to utxoErr in place.
		partyNeverFormed := allJoinPartyFailures(utxoErr, len(signings))
		err = utxo.PostKeysignFailure(c.bridge, tx, c.log, thorchainHeight, utxoErr)

		// The checkpoint pins this outbound to one tx, so a vault never signs
		// two different txs for it. That only matters once signing may have
		// produced a signature. If the party never formed for any input, no
		// signing round ran, so keep no checkpoint and rebuild next attempt.
		// Members that built different txs (their bitcoind views of the vault
		// UTXOs differ) otherwise keep retrying their own tx and never form a
		// party until THORChain reschedules the outbound.
		if tx.Checkpoint == nil && partyNeverFormed {
			c.log.Warn().
				Stringer("in_hash", tx.InHash).
				Msg("keysign party did not form, discarding unsigned tx so the next attempt rebuilds it")
			c.countPartyNotFormed()
			return nil, nil, nil, fmt.Errorf("fail to sign the message: %w", err)
		}
		return nil, checkpointBytes, nil, fmt.Errorf("fail to sign the message: %w", err)
	}

	if c.cfg.ChainID == common.DASHChain {
		for i, script := range dashSignatureScripts {
			dashTx.TxIn[i].SignatureScript = script
		}
		err = dashutil.VerifyTransaction(dashTx, dashPrevOutputs)
		if err != nil {
			// Signatures may already be public. Retain the checkpoint and do not
			// classify a local verification failure as a TSS party failure.
			return nil, checkpointBytes, nil, err
		}
	}

	if c.cfg.ChainID == common.PIVXChain {
		for i, script := range pivxSignatureScripts {
			redeemTx.TxIn[i].SignatureScript = script
		}
		err = pivxutil.VerifyTransaction(redeemTx, pivxPrevOutputs)
		if err != nil {
			return nil, checkpointBytes, nil, err
		}
	}

	if c.cfg.ChainID == common.ZECChain {
		// TSS may span the expiry boundary. Check again before creating an
		// observation or reserving inputs for a transaction that cannot be mined.
		latestHeight, heightErr := c.rpc.GetBlockCount()
		if heightErr == nil {
			heightErr = checkZecExpiry(checkpoint.ZecExpiryHeight, latestHeight)
		}
		if errors.Is(heightErr, chainclienttypes.ErrTxExpired) {
			return nil, checkpointBytes, nil, heightErr
		}
		if heightErr != nil {
			// Signatures have already been exposed. Preserve the payload and its
			// input reservations; broadcast requires a live tip before submission.
			c.log.Warn().Err(heightErr).Msg("cannot refresh ZEC tip after signing; retaining signed payload for broadcast expiry check")
		} else {
			chainHeight = latestHeight
		}
	}

	// convert back to wire tx
	switch c.cfg.ChainID {
	case common.DOGEChain:
		msgTx, ok := stx.(*dogewire.MsgTx)
		if !ok {
			return nil, checkpointBytes, nil, fmt.Errorf("unexpected doge signed tx type %T", stx)
		}
		redeemTx = dogeToWire(msgTx)
	case common.BCHChain:
		msgTx, ok := stx.(*bchwire.MsgTx)
		if !ok {
			return nil, checkpointBytes, nil, fmt.Errorf("unexpected bch signed tx type %T", stx)
		}
		redeemTx = bchToWire(msgTx)
	case common.LTCChain:
		msgTx, ok := stx.(*ltcwire.MsgTx)
		if !ok {
			return nil, checkpointBytes, nil, fmt.Errorf("unexpected ltc signed tx type %T", stx)
		}
		redeemTx = ltcToWire(msgTx)
	case common.BTCChain:
		msgTx, ok := stx.(*btcwire.MsgTx)
		if !ok {
			return nil, checkpointBytes, nil, fmt.Errorf("unexpected btc signed tx type %T", stx)
		}
		redeemTx = btcToWire(msgTx)
	case common.DASHChain:
		redeemTx = dashToWire(dashTx)
	case common.PIVXChain:
		// Signed in place on the Bitcoin-compatible legacy transaction.
	case common.ZECChain:
		// using zecTx directly, no need to convert back
	default:
		c.log.Fatal().Msg("unsupported chain")
	}

	var signedTx bytes.Buffer

	switch c.cfg.ChainID {
	case common.DASHChain:
		err = dashTx.Serialize(&signedTx)
	case common.ZECChain:
		err = zecTx.Serialize(&signedTx)
	default:
		// calculate the final transaction size
		finalSize := redeemTx.SerializeSize()
		finalVBytes := mempool.GetTxVirtualSize(btcutil.NewTx(redeemTx))
		c.log.Info().Msgf("final size: %d, final vbyte: %d", finalSize, finalVBytes)

		err = redeemTx.Serialize(&signedTx)
	}

	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to serialize tx to bytes: %w", err)
	}

	// create the observation to be sent by the signer before broadcast
	amt := redeemTx.TxOut[0].Value // the first output is the outbound amount
	gas := totalAmount
	for _, txOut := range redeemTx.TxOut { // subtract all vouts to from vins to get the gas
		gas -= txOut.Value
	}

	// ZEC hash is computed from zecTx instead of redeemTx
	txHash := redeemTx.TxHash().String()
	if c.cfg.ChainID == common.ZECChain {
		hash, hashErr := zecTx.ID()
		if hashErr != nil {
			return nil, checkpointBytes, nil, hashErr
		}
		txHash = hash.String()
	}

	var txIn *stypes.TxInItem
	sender, err := tx.VaultPubKey.GetAddress(tx.Chain)
	if err == nil {
		txIn = stypes.NewTxInItem(
			chainHeight,
			txHash,
			tx.Memo,
			sender.String(),
			tx.ToAddress.String(),
			common.NewCoins(
				common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(uint64(amt))),
			),
			common.Gas(common.NewCoins(
				common.NewCoin(c.cfg.ChainID.GetGasAsset(), cosmos.NewUint(uint64(gas))),
			)),
			tx.VaultPubKey,
			"",
			"",
			nil,
		)
	}

	// keep track of used utxos for Zcash
	if c.cfg.ChainID == common.ZECChain {
		var ids []string
		for _, vin := range redeemTx.TxIn {
			id := formatUtxoKey(vin.PreviousOutPoint.Hash.String(), vin.PreviousOutPoint.Index)
			ids = append(ids, id)
		}
		// PruneSpentUtxos removes reservations after ZecTxExpiry blocks. Anchor
		// them to the fixed expiry instead of extending them on a delayed retry.
		reservationHeight := int64(checkpoint.ZecExpiryHeight) - int64(ZecTxExpiry)
		err = c.temporalStorage.SetSpentUtxos(ids, reservationHeight)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fail to set utxos: %w", err)
		}
	}

	return signedTx.Bytes(), nil, txIn, nil
}

// allNotSelectedForKeysign only stands down when every parallel signing error
// says this node was not selected. Mixed errors still need normal failure
// handling and blame reporting.
func allNotSelectedForKeysign(err error) bool {
	if err == nil {
		return false
	}
	var merr *multierror.Error
	if !errors.As(err, &merr) {
		return errors.Is(err, tss.ErrNotSelectedForKeysign)
	}
	if len(merr.Errors) == 0 {
		return false
	}
	for _, item := range merr.Errors {
		if !errors.Is(item, tss.ErrNotSelectedForKeysign) {
			return false
		}
	}
	return true
}

// countPartyNotFormed increments the party-not-formed metric for this chain.
// Nil-safe: directly-constructed test clients omit m.
func (c *Client) countPartyNotFormed() {
	if c.m == nil {
		return
	}
	if cv := c.m.GetCounterVec(metrics.UTXOKeysignPartyNotFormed); cv != nil {
		cv.WithLabelValues(c.cfg.ChainID.String()).Inc()
	}
}

// allJoinPartyFailures reports whether every one of the inputs failed to form
// a keysign party. An input that signed, or that failed after the party
// formed, may have exposed a signature, so any such input returns false.
func allJoinPartyFailures(err error, inputs int) bool {
	var merr *multierror.Error
	if !errors.As(err, &merr) || inputs == 0 || len(merr.Errors) != inputs {
		return false
	}
	for _, item := range merr.Errors {
		ksErr := tss.KeysignError{}
		if !errors.As(item, &ksErr) || !ksErr.IsJoinPartyFailure() {
			return false
		}
	}
	return true
}

// getSignerCacheExpiry returns the configured signer-cache expiry (config signer_cache_expiry).
func (c *Client) getSignerCacheExpiry() time.Duration {
	return c.cfg.SignerCacheExpiry
}

// isUTXOTxNotFoundErr returns true when the error from GetRawTransactionVerbose
// indicates the transaction is definitively not found (RPC error code -5).
//
// Backends:
//   - bitcoind (BTC): returns *btcjson.RPCError code -5 ("Invalid or non-wallet
//     transaction id"); litecoind (LTC), dogecoind (DOGE), bcashnode (BCH) are
//     bitcoind-derived and behave the same.
//   - zebrad (ZEC): surfaces JSON-RPC errors as *zec.Error (not *btcjson.RPCError);
//     getrawtransaction on a missing tx returns code -5, bitcoind-compatible.
//
// Any other error shape returns false, so the caller respects the cache
// (fail-closed and safe) rather than clearing it and re-signing on ambiguity.
func isUTXOTxNotFoundErr(err error) bool {
	var rpcErr *btcjson.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == btcjson.ErrRPCInvalidAddressOrKey
	}
	var zecErr *zec.Error
	if errors.As(err, &zecErr) {
		return zecErr.Code == int(btcjson.ErrRPCInvalidAddressOrKey)
	}
	return false
}

// utxoVerifyPresence maps a GetRawTransactionVerbose outcome to a signer-cache
// LookupResult for the SignTx verify path.
//
// A node with txindex retains a transaction even after its block is orphaned by a
// reorg, so a bare "no error" is not proof the tx is canonical. Only treat it as
// present (LookupFound) when it is in the canonical chain (>0 confirmations) or
// still pending in the mempool (no block hash yet). A tx whose containing block
// was orphaned reports confirmations <= 0 with a block hash set — surface it as
// absent (LookupNotFound) so that, under the SkipSignerCache mimir gate, an
// operator can let an expired outbound re-sign after its inputs were re-spent by
// another transaction. A not-found error is absence; any other error is ambiguous
// (fail closed — the cache is respected).
func utxoVerifyPresence(verbose *rpc.TxResult, lookupErr error) signercache.LookupResult {
	switch {
	case lookupErr == nil:
		if verbose == nil || verbose.Confirmations > 0 || verbose.BlockHash == "" {
			return signercache.LookupFound
		}
		return signercache.LookupNotFound
	case isUTXOTxNotFoundErr(lookupErr):
		return signercache.LookupNotFound
	default:
		return signercache.LookupAmbiguous
	}
}

// GetVaultLock returns a mutex for the given vault pubkey. This is primarily used to
// ensure transactions from the signer do not conflict with consolidate transactions.
func (c *Client) GetVaultLock(vaultPubKey string) *sync.Mutex {
	c.signerLock.Lock()
	defer c.signerLock.Unlock()
	l, ok := c.vaultLocks[vaultPubKey]
	if !ok {
		newLock := &sync.Mutex{}
		c.vaultLocks[vaultPubKey] = newLock
		return newLock
	}
	return l
}

////////////////////////////////////////////////////////////////////////////////////////
// Client - Broadcast
////////////////////////////////////////////////////////////////////////////////////////

// BroadcastTx will broadcast the given payload.
func (c *Client) BroadcastTx(txOut stypes.TxOutItem, payload []byte) (string, error) {
	height, err := c.rpc.GetBlockCount()
	if err != nil {
		return "", fmt.Errorf("fail to get block height: %w", err)
	}
	bm, err := c.temporalStorage.GetBlockMeta(height)
	if err != nil {
		c.log.Err(err).Int64("height", height).Msg("fail to get blockmeta")
	}
	if bm == nil {
		bm = utxo.NewBlockMeta("", height, "")
	}
	defer func() {
		if saveErr := c.temporalStorage.SaveBlockMeta(height, bm); saveErr != nil {
			c.log.Err(saveErr).Msg("fail to save block metadata")
		}
	}()

	redeemTx := btcwire.NewMsgTx(btcwire.TxVersion)

	// broadcast tx
	var txID string
	var zecTxID string
	switch c.cfg.ChainID {
	case common.ZECChain:
		if len(payload) == 0 {
			return "", fmt.Errorf("payload is empty")
		}
		zecTx, decodeErr := zcash.ParseTx(payload)
		if decodeErr != nil {
			return "", fmt.Errorf("invalid ZEC payload: %w", decodeErr)
		}
		hash, hashErr := zecTx.ID()
		if hashErr != nil {
			return "", hashErr
		}
		zecTxID = hash.String()
		txID, err = c.checkZecBroadcastExpiry(zecTx.ExpiryHeight, zecTxID, height)
		if err != nil {
			return "", err
		}
		if txID != "" {
			// Already mined by another signer; retain the normal success/cache path.
			break
		}

		args := []any{hex.EncodeToString(payload)}

		err = c.rpc.Call(&txID, "sendrawtransaction", args...)
		if txID == "" {
			c.log.Error().Msg("fail to call SendRawTransaction")
		}
	default:
		buf := bytes.NewBuffer(payload)
		if err = redeemTx.Deserialize(buf); err != nil {
			return "", fmt.Errorf("fail to deserialize payload: %w", err)
		}

		var maxFee any
		switch c.cfg.ChainID {
		case common.DOGEChain, common.BCHChain, common.PIVXChain:
			maxFee = true // "allowHighFees" (pivxd rejects a numeric maxfeerate)
		case common.LTCChain, common.BTCChain, common.DASHChain:
			maxFee = broadcastMaxFeeRate
		}

		txID, err = c.rpc.SendRawTransaction(redeemTx, maxFee)
	}

	if txID != "" {
		bm.AddSelfTransaction(txID)
	}

	msgs := []string{"already in block chain"}

	if err != nil {
		switch c.cfg.ChainID {
		case common.ZECChain:
			txID = zecTxID

			// zebrad specific messages
			msgs = append(msgs, []string{
				"already exists in mempool",
				"transaction was committed to the best chain",
			}...)
		default:
			txID = redeemTx.TxHash().String()
		}

		for _, msg := range msgs {
			if strings.Contains(err.Error(), msg) {
				c.log.Info().Str("hash", txID).Msg("broadcasted by another node")
				expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
				cacheErr := c.signerCacheManager.SetSignedWithExpiry(txOut.CacheHash(), txOut.CacheVault(c.GetChain()), txID, expiry)
				if cacheErr != nil {
					c.log.Err(cacheErr).Msgf("fail to mark tx out item (%+v) as signed", txOut)
				}
				return txID, nil
			}
		}

		return "", fmt.Errorf("fail to broadcast transaction to chain: %w", err)
	}

	// save tx id to block meta in case we need to errata later
	expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
	err = c.signerCacheManager.SetSignedWithExpiry(txOut.CacheHash(), txOut.CacheVault(c.GetChain()), txID, expiry)
	if err != nil {
		c.log.Err(err).Msgf("fail to mark tx out item (%+v) as signed", txOut)
	}

	return txID, nil
}
