package signer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"

	"github.com/btcsuite/btcd/btcec"
	"github.com/cenkalti/backoff"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/observer"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Signer will pull the tx out from thorchain and then forward it to chain
type Signer struct {
	logger                zerolog.Logger
	cfg                   config.Bifrost
	wg                    *sync.WaitGroup
	thorchainBridge       thorclient.ThorchainBridge
	stopChan              chan struct{}
	blockScanner          *blockscanner.BlockScanner
	thorchainBlockScanner *ThorchainBlockScan
	chains                map[common.Chain]chainclients.ChainClient
	storage               SignerStorage
	m                     *metrics.Metrics
	errCounter            *prometheus.CounterVec
	tssKeygen             keygenGenerator
	tssServer             *tssp.TssServer
	pubkeyMgr             pubkeymanager.PubKeyValidator
	constantsProvider     *ConstantsProvider
	localPubKeyECDSA      common.PubKey
	localPubKeyEDDSA      common.PubKey
	tssKeysignMetricMgr   *metrics.TssKeysignMetricMgr
	observer              signerObserver
	pipeline              *pipeline
	// The normal keygen consumer is serial, but scheduled retries re-enter from
	// independent goroutines. Signer-owned admission prevents a retry or replayed
	// delivery from starting a second XMR sidecar DKG while one is already active.
	xmrKeygen xmrKeygenSlot
	// Waiting workers are joined before signer resources close. The mutex makes
	// worker admission and shutdown atomic; it is never held during an RPC.
	keygenWorkerMu sync.Mutex
	keygenWorkerWG sync.WaitGroup
	keygenRetries  sync.Map // keygenRetryID -> *keygenRetry
}

// keygenGenerator is the ordinary TSS execution boundary. Keeping its narrow
// contract explicit lets retry tests observe real dispatch without a network DKG.
type keygenGenerator interface {
	GenerateNewKey(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error)
}

// signerObserver keeps readiness and signed observations on one owner.
type signerObserver interface {
	ObserveSigned(types.TxIn)
	IsChainClientStarted(common.Chain) bool
}

type pendingKeygenBroadcast struct {
	height               int64
	poolPk               common.PubKey
	secp256k1Signature   []byte
	blame                []ttypes.Blame
	input                common.PubKeys
	keygenType           ttypes.KeygenType
	chains               common.Chains
	keygenTime           int64
	poolPubKeyEddsa      common.PubKey
	poolPubKeyFrost      common.PubKey
	frostKeyshareHex     string
	observedMoneroHeight int64
}

// keygenCheckBridge suppresses signer filtering for the self-check signature
// because the new key has not been saved into a vault policy yet.
type keygenCheckBridge struct {
	thorclient.ThorchainBridge
}

// GetKeysignPolicy returns an empty policy so go-tss falls back to the local
// keygen membership for the pre-vault check signature.
func (b keygenCheckBridge) GetKeysignPolicy(common.PubKey, int64) (thorclient.KeysignPolicy, error) {
	return thorclient.KeysignPolicy{}, nil
}

type keygenBackups struct {
	keyshares      []byte
	keysharesEddsa []byte
	keysharesFrost []byte
}

var (
	encryptKeysharesFn    = tss.EncryptKeyshares
	encryptRawKeysharesFn = tss.EncryptRawKeyshares
	keygenNowFn           = time.Now
)

const secp256k1VerificationSignatureTimeout = tss.RemoteSignTimeout + 30*time.Second

type vaultLocker interface {
	GetVaultLock(vaultPubKey string) *sync.Mutex
}

type vaultLockKeyResolver interface {
	GetVaultLockKey(tx types.TxOutItem) string
}

func chainVaultLock(chain chainclients.ChainClient, vaultPubKey string) *sync.Mutex {
	lockable, ok := chain.(vaultLocker)
	if !ok {
		return nil
	}
	return lockable.GetVaultLock(vaultPubKey)
}

func chainVaultLockKey(chain chainclients.ChainClient, tx types.TxOutItem) string {
	resolver, ok := chain.(vaultLockKeyResolver)
	if !ok {
		return tx.VaultPubKey.String()
	}
	key := strings.TrimSpace(resolver.GetVaultLockKey(tx))
	if key == "" {
		return tx.VaultPubKey.String()
	}
	return key
}

func txOutItemEqualsIgnoringGas(a, b types.TxOutItem) bool {
	a.MaxGas = b.MaxGas
	a.GasRate = b.GasRate
	return a.Equals(b)
}

func txOutItemGasChanged(a, b types.TxOutItem) bool {
	return a.GasRate != b.GasRate || !a.MaxGas.Equals(b.MaxGas)
}

// NewSigner create a new instance of signer
func NewSigner(cfg config.Bifrost,
	thorchainBridge thorclient.ThorchainBridge,
	thorKeys *thorclient.Keys,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	tssServer *tssp.TssServer,
	chains map[common.Chain]chainclients.ChainClient,
	m *metrics.Metrics,
	tssKeysignMetricMgr *metrics.TssKeysignMetricMgr,
	obs *observer.Observer,
) (*Signer, error) {
	storage, err := NewSignerStore(cfg.Signer.SignerDbPath, cfg.Signer.LevelDB, thorchainBridge.GetConfig().SignerPasswd)
	if err != nil {
		return nil, fmt.Errorf("fail to create thorchain scan storage: %w", err)
	}
	if tssKeysignMetricMgr == nil {
		return nil, fmt.Errorf("fail to create signer , tss keysign metric manager is nil")
	}
	var na *ttypes.NodeAccount
	for i := 0; i < 300; i++ { // wait for 5 min before timing out
		var signerAddr sdktypes.AccAddress
		signerAddr, err = thorKeys.GetSignerInfo().GetAddress()
		if err != nil {
			return nil, fmt.Errorf("failed to get address from thorKeys signer: %w", err)
		}
		na, err = thorchainBridge.GetNodeAccount(signerAddr.String())
		if err != nil {
			return nil, fmt.Errorf("fail to get node account from thorchain,err:%w", err)
		}

		if !na.PubKeySet.Secp256k1.IsEmpty() {
			break
		}
		time.Sleep(constants.ThorchainBlockTime)
		log.Info().Msg("Waiting for node account to be registered...")
	}

	if na.PubKeySet.Secp256k1.IsEmpty() {
		return nil, fmt.Errorf("unable to find pubkey for this node account. exiting... ")
	}
	pubkeyMgr.AddNodePubKey(na.PubKeySet.Secp256k1, common.SigningAlgoSecp256k1)

	if !na.PubKeySet.Ed25519.IsEmpty() {
		pubkeyMgr.AddNodePubKey(na.PubKeySet.Ed25519, common.SigningAlgoEd25519)
	}

	cfg.Signer.BlockScanner.ChainID = common.THORChain // hard code to thorchain

	// Create pubkey manager and add our private key
	thorchainBlockScanner, err := NewThorchainBlockScan(cfg.Signer.BlockScanner, storage, thorchainBridge, m, pubkeyMgr)
	if err != nil {
		return nil, fmt.Errorf("fail to create thorchain block scan: %w", err)
	}

	blockScanner, err := blockscanner.NewBlockScanner(cfg.Signer.BlockScanner, storage, m, thorchainBridge, thorchainBlockScanner)
	if err != nil {
		return nil, fmt.Errorf("fail to create block scanner: %w", err)
	}

	kg, err := tss.NewTssKeyGen(thorKeys, tssServer, thorchainBridge)
	if err != nil {
		return nil, fmt.Errorf("fail to create Tss Key gen,err:%w", err)
	}
	constantProvider := NewConstantsProvider(thorchainBridge)
	return &Signer{
		logger:                log.With().Str("module", "signer").Logger(),
		cfg:                   cfg,
		wg:                    &sync.WaitGroup{},
		stopChan:              make(chan struct{}),
		blockScanner:          blockScanner,
		thorchainBlockScanner: thorchainBlockScanner,
		chains:                chains,
		m:                     m,
		storage:               storage,
		errCounter:            m.GetCounterVec(metrics.SignerError),
		pubkeyMgr:             pubkeyMgr,
		thorchainBridge:       thorchainBridge,
		tssKeygen:             kg,
		tssServer:             tssServer,
		constantsProvider:     constantProvider,
		localPubKeyECDSA:      na.PubKeySet.Secp256k1,
		localPubKeyEDDSA:      na.PubKeySet.Ed25519,
		tssKeysignMetricMgr:   tssKeysignMetricMgr,
		observer:              obs,
	}, nil
}

func (s *Signer) getChain(chainID common.Chain) (chainclients.ChainClient, error) {
	chain, ok := s.chains[chainID]
	if !ok {
		s.logger.Debug().Str("chain", chainID.String()).Msg("is not supported yet")
		return nil, errors.New("not supported")
	}
	return chain, nil
}

// Start signer process
func (s *Signer) Start() error {
	s.wg.Add(1)
	go s.processTxnOut(s.thorchainBlockScanner.GetTxOutMessages(), 1)

	s.wg.Add(1)
	go s.processKeygen(s.thorchainBlockScanner.GetKeygenMessages())

	s.wg.Add(1)
	go s.signTransactions()

	s.blockScanner.Start(nil, nil)
	return nil
}

func (s *Signer) shouldSign(tx types.TxOutItem) bool {
	return s.pubkeyMgr.HasPubKey(tx.VaultPubKey) ||
		s.pubkeyMgr.HasPubKey(tx.VaultPubKeyEddsa) ||
		s.pubkeyMgr.HasPubKey(tx.VaultPubKeyFrost)
}

func (s *Signer) shouldAutoObserve(tx types.TxOutItem) bool {
	// Dash already reports outbounds from the mempool and once more with lock
	// finality. An automatic observation would add a third, chargeable variant.
	if tx.Chain.Equals(common.DASHChain) {
		chain, ok := s.chains[tx.Chain]
		if ok && chain.GetConfig().BlockScanner.ScanMemPool {
			return false
		}
	}

	// Monero outbounds need sign-time observation even when global auto-observe is disabled.
	// The chain scanner can later see the mined tx as an incoming change output, which is
	// insufficient to attribute the scheduled outbound on THORChain.
	if tx.Chain.Equals(common.XMRChain) {
		return true
	}

	// Memoless outbounds are intentionally broadcast without the canonical memo on-chain.
	// The signer still has OriginalMemo, so submit the observation directly instead of
	// waiting for a scanner observation that cannot reconstruct the inbound linkage.
	return s.cfg.Signer.AutoObserve || (tx.Memo == "" && tx.OriginalMemo != "")
}

// signTransactions - looks for work to do by getting a list of all unsigned
// transactions stored in the storage
func (s *Signer) signTransactions() {
	s.logger.Info().Msg("start to sign transactions")
	defer s.logger.Info().Msg("stop to sign transactions")
	defer s.wg.Done()
	for {
		select {
		case <-s.stopChan:
			return
		default:
			// When THORChain is catching up , bifrost might get stale data from thornode , thus it shall pause signing
			catchingUp, err := s.thorchainBridge.IsCatchingUp()
			if err != nil {
				s.logger.Error().Err(err).Msg("fail to get thorchain sync status")
				time.Sleep(constants.ThorchainBlockTime)
				break // this will break select
			}
			if !catchingUp {
				s.processTransactions()
			}
			time.Sleep(1 * time.Second)
		}
	}
}

func runWithContext(ctx context.Context, fn func() ([]byte, *types.TxInItem, error)) ([]byte, *types.TxInItem, error) {
	ch := make(chan error, 1)
	var checkpoint []byte
	var txIn *types.TxInItem
	go func() {
		var err error
		checkpoint, txIn, err = fn()
		ch <- err
	}()
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case err := <-ch:
		return checkpoint, txIn, err
	}
}

func (s *Signer) processTransactions() {
	signerConcurrency, err := s.thorchainBridge.GetMimir(constants.SignerConcurrency.String())
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get signer concurrency mimir")
		return
	}

	// default to 10 if unset
	if signerConcurrency <= 0 {
		signerConcurrency = 10
	}

	// if previously set to different concurrency, drain existing signings
	if s.pipeline != nil && s.pipeline.concurrency != signerConcurrency {
		s.pipeline.Wait()
		s.pipeline = nil
	}

	// if not set, or set to different concurrency, create new pipeline
	if s.pipeline == nil {
		s.pipeline, err = newPipeline(signerConcurrency)
		if err != nil {
			s.logger.Error().Err(err).Msg("fail to create new pipeline")
			return
		}
	}

	// process transactions
	s.pipeline.SpawnSignings(s, s.thorchainBridge)
}

// processTxnOut processes outbound TxOuts and save them to storage
func (s *Signer) processTxnOut(ch <-chan types.TxOut, idx int) {
	s.logger.Info().Int("idx", idx).Msg("start to process tx out")
	defer s.logger.Info().Int("idx", idx).Msg("stop to process tx out")
	defer s.wg.Done()
	for {
		select {
		case <-s.stopChan:
			return
		case txOut, more := <-ch:
			if !more {
				return
			}
			s.logger.Info().Msgf("Received a TxOut Array of %v from the Thorchain", txOut)
			if err := s.storeTxOut(txOut); err != nil {
				s.logger.Error().Err(err).Msg("fail to save tx out items to storage")
			}
		}
	}
}

// storeTxOut filters fresh impossible XMR attempts before they enter the signing
// queue. Rescheduled obligations are checked anew; nothing is marked paid on
// THORChain. Existing records, including recovery artifacts, are never replaced
// by an XMR redelivery.
func (s *Signer) storeTxOut(txOut types.TxOut) error {
	items := make([]TxOutStoreItem, 0, len(txOut.TxArray))
	var storeErrors []error
	for i, tx := range txOut.TxArray {
		item := NewTxOutStoreItem(txOut.Height, tx.TxOutItem(txOut.Height), int64(i))
		if item.TxOutItem.Chain.Equals(common.XMRChain) {
			existing, err := s.storage.Get(item.Key())
			if err != nil {
				// An unreadable record may contain recovery evidence. Leave it
				// untouched, but do not prevent the other attempts from being
				// saved. Nothing is marked paid: a missing attempt can return
				// through THORChain rescheduling or redelivery after recovery.
				storeErrors = append(storeErrors, fmt.Errorf("read XMR signer record %s: %w", item.Key(), err))
				continue
			}
			if existing.RetrievalKey != "" {
				continue
			}
			if admission, ok := s.chains[common.XMRChain].(interface {
				CanSkipUnsupportedXMRDestination(types.TxOutItem) (bool, error)
			}); ok {
				skip, checkErr := admission.CanSkipUnsupportedXMRDestination(item.TxOutItem)
				if checkErr != nil {
					s.logger.Warn().Err(checkErr).Msg("retaining XMR attempt while admission state is unknown")
				} else if skip {
					continue
				}
			}
		}
		items = append(items, item)
	}
	if len(items) > 0 {
		storeErrors = append(storeErrors, s.storage.Batch(items))
	}
	return errors.Join(storeErrors...)
}

func (s *Signer) processKeygen(ch <-chan ttypes.KeygenBlock) {
	s.logger.Info().Msg("start to process keygen")
	defer s.logger.Info().Msg("stop to process keygen")
	defer s.wg.Done()
	for {
		select {
		case <-s.stopChan:
			return
		case keygenBlock, more := <-ch:
			if !more {
				return
			}
			s.logger.Info().Interface("keygenBlock", keygenBlock).Msg("received a keygen block from thorchain")
			s.processKeygenBlock(keygenBlock)
		}
	}
}

// scheduleKeygenRetry retains unknown policy for all keygen types, including
// Asgard. This is shared retry behavior, not an XMR-only execution gate.
func (s *Signer) scheduleKeygenRetry(keygenBlock ttypes.KeygenBlock, keygenReq ttypes.Keygen) bool {
	if s.isStopped() {
		return false
	}
	retry := &keygenRetry{block: keygenBlock, keygen: keygenReq}
	id := retryID(keygenBlock, keygenReq)
	if _, loaded := s.keygenRetries.LoadOrStore(id, retry); loaded {
		return true // One waiting worker already owns this retry intent.
	}
	finished, err := s.prepareKeygenRetry(retry)
	if finished {
		s.keygenRetries.CompareAndDelete(id, retry)
		return false
	}
	if err != nil {
		s.logger.Warn().Err(err).Msg("retaining keygen retry while policy is unavailable")
	}
	targetRetryHeight := retry.targetHeight
	if !s.startKeygenWorker(func() {
		defer s.keygenRetries.CompareAndDelete(id, retry)
		ticker := time.NewTicker(constants.ThorchainBlockTime)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopChan:
				return
			case <-ticker.C:
				if retry.targetHeight == 0 {
					finished, err := s.prepareKeygenRetry(retry)
					if finished {
						return
					}
					if err != nil {
						continue
					}
				}
				if s.tryKeygenRetry(retry) {
					return
				}
			}
		}
	}) {
		s.keygenRetries.CompareAndDelete(id, retry)
		return false
	}

	s.logger.Info().
		Interface("keygenBlock", keygenBlock).
		Int64("retryHeight", targetRetryHeight).
		Msg("scheduled keygen retry")

	return true
}

func singleKeygenBlock(height int64, keygen ttypes.Keygen) ttypes.KeygenBlock {
	return ttypes.KeygenBlock{
		Height:  height,
		Keygens: []ttypes.Keygen{keygen},
	}
}

// orderedKeygensForProcessing returns a stable copy ordered by ceremony risk;
// XMR/FROST keygen enters its sidecar DKG before regular keygen backup work.
func orderedKeygensForProcessing(keygens []ttypes.Keygen) []ttypes.Keygen {
	ordered := append([]ttypes.Keygen(nil), keygens...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return keygenProcessingPriority(ordered[i].Type) < keygenProcessingPriority(ordered[j].Type)
	})
	return ordered
}

// keygenProcessingPriority ranks keygen types by how sensitive they are to
// startup delay in the current block.
func keygenProcessingPriority(keygenType ttypes.KeygenType) int {
	// A churn block can contain both regular Asgard keygen and XMR/FROST
	// monovault keygen. Run XMR first so all nodes enter the sidecar DKG
	// ceremony before regular keygen self-check signing or keyshare backup work
	// can delay one participant long enough to make the FROST ceremony timeout.
	if keygenType == ttypes.KeygenType_XMRMonovaultKeygen {
		return 0
	}
	return 1
}

func (s *Signer) processKeygenBlock(keygenBlock ttypes.KeygenBlock) {
	s.logger.Info().Interface("keygenBlock", keygenBlock).Msg("processing keygen block")

	// Process XMR/FROST keygens before regular TSS keygens so every node enters
	// the sidecar ceremony before regular keygen self-checks can delay a leader.
	for _, keygenReq := range orderedKeygensForProcessing(keygenBlock.Keygens) {
		s.processKeygenRequest(keygenBlock, keygenReq)
	}
}

// processKeygenRequest is the execution choke point for one keygen request.
// XMR ownership remains held through result construction and broadcast, while
// non-XMR requests retain the existing direct path without guard contention.
func (s *Signer) processKeygenRequest(keygenBlock ttypes.KeygenBlock, keygenReq ttypes.Keygen) {
	if keygenReq.Type != ttypes.KeygenType_XMRMonovaultKeygen {
		s.runKeygenRequest(keygenBlock, keygenReq)
		return
	}
	s.processXMRKeygenRequest(keygenBlock, keygenReq, false)
}

// processXMRKeygenRequest admits once and transfers only the admitted
// continuation when policy is unavailable. Retry context survives that wait.
func (s *Signer) processXMRKeygenRequest(keygenBlock ttypes.KeygenBlock, keygenReq ttypes.Keygen, retry bool) {
	current, disposition := s.xmrKeygen.admit(keygenBlock.Height, keygenReq, retry)
	if current == nil {
		s.logger.Warn().Int64("keygen_height", keygenBlock.Height).
			Stringer("keygen_id", keygenReq.ID).Str("disposition", disposition).
			Msg("XMR monovault keygen admission")
		return
	}
	if current = s.runAdmittedXMRKeygen(current); current != nil {
		// Transfer the admitted slot, not a new admission. Returning here leaves
		// finishAndPromote to the worker and lets the consumer process Asgard.
		if !s.startKeygenWorker(func() { s.retainXMRKeygen(current) }) {
			s.runAdmittedXMRKeygen(current) // Shutdown drains ownership without DKG.
		}
	}
}

// runKeygenRequest executes an admitted request through result broadcast.
// All returns come back to the admission loop, which releases or transfers XMR
// ownership even when execution fails or the next attempt is halted.
func (s *Signer) runKeygenRequest(keygenBlock ttypes.KeygenBlock, keygenReq ttypes.Keygen) {
	keygenChains := s.keygenChainsForType(keygenReq.Type)
	go s.warnOnChainsMissingFromKeygen(keygenChains, keygenReq.Type)
	var keygenStart time.Time
	var pubKey common.PubKeySet
	var blame []ttypes.Blame
	var frostPubKey common.PubKey
	var frostKeyshareHex string
	var observedMoneroHeight int64
	var err error
	var secp256k1Sig []byte

	switch keygenReq.Type {
	case ttypes.KeygenType_XMRMonovaultKeygen:
		keygenStart = keygenNowFn()
		pubKey.Ed25519, frostPubKey, frostKeyshareHex, observedMoneroHeight, err = s.runCustomKeygenHooks(
			keygenChains,
			keygenReq.GetMembers().Strings(),
			keygenBlock.Height,
			common.EmptyPubKey,
			common.EmptyPubKey,
		)
		if err != nil {
			s.errCounter.WithLabelValues("fail_custom_keygen_hook", "").Inc()
			s.logger.Error().Err(err).Interface("keygenBlock", keygenBlock).Msg("xmr monovault keygen hooks failed")
			if s.scheduleKeygenRetry(keygenBlock, keygenReq) {
				return
			}
			s.logger.Error().Interface("keygen", keygenReq).Msg("done with keygen retries after xmr monovault keygen failure")
			return
		}
		if pubKey.Ed25519.IsEmpty() {
			pubKey.Ed25519 = frostPubKey
		}
		if frostPubKey.IsEmpty() {
			frostPubKey = pubKey.Ed25519
		}
		pubKey.Secp256k1 = frostPubKey
	default:
		keygenStart = keygenNowFn()
		pubKey, blame, err = s.tssKeygen.GenerateNewKey(keygenBlock.Height, keygenReq.GetMembers())
		if len(blame) > 0 {
			for _, b := range blame {
				s.logger.Error().
					Str("reason", b.FailReason).
					Interface("nodes", b.BlameNodes).
					Msg("keygen blame")
			}
		}
		if err != nil {
			s.errCounter.WithLabelValues("fail_to_keygen_pubkey", "").Inc()
			s.logger.Error().Err(err).Msg("fail to generate new pubkey")
		}

		// re-enqueue the keygen block to retry if we failed to generate a key
		if pubKey.Secp256k1.IsEmpty() || pubKey.Ed25519.IsEmpty() {
			if s.scheduleKeygenRetry(keygenBlock, keygenReq) {
				return
			}
			s.logger.Error().Interface("keygen", keygenReq).Msg("done with keygen retries")
			return
		}

		// Give chain clients with custom keygen logic a chance to initialize chain-specific
		// signer state (for example Monero multisig address mapping) before keygen is finalized.
		pubKey.Ed25519, frostPubKey, frostKeyshareHex, observedMoneroHeight, err = s.runCustomKeygenHooks(
			keygenChains,
			keygenReq.GetMembers().Strings(),
			keygenBlock.Height,
			pubKey.Secp256k1,
			pubKey.Ed25519,
		)
		if err != nil {
			s.errCounter.WithLabelValues("fail_custom_keygen_hook", "").Inc()
			s.logger.Error().Err(err).Interface("keygenBlock", keygenBlock).Msg("custom keygen hooks failed")
			if s.scheduleKeygenRetry(keygenBlock, keygenReq) {
				return
			}
			s.logger.Error().Interface("keygen", keygenReq).Msg("done with keygen retries after custom keygen hook failure")
			return
		}

		// generate a verification signature to ensure we can sign with the new key
		secp256k1Sig = s.secp256k1VerificationSignature(pubKey.Secp256k1, keygenBlock.Height)
	}
	keygenTime := keygenNowFn().Sub(keygenStart).Milliseconds()

	if pubKey.Secp256k1.IsEmpty() || pubKey.Ed25519.IsEmpty() {
		if s.scheduleKeygenRetry(keygenBlock, keygenReq) {
			return
		}
		s.logger.Error().Interface("keygen", keygenReq).Msg("done with keygen retries after empty keygen result")
		return
	}

	s.flushPendingKeygenBroadcasts([]pendingKeygenBroadcast{{
		height:               keygenBlock.Height,
		poolPk:               pubKey.Secp256k1,
		secp256k1Signature:   secp256k1Sig,
		blame:                blame,
		input:                keygenReq.GetMembers(),
		keygenType:           keygenReq.Type,
		chains:               keygenChains,
		keygenTime:           keygenTime,
		poolPubKeyEddsa:      pubKey.Ed25519,
		poolPubKeyFrost:      frostPubKey,
		frostKeyshareHex:     frostKeyshareHex,
		observedMoneroHeight: observedMoneroHeight,
	}})
}

// xmrMonovaultKeygenAllowed is Bifrost's execution-side keygen gate. The
// temporary enable Mimir makes rollout opt-in; only then is the independently
// operable halt checked. It covers records already delivered to Bifrost and
// local retry re-entry without changing THORNode's scheduling behavior. Every
// unknown or unavailable enable state fails closed for execution; the error
// return lets admission retain the request instead of treating it as a halt.
func (s *Signer) xmrMonovaultKeygenAllowed(keygenHeight int64) (bool, error) {
	enabled, err := s.thorchainBridge.GetMimir(constants.MimirKeyEnableMonovaultKeygen)
	if err != nil {
		s.logger.Error().
			Err(err).
			Int64("keygen_height", keygenHeight).
			Msg("skipping XMR monovault keygen: unable to verify EnableMonovaultKeygen")
		return false, err
	}
	if enabled <= 0 {
		s.logger.Info().
			Int64("keygen_height", keygenHeight).
			Int64("enable_mimir", enabled).
			Msg("skipping XMR monovault keygen: EnableMonovaultKeygen is not active")
		return false, nil
	}

	halt, err := s.thorchainBridge.GetMimir(constants.MimirKeyHaltMonovaultKeygen)
	if err != nil {
		s.logger.Error().
			Err(err).
			Int64("keygen_height", keygenHeight).
			Msg("skipping XMR monovault keygen: unable to verify HaltMonovaultKeygen")
		return false, err
	}
	if halt <= 0 {
		return true, nil
	}

	currentHeight, err := s.keygenCurrentHeight(keygenHeight)
	if err != nil {
		s.logger.Error().
			Err(err).
			Int64("keygen_height", keygenHeight).
			Int64("halt_mimir", halt).
			Int64("current_height", currentHeight).
			Msg("skipping XMR monovault keygen: unable to verify halt activation height")
		return false, err
	}
	if halt <= currentHeight {
		s.logger.Warn().
			Int64("keygen_height", keygenHeight).
			Int64("halt_mimir", halt).
			Int64("current_height", currentHeight).
			Msg("skipping XMR monovault keygen due to HaltMonovaultKeygen")
		return false, nil
	}

	s.logger.Info().
		Int64("keygen_height", keygenHeight).
		Int64("halt_mimir", halt).
		Int64("current_height", currentHeight).
		Msg("HaltMonovaultKeygen is scheduled for a future height; allowing XMR monovault keygen")
	return true, nil
}

func (s *Signer) flushPendingKeygenBroadcasts(pending []pendingKeygenBroadcast) {
	if len(pending) == 0 {
		return
	}

	msgs := make([]sdktypes.Msg, len(pending))
	for i, item := range pending {
		// Build individual keygen messages in-process one at a time. The real
		// backup/encryption work inside buildKeygenMsg has shown signs of
		// wedging some nodes when multiple builders run concurrently.
		msg, err := s.buildKeygenMsg(item.height, item.poolPk, item.secp256k1Signature, item.blame, item.input, item.keygenType, item.chains, item.keygenTime, item.poolPubKeyEddsa, item.poolPubKeyFrost, item.frostKeyshareHex, item.observedMoneroHeight)
		if err != nil {
			s.errCounter.WithLabelValues("fail_to_broadcast_keygen", "").Inc()
			s.logger.Error().Err(err).Str("keygen_type", item.keygenType.String()).Msg("fail to build keygen")
			return
		}
		msgs[i] = msg
	}

	txID, err := s.broadcastKeygenMsgs(pending[0].height, msgs...)
	if err != nil {
		s.errCounter.WithLabelValues("fail_to_broadcast_keygen", "").Inc()
		s.logger.Error().Err(err).Msg("fail to broadcast keygen")
		return
	}

	for _, item := range pending {
		s.logger.Info().
			Stringer("txid", txID).
			Int64("block", item.height).
			Str("keygen_type", item.keygenType.String()).
			Msg("sent keygen tx to thorchain")

		if !item.poolPk.IsEmpty() {
			algo := common.SigningAlgoSecp256k1
			if item.keygenType == ttypes.KeygenType_XMRMonovaultKeygen {
				algo = common.SigningAlgoEd25519
			}
			s.pubkeyMgr.AddPubKey(item.poolPk, true, algo)
		}
		if !item.poolPubKeyEddsa.IsEmpty() {
			s.pubkeyMgr.AddPubKey(item.poolPubKeyEddsa, true, common.SigningAlgoEd25519)
		}
		for _, pk := range item.input {
			s.pubkeyMgr.AddPubKey(pk, false, common.SigningAlgoSecp256k1)
		}
	}
}

func (s *Signer) keygenChainsForType(keygenType ttypes.KeygenType) common.Chains {
	if keygenType == ttypes.KeygenType_XMRMonovaultKeygen {
		return common.Chains{common.THORChain, common.XMRChain}
	}
	chains := common.Chains{common.THORChain}
	for chain, chainCfg := range s.cfg.GetChains() {
		if chainCfg.OptToRetire || chainCfg.Disabled || chain == common.XMRChain {
			continue
		}
		chains = append(chains, chain)
	}
	return chains.Distinct()
}

// warnOnChainsMissingFromKeygen reports chains the active vaults serve that this
// bifrost is not configured for. THORNode keeps a chain in a new vault only when
// a supermajority of the keygen members report it, so a node running with a
// chain disabled can drop that chain from its vault and stall the churn on the
// init vault chain set guard. Log only - never block or alter the report.
//
// Runs off the keygen goroutine: GetAsgards goes through the retrying HTTP
// client with no overall deadline, so a stalled thornode must not delay this
// node's entry into the ceremony past the party timeout.
func (s *Signer) warnOnChainsMissingFromKeygen(keygenChains common.Chains, keygenType ttypes.KeygenType) {
	if keygenType == ttypes.KeygenType_XMRMonovaultKeygen {
		return // fixed chain set, not derived from local config
	}
	asgards, err := s.thorchainBridge.GetAsgards()
	if err != nil {
		s.logger.Debug().Err(err).Msg("fail to fetch asgards for keygen chain check")
		return
	}
	var missing common.Chains
	for _, vault := range asgards {
		for _, chain := range vault.GetChains() {
			if chain.Equals(common.XMRChain) || keygenChains.Has(chain) || missing.Has(chain) {
				continue
			}
			missing = append(missing, chain)
		}
	}
	for _, chain := range missing {
		s.logger.Error().
			Stringer("chain", chain).
			Msg("chain is served by the active vaults but disabled in this bifrost - keygen will report without it and may stall the churn")
	}
}

func (s *Signer) runCustomKeygenHooks(
	chains common.Chains,
	keys []string,
	blockHeight int64,
	poolPubKey common.PubKey,
	poolPubKeyEddsa common.PubKey,
) (common.PubKey, common.PubKey, string, int64, error) {
	var customHookErr error
	effectivePoolPubKeyEddsa := poolPubKeyEddsa
	var frostPoolPubKey common.PubKey
	var frostKeyshareHex string
	var observedMoneroHeight int64
	for chainID, chainClient := range s.chains {
		if !chains.Has(chainID) {
			continue
		}
		custom, ok := chainClient.(chainclients.CustomKeygenChainClient)
		if !ok || !custom.CustomKeygen() {
			continue
		}

		result, err := custom.Keygen(chainclienttypes.CustomKeygenRequest{
			Members:         keys,
			BlockHeight:     blockHeight,
			PoolPubKey:      poolPubKey,
			PoolPubKeyEddsa: effectivePoolPubKeyEddsa,
		})
		if err != nil {
			s.logger.Error().Err(err).Str("chain", chainID.String()).Msg("custom keygen failed")
			for _, b := range result.Blame {
				s.logger.Error().
					Str("chain", chainID.String()).
					Str("reason", b.FailReason).
					Interface("nodes", b.BlameNodes).
					Msg("custom keygen blame")
			}
			customHookErr = errors.Join(customHookErr, fmt.Errorf("custom keygen failed for %s: %w", chainID.String(), err))
			continue
		}
		if result.FrostKeyshareHex != "" {
			frostKeyshareHex = result.FrostKeyshareHex
		}
		if result.ObservedMoneroHeight > 0 {
			observedMoneroHeight = result.ObservedMoneroHeight
		}
		if !result.PoolPubKeyFrost.IsEmpty() {
			frostPoolPubKey = result.PoolPubKeyFrost
		}
		if chainID == common.XMRChain && !result.PoolPubKeyFrost.IsEmpty() {
			effectivePoolPubKeyEddsa = common.EmptyPubKey
		}
		if !result.PoolPubKeyEddsa.IsEmpty() && !result.PoolPubKeyEddsa.Equals(effectivePoolPubKeyEddsa) {
			effectivePoolPubKeyEddsa = result.PoolPubKeyEddsa
			s.logger.Info().
				Str("chain", chainID.String()).
				Str("pool_pubkey_eddsa", result.PoolPubKeyEddsa.String()).
				Str("pool_pubkey_frost", frostPoolPubKey.String()).
				Msg("custom keygen updated eddsa vault pubkey (FROST group key)")
		}
		if !result.Address.IsEmpty() {
			s.logger.Info().
				Str("chain", chainID.String()).
				Str("address", result.Address.String()).
				Msg("custom keygen produced chain address")
		}
	}
	return effectivePoolPubKeyEddsa, frostPoolPubKey, frostKeyshareHex, observedMoneroHeight, customHookErr
}

// secp256k1VerificationSignature signs the public key with its own private key as a
// sanity check that the parties can sign. Asgard keygen requires a quorum on this
// check signature before THORNode accepts the keygen. Nodes outside the signing
// party may legitimately return no signature.
func (s *Signer) secp256k1VerificationSignature(pk common.PubKey, thorchainHeight int64) []byte {
	return s.secp256k1VerificationSignatureWithTimeout(
		pk,
		thorchainHeight,
		secp256k1VerificationSignatureTimeout,
		s.secp256k1VerificationSignatureSync,
	)
}

func (s *Signer) secp256k1VerificationSignatureWithTimeout(
	pk common.PubKey,
	thorchainHeight int64,
	timeout time.Duration,
	sign func(common.PubKey, int64) []byte,
) []byte {
	type verificationResult struct {
		sig []byte
	}
	resultCh := make(chan verificationResult, 1)
	go func() {
		resultCh <- verificationResult{sig: sign(pk, thorchainHeight)}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case result := <-resultCh:
		return result.sig
	case <-timer.C:
		s.logger.Warn().
			Stringer("pubkey", pk).
			Stringer("timeout", timeout).
			Msg("timed out generating secp256k1 keygen check signature")
		return nil
	}
}

func (s *Signer) secp256k1VerificationSignatureSync(pk common.PubKey, thorchainHeight int64) []byte {
	// create keysign instance
	ks, err := tss.NewKeySign(s.tssServer, keygenCheckBridge{ThorchainBridge: s.thorchainBridge})
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to create keysign for secp256k1 check signing")
		return nil
	}
	ks.Start()
	defer ks.Stop()

	// sign the public key with its own private key
	data := []byte(pk.String())
	// The check signature proves the newly generated secp256k1 key can sign
	// before thornode has saved the new vault, so attach an empty policy and
	// let go-tss resolve the full local keygen membership.
	sigBytes, _, err := ks.RemoteSign(data, common.SigningAlgoSecp256k1, pk.String(), thorchainHeight)
	if err != nil {
		if errors.Is(err, tss.ErrNotSelectedForKeysign) {
			return nil
		}
		// this is expected in some cases if we were not in the signing party
		s.logger.Info().Err(err).Msg("fail secp256k1 check signing")
		return nil

	} else if sigBytes == nil {
		// This is expected in other cases when not in the signing party,
		// when RemoteSign's len(resp.R) and len(resp.S) are both nil.
		return nil
	}

	// build the signature
	if len(sigBytes) < 64 {
		s.logger.Error().Int("len", len(sigBytes)).Msg("unexpected sigBytes length")
		return nil
	}
	r := new(big.Int).SetBytes(sigBytes[:32])
	ss := new(big.Int).SetBytes(sigBytes[32:])
	signature := &btcec.Signature{R: r, S: ss}

	// verify the signature (thornode will also verify and reject if invalid)
	spk, err := pk.Secp256K1()
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get secp256k1 pubkey")
		return nil
	}
	if !signature.Verify(data, spk) {
		s.logger.Error().Msg("secp256k1 check signature verification failed")
	} else {
		s.logger.Info().Msg("secp256k1 check signature verified")
	}

	return sigBytes
}

func (s *Signer) buildKeygenMsg(height int64, poolPk common.PubKey, secp256k1Signature []byte, blame []ttypes.Blame, input common.PubKeys, keygenType ttypes.KeygenType, chains common.Chains, keygenTime int64, poolPubKeyEddsa common.PubKey, poolPubKeyFrost common.PubKey, frostKeyshareHex string, observedMoneroHeight int64) (sdktypes.Msg, error) {
	secpKeysharePath := ""
	eddsaKeysharePath := ""
	if s.cfg.Signer.BackupKeyshares && keygenType != ttypes.KeygenType_XMRMonovaultKeygen {
		if !poolPk.IsEmpty() {
			secpKeysharePath = filepath.Join(app.DefaultNodeHome, fmt.Sprintf("localstate-%s.json", poolPk))
		}
		if !poolPubKeyEddsa.IsEmpty() {
			eddsaKeysharePath = filepath.Join(app.DefaultNodeHome, fmt.Sprintf("localstate-%s.json", poolPubKeyEddsa))
		}
	}

	backups := s.buildKeygenBackups(secpKeysharePath, eddsaKeysharePath, frostKeyshareHex)
	keygenMsg, err := s.thorchainBridge.GetKeygenStdTx(poolPk, secp256k1Signature, backups.keyshares, blame, input, keygenType, chains, height, keygenTime, poolPubKeyEddsa, backups.keysharesEddsa, backups.keysharesFrost, poolPubKeyFrost, observedMoneroHeight)
	if err != nil {
		return nil, fmt.Errorf("fail to get keygen id: %w", err)
	}
	return keygenMsg, nil
}

func (s *Signer) sendKeygenToThorchain(height int64, poolPk common.PubKey, secp256k1Signature []byte, blame []ttypes.Blame, input common.PubKeys, keygenType ttypes.KeygenType, keygenTime int64, poolPubKeyEddsa common.PubKey, poolPubKeyFrost common.PubKey, frostKeyshareHex string, observedMoneroHeight int64) error {
	chains := s.keygenChainsForType(keygenType)
	keygenMsg, err := s.buildKeygenMsg(height, poolPk, secp256k1Signature, blame, input, keygenType, chains, keygenTime, poolPubKeyEddsa, poolPubKeyFrost, frostKeyshareHex, observedMoneroHeight)
	if err != nil {
		return err
	}

	txID, err := s.broadcastKeygenMsgs(height, keygenMsg)
	if err != nil {
		return err
	}

	s.logger.Info().
		Stringer("txid", txID).
		Int64("block", height).
		Str("keygen_type", keygenType.String()).
		Msg("sent keygen tx to thorchain")

	return nil
}

func (s *Signer) buildKeygenBackups(secpKeysharePath, eddsaKeysharePath, frostKeyshareHex string) keygenBackups {
	if !s.cfg.Signer.BackupKeyshares {
		return keygenBackups{}
	}

	type backupResult struct {
		kind string
		data []byte
		err  error
		path string
	}

	results := make(chan backupResult, 3)
	seedPhrase, seedErr := common.GetSignerSeedPhrase()
	if seedErr != nil {
		s.logger.Error().Err(seedErr).Msg("fail to get signer seed phrase")
		return keygenBackups{}
	}
	// The seed encrypts the keyshare disaster-recovery backup, and EncryptKeyshares
	// requires a 24-word (256-bit) seed. A shorter — but still valid BIP39 — seed is
	// accepted at startup so the node can run and sign, but it cannot produce a DR
	// backup. Surface that loudly on every keygen instead of failing each encrypt
	// attempt with an opaque "not 24 words" error.
	if wordCount := len(strings.Fields(seedPhrase)); wordCount < 24 {
		s.logger.Error().Int("seed_words", wordCount).Msgf(
			"signer seed is %d words: NO keyshare disaster-recovery backup written — regenerate as a 24-word seed to enable DR", wordCount)
		return keygenBackups{}
	}
	expected := 0

	if secpKeysharePath != "" {
		expected++
		path := secpKeysharePath
		go func() {
			data, err := encryptKeysharesFn(path, seedPhrase)
			results <- backupResult{kind: "secp256k1", data: data, err: err, path: path}
		}()
	}

	if eddsaKeysharePath != "" {
		if _, statErr := os.Stat(eddsaKeysharePath); statErr == nil {
			expected++
			path := eddsaKeysharePath
			go func() {
				data, err := encryptKeysharesFn(path, seedPhrase)
				results <- backupResult{kind: "eddsa", data: data, err: err, path: path}
			}()
		} else if !errors.Is(statErr, os.ErrNotExist) {
			s.logger.Error().Err(statErr).Str("path", eddsaKeysharePath).Msg("fail to stat eddsa keyshares")
		}
	}

	if frostKeyshareHex != "" {
		rawKey, decodeErr := hex.DecodeString(frostKeyshareHex)
		if decodeErr != nil {
			s.logger.Error().Err(decodeErr).Msg("fail to decode frost keyshare hex")
		} else {
			expected++
			go func(raw []byte) {
				data, err := encryptRawKeysharesFn(raw, seedPhrase)
				results <- backupResult{kind: "frost", data: data, err: err}
			}(rawKey)
		}
	}

	var backups keygenBackups
	for i := 0; i < expected; i++ {
		result := <-results
		if result.err != nil {
			switch result.kind {
			case "secp256k1":
				s.logger.Error().Err(result.err).Str("path", result.path).Msg("fail to encrypt secp256k1 keyshares")
			case "eddsa":
				s.logger.Error().Err(result.err).Str("path", result.path).Msg("fail to encrypt eddsa keyshares")
			case "frost":
				s.logger.Error().Err(result.err).Msg("fail to encrypt frost keyshare")
			}
			continue
		}
		switch result.kind {
		case "secp256k1":
			backups.keyshares = result.data
		case "eddsa":
			backups.keysharesEddsa = result.data
		case "frost":
			backups.keysharesFrost = result.data
		}
	}

	return backups
}

func (s *Signer) broadcastKeygenMsgs(height int64, msgs ...sdktypes.Msg) (common.TxID, error) {
	if len(msgs) == 0 {
		return common.BlankTxID, nil
	}
	strHeight := strconv.FormatInt(height, 10)
	var txID common.TxID

	bf := backoff.NewExponentialBackOff()
	bf.MaxElapsedTime = time.Minute
	err := backoff.Retry(func() error {
		var err error
		txID, err = s.thorchainBridge.Broadcast(msgs...)
		if err != nil {
			s.logger.Warn().Err(err).Msg("fail to send keygen tx to thorchain")
			s.errCounter.WithLabelValues("fail_to_send_to_thorchain", strHeight).Inc()
			return fmt.Errorf("fail to send the tx to thorchain: %w", err)
		}
		return nil
	}, bf)
	return txID, err
}

// signAndBroadcast will sign the tx and broadcast it to the corresponding chain. On
// SignTx error for the chain client, if we receive checkpoint bytes we also return them
// with the error so they can be set on the TxOutStoreItem and re-used on a subsequent
// retry to avoid double spend. The second returned value is an optional observation
// that should be submitted to THORChain.
func (s *Signer) signAndBroadcast(item TxOutStoreItem) ([]byte, *types.TxInItem, error) {
	height := item.Height
	tx := item.TxOutItem

	// Block legacy TEX outbounds before signing or rebroadcasting saved bytes.
	if tx.Chain.Equals(common.ZECChain) && strings.HasPrefix(tx.ToAddress.String(), "tex") {
		s.logger.Warn().Stringer("to_address", tx.ToAddress).Str("memo", tx.Memo).
			Msg("skipping ZEC TEX outbound; requires manual reconciliation")
		return nil, nil, nil
	}

	// set the checkpoint on the tx out item if it was stored
	if item.Checkpoint != nil {
		tx.Checkpoint = item.Checkpoint
	}

	blockHeight, err := s.thorchainBridge.GetBlockHeight()
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to get block height")
		return nil, nil, err
	}
	signingTransactionPeriod, err := s.constantsProvider.GetInt64Value(blockHeight, constants.SigningTransactionPeriod)
	s.logger.Debug().Msgf("signing transaction period:%d", signingTransactionPeriod)
	if err != nil {
		s.logger.Error().Err(err).Msgf("fail to get constant value for(%s)", constants.SigningTransactionPeriod)
		return nil, nil, err
	}

	// if in round 7 retry, discard outbound if over the max outbound attempts
	inactiveVaultRound7Retry := false
	if item.Round7Retry {
		mimirKey := "MAXOUTBOUNDATTEMPTS"
		var maxOutboundAttemptsMimir int64
		maxOutboundAttemptsMimir, err = s.thorchainBridge.GetMimir(mimirKey)
		if err != nil {
			s.logger.Err(err).Msgf("fail to get %s", mimirKey)
			return nil, nil, err
		}
		attempt := (blockHeight - height) / signingTransactionPeriod
		if attempt > maxOutboundAttemptsMimir {
			s.logger.Warn().
				Int64("outbound_height", height).
				Int64("current_height", blockHeight).
				Int64("attempt", attempt).
				Msg("round 7 retry outbound tx has reached max outbound attempts")
			return nil, nil, nil
		}

		// determine if the round 7 retry is for an inactive vault
		var vault ttypes.Vault
		vault, err = s.thorchainBridge.GetVault(item.TxOutItem.VaultPubKey.String())
		if err != nil {
			log.Err(err).
				Stringer("vault_pubkey", item.TxOutItem.VaultPubKey).
				Msg("failed to get tx out item vault")
			return nil, nil, err
		}
		inactiveVaultRound7Retry = vault.Status == ttypes.VaultStatus_InactiveVault
	}

	// Stale items must stop here so an old transaction cannot be broadcast after
	// THORChain has rescheduled the outbound. XMR is the only exception: its
	// vault-independent durable claim prevents a replacement signing ceremony,
	// so a persisted payload can safely rebroadcast the exact same transaction.
	hasSignedRetry := len(item.SignedTx) > 0
	allowStaleSignedRetry := hasSignedRetry && tx.Chain.Equals(common.XMRChain)
	signingWindow := signingTransactionPeriod
	if tx.Chain.Equals(common.XMRChain) {
		// Match THORChain's XMR outbound lifetime. Scanner catch-up and a failed
		// FROST join can consume the base period before all participants retry.
		// Keep the reschedule buffer and completed-request check below intact.
		signingWindow = ttypes.MaxTxOutSigningTransactionPeriod(signingTransactionPeriod)
	}
	if !allowStaleSignedRetry && (!item.Round7Retry || inactiveVaultRound7Retry) {
		if blockHeight-signingWindow > height-s.cfg.Signer.RescheduleBufferBlocks {
			s.logger.Error().Msgf("tx was created at block height(%d), now it is (%d), it is older than (%d) blocks, skip it", height, blockHeight, signingWindow)
			return nil, nil, nil
		}
	}

	chain, err := s.getChain(tx.Chain)
	if err != nil {
		s.logger.Error().Err(err).Msgf("not supported %s", tx.Chain.String())
		return nil, nil, err
	}
	halted, global, err := signing.CheckSigningHalt(s.thorchainBridge, tx.Chain, blockHeight)
	if err != nil {
		s.logger.Err(err).Msg("fail to check signing halt")
		return nil, nil, err
	}
	if halted {
		if global {
			s.logger.Info().Msg("signing has been halted globally")
		} else {
			s.logger.Info().Msgf("signing for %s is halted", tx.Chain)
		}
		return nil, nil, nil
	}
	if !s.shouldSign(tx) {
		s.logger.Info().Str("signer_address", chain.GetAddress(tx.VaultPubKey)).Msg("different pool address, ignore")
		return nil, nil, nil
	}

	if len(tx.ToAddress) == 0 {
		s.logger.Info().Msg("To address is empty, THORNode don't know where to send the fund , ignore")
		return nil, nil, nil // return nil and discard item
	}

	// don't sign if the block scanner is unhealthy. This is because the
	// network may not be able to detect the outbound transaction, and
	// therefore reschedule the transaction to another signer. In a disaster
	// scenario, the network could broadcast a transaction several times,
	// bleeding funds.
	if !chain.IsBlockScannerHealthy() {
		return nil, nil, fmt.Errorf("the block scanner for chain %s is unhealthy, not signing transactions due to it", chain.GetChain())
	}

	start := time.Now()
	defer func() {
		if h := s.m.GetHistograms(metrics.SignAndBroadcastDuration(chain.GetChain())); h != nil {
			h.Observe(time.Since(start).Seconds())
		}
	}()

	if !tx.OutHash.IsEmpty() {
		s.logger.Info().Str("OutHash", tx.OutHash.String()).Msg("tx had been sent out before")
		return nil, nil, nil // return nil and discard item
	}

	// We get the keysign object from thorchain again to ensure it hasn't
	// been signed already, and we can skip. This helps us not get stuck on
	// a task that we'll never sign, because 2/3rds already has and will
	// never be available to sign again.
	txOut, err := s.thorchainBridge.GetKeysign(height, tx.VaultPubKey.String())
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get keysign items")
		return nil, nil, err
	}
	for _, txArray := range txOut.TxArray {
		latestTx := txArray.TxOutItem(item.TxOutItem.Height)
		if !txOutItemEqualsIgnoringGas(latestTx, tx) {
			continue
		}
		if !latestTx.OutHash.IsEmpty() {
			// already been signed, we can skip it
			s.logger.Info().Str("tx_id", latestTx.OutHash.String()).Msgf("already signed. skipping...")
			return nil, nil, nil
		}
		// Signed bytes, a checkpoint, or a terminal-round marker all cross the
		// transaction-immutability boundary. Gas may be part of the chain tx hash,
		// so only a wholly unexposed attempt may adopt refreshed gas.
		if len(item.SignedTx) == 0 && len(item.Checkpoint) == 0 && !item.Round7Retry && txOutItemGasChanged(latestTx, tx) {
			s.logger.Info().
				Str("chain", tx.Chain.String()).
				Str("memo", tx.Memo).
				Int64("old_gas_rate", tx.GasRate).
				Int64("new_gas_rate", latestTx.GasRate).
				Msg("refreshing signer txout gas from latest keysign")
			tx.MaxGas = latestTx.MaxGas
			tx.GasRate = latestTx.GasRate
			item.TxOutItem.MaxGas = latestTx.MaxGas
			item.TxOutItem.GasRate = latestTx.GasRate
			if s.storage != nil {
				if storeErr := s.storage.Set(item); storeErr != nil {
					s.logger.Error().Err(storeErr).Msg("fail to persist refreshed txout gas")
					return nil, nil, fmt.Errorf("persist refreshed txout gas before signing: %w", storeErr)
				}
			}
		}
		break
	}

	// If the chain client exposes a vault lock, hold it for sign+broadcast so
	// chain-specific consolidation routines cannot race normal outbounds.
	lockKey := chainVaultLockKey(chain, tx)
	if lock := chainVaultLock(chain, lockKey); lock != nil {
		lock.Lock()
		defer lock.Unlock()
	}

	// If SignedTx is set, we already signed and should only retry broadcast.
	var signedTx, checkpoint []byte
	var elapse time.Duration
	var observation *types.TxInItem
	if hasSignedRetry {
		s.logger.Info().Str("memo", tx.Memo).Msg("retrying broadcast of already signed tx")
		signedTx = item.SignedTx
		observation = item.Observation
	} else {
		tx.SigningID = item.Key()
		startKeySign := time.Now()
		signedTx, checkpoint, observation, err = chain.SignTx(tx, height)
		if err != nil {
			if !errors.Is(err, chainclienttypes.ErrUnsupportedXMRDestination) {
				s.logger.Error().Err(err).Msg("fail to sign tx")
			}
			return checkpoint, nil, err
		}
		elapse = time.Since(startKeySign)

		// Store the signed payload before broadcast so a restart can retry the
		// same transaction. Persist the chain-provided observation with it:
		// most chains return nil, while XMR uses the observation to carry spent
		// refs that must survive until import/observation is durable.
		item.SignedTx = signedTx
		item.Observation = observation
		if storeErr := s.storage.Set(item); storeErr != nil {
			s.logger.Error().Err(storeErr).Msg("fail to persist signed tx before broadcast")
			// Never broadcast an unpersisted signature, but hand the signed result
			// back so processTransaction retries the write before another attempt
			// can re-sign (and possibly pick a different nonce).
			return checkpoint, observation, &unsavedSignedTxError{item: item, err: storeErr}
		}
	}

	// looks like the transaction is already signed
	if len(signedTx) == 0 {
		s.logger.Warn().Msgf("signed transaction is empty")
		return nil, nil, nil
	}

	// broadcast the transaction
	hash, err := chain.BroadcastTx(tx, signedTx)
	if err != nil {
		s.logger.Error().Err(err).Str("memo", tx.Memo).Msg("fail to broadcast tx to chain")

		// A chain client returns ErrResignRequired when the signed bytes can never
		// land (e.g. a SOL tx whose blockhash expired mid-keysign): re-broadcasting
		// the same bytes loops forever. Clear the stored signed tx + checkpoint so
		// the next pass re-enters SignTx, where the chain's sign-time validity gate
		// re-evaluates (for SOL, dropping the item for a deterministic fresh-height
		// reschedule). Every other broadcast error keeps the signed tx for a plain
		// broadcast retry, since re-signing wastes a keysign and risks a double
		// broadcast when the tx may still land.
		if errors.Is(err, types.ErrResignRequired) {
			item.SignedTx = nil
			item.Checkpoint = nil
			if storeErr := s.storage.Set(item); storeErr != nil {
				s.logger.Error().Err(storeErr).Msg("fail to clear signed tx for re-sign")
			}
			return nil, observation, err
		}

		// store the signed tx for the next retry
		item.SignedTx = signedTx
		item.Observation = observation
		if storeErr := s.storage.Set(item); storeErr != nil {
			s.logger.Error().Err(storeErr).Msg("fail to update tx out store item with signed tx")
		}

		return nil, observation, err
	}
	s.logger.Info().
		Str("chain", chain.GetChain().String()).
		Str("txid", hash).
		Str("memo", tx.Memo).
		Msg("broadcasted tx to chain")

	if s.isTssKeysign(tx.VaultPubKey) || s.isTssKeysign(tx.VaultPubKeyEddsa) {
		s.tssKeysignMetricMgr.SetTssKeysignMetric(hash, elapse.Milliseconds())
	}

	return nil, observation, nil
}

// unsavedSignedTxError reports a signed payload whose pre-broadcast write failed.
// It carries the complete item that should have been written (signed payload,
// observation and any gas refreshed before signing) so the retry write cannot
// pair the signature with the caller's stale copy.
type unsavedSignedTxError struct {
	item TxOutStoreItem
	err  error
}

func (e *unsavedSignedTxError) Error() string {
	return "persist signed tx before broadcast: " + e.err.Error()
}

func (e *unsavedSignedTxError) Unwrap() error { return e.err }

func (s *Signer) isTssKeysign(pubKey common.PubKey) bool {
	return !s.localPubKeyECDSA.Equals(pubKey) && !s.localPubKeyEDDSA.Equals(pubKey)
}

// Stop the signer process
func (s *Signer) Stop() error {
	s.logger.Info().Msg("receive request to stop signer")
	defer s.logger.Info().Msg("signer stopped successfully")
	s.keygenWorkerMu.Lock()
	close(s.stopChan)
	s.keygenWorkerMu.Unlock()
	s.wg.Wait()
	// Retry execution must finish before its resources close, just like the
	// normal consumer. This is not bounded by one keygen timeout: phase timers,
	// sidecar calls and result waits are separate until active DKG cancellation
	// is implemented.
	s.keygenWorkerWG.Wait()
	if err := s.m.Stop(); err != nil {
		s.logger.Error().Err(err).Msg("fail to stop metric server")
	}
	s.blockScanner.Stop()
	return s.storage.Close()
}

////////////////////////////////////////////////////////////////////////////////////////
// pipelineSigner Interface
////////////////////////////////////////////////////////////////////////////////////////

func (s *Signer) isStopped() bool {
	select {
	case <-s.stopChan:
		return true
	default:
		return false
	}
}

func (s *Signer) storageList() []TxOutStoreItem {
	items := s.storage.List()
	if s.observer != nil && s.observer.IsChainClientStarted(common.XMRChain) {
		return items
	}
	// Filter only the materialized list. Deferred XMR, including signed retries,
	// stays durable without consuming signing attempts or expiring in the pipeline.
	return slices.DeleteFunc(items, func(item TxOutStoreItem) bool {
		return item.TxOutItem.Chain.Equals(common.XMRChain)
	})
}

func (s *Signer) processTransaction(item TxOutStoreItem) {
	s.logger.Info().
		Str("chain", item.TxOutItem.Chain.String()).
		Int64("height", item.Height).
		Int("status", int(item.Status)).
		Interface("tx", item.TxOutItem).
		Msg("Signing transaction")

	ctx, cancel := context.WithTimeout(context.Background(), signingTimeoutForChain(item.TxOutItem.Chain))
	checkpoint, obs, err := runWithContext(ctx, func() ([]byte, *types.TxInItem, error) {
		return s.signAndBroadcast(item)
	})

	if errors.Is(err, chainclienttypes.ErrTxExpired) {
		// Expiry is terminal for this attempt, including saved payloads and
		// terminal-round retries. Never auto-observe it as a successful outbound.
		s.logger.Warn().Err(err).Interface("tx", item.TxOutItem).
			Msg("retiring expired transaction; awaiting THORChain rescheduling")
		if removeErr := s.storage.Remove(item); removeErr != nil {
			s.logger.Error().Err(removeErr).Msg("fail to remove expired tx out store item")
		}
		cancel()
		return
	}

	// Apply spent output refs from the signing observation before handling
	// errors. The observation is populated during FROST signing and contains
	// the real inputs consumed. Applying immediately ensures the sidecar
	// marks outputs spent even when broadcast fails (another signer may have
	// broadcast the same tx successfully).
	// If the chain client can report import durability, keep the txout item
	// until refs are applied so a restart can retry the same signed payload.
	// Fall back to the stored observation from a prior signing attempt.
	obsForRefs := obs
	if obsForRefs == nil {
		obsForRefs = item.Observation
	}
	if !s.applySpentOutputRefsFromObs(ctx, item.TxOutItem, obsForRefs) {
		s.logger.Warn().
			Interface("tx", item.TxOutItem).
			Msg("xmr spent output refs not durable; keeping tx out store item for retry")
		cancel()
		return
	}

	if err != nil {
		if errors.Is(err, chainclienttypes.ErrUnsupportedXMRDestination) && item.TxOutItem.Chain.Equals(common.XMRChain) &&
			!item.Round7Retry && len(item.SignedTx) == 0 && len(item.Checkpoint) == 0 && len(item.TxOutItem.Checkpoint) == 0 &&
			item.Observation == nil && len(checkpoint) == 0 && obs == nil {
			// Retire only this local unsigned attempt, never the on-chain
			// obligation. A delete failure leaves it retryable; THORChain can
			// also supply the same unsupported destination in a later schedule.
			if removeErr := s.storage.Remove(item); removeErr != nil {
				s.logger.Error().Err(removeErr).Str("item", item.Key()).
					Msg("fail to retire unsupported unsigned XMR destination")
			} else {
				s.logger.Warn().Str("chain", item.TxOutItem.Chain.String()).Str("item", item.Key()).
					Stringer("destination", item.TxOutItem.ToAddress).
					Msg("retired local unsigned attempt for unsupported destination; on-chain obligation remains pending")
			}
			cancel()
			return
		}
		// Always store the checkpoint of the built tx if we have one. This ensures the same
		// vault will never attempt multiple txs for an outbound, as further protection
		// against cases like https://gitlab.com/thorchain/thornode/-/merge_requests/4344.
		// In tradeoff, we accept a higher likelihood of temporary stuck outbounds.
		ksErr := tss.KeysignError{}
		// Terminal-round failure = the keysign crossed a signature-exposure
		// boundary (secp256k1 KEYSIGN9 under tss-lib v3, EDDSAKEYSIGN3 under
		// ed25519, or FROST frost_share/frost_result). FROST preprocess remains
		// non-terminal because no signature share has been broadcast yet.
		// Round7Retry persists on the item — its name is the legacy v1 label and
		// is kept stable for on-disk schema compatibility.
		isTerminalRound := errors.As(err, &ksErr) && ksErr.IsTerminalRound()
		// A signature whose pre-broadcast write failed already exists; retry that
		// write so the next pass only rebroadcasts these bytes instead of re-signing.
		var unsaved *unsavedSignedTxError
		hasUnsaved := errors.As(err, &unsaved)
		if hasUnsaved {
			item = unsaved.item
			if checkpoint == nil {
				checkpoint = item.Checkpoint
			}
		}
		if checkpoint != nil || isTerminalRound || hasUnsaved {
			item.Checkpoint = checkpoint
			if isTerminalRound {
				s.logger.Error().Err(err).Interface("tx", item.TxOutItem).Msg("terminal-round signing error")
				item.Round7Retry = true
			}
			if storeErr := s.storage.Set(item); storeErr != nil {
				s.logger.Error().Err(storeErr).Msg("fail to persist checkpoint after sign failure")
			}
		}

		s.logger.Error().Interface("tx", item.TxOutItem).Err(err).Msg("fail to sign and broadcast tx out store item")
		cancel()
		return
		// The 'item' for loop should not be items[0],
		// because problems which return 'nil, nil' should be skipped over instead of blocking others.
		// When signAndBroadcast returns an error (such as from a keysign timeout),
		// a 'return' and not a 'continue' should be used so that nodes can all restart the list,
		// for when the keysign failure was from a loss of list synchrony.
		// Otherwise, out-of-sync lists would cycle one timeout at a time, maybe never resynchronising.
	}
	cancel()

	// if enabled and the observation is non-nil, instant observe the outbound
	if s.shouldAutoObserve(item.TxOutItem) && obs != nil {
		s.observer.ObserveSigned(types.TxIn{
			Chain:                  item.TxOutItem.Chain,
			TxArray:                []*types.TxInItem{obs},
			MemPool:                true,
			Filtered:               true,
			ConfirmationRequired:   0,
			AllowFutureObservation: true,
		})
	}

	// We have a successful broadcast! Remove the item from our store
	if err = s.storage.Remove(item); err != nil {
		s.logger.Error().Err(err).Msg("fail to update tx out store item")
	}
}

func signingTimeoutForChain(chain common.Chain) time.Duration {
	if chain.Equals(common.XMRChain) {
		return 15 * time.Minute
	}
	// a single non-XMR keysign should not take longer than 5 minutes, regardless TSS or local
	return 5 * time.Minute
}

func (s *Signer) applySpentOutputRefsFromObs(ctx context.Context, tx types.TxOutItem, obs *types.TxInItem) bool {
	if obs == nil || len(obs.SpentOutputRefs) == 0 {
		return true
	}
	chain, ok := s.chains[tx.Chain]
	if !ok {
		return !tx.Chain.Equals(common.XMRChain)
	}
	obsTx := common.NewObservedTx(
		common.NewTx(
			common.TxID(obs.Tx),
			common.Address(obs.Sender),
			common.Address(obs.To),
			obs.Coins,
			obs.Gas,
			obs.Memo,
		),
		obs.BlockHeight,
		obs.ObservedVaultPubKey,
		obs.BlockHeight,
	)
	obsTx.SpentOutputRefs = common.OutputRefsFromPointers(obs.SpentOutputRefs)
	obsTx.XmrTxKey = obs.TxKey
	s.logger.Info().
		Str("tx_hash", obs.Tx).
		Int("refs", len(obs.SpentOutputRefs)).
		Msg("applying spent output refs from signing observation")
	outcome := make(chan bool, 1)
	onOutcome := func(applied bool) {
		// The contract is exactly-once, but keep a misbehaving implementation
		// from blocking its retry goroutine after the signer has timed out.
		select {
		case outcome <- applied:
		default:
		}
	}
	applier, ok := chain.(chainclienttypes.SpentOutputRefsApplier)
	if !ok {
		// Exact spent refs and wire-memo authentication are XMR signing
		// invariants. Never discard an XMR TxOut without the production hook.
		return !tx.Chain.Equals(common.XMRChain)
	}
	applier.ApplySpentOutputRefsWithOutcome(chainclienttypes.SpentOutputRefsImport{
		Tx:       obsTx,
		WireMemo: types.XMRWireMemo(tx.Memo, tx.OriginalMemo),
	}, onOutcome)
	select {
	case applied := <-outcome:
		return applied
	case <-ctx.Done():
		return false
	case <-s.stopChan:
		return false
	}
}
