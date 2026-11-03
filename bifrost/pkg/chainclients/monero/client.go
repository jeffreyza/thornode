package monero

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blang/semver"
	"github.com/btcsuite/btcutil/bech32"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	bifrosttss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	gotssblame "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	gotsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	maxAsgardAddresses = 100

	mimirXMRConsolidationEnabled             = "XMRConsolidationEnabled"
	mimirXMRConsolidationCheckIntervalBlocks = "XMRConsolidationCheckIntervalBlocks"
	mimirXMRConsolidationCooldownBlocks      = "XMRConsolidationCooldownBlocks"
	mimirXMRConsolidationMinUnlockedOutputs  = "XMRConsolidationMinUnlockedOutputs"
	mimirXMRConsolidationTargetOutputs       = "XMRConsolidationTargetOutputs"
	mimirXMRConsolidationMinUnlockedAtomic   = "XMRConsolidationMinUnlockedAtomic"
	mimirXMRConsolidationReserveBps          = "XMRConsolidationReserveBps"
	mimirXMRConsolidationChurnLookahead      = "XMRConsolidationChurnLookaheadBlocks"
	mimirXMRConsolidationMaxRetries          = "XMRConsolidationMaxRetries"
	mimirXMRConsolidationMaxInputs           = "XMRConsolidationMaxInputs"
	mimirXMRConsolidationMaxLockBps          = "XMRConsolidationMaxLockBps"

	defaultXMRConsolidationCheckIntervalBlocks = int64(10)
	defaultXMRConsolidationCooldownBlocks      = int64(30)
	defaultXMRConsolidationMinUnlockedOutputs  = int64(40)
	defaultXMRConsolidationTargetOutputs       = int64(15)
	defaultXMRConsolidationMinUnlockedAtomic   = uint64(1_000_000_000_000) // 1 XMR (piconero)
	defaultXMRConsolidationReserveBps          = int64(50)                 // 0.50%
	defaultXMRConsolidationChurnLookahead      = int64(720)
	defaultXMRConsolidationMaxRetries          = int64(3)
	defaultXMRConsolidationMaxInputs           = int64(8)
	defaultXMRConsolidationMaxLockBps          = int64(1000)        // 10.00%
	defaultXMRConsolidationMinReserveAtomic    = uint64(50_000_000) // 0.00005 XMR (piconero)
	defaultXMRSpentRefsRetryMaxAttempts        = 60                 // bounded peer-import retry window after the first failed attempt
	defaultXMRCommittedRefsImportMaxAttempts   = 3                  // cover the DeliverTx-to-commit race without replacing periodic catch-up
	defaultXMRRecoveryRetryMaxAttempts         = 0                  // zero means retry for the process lifetime
	maxXMRRecoveryFileBytes                    = int64(1024 * 1024) // encrypted keyshare envelopes are expected to be far smaller
	xmrSpendableConfirmations                  = uint64(10)

	// xmrSolvencyDeferralEscalationRounds is the number of consecutive deferred
	// solvency rounds (one per SolvencyBlocks interval) after which the deferral
	// log escalates from Warn to Error. The sidecar's verified expiry should
	// resolve a dead spend well within this window.
	xmrSolvencyDeferralEscalationRounds = 6
)

var (
	ErrSidecarNotConfigured   = errors.New("xmr sidecar requires signer_endpoint or api_host configuration")
	errXMRBalanceUnreconciled = errors.New("xmr sidecar balance is not reconciled")
)

type signedTxPayload struct {
	TxHash           string                `json:"tx_hash"`
	TxMetadata       string                `json:"tx_metadata"`
	TxKey            string                `json:"tx_key"`
	FeeAtomic        uint64                `json:"fee_atomic"`
	MsgID            string                `json:"msg_id,omitempty"`
	Eventuality      tssp.FrostEventuality `json:"eventuality,omitempty"`
	Broadcasted      bool                  `json:"broadcasted,omitempty"`
	SpentOutputRefs  []sidecar.SpentRef    `json:"spent_output_refs,omitempty"`
	Observation      *stypes.TxInItem      `json:"observation,omitempty"`
	ClaimCacheVault  string                `json:"claim_cache_vault,omitempty"`
	ClaimCacheHash   string                `json:"claim_cache_hash,omitempty"`
	ClaimLogicalHash string                `json:"claim_logical_hash,omitempty"`
	ClaimOwnerVault  string                `json:"claim_owner_vault,omitempty"`
}

type signCheckpoint struct {
	SpentOutputRefs []sidecar.SpentRef `json:"spent_output_refs"`
}

// durableXMRClaimTxOut preserves fields intentionally omitted by TxOutItem's
// normal JSON representation. SigningID is part of consolidation identity and
// Checkpoint binds an exact-input retry, so recovery must never substitute a
// later rescheduled item for either field.
type durableXMRClaimTxOut struct {
	TxOut      stypes.TxOutItem `json:"tx_out"`
	SigningID  string           `json:"signing_id"`
	Checkpoint []byte           `json:"checkpoint,omitempty"`
}

func encodeDurableXMRClaimTxOut(tx stypes.TxOutItem) ([]byte, error) {
	return json.Marshal(durableXMRClaimTxOut{
		TxOut:      tx,
		SigningID:  tx.SigningID,
		Checkpoint: append([]byte(nil), tx.Checkpoint...),
	})
}

type validatedSignRequest struct {
	vaultPubKey  common.PubKey
	fromAddress  string
	toAddress    string
	amountAtomic uint64
	maxGasAtomic uint64
}

type consolidationSettings struct {
	Enabled             bool
	CheckIntervalBlocks int64
	CooldownBlocks      int64
	MinUnlockedOutputs  int64
	TargetOutputs       int64
	MinUnlockedAtomic   uint64
	ReserveBps          int64
	ChurnLookahead      int64
	MaxRetries          int64
	MaxInputs           int64
	MaxLockBps          int64
}

type consolidationVaultState struct {
	LastAttemptHeight int64
	LastSuccessHeight int64
	LastTxHash        string
	ConsecutiveFails  int64
}

// Client implements the chain client interface for Monero.
type Client struct {
	logger zerolog.Logger
	cfg    config.BifrostChainConfiguration
	chain  common.Chain

	thorchainBridge       thorclient.ThorchainBridge
	localNodePubKey       common.PubKey
	vaultBootstrap        *vaultBootstrap
	storage               *blockscanner.BlockScannerStorage
	blockScanner          *blockscanner.BlockScanner
	scanner               *Scanner
	sidecar               *sidecar.Client
	tssServer             tssp.Server
	signerCacheManager    *signercache.CacheManager
	signedTxPayloadStore  *signedTxPayloadStore
	vaultAddressStore     *VaultAddressStore
	vaultAddressMap       map[string]common.Address
	vaultAddrToKey        map[string]string
	vaultAddressMu        sync.RWMutex
	globalSolvencyQueue   chan stypes.Solvency
	globalNetworkFeeQueue chan common.NetworkFee

	asgardMu                sync.RWMutex
	asgardAddresses         []common.Address
	lastAsgard              time.Time
	consolidationInitOnce   sync.Once
	consolidationSettingsMu sync.RWMutex
	consolidationSettings   consolidationSettings
	lastConsolidationConfig time.Time
	hasConsolidationConfig  bool
	vaultLockMu             sync.Mutex
	vaultLocks              map[string]*sync.Mutex
	emptyVaultLock          sync.Mutex
	sidecarSignMu           sync.Mutex
	consolidationStateMu    sync.Mutex
	consolidationState      map[string]consolidationVaultState
	consolidateInProgress   *atomic.Bool
	lastConsolidationHeight *atomic.Int64
	lastSolvencyCheckHeight *atomic.Int64
	wg                      *sync.WaitGroup
	stopchan                chan struct{}
	lifecycleMu             sync.Mutex

	solvencyDeferralMu     sync.Mutex
	solvencyDeferralRounds map[string]int

	spentRefsRetryInterval    time.Duration
	spentRefsRetryMaxAttempts int
	spentRefsGuardMu          sync.RWMutex
	spentRefsGuard            types.SpentOutputRefsGuard

	recoveryRetryInterval    time.Duration
	recoveryRetryMaxAttempts int
}

// Keep the production Monero client and the signer durability contract from
// drifting independently. A missing final-outcome hook must be a build error,
// not a runtime assertion miss which lets the signer discard the outbound.
var _ types.SpentOutputRefsApplier = (*Client)(nil)

// validateMoneroDaemonNetwork binds THORChain mainnet builds to Monero mainnet.
// Other deployments may choose a different daemon network; the sidecar
// compatibility check validates its supported value and reported identity.
func validateMoneroDaemonNetwork(thorNetwork common.ChainNetwork, chainNetwork string) error {
	network := strings.ToLower(strings.TrimSpace(chainNetwork))
	if network == "" {
		return errors.New("xmr chain_network is required for daemon identity verification")
	}
	if thorNetwork == common.MainNet && network != "mainnet" {
		return fmt.Errorf("xmr chain_network must be mainnet for a THORChain mainnet build, got %q", network)
	}
	return nil
}

// NewClient creates a monero chain client backed by the shared block scanner.
func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	tssServer *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
) (types.ChainClient, error) {
	if !cfg.ChainID.Equals(common.XMRChain) {
		return nil, fmt.Errorf("monero client requires chain id %s, got %s", common.XMRChain, cfg.ChainID)
	}
	// Avoid starting a polling loop for XMR's disabled mempool scanner.
	cfg.BlockScanner.ScanMemPool = false
	if cfg.BlockScanner.PrefetchBlocks > 1 {
		return nil, fmt.Errorf("xmr block prefetch must be 0 or 1 to preserve scanner height order")
	}
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	if m == nil {
		return nil, errors.New("metrics instance is nil")
	}
	if tssServer == nil {
		return nil, errors.New("tss server is nil")
	}
	if err := validateMoneroDaemonNetwork(common.CurrentChainNetwork, cfg.ChainNetwork); err != nil {
		return nil, err
	}
	logger := log.With().Str("module", cfg.ChainID.String()).Logger()
	sc, err := sidecar.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("fail to create sidecar client: %w", err)
	}
	expectedSignerNetwork := strings.TrimSpace(cfg.SignerExpectedNetwork)
	if expectedSignerNetwork == "" {
		return nil, fmt.Errorf("xmr signer_expected_network is required")
	}
	info, compatibilityErr := sc.RequireCompatibility(
		sidecar.CapabilityAtomicOutgoingV2,
		sidecar.CapabilityBalanceReconciliation,
		sidecar.CapabilityBoundRecovery,
		sidecar.CapabilityPerVaultKeyStatus,
		sidecar.CapabilityRestartReservation,
		sidecar.CapabilityDeadOutgoingQuarantine,
		sidecar.CapabilityScannerContinuity,
		sidecar.CapabilityDurableInboundReplay,
		sidecar.CapabilityScannerBoundedRewind,
		sidecar.CapabilitySharePlanQuarantine,
		sidecar.CapabilityVerifiedAbsenceBalance,
		sidecar.CapabilityCanonicalObservation,
		sidecar.CapabilityCanonicalTxLocation,
		sidecar.CapabilityMoneroNetworkIdentity,
	)
	if compatibilityErr != nil {
		return nil, fmt.Errorf("xmr signer compatibility check failed: %w", compatibilityErr)
	}
	// Bounded combine retries are only safe once the signer proves it durably
	// replays combine; an older signer keeps single-shot combine rather than
	// risking a second broadcast of the same Monero transaction.
	idempotentCombine := info.HasCapability(sidecar.CapabilityIdempotentCombine)
	sc.SetSignCombineIdempotent(idempotentCombine)
	logger.Info().
		Str("signer_revision", info.GitCommit).
		Str("signer_network", info.Network).
		Str("monero_nettype", info.MoneroNettype).
		Str("monero_genesis_hash", info.MoneroGenesisHash).
		Int("signer_state_schema", info.StateSchema).
		Bool("idempotent_combine", idempotentCombine).
		Msg("verified xmr signer compatibility contract")
	if cleared, clearErr := sc.ClearSessions(); clearErr != nil {
		logger.Warn().Err(clearErr).Msg("fail to clear sidecar sessions on bifrost startup; sidecar may carry stale state")
	} else if cleared > 0 {
		logger.Info().Int("cleared", cleared).Msg("cleared stale sidecar sessions on bifrost startup")
	}

	localNodePubKey := common.EmptyPubKey
	if thorKeys != nil {
		if privKey, keyErr := thorKeys.GetPrivateKey(); keyErr != nil {
			logger.Debug().Err(keyErr).Msg("fail to get local node private key for monero vault mapping")
		} else if tmPubKey, convErr := codec.ToCmtPubKeyInterface(privKey.PubKey()); convErr != nil {
			logger.Debug().Err(convErr).Msg("fail to convert local node pubkey for monero vault mapping")
		} else if pk, pubErr := common.NewPubKeyFromCrypto(tmPubKey); pubErr != nil {
			logger.Debug().Err(pubErr).Msg("fail to encode local node pubkey for monero vault mapping")
		} else {
			localNodePubKey = pk
		}
	}

	vaultBootstrap, bootstrapErr := deriveVaultBootstrap(thorKeys)
	if bootstrapErr != nil {
		logger.Debug().Err(bootstrapErr).Msg("fail to derive local monero frost vault bootstrap material")
	}
	scanner := NewScanner(logger, sc, bridge, cfg.ChainID, localNodePubKey, cfg.BlockScanner.MaxReorgRescanBlocks)
	scanner.SetVaultBootstrap(vaultBootstrap)

	c := &Client{
		logger:                    logger,
		cfg:                       cfg,
		chain:                     cfg.ChainID,
		thorchainBridge:           bridge,
		localNodePubKey:           localNodePubKey,
		vaultBootstrap:            vaultBootstrap,
		scanner:                   scanner,
		sidecar:                   sc,
		tssServer:                 tssServer,
		asgardAddresses:           make([]common.Address, 0),
		vaultLocks:                make(map[string]*sync.Mutex),
		consolidationState:        make(map[string]consolidationVaultState),
		consolidateInProgress:     atomic.NewBool(false),
		lastConsolidationHeight:   atomic.NewInt64(0),
		signerCacheManager:        nil,
		vaultAddressMap:           make(map[string]common.Address),
		vaultAddrToKey:            make(map[string]string),
		lastSolvencyCheckHeight:   atomic.NewInt64(0),
		wg:                        &sync.WaitGroup{},
		stopchan:                  make(chan struct{}),
		spentRefsRetryInterval:    constants.ThorchainBlockTime,
		spentRefsRetryMaxAttempts: defaultXMRSpentRefsRetryMaxAttempts,
		recoveryRetryInterval:     constants.ThorchainBlockTime,
		recoveryRetryMaxAttempts:  defaultXMRRecoveryRetryMaxAttempts,
	}

	var path string
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}

	c.storage, err = blockscanner.NewBlockScannerStorageWithScanPosSync(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scanner storage: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(c.cfg.BlockScanner, c.storage, m, c.thorchainBridge, c.scanner)
	if err != nil {
		return nil, fmt.Errorf("fail to create block scanner: %w", err)
	}

	c.signerCacheManager, err = signercache.NewSignerCacheManager(c.storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager: %w", err)
	}
	c.signedTxPayloadStore = newSignedTxPayloadStore(c.storage.GetInternalDb())
	c.vaultAddressStore = NewVaultAddressStore(c.storage.GetInternalDb())
	c.scanner.SetReorgStore(NewReorgStore(c.storage.GetInternalDb()))
	internalReplayStore := newAuthenticatedInternalInboundReplayStore(c.storage.GetInternalDb())
	if err = internalReplayStore.ResetClaims(); err != nil {
		return nil, fmt.Errorf("reset authenticated xmr migration replay claims: %w", err)
	}
	c.scanner.SetAuthenticatedInternalInboundReplayStore(internalReplayStore)
	c.scanner.SetClaimQuarantineHandler(c.signedTxPayloadStore.QuarantineReverted)
	c.scanner.SetLocalSignedTxHandler(c.signedTxPayloadStore.IsLocallySignedTx)
	// Wire solvency reporting to the per-block scan loop. Without this, the
	// chain client's ReportSolvency only fires from the auto-unhalt
	// SolvencyCheckRunner — which itself is gated on the chain being already
	// halted, so vault drift is never detected during normal operation.
	// Match the XRP / Solana wiring pattern (their scanners take a
	// SolvencyReporter callback in their constructors).
	c.scanner.SetSolvencyReporter(c.ReportSolvency)
	if err := c.loadVaultAddressMap(); err != nil {
		c.logger.Warn().Err(err).Msg("fail to load persisted monero vault addresses")
	}

	return c, nil
}

func (c *Client) Start(
	globalTxsQueue chan stypes.TxIn,
	globalErrataQueue chan stypes.ErrataBlock,
	globalSolvencyQueue chan stypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.ensureConsolidationState()
	c.globalSolvencyQueue = globalSolvencyQueue
	c.scanner.SetErrataQueue(globalErrataQueue)
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.thorchainBridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
	c.wg.Add(1)
	go c.consolidationRunner()
	c.wg.Add(1)
	go c.spentOutputRefsSyncRunner()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.restoreKeysharesFromRecoveryFiles()
	}()
}

// BlockPermanentErrataBroadcast fences the signed hash before receipt delivery.
func (c *Client) BlockPermanentErrataBroadcast(errata common.ErrataTx) error {
	if !errata.Chain.Equals(common.XMRChain) || errata.Id.IsEmpty() {
		return errors.New("invalid XMR permanent errata broadcast block request")
	}
	return c.signedTxPayloadStore.BlockRevertedTxHash(errata.Id.String())
}

// QuarantinePermanentErrata closes the event-first gap where THORChain can
// commit a Reverted voter before this node's Scanner has produced a receipt
// callback. The exact signed artifact is blocked first, then the scanner's
// permanent TxID filter is fsynced. Both writes are idempotent, so Observer can
// safely retry unfinished quarantine after a partial failure or restart.
func (c *Client) QuarantinePermanentErrata(errata common.ErrataTx, orphanHeight int64) error {
	if !errata.Chain.Equals(common.XMRChain) || errata.Id.IsEmpty() {
		return errors.New("invalid XMR permanent errata quarantine request")
	}
	if orphanHeight < 0 {
		return errors.New("invalid XMR permanent errata height")
	}
	if c.signedTxPayloadStore == nil {
		return errors.New("XMR signed transaction quarantine is unavailable")
	}
	if err := c.signedTxPayloadStore.QuarantineReverted(errata.Id.String()); err != nil {
		return fmt.Errorf("quarantine permanently reverted XMR signed transaction: %w", err)
	}
	if c.scanner == nil || c.scanner.reorgStore == nil {
		return errors.New("XMR scanner reverted transaction quarantine is unavailable")
	}
	if err := c.scanner.reorgStore.QuarantineRevertedTxs(orphanHeight, []stypes.ErrataTx{{
		Chain: common.XMRChain,
		TxID:  errata.Id,
	}}); err != nil {
		return fmt.Errorf("quarantine permanently reverted XMR scanner transaction: %w", err)
	}
	return nil
}

func (c *Client) restoreKeysharesFromRecoveryFiles() {
	interval := c.recoveryRetryInterval
	if interval <= 0 {
		interval = constants.ThorchainBlockTime
	}
	for {
		// A pass may return because no artifact exists yet, every current file
		// was consumed, or a configured per-pass retry ceiling was reached.
		// Keep supervising for the process lifetime so a recovery artifact
		// generated later is never missed.
		c.restoreKeysharesFromDir(app.DefaultNodeHome)
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-c.stopchan:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (c *Client) restoreKeysharesFromDir(dir string) {
	if c.sidecar == nil {
		return
	}
	interval := c.recoveryRetryInterval
	if interval <= 0 {
		interval = constants.ThorchainBlockTime
	}
	maxAttempts := c.recoveryRetryMaxAttempts
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	defer func() {
		close(done)
		cancel()
	}()
	go func() {
		select {
		case <-c.stopchan:
			cancel()
		case <-done:
		}
	}()

	for attempt := 1; ; attempt++ {
		pending, found := c.restoreKeysharesPassContext(ctx, dir, attempt)
		if ctx.Err() != nil {
			return
		}
		if !found || pending == 0 {
			return
		}
		if maxAttempts > 0 && attempt >= maxAttempts {
			c.logger.Error().Int("attempts", attempt).Int("pending", pending).Msg("xmr recovery: retry limit reached; encrypted artifacts retained")
			return
		}

		delay := recoveryBackoff(interval, attempt)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-c.stopchan:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func (c *Client) restoreKeysharesPassContext(ctx context.Context, dir string, attempt int) (pending int, found bool) {
	if sweepErr := sweepStaleConsumedRecoveryFiles(dir); sweepErr != nil {
		c.logger.Warn().Err(sweepErr).Str("dir", dir).Msg("xmr recovery: failed to sweep stale consumed artifacts")
	}
	pattern := filepath.Join(dir, "frost-recovery-*.hex")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		c.logger.Error().Err(err).Msg("xmr recovery: invalid recovery glob")
		return 1, true
	}
	for _, path := range matches {
		if ctx.Err() != nil {
			return pending, found
		}
		found = true
		base := filepath.Base(path)
		vaultPubKeyStr := strings.TrimSuffix(strings.TrimPrefix(base, "frost-recovery-"), ".hex")
		vaultPubKey, parseErr := common.NewPubKey(vaultPubKeyStr)
		if parseErr != nil {
			// A malformed pubkey filename can never become valid; retrying it
			// would keep this supervisor alive for the process lifetime even
			// after every real keyshare has restored. Log and skip WITHOUT
			// counting it pending. The file is left on disk for an operator to
			// inspect. Transient failures below still keep the loop retrying.
			c.logger.Err(parseErr).Str("file", base).Msg("xmr recovery: invalid vault pubkey in filename; skipping permanently")
			continue
		}

		keyBlob, readErr := readRecoveryFile(path)
		if readErr != nil {
			pending++
			c.logger.Err(readErr).Str("file", base).Msg("xmr recovery: rejected recovery file")
			continue
		}

		if restoreErr := c.registerAndRestoreKeyshareContext(ctx, vaultPubKey, keyBlob); restoreErr != nil {
			pending++
			c.logger.Warn().Err(restoreErr).Str("vault", vaultPubKeyStr).Int("attempt", attempt).Msg("xmr recovery: restore not yet verified; encrypted artifact retained")
			continue
		}

		if removeErr := consumeRecoveryFile(path); removeErr != nil {
			pending++
			c.logger.Error().Err(removeErr).Str("vault", vaultPubKeyStr).Str("path", path).Msg("xmr recovery: verified restore but failed to consume encrypted artifact")
			continue
		}
		c.logger.Info().Str("vault", vaultPubKeyStr).Msg("xmr recovery: exact-vault signability verified and encrypted artifact consumed")
	}
	return pending, found
}

func recoveryBackoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	shift := attempt - 1
	if shift > 6 {
		shift = 6
	}
	delay := base * time.Duration(1<<shift)
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

// registerAndRestoreKeyshare registers the vault with the sidecar before
// restoring the keyshare: the sidecar resolves bech32 vault identifiers only
// for registered vaults, so restoring first races the independent vault
// registration path and 400s until it happens to land. Registration is
// idempotent and derived entirely from local inputs. The pair is retried
// together because at bifrost startup the sidecar may not be reachable yet.
func (c *Client) registerAndRestoreKeyshareContext(ctx context.Context, vaultPubKey common.PubKey, keyBlob []byte) error {
	if err := c.ensureVaultRegisteredContext(ctx, vaultPubKey); err != nil {
		return fmt.Errorf("register vault before restore: %w", err)
	}
	return c.restoreKeyshareContext(ctx, vaultPubKey, keyBlob)
}

func readRecoveryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat recovery file: %w", err)
	}
	if err = repairRecoveryReadAlias(path, info); err != nil {
		return nil, err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat repaired recovery file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("recovery path is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxXMRRecoveryFileBytes {
		return nil, fmt.Errorf("recovery file size %d is outside 1..%d", info.Size(), maxXMRRecoveryFileBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recovery file: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat opened recovery file: %w", err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("recovery file changed during no-follow validation")
	}
	if stat, ok := opened.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
		return nil, fmt.Errorf("recovery file must have exactly one hard link")
	}
	buf := make([]byte, opened.Size())
	if _, err = io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("read recovery file: %w", err)
	}
	return buf, nil
}

// repairRecoveryReadAlias completes a crash-interrupted no-replace publish.
// It only removes a tool-owned same-directory temporary name that references
// the exact final inode; unrelated hard links remain a fail-closed condition.
func repairRecoveryReadAlias(path string, finalInfo os.FileInfo) error {
	stat, ok := finalInfo.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink <= 1 {
		return nil
	}
	matches, err := filepath.Glob(path + ".tmp-*")
	if err != nil {
		return fmt.Errorf("inspect recovery publish aliases: %w", err)
	}
	removed := false
	for _, candidate := range matches {
		candidateInfo, statErr := os.Lstat(candidate)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return fmt.Errorf("stat recovery publish alias: %w", statErr)
		}
		if !candidateInfo.Mode().IsRegular() || !os.SameFile(finalInfo, candidateInfo) {
			continue
		}
		if removeErr := os.Remove(candidate); removeErr != nil {
			return fmt.Errorf("remove recovery publish alias: %w", removeErr)
		}
		removed = true
	}
	if removed {
		if err = syncDirectory(filepath.Dir(path)); err != nil {
			return err
		}
	}
	refreshed, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat recovery file after alias repair: %w", err)
	}
	refreshedStat, ok := refreshed.Sys().(*syscall.Stat_t)
	if !ok || refreshedStat.Nlink != 1 {
		return fmt.Errorf("recovery file has unexpected hard links")
	}
	return nil
}

// consumeRecoveryFile unlinks a restored recovery artifact. The unlink is
// atomic and the directory sync makes it durable, so no intermediate
// ".consumed-*" name is published: a crash mid-consume either leaves the
// original file (retried on the next pass) or nothing at all, never a stranded
// alias that no glob revisits.
func consumeRecoveryFile(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove consumed recovery file: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

// sweepStaleConsumedRecoveryFiles removes ".consumed-*" artifacts left behind
// by the previous rename-then-remove consume path when it crashed between the
// two steps. They no longer match the recovery glob, so nothing else would ever
// reclaim them.
//
// ponytail: only ".consumed-*" is swept. An orphaned ".tmp-*" with no final
// inode may be an in-flight publish by the recovery tool, and removing it would
// race that writer; repairRecoveryReadAlias already reclaims the ".tmp-*"
// aliases that reference a published inode.
func sweepStaleConsumedRecoveryFiles(dir string) error {
	matches, err := filepath.Glob(filepath.Join(dir, "frost-recovery-*.hex.consumed-*"))
	if err != nil {
		return fmt.Errorf("inspect stale consumed recovery files: %w", err)
	}
	removed := false
	for _, path := range matches {
		if removeErr := os.Remove(path); removeErr != nil {
			if os.IsNotExist(removeErr) {
				continue
			}
			return fmt.Errorf("remove stale consumed recovery file: %w", removeErr)
		}
		removed = true
	}
	if !removed {
		return nil
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open recovery directory: %w", err)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("sync recovery directory: %w", err)
	}
	return nil
}

func (c *Client) spentOutputRefsSyncRunner() {
	defer c.wg.Done()

	c.catchupSpentOutputRefs()

	ticker := time.NewTicker(constants.ThorchainBlockTime * 10)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			c.catchupSpentOutputRefs()
		}
	}
}

func (c *Client) catchupSpentOutputRefs() {
	if c.thorchainBridge == nil || c.sidecar == nil {
		return
	}
	if blockscanner.IsXMRScannerPaused(c.logger, c.thorchainBridge) {
		return
	}
	vaultKeys, err := c.getSpentOutputCatchupVaultKeys()
	if err != nil {
		c.logger.Err(err).Msg("xmr catchup: failed to get vault pubkeys")
		return
	}
	for _, vaultPubKey := range vaultKeys {
		refs, err := c.thorchainBridge.GetXMRSpentOutputRefs(vaultPubKey)
		if err != nil {
			c.logger.Warn().
				Err(err).
				Str("vault", vaultPubKey.String()).
				Msg("xmr catchup: failed to get spent output refs")
			continue
		}

		refsBySpendTx := make(map[string][]common.OutputRef)
		for _, ref := range refs {
			if ref == nil {
				continue
			}
			spendTxHash := strings.TrimSpace(ref.SpendTxHash)
			if spendTxHash == "" {
				c.logger.Warn().
					Str("vault", vaultPubKey.String()).
					Str("input_tx_hash", ref.TxHash).
					Uint32("output_index", ref.OutputIndex).
					Msg("xmr catchup: skipping spent ref without spend tx hash")
				continue
			}
			refsBySpendTx[spendTxHash] = append(refsBySpendTx[spendTxHash], *ref)
		}

		for spendTxHash, refs := range refsBySpendTx {
			// Use the same verified import path as live observations so durable
			// signer state is committed before a migration replay is enrolled.
			// Catch-up itself runs periodically, so do not create a per-import
			// retry goroutine here; an incomplete import is retried on the next tick.
			_, applied, retry := c.applyCommittedSpentOutputRefsOnce(
				context.Background(),
				vaultPubKey,
				common.TxID(spendTxHash),
				refs,
				false,
			)
			switch {
			case applied:
				c.logger.Info().
					Str("vault", vaultPubKey.String()).
					Str("tx_hash", spendTxHash).
					Msg("xmr catchup: applied spent output refs from thorchain")
			case retry:
				c.logger.Warn().
					Str("vault", vaultPubKey.String()).
					Str("tx_hash", spendTxHash).
					Msg("xmr catchup: spent output refs import will retry on the next scan")
			default:
				c.logger.Warn().
					Str("vault", vaultPubKey.String()).
					Str("tx_hash", spendTxHash).
					Msg("xmr catchup: spent output refs import did not apply; manual investigation may be required")
			}
		}
	}
}

// ApplyCommittedSpentOutputRefs treats a quorum event as a bounded wake-up hint.
// The event contributes only the vault and transaction ID; all refs and outbound
// metadata are reconstructed from fresh, consensus-anchored THORChain queries.
func (c *Client) ApplyCommittedSpentOutputRefs(
	ctx context.Context,
	vaultPubKey common.PubKey,
	txID common.TxID,
) (common.ObservedTx, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := c.spentRefsRetryInterval
	if interval <= 0 {
		// Give THORChain one block for the committed state to become visible.
		interval = constants.ThorchainBlockTime
	}

	for attempt := 1; attempt <= defaultXMRCommittedRefsImportMaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return common.ObservedTx{}, false
		case <-c.stopchan:
			return common.ObservedTx{}, false
		default:
		}
		imported, applied, retry := c.applyCommittedSpentOutputRefsHintOnce(ctx, vaultPubKey, txID)
		if applied {
			return imported, true
		}
		if !retry || ctx.Err() != nil {
			return common.ObservedTx{}, false
		}
		if attempt == defaultXMRCommittedRefsImportMaxAttempts {
			break
		}

		c.logger.Debug().
			Str("vault", vaultPubKey.String()).
			Str("tx_hash", txID.String()).
			Int("attempt", attempt).
			Msg("xmr committed spent-ref state is not ready; waiting for thornode commit")
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return common.ObservedTx{}, false
		case <-c.stopchan:
			timer.Stop()
			return common.ObservedTx{}, false
		case <-timer.C:
		}
	}

	c.logger.Warn().
		Str("vault", vaultPubKey.String()).
		Str("tx_hash", txID.String()).
		Int("attempts", defaultXMRCommittedRefsImportMaxAttempts).
		Msg("xmr committed spent-ref import did not become ready; periodic catch-up will retry")
	return common.ObservedTx{}, false
}

func (c *Client) applyCommittedSpentOutputRefsHintOnce(
	ctx context.Context,
	vaultPubKey common.PubKey,
	txID common.TxID,
) (imported common.ObservedTx, applied, retry bool) {
	if c.thorchainBridge == nil || c.sidecar == nil {
		return common.ObservedTx{}, false, false
	}
	// Live hints must not wait on the shared cache lock held by catch-up.
	// The uncached query bounds the request and inherits caller cancellation.
	refs, err := c.thorchainBridge.GetXMRSpentOutputRefsUncached(ctx, vaultPubKey)
	if err != nil {
		level := zerolog.WarnLevel
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
			level = zerolog.DebugLevel
		}
		c.logger.WithLevel(level).Err(err).
			Str("vault", vaultPubKey.String()).
			Str("tx_hash", txID.String()).
			Msg("failed to load committed xmr spent output refs")
		return common.ObservedTx{}, false, true
	}

	matching := make([]common.OutputRef, 0, len(refs))
	for _, ref := range refs {
		if ref == nil || !common.TxID(strings.TrimSpace(ref.SpendTxHash)).Equals(txID) {
			continue
		}
		matching = append(matching, *ref)
	}
	if len(matching) == 0 {
		return common.ObservedTx{}, false, true
	}
	return c.applyCommittedSpentOutputRefsOnce(ctx, vaultPubKey, txID, matching, true)
}

func (c *Client) applyCommittedSpentOutputRefsOnce(
	ctx context.Context,
	vaultPubKey common.PubKey,
	txID common.TxID,
	refs []common.OutputRef,
	uncachedVoter bool,
) (imported common.ObservedTx, applied, retry bool) {
	if ctx.Err() != nil {
		return common.ObservedTx{}, false, false
	}
	obsTx, retry, ok := c.committedXMROutboundForSpentRefs(ctx, txID, vaultPubKey, refs, uncachedVoter)
	if !ok {
		return common.ObservedTx{}, false, retry
	}
	applied, retry = c.applySpentOutputRefsOnceContext(ctx, types.SpentOutputRefsImport{
		Tx:             obsTx,
		ChainCommitted: true,
	})
	return obsTx, applied, retry
}

func (c *Client) committedXMROutboundForSpentRefs(
	ctx context.Context,
	txID common.TxID,
	vaultPubKey common.PubKey,
	refs []common.OutputRef,
	uncachedVoter bool,
) (observed common.ObservedTx, retry, ok bool) {
	if c.thorchainBridge == nil {
		return common.ObservedTx{}, false, false
	}
	var (
		voter *ttypes.QueryObservedTxVoter
		found bool
		err   error
	)
	if uncachedVoter {
		voter, found, err = c.thorchainBridge.GetObservedTxOutVoterUncached(ctx, txID.String())
	} else {
		voter, err = c.thorchainBridge.GetObservedTxOutVoter(txID.String())
		found = voter != nil
	}
	if err != nil {
		level := zerolog.WarnLevel
		if errors.Is(ctx.Err(), context.Canceled) && errors.Is(err, context.Canceled) {
			level = zerolog.DebugLevel
		}
		c.logger.WithLevel(level).Err(err).Str("tx_hash", txID.String()).Msg("failed to load committed xmr observed tx metadata")
		return common.ObservedTx{}, true, false
	}
	if !found {
		return common.ObservedTx{}, true, false
	}
	observed, retry, ok = queryConsensusObservedXMRTx(voter, txID)
	if !ok {
		if !retry {
			c.logger.Warn().
				Str("tx_hash", txID.String()).
				Bool("reverted", voter.Reverted).
				Msg("reject committed xmr spent refs without a matching canonical voter transaction")
		}
		return common.ObservedTx{}, retry, false
	}
	if !observed.ObservedPubKey.Equals(vaultPubKey) {
		c.logger.Warn().
			Str("tx_hash", txID.String()).
			Str("observed_vault", observed.ObservedPubKey.String()).
			Str("spent_ref_vault", vaultPubKey.String()).
			Msg("committed xmr spent refs vault does not match consensus tx")
		return common.ObservedTx{}, false, false
	}
	observed.SpentOutputRefs = refs
	if err := validateXMRSpentRefsForSidecar(observed); err != nil {
		c.logger.Warn().
			Err(err).
			Str("tx_hash", txID.String()).
			Str("vault", vaultPubKey.String()).
			Msg("skipping invalid committed xmr spent output refs")
		return common.ObservedTx{}, false, false
	}
	return observed, false, true
}

// queryConsensusObservedXMRTx selects only the canonical voter transaction.
// Non-consensus Txs entries are observations, not proof that THORChain accepted
// their metadata. A reverted voter is a terminal rejection, while a voter that
// has not reached consensus yet remains retryable for the short commit race.
func queryConsensusObservedXMRTx(
	voter *ttypes.QueryObservedTxVoter,
	txID common.TxID,
) (observed common.ObservedTx, retry, ok bool) {
	if voter == nil {
		return common.ObservedTx{}, true, false
	}
	if voter.Reverted {
		return common.ObservedTx{}, false, false
	}
	if voter.GetHeight() <= 0 && voter.GetFinalisedHeight() <= 0 {
		return common.ObservedTx{}, true, false
	}
	if !voter.GetTxID().Equals(txID) {
		return common.ObservedTx{}, false, false
	}
	candidate := voter.GetTx()
	tx := candidate.GetTx()
	if !tx.ID.Equals(txID) || !tx.Chain.Equals(common.XMRChain) {
		return common.ObservedTx{}, false, false
	}
	return common.ObservedTx{
		Tx:                    tx,
		Status:                common.Status_incomplete,
		BlockHeight:           candidate.GetBlockHeight(),
		ObservedPubKey:        candidate.GetObservedPubKey(),
		FinaliseHeight:        candidate.GetFinaliseHeight(),
		Aggregator:            candidate.GetAggregator(),
		AggregatorTarget:      candidate.GetAggregatorTarget(),
		AggregatorTargetLimit: candidate.AggregatorTargetLimit,
		SpentOutputRefs:       xmrOutputRefsFromPointers(candidate.GetSpentOutputRefs()),
		XmrTxKey:              candidate.GetXmrTxKey(),
		XmrMinedHeight:        candidate.GetXmrMinedHeight(),
	}, false, true
}

func xmrOutboundMeta(tx common.Tx, txKey string) *sidecar.OutboundMeta {
	var amount, fee uint64
	xmrCoin := tx.Coins.GetCoin(common.XMRAsset)
	if !xmrCoin.IsEmpty() {
		amount, _ = thorchainAmountToAtomic(xmrCoin.Amount)
	}
	gasCoin := tx.Gas.ToCoins().GetCoin(common.XMRAsset)
	if !gasCoin.IsEmpty() {
		fee, _ = thorchainAmountToAtomic(gasCoin.Amount)
	}
	return &sidecar.OutboundMeta{
		FromAddress: tx.FromAddress.String(),
		ToAddress:   tx.ToAddress.String(),
		TxKey:       txKey,
		Amount:      amount,
		Fee:         fee,
		Memo:        tx.Memo,
	}
}

// A successful idempotent MarkSpent may report Marked=0 when every output was
// already imported. NotFound is the authoritative proof that all requested
// refs were located; impossible counters still fail closed.
func verifiedXMRMarkSpentResult(result sidecar.MarkSpentResp, requested int) bool {
	return requested > 0 && result.NotFound == 0 && result.Marked >= 0 && result.Marked <= requested
}

func (c *Client) getSpentOutputCatchupVaultKeys() (common.PubKeys, error) {
	if c.thorchainBridge == nil {
		return nil, fmt.Errorf("thorchain bridge is nil")
	}
	pubKeys, err := c.thorchainBridge.GetPubKeys()
	if err != nil {
		return nil, fmt.Errorf("fail to get vault pubkeys: %w", err)
	}

	vaultKeys := make(common.PubKeys, 0, len(pubKeys))
	seen := make(map[string]struct{}, len(pubKeys))
	for _, pair := range pubKeys {
		if pair.Algo != common.SigningAlgoEd25519 || pair.PubKey.IsEmpty() {
			continue
		}
		key := pair.PubKey.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		vaultKeys = append(vaultKeys, pair.PubKey)
	}
	return vaultKeys, nil
}

func (c *Client) Stop() {
	c.blockScanner.Stop()
	c.lifecycleMu.Lock()
	close(c.stopchan)
	c.lifecycleMu.Unlock()
	c.wg.Wait()
}

func (c *Client) IsBlockScannerHealthy() bool {
	if c.blockScanner == nil {
		return false
	}
	return c.blockScanner.IsHealthy()
}

func (c *Client) ensureConsolidationState() {
	c.consolidationInitOnce.Do(func() {
		if c.consolidateInProgress == nil {
			c.consolidateInProgress = atomic.NewBool(false)
		}
		if c.lastConsolidationHeight == nil {
			c.lastConsolidationHeight = atomic.NewInt64(0)
		}
		if c.lastSolvencyCheckHeight == nil {
			c.lastSolvencyCheckHeight = atomic.NewInt64(0)
		}
		if c.vaultLocks == nil {
			c.vaultLocks = make(map[string]*sync.Mutex)
		}
		if c.consolidationState == nil {
			c.consolidationState = make(map[string]consolidationVaultState)
		}
	})
}

func (c *Client) GetVaultLock(vaultPubKey string) *sync.Mutex {
	c.ensureConsolidationState()
	key := strings.TrimSpace(vaultPubKey)
	if key == "" {
		return &c.emptyVaultLock
	}

	c.vaultLockMu.Lock()
	defer c.vaultLockMu.Unlock()
	lock, ok := c.vaultLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		c.vaultLocks[key] = lock
	}
	return lock
}

func (c *Client) GetVaultLockKey(tx stypes.TxOutItem) string {
	vaultKey := txVaultPubKey(tx)
	return strings.TrimSpace(vaultKey.String())
}

func (c *Client) consolidationRunner() {
	defer c.wg.Done()

	ticker := time.NewTicker(constants.ThorchainBlockTime)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			c.tryConsolidationPass()
		}
	}
}

func (c *Client) tryConsolidationPass() {
	c.ensureConsolidationState()
	if !c.consolidateInProgress.CompareAndSwap(false, true) {
		return
	}
	defer c.consolidateInProgress.Store(false)

	if err := c.runConsolidationPass(); err != nil {
		c.logger.Debug().Err(err).Msg("xmr consolidation pass failed")
	}
}

func (c *Client) runConsolidationPass() error {
	c.ensureConsolidationState()
	if c.thorchainBridge == nil || c.sidecar == nil || c.tssServer == nil {
		return nil
	}
	if c.localNodePubKey.IsEmpty() {
		return nil
	}
	if c.blockScanner != nil && !c.IsBlockScannerHealthy() {
		return nil
	}

	height, err := c.thorchainBridge.GetBlockHeight()
	if err != nil {
		return fmt.Errorf("fail to get thorchain block height for xmr consolidation: %w", err)
	}
	if height <= 0 {
		return nil
	}
	if c.lastConsolidationHeight.Load() >= height {
		return nil
	}
	c.lastConsolidationHeight.Store(height)

	settings := c.loadConsolidationSettings()
	if !settings.Enabled {
		return nil
	}

	nodeStatus, err := c.thorchainBridge.FetchNodeStatus()
	if err != nil {
		return fmt.Errorf("fail to get node status for xmr consolidation: %w", err)
	}
	if nodeStatus != ttypes.NodeStatus_Active {
		return nil
	}

	// Skip when HALTSIGNING / HaltSigning<Chain> is active. The shared helper is also
	// invoked from SignTx, so this entry-point check is a cheap fast-path that avoids
	// the GetAsgards round-trip below when the network has halted.
	if signing.IsSigningHalted(c.thorchainBridge, c.chain, c.logger) {
		return nil
	}

	vaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards for xmr consolidation: %w", err)
	}
	if len(vaults) == 0 {
		return nil
	}

	churnInterval := c.getChurnInterval()
	for _, vault := range vaults {
		if !vault.Contains(c.localNodePubKey) {
			continue
		}
		if !vault.GetChains().Has(c.chain) {
			continue
		}
		if c.shouldSkipConsolidationNearChurn(vault, height, churnInterval, settings.ChurnLookahead) {
			c.logger.Debug().
				Str("vault_pubkey", txVaultPubKey(stypes.TxOutItem{VaultPubKey: vault.PubKey, VaultPubKeyFrost: vault.PubKeyFrost}).String()).
				Int64("height", height).
				Int64("churn_lookahead", settings.ChurnLookahead).
				Msg("skip xmr consolidation: near churn")
			continue
		}
		triggerPeriodic := settings.CheckIntervalBlocks > 0 && height%settings.CheckIntervalBlocks == 0
		if !triggerPeriodic {
			continue
		}
		c.consolidateVault(vault, height, settings, triggerPeriodic)
	}
	return nil
}

func (c *Client) shouldSkipConsolidationNearChurn(vault ttypes.Vault, thorchainHeight, churnInterval, lookahead int64) bool {
	if vault.IsRetiring() {
		return true
	}
	if !vault.IsActive() || churnInterval <= 0 || lookahead <= 0 {
		return false
	}
	nextChurnHeight := vault.StatusSince + churnInterval
	return thorchainHeight+lookahead >= nextChurnHeight
}

func (c *Client) consolidateVault(
	vault ttypes.Vault,
	thorchainHeight int64,
	settings consolidationSettings,
	triggerPeriodic bool,
) {
	vaultPubKey := txVaultPubKey(stypes.TxOutItem{
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: common.EmptyPubKey,
		VaultPubKeyFrost: vault.PubKeyFrost,
	})
	if vaultPubKey.IsEmpty() {
		return
	}

	stateKey := vaultPubKey.String()
	state := c.getConsolidationVaultState(stateKey)
	effectiveCooldown := scaledConsolidationCooldown(settings.CooldownBlocks, state.ConsecutiveFails)
	if effectiveCooldown > 0 && state.LastAttemptHeight > 0 && thorchainHeight-state.LastAttemptHeight < effectiveCooldown {
		c.logger.Debug().
			Str("vault_pubkey", stateKey).
			Int64("last_attempt_height", state.LastAttemptHeight).
			Int64("cooldown_blocks", effectiveCooldown).
			Msg("skip xmr consolidation: cooldown active")
		return
	}

	vaultAddress := c.resolveVaultAddress(vault)
	if vaultAddress.IsEmpty() {
		return
	}

	lock := c.GetVaultLock(stateKey)
	lock.Lock()
	defer lock.Unlock()

	hasMempoolOutgoing, err := c.hasMempoolOutgoingForVault(vaultPubKey, vaultAddress.String())
	if err != nil {
		c.logger.Debug().Err(err).Str("vault_pubkey", stateKey).Msg("skip xmr consolidation: fail to check mempool")
		return
	}
	if hasMempoolOutgoing {
		c.logger.Debug().Str("vault_pubkey", stateKey).Msg("skip xmr consolidation: pending mempool outbound")
		return
	}

	if err = c.ensureVaultRegistered(vaultPubKey); err != nil {
		c.logger.Debug().Err(err).Str("vault_pubkey", stateKey).Msg("skip xmr consolidation: fail to register vault")
		return
	}

	balance, err := c.sidecar.Balance(stateKey)
	if err != nil {
		c.logger.Debug().Err(err).Str("vault_pubkey", stateKey).Msg("skip xmr consolidation: fail to query balance")
		return
	}

	spendableAtomic := spendableBalanceAtomic(balance)
	if spendableAtomic < settings.MinUnlockedAtomic {
		c.logger.Debug().
			Str("vault_pubkey", stateKey).
			Uint64("spendable_atomic", spendableAtomic).
			Uint64("min_unlocked_atomic", settings.MinUnlockedAtomic).
			Msg("skip xmr consolidation: spendable balance below threshold")
		return
	}

	outputs, err := c.sidecar.Outputs(stateKey)
	if err != nil {
		c.logger.Debug().Err(err).Str("vault_pubkey", stateKey).Msg("skip xmr consolidation: fail to query outputs")
		return
	}
	currentHeight := int64(0)
	if c.scanner != nil {
		currentHeight = c.scanner.CurrentHeight()
	}
	unlockedOutputs := countUnlockedOutputs(outputs, currentHeight)
	if unlockedOutputs < settings.MinUnlockedOutputs {
		c.logger.Debug().
			Str("vault_pubkey", stateKey).
			Int64("unlocked_outputs", unlockedOutputs).
			Int64("min_unlocked_outputs", settings.MinUnlockedOutputs).
			Msg("skip xmr consolidation: output count below threshold")
		return
	}
	if unlockedOutputs <= settings.TargetOutputs {
		c.logger.Debug().
			Str("vault_pubkey", stateKey).
			Int64("unlocked_outputs", unlockedOutputs).
			Int64("target_outputs", settings.TargetOutputs).
			Msg("skip xmr consolidation: already at target output count")
		return
	}

	feeRate, err := c.sidecar.FeeRate()
	if err != nil {
		c.logger.Debug().Err(err).Str("vault_pubkey", stateKey).Msg("skip xmr consolidation: fail to query fee rate")
		return
	}

	maxRetries := settings.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 1
	}
	reserve := initialConsolidationReserve(spendableAtomic, settings.ReserveBps)
	maxLockAtomic := xmrConsolidationMaxLockAtomic(spendableAtomic, settings.MaxLockBps)
	if maxLockAtomic == 0 {
		c.logger.Debug().
			Str("vault_pubkey", stateKey).
			Uint64("spendable_atomic", spendableAtomic).
			Int64("max_lock_bps", settings.MaxLockBps).
			Msg("skip xmr consolidation: management lock budget is zero")
		return
	}
	lastErr := error(nil)
	for attempt := int64(1); attempt <= maxRetries; attempt++ {
		if reserve >= spendableAtomic {
			break
		}
		amountAtomic := spendableAtomic - reserve
		if amountAtomic > maxLockAtomic {
			amountAtomic = maxLockAtomic
		}
		amountThorchain := amountAtomic / moneroAtomicToThorchain
		if amountThorchain == 0 {
			break
		}

		gasRate := int64(feeRate)
		if feeRate > uint64(math.MaxInt64) {
			gasRate = math.MaxInt64
		}
		txOutItem := stypes.TxOutItem{
			Chain:            c.chain,
			ToAddress:        vaultAddress,
			VaultPubKey:      vault.PubKey,
			VaultPubKeyEddsa: common.EmptyPubKey,
			VaultPubKeyFrost: vault.PubKeyFrost,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(amountThorchain)),
			},
			Memo:      memo.NewConsolidateMemo().String(),
			GasRate:   gasRate,
			SigningID: fmt.Sprintf("xmr-consolidation:%s:%d:%d:%d", stateKey, thorchainHeight, attempt, amountAtomic),
		}

		rawTx, _, observation, signErr := c.SignTx(txOutItem, thorchainHeight)
		if signErr != nil {
			lastErr = signErr
			if !isRetryableConsolidationErr(signErr) {
				break
			}
			reserve = bumpConsolidationReserve(reserve, settings.ReserveBps)
			continue
		}
		if len(rawTx) == 0 {
			lastErr = errors.New("signed consolidation transaction is empty")
			break
		}
		c.applySpentOutputRefsFromTxInItem(observation)

		txHash, broadcastErr := c.BroadcastTx(txOutItem, rawTx)
		if broadcastErr != nil {
			lastErr = broadcastErr
			if !isRetryableConsolidationErr(broadcastErr) {
				break
			}
			reserve = bumpConsolidationReserve(reserve, settings.ReserveBps)
			continue
		}

		c.setConsolidationVaultState(stateKey, consolidationVaultState{
			LastAttemptHeight: thorchainHeight,
			LastSuccessHeight: thorchainHeight,
			LastTxHash:        txHash,
			ConsecutiveFails:  0,
		})
		c.logger.Info().
			Str("vault_pubkey", stateKey).
			Str("tx_hash", txHash).
			Int64("unlocked_outputs", unlockedOutputs).
			Bool("periodic_trigger", triggerPeriodic).
			Msg("xmr consolidation broadcasted")
		return
	}

	state.LastAttemptHeight = thorchainHeight
	state.ConsecutiveFails++
	c.setConsolidationVaultState(stateKey, state)
	if lastErr != nil {
		c.logger.Debug().
			Err(lastErr).
			Str("vault_pubkey", stateKey).
			Int64("consecutive_failures", state.ConsecutiveFails).
			Msg("xmr consolidation failed")
	}
}

func (c *Client) applySpentOutputRefsFromTxInItem(obs *stypes.TxInItem) {
	if obs == nil || len(obs.SpentOutputRefs) == 0 {
		return
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
	obsTx.SpentOutputRefs = xmrOutputRefsFromPointers(obs.SpentOutputRefs)
	obsTx.XmrTxKey = obs.TxKey
	c.ApplySpentOutputRefs(obsTx)
}

func countUnlockedOutputs(outputs []sidecar.Output, currentHeight int64) int64 {
	if len(outputs) == 0 {
		return 0
	}
	count := int64(0)
	for _, out := range outputs {
		if out.Spent {
			continue
		}
		if currentHeight > 0 {
			if currentHeight-int64(out.Height) < 10 {
				continue
			}
		}
		count++
	}
	return count
}

func initialConsolidationReserve(availableAtomic uint64, reserveBps int64) uint64 {
	if reserveBps <= 0 {
		reserveBps = defaultXMRConsolidationReserveBps
	}
	reserve := scaleUint64ByBasisPoints(availableAtomic, reserveBps)
	if reserve < defaultXMRConsolidationMinReserveAtomic {
		reserve = defaultXMRConsolidationMinReserveAtomic
	}
	return reserve
}

func xmrConsolidationMaxLockAtomic(spendableAtomic uint64, maxLockBps int64) uint64 {
	if maxLockBps <= 0 {
		maxLockBps = defaultXMRConsolidationMaxLockBps
	}
	if maxLockBps > 10_000 {
		maxLockBps = 10_000
	}
	return scaleUint64ByBasisPoints(spendableAtomic, maxLockBps)
}

func bumpConsolidationReserve(currentReserve uint64, reserveBps int64) uint64 {
	step := currentReserve / 2
	if reserveBps > 0 {
		extra := scaleUint64ByBasisPoints(currentReserve, reserveBps)
		if extra > step {
			step = extra
		}
	}
	if step < defaultXMRConsolidationMinReserveAtomic {
		step = defaultXMRConsolidationMinReserveAtomic
	}
	if currentReserve > ^uint64(0)-step {
		return ^uint64(0)
	}
	return currentReserve + step
}

func scaleUint64ByBasisPoints(value uint64, bps int64) uint64 {
	if value == 0 || bps <= 0 {
		return 0
	}

	scaled := new(big.Int).SetUint64(value)
	scaled.Mul(scaled, big.NewInt(bps))
	scaled.Quo(scaled, big.NewInt(10_000))
	if !scaled.IsUint64() {
		return math.MaxUint64
	}
	return scaled.Uint64()
}

func scaledConsolidationCooldown(baseCooldown, consecutiveFails int64) int64 {
	if baseCooldown <= 0 {
		return 0
	}
	if consecutiveFails <= 1 {
		return baseCooldown
	}
	if consecutiveFails > math.MaxInt64/baseCooldown {
		return math.MaxInt64
	}
	return baseCooldown * consecutiveFails
}

func isRetryableConsolidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "insufficient") || strings.Contains(msg, "not enough funds") {
		return true
	}
	if strings.Contains(msg, "fee") || strings.Contains(msg, "mempool") {
		return true
	}
	return false
}

func (c *Client) hasMempoolOutgoingForVault(vaultPubKey common.PubKey, vaultAddress string) (bool, error) {
	resp, err := c.sidecar.Mempool()
	if err != nil {
		return false, err
	}
	vaultKey := strings.TrimSpace(vaultPubKey.String())
	vaultAddress = strings.TrimSpace(vaultAddress)
	for _, transfer := range resp.Outgoing {
		if transfer.VaultPubKey != "" && strings.EqualFold(strings.TrimSpace(transfer.VaultPubKey), vaultKey) {
			return true, nil
		}
		if transfer.FromAddress != "" && vaultAddress != "" && strings.EqualFold(strings.TrimSpace(transfer.FromAddress), vaultAddress) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) getConsolidationVaultState(vaultPubKey string) consolidationVaultState {
	c.ensureConsolidationState()
	c.consolidationStateMu.Lock()
	defer c.consolidationStateMu.Unlock()
	return c.consolidationState[vaultPubKey]
}

func (c *Client) setConsolidationVaultState(vaultPubKey string, state consolidationVaultState) {
	c.ensureConsolidationState()
	c.consolidationStateMu.Lock()
	c.consolidationState[vaultPubKey] = state
	c.consolidationStateMu.Unlock()
}

func (c *Client) loadConsolidationSettings() consolidationSettings {
	c.consolidationSettingsMu.RLock()
	if c.hasConsolidationConfig && time.Since(c.lastConsolidationConfig) < constants.ThorchainBlockTime {
		settings := c.consolidationSettings
		c.consolidationSettingsMu.RUnlock()
		return settings
	}
	c.consolidationSettingsMu.RUnlock()

	c.consolidationSettingsMu.Lock()
	defer c.consolidationSettingsMu.Unlock()
	if c.hasConsolidationConfig && time.Since(c.lastConsolidationConfig) < constants.ThorchainBlockTime {
		return c.consolidationSettings
	}

	enabled := c.cfg.XMRConsolidationEnabled
	if mimirEnabled, ok := c.mimirInt64Raw(mimirXMRConsolidationEnabled); ok {
		enabled = mimirEnabled > 0
	}
	settings := consolidationSettings{
		Enabled:             enabled,
		CheckIntervalBlocks: c.mimirInt64(mimirXMRConsolidationCheckIntervalBlocks, int64OrFallback(c.cfg.XMRConsolidationCheckIntervalBlocks, defaultXMRConsolidationCheckIntervalBlocks)),
		CooldownBlocks:      c.mimirInt64(mimirXMRConsolidationCooldownBlocks, int64OrFallback(c.cfg.XMRConsolidationCooldownBlocks, defaultXMRConsolidationCooldownBlocks)),
		MinUnlockedOutputs:  c.mimirInt64(mimirXMRConsolidationMinUnlockedOutputs, int64OrFallback(c.cfg.XMRConsolidationMinUnlockedOutputs, defaultXMRConsolidationMinUnlockedOutputs)),
		TargetOutputs:       c.mimirInt64(mimirXMRConsolidationTargetOutputs, int64OrFallback(c.cfg.XMRConsolidationTargetOutputs, defaultXMRConsolidationTargetOutputs)),
		MinUnlockedAtomic:   c.mimirUint64(mimirXMRConsolidationMinUnlockedAtomic, uint64OrFallback(c.cfg.XMRConsolidationMinUnlockedAtomic, defaultXMRConsolidationMinUnlockedAtomic)),
		ReserveBps:          c.mimirInt64(mimirXMRConsolidationReserveBps, int64OrFallback(c.cfg.XMRConsolidationReserveBps, defaultXMRConsolidationReserveBps)),
		ChurnLookahead:      c.mimirInt64(mimirXMRConsolidationChurnLookahead, int64OrFallback(c.cfg.XMRConsolidationChurnLookaheadBlocks, defaultXMRConsolidationChurnLookahead)),
		MaxRetries:          c.mimirInt64(mimirXMRConsolidationMaxRetries, int64OrFallback(c.cfg.XMRConsolidationMaxRetries, defaultXMRConsolidationMaxRetries)),
		MaxInputs:           c.mimirInt64(mimirXMRConsolidationMaxInputs, int64OrFallback(c.cfg.XMRConsolidationMaxInputs, defaultXMRConsolidationMaxInputs)),
		MaxLockBps:          c.mimirInt64(mimirXMRConsolidationMaxLockBps, int64OrFallback(c.cfg.XMRConsolidationMaxLockBps, defaultXMRConsolidationMaxLockBps)),
	}
	if settings.CheckIntervalBlocks <= 0 {
		settings.CheckIntervalBlocks = defaultXMRConsolidationCheckIntervalBlocks
	}
	if settings.CooldownBlocks <= 0 {
		settings.CooldownBlocks = defaultXMRConsolidationCooldownBlocks
	}
	if settings.MinUnlockedOutputs <= 0 {
		settings.MinUnlockedOutputs = defaultXMRConsolidationMinUnlockedOutputs
	}
	if settings.TargetOutputs <= 0 {
		settings.TargetOutputs = defaultXMRConsolidationTargetOutputs
	}
	if settings.MinUnlockedAtomic == 0 {
		settings.MinUnlockedAtomic = defaultXMRConsolidationMinUnlockedAtomic
	}
	if settings.ReserveBps <= 0 {
		settings.ReserveBps = defaultXMRConsolidationReserveBps
	}
	if settings.ChurnLookahead <= 0 {
		settings.ChurnLookahead = defaultXMRConsolidationChurnLookahead
	}
	if settings.MaxRetries <= 0 {
		settings.MaxRetries = defaultXMRConsolidationMaxRetries
	}
	if settings.MaxInputs <= 0 {
		settings.MaxInputs = defaultXMRConsolidationMaxInputs
	}
	if settings.MaxInputs > int64(common.MaxXMRSpentOutputRefs) {
		settings.MaxInputs = int64(common.MaxXMRSpentOutputRefs)
	}
	if settings.MaxLockBps <= 0 {
		settings.MaxLockBps = defaultXMRConsolidationMaxLockBps
	}
	if settings.MaxLockBps > 10000 {
		settings.MaxLockBps = 10000
	}
	c.consolidationSettings = settings
	c.lastConsolidationConfig = time.Now()
	c.hasConsolidationConfig = true
	return settings
}

func (c *Client) mimirInt64Raw(key string) (int64, bool) {
	if c.thorchainBridge == nil {
		return 0, false
	}
	value, err := c.thorchainBridge.GetMimir(key)
	if err != nil {
		c.logger.Debug().Err(err).Str("mimir_key", key).Msg("fail to fetch xmr consolidation mimir value")
		return 0, false
	}
	if value < 0 {
		return 0, false
	}
	return value, true
}

func (c *Client) mimirInt64(key string, fallback int64) int64 {
	value, ok := c.mimirInt64Raw(key)
	if !ok {
		return fallback
	}
	if value <= 0 {
		return fallback
	}
	return value
}

func (c *Client) mimirUint64(key string, fallback uint64) uint64 {
	value := c.mimirInt64(key, int64(fallback))
	if value <= 0 {
		return fallback
	}
	return uint64(value)
}

func int64OrFallback(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func uint64OrFallback(value, fallback uint64) uint64 {
	if value == 0 {
		return fallback
	}
	return value
}

func (c *Client) getChurnInterval() int64 {
	consts, err := c.thorchainBridge.GetConstants()
	if err == nil {
		if interval := consts[constants.ChurnInterval.String()]; interval > 0 {
			return interval
		}
	}
	return constants.GetConstantValues(semver.MustParse("0.0.0")).GetInt64Value(constants.ChurnInterval)
}

// CanSkipUnsupportedXMRDestination permits omission of a fresh, unsigned local
// attempt only. It neither completes the on-chain obligation nor deletes any
// signing evidence. The signer must preserve an existing queue entry on replay.
// Admission uses a fixed number of indexed reads, without a sidecar/RPC call or
// a scan of earlier unsupported payouts.
func (c *Client) CanSkipUnsupportedXMRDestination(tx stypes.TxOutItem) (bool, error) {
	if !tx.Chain.Equals(common.XMRChain) || len(tx.Checkpoint) != 0 ||
		!tx.ToAddress.IsChain(common.XMRChain) || common.IsSupportedXMRAddress(tx.ToAddress.String()) ||
		isConsolidationTx(tx) {
		return false, nil
	}
	if c.sidecar == nil || c.tssServer == nil || c.signedTxPayloadStore == nil || c.signedTxPayloadStore.db == nil || c.signerCacheManager == nil {
		return false, errors.New("XMR admission recovery stores are unavailable")
	}
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		return false, err
	}
	if c.signerCacheManager.HasSigned(identity.CacheHash) {
		return false, nil
	}
	store := c.signedTxPayloadStore
	store.mu.Lock()
	defer store.mu.Unlock()
	keys := []string{
		signedTxPayloadKey(txCacheVault(tx, common.XMRChain), identity.CacheHash),
		signedTxPayloadClaimKey(identity.LogicalHash),
		signedTxCommittedLogicalKey(identity.LogicalHash),
		signedTxObservedLogicalKey(identity.LogicalHash),
		signedTxQuarantinedLogicalKey(identity.LogicalHash),
		signedTxBroadcastAttemptKey(identity.LogicalHash),
	}
	for _, alias := range identity.ObserveKeys {
		keys = append(keys, signedTxPayloadObserveKey(alias), signedTxCommittedAliasKey(alias),
			signedTxQuarantinedAliasKey(alias), signedTxObserveEpochKey(alias))
	}
	for _, key := range keys {
		found, readErr := store.db.Has([]byte(key), nil)
		if readErr != nil || found {
			return false, readErr
		}
	}
	return true, nil
}

func (c *Client) SignTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if !tx.Chain.Equals(c.chain) {
		return nil, nil, nil, fmt.Errorf("cannot sign non-xmr tx on monero client: %s", tx.Chain)
	}
	signedTx, checkpoint, observation, err := c.signTx(tx, height)
	if err != nil {
		c.reportXMRMaxGasExceeded(err)
		err = c.postKeysignFailure(tx, height, err)
	}
	return signedTx, checkpoint, observation, err
}

func (c *Client) BroadcastTx(tx stypes.TxOutItem, txBytes []byte) (string, error) {
	var payload signedTxPayload
	if err := json.Unmarshal(txBytes, &payload); err != nil {
		return "", fmt.Errorf("fail to decode monero signed tx payload: %w", err)
	}
	return c.broadcastTx(tx, payload)
}

func (c *Client) broadcastTx(tx stypes.TxOutItem, payload signedTxPayload) (string, error) {
	payload.TxHash = strings.ToLower(strings.TrimSpace(payload.TxHash))
	if payload.TxHash == "" {
		return "", fmt.Errorf("FROST signed tx payload missing tx hash")
	}
	if strings.TrimSpace(payload.TxMetadata) == "" {
		return "", fmt.Errorf("FROST signed tx payload missing tx_hex for broadcast")
	}
	txKeyHash, err := tssp.FrostTxKeyHash(payload.TxKey)
	if err != nil {
		return "", fmt.Errorf("FROST signed tx payload has invalid tx_key: %w", err)
	}
	if !strings.EqualFold(txKeyHash, strings.TrimSpace(payload.Eventuality.TxKeyHash)) {
		return "", fmt.Errorf("FROST signed tx payload tx_key does not match eventuality")
	}
	expectedEventuality, err := tssp.BuildFrostEventuality(
		payload.Eventuality.PlanHash,
		payload.TxHash,
		payload.TxMetadata,
		payload.TxKey,
		payload.FeeAtomic,
	)
	if err != nil {
		return "", fmt.Errorf("FROST signed tx payload has invalid eventuality: %w", err)
	}
	if expectedEventuality != payload.Eventuality {
		return "", fmt.Errorf("FROST signed tx payload does not match eventuality commitment")
	}
	msgID := strings.TrimSpace(payload.MsgID)
	if msgID == "" {
		return "", fmt.Errorf("FROST signed tx payload missing msg_id")
	}
	currentVaultPubKey := strings.TrimSpace(txVaultPubKey(tx).String())
	if currentVaultPubKey == "" {
		return "", fmt.Errorf("FROST signed tx payload missing vault_pub_key_frost")
	}
	cacheVault := strings.TrimSpace(payload.ClaimCacheVault)
	cacheHash := strings.TrimSpace(payload.ClaimCacheHash)
	logicalHash := strings.TrimSpace(payload.ClaimLogicalHash)
	claimOwnerVault := strings.TrimSpace(payload.ClaimOwnerVault)
	vaultPubKey := currentVaultPubKey
	if cacheVault == "" || cacheHash == "" || logicalHash == "" || claimOwnerVault == "" {
		if c.signedTxPayloadStore != nil {
			return "", errors.New("FROST signed tx payload is missing durable claim identity")
		}
		identity, identityErr := xmrSigningCacheIdentity(tx)
		if identityErr != nil {
			return "", identityErr
		}
		cacheVault = txCacheVault(tx, c.GetChain())
		cacheHash = identity.CacheHash
		logicalHash = identity.LogicalHash
		claimOwnerVault = currentVaultPubKey
	} else {
		// A later THORChain reschedule may hand this node the same logical
		// outbound under a different vault. The signed bytes still belong to the
		// vault recorded before FROST, so broadcast against that durable owner.
		vaultPubKey = claimOwnerVault
	}

	if c.sidecar == nil {
		return "", fmt.Errorf("%w: sidecar is not initialized", ErrSidecarNotConfigured)
	}

	broadcast := func() (string, error) {
		resp, broadcastErr := c.sidecar.Broadcast(sidecar.BroadcastReq{
			TxHex:       payload.TxMetadata,
			TxHash:      payload.TxHash,
			TxKey:       payload.TxKey,
			VaultPubKey: vaultPubKey,
			MsgID:       msgID,
		})
		if broadcastErr != nil {
			return "", fmt.Errorf("fail to broadcast FROST tx: %w", broadcastErr)
		}
		if strings.TrimSpace(resp.TxKey) != payload.TxKey {
			return "", fmt.Errorf("sidecar broadcast returned tx_key that differs from FROST quorum result")
		}
		if sidecarHash := strings.TrimSpace(resp.TxHash); sidecarHash != "" && !strings.EqualFold(sidecarHash, payload.TxHash) {
			c.logger.Warn().Str("frost_hash", payload.TxHash).Str("sidecar_hash", sidecarHash).
				Msg("sidecar broadcast returned different tx hash than FROST-validated hash; using FROST hash")
		}
		return payload.TxHash, nil
	}
	txHash := ""
	if c.signedTxPayloadStore != nil {
		txHash, err = c.signedTxPayloadStore.WithBroadcastAuthorization(
			cacheVault,
			cacheHash,
			logicalHash,
			claimOwnerVault,
			payload.TxHash,
			payload.MsgID,
			payload.Eventuality.PlanHash,
			broadcast,
		)
	} else {
		txHash, err = broadcast()
	}
	if err != nil {
		return "", err
	}

	// The signer cache means a chain tx hash has been accepted for broadcast.
	// The pre-broadcast duplicate-FROST guard is the signed payload store, so do
	// not delete that payload here or on spent-ref import. Only authoritative
	// observation may suspend/terminalize its exact broadcast authorization.
	if err := c.signerCacheManager.SetSigned(cacheHash, cacheVault, txHash); err != nil {
		c.logger.Err(err).Msg("fail to set signer cache after FROST broadcast")
	}
	return txHash, nil
}

func (c *Client) signTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if c.sidecar == nil {
		return nil, nil, nil, fmt.Errorf("%w: sidecar is not initialized", ErrSidecarNotConfigured)
	}
	if c.tssServer == nil {
		return nil, nil, nil, errors.New("tss server is not initialized")
	}
	if tx.Coins.IsEmpty() {
		return nil, nil, nil, nil
	}
	identity, err := xmrSigningCacheIdentity(tx)
	if err != nil {
		return nil, nil, nil, err
	}
	if isConsolidationTx(tx) {
		active, found, activeErr := c.signedTxPayloadStore.ActiveClaim(identity.ObserveKeys, txVaultPubKey(tx).String())
		if activeErr != nil {
			return nil, nil, nil, activeErr
		}
		if found {
			identity.CacheHash = active.CacheHash
			identity.LogicalHash = active.LogicalHash
		}
	}
	// The signed payload store is the XMR-specific pre-broadcast retry guard.
	// If FROST already produced a payload, reuse that exact transaction instead
	// of starting another ceremony with a potentially different input set. This
	// must run before signer-cache suppression: BroadcastTx marks signer cache
	// immediately after a broadcast attempt, while the signed payload remains
	// the only durable artifact that can actually rebroadcast the same XMR tx
	// until an authoritative committed observation takes over.
	if signedTx, checkpoint, observation, ok, recoverErr := c.recoverSignedXMRTxPayload(tx, identity); recoverErr != nil {
		return nil, nil, nil, recoverErr
	} else if ok {
		return signedTx, checkpoint, observation, nil
	}
	if c.signerCacheManager.HasSigned(identity.CacheHash) {
		c.logger.Info().Interface("tx", tx).Msg("transaction already signed, ignoring")
		return nil, nil, nil, nil
	}
	signReqCtx, err := c.validateSignTx(tx, height, true)
	if err != nil {
		// Recover signed artifacts above before considering local retirement.
		// Even a reserved claim can have exposed shares without a complete
		// payload, so absence of bytes alone is not proof that this is unsigned.
		// Unknown storage state and all operational errors remain retryable.
		if len(tx.Checkpoint) == 0 && c.signedTxPayloadStore != nil && c.signedTxPayloadStore.db != nil &&
			tx.ToAddress.IsChain(common.XMRChain) && !common.IsSupportedXMRAddress(tx.ToAddress.String()) {
			_, claimed, claimErr := c.signedTxPayloadStore.Claim(identity.LogicalHash)
			if claimErr != nil {
				return nil, nil, nil, claimErr
			}
			if !claimed {
				return nil, nil, nil, fmt.Errorf("%w: %s", types.ErrUnsupportedXMRDestination, tx.ToAddress)
			}
		}
		return nil, nil, nil, err
	}

	policyHeight := height
	members, activeMembers, memberErr := c.vaultSigningMembers(signReqCtx.vaultPubKey, policyHeight)
	if memberErr != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve vault signing members for FROST signing: %w", memberErr)
	}

	// A node whose sidecar holds no threshold share for this vault cannot
	// contribute signing shares, and if elected FROST leader it would consume a
	// coordinator attempt before the party rotates. Stand down proactively;
	// the filtered party can reach threshold without us.
	// An unavailable or malformed key-status response cannot prove signability.
	// Fail before party formation instead of letting a node with an unknown local
	// share state join and veto the round. Bifrost will retry once exact-vault
	// signability is available again; no blame is posted for this local error.
	hasKeys, keysErr := c.sidecarHasVaultKeys(signReqCtx.vaultPubKey)
	if keysErr != nil {
		return nil, nil, nil, fmt.Errorf(
			"refuse XMR FROST keysign while local vault signability is unknown: %w",
			keysErr,
		)
	}
	if !hasKeys {
		c.logger.Warn().
			Str("vault_pubkey", signReqCtx.vaultPubKey.String()).
			Msg("local sidecar holds no FROST keyshare for vault; standing down from keysign")
		return nil, nil, nil, nil
	}
	version, err := c.thorchainBridge.GetThorchainVersion()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve thorchain version: %w", err)
	}

	txInHash := ""
	if !tx.InHash.IsEmpty() {
		txInHash = tx.InHash.String()
	}
	feeRate := uint64(0)
	if tx.GasRate > 0 {
		feeRate = uint64(tx.GasRate)
	}
	maxInputs := maxInputsFromMimir(0)
	if c.thorchainBridge != nil {
		m, mErr := c.thorchainBridge.GetMimir("MaxUTXOsToSpend")
		if mErr != nil {
			c.logger.Err(mErr).Msg("fail to get MaxUTXOsToSpend")
		}
		if m > 0 {
			maxInputs = maxInputsFromMimir(m)
		}
	}
	maxSelectedInputValueAtomic := uint64(0)
	if isConsolidationTx(tx) {
		consolidationSettings := c.loadConsolidationSettings()
		consolidationMaxInputs := maxInputsFromMimir(consolidationSettings.MaxInputs)
		if consolidationMaxInputs < maxInputs {
			maxInputs = consolidationMaxInputs
		}
		balance, balanceErr := c.sidecar.Balance(signReqCtx.vaultPubKey.String())
		if balanceErr != nil {
			return nil, nil, nil, fmt.Errorf("fail to query xmr consolidation spendable balance: %w", balanceErr)
		}
		maxSelectedInputValueAtomic = xmrConsolidationMaxLockAtomic(spendableBalanceAtomic(balance), consolidationSettings.MaxLockBps)
		if maxSelectedInputValueAtomic == 0 {
			return nil, nil, nil, fmt.Errorf("xmr consolidation selected-input budget is zero")
		}
	}
	dustThreshold, err := thorchainAmountToAtomic(common.XMRChain.DustThreshold())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to convert xmr dust threshold to atomic units: %w", err)
	}

	var requiredInputs []tssp.FrostSpentOutputRef
	if len(tx.Checkpoint) > 0 {
		var ckpt signCheckpoint
		ckptErr := json.Unmarshal(tx.Checkpoint, &ckpt)
		if ckptErr != nil {
			return nil, nil, nil, fmt.Errorf("fail to unmarshal xmr sign checkpoint: %w", ckptErr)
		}
		for _, ref := range ckpt.SpentOutputRefs {
			requiredInputs = append(requiredInputs, tssp.FrostSpentOutputRef{
				TxHash:      ref.TxHash,
				OutputIndex: ref.OutputIndex,
				KeyImage:    ref.KeyImage,
				SpendTxHash: ref.SpendTxHash,
			})
		}
		c.logger.Info().Int("required_inputs", len(requiredInputs)).Msg("using checkpointed inputs for xmr signing retry")
	}
	frostReq := tssp.FrostKeySignRequest{
		VaultPubKey:                 signReqCtx.vaultPubKey.String(),
		Network:                     strings.ToLower(strings.TrimSpace(c.cfg.SignerExpectedNetwork)),
		ToAddress:                   signReqCtx.toAddress,
		AmountAtomic:                signReqCtx.amountAtomic,
		Memo:                        stypes.XMRWireMemo(tx.Memo, tx.OriginalMemo),
		FeeRate:                     feeRate,
		SignerPubKeys:               members,
		ActiveSignerPubKeys:         activeMembers,
		BlockHeight:                 policyHeight,
		Version:                     version.String(),
		TxInHash:                    txInHash,
		RequestID:                   tx.SigningID,
		RequiredInputs:              requiredInputs,
		MaxInputs:                   maxInputs,
		DustThreshold:               dustThreshold,
		MaxSelectedInputValueAtomic: maxSelectedInputValueAtomic,
		MaxGasAtomic:                signReqCtx.maxGasAtomic,
	}

	// ClearSessions is sidecar-global, so only one XMR FROST ceremony may be
	// active on this sidecar while Bifrost may need to clean up a failed round.
	c.sidecarSignMu.Lock()
	defer c.sidecarSignMu.Unlock()
	if isConsolidationTx(tx) {
		active, found, activeErr := c.signedTxPayloadStore.ActiveClaim(identity.ObserveKeys, txVaultPubKey(tx).String())
		if activeErr != nil {
			return nil, nil, nil, activeErr
		}
		if found {
			identity.CacheHash = active.CacheHash
			identity.LogicalHash = active.LogicalHash
		}
	}
	// Recovery is checked again under the sidecar-global signing lock. Two
	// concurrent reschedules can otherwise both miss the ownership record
	// before the first FROST result is durably persisted, then serialize only
	// the ceremonies and produce signed transactions from different vaults.
	if signedTx, checkpoint, observation, ok, recoverErr := c.recoverSignedXMRTxPayload(tx, identity); recoverErr != nil {
		return nil, nil, nil, recoverErr
	} else if ok {
		return signedTx, checkpoint, observation, nil
	}
	if c.signerCacheManager.HasSigned(identity.CacheHash) {
		c.logger.Info().Interface("tx", tx).Msg("transaction already signed, ignoring")
		return nil, nil, nil, nil
	}
	if existingClaim, found, claimErr := c.signedTxPayloadStore.Claim(identity.LogicalHash); claimErr != nil {
		return nil, nil, nil, claimErr
	} else if found && existingClaim.State == signedTxClaimReserved {
		// A durable Go reservation means a ceremony may already have produced a
		// transaction at another party. Never enter FROST a second time; only a
		// committed fulfillment or an explicit operator recovery may resolve it.
		return nil, nil, nil, fmt.Errorf(
			"%w: reserved xmr signing attempt has no persisted payload; refusing a second ceremony",
			errXMRSigningClaimConflict,
		)
	}
	expectedMsgID, err := tssp.FrostKeySignMsgID(frostReq)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("derive xmr FROST message id before reservation: %w", err)
	}
	planHash, err := tssp.FrostPlanHash(frostReq, expectedMsgID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("derive xmr FROST plan hash before reservation: %w", err)
	}
	observeEpoch, err := c.signedTxPayloadStore.SnapshotObserveEpochs(identity.ObserveKeys)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("snapshot xmr observation aliases before reservation: %w", err)
	}
	txOutJSON, err := encodeDurableXMRClaimTxOut(tx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encode xmr txout for durable recovery: %w", err)
	}
	cacheVault := txCacheVault(tx, c.GetChain())
	claimPlan := signedTxPayloadClaimPlan{
		CacheVault:   cacheVault,
		CacheHash:    identity.CacheHash,
		LogicalHash:  identity.LogicalHash,
		OwnerVault:   txVaultPubKey(tx).String(),
		MsgID:        expectedMsgID,
		PlanHash:     planHash,
		ObserveKeys:  identity.ObserveKeys,
		ObserveEpoch: observeEpoch,
		TxOutJSON:    txOutJSON,
	}
	// Delay reservation until go-tss has selected this node into the actual
	// signing party. Stand-by nodes that time out waiting for result gossip must
	// not leave an unresolvable reserved-only claim. go-tss still invokes this
	// barrier before an active node can call the sidecar, and invokes it for a
	// stand-by node only after accepting a quorum-verified successful result.
	frostReq.ReserveSigningClaim = func() error {
		if reserveErr := c.signedTxPayloadStore.Reserve(claimPlan); reserveErr != nil {
			return fmt.Errorf("reserve logical xmr outbound before FROST: %w", reserveErr)
		}
		return nil
	}
	frostReq.ReleaseSigningClaim = func() error {
		return c.signedTxPayloadStore.ReleaseReserved(claimPlan)
	}
	resp, err := c.tssServer.FrostKeySign(frostReq, sidecar.NewFrostAdapterWithStop(c.sidecar, c.stopchan))
	if err != nil {
		if errors.Is(err, errXMRSigningClaimObserved) || errors.Is(err, errXMRSigningClaimCommitted) {
			c.logger.Info().Interface("tx", tx).Msg("xmr signing claim already committed, ignoring")
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("fail to execute FROST keysign: %w", err)
	}
	if resp.Status == gotsscommon.NA && strings.TrimSpace(resp.TxHex) == "" && strings.TrimSpace(resp.Blame.FailReason) == "" && len(resp.Blame.BlameNodes) == 0 {
		c.logger.Info().Str("vault_pubkey", signReqCtx.vaultPubKey.String()).Msg("local node is not selected for FROST keysign party; standing down")
		return nil, nil, nil, nil
	}
	if resp.Status != gotsscommon.Success {
		signErr := fmt.Errorf("FROST keysign returned status %d: %s", resp.Status, resp.Blame.FailReason)
		// MaxGas overages are deterministic local policy failures, not signer
		// misbehavior. They are still terminal because the fee is only known
		// after the signature-share boundary, but carry no peer blame.
		if maxGasErr, ok := xmrMaxGasExceededFromFrostResponse(resp, signReqCtx.maxGasAtomic); ok {
			return nil, nil, nil, newTerminalFrostResultError(maxGasErr)
		}
		thorchainBlame := frostBlameToThorchainBlame(resp.Blame)
		if len(resp.Blame.BlameNodes) > 0 {
			return nil, nil, nil, bifrosttss.NewKeysignError(thorchainBlame)
		}
		// Preserve a recognized phase even when the failure cannot attribute a
		// culprit. In particular, frost_share is locally safety-terminal because
		// a usable share may already have escaped. postKeysignFailure still
		// suppresses the on-chain report when BlameNodes is empty.
		if round, roundErr := ttypes.NormalizeTssKeysignRound(thorchainBlame.Round); roundErr == nil && round != "" {
			thorchainBlame.Round = round
			return nil, nil, nil, bifrosttss.NewKeysignError(thorchainBlame)
		}
		return nil, nil, nil, signErr
	}
	// Production go-tss reserves after party selection and before any sidecar
	// share RPC. Keep the client boundary fail-safe for alternate implementations
	// and tests: a successful result must still acquire the exact claim before
	// the signed bytes are committed.
	if err = c.signedTxPayloadStore.Reserve(claimPlan); err != nil {
		if errors.Is(err, errXMRSigningClaimObserved) || errors.Is(err, errXMRSigningClaimCommitted) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, newTerminalFrostResultError(fmt.Errorf("reserve successful xmr FROST result before persistence: %w", err))
	}
	msgID := strings.TrimSpace(resp.MsgID)
	if msgID == "" {
		return nil, nil, nil, newTerminalFrostResultError(errors.New("FROST keysign returned empty msg_id"))
	}
	if !strings.EqualFold(msgID, expectedMsgID) {
		return nil, nil, nil, newTerminalFrostResultError(errors.New("FROST keysign returned msg_id for a different reserved plan"))
	}
	// Treat the TSS response as untrusted until Bifrost independently verifies
	// the actual fee against the scheduled cap. This is the local guard for
	// signers that predate sidecar-side MaxGas enforcement.
	if maxGasErr := enforceXMRMaxGas(resp.FeeAtomic, signReqCtx.maxGasAtomic); maxGasErr != nil {
		return nil, nil, nil, newTerminalFrostResultError(maxGasErr)
	}
	txKey := strings.ToLower(strings.TrimSpace(resp.TxKey))
	if validateErr := tssp.ValidateFrostEventuality(
		frostReq,
		msgID,
		resp.TxHash,
		resp.TxHex,
		txKey,
		resp.FeeAtomic,
		resp.Eventuality,
	); validateErr != nil {
		return nil, nil, nil, newTerminalFrostResultError(fmt.Errorf("invalid FROST eventuality: %w", validateErr))
	}
	if validateErr := validateFixedHex(txKey, 32, "xmr tx key"); validateErr != nil {
		return nil, nil, nil, newTerminalFrostResultError(validateErr)
	}
	var localSpentRefs []sidecar.SpentRef
	for _, ref := range resp.SpentOutputRefs {
		localSpentRefs = append(localSpentRefs, sidecar.SpentRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		})
	}

	observation := c.buildOutboundObservation(tx, resp.TxHash, txKey, resp.FeeAtomic, localSpentRefs)
	payload := signedTxPayload{
		TxHash:           resp.TxHash,
		TxMetadata:       resp.TxHex,
		TxKey:            txKey,
		FeeAtomic:        resp.FeeAtomic,
		MsgID:            msgID,
		Eventuality:      resp.Eventuality,
		Broadcasted:      false,
		SpentOutputRefs:  localSpentRefs,
		Observation:      observation,
		ClaimCacheVault:  cacheVault,
		ClaimCacheHash:   identity.CacheHash,
		ClaimLogicalHash: identity.LogicalHash,
		ClaimOwnerVault:  txVaultPubKey(tx).String(),
	}
	signedTx, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, nil, newTerminalFrostResultError(fmt.Errorf("fail to encode FROST signed tx payload: %w", err))
	}
	ckptBytes, ckptErr := json.Marshal(signCheckpoint{SpentOutputRefs: localSpentRefs})
	if ckptErr != nil {
		c.logger.Error().Err(ckptErr).Msg("fail to marshal xmr sign checkpoint")
	}
	// Persist the complete signed artifact as the pre-broadcast retry guard.
	if err := c.signedTxPayloadStore.Set(claimPlan, payload.TxHash, signedTx); err != nil {
		if errors.Is(err, errXMRSigningClaimObserved) || errors.Is(err, errXMRSigningClaimCommitted) {
			c.logger.Warn().
				Str("tx_hash", payload.TxHash).
				Msg("retaining late xmr FROST result only in the sidecar because the logical outbound is already committed")
			return nil, nil, nil, nil
		}
		return nil, nil, nil, newTerminalFrostResultError(fmt.Errorf("fail to persist xmr FROST signed payload: %w", err))
	}
	return signedTx, ckptBytes, observation, nil
}

// recoverSignedXMRTxPayload returns a previously persisted FROST result for the
// same outbound. This closes the retry/restart gap between a successful FROST
// response and local broadcast without allowing a second FROST ceremony.
func (c *Client) recoverSignedXMRTxPayload(
	tx stypes.TxOutItem,
	identity xmrSigningIdentity,
) ([]byte, []byte, *stypes.TxInItem, bool, error) {
	cacheVault := txCacheVault(tx, c.GetChain())
	signedTx, ok, err := c.signedTxPayloadStore.GetOwned(cacheVault, identity.CacheHash, identity.LogicalHash)
	if errors.Is(err, errXMRSigningClaimConflict) {
		// TxOut rescheduling may move the same semantic outbound to a new vault.
		// If the old vault already has a complete signed artifact, recover that
		// exact transaction instead of authorizing the replacement vault to sign.
		claim, found, claimErr := c.signedTxPayloadStore.Claim(identity.LogicalHash)
		if claimErr != nil {
			return nil, nil, nil, false, claimErr
		}
		if found && claim.State == signedTxClaimSigned && strings.EqualFold(claim.CacheHash, identity.CacheHash) {
			signedTx, ok, err = c.signedTxPayloadStore.GetOwned(claim.CacheVault, claim.CacheHash, claim.LogicalHash)
		}
	}
	if err != nil {
		if errors.Is(err, errXMRSigningClaimObserved) || errors.Is(err, errXMRSigningClaimCommitted) {
			c.logger.Info().Interface("tx", tx).Msg("logical xmr outbound is already committed, suppressing retry")
			return nil, nil, nil, true, nil
		}
		return nil, nil, nil, false, err
	}
	if !ok {
		return nil, nil, nil, false, nil
	}

	payload, err := decodeStoredSignedXMRTxPayload(signedTx)
	if err != nil {
		return nil, nil, nil, false, err
	}
	ckptBytes, ckptErr := json.Marshal(signCheckpoint{SpentOutputRefs: payload.SpentOutputRefs})
	if ckptErr != nil {
		return nil, nil, nil, false, fmt.Errorf("fail to marshal cached xmr sign checkpoint: %w", ckptErr)
	}
	observation := payload.Observation
	if observation == nil {
		observation = c.buildOutboundObservation(tx, payload.TxHash, payload.TxKey, payload.FeeAtomic, payload.SpentOutputRefs)
	} else {
		observation = cloneXMRObservation(observation)
		if !strings.EqualFold(strings.TrimSpace(observation.Tx), payload.TxHash) {
			return nil, nil, nil, false, fmt.Errorf("cached xmr observation transaction hash does not match payload")
		}
	}
	c.logger.Info().Str("tx_hash", payload.TxHash).Interface("tx", tx).Msg("reusing cached xmr FROST signed payload")
	return signedTx, ckptBytes, observation, true, nil
}

func cloneXMRObservation(in *stypes.TxInItem) *stypes.TxInItem {
	if in == nil {
		return nil
	}
	out := *in
	out.Coins = append(common.Coins(nil), in.Coins...)
	out.Gas = append(common.Gas(nil), in.Gas...)
	out.SpentOutputRefs = make([]*common.OutputRef, 0, len(in.SpentOutputRefs))
	for _, ref := range in.SpentOutputRefs {
		if ref == nil {
			continue
		}
		copyRef := *ref
		out.SpentOutputRefs = append(out.SpentOutputRefs, &copyRef)
	}
	return &out
}

// decodeStoredSignedXMRTxPayload validates the fields needed to rebroadcast and
// observe a cached signed transaction. Corrupt cache data fails closed instead
// of falling through to a new FROST signing attempt.
func decodeStoredSignedXMRTxPayload(signedTx []byte) (signedTxPayload, error) {
	var payload signedTxPayload
	if err := json.Unmarshal(signedTx, &payload); err != nil {
		return signedTxPayload{}, fmt.Errorf("fail to decode cached xmr signed tx payload: %w", err)
	}
	if strings.TrimSpace(payload.TxHash) == "" {
		return signedTxPayload{}, fmt.Errorf("cached xmr signed tx payload missing tx hash")
	}
	payload.TxHash = strings.ToLower(strings.TrimSpace(payload.TxHash))
	if err := validateFixedHex(payload.TxHash, 32, "cached xmr tx hash"); err != nil {
		return signedTxPayload{}, err
	}
	payload.TxMetadata = strings.ToLower(strings.TrimSpace(payload.TxMetadata))
	if payload.TxMetadata == "" {
		return signedTxPayload{}, fmt.Errorf("cached xmr signed tx payload missing tx_hex")
	}
	if _, err := hex.DecodeString(payload.TxMetadata); err != nil {
		return signedTxPayload{}, fmt.Errorf("cached xmr signed tx payload tx_hex is not valid hex")
	}
	if strings.TrimSpace(payload.MsgID) == "" {
		return signedTxPayload{}, fmt.Errorf("cached xmr signed tx payload missing msg_id")
	}
	payload.TxKey = strings.ToLower(strings.TrimSpace(payload.TxKey))
	if err := validateFixedHex(payload.TxKey, 32, "cached xmr tx key"); err != nil {
		return signedTxPayload{}, err
	}
	return payload, nil
}

func (c *Client) validateSignTx(tx stypes.TxOutItem, thorchainHeight int64, requireThorchainHeight bool) (validatedSignRequest, error) {
	if tx.ToAddress.IsEmpty() {
		return validatedSignRequest{}, fmt.Errorf("to address is empty")
	}
	// The shared external-recipient policy rejects unsupported address forms
	// before they enter FROST signing.
	if !isValidXMROutboundDestination(c.chain, tx.ToAddress) {
		return validatedSignRequest{}, fmt.Errorf("to address %s is not a valid %s address", tx.ToAddress.String(), c.chain.String())
	}
	if requireThorchainHeight && thorchainHeight <= 0 {
		return validatedSignRequest{}, fmt.Errorf("thorchain height must be greater than zero")
	}

	memoForParsing := tx.GetMemo()
	if strings.TrimSpace(stypes.XMRWireMemo(tx.Memo, tx.OriginalMemo)) == "" {
		return validatedSignRequest{}, errors.New("new xmr payout requires its original memo")
	}
	if memoForParsing != "" {
		parsedMemo, err := memo.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return validatedSignRequest{}, fmt.Errorf("fail to parse outbound memo(%s): %w", memoForParsing, err)
		}
		if parsedMemo.IsInbound() {
			return validatedSignRequest{}, fmt.Errorf("inbound memo should not be used for outbound tx")
		}
	}

	if len(tx.Coins) != 1 {
		return validatedSignRequest{}, fmt.Errorf("xmr signing requires exactly one coin, got %d", len(tx.Coins))
	}
	xmrCoin := tx.Coins.GetCoin(common.XMRAsset)
	if xmrCoin.IsEmpty() {
		return validatedSignRequest{}, fmt.Errorf("xmr tx does not contain XMR coin")
	}
	if xmrCoin.Amount.IsZero() {
		return validatedSignRequest{}, fmt.Errorf("xmr outbound amount is zero")
	}
	amountAtomic, err := thorchainAmountToAtomic(xmrCoin.Amount)
	if err != nil {
		return validatedSignRequest{}, fmt.Errorf("fail to convert xmr outbound amount for signing: %w", err)
	}
	if amountAtomic == 0 {
		return validatedSignRequest{}, fmt.Errorf("xmr outbound amount converted to zero atomic units")
	}
	maxGasAtomic, err := maxGasToAtomic(tx.MaxGas)
	if err != nil {
		return validatedSignRequest{}, fmt.Errorf("fail to convert xmr max gas for signing: %w", err)
	}

	vaultPubKey := txVaultPubKey(tx)
	if vaultPubKey.IsEmpty() {
		return validatedSignRequest{}, fmt.Errorf("vault pubkey is empty")
	}
	fromAddress := strings.TrimSpace(c.deriveVaultAddress(vaultPubKey))
	if fromAddress == "" {
		return validatedSignRequest{}, fmt.Errorf("fail to derive monero vault address from pubkey %s", vaultPubKey.String())
	}
	from, err := common.NewAddress(fromAddress)
	if err != nil {
		return validatedSignRequest{}, fmt.Errorf("derived monero vault address %s is invalid: %w", fromAddress, err)
	}
	if !c.chain.IsValidAddress(from) {
		return validatedSignRequest{}, fmt.Errorf("derived monero vault address %s is not on %s", fromAddress, c.chain.String())
	}

	if err = c.ensureVaultBalance(vaultPubKey, amountAtomic, maxGasAtomic); err != nil {
		return validatedSignRequest{}, err
	}

	return validatedSignRequest{
		vaultPubKey:  vaultPubKey,
		fromAddress:  fromAddress,
		toAddress:    tx.ToAddress.String(),
		amountAtomic: amountAtomic,
		maxGasAtomic: maxGasAtomic,
	}, nil
}

func isValidXMROutboundDestination(chain common.Chain, address common.Address) bool {
	if !chain.Equals(common.XMRChain) {
		return chain.IsValidAddress(address)
	}
	return common.IsSupportedXMRAddress(address.String())
}

func (c *Client) ensureVaultBalance(vaultPubKey common.PubKey, amountAtomic, maxGasAtomic uint64) error {
	if c.sidecar == nil {
		return nil
	}

	if err := c.ensureVaultRegistered(vaultPubKey); err != nil {
		return err
	}

	balance, err := c.sidecar.Balance(vaultPubKey.String())
	if err != nil {
		return fmt.Errorf("fail to verify xmr vault balance for %s: %w", vaultPubKey.String(), err)
	}
	availableAtomic := spendableBalanceAtomic(balance)

	requiredAtomic := amountAtomic
	if maxGasAtomic > 0 {
		if requiredAtomic > (^uint64(0) - maxGasAtomic) {
			return fmt.Errorf("xmr outbound amount overflows uint64 when max gas is applied")
		}
		requiredAtomic += maxGasAtomic
	}

	if availableAtomic < requiredAtomic {
		totalAtomic := balance.Balance
		lockedAtomic := totalAtomic - availableAtomic
		if lockedAtomic > 0 {
			return fmt.Errorf(
				"insufficient xmr balance for signing: available=%d required=%d (total=%d locked=%d, outputs may need 10 confirmations)",
				availableAtomic, requiredAtomic, totalAtomic, lockedAtomic,
			)
		}
		return fmt.Errorf(
			"insufficient xmr balance for signing: available=%d required=%d (amount=%d max_gas=%d)",
			availableAtomic, requiredAtomic, amountAtomic, maxGasAtomic,
		)
	}

	return nil
}

func spendableBalanceAtomic(balance sidecar.Balance) uint64 {
	availableAtomic := balance.UnlockedBalance
	if availableAtomic > balance.Balance {
		availableAtomic = balance.Balance
	}
	if balance.SpendableBalance != nil {
		availableAtomic = *balance.SpendableBalance
		if availableAtomic > balance.Balance {
			availableAtomic = balance.Balance
		}
	}
	return availableAtomic
}

func maxInputsFromMimir(mimirValue int64) int64 {
	maxInputs := int64(15)
	if mimirValue > 0 {
		maxInputs = mimirValue
	}
	if maxInputs > int64(common.MaxXMRSpentOutputRefs) {
		return int64(common.MaxXMRSpentOutputRefs)
	}
	return maxInputs
}

func isConsolidationTx(tx stypes.TxOutItem) bool {
	return tx.Chain.Equals(common.XMRChain) &&
		strings.EqualFold(strings.TrimSpace(tx.Memo), memo.NewConsolidateMemo().String())
}

func maxGasToAtomic(maxGas common.Gas) (uint64, error) {
	if len(maxGas) == 0 {
		return 0, nil
	}
	xmrGasCoin := common.Coins(maxGas).GetCoin(common.XMRAsset)
	if xmrGasCoin.IsEmpty() || xmrGasCoin.Amount.IsZero() {
		return 0, nil
	}
	return thorchainAmountToAtomic(xmrGasCoin.Amount)
}

type xmrMaxGasExceededError struct {
	actualFeeAtomic uint64
	maxGasAtomic    uint64
}

func (e xmrMaxGasExceededError) Error() string {
	return fmt.Sprintf(
		"%s actual_fee_atomic=%d max_gas_atomic=%d",
		tssp.FrostFailReasonXMRMaxGasExceeded,
		e.actualFeeAtomic,
		e.maxGasAtomic,
	)
}

// Unwrap preserves a terminal FROST phase for signer retry safety even when the
// fee error is returned bare (for example straight out of enforceXMRMaxGas).
// The fee is only known after the signature-share boundary, so rescheduling to
// another vault is unsafe. The embedded blame has no nodes, so it cannot produce
// an on-chain failure vote or slash; it only prevents rescheduling.
func (e xmrMaxGasExceededError) Unwrap() error {
	return bifrosttss.NewKeysignError(ttypes.Blame{
		FailReason: e.Error(),
		Round:      tssMessages.FROSTKEYSIGNRESULT,
	})
}

// terminalFrostResultError keeps the original local failure discoverable while
// also exposing the signature-exposure boundary to the generic signer retry
// policy. Its synthesized KeysignError intentionally has no blame nodes: local
// validation, durability, and fee-policy failures are not evidence of peer
// misbehavior and must never produce an on-chain blame transaction.
type terminalFrostResultError struct {
	cause   error
	keysign bifrosttss.KeysignError
}

func newTerminalFrostResultError(cause error) error {
	if cause == nil {
		return nil
	}
	return terminalFrostResultError{
		cause: cause,
		keysign: bifrosttss.NewKeysignError(ttypes.Blame{
			FailReason: cause.Error(),
			Round:      tssMessages.FROSTKEYSIGNRESULT,
		}),
	}
}

func (e terminalFrostResultError) Error() string {
	return e.cause.Error()
}

func (e terminalFrostResultError) Unwrap() []error {
	return []error{e.cause, e.keysign}
}

// enforceXMRMaxGas is the Bifrost-side safety check for older or mismatched
// signers. The sidecar should reject over-cap transactions during planning,
// but Bifrost must still refuse any signed result whose actual fee exceeds the
// scheduled MaxGas.
func enforceXMRMaxGas(actualFeeAtomic, maxGasAtomic uint64) error {
	if maxGasAtomic == 0 || actualFeeAtomic <= maxGasAtomic {
		return nil
	}
	return xmrMaxGasExceededError{
		actualFeeAtomic: actualFeeAtomic,
		maxGasAtomic:    maxGasAtomic,
	}
}

// xmrMaxGasExceededFromFrostResponse converts the stable FROST blame prefix
// back into the local typed error. MaxGas overages are deterministic fee-policy
// failures, so they should not be turned into signer-blame transactions.
func xmrMaxGasExceededFromFrostResponse(resp tssp.FrostKeySignResponse, maxGasAtomic uint64) (error, bool) {
	reason := strings.TrimSpace(resp.Blame.FailReason)
	if reason == "" || !strings.Contains(reason, tssp.FrostFailReasonXMRMaxGasExceeded) {
		return nil, false
	}

	actualFeeAtomic := resp.FeeAtomic
	if parsed, ok := parseUintReasonField(reason, "actual_fee_atomic"); ok {
		actualFeeAtomic = parsed
	}
	if parsed, ok := parseUintReasonField(reason, "max_gas_atomic"); ok {
		maxGasAtomic = parsed
	}
	return xmrMaxGasExceededError{
		actualFeeAtomic: actualFeeAtomic,
		maxGasAtomic:    maxGasAtomic,
	}, true
}

// parseUintReasonField accepts the simple key=value fields emitted in FROST
// blame text. It intentionally stays narrow so arbitrary signer error strings
// do not become part of the protocol surface.
func parseUintReasonField(reason, key string) (uint64, bool) {
	for _, field := range strings.Fields(reason) {
		name, value, ok := strings.Cut(field, "=")
		if !ok || name != key {
			continue
		}
		parsed, err := strconv.ParseUint(strings.Trim(value, ",;"), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func fallbackOutboundObservationFee(scanner *Scanner) uint64 {
	if scanner == nil {
		return 0
	}

	txSize, feeRate := scanner.GetNetworkFee()
	if txSize == 0 || feeRate == 0 {
		return 0
	}

	totalAtomic := uint64(0)
	if feeRate > 0 && txSize > math.MaxUint64/feeRate {
		totalAtomic = math.MaxUint64
	} else {
		totalAtomic = txSize * feeRate
	}

	fee := totalFeeAtomicToThorchain(totalAtomic)
	if fee == 0 && totalAtomic > 0 {
		return 1
	}
	return fee
}

func (c *Client) buildOutboundObservation(tx stypes.TxOutItem, txHash, txKey string, feeAtomic uint64, spentRefs []sidecar.SpentRef) *stypes.TxInItem {
	vaultPubKey := txVaultPubKey(tx)
	from := c.deriveVaultAddress(vaultPubKey)
	height := int64(0)
	if c.scanner != nil {
		height = c.scanner.CurrentHeight()
	}
	if from == "" || tx.ToAddress.IsEmpty() {
		return nil
	}

	fee := feeAtomic / moneroAtomicToThorchain
	if fee == 0 && c.scanner != nil {
		fee = fallbackOutboundObservationFee(c.scanner)
	}
	coins := xmrObservedCoinsForSignedOutbound(tx, feeAtomic)
	item := stypes.NewTxInItem(
		height,
		txHash,
		tx.GetMemo(),
		from,
		tx.ToAddress.String(),
		coins,
		common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(fee))},
		vaultPubKey,
		"",
		"",
		nil,
	)
	if item != nil && len(spentRefs) > 0 {
		refs := make([]*common.OutputRef, len(spentRefs))
		for i, ref := range spentRefs {
			refs[i] = &common.OutputRef{
				TxHash:      ref.TxHash,
				OutputIndex: ref.OutputIndex,
				KeyImage:    ref.KeyImage,
				SpendTxHash: txHash,
			}
		}
		item.SpentOutputRefs = refs
	}
	if item != nil {
		item.TxKey = txKey
	}
	return item
}

// xmrObservedCoinsForSignedOutbound mirrors the signer-side migration sweep
// rule: XMR migrations schedule amount + MaxGas, and the sidecar sends any
// unused MaxGas to the destination vault instead of leaving it behind in the
// source vault. The synthetic Bifrost observation must therefore credit the
// actual migrated amount for MIGRATE only; normal outbound observations keep
// the scheduled coin amount.
func xmrObservedCoinsForSignedOutbound(tx stypes.TxOutItem, feeAtomic uint64) common.Coins {
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, tx.GetMemo())
	if err != nil || !parsedMemo.IsType(memo.TxMigrate) {
		return tx.Coins
	}

	xmrCoin := tx.Coins.GetCoin(common.XMRAsset)
	if xmrCoin.IsEmpty() {
		return tx.Coins
	}
	maxGasAtomic, err := maxGasToAtomic(tx.MaxGas)
	if err != nil || maxGasAtomic <= feeAtomic {
		return tx.Coins
	}

	unusedMaxGasThorchain := (maxGasAtomic - feeAtomic) / moneroAtomicToThorchain
	if unusedMaxGasThorchain == 0 {
		return tx.Coins
	}
	return common.NewCoins(common.NewCoin(
		common.XMRAsset,
		xmrCoin.Amount.Add(cosmos.NewUint(unusedMaxGasThorchain)),
	))
}

func (c *Client) ExportKeyshare(groupKey common.PubKey) ([]byte, error) {
	if c.sidecar == nil {
		return nil, fmt.Errorf("sidecar not configured")
	}
	hexKey, err := c.sidecar.ExportKeys(groupKey.String())
	if err != nil {
		return nil, fmt.Errorf("fail to export frost keys for group key %s: %w", groupKey.String(), err)
	}
	return []byte(hexKey), nil
}

func (c *Client) RestoreKeyshare(vaultPubKey common.PubKey, keyBlob []byte) error {
	return c.restoreKeyshareContext(context.Background(), vaultPubKey, keyBlob)
}

func (c *Client) restoreKeyshareContext(ctx context.Context, vaultPubKey common.PubKey, keyBlob []byte) error {
	if c.sidecar == nil {
		return fmt.Errorf("sidecar not configured")
	}
	restored, err := c.sidecar.RestoreKeysContext(ctx, vaultPubKey.String(), keyBlob)
	if err != nil {
		return err
	}
	if !restored {
		return fmt.Errorf("sidecar returned an unverified restore result for vault %s", vaultPubKey)
	}
	signable, statusErr := c.sidecarHasVaultKeysContext(ctx, vaultPubKey)
	if statusErr != nil {
		return fmt.Errorf("verify restored keyshare signability: %w", statusErr)
	}
	if !signable {
		return fmt.Errorf("restored keyshare for vault %s is not signable", vaultPubKey)
	}
	c.logger.Info().Str("vault", vaultPubKey.String()).Msg("restored and verified FROST keyshare from on-chain backup")
	return nil
}

// ApplySpentOutputRefs imports XMR spent refs and outbound metadata from an
// attestation into the sidecar without creating a local attestation. A verified
// outbound also seeds the signer cache so this node does not sign the same
// outbound again while waiting for scanner observation.
//
// Callers that do not need the final outcome can use this fire-and-forget
// wrapper; the bounded retry continues in the background when appropriate.
func (c *Client) ApplySpentOutputRefs(tx common.ObservedTx) {
	c.applySpentOutputRefs(types.SpentOutputRefsImport{
		Tx:       tx,
		WireMemo: tx.Tx.Memo,
	}, nil)
}

// ApplySpentOutputRefsWithOutcome reports the import's final outcome exactly
// once: true when the sidecar confirmed the refs, immediately or on a later
// bounded retry; false when the import failed terminally, exhausted its
// retries, or could not be queued. Vault-certified requests authenticate the
// exact WireMemo. ChainCommitted internal transactions attach their committed
// pairing proof; other committed transactions remain accounting-only imports.
func (c *Client) ApplySpentOutputRefsWithOutcome(request types.SpentOutputRefsImport, onOutcome func(applied bool)) {
	c.applySpentOutputRefs(request, onOutcome)
}

// SetSpentOutputRefsGuard connects Observer's durable admission before startup.
// All import paths share it, including local signing when Observer is wired.
func (c *Client) SetSpentOutputRefsGuard(guard types.SpentOutputRefsGuard) {
	c.spentRefsGuardMu.Lock()
	defer c.spentRefsGuardMu.Unlock()
	c.spentRefsGuard = guard
}

func (c *Client) spentOutputRefsAdmission(ctx context.Context, request types.SpentOutputRefsImport) func() (func(), bool) {
	c.spentRefsGuardMu.RLock()
	guard := c.spentRefsGuard
	c.spentRefsGuardMu.RUnlock()
	return func() (func(), bool) {
		if ctx.Err() != nil {
			return nil, false
		}
		select {
		case <-c.stopchan:
			return nil, false
		default:
		}
		if guard == nil {
			// Only standalone local signing can run without Observer. Imported
			// consensus or peer evidence must never bypass durable admission.
			if request.ChainCommitted || request.ReplaySource {
				return nil, false
			}
			return func() {}, true
		}
		release, ok := guard(request.Tx)
		if !ok || release == nil {
			return nil, false
		}
		if ctx.Err() != nil {
			release()
			return nil, false
		}
		select {
		case <-c.stopchan:
			release()
			return nil, false
		default:
		}
		return release, true
	}
}

// applySpentOutputRefs centralizes the first attempt and final-outcome callback
// so observer conflict bookkeeping can wait for retry exhaustion or success.
func (c *Client) applySpentOutputRefs(
	request types.SpentOutputRefsImport,
	onOutcome func(applied bool),
) bool {
	tx := request.Tx
	applied, retry := c.applySpentOutputRefsOnce(request)
	if !retry {
		if onOutcome != nil {
			onOutcome(applied)
		}
		return applied
	}

	interval := c.spentRefsRetryInterval
	if interval <= 0 {
		interval = constants.ThorchainBlockTime
	}
	maxAttempts := c.spentRefsRetryMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultXMRSpentRefsRetryMaxAttempts
	}

	c.logger.Info().
		Str("tx_hash", tx.Tx.ID.String()).
		Str("vault", tx.ObservedPubKey.String()).
		Dur("interval", interval).
		Int("max_attempts", maxAttempts).
		Msg("queued xmr spent output refs retry")

	// The first attempt has already run synchronously. Retry asynchronously so
	// callers that still need to broadcast the tx, such as local
	// sign/consolidation paths, are not blocked waiting for the tx to appear in
	// the Monero daemon.
	c.queueSpentOutputRefsRetry(request, interval, maxAttempts, onOutcome)
	return applied
}

// queueSpentOutputRefsRetry owns the async retry goroutine. If the client is
// stopping, it reports a failed outcome immediately so observer latches are
// not left pending forever.
func (c *Client) queueSpentOutputRefsRetry(
	request types.SpentOutputRefsImport,
	interval time.Duration,
	maxAttempts int,
	onOutcome func(applied bool),
) {
	tx := request.Tx
	if c.wg == nil || c.stopchan == nil {
		c.logger.Warn().Str("tx_hash", tx.Tx.ID.String()).Msg("skip xmr spent output refs retry: client lifecycle is not initialized")
		if onOutcome != nil {
			onOutcome(false)
		}
		return
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	select {
	case <-c.stopchan:
		c.logger.Debug().
			Str("tx_hash", tx.Tx.ID.String()).
			Str("vault", tx.ObservedPubKey.String()).
			Msg("skip xmr spent output refs retry: client is stopping")
		if onOutcome != nil {
			onOutcome(false)
		}
		return
	default:
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.retrySpentOutputRefs(request, interval, maxAttempts, onOutcome)
	}()
}

// applySpentOutputRefsOnce performs one verified mark-spent attempt. Its retry
// result is true only for transient failures where another bounded attempt can
// still seed the sidecar, such as the spend tx not being indexed yet or the
// sidecar being temporarily unavailable.
func (c *Client) applySpentOutputRefsOnce(request types.SpentOutputRefsImport) (bool, bool) {
	return c.applySpentOutputRefsOnceContext(context.Background(), request)
}

// applySpentOutputRefsOnceContext checks cancellation before starting new work.
// A started MarkSpent keeps its existing sidecar timeout and completes durable
// signer bookkeeping on success; deferred replay can retry through catch-up.
func (c *Client) applySpentOutputRefsOnceContext(ctx context.Context, request types.SpentOutputRefsImport) (bool, bool) {
	tx := request.Tx
	if err := validateXMRSpentRefsForSidecar(tx); err != nil {
		c.logger.Warn().
			Err(err).
			Str("tx_hash", tx.Tx.ID.String()).
			Str("vault", tx.ObservedPubKey.String()).
			Msg("skip invalid xmr spent output refs")
		return false, false
	}
	vaultPubKey := tx.ObservedPubKey.String()
	txHash := tx.Tx.ID.String()
	if c.sidecar == nil {
		c.logger.Warn().Str("tx_hash", txHash).Str("vault", vaultPubKey).Msg("skip xmr spent output refs: sidecar is not initialized")
		return false, false
	}

	refs := xmrSpentRefsToSidecar(tx.SpentOutputRefs)
	var outbound *sidecar.OutboundMeta
	if request.ChainCommitted {
		// XMR keeps its original memo on-chain. Restore the complete verified
		// payment so a late import can also prove that it was mined, without
		// upgrading accounting-only evidence into a source observation.
		outbound = xmrOutboundMeta(tx.Tx, tx.XmrTxKey)
		outbound.Origin = sidecar.OutgoingOriginChainCommitted
	} else {
		certificateHash, hashErr := xmrSpentRefsCertificateHash(tx)
		if hashErr != nil {
			c.logger.Warn().Err(hashErr).Str("tx_hash", txHash).Msg("skip xmr spent refs with unhashable source-vault certificate")
			return false, false
		}
		outbound = xmrOutboundMeta(tx.Tx, tx.XmrTxKey)
		// Keep the certificate and durable claim bound to tx's semantic memo;
		// only the Sidecar metadata proof is checked against the exact wire memo.
		outbound.Memo = request.WireMemo
		outbound.Origin = sidecar.OutgoingOriginVaultCertified
		outbound.CertificateHash = certificateHash
	}
	if ctx.Err() != nil {
		return false, false
	}
	guard := c.spentOutputRefsAdmission(ctx, request)
	release, admitted := guard()
	if !admitted {
		return false, false
	}
	guarded := true
	defer func() {
		if guarded {
			release()
		}
	}()
	// MarkSpent changes accounting. Keep admission through durable signer
	// bookkeeping so errata cleanup cannot be followed by a stale import.
	result, err := c.sidecar.MarkSpent(vaultPubKey, txHash, refs, false, outbound)
	if err != nil {
		// Peer pre-quorum imports cannot rely on Thornode catchup until the refs
		// are committed, so retry sidecar-local transient failures. Validation
		// failures remain terminal and a later committed catchup can still
		// reconcile successfully applied refs independently.
		if isRetryableSpentRefsSidecarErr(err) {
			c.logger.Warn().
				Err(err).
				Str("tx_hash", txHash).
				Str("vault", vaultPubKey).
				Int("refs", len(refs)).
				Msg("mark-spent transient failure")
			return false, true
		}
		c.logger.Err(err).
			Str("tx_hash", txHash).
			Str("vault", vaultPubKey).
			Int("refs", len(refs)).
			Msg("failed to apply spent output refs import")
		return false, false
	}
	c.logger.Info().
		Str("tx_hash", txHash).
		Str("vault", vaultPubKey).
		Int("marked", result.Marked).
		Int("not_found", result.NotFound).
		Msg("applied spent output refs import")
	// A ref the sidecar could not find is the same transient state as a 404:
	// this peer's scanner may simply not have reached the funding output yet.
	// Keep the bounded retry window open and replay the complete ref set. The
	// sidecar validates all refs before mutation and treats identical
	// key-image bindings as idempotent; sending only missing refs would fail its
	// exact transaction-input coverage check.
	if result.NotFound > 0 {
		return false, true
	}
	if !verifiedXMRMarkSpentResult(result, len(refs)) {
		return false, false
	}
	if err = c.markImportedXMROutboundSigned(tx, request.ChainCommitted); err != nil {
		c.logger.Warn().Err(err).Str("tx_hash", txHash).
			Msg("refuse to enroll xmr spent-ref import without authenticated durable signer state")
		return false, false
	}
	// Reorg scanning holds scanMu while waiting for Observer cleanup. Release
	// the admission reader before replay takes scanMu; replay reacquires it
	// under that lock before any sidecar or durable mutation.
	release()
	guarded = false
	if request.ReplaySource && !request.ChainCommitted && c.scanner == nil {
		return false, true
	}
	if c.scanner != nil {
		if ctx.Err() != nil {
			// The refs and signer state are durable. Periodic catch-up can
			// retry the import and enroll replay after this request is canceled.
			return false, true
		}
		if request.ChainCommitted && !isXMRTxMigrateMemo(tx.Tx.Memo) {
			// Migrations already bind their journal and mark the source mined
			// through the destination-replay path below. Ordinary committed
			// payouts need the same accounting reconciliation, but no replay.
			if reconcileErr := c.scanner.reconcileCommittedOutboundGuarded(tx, guard); reconcileErr != nil {
				if errors.Is(reconcileErr, errXMRSpentRefsAdmissionRejected) {
					return false, false
				}
				c.logger.Warn().Err(reconcileErr).Str("tx_hash", txHash).
					Msg("committed xmr payout mined reconciliation will retry")
				return false, true
			}
		}
		if request.ReplaySource && !request.ChainCommitted {
			if _, replayErr := c.scanner.enqueueAuthenticatedOutboundReplayGuarded(tx, guard); replayErr != nil {
				if errors.Is(replayErr, errXMRSpentRefsAdmissionRejected) {
					return false, false
				}
				c.logger.Error().Err(replayErr).Str("tx_hash", txHash).Msg("failed to queue authenticated xmr payout replay")
				return false, true
			}
		}
		queued, replayErr := c.scanner.enqueueAuthenticatedInternalInboundReplayGuarded(tx, guard)
		if replayErr != nil {
			if errors.Is(replayErr, errXMRSpentRefsAdmissionRejected) {
				return false, false
			}
			c.logger.Error().Err(replayErr).Str("tx_hash", txHash).
				Msg("failed to queue authenticated xmr migration replay")
			// MarkSpent is idempotent. Retry the complete import so a transient
			// canonical lookup or durable outbox failure cannot strand a
			// migration destination credit.
			return false, true
		}
		if queued {
			c.logger.Info().Str("tx_hash", txHash).
				Msg("authenticated xmr migration destination queued for scanner replay")
		}
	}
	return true, false
}

func (c *Client) markImportedXMROutboundSigned(tx common.ObservedTx, thorchainSourced bool) error {
	vaultAddress := common.Address(c.deriveVaultAddress(tx.ObservedPubKey))
	if vaultAddress.String() == "" {
		return errors.New("cannot authenticate imported xmr fulfillment without a derived vault address")
	}
	if tx.Tx.FromAddress.String() != vaultAddress.String() {
		return errors.New("refusing imported xmr fulfillment that is not vault-origin")
	}
	if c.signedTxPayloadStore == nil {
		return nil
	}
	// MarkSpent establishes transaction visibility, which includes the mempool.
	// Neither local signing metadata nor peer-supplied final-looking heights are
	// authoritative finality. Preserve a matching exact signed artifact; keep
	// the conservative duplicate-payment barrier for unknown/different results.
	// OnObservedTxInDurable owns authoritative observation transitions, except
	// that THORChain-sourced catch-up keeps its final transition: a node that
	// never observes the transaction itself would otherwise keep reusable
	// aliases (consolidation, wire-memoless) committed-unfinalized forever.
	finalized := thorchainSourced && tx.BlockHeight > 0 && tx.FinaliseHeight > 0 && tx.IsFinal()
	recordExact := func(observeKey, ownerVault, txHash string) error {
		var recordErr error
		if finalized {
			_, _, recordErr = c.signedTxPayloadStore.CompleteObservedExact(observeKey, ownerVault, txHash)
		} else {
			_, _, recordErr = c.signedTxPayloadStore.ImportVerifiedExact(observeKey, ownerVault, txHash)
		}
		return recordErr
	}
	recordLogical := func(logicalHash string, observeKeys []string, txHash string) error {
		var recordErr error
		if finalized {
			_, _, recordErr = c.signedTxPayloadStore.CompleteObservedLogical(logicalHash, observeKeys, txHash)
		} else {
			_, _, recordErr = c.signedTxPayloadStore.ImportVerifiedLogical(logicalHash, observeKeys, txHash)
		}
		return recordErr
	}
	m, err := memo.ParseMemo(common.LatestVersion, tx.Tx.Memo)
	if err != nil {
		// A wire-memoless observation is ambiguous without its resolved original
		// memo. Record only an exact same-vault hash event; signer-created and
		// quorum-certified spent-ref observations carry the semantic memo and use
		// the vault-independent path below.
		return recordExact(
			xmrScheduledObserveKey(tx.ObservedPubKey, tx.Tx.ToAddress.String(), tx.Tx.Coins, tx.Tx.Memo),
			tx.ObservedPubKey.String(),
			tx.Tx.ID.String(),
		)
	}

	var cacheHash string
	switch {
	case m.IsType(memo.TxMigrate):
		logicalHash := xmrMigrationObserveKey(tx.ObservedPubKey, tx.Tx.ToAddress.String(), tx.Tx.Memo)
		if err = recordLogical(
			logicalHash,
			[]string{logicalHash},
			tx.Tx.ID.String(),
		); err != nil {
			return err
		}
		cacheHash = logicalHash
	case m.IsType(memo.TxConsolidate):
		if err = recordExact(
			xmrConsolidationObserveKey(tx.ObservedPubKey),
			tx.ObservedPubKey.String(),
			tx.Tx.ID.String(),
		); err != nil {
			return err
		}
		cacheHash = tx.Tx.ID.String()
	case m.IsOutbound():
		logicalHash := xmrScheduledLogicalHash(c.GetChain(), tx.Tx.ToAddress.String(), tx.Tx.Coins, tx.Tx.Memo)
		observeKey := xmrScheduledObserveKey(tx.ObservedPubKey, tx.Tx.ToAddress.String(), tx.Tx.Coins, tx.Tx.Memo)
		if err = recordLogical(
			logicalHash,
			[]string{observeKey},
			tx.Tx.ID.String(),
		); err != nil {
			return err
		}
		item := &stypes.TxInItem{
			To:                  tx.Tx.ToAddress.String(),
			Coins:               tx.Tx.Coins,
			Memo:                tx.Tx.Memo,
			ObservedVaultPubKey: tx.ObservedPubKey,
		}
		cacheHash = item.CacheHash(c.GetChain(), m.GetTxID().String())
	default:
		return nil
	}

	if c.signerCacheManager == nil {
		return nil
	}

	signerCacheVault := stypes.InboundCacheKey(tx.ObservedPubKey.String(), c.GetChain().String())
	if err := c.signerCacheManager.SetSigned(cacheHash, signerCacheVault, tx.Tx.ID.String()); err != nil {
		c.logger.Err(err).
			Str("tx_hash", tx.Tx.ID.String()).
			Str("cache_hash", cacheHash).
			Msg("fail to set signer cache from imported xmr outbound")
		return nil
	}
	return nil
}

func (c *Client) recordObservedXMRExactClaim(
	finalized bool,
	observeKeys []string,
	ownerVault,
	txHash,
	reason string,
) (bool, error) {
	recorded := false
	for _, observeKey := range observeKeys {
		var (
			claim     signedTxPayloadClaim
			completed bool
			err       error
		)
		if finalized {
			claim, completed, err = c.signedTxPayloadStore.CompleteObservedExact(observeKey, ownerVault, txHash)
		} else {
			claim, completed, err = c.signedTxPayloadStore.CommitUnfinalizedExact(observeKey, ownerVault, txHash)
		}
		if err != nil {
			c.logger.Err(err).
				Str("tx_hash", txHash).
				Str("reason", reason).
				Msg("fail to persist observed xmr signing claim state")
			return false, err
		}
		recorded = true
		if !completed {
			continue
		}
		if claim.SignedTxHash != "" && !strings.EqualFold(claim.SignedTxHash, txHash) {
			c.logger.Error().
				Str("signed_tx_hash", claim.SignedTxHash).
				Str("observed_tx_hash", txHash).
				Str("reason", reason).
				Msg("committed xmr observation differs from reserved signed payload; retained incident evidence")
		} else {
			c.logger.Debug().
				Str("tx_hash", txHash).
				Str("reason", reason).
				Bool("finalized", finalized).
				Msg("persisted observed xmr signing claim state")
		}
		return true, nil
	}
	return recorded, nil
}

func (c *Client) recordObservedXMRLogicalClaim(
	finalized bool,
	logicalHash string,
	observeKeys []string,
	txHash,
	reason string,
) (bool, error) {
	var (
		claim     signedTxPayloadClaim
		completed bool
		err       error
	)
	if finalized {
		claim, completed, err = c.signedTxPayloadStore.CompleteObservedLogical(logicalHash, observeKeys, txHash)
	} else {
		claim, completed, err = c.signedTxPayloadStore.CommitUnfinalizedLogical(logicalHash, observeKeys, txHash)
	}
	if err != nil {
		c.logger.Err(err).Str("tx_hash", txHash).Str("reason", reason).
			Msg("fail to persist logical xmr observation state")
		return false, err
	}
	if !completed {
		return true, nil
	}
	if claim.SignedTxHash != "" && !strings.EqualFold(claim.SignedTxHash, txHash) {
		c.logger.Error().
			Str("signed_tx_hash", claim.SignedTxHash).
			Str("observed_tx_hash", txHash).
			Str("reason", reason).
			Msg("different committed xmr transaction fulfilled the logical outbound; local payload quarantined")
	}
	return true, nil
}

func xmrSpentRefsCertificateHash(tx common.ObservedTx) (string, error) {
	payload, err := tx.GetSignablePayloadWithInbound(false)
	if err != nil {
		return "", fmt.Errorf("encode exact observed tx certificate payload: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// isRetryableSpentRefsSidecarErr identifies sidecar failures that should not
// permanently consume the peer-import opportunity. HTTP 404 covers the sidecar's
// get_transactions race window; 429/5xx and non-HTTP call failures cover local
// sidecar startup, overload, or temporary transport/read/decode failures.
func isRetryableSpentRefsSidecarErr(err error) bool {
	var httpErr *sidecar.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusNotFound ||
			httpErr.Status == http.StatusTooManyRequests ||
			httpErr.Status >= http.StatusInternalServerError
	}
	return err != nil
}

// retrySpentOutputRefs keeps retrying transient sidecar failures for a bounded
// window. This covers peer pre-quorum imports where the XMR spend may not be
// mined/indexed yet or where the local sidecar is temporarily unavailable, and
// Thornode catchup cannot help until the refs are committed.
func (c *Client) retrySpentOutputRefs(
	request types.SpentOutputRefsImport,
	interval time.Duration,
	maxAttempts int,
	onOutcome func(applied bool),
) {
	tx := request.Tx
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Wait before each retry so a just-gossiped outbound has time to appear
		// in the Monero daemon/sidecar transaction lookup.
		select {
		case <-c.stopchan:
			if onOutcome != nil {
				onOutcome(false)
			}
			return
		case <-timer.C:
		}

		applied, retry := c.applySpentOutputRefsOnce(request)
		if !retry {
			if onOutcome != nil {
				onOutcome(applied)
			}
			return
		}
		if attempt == maxAttempts {
			break
		}
		// Keep the retry cadence simple and bounded; successful committed
		// catchup can still reconcile the same refs independently later.
		timer.Reset(interval)
	}

	c.logger.Warn().
		Str("tx_hash", tx.Tx.ID.String()).
		Str("vault", tx.ObservedPubKey.String()).
		Int("attempts", maxAttempts).
		Msg("xmr spent output refs retry exhausted")
	if onOutcome != nil {
		onOutcome(false)
	}
}

func xmrSpentRefsToSidecar(refs []common.OutputRef) []sidecar.SpentRef {
	sidecarRefs := make([]sidecar.SpentRef, len(refs))
	for i, ref := range refs {
		sidecarRefs[i] = sidecar.SpentRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
			SpendTxHash: ref.SpendTxHash,
		}
	}
	return sidecarRefs
}

func xmrOutputRefsFromPointers(refs []*common.OutputRef) []common.OutputRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]common.OutputRef, 0, len(refs))
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		out = append(out, *ref)
	}
	return out
}

func validateXMRSpentRefsForSidecar(tx common.ObservedTx) error {
	if len(tx.SpentOutputRefs) == 0 {
		return fmt.Errorf("xmr spent refs are empty")
	}
	if err := tx.Valid(); err != nil {
		return err
	}
	if !tx.Tx.Chain.Equals(common.XMRChain) {
		return fmt.Errorf("spent output refs are only valid for XMR observed txs")
	}
	if err := validateFixedHex(tx.Tx.ID.String(), 32, "spend tx hash"); err != nil {
		return err
	}
	if strings.TrimSpace(tx.XmrTxKey) == "" {
		return fmt.Errorf("xmr spent refs require outbound tx key")
	}
	if err := validateFixedHex(tx.XmrTxKey, 32, "xmr tx key"); err != nil {
		return err
	}
	if !common.XMRChain.IsValidAddress(tx.Tx.ToAddress) {
		return fmt.Errorf("xmr spent refs require a valid xmr destination address")
	}
	vaultAddress, err := tx.ObservedPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return fmt.Errorf("derive xmr vault address from observed pubkey: %w", err)
	}
	if !common.AddressEquals(common.XMRChain, tx.Tx.FromAddress, vaultAddress) {
		return fmt.Errorf("xmr spent refs require outbound from observed vault")
	}
	seenKeyImages := make(map[string]struct{}, len(tx.SpentOutputRefs))
	for _, ref := range tx.SpentOutputRefs {
		if err := validateFixedHex(ref.TxHash, 32, "spent output tx hash"); err != nil {
			return err
		}
		if err := validateFixedHex(ref.KeyImage, 32, "key image"); err != nil {
			return err
		}
		keyImage := strings.ToLower(strings.TrimSpace(ref.KeyImage))
		if _, duplicate := seenKeyImages[keyImage]; duplicate {
			return fmt.Errorf("duplicate xmr spent ref key image")
		}
		seenKeyImages[keyImage] = struct{}{}
		if err := validateFixedHex(ref.SpendTxHash, 32, "spend tx hash"); err != nil {
			return err
		}
	}
	return nil
}

func validateFixedHex(value string, byteLen int, field string) error {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != byteLen*2 {
		return fmt.Errorf("%s must be %d-byte hex", field, byteLen)
	}
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return fmt.Errorf("%s must be hex: %w", field, err)
	}
	if len(decoded) != byteLen {
		return fmt.Errorf("%s must be %d-byte hex", field, byteLen)
	}
	return nil
}

func (c *Client) postKeysignFailure(tx stypes.TxOutItem, height int64, signErr error) error {
	if signErr == nil || c.thorchainBridge == nil {
		return signErr
	}

	var keysignError bifrosttss.KeysignError
	if !errors.As(signErr, &keysignError) {
		return signErr
	}
	if len(keysignError.Blame.BlameNodes) == 0 {
		return signErr
	}

	txID, err := c.thorchainBridge.PostKeysignFailure(
		keysignError.Blame,
		height,
		tx.Memo,
		tx.Coins,
		txVaultPubKey(tx),
	)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to post keysign failure to thorchain")
		return multierror.Append(signErr, fmt.Errorf("fail to post keysign failure to THORChain: %w", err))
	}

	c.logger.Info().Str("tx_id", txID.String()).Msg("post keysign failure to thorchain")
	return signErr
}

func (c *Client) reportXMRMaxGasExceeded(signErr error) {
	if signErr == nil || c.globalNetworkFeeQueue == nil {
		return
	}
	var maxGasErr xmrMaxGasExceededError
	if !errors.As(signErr, &maxGasErr) {
		return
	}
	if c.scanner == nil {
		c.logger.Warn().
			Err(signErr).
			Msg("skip xmr max gas exceeded network fee report: scanner is unavailable")
		return
	}
	processedHeight := c.scanner.CurrentHeight()
	height := canonicalXMRNetworkFeeReportHeight(processedHeight)
	if height <= 0 {
		c.logger.Warn().
			Err(signErr).
			Msg("skip xmr max gas exceeded network fee report: invalid processed xmr height")
		return
	}
	feeRate := feeRateFromTotalAtomic(maxGasErr.actualFeeAtomic)
	if feeRate == 0 {
		c.logger.Warn().
			Uint64("actual_fee_atomic", maxGasErr.actualFeeAtomic).
			Uint64("max_gas_atomic", maxGasErr.maxGasAtomic).
			Msg("skip xmr max gas exceeded network fee report: fee rate is zero")
		return
	}

	networkFee := common.NetworkFee{
		Chain:           c.chain,
		Height:          height,
		TransactionSize: typicalVaultOutboundWeight,
		TransactionRate: feeRate,
	}
	select {
	case c.globalNetworkFeeQueue <- networkFee:
		c.logger.Info().
			Int64("xmr_height", height).
			Int64("processed_xmr_height", processedHeight).
			Uint64("tx_size", networkFee.TransactionSize).
			Uint64("fee_rate", networkFee.TransactionRate).
			Uint64("actual_fee_atomic", maxGasErr.actualFeeAtomic).
			Uint64("max_gas_atomic", maxGasErr.maxGasAtomic).
			Msg("reported xmr max gas exceeded network fee")
	default:
		c.logger.Warn().
			Int64("xmr_height", height).
			Int64("processed_xmr_height", processedHeight).
			Uint64("tx_size", networkFee.TransactionSize).
			Uint64("fee_rate", networkFee.TransactionRate).
			Uint64("actual_fee_atomic", maxGasErr.actualFeeAtomic).
			Uint64("max_gas_atomic", maxGasErr.maxGasAtomic).
			Msg("drop xmr max gas exceeded network fee report: queue full")
	}
}

const xmrNetworkFeeReportHeightBucket int64 = 10

// canonicalXMRNetworkFeeReportHeight keeps the attested height on the Monero
// chain while allowing validators whose scanners differ by a few blocks to
// sign the same one-shot max-gas correction. NetworkFee voters include Height
// in their identity, so using each node's exact asynchronous scanner cursor
// would fragment an otherwise identical fee report and prevent quorum.
func canonicalXMRNetworkFeeReportHeight(processedHeight int64) int64 {
	if processedHeight <= 0 {
		return 0
	}
	return processedHeight - processedHeight%xmrNetworkFeeReportHeightBucket
}

func (c *Client) GetHeight() (int64, error) {
	if c.scanner == nil {
		return 0, errors.New("monero scanner is not initialized")
	}
	return c.scanner.GetHeight()
}

func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	if addr := c.deriveVaultAddress(poolPubKey); addr != "" {
		return addr
	}
	addr, err := poolPubKey.GetAddress(c.chain)
	if err != nil {
		c.logger.Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get monero address for pool pubkey")
		return ""
	}
	return addr.String()
}

func (c *Client) GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error) {
	if addr := c.deriveVaultAddress(poolPubKey); addr != "" {
		return c.GetAccountByAddress(addr, height)
	}

	addr, err := poolPubKey.GetAddress(c.chain)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get monero address from pubkey: %w", err)
	}
	if addr.IsEmpty() {
		return common.Account{}, fmt.Errorf("fail to resolve monero vault address for %s", poolPubKey.String())
	}
	return c.GetAccountByAddress(addr.String(), height)
}

func (c *Client) GetAccountByAddress(address string, height *big.Int) (common.Account, error) {
	return c.getAccountByAddress(address, height, false)
}

func (c *Client) getSolvencyAccountByAddress(address string) (common.Account, error) {
	return c.getAccountByAddress(address, nil, true)
}

func (c *Client) getAccountByAddress(address string, _ *big.Int, requireReconciled bool) (common.Account, error) {
	if c.sidecar == nil {
		return common.Account{}, fmt.Errorf("xmr account query requires sidecar")
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return common.Account{}, fmt.Errorf("xmr account query requires vault address")
	}

	pubKey, found := c.findVaultPubKeyByAddress(address)
	if !found {
		return common.Account{}, fmt.Errorf("no vault pub key found for address %s", address)
	}

	if err := c.ensureVaultRegistered(pubKey); err != nil {
		return common.Account{}, fmt.Errorf("fail to register monero vault for %s: %w", address, err)
	}

	balance, err := c.sidecar.Balance(pubKey.String())
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get monero wallet balance for %s: %w", address, err)
	}
	if requireReconciled && !balance.IsReconciled() {
		pendingOutgoingCount := -1
		if balance.PendingOutgoingCount != nil {
			pendingOutgoingCount = *balance.PendingOutgoingCount
		}
		return common.Account{}, fmt.Errorf(
			"%w for %s: pending_outgoing_count=%d",
			errXMRBalanceUnreconciled,
			address,
			pendingOutgoingCount,
		)
	}
	if requireReconciled && balance.QuarantinedOutgoingCount != nil && *balance.QuarantinedOutgoingCount > 0 {
		c.logger.Error().
			Str("address", address).
			Int("quarantined_outgoing_count", *balance.QuarantinedOutgoingCount).
			Msg("monero balance reconciled with verified-absent outgoings held non-spendable")
	}
	// Account reporting (used by solvency comparison vs thornode's vault.coins)
	// must use the TOTAL on-chain balance — locked + unlocked. Locked outputs
	// (those in the 10-conf maturity window) DO exist on chain and must count
	// toward the wallet's view of "what we own", otherwise every fresh inbound
	// triggers a false-positive insolvency report during normal operation.
	// Spendability for sign-time input selection is a separate concern handled
	// by ensureVaultBalance which uses spendableBalanceAtomic.
	amount := cosmos.NewUint(balance.Balance / moneroAtomicToThorchain)
	return common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins: common.NewCoins(
			common.NewCoin(common.XMRAsset, amount),
		),
		HasMemoFlag: false,
	}, nil
}

func (c *Client) findVaultPubKeyByAddress(address string) (common.PubKey, bool) {
	address = strings.TrimSpace(address)
	if address == "" {
		return common.EmptyPubKey, false
	}
	c.vaultAddressMu.RLock()
	defer c.vaultAddressMu.RUnlock()
	if pubKeyStr, ok := c.vaultAddrToKey[address]; ok {
		pk, err := common.NewPubKey(pubKeyStr)
		if err == nil {
			return pk, true
		}
	}
	for pubKeyStr, addr := range c.vaultAddressMap {
		if addr.String() == address {
			pk, err := common.NewPubKey(pubKeyStr)
			if err == nil {
				return pk, true
			}
		}
	}
	return common.EmptyPubKey, false
}

func (c *Client) GetChain() common.Chain {
	return c.chain
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

func (c *Client) CustomKeygen() bool {
	return true
}

func (c *Client) Keygen(req types.CustomKeygenRequest) (types.CustomKeygenResult, error) {
	if c.sidecar == nil {
		return types.CustomKeygenResult{}, fmt.Errorf("%w: sidecar is not initialized", ErrSidecarNotConfigured)
	}
	if c.tssServer == nil {
		return types.CustomKeygenResult{}, fmt.Errorf("tss server is not initialized")
	}

	// Join before querying height: round 3 collects the birthday-height quorum
	// and treats an unavailable local height as an abstention. A preflight RPC
	// only adds node-specific join delay; actual sidecar DKG failures still fail.

	members := make([]string, 0, len(req.Members))
	for _, member := range req.Members {
		member = strings.TrimSpace(member)
		if member != "" {
			members = append(members, member)
		}
	}
	if len(members) == 0 {
		return types.CustomKeygenResult{}, fmt.Errorf("xmr keygen requires non-empty member set")
	}
	requestedVaultKey := monovaultKeygenLabel(req.BlockHeight, req.PoolPubKey, members)

	frostResp, err := c.tssServer.FrostKeygen(tssp.FrostKeygenRequest{
		VaultPubKey:   requestedVaultKey,
		SignerPubKeys: members,
		BlockHeight:   req.BlockHeight,
	}, sidecar.NewFrostAdapter(c.sidecar))
	if err != nil {
		c.logFailClosedCoordination("frost_tss_keygen", err)
		return types.CustomKeygenResult{}, fmt.Errorf("fail to execute FROST keygen: %w", err)
	}
	if frostResp.Status != gotsscommon.Success {
		c.logFailClosedCoordination("frost_tss_keygen", fmt.Errorf("frost keygen failed: %s", frostResp.Blame.FailReason))
		var blame []ttypes.Blame
		if len(frostResp.Blame.BlameNodes) > 0 || strings.TrimSpace(frostResp.Blame.FailReason) != "" {
			blame = append(blame, frostBlameToThorchainBlame(frostResp.Blame))
		}
		return types.CustomKeygenResult{Blame: blame}, fmt.Errorf("FROST keygen failed: %s", strings.TrimSpace(frostResp.Blame.FailReason))
	}

	frostAddress := strings.TrimSpace(frostResp.Address)
	if frostAddress == "" {
		return types.CustomKeygenResult{}, fmt.Errorf("FROST keygen succeeded without an address")
	}
	frostAddr, err := common.NewAddress(frostAddress)
	if err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("invalid FROST keygen address %q: %w", frostAddress, err)
	}
	groupKeyHex := strings.TrimSpace(frostResp.GroupKey)
	if groupKeyHex == "" {
		return types.CustomKeygenResult{}, fmt.Errorf("FROST keygen succeeded without a group key")
	}

	frostVaultPubKey, err := vaultPubKeyFromGroupKeyHex(groupKeyHex)
	if err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("fail to convert FROST group key to vault pubkey: %w", err)
	}
	expectedAddress, err := frostVaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("fail to derive monero address from FROST group key %s: %w", groupKeyHex, err)
	}
	if !frostAddr.Equals(expectedAddress) {
		return types.CustomKeygenResult{}, fmt.Errorf(
			"FROST keygen returned address %s but expected derived vault address %s",
			frostAddr.String(),
			expectedAddress.String(),
		)
	}

	if c.scanner != nil {
		c.scanner.scanMu.Lock()
	}
	registrationBirthday := frostResp.ObservedMoneroHeight
	if c.blockScanner != nil {
		registrationBirthday = xmrVaultRegistrationBirthday(
			registrationBirthday,
			c.blockScanner.PreviousHeight()+1,
		)
	}
	_, registerErr := c.sidecar.RegisterVault(sidecar.RegisterVaultReq{
		VaultPubKey:         frostVaultPubKey.String(),
		GroupKey:            groupKeyHex,
		Address:             frostAddr.String(),
		Source:              "bifrost",
		VaultCreationHeight: registrationBirthday,
	})
	if c.scanner != nil {
		c.scanner.scanMu.Unlock()
	}
	if registerErr != nil {
		c.logFailClosedCoordination("frost_tss_keygen_register", registerErr)
		return types.CustomKeygenResult{}, fmt.Errorf("fail to register FROST vault mapping for %s: %w", frostVaultPubKey.String(), registerErr)
	}

	c.setVaultAddressMapping(frostVaultPubKey, frostAddr)
	keyshareHex := strings.TrimSpace(frostResp.KeyBytesHex)
	if keyshareHex == "" {
		return types.CustomKeygenResult{}, fmt.Errorf("FROST keygen returned empty recovery keyshare")
	}
	if _, err := hex.DecodeString(keyshareHex); err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("FROST keygen returned invalid recovery keyshare hex: %w", err)
	}
	return types.CustomKeygenResult{
		Address:              frostAddr,
		PoolPubKeyFrost:      frostVaultPubKey,
		FrostKeyshareHex:     keyshareHex,
		ObservedMoneroHeight: frostResp.ObservedMoneroHeight,
	}, nil
}

// xmrVaultRegistrationBirthday prevents vault registration from starting the
// sidecar scanner ahead of Bifrost's next requested block. Scanning a
// conservative range before key creation is safe, while a forward cursor jump
// could skip blocks Bifrost still expects to observe.
func xmrVaultRegistrationBirthday(observedHeight, nextScanHeight int64) int64 {
	if observedHeight > 0 && nextScanHeight > 0 && nextScanHeight < observedHeight {
		return nextScanHeight
	}
	return observedHeight
}

func monovaultKeygenLabel(blockHeight int64, poolPubKey common.PubKey, members []string) string {
	var canonicalMembers []string
	for _, member := range members {
		member = strings.TrimSpace(member)
		if member != "" {
			canonicalMembers = append(canonicalMembers, member)
		}
	}
	sort.Strings(canonicalMembers)
	labelInput := fmt.Sprintf(
		"thorchain-xmr-monovault-keygen\nheight=%d\npool=%s\nmembers=%s",
		blockHeight,
		strings.TrimSpace(poolPubKey.String()),
		strings.Join(canonicalMembers, ","),
	)
	sum := sha256.Sum256([]byte(labelInput))
	hrp := sdk.GetConfig().GetBech32AccountPubPrefix()

	converted, err := bech32.ConvertBits(sum[:20], 8, 5, true)
	if err != nil {
		return hrp + "1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	}
	placeholder, err := bech32.Encode(hrp, converted)
	if err != nil {
		return hrp + "1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"
	}
	return placeholder
}

// sidecarHasVaultKeys reports whether the sidecar has a signable share bound
// to the exact requested FROST group key.
func (c *Client) sidecarHasVaultKeys(vaultPubKey common.PubKey) (bool, error) {
	return c.sidecarHasVaultKeysContext(context.Background(), vaultPubKey)
}

func (c *Client) sidecarHasVaultKeysContext(ctx context.Context, vaultPubKey common.PubKey) (bool, error) {
	if c.sidecar == nil {
		return false, fmt.Errorf("sidecar not configured")
	}
	if vaultPubKey.IsEmpty() {
		return false, fmt.Errorf("vault pubkey is empty")
	}
	vaults, err := c.sidecar.VaultsContext(ctx)
	if err != nil {
		return false, fmt.Errorf("fail to list sidecar vault keys: %w", err)
	}
	expectedGroupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		return false, fmt.Errorf("derive expected sidecar group key: %w", err)
	}
	identifiers := []string{vaultPubKey.String(), expectedGroupKey}
	for _, vault := range vaults {
		recorded := strings.TrimSpace(vault.VaultPubKey)
		for _, identifier := range identifiers {
			if strings.EqualFold(recorded, identifier) {
				if !strings.EqualFold(strings.TrimSpace(vault.GroupKey), expectedGroupKey) {
					return false, fmt.Errorf("sidecar vault key record does not match requested vault group key")
				}
				return vault.HasFrostKeys && vault.Signable, nil
			}
		}
	}
	return false, nil
}

// vaultSigningMembers returns both signer views needed by FROST: full vault
// membership for participant indexes and active membership for this keysign.
func (c *Client) vaultSigningMembers(vaultPubKey common.PubKey, blockHeight int64) ([]string, []string, error) {
	if vaultPubKey.IsEmpty() {
		return nil, nil, fmt.Errorf("vault pubkey is empty")
	}
	vault, err := c.getVaultByPubKey(vaultPubKey)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get vault membership: %w", err)
	}
	members := vault.GetMembership().Strings()
	if len(members) == 0 {
		// Some test and legacy vault records only populate the raw membership
		// strings. Keep that path so FROST can still derive stable indexes.
		for _, member := range vault.Membership {
			member = strings.TrimSpace(member)
			if member != "" {
				members = append(members, member)
			}
		}
	}
	if len(members) == 0 {
		return nil, nil, fmt.Errorf("vault %s has empty membership", vaultPubKey.String())
	}
	policy, err := c.thorchainBridge.GetKeysignPolicy(vaultPubKey, blockHeight)
	if err != nil {
		return nil, nil, err
	}
	activeMembers := policy.Signers.Strings()
	if len(activeMembers) == 0 {
		return nil, nil, fmt.Errorf("vault %s has empty active membership", vaultPubKey.String())
	}
	// The active signer set must be a filtered subset of full membership. This
	// protects FROST participant indexes from a malformed policy response.
	memberSet := make(map[string]bool, len(members))
	for _, member := range members {
		memberSet[strings.TrimSpace(member)] = true
	}
	for _, activeMember := range activeMembers {
		if !memberSet[strings.TrimSpace(activeMember)] {
			return nil, nil, fmt.Errorf("keysign policy signer %s is not in vault %s membership", activeMember, vaultPubKey.String())
		}
	}
	return members, activeMembers, nil
}

func frostBlameToThorchainBlame(in gotssblame.Blame) ttypes.Blame {
	out := ttypes.Blame{
		FailReason: in.FailReason,
		Round:      in.Round,
		IsUnicast:  in.IsUnicast,
		BlameNodes: make([]ttypes.Node, len(in.BlameNodes)),
	}
	for i, node := range in.BlameNodes {
		out.BlameNodes[i] = ttypes.Node{
			Pubkey:         node.Pubkey,
			BlameData:      node.BlameData,
			BlameSignature: node.BlameSignature,
		}
	}
	return out
}

func isAuthenticatedXMRVaultOrigin(item *stypes.TxInItem) bool {
	if item == nil {
		return false
	}
	sender := common.ResolveObservedSender(common.XMRChain, common.Address(item.Sender))
	if !sender.Known || !common.XMRChain.IsValidAddress(sender.Address) || item.ObservedVaultPubKey.IsEmpty() {
		return false
	}
	vaultAddress, err := item.ObservedVaultPubKey.GetAddress(common.XMRChain)
	if err != nil || vaultAddress.IsEmpty() {
		return false
	}
	return common.AddressEquals(common.XMRChain, sender.Address, vaultAddress)
}

func (c *Client) OnObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	if err := c.OnObservedTxInDurable(txIn, blockHeight); err != nil {
		c.logger.Error().Err(err).Str("tx_hash", txIn.Tx).Msg("fail to persist xmr observation authorization state")
	}
}

// OnObservedTxInDurable lets Observer acknowledge an XMR commit only after its
// local duplicate-payment guard is synced to disk. A committed-unfinalized
// observation suspends broadcasting reversibly; finality terminalizes it.
func (c *Client) OnObservedTxInDurable(txIn stypes.TxInItem, _ int64) error {
	finalized := !txIn.CommittedUnFinalised
	ownerVault := txIn.ObservedVaultPubKey.String()
	if !isAuthenticatedXMRVaultOrigin(&txIn) {
		c.logger.Warn().
			Str("tx_hash", txIn.Tx).
			Str("sender", txIn.Sender).
			Str("vault_pubkey", ownerVault).
			Msg("ignoring xmr claim transition not authenticated as a vault-origin transaction")
		return nil
	}
	m, err := memo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// A memoless wire observation cannot reveal its inbound/reference ID.
		// Treat its vault/to/amount alias as a race epoch, but terminalize only
		// an exact locally signed transaction hash so two identical legitimate
		// memoless outbounds cannot consume one another.
		if _, recordErr := c.recordObservedXMRExactClaim(
			finalized,
			[]string{xmrScheduledObserveKey(txIn.ObservedVaultPubKey, txIn.To, txIn.Coins, txIn.Memo)},
			ownerVault,
			txIn.Tx,
			"observed_unparsed",
		); recordErr != nil {
			return recordErr
		}
		c.logger.Debug().Err(err).Str("memo", txIn.Memo).Msg("fail to parse memo")
		return nil
	}
	if m.IsInternal() {
		if strings.TrimSpace(txIn.Tx) == "" {
			return nil
		}
		observationRecorded := false
		switch {
		case m.IsType(memo.TxMigrate):
			observeKey := xmrMigrationObserveKey(
				txIn.ObservedVaultPubKey,
				txIn.To,
				txIn.Memo,
			)
			observationRecorded, err = c.recordObservedXMRLogicalClaim(finalized, observeKey, []string{observeKey}, txIn.Tx, "observed_migration")
		case m.IsType(memo.TxConsolidate):
			observationRecorded, err = c.recordObservedXMRExactClaim(
				finalized,
				[]string{xmrConsolidationObserveKey(txIn.ObservedVaultPubKey)},
				ownerVault,
				txIn.Tx,
				"observed_consolidation",
			)
		}
		if err != nil {
			return err
		}
		if finalized && observationRecorded && c.signerCacheManager != nil {
			c.signerCacheManager.RemoveSigned(txIn.Tx)
		}
		return nil
	}
	if !m.IsOutbound() {
		return nil
	}

	// A committed outbound observation is the durable post-broadcast duplicate
	// guard. Rebuild the same cache key used by signing from the observed
	// outbound and its original inbound hash, then let signer cache suppress
	// future retries for this outbound.
	//
	// The claim tombstone is the synchronous duplicate guard. Signer-cache
	// seeding remains useful for the shared observer/signer paths, but failure
	// there must not prevent terminalizing an already committed chain result.
	cacheHash := txIn.CacheHash(c.GetChain(), m.GetTxID().String())
	logicalHash := xmrScheduledLogicalHash(c.GetChain(), txIn.To, txIn.Coins, txIn.Memo)
	observeKeys := []string{xmrScheduledObserveKey(txIn.ObservedVaultPubKey, txIn.To, txIn.Coins, txIn.Memo)}
	if _, err = c.recordObservedXMRLogicalClaim(finalized, logicalHash, observeKeys, txIn.Tx, "observed_outbound"); err != nil {
		return err
	}
	if !finalized || c.signerCacheManager == nil {
		return nil
	}
	signerCacheVault := txIn.CacheVault(c.GetChain())
	if err = c.signerCacheManager.SetSigned(cacheHash, signerCacheVault, txIn.Tx); err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
	return nil
}

func (c *Client) GetConfirmationCount(txIn stypes.TxIn) int64 {
	if len(txIn.TxArray) == 0 || txIn.MemPool {
		return 0
	}

	addresses, err := c.getAsgardAddress()
	if err != nil {
		c.logger.Err(err).Msg("fail to get asgard addresses")
		c.asgardMu.RLock()
		addresses = append(addresses, c.asgardAddresses...)
		c.asgardMu.RUnlock()
	}

	totalValue := txIn.GetTotalTransactionValue(common.XMRAsset, addresses)
	coinbaseThorchain := coinbaseThorchain()

	// Apply mimir-configurable confirmation multiplier (same lever as UTXO/ETH chains).
	// Default confMul = MaxBasisPts (10000) = 1x coinbase. Higher values reduce
	// required confirmations; lower values increase them.
	confMul, err := utxo.GetConfMulBasisPoint(c.GetChain().String(), c.thorchainBridge)
	if err != nil {
		c.logger.Debug().Err(err).Msg("fail to get xmr conf multiplier mimir value")
	}
	confValue := common.GetUncappedShare(confMul, cosmos.NewUint(constants.MaxBasisPts), cosmos.NewUint(coinbaseThorchain))
	if confValue.IsZero() {
		confValue = cosmos.NewUint(coinbaseThorchain)
	}
	confirm := confirmationRequired(totalValue, confValue)

	// Apply mimir-configurable max confirmation cap.
	confirm, err = utxo.MaxConfAdjustment(confirm, c.GetChain().String(), c.thorchainBridge)
	if err != nil {
		c.logger.Debug().Err(err).Msg("fail to get xmr max conf adjustment")
	}

	// Monero wallet outputs are not spendable until the network unlock period
	// has elapsed. Do not let the value-based confirmation calculation or
	// max-confirmation cap observe vault funds before they can be migrated or
	// spent by the signer.
	if confirm < xmrSpendableConfirmations {
		confirm = xmrSpendableConfirmations
	}

	// Apply configured minimum floor (always wins over max cap).
	if confirm < c.cfg.MinConfirmations {
		confirm = c.cfg.MinConfirmations
	}

	c.logger.Debug().
		Str("total_tx_value", totalValue.String()).
		Uint64("coinbase_atomic", defaultCoinbaseValue).
		Uint64("coinbase_thorchain", coinbaseThorchain).
		Str("conf_multiplier", confMul.String()).
		Str("conf_value", confValue.String()).
		Uint64("spendable_confirmations", xmrSpendableConfirmations).
		Uint64("min_confirmations", c.cfg.MinConfirmations).
		Uint64("confirmations", confirm).
		Msg("calculated xmr confirmation requirement")

	return int64(confirm)
}

func (c *Client) ConfirmationCountReady(txIn stypes.TxIn) bool {
	if len(txIn.TxArray) == 0 || txIn.MemPool {
		return true
	}

	blockHeight := txIn.TxArray[0].BlockHeight
	confirm := txIn.ConfirmationRequired
	return (c.scanner.CurrentHeight() - blockHeight) >= confirm
}

func (c *Client) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadcasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadcasted, err
}

func (c *Client) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

func (c *Client) ShouldReportSolvency(height int64) bool {
	if c.lastSolvencyCheckHeight == nil || c.scanner == nil {
		return false
	}
	if height <= c.lastSolvencyCheckHeight.Load() {
		return false
	}
	solvencyBlocks := c.cfg.SolvencyBlocks
	if solvencyBlocks <= 0 {
		solvencyBlocks = 10
	}
	return height%solvencyBlocks == 0 && c.scanner.lastFee.Load() > 0
}

// noteSolvencyDeferral counts consecutive deferred solvency rounds for a vault
// address and returns the updated count.
func (c *Client) noteSolvencyDeferral(address string) int {
	c.solvencyDeferralMu.Lock()
	defer c.solvencyDeferralMu.Unlock()
	if c.solvencyDeferralRounds == nil {
		c.solvencyDeferralRounds = make(map[string]int)
	}
	c.solvencyDeferralRounds[address]++
	return c.solvencyDeferralRounds[address]
}

func (c *Client) clearSolvencyDeferral(address string) {
	c.solvencyDeferralMu.Lock()
	defer c.solvencyDeferralMu.Unlock()
	delete(c.solvencyDeferralRounds, address)
}

func (c *Client) ReportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}
	// when block scanner is not healthy, only report from auto-unhalt SolvencyCheckRunner
	if !c.IsBlockScannerHealthy() {
		if c.blockScanner != nil && height == c.blockScanner.PreviousHeight()+1 {
			return nil
		}
	}

	asgardVaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards: %w", err)
	}

	currentGasFee := cosmos.NewUint(3 * totalFeeAtomicToThorchain(c.scanner.lastFee.Load()))
	msgs := make([]stypes.Solvency, 0, len(asgardVaults))
	solventMsgs := make([]stypes.Solvency, 0, len(asgardVaults))
	checkedVaults := 0
	deferredForReconciliation := false
	for i := range asgardVaults {
		address := c.resolveVaultAddress(asgardVaults[i])
		if address.IsEmpty() {
			continue
		}

		acct, acctErr := c.getSolvencyAccountByAddress(address.String())
		if acctErr != nil {
			if errors.Is(acctErr, errXMRBalanceUnreconciled) {
				deferredForReconciliation = true
				rounds := c.noteSolvencyDeferral(address.String())
				logEvent := c.logger.Warn()
				if rounds >= xmrSolvencyDeferralEscalationRounds {
					// Verified absence should move a dead spend into balance-counted,
					// non-spendable quarantine. A deferral running this long means
					// the pending spend is neither landing nor reaching that evidence
					// threshold, so operator attention is required.
					logEvent = c.logger.Error()
				}
				logEvent.Err(acctErr).Str("address", address.String()).Int64("height", height).Int("consecutive_rounds", rounds).Msg("defer monero solvency while sidecar balance reconciles")
				continue
			}
			c.logger.Err(acctErr).Str("address", address.String()).Msg("fail to get monero account balance for solvency")
			continue
		}
		c.clearSolvencyDeferral(address.String())
		checkedVaults++

		msg := stypes.Solvency{
			Height: height,
			Chain:  c.chain,
			PubKey: asgardVaults[i].PubKey,
			Coins:  acct.Coins,
		}

		if runners.IsVaultSolvent(c.chain, acct, asgardVaults[i], currentGasFee) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}
		msgs = append(msgs, msg)
	}

	if deferredForReconciliation && checkedVaults == 0 {
		return nil
	}

	// Report all vaults solvent only while THORChain itself has XMR halted or solvency-halted and
	// this node's scanner is paused for it: the same gate as the other chain clients. A scanner
	// that is unhealthy for a local reason (daemon or sidecar lag) must stay silent, because an
	// all-clear only one node sends can never reach consensus and each one costs that node an
	// unrefunded observation slash point. On a bridge error stay silent; the runner retries.
	halted, haltErr := runners.IsHaltedOnChain(c.thorchainBridge, c.chain)
	if haltErr != nil {
		c.logger.Err(haltErr).Msg("fail to read chain halt state, skipping solvency all-clear")
	}
	if halted && !c.IsBlockScannerHealthy() && !deferredForReconciliation && checkedVaults > 0 && len(solventMsgs) == checkedVaults {
		msgs = solventMsgs
	}

	for i := range msgs {
		select {
		case c.globalSolvencyQueue <- msgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msg("fail to send monero solvency info to THORChain: timeout")
		}
	}
	c.lastSolvencyCheckHeight.Store(height)
	return nil
}

func (c *Client) logFailClosedCoordination(path string, err error) {
	if err == nil {
		return
	}
	c.logger.Error().
		Err(err).
		Str("coordination_path", path).
		Msg("xmr frost coordination failed closed")
}

func (c *Client) getAsgardAddress() ([]common.Address, error) {
	c.asgardMu.RLock()
	if time.Since(c.lastAsgard) < constants.ThorchainBlockTime && len(c.asgardAddresses) > 0 {
		result := append([]common.Address(nil), c.asgardAddresses...)
		c.asgardMu.RUnlock()
		return result, nil
	}
	c.asgardMu.RUnlock()

	c.asgardMu.Lock()
	defer c.asgardMu.Unlock()
	if time.Since(c.lastAsgard) < constants.ThorchainBlockTime && len(c.asgardAddresses) > 0 {
		return append([]common.Address(nil), c.asgardAddresses...), nil
	}

	vaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards: %w", err)
	}

	next := make([]common.Address, 0, len(vaults))
	seen := make(map[string]struct{}, len(vaults))

	for _, vault := range vaults {
		address := c.resolveVaultAddress(vault)
		if address.IsEmpty() {
			continue
		}
		key := address.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		next = append(next, address)
	}

	if len(next) > maxAsgardAddresses {
		next = next[len(next)-maxAsgardAddresses:]
	}

	c.asgardAddresses = next
	c.lastAsgard = time.Now()
	result := append([]common.Address(nil), c.asgardAddresses...)

	return result, nil
}

func (c *Client) ensureVaultRegistered(vaultPubKey common.PubKey) error {
	return c.ensureVaultRegisteredContext(context.Background(), vaultPubKey)
}

func (c *Client) ensureVaultRegisteredContext(ctx context.Context, vaultPubKey common.PubKey) error {
	if c.sidecar == nil || vaultPubKey.IsEmpty() {
		return nil
	}

	// Registration can atomically rewind the sidecar cursor to an older vault
	// birthday. Serialize every Client entry point with FetchTxs so a reset
	// cannot land between reorg processing and local metadata persistence.
	if c.scanner != nil {
		c.scanner.scanMu.Lock()
		defer c.scanner.scanMu.Unlock()
	}
	address, err := ensureVaultRegisteredContext(ctx, c.logger, c.sidecar, c.chain, vaultPubKey, c.vaultBootstrap, "bifrost", 0)
	if err != nil {
		return err
	}
	if !address.IsEmpty() {
		c.setVaultAddressMapping(vaultPubKey, address)
	}
	return nil
}

func (c *Client) deriveVaultAddress(vaultPubKey common.PubKey) string {
	if vaultPubKey.IsEmpty() {
		return ""
	}
	address, err := vaultPubKey.GetAddress(c.chain)
	if err != nil || address.IsEmpty() {
		c.logger.Debug().Err(err).Str("vault_pubkey", vaultPubKey.String()).Msg("fail to derive monero vault address from pubkey")
		return ""
	}
	return address.String()
}

func (c *Client) resolveVaultAddress(vault ttypes.Vault) common.Address {
	vaultPubKey := vault.PubKeyFrost
	if vaultPubKey.IsEmpty() {
		return common.NoAddress
	}

	addr, err := vaultPubKey.GetAddress(c.chain)
	if err != nil || addr.IsEmpty() {
		return common.NoAddress
	}
	c.setVaultAddressMapping(vaultPubKey, addr)
	return addr
}

func (c *Client) setVaultAddressMapping(vaultPubKey common.PubKey, address common.Address) {
	if vaultPubKey.IsEmpty() || address.IsEmpty() {
		return
	}

	key := vaultPubKey.String()
	c.vaultAddressMu.Lock()
	if c.vaultAddressMap == nil {
		c.vaultAddressMap = make(map[string]common.Address)
	}
	if c.vaultAddrToKey == nil {
		c.vaultAddrToKey = make(map[string]string)
	}
	if existing, ok := c.vaultAddressMap[key]; ok && existing.Equals(address) {
		// Repair the reverse index as well as accepting an idempotent forward
		// mapping. Tests and recovery paths may construct a client with only the
		// forward cache populated; leaving the reverse cache empty makes inbound
		// attribution depend on its slower forward-map fallback.
		c.vaultAddrToKey[address.String()] = key
		c.vaultAddressMu.Unlock()
		return
	}
	if existing, ok := c.vaultAddressMap[key]; ok {
		delete(c.vaultAddrToKey, existing.String())
	}
	c.vaultAddressMap[key] = address
	c.vaultAddrToKey[address.String()] = key
	c.vaultAddressMu.Unlock()

	if c.vaultAddressStore != nil {
		if err := c.vaultAddressStore.Set(key, address.String()); err != nil {
			c.logger.Debug().Err(err).Str("vault_pubkey", key).Msg("fail to persist monero vault address mapping")
		}
	}
	if c.scanner != nil {
		c.scanner.SetManualVaultAddress(vaultPubKey, address.String())
	}
}

func (c *Client) cachedVaultAddress(vaultPubKey common.PubKey) (common.Address, bool) {
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, false
	}

	c.vaultAddressMu.RLock()
	address, ok := c.vaultAddressMap[vaultPubKey.String()]
	c.vaultAddressMu.RUnlock()
	if ok && !address.IsEmpty() {
		return address, true
	}
	return common.NoAddress, false
}

func (c *Client) loadVaultAddressMap() error {
	if c.vaultAddressStore == nil {
		return nil
	}
	stored, err := c.vaultAddressStore.List()
	if err != nil {
		return err
	}
	for vaultPubKey, addressStr := range stored {
		vaultKey, keyErr := common.NewPubKey(vaultPubKey)
		if keyErr != nil {
			c.logger.Debug().Err(keyErr).Str("vault_pubkey", vaultPubKey).Msg("skip invalid persisted monero vault pubkey")
			continue
		}
		address, addrErr := common.NewAddress(addressStr)
		if addrErr != nil {
			c.logger.Debug().Err(addrErr).Str("address", addressStr).Msg("skip invalid persisted monero vault address")
			continue
		}
		derived, deriveErr := vaultKey.GetAddress(c.chain)
		if deriveErr != nil || derived.IsEmpty() {
			c.logger.Debug().Err(deriveErr).Str("vault_pubkey", vaultPubKey).Msg("skip persisted monero vault mapping: cannot derive address from vault pubkey")
			continue
		}
		if !derived.Equals(address) {
			c.logger.Warn().
				Str("vault_pubkey", vaultPubKey).
				Str("persisted_address", address.String()).
				Str("derived_address", derived.String()).
				Msg("ignoring persisted monero vault mapping that does not match derived vault address")
			continue
		}
		c.setVaultAddressMapping(vaultKey, address)
	}
	return nil
}

func txVaultPubKey(tx stypes.TxOutItem) common.PubKey {
	return tx.VaultPubKeyFrost
}

func txCacheVault(tx stypes.TxOutItem, chain common.Chain) string {
	return stypes.BroadcastCacheKey(txVaultPubKey(tx).String(), chain.String())
}

type xmrSigningIdentity struct {
	CacheHash   string
	LogicalHash string
	ObserveKeys []string
}

// xmrSigningCacheIdentity separates local consolidation attempts while
// preserving the vault-independent identity used for scheduled outbounds and
// migrations. The latter must retain one global ownership claim across vault
// reschedules; otherwise two vaults can each produce a valid payment for the
// same logical outbound before either transaction is observed.
func xmrSigningCacheIdentity(tx stypes.TxOutItem) (xmrSigningIdentity, error) {
	baseHash := tx.CacheHash()
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, tx.Memo)
	if err != nil || !parsedMemo.IsType(memo.TxConsolidate) {
		observeKeys := xmrScheduledObserveKeys(tx)
		logicalHash := xmrScheduledLogicalHash(
			tx.Chain,
			tx.ToAddress.String(),
			tx.Coins,
			tx.GetMemo(),
		)
		if err == nil && parsedMemo.IsType(memo.TxMigrate) {
			logicalHash = xmrMigrationObserveKey(
				txVaultPubKey(tx),
				tx.ToAddress.String(),
				tx.Memo,
			)
			observeKeys = []string{logicalHash}
		}
		return xmrSigningIdentity{
			CacheHash:   baseHash,
			LogicalHash: logicalHash,
			ObserveKeys: observeKeys,
		}, nil
	}

	signingID := strings.TrimSpace(tx.SigningID)
	if signingID == "" {
		return xmrSigningIdentity{}, errors.New("xmr consolidation requires a stable signing id")
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"thorchain-xmr-consolidation-signing-v1",
		baseHash,
		signingID,
	}, "\n")))
	operationHash := fmt.Sprintf("%X", digest)
	if txVaultPubKey(tx).IsEmpty() {
		return xmrSigningIdentity{}, errors.New("xmr consolidation requires a FROST vault pubkey")
	}
	return xmrSigningIdentity{
		CacheHash:   operationHash,
		LogicalHash: operationHash,
		ObserveKeys: []string{xmrConsolidationObserveKey(txVaultPubKey(tx))},
	}, nil
}

func xmrScheduledObserveKeys(tx stypes.TxOutItem) []string {
	return []string{xmrScheduledObserveKey(
		txVaultPubKey(tx), tx.ToAddress.String(), tx.Coins,
		stypes.XMRWireMemo(tx.Memo, tx.OriginalMemo),
	)}
}

// xmrScheduledLogicalHash is the vault-independent semantic identity of a
// scheduled outbound. Keep the legacy CacheHash separate: memoless TxOutItems
// intentionally have an empty wire Memo, while their OriginalMemo carries the
// OUT/REFUND identity used by THORChain and by signer-created observations.
// Binding GetMemo here makes both forms select one durable claim across vault
// reschedules. RAGNAROK is covered by its full height-bearing memo even though
// it has no inbound transaction ID.
func xmrScheduledLogicalHash(chain common.Chain, toAddress string, coins common.Coins, semanticMemo string) string {
	return xmrObserveKey(
		"scheduled-logical-v2",
		chain.String(),
		toAddress,
		coins.String(),
		semanticMemo,
	)
}

func xmrScheduledObserveKey(
	vaultPubKey common.PubKey,
	toAddress string,
	coins common.Coins,
	observedMemo string,
) string {
	return xmrObserveKey(
		"scheduled",
		vaultPubKey.String(),
		toAddress,
		coins.String(),
		observedMemo,
	)
}

func xmrMigrationObserveKey(vaultPubKey common.PubKey, toAddress, observedMemo string) string {
	return xmrObserveKey("migrate", vaultPubKey.String(), toAddress, observedMemo)
}

func xmrConsolidationObserveKey(vaultPubKey common.PubKey) string {
	return xmrObserveKey("consolidate", vaultPubKey.String())
}

func xmrObserveKey(parts ...string) string {
	canonical := make([]string, 0, len(parts)+1)
	canonical = append(canonical, "thorchain-xmr-observation-claim-v1")
	for _, part := range parts {
		// Monero addresses and memo payloads are case-sensitive. Callers supply
		// canonical vault/type fields; preserve every wire field byte-for-byte
		// apart from surrounding transport whitespace.
		canonical = append(canonical, strings.TrimSpace(part))
	}
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\n")))
	return fmt.Sprintf("%X", digest)
}

func coinbaseThorchain() uint64 {
	coinbaseThorchain := uint64(defaultCoinbaseValue / moneroAtomicToThorchain)
	if coinbaseThorchain == 0 {
		return 1
	}
	return coinbaseThorchain
}

func confirmationRequired(totalValue, confValue cosmos.Uint) uint64 {
	if confValue.IsZero() {
		return 0
	}
	return totalValue.Quo(confValue).Uint64()
}

func thorchainAmountToAtomic(amount cosmos.Uint) (uint64, error) {
	if amount.IsNil() {
		return 0, fmt.Errorf("xmr outbound amount is nil")
	}

	amountAtomic := amount.MulUint64(moneroAtomicToThorchain)
	amountAtomicBI := amountAtomic.BigInt()
	if amountAtomicBI == nil || !amountAtomicBI.IsUint64() {
		return 0, fmt.Errorf("xmr outbound amount %s overflows uint64 after atomic conversion", amount.String())
	}

	return amountAtomicBI.Uint64(), nil
}

func (c *Client) getVaultByPubKey(vaultPubKey common.PubKey) (ttypes.Vault, error) {
	if vaultPubKey.IsEmpty() {
		return ttypes.Vault{}, fmt.Errorf("vault pubkey is empty")
	}
	vault, err := c.thorchainBridge.GetVault(vaultPubKey.String())
	if err == nil {
		return vault, nil
	}

	vaults, asgardErr := c.thorchainBridge.GetAsgards()
	if asgardErr != nil {
		return ttypes.Vault{}, asgardErr
	}
	for _, item := range vaults {
		if item.PubKeyFrost.Equals(vaultPubKey) {
			return item, nil
		}
	}
	return ttypes.Vault{}, err
}
