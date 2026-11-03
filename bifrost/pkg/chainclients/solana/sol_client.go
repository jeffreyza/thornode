package solana

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/mr-tron/base58"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"

	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

// signerCacheExpiryOverride, when > 0, replaces the config-sourced signer-cache
// expiry. Only the mocknet build sets it (see sol_client_mocknet.go), shortening
// the window so simulations can retry dropped broadcasts quickly. Mainnet leaves
// it zero and sources the window from config (signer_cache_expiry, default 2h).
var signerCacheExpiryOverride time.Duration

// getSignerCacheExpiry returns the window after a broadcast during which SignTx
// respects the cache unconditionally, without an RPC lookup. Past this window,
// SignTx verifies the recorded signature on chain and only re-signs if the RPC
// definitively reports it absent. Both conditions must hold — expiry alone never
// invalidates, because thorchain observation consensus can lag actual Solana
// inclusion and re-signing a landed-but-not-yet-observed tx would double-spend.
//
// The configured 2h (config signer_cache_expiry) is well past Solana's ~45s
// blockhash validity (MAX_PROCESSING_AGE = 150 blocks at ~300ms) and generous against
// prolonged scanner or consensus lag.
func (c *SOLClient) getSignerCacheExpiry() time.Duration {
	if signerCacheExpiryOverride > 0 {
		return signerCacheExpiryOverride
	}
	return c.cfg.SignerCacheExpiry
}

// broadcastLookupDelay is the time to wait after a broadcast error before
// querying the chain to see if the transaction landed anyway. Exposed as a
// variable so tests can override it.
var broadcastLookupDelay = 3 * time.Second

// SOLClient is a client for the Solana blockchain
type SOLClient struct {
	logger              zerolog.Logger
	cfg                 config.BifrostChainConfiguration
	localPubKey         string
	k                   *cosmoscryptoed25519.PrivKey
	solScanner          *SOLScanner
	bridge              thorclient.ThorchainBridge
	poolMgr             thorclient.PoolManager
	tssKeyManager       tss.ThorchainKeyManager
	pubkeyMgr           pubkeymanager.PubKeyValidator
	wg                  *sync.WaitGroup
	stopchan            chan struct{}
	globalSolvencyQueue chan stypes.Solvency
	signerCacheManager  *signercache.CacheManager
	rpcClient           *rpc.SolRPC
	m                   *metrics.Metrics

	// keepAliveThorMultiple is the last keep-alive cadence boundary this node acted
	// on. The trigger + keysign anchor are driven off the shared THORChain block
	// height (not a per-node wall clock or Solana slot) so every member crosses the
	// same boundary and hands the keysign an identical, valid THORChain height —
	// converging the party the way scheduled outbounds do via their consensus
	// txOut.Height. Guarded by the mutex since the loop reads and writes it.
	keepAliveThorMultipleMu sync.Mutex
	keepAliveThorMultiple   int64

	// nonceProvisionThorMultiple is the last durable-nonce provisioning cadence
	// boundary this node acted on (its own, finer cadence than keep-alive). Same
	// shared-THORChain-height convergence discipline. Guarded by its mutex.
	nonceProvisionThorMultipleMu sync.Mutex
	nonceProvisionThorMultiple   int64

	// vaultLocks holds a per-vault mutex, shared by the outbound signer (via the
	// GetVaultLock hook) and durable-nonce create/close, so this node never runs an
	// outbound keysign and a nonce keysign for the same vault concurrently (no local
	// TSS-party contention). Mirrors the UTXO consolidate vault lock.
	vaultLocks sync.Map // map[string]*sync.Mutex, keyed by vault pubkey

	// durableRejects remembers outbounds this node's pre-sign filters refused to sign
	// (key -> expiry), so the serialize gate does not hold later siblings behind them.
	durableRejects sync.Map

	// nonceReader overrides the chain reads of durable-nonce re-attestation (tests);
	// nil uses rpcClient.
	nonceReader solNonceAccountReader

	// solvencySlotMultiple is the last reported solvency cadence boundary. It is
	// read and written from two goroutines — the block scanner's solvency reporter
	// and the SolvencyCheckRunner, both of which reach ReportSolvency — so every
	// access goes through solvencySlotMultipleMu.
	solvencySlotMultipleMu sync.Mutex
	solvencySlotMultiple   int64

	// reportInFlight gates ReportSolvency so only one report runs at a time. The
	// two callers can otherwise both pass the boundary guard and either emit a
	// duplicate solvency Id (self-slashed on chain) or redundantly fetch balances.
	// The loser TryLocks, fails, and returns immediately (no stall); the winner
	// still writes the counter at the end, preserving retry-on-failure.
	reportInFlight sync.Mutex

	// consensusHashes memoizes successful consensusBlockHash derivations by keysign
	// height. A successful height -> (blockhash, slot) mapping is immutable (CometBFT
	// block time + finalized SOL slots never change), so every SignTx for the same
	// height — the other outbounds in that TxOut, retries, a second vault, the
	// keep-alive goroutine — reuses one derivation instead of repeating the
	// ~14-call RPC search. Only successes are stored: errors and the empty-hash
	// abstention are node-local and time-varying, and the per-attempt
	// IsBlockhashValid gate stays outside the cache. consensusHashGroup coalesces
	// concurrent first lookups for one height into a single in-flight derivation.
	consensusHashMu     sync.Mutex
	consensusHashes     map[int64]consensusBlockHashEntry
	consensusHashNewest int64
	consensusHashGroup  singleflight.Group

	// resignDeadHash remembers blockhashes whose expiry was confirmed at or
	// beyond their anchor slot. SignTx can then drop other items using the same
	// expired hash without repeating keysign, even if RPC backends disagree.
	resignDeadMu   sync.Mutex
	resignDeadHash map[string]struct{}
}

// consensusBlockHashEntry is one immutable keysign-height derivation.
type consensusBlockHashEntry struct {
	hash string
	slot int64
}

// NewSOLClient creates a new instance of a SOLClient
func NewSOLClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	server *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
	pubkeyMgr pubkeymanager.PubKeyValidator,
	poolMgr thorclient.PoolManager,
) (*SOLClient, error) {
	// check required arguments
	if thorKeys == nil {
		return nil, errors.New("thor keys empty")
	}
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	if pubkeyMgr == nil {
		return nil, errors.New("pubkey manager is nil")
	}
	if poolMgr == nil {
		return nil, errors.New("pool manager is nil")
	}

	// // create keys
	tssKm, err := tss.NewKeySign(server, bridge)
	if err != nil {
		return nil, fmt.Errorf("failed to create tss signer: %w", err)
	}
	priv, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}

	pub, ok := priv.PubKey().(*cosmoscryptoed25519.PubKey)
	if !ok {
		return nil, errors.New("failed to cast pubkey to ed25519")
	}
	formattedPub := base58.Encode(pub.Bytes())

	// create rpc clients
	rpcClient := rpc.NewSolRPC(cfg.RPCHost, cfg.BlockScanner.HTTPRequestTimeout)

	c := &SOLClient{
		logger:        log.With().Str("module", "sol").Stringer("chain", cfg.ChainID).Logger(),
		cfg:           cfg,
		localPubKey:   formattedPub,
		k:             priv,
		bridge:        bridge,
		poolMgr:       poolMgr,
		tssKeyManager: tssKm,
		pubkeyMgr:     pubkeyMgr,
		wg:            &sync.WaitGroup{},
		stopchan:      make(chan struct{}),
		rpcClient:     rpcClient,
		m:             m,
	}

	// initialize storage
	var path string // if not set later, will in memory storage
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}
	storage, err := blockscanner.NewBlockScannerStorageSolana(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return c, fmt.Errorf("fail to create blockscanner storage: %w", err)
	}
	signerCacheManager, err := signercache.NewSignerCacheManager(storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager: %w", err)
	}
	c.signerCacheManager = signerCacheManager

	// create block scanner
	c.solScanner, err = NewSOLScanner(
		c.stopchan,
		c.cfg.BlockScanner,
		storage,
		bridge,
		m,
		rpcClient,
		pubkeyMgr,
		c.ReportSolvency,
		signerCacheManager,
	)
	if err != nil {
		return c, fmt.Errorf("fail to create solana scanner: %w", err)
	}

	return c, nil
}

// Start starts the chain client with the given queues.
func (c *SOLClient) Start(
	globalTxsQueue chan stypes.TxIn,
	globalErrataQueue chan stypes.ErrataBlock,
	globalSolvencyQueue chan stypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.globalSolvencyQueue = globalSolvencyQueue
	c.solScanner.globalNetworkFeeQueue = globalNetworkFeeQueue
	c.solScanner.globalTxsQueue = globalTxsQueue
	c.tssKeyManager.Start()
	go c.solScanner.Start()
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.bridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
	c.wg.Add(1)
	go c.keepAliveLoop()
}

// Stop stops the chain client.
func (c *SOLClient) Stop() {
	c.tssKeyManager.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

// IsBlockScannerHealthy returns true if the block scanner is healthy.
func (c *SOLClient) IsBlockScannerHealthy() bool {
	return c.solScanner.IsHealthy()
}

// --------------------------------- config ---------------------------------

// GetConfig returns the chain configuration.
func (c *SOLClient) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

// GetChain returns the chain.
func (c *SOLClient) GetChain() common.Chain {
	return common.SOLChain
}

// --------------------------------- status ---------------------------------

// GetHeight returns the current height of the chain.
func (c *SOLClient) GetHeight() (int64, error) {
	return c.solScanner.GetHeight()
}

// GetBlockScannerHeight returns blockscanner height
func (c *SOLClient) GetBlockScannerHeight() (int64, error) {
	return c.solScanner.ScanHeight()
}

// RollbackBlockScanner rolls back the block scanner to the last observed block
func (c *SOLClient) RollbackBlockScanner() error {
	// rollback disabled for solana, as we scan by transaction signatures, not blocks heights.
	// a rollback to fetch all transactions for a vault could cause many duplicate observations and likewise the slashes for them.

	// if determined this is absolutely necessary, we could query all transactions for a vault address that exist on the node,
	// and then determine which transaction signature is the one immediately behind the desired rollback height, then use that
	// for queryLastSig passed to the SOLScanner.scanVault method.
	return nil
}

func (c *SOLClient) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadCasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadCasted, err
}

// --------------------------------- addresses ---------------------------------

// GetAddress returns the address for the given public key.
func (c *SOLClient) GetAddress(poolPubKey common.PubKey) string {
	addr, err := poolPubKey.GetAddress(c.cfg.ChainID)
	if err != nil {
		c.logger.Error().Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get pool address")
		return ""
	}
	return addr.String()
}

// --------------------------------- balances ---------------------------------

// GetAccount returns the balance of the provided address.
func (c *SOLClient) GetAccount(pk common.PubKey, height *big.Int) (common.Account, error) {
	addr, err := pk.GetAddress(common.SOLChain)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	return c.GetAccountByAddress(addr.String(), height)
}

// GetAccountByAddress return account information
func (c *SOLClient) GetAccountByAddress(addr string, height *big.Int) (common.Account, error) {
	lamportsBalance, err := c.rpcClient.GetBalance(addr, "finalized", 0)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get balance: %w", err)
	}

	// get amount
	amount := cosmos.NewUintFromBigInt(lamportsBalance)
	amount = amount.Quo(cosmos.NewUint(10)) // 1e9 -> 1e8

	coins := common.Coins{
		common.NewCoin(common.SOLAsset, amount),
	}

	return common.Account{
		Coins: coins,
	}, nil
}

// --------------------------------- solvency ---------------------------------

// GetVaultLock returns the per-vault mutex the outbound signer (sign.go's vaultLocker
// hook) acquires around sign+broadcast. Durable-nonce create/close acquire the SAME
// mutex, so an outbound and a nonce keysign for one vault never run concurrently on
// this node — no local TSS-party contention (mirrors the UTXO consolidate vault lock).
func (c *SOLClient) GetVaultLock(vaultPubKey string) *sync.Mutex {
	v, _ := c.vaultLocks.LoadOrStore(vaultPubKey, &sync.Mutex{})
	mu, _ := v.(*sync.Mutex)
	return mu
}

// GetGasEstimateForSolvency returns the current gas fee estimate used by solvency checks.
func (c *SOLClient) GetGasEstimateForSolvency() cosmos.Uint {
	nativeCurrentFee := cosmos.NewUint(3).
		MulUint64(c.cfg.BlockScanner.MaxGasLimit).
		MulUint64(c.solScanner.lastFeeRate.Load())
	return c.cfg.ChainID.NativeGasToThorchain(nativeCurrentFee)
}

// ReportSolvency reports solvency once per configured solvency blocks.
func (c *SOLClient) ReportSolvency(height int64) error {
	reportSlotMultiple := c.nextSolvencyReportSlotMultiple(height)
	if !c.reportInFlight.TryLock() {
		return nil
	}
	defer c.reportInFlight.Unlock()
	if reportSlotMultiple <= c.getSolvencySlotMultiple() {
		return nil
	}
	reportHeight := reportSlotMultiple * c.cfg.SolvencyBlocks

	// When the block scanner is unhealthy, only report from the auto-unhalt
	// SolvencyCheckRunner. The scan loop stores the slot it is processing in
	// currentScanSlot just before calling here, while the runner passes the live
	// chain tip (GetHeight); so a scan-driven call matches currentScanSlot and is
	// skipped, deferring to the runner's up-to-date report.
	if !c.IsBlockScannerHealthy() && height == c.solScanner.CurrentScanSlot() {
		return nil
	}

	// fetch all asgard vaults
	asgardVaults, err := c.bridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards, err: %w", err)
	}

	currentFeeRate := c.GetGasEstimateForSolvency()

	// report insolvent asgard vaults,
	// or else all if the chain is halted and all are solvent
	chainVaults := asgardVaults[:0]
	for _, asgard := range asgardVaults {
		if asgard.HasFundsForChain(c.cfg.ChainID) {
			chainVaults = append(chainVaults, asgard)
		}
	}

	msgs := make([]stypes.Solvency, 0, len(chainVaults))
	solventMsgs := make([]stypes.Solvency, 0, len(chainVaults))
	processedVaults := 0
	for _, v := range chainVaults {
		if v.PubKeyEddsa.IsEmpty() {
			c.logger.Error().Msg("asgard vault pubkey is empty, skipping solvency check")
			continue
		}
		acct, err := c.GetAccount(v.PubKeyEddsa, nil)
		if err != nil {
			c.logger.Err(err).Msg("fail to get account balance")
			continue
		}
		msg := stypes.Solvency{
			Height: reportHeight,
			Chain:  c.cfg.ChainID,
			PubKey: v.PubKey, // or v.PubKeyEddsa
			Coins:  acct.Coins,
		}
		processedVaults++

		if runners.IsVaultSolvent(c.cfg.ChainID, acct, v, currentFeeRate) {
			solventMsgs = append(solventMsgs, msg) // Solvent-vault message
			continue
		}
		msgs = append(msgs, msg) // Insolvent-vault message
	}

	// Only if THORChain itself has this chain halted or solvency-halted, this node's block scanner
	// is paused for it (unhealthy), and all chain-specific vaults are solvent, report that all the
	// chain-specific vaults are solvent. That "all clear" exists to lift a network SolvencyHalt
	// (see SolvencyCheckRunner), so the network's halt state is the gate, not this node's own
	// block-scanner health: a scanner that is unhealthy for a local reason (RPC trouble, a stalled
	// fetch) must stay silent here, because an all-clear only one node sends can never reach
	// consensus and each one costs that node an unrefunded observation slash point. The paused
	// scanner is still required alongside the halt: the scanner re-reads the pause mimirs only once
	// per THORChain block, and reads a mimir error as not paused, so just after a halt takes effect
	// a healthy scanner can still reach here from the scan path. Its all-clear would carry the
	// scanned height rather than the runner's, a different voter, and so would be a lone vote too.
	// On a bridge error stay silent; the runner retries.
	// If there are any insolvent vaults, report only them.
	// Not reporting both solvent and insolvent vaults is to avoid noise (spam):
	// Reporting both could halt-and-unhalt SolvencyHalt in the same THOR block
	// (resetting its height), plus making it harder to know at a glance from solvency reports which vaults were insolvent.
	solvent := false
	allVaultsProcessed := len(chainVaults) > 0 && processedVaults == len(chainVaults)
	halted, haltErr := runners.IsHaltedOnChain(c.bridge, c.cfg.ChainID)
	if haltErr != nil {
		c.logger.Err(haltErr).Msg("fail to read chain halt state, skipping solvency all-clear")
	}
	if halted && !c.IsBlockScannerHealthy() && allVaultsProcessed && len(solventMsgs) == processedVaults {
		msgs = solventMsgs
		solvent = true
	}

	for i := range msgs {
		c.logger.Info().
			Stringer("asgard", msgs[i].PubKey).
			Interface("coins", msgs[i].Coins).
			Bool("solvent", solvent).
			Msg("reporting solvency")

		// send solvency to thorchain via global queue consumed by the observer
		select {
		case c.globalSolvencyQueue <- msgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msg("fail to send solvency info to thorchain, timeout")
		}
	}
	c.advanceSolvencySlotMultiple(reportSlotMultiple)
	return nil
}

// ShouldReportSolvency returns true if the given height is a solvency report height.
func (c *SOLClient) ShouldReportSolvency(height int64) bool {
	return c.nextSolvencyReportSlotMultiple(height) > c.getSolvencySlotMultiple()
}

func (c *SOLClient) nextSolvencyReportSlotMultiple(height int64) int64 {
	current := c.getSolvencySlotMultiple()
	if c.cfg.SolvencyBlocks <= 0 {
		return current
	}

	crossedMultiple := height / c.cfg.SolvencyBlocks
	if height%c.cfg.SolvencyBlocks == 0 {
		crossedMultiple--
	}
	if crossedMultiple <= current {
		return current
	}

	// Fresh nodes should not replay historical SOL solvency windows from zero.
	// After initialization, advance one cadence boundary at a time so nodes with
	// different tips still report the same next solvency height.
	if current == 0 && crossedMultiple > 1 {
		return crossedMultiple
	}
	return current + 1
}

// getSolvencySlotMultiple returns the last reported solvency cadence boundary,
// guarded by solvencySlotMultipleMu.
func (c *SOLClient) getSolvencySlotMultiple() int64 {
	c.solvencySlotMultipleMu.Lock()
	defer c.solvencySlotMultipleMu.Unlock()
	return c.solvencySlotMultiple
}

// advanceSolvencySlotMultiple records v as the last reported solvency cadence
// boundary, but only ever moves the counter forward. The check-then-set in
// ReportSolvency is not atomic, so two reporters racing on the fresh-node
// catch-up path (current == 0) can compute different boundaries from their own
// heights; a plain write would let the lower one regress the counter and
// re-report already-reported boundaries. Guarded by solvencySlotMultipleMu.
func (c *SOLClient) advanceSolvencySlotMultiple(v int64) {
	c.solvencySlotMultipleMu.Lock()
	defer c.solvencySlotMultipleMu.Unlock()
	if v > c.solvencySlotMultiple {
		c.solvencySlotMultiple = v
	}
}

// --------------------------------- sign ---------------------------------

// dropForReschedule abandons an outbound whose blockhash can never be usable at this
// keysign height, counting the reason and logging ev. The (nil,nil,nil,nil) no-op is
// read by sign.go as "nothing to broadcast", so the signer clears the item and
// THORChain reschedules it at a FRESH height. Returning an ERROR instead would RETAIN
// it, and a retained item stays the lowest-height SOL outbound for its vault: the
// signer pipeline (lowest-height-first, one item per {vault,chain} per pass) would let
// it re-claim the vault's single signing slot every pass and starve every later SOL
// outbound for that vault. Callers pass a partially-built event so each drop can carry
// its own fields; tx and the abstain metric are attached here.
func (c *SOLClient) dropForReschedule(tx stypes.TxOutItem, reason string, ev *zerolog.Event, msg string) ([]byte, []byte, *stypes.TxInItem, error) {
	ev.Interface("tx", tx).Msg(msg)
	c.countAbstain(reason)
	return nil, nil, nil, nil
}

// countAbstain increments the SOL keysign-abstain metric by reason, so a fleet-wide
// stall (all nodes failing the blockhash gate) is observable rather than looking like
// routine keysign non-selection. Nil-safe: directly-constructed test clients omit m.
func (c *SOLClient) countAbstain(reason string) {
	if c.m == nil {
		return
	}
	if cv := c.m.GetCounterVec(metrics.SolKeysignAbstain); cv != nil {
		cv.WithLabelValues(reason).Inc()
	}
}

// SignTx returns the signed transaction.
func (c *SOLClient) SignTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if tx.Chain != common.SOLChain {
		return nil, nil, nil, errors.New("tx chain is not solana")
	}
	if tx.ToAddress.IsEmpty() {
		return nil, nil, nil, errors.New("to address is empty")
	}
	if tx.VaultPubKeyEddsa.IsEmpty() {
		return nil, nil, nil, errors.New("vault public key is empty")
	}

	// Solana is the canonical reference for the cache-and-verify pattern. Unlike
	// the EVM/UTXO/Cosmos/TRON/XRP clients (which gate the verification path
	// behind a SkipSignerCache<Chain> mimir), Solana enables verification
	// unconditionally because the broadcast's blockhash already constrains
	// re-sign safety to a tight window (~45s).
	verifyFn := func(sig string) signercache.LookupResult {
		result, lookupErr := c.rpcClient.GetTransactionConfirmed(sig)
		switch {
		case lookupErr != nil:
			return signercache.LookupAmbiguous
		case result.Slot > 0 && result.Meta.Err == nil:
			return signercache.LookupFound
		case result.Slot > 0:
			// Landed on chain but FAILED at execution (meta.err set, e.g.
			// "insufficient lamports"): the fee was burned but the transfer never
			// happened, so the outbound is unfulfilled. Presence on chain is not
			// enough — only a SUCCESSFUL broadcast is authoritative. Treat a failed
			// landing like a dropped one (LookupNotFound) so the cache manager clears
			// it and expiry+verify re-signs. Without this the failed sig keeps
			// verifying as "found" and permanently wedges the outbound (#3089).
			c.logger.Warn().Str("sig", sig).Interface("meta_err", result.Meta.Err).Interface("tx", tx).
				Msg("cached broadcast failed on chain, re-signing")
			return signercache.LookupNotFound
		default:
			return signercache.LookupNotFound
		}
	}
	check := c.signerCacheManager.SignTxCacheCheck(tx.CacheHash(), func() bool { return true }, verifyFn)
	if check.Phase != signercache.PhaseNoEntry {
		ev := c.logger.Info()
		if check.Noteworthy() {
			ev = c.logger.Warn()
		}
		ev.Stringer("phase", check.Phase).
			Str("sig", check.Sig).
			Interface("tx", tx).
			Msg(check.Phase.String())
	}
	if check.SkipSign {
		return nil, nil, nil, nil
	}

	// GetMemo returns OriginalMemo for memoless outbounds
	// For truly memoless transactions (no Memo or OriginalMemo), default to TxOutbound
	memoForParsing := tx.GetMemo()

	// Durable-nonce path. A vault whose durable nonce is committed on THORChain signs
	// every non-excluded SOL outbound durably (AdvanceNonceAccount as instruction[0],
	// the stored nonce baked in place of a recent blockhash), so it cannot expire
	// during the multi-party TSS keysign+broadcast window. The nonce is read from the
	// value THORChain committed, at the keysign height — no mimir gate and no Solana
	// account read at sign time — so every keysign party builds identical bytes. (The
	// SOLDurableNonce mimir gates only the provisioning of new nonce accounts.)
	//
	// Invariant I (no mixing): once a vault's nonce is committed, never fall back to a
	// recent blockhash for it. A legacy copy does not consume the nonce, so a legacy
	// copy and a still-valid durable copy of the same outbound could BOTH land. An
	// unreadable committed value therefore RETAINS the outbound instead. A vault with
	// no committed nonce (never provisioned) uses the recent-blockhash path below, as
	// do excluded recipients (self-send, the vault's own nonce account — see
	// durableDestinationExcluded), which are never durable so never mix.
	if useSolDurableNonce && !c.durableDestinationExcluded(tx) {
		committed, known, cErr := c.committedSolVaultNonce(tx, height)
		if cErr != nil {
			c.countAbstain("durable_nonce_read_error")
			return nil, nil, nil, fmt.Errorf("fail to read committed durable nonce; retaining outbound: %w", cErr)
		}
		if known {
			// Consumed-nonce recovery (B5). If the vault's committed nonce advanced
			// AFTER this outbound's keysign height, the value committed at that height —
			// the only one this outbound can be signed with here — is already consumed,
			// so any copy signed now, or a stored signed copy being retried, is dead.
			// Drop it for reschedule instead of retrying dead bytes and head-of-line
			// blocking the vault's later SOL outbounds. THORChain re-issues it at a new
			// height (signed with the advanced nonce) once LackSigning proves no copy of
			// it landed (solDurableReissue). The drop never re-signs, so it cannot pay
			// twice. The latest read is per node and liveness-only: a failed read just
			// proceeds (a dead copy cannot land either).
			if c.committedNonceAdvancedSince(tx, height) {
				return c.dropForReschedule(tx, "durable_nonce_consumed",
					c.logger.Warn(),
					"committed durable nonce advanced after this outbound's keysign height, dropping for reschedule")
			}
			// Serialize-with-finality (N2): only the lowest-height not-yet-observed
			// SOL outbound for this vault may sign durable, so at most one durable tx
			// is in flight per vault. A higher-height sibling WAITS (retain + retry)
			// until the earlier one is observed and the committed nonce advances.
			if !c.durableSerializeAllows(tx, height) {
				c.countAbstain("durable_nonce_serialize_wait")
				return nil, nil, nil, errors.New("durable-nonce serialize: waiting for a lower-height in-flight SOL outbound for this vault to be observed")
			}
			nonceAccount, nonceAuthority, kErr := c.committedNonceKeys(tx, committed)
			if kErr != nil {
				c.countAbstain("durable_nonce_error")
				return nil, nil, nil, fmt.Errorf("invalid committed durable nonce; retaining outbound: %w", kErr)
			}
			solTx, buildErr := c.CreateTxWithNonce(tx, memoForParsing, committed.Nonce, nonceAccount, nonceAuthority)
			if buildErr != nil {
				return nil, nil, nil, fmt.Errorf("fail to create durable-nonce tx: %w", buildErr)
			}
			// Pre-sign filters: never sign a durable tx that would land and fail (it
			// would consume the nonce unobserved and wedge the vault).
			if reason := c.durablePreSignReject(tx, solTx); reason != "" {
				c.markDurableRejected(tx, height)
				return c.dropForReschedule(tx, reason, c.logger.Warn(),
					"durable outbound would fail on-chain; not signing, dropping for reschedule")
			}
			return c.finishSign(solTx, tx, memoForParsing, height)
		}
	}

	// Deterministic blockhash: derive it from the CONSENSUS keysign height (txOut.Height
	// for scheduled outbounds, the shared keep-alive anchor otherwise) instead of the
	// per-node scanner, so every member signs identical bytes and converges on ONE TSS
	// party — fixing the scanner-blockhash desync that fragmented SOL keysigns. Same rule
	// the keep-alive already follows: the signed message is driven off the shared
	// THORChain height; per-node values only gate participation, never the bytes.
	// Memoized by height: the mapping is immutable, so the other outbounds in this
	// TxOut, retries, other vaults and the keep-alive share one RPC derivation.
	recentBlockhash, anchorSlot, err := c.cachedConsensusBlockHash(height)
	if err != nil {
		if errors.Is(err, errAnchorExpired) {
			// This height's anchor is provably past MAX_PROCESSING_AGE, so no derivation
			// at it can produce a hash that lands — including the case where the search
			// cannot complete at all and so never reaches the IsBlockhashValid gate
			// below (#3344). Escalate to a fresh-height reschedule instead of retrying
			// for the rest of the signing window.
			return c.dropForReschedule(tx, "anchor_expired_dropped",
				c.logger.Warn().Err(err),
				"SOL consensus anchor is past MAX_PROCESSING_AGE, dropping for reschedule")
		}
		// RPC failures / probe exhaustion land here — the most likely abstain trigger,
		// so count it too (the signer retains + retries on this error).
		c.countAbstain("derive_error")
		return nil, nil, nil, fmt.Errorf("fail to derive consensus blockhash: %w", err)
	}
	if recentBlockhash == "" {
		// No finalized consensus block for this height yet -> return an ERROR so the
		// signer RETAINS the outbound and retries next cycle (matching the pre-fix
		// CreateTx-error behaviour). A (nil,nil,nil,nil) no-op is read as a successful
		// broadcast (sign.go:signAndBroadcast len(signedTx)==0 -> nil err) and the item
		// is Remove()d from the store, so a transient miss would DROP the outbound until
		// THORChain reschedules ~300 blocks later. Retaining+retrying is the fast path;
		// once consensusBlockHash can PROVE the anchor is past MAX_PROCESSING_AGE it
		// returns errAnchorExpired above and the retry loop ends in a drop instead.
		c.logger.Info().Interface("tx", tx).Msg("no consensus blockhash for SOL sign yet, retaining outbound for retry")
		c.countAbstain("no_consensus_blockhash")
		return nil, nil, nil, errors.New("no consensus blockhash for SOL height yet; retaining outbound for retry until reschedule")
	}
	// Only a confirmed expiry at broadcast can mark this hash dead. A preflight
	// rejection alone may come from a lagging backend and must not suppress
	// participation in other outbounds that share the same consensus hash.
	if c.isResignDeadHash(recentBlockhash) {
		return c.dropForReschedule(tx, "blockhash_dead_resign_dropped",
			c.logger.Warn().Str("blockhash", recentBlockhash),
			"consensus blockhash expiry already confirmed, dropping for reschedule")
	}
	// Per-node participation gate (never touches the signed bytes — the blockhash
	// above is deterministic): is the derived blockhash still usable against the
	// PROCESSING tip, i.e. the bank a broadcast tx is actually validated against?
	// The finalized slot lags ~13s (~32 slots) behind processed, so an age check
	// against finalized understates the hash age and can admit a hash already past
	// Solana's ~151-block MAX_PROCESSING_AGE (a tx that can never land). Ask the
	// node directly at "processed" commitment, and fail CLOSED on error.
	// Pass anchorSlot as minContextSlot so a LAGGING backend (its processing bank
	// behind the network) REJECTS the request instead of answering false for a hash
	// that is still live on the tip — otherwise the !valid drop below would discard a
	// perfectly good outbound. A lagging backend then surfaces as vErr -> retain.
	valid, vErr := c.rpcClient.IsBlockhashValid(recentBlockhash, "processed", uint64(anchorSlot))
	if vErr != nil {
		c.logger.Warn().Err(vErr).Int64("anchor_slot", anchorSlot).Interface("tx", tx).
			Msg("cannot verify SOL blockhash validity, retaining outbound for retry (fail closed)")
		c.countAbstain("blockhash_valid_rpc_error")
		return nil, nil, nil, fmt.Errorf("cannot verify SOL blockhash validity (fail closed), retaining outbound for retry: %w", vErr)
	}
	if !valid {
		// PERMANENT for this fixed height: the blockhash is a pure function of
		// `height`, so re-deriving yields the same, now-expired hash — it can never
		// become valid again at this height. This differs from the two TRANSIENT
		// cases above (no consensus block yet / IsBlockhashValid RPC error), which
		// return an error to RETAIN and retry. Retaining here would be actively
		// harmful: the item stays the lowest-height SOL outbound for its vault, and
		// the signer pipeline (lowest-height-first, one item per {vault,chain} per
		// pass) would let it re-claim the vault's single signing slot every pass and
		// STARVE every later SOL outbound for that vault until reschedule. Return the
		// (nil,nil,nil,nil) no-op so the signer clears it; THORChain reschedules at a
		// FRESH height where a live hash derives. (The transient-blip drop the earlier
		// review flagged is the IsBlockhashValid-ERROR path above — that stays retained.)
		return c.dropForReschedule(tx, "blockhash_expired_dropped",
			c.logger.Warn().Int64("anchor_slot", anchorSlot),
			"consensus blockhash expired at processed tip (permanent for this height), dropping for reschedule")
	}

	solTx, err := c.CreateTx(tx, memoForParsing, recentBlockhash)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to create tx: %w", err)
	}

	return c.finishSign(solTx, tx, memoForParsing, height)
}

// finishSign serializes the checkpoint, runs the multi-party TSS keysign, and
// assembles the observation. It is shared by the recent-blockhash path and the
// durable-nonce path so both produce identical downstream behaviour; the
// only difference between the two is how solTx was built (recent blockhash vs.
// AdvanceNonceAccount + stored durable nonce).
func (c *SOLClient) finishSign(solTx types.Transaction, tx stypes.TxOutItem, memoForParsing string, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	checkPointBytes, err := solTx.Message.Serialize()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to serialize checkpoint bytes: %w", err)
	}

	signedTx, err := c.signTransaction(solTx, tx, height)
	if err != nil {
		if errors.Is(err, tss.ErrNotSelectedForKeysign) {
			return nil, nil, nil, nil
		}
		// Forward blamed TSS failures to Thornode so EdDSA outbounds participate
		// in the same slash/freeze handling as the secp256k1 signer paths.
		var keysignError tss.KeysignError
		if errors.As(err, &keysignError) && len(keysignError.Blame.BlameNodes) > 0 {
			txID, errPostKeysignFail := c.bridge.PostKeysignFailure(keysignError.Blame, height, tx.GetMemo(), tx.Coins, tx.VaultPubKeyEddsa)
			if errPostKeysignFail != nil {
				return checkPointBytes, nil, nil, multierror.Append(err, errPostKeysignFail)
			}
			c.logger.Info().Str("tx_id", txID.String()).Msg("post keysign failure to THORChain")
		}
		return checkPointBytes, nil, nil, fmt.Errorf("fail to sign tx: %w", err)
	}

	signedBytes, err := signedTx.Serialize()
	if err != nil {
		return checkPointBytes, nil, nil, fmt.Errorf("fail to serialize signed tx: %w", err)
	}

	fromAddr, err := tx.VaultPubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil {
		return checkPointBytes, nil, nil, fmt.Errorf("fail to get vault address: %w", err)
	}

	gasCoin := tx.Chain.NativeGasToThorCoin(cosmos.NewUint(baseFeePerSigLamports))
	txIn := stypes.NewTxInItem(
		height,
		base58.Encode(signedTx.Signatures[0]),
		memoForParsing,
		fromAddr.String(),
		tx.ToAddress.String(),
		tx.Coins,
		common.Gas([]common.Coin{gasCoin}),
		tx.VaultPubKeyEddsa,
		"",
		"",
		nil,
	)

	return signedBytes, nil, txIn, nil
}

// SignTransaction takes a rpc.Transaction and signs with the pubkey
func (c *SOLClient) signTransaction(solTx types.Transaction, txOut stypes.TxOutItem, thorchainHeight int64) (types.Transaction, error) {
	message, err := solTx.Message.Serialize()
	if err != nil {
		return solTx, fmt.Errorf("failed to serialize transaction: %w", err)
	}

	var sig []byte

	parsedPub, err := txOut.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return types.Transaction{}, fmt.Errorf("failed to get address from pubkey: %w", err)
	}

	if parsedPub.String() == c.localPubKey {
		sig, err = c.k.Sign(message)
		if err != nil {
			return types.Transaction{}, fmt.Errorf("failed to sign local transaction: %w", err)
		}
	} else {
		sig, _, err = c.tssKeyManager.RemoteSign(message, common.SigningAlgoEd25519, txOut.VaultPubKeyEddsa.String(), thorchainHeight)
		if err != nil {
			return types.Transaction{}, fmt.Errorf("failed to sign remote transaction: %w", err)
		}
		if sig == nil {
			return types.Transaction{}, tss.ErrNotSelectedForKeysign
		}
	}

	vaultAccount := solTx.Message.Accounts[0]
	if !ed25519.Verify(vaultAccount.Bytes(), message, sig) {
		return types.Transaction{}, errors.New("signature verification failed")
	}

	if err := solTx.AddSignature(sig); err != nil {
		return types.Transaction{}, fmt.Errorf("failed to add signature: %w", err)
	}

	return solTx, nil
}

// CreateTx builds a solana transfer function from a TxOutItem using the transaction
// specification (https://solana.com/docs/core/transactions#manual-sol-transfer)
const (
	// solFinalizationMarginSeconds steps the target time back from the consensus
	// THORChain block time so the chosen SOL slot is stably finalized on every
	// node's RPC (one node must not pick a slot another hasn't finalized yet). Kept
	// in whole seconds and only ever combined with time.Time.Unix() values, so there
	// is no time.Duration unit hazard.
	//
	// HARD FLOOR ~13s: Solana `finalized` commitment lags ~13s (rooted, ~31 confs),
	// so the newest finalized slot is always ~13s old. The bracket guard's upper
	// witness requires a finalized slot NEWER than target = (t - margin); if margin
	// drops below the finalization lag, target is newer than the finalization
	// frontier on EVERY node -> all abstain -> the outbound stalls. 15s = ~13s
	// finalization + ~2s fleet-lag spread, and frees ~5s vs the prior 20s toward the
	// ~45s blockhash-liveness budget for keysign (tc-vals timing, 2026-08-31). Do NOT
	// go below ~15s without first measuring the fleet's real p95 finalization lag.
	//
	// This is the DEFAULT; MimirKeySOLAnchorMarginSeconds overrides it at the keysign
	// height (see anchorMarginAtHeight), clamped to
	// [solAnchorMarginMinSeconds, solAnchorMarginMaxSeconds].
	solFinalizationMarginSeconds = int64(15)

	// solAnchorMarginMinSeconds floors the mimir override. At ~300ms slots the
	// ~32-slot finalization lag is ~9-10s, plus ~2s fleet spread; below that every
	// node abstains and SOL outbounds stall.
	solAnchorMarginMinSeconds = int64(12)

	// solAnchorMarginMaxSeconds caps the mimir override: a larger margin only eats the
	// 150-block (~40-45s) blockhash validity budget left for keysign + broadcast.
	solAnchorMarginMaxSeconds = int64(30)

	// solConsensusInitialLookbackSlots is the first getBlocks lookback below the
	// finalized tip. It only has to reach a slot OLDER than the target time; it is
	// NOT a slot-rate estimate — if the window is too shallow to bracket target the
	// search gallops it wider, so no fixed rate is baked into the answer.
	solConsensusInitialLookbackSlots = int64(400)

	// solConsensusMaxLookbackSlots caps the galloping expansion (and thus the
	// getBlocks range) so a degraded / mis-synced RPC can't drive an unbounded scan.
	solConsensusMaxLookbackSlots = int64(6400)

	// solConsensusMaxProbes bounds total getBlockTime probes per derivation so a
	// single SignTx can't hold its goroutine on RPC latency without limit. On exceed
	// we surface an error (retry) rather than a best-effort — never a wrong slot.
	solConsensusMaxProbes = 32

	// solConsensusCacheWindow bounds the consensus-blockhash memo: entries more than
	// this many THORChain heights behind the newest stored height are evicted on
	// insert. Keysign heights only move forward and a derived hash is only usable
	// for ~45s at ~300ms slots (~7-8 heights), so 128 heights (~13 min) is ample while keeping the
	// map to at most window+1 entries.
	solConsensusCacheWindow = int64(128)

	// solMaxProcessingAgeBlocks mirrors Solana's MAX_PROCESSING_AGE: a transaction is
	// rejected once more than this many BLOCKS have been produced after the block whose
	// blockhash it carries. The unit is load-bearing and is neither seconds nor slots.
	// At the nominal ~300ms slot 150 blocks is ~45s, but a cluster slowdown stretches the
	// same 150 blocks over several minutes, and skipped slots stretch them over more
	// than 150 slots. A wall-clock or slot-count stand-in therefore drifts from the real
	// rule in one direction or the other; the expiry proof in consensusBlockHash counts
	// blocks and nothing else.
	solMaxProcessingAgeBlocks = 150
)

// errTooManyProbes caps the getBlockTime fan-out inside consensusBlockHash.
var errTooManyProbes = errors.New("consensus blockhash search exceeded probe budget")

// errAnchorExpired reports a keysign height whose anchor slot S* is PROVABLY past
// Solana's MAX_PROCESSING_AGE: more than solMaxProcessingAgeBlocks finalized blocks
// have been produced after it, so no blockhash derived at this height can ever land.
// SignTx drops the outbound for a fresh-height reschedule instead of retrying for the
// rest of the signing window (#3344). Errors are never memoized, so the proof is
// re-established from live RPC data on every attempt.
var errAnchorExpired = errors.New("SOL consensus anchor is past MAX_PROCESSING_AGE")

// cachedConsensusBlockHash is consensusBlockHash memoized by keysign height. A
// successful derivation is immutable, so it is returned from memory on every later
// call for that height; concurrent first lookups for one height share a single
// in-flight derivation. Errors and the empty-hash abstention are passed through
// (and shared with any coalesced waiters) but never stored, so a transient "not
// finalized yet" or RPC failure is re-derived on the next attempt.
func (c *SOLClient) cachedConsensusBlockHash(height int64) (string, int64, error) {
	if e, ok := c.lookupConsensusBlockHash(height); ok {
		return e.hash, e.slot, nil
	}
	v, err, _ := c.consensusHashGroup.Do(strconv.FormatInt(height, 10), func() (interface{}, error) {
		// A leader may have filled the cache between our miss and joining the group.
		if e, ok := c.lookupConsensusBlockHash(height); ok {
			return e, nil
		}
		hash, slot, err := c.consensusBlockHash(height)
		if err != nil {
			return nil, err
		}
		e := consensusBlockHashEntry{hash: hash, slot: slot}
		if hash != "" {
			c.storeConsensusBlockHash(height, e)
		}
		return e, nil
	})
	if err != nil {
		return "", 0, err
	}
	e, ok := v.(consensusBlockHashEntry)
	if !ok {
		return "", 0, fmt.Errorf("unexpected consensus blockhash cache value %T", v)
	}
	return e.hash, e.slot, nil
}

// lookupConsensusBlockHash returns the memoized derivation for height, if any.
func (c *SOLClient) lookupConsensusBlockHash(height int64) (consensusBlockHashEntry, bool) {
	c.consensusHashMu.Lock()
	defer c.consensusHashMu.Unlock()
	e, ok := c.consensusHashes[height]
	return e, ok
}

// storeConsensusBlockHash memoizes a SUCCESSFUL derivation and evicts entries more
// than solConsensusCacheWindow heights behind the newest stored height, bounding
// the map to window+1 entries. Callers must not store errors or empty hashes.
func (c *SOLClient) storeConsensusBlockHash(height int64, e consensusBlockHashEntry) {
	c.consensusHashMu.Lock()
	defer c.consensusHashMu.Unlock()
	if c.consensusHashes == nil {
		c.consensusHashes = make(map[int64]consensusBlockHashEntry)
	}
	c.consensusHashes[height] = e
	if height > c.consensusHashNewest {
		c.consensusHashNewest = height
	}
	floor := c.consensusHashNewest - solConsensusCacheWindow
	for h := range c.consensusHashes {
		if h < floor {
			delete(c.consensusHashes, h)
		}
	}
}

// maxResignDeadHashes caps the dead-blockhash set. Dead hashes are self-obsolete
// (a reschedule derives a new height -> new hash that is never dead), so this is
// only a guard against unbounded growth in a pathological loop.
const maxResignDeadHashes = 512

// markResignDead records only a blockhash whose expiry was confirmed by
// IsBlockhashValid at a processing bank at or beyond that hash's anchor slot.
func (c *SOLClient) markResignDead(blockhash string) {
	c.resignDeadMu.Lock()
	defer c.resignDeadMu.Unlock()
	if c.resignDeadHash == nil {
		c.resignDeadHash = make(map[string]struct{})
	}
	if len(c.resignDeadHash) >= maxResignDeadHashes {
		for k := range c.resignDeadHash {
			delete(c.resignDeadHash, k)
			break
		}
	}
	c.resignDeadHash[blockhash] = struct{}{}
}

// isResignDeadHash reports whether a derived consensus blockhash was already
// confirmed expired at broadcast and must not be re-signed.
func (c *SOLClient) isResignDeadHash(blockhash string) bool {
	c.resignDeadMu.Lock()
	defer c.resignDeadMu.Unlock()
	_, dead := c.resignDeadHash[blockhash]
	return dead
}

// consensusBlockHash derives the SOL blockhash every signer must embed from a CONSENSUS
// THORChain height (the keysign height: txOut.Height for scheduled outbounds, the
// keep-alive anchor otherwise) — never the per-node scanner — so all members sign
// identical bytes and form ONE TSS party. Returns ("",0,nil) when this node cannot yet
// PROVE it has finalized past the target time, so SignTx skips and the outbound
// reschedules rather than signing divergent bytes.
//
// Determinism (the load-bearing argument):
//   - t = GetBlockTimestamp(height) is CometBFT consensus data (identical on all nodes),
//     so target = t - margin is identical everywhere.
//   - S* := the greatest FINALIZED SOL slot with blockTime <= target is a global
//     property of the finalized chain — identical on every node.
//   - This function returns a hash ONLY for a node whose fetched finalized window
//     BRACKETS target (oldest listed blockTime <= target < newest listed blockTime).
//     Any bracketing window contains S* and its successor, so the rightmost listed slot
//     with blockTime <= target IS S* regardless of where the per-node window starts or
//     ends — the per-node tip only BOUNDS the search, it never DETERMINES the answer.
//     If the node has NOT finalized past target (newest listed blockTime <= target) it
//     returns "" (reschedule) instead of silently returning a smaller, wrong slot — the
//     exact failure mode that would reintroduce the per-node divergence this fix removes.
//
// No slot-rate constant is used: the lookback is only an initial guess, galloped wider
// until target is bracketed, and cut short only once getFirstAvailableBlock proves the
// window already reaches the RPC's retained history. Only slots returned by getBlocks
// (finalized listing) are ever probed/fetched, so no unvetted raw slot hits
// getBlockTime/getBlock.
//
// Abandoning a height is likewise decided on Solana evidence, never on a clock: only
// when the finalized listing PROVES more than solMaxProcessingAgeBlocks blocks have been
// produced after S* does this return errAnchorExpired, telling SignTx to reschedule
// rather than keep searching (#3344). Everything else stays ("",0,nil) — retry.
func (c *SOLClient) consensusBlockHash(height int64) (string, int64, error) {
	// The margin is read FIRST, at the keysign height and on every derivation: it is
	// a pure function of height (mimir-at-height is immutable), so the per-height memo
	// in cachedConsensusBlockHash stays valid across a governance flip, and nodes that
	// retry an old height keep that height's margin.
	margin, err := c.anchorMarginAtHeight(height)
	if err != nil {
		return "", 0, fmt.Errorf("fail to get %s at height %d: %w", constants.MimirKeySOLAnchorMarginSeconds, height, err)
	}
	t, err := c.bridge.GetBlockTimestamp(height)
	if err != nil {
		return "", 0, fmt.Errorf("fail to get consensus block timestamp: %w", err)
	}
	target := t.Unix() - margin

	tip, err := c.rpcClient.GetSlot()
	if err != nil {
		return "", 0, fmt.Errorf("fail to get finalized slot: %w", err)
	}

	// Memoized, probe-bounded getBlockTime. ok=false means the RPC reported null for
	// that slot (no block / below history) — treated as "not available", NEVER as an
	// ancient timestamp that would satisfy <= target.
	times := make(map[uint64]int64)
	probes := 0
	blockTimeAt := func(slot uint64) (bt int64, ok bool, err error) {
		if v, hit := times[slot]; hit {
			return v, true, nil
		}
		if probes >= solConsensusMaxProbes {
			return 0, false, errTooManyProbes
		}
		probes++
		p, err := c.rpcClient.GetBlockTime(slot)
		if err != nil {
			return 0, false, err
		}
		if p == nil {
			return 0, false, nil
		}
		times[slot] = *p
		return *p, true, nil
	}

	lookback := solConsensusInitialLookbackSlots
	// historyFloor memoizes getFirstAvailableBlock — the oldest slot this RPC has NOT
	// purged. Fetched lazily (only when a window fails to reach target) and at most
	// once per derivation, since it cannot move backwards while we search.
	var (
		historyFloor     uint64
		haveHistoryFloor bool
	)
	for {
		startI := int64(tip) - lookback
		if startI < 0 {
			startI = 0
		}
		slots, err := c.rpcClient.GetBlockHeights(uint64(startI), tip)
		if err != nil {
			return "", 0, fmt.Errorf("fail to get finalized slot range: %w", err)
		}
		if len(slots) == 0 {
			// No finalized blocks in a wide window below tip -> RPC badly behind.
			if lookback >= solConsensusMaxLookbackSlots {
				return "", 0, nil // give up this round -> retry
			}
			lookback *= 2
			continue
		}

		// Upper witness (bracket guard): the newest finalized slot must be strictly
		// PAST target, else this node hasn't caught up -> retry, don't guess.
		newest := slots[len(slots)-1]
		newestTime, ok, err := blockTimeAt(newest)
		if err != nil {
			return "", 0, err
		}
		if !ok || newestTime <= target {
			return "", 0, nil // not finalized past target yet -> retry
		}

		// Lower witness: the window must reach back to <= target, else gallop wider.
		oldest := slots[0]
		oldestTime, ok, err := blockTimeAt(oldest)
		if err != nil {
			return "", 0, err
		}
		if !ok || oldestTime > target {
			// Stop early only when widening is PROVABLY pointless: the window already
			// reaches the oldest slot the RPC retains, so no wider one can return
			// anything older. Without this, an outbound whose target had dropped below a
			// pruning RPC's history floor climbed the whole 400->6400 ladder on every
			// retry pass (#3344).
			//
			// The floor must come from getFirstAvailableBlock, never from the shape of
			// the getBlocks listing: a window whose oldest slot sits above startI — even
			// one that repeats across two widenings — is equally explained by a long run
			// of SKIPPED slots, below which the blocks are still served. Guessing pruning
			// there abandons a target the very next widening would have bracketed.
			if !haveHistoryFloor {
				floor, floorErr := c.rpcClient.GetFirstAvailableBlock()
				if floorErr != nil {
					// Unproven, so keep searching: the cap still bounds the work, and a
					// wrong "pruned" call would strand a reachable target.
					c.logger.Debug().Err(floorErr).Msg("fail to get SOL first available block, continuing bounded search")
					floor = 0
				}
				historyFloor, haveHistoryFloor = floor, true
			}
			// startI == 0 satisfies this too: nothing is older than genesis.
			atHistoryFloor := uint64(startI) <= historyFloor
			if atHistoryFloor || lookback >= solConsensusMaxLookbackSlots {
				// The search is over; decide retry-vs-drop on evidence already in hand,
				// denominated in BLOCKS exactly like Solana's rule:
				//
				//   ok (the oldest listed block has a timestamp) and oldestTime > target,
				//   and blockTime is monotonic in slot, so S* lies strictly below
				//   slots[0] and every one of the len(slots) finalized blocks listed here
				//   was produced AFTER S*. More than solMaxProcessingAgeBlocks of them
				//   means S*'s blockhash has already aged out of the recent-blockhash
				//   queue — whatever the slot cadence was and however many slots were
				//   skipped. A slow cluster stretches those blocks over more wall-clock
				//   time; it does not make the count smaller, so a slowdown can never
				//   manufacture this verdict.
				//
				// The ok term is load-bearing: a null timestamp on the oldest slot leaves
				// it unordered against target, and without oldestTime > target the listed
				// blocks are not known to postdate S* at all. Unproven -> retry.
				//
				// tip is the FINALIZED tip, ~32 slots behind the processing bank a tx is
				// really validated against, so the count under-reports the true age: this
				// test can only ever be too cautious, never too eager.
				if ok && len(slots) > solMaxProcessingAgeBlocks {
					return "", 0, fmt.Errorf("%w: keysign height %d anchors below the %d finalized blocks at or under slot tip %d", errAnchorExpired, height, len(slots), tip)
				}
				return "", 0, nil // can't bracket, expiry unproven -> retry
			}
			lookback *= 2
			continue
		}

		// target is bracketed (oldestTime <= target < newestTime). Binary-search
		// slots[] (ascending, blockTime monotonic non-decreasing) for the RIGHTMOST
		// slot with blockTime <= target == S*. Ties on the coarse blockTime cadence
		// are fine: selection is by slot index and S* is a global property, so every
		// bracketing node agrees.
		lo, hi := 0, len(slots)-1
		best := slots[0]
		for lo <= hi {
			mid := int(uint(lo+hi) >> 1)
			bt, btOK, btErr := blockTimeAt(slots[mid])
			if btErr != nil {
				return "", 0, btErr
			}
			if !btOK {
				// Unexpected null inside a finalized listing -> can't order; retry.
				return "", 0, fmt.Errorf("no block time for finalized slot %d", slots[mid])
			}
			if bt <= target {
				best = slots[mid]
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}

		// Blockhash only: the full block body (transaction list) is megabytes on
		// mainnet and would be downloaded inside the ~45s liveness budget and dropped.
		blockhash, err := c.rpcClient.GetBlockhashAtSlot(best)
		if err != nil {
			return "", 0, fmt.Errorf("fail to fetch consensus blockhash for slot %d: %w", best, err)
		}
		if blockhash == "" {
			return "", 0, fmt.Errorf("consensus block %d has empty blockhash", best)
		}
		return blockhash, int64(best), nil
	}
}

func (c *SOLClient) CreateTx(tx stypes.TxOutItem, memoForParsing, recentBlockhash string) (types.Transaction, error) {
	if recentBlockhash == "" {
		return types.Transaction{}, errors.New("recent blockhash is empty")
	}

	// Validation - use memoForParsing (which may be OriginalMemo for memoless outbounds)
	// For truly memoless transactions, default to TxOutbound
	var memo mem.Memo
	if memoForParsing == "" {
		memo = mem.NewOutboundMemo(tx.InHash)
	} else {
		var err error
		memo, err = mem.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return types.Transaction{}, fmt.Errorf("fail to parse memo(%s):%w", memoForParsing, err)
		}
		if memo.IsInbound() {
			return types.Transaction{}, errors.New("inbound memo should not be used for outbound tx")
		}
	}
	if len(tx.Coins) != 1 {
		return types.Transaction{}, fmt.Errorf("only support one coin per transaction, received: %d", len(tx.Coins))
	}

	solTx := types.Transaction{}

	// Create account pubkeys
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return types.Transaction{}, fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return solTx, fmt.Errorf("fail to create vault public key: %w", err)
	}
	toPubkey, err := types.PublicKeyFromString(tx.ToAddress.String())
	if err != nil {
		return solTx, fmt.Errorf("fail to create to public key: %w", err)
	}

	solValue := convertToLamports(tx.Coins[0].Amount)
	if solValue.Int64() == 0 {
		return solTx, errors.New("sol value is zero")
	}

	// A vault sending to its own address (the keep-alive self-sign) must list the
	// vault account exactly once, or Solana rejects the tx with AccountLoadedTwice.
	if toPubkey == vaultPubKey {
		return types.NewSelfTransferTransaction(vaultPubKey, tx.Coins[0].Amount.Uint64(), tx.Memo, recentBlockhash), nil
	}

	return types.NewTransferTransaction(vaultPubKey, toPubkey, tx.Coins[0].Amount.Uint64(), tx.Memo, recentBlockhash), nil
}

// useSolDurableNonce enables the durable-nonce durable-nonce outbound branch in SignTx.
// When on, SignTx anchors a SOL outbound on the vault's stored nonce IF a usable
// nonce account exists (durableNonceForVault ok); otherwise it falls through to the
// recent-blockhash path unchanged. It is therefore SAFE to leave on in every build:
// the effective rollout gate is MimirKeySOLDurableNonce, which controls whether the
// provisioning loop ever creates the nonce accounts. With provisioning off, no
// account exists, durableNonceForVault returns ok=false, and behaviour is identical
// to the pre-durable-nonce path — including during a mixed-version fleet rollout, since both
// on and off builds produce the recent-blockhash tx when no nonce account exists.
// SignTx reads MimirKeySOLDurableNonce at the CONSENSUS keysign height
// (durableNonceMimirAtHeight → the bridge GetMimirAtHeight ?height= query), so every
// party evaluating an outbound observes the same gate value and a governance flip
// cannot split the party between the durable and legacy paths (N3/N10). It then
// branches on nonce-account existence read at the consensus anchor slot. HaltSigningSOL
// remains the emergency stop.
const useSolDurableNonce = true

// solNonceSeed is the fixed create_with_seed seed that fixes a vault's durable-nonce
// account address as a pure function of the vault pubkey (CreateWithSeed(vault,
// solNonceSeed, SystemProgram)). Every keysign party derives the identical address
// with no shared state, and a churn-in vault's account is deterministic from its own
// pubkey. Changing this string orphans every existing nonce account, so it is
// versioned and never edited in place.
const solNonceSeed = "thornode-durable-nonce-v1"

// nonceAccountRentLamports funds a new nonce account. It must be >= the rent-exempt
// minimum for an 80-byte (types.NonceAccountSize) account — 1,447,680 lamports under
// the current Solana rent schedule — and is a FIXED constant (not an RPC read) so
// every keysign party funds the identical amount and the create tx bytes converge.
// Rounded up to 1,500,000 for headroom against a rent-schedule tweak; any surplus is
// reclaimed when the account is closed at churn.
const nonceAccountRentLamports = uint64(1_500_000)

// nonceAddressForVault derives the deterministic durable-nonce account address for a
// vault: CreateWithSeed(vaultPubKey, solNonceSeed, SystemProgram). It is a pure
// function of the vault pubkey, so every keysign party — and the provisioning loop —
// compute the identical address with no shared state or coordination.
func (c *SOLClient) nonceAddressForVault(vaultPubKey types.PublicKey) (types.PublicKey, error) {
	return types.CreateWithSeed(vaultPubKey, solNonceSeed, types.SystemProgramID)
}

// durableDestinationExcluded reports whether an outbound's recipient rules out the
// durable-nonce path. It is a pure function of the tx and vault, so every keysign
// party reaches the same answer. Excluded recipients take the recent-blockhash path:
//   - a self-send (vault keep-alive / consolidate): a nonce tx lists the vault once as
//     fee-payer+source+authority and cannot also be the distinct recipient. Keeping
//     it off the durable path keeps the keep-alive working when the mimir is on
//     (otherwise the -32020 scanner prune-wedge re-opens);
//   - a send TO the vault's own durable-nonce account: the account would appear twice
//     in the key list (recipient + nonce account), which Solana rejects, so the
//     retained signed bytes would fail forever and head-of-line block the vault's
//     later SOL outbounds. The nonce address is a public, deterministic function of
//     the vault, so anyone could route an outbound there.
func (c *SOLClient) durableDestinationExcluded(tx stypes.TxOutItem) bool {
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil {
		return false
	}
	if tx.ToAddress.Equals(vaultAddr) {
		return true
	}
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return false
	}
	nonceAddr, err := c.nonceAddressForVault(vaultPubKey)
	if err != nil {
		return false
	}
	return tx.ToAddress.String() == nonceAddr.String()
}

// recentBlockhashDestinations returns the addresses durableDestinationExcluded sends
// on the recent-blockhash path for tx's vault: its own SOL address and nonce account.
func (c *SOLClient) recentBlockhashDestinations(tx stypes.TxOutItem) map[string]bool {
	out := map[string]bool{}
	if tx.VaultPubKeyEddsa.IsEmpty() {
		return out
	}
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil || vaultAddr.IsEmpty() {
		return out
	}
	out[vaultAddr.String()] = true
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return out
	}
	if nonceAddr, err := c.nonceAddressForVault(vaultPubKey); err == nil {
		out[nonceAddr.String()] = true
	}
	return out
}

// mimirHeightReader is implemented by the production thorchain bridge (see
// thorclient GetMimirAtHeight): it reads a mimir as of a consensus height so every
// keysign party observes the same value. Test bridges that do not implement it fall
// back to the live read.
type mimirHeightReader interface {
	GetMimirAtHeight(key string, height int64) (int64, error)
}

// durableNonceMimirAtHeight reads the SOLDurableNonce gate at the consensus keysign
// height so the durable-vs-legacy choice is identical across all parties, not a
// divergent per-node live read that could fragment a keysign across a governance flip
// (N3/N10). Falls back to the live GetMimir only for test bridges that do not
// implement the height-anchored read; production always uses the concrete bridge.
func (c *SOLClient) durableNonceMimirAtHeight(height int64) (int64, error) {
	if r, ok := c.bridge.(mimirHeightReader); ok {
		return r.GetMimirAtHeight(constants.MimirKeySOLDurableNonce, height)
	}
	return c.bridge.GetMimir(constants.MimirKeySOLDurableNonce)
}

// anchorMarginAtHeight returns the consensus-blockhash anchor margin in seconds,
// read from MimirKeySOLAnchorMarginSeconds at the consensus keysign height so every
// party derives the same target. Unset reads as 0 (null) or -1 (deleted): any value
// <= 0 maps to the solFinalizationMarginSeconds default BEFORE clamping, so an unset
// or reset mimir never lands on the clamp floor. Positive values are clamped to
// [solAnchorMarginMinSeconds, solAnchorMarginMaxSeconds]. A read error is returned
// so the caller retains the outbound instead of guessing a margin.
func (c *SOLClient) anchorMarginAtHeight(height int64) (int64, error) {
	var v int64
	var err error
	if r, ok := c.bridge.(mimirHeightReader); ok {
		v, err = r.GetMimirAtHeight(constants.MimirKeySOLAnchorMarginSeconds, height)
	} else {
		v, err = c.bridge.GetMimir(constants.MimirKeySOLAnchorMarginSeconds)
	}
	if err != nil {
		return 0, err
	}
	return clampAnchorMargin(v), nil
}

// clampAnchorMargin maps a raw SOLAnchorMarginSeconds mimir value to the margin used.
func clampAnchorMargin(v int64) int64 {
	switch {
	case v <= 0:
		return solFinalizationMarginSeconds
	case v < solAnchorMarginMinSeconds:
		return solAnchorMarginMinSeconds
	case v > solAnchorMarginMaxSeconds:
		return solAnchorMarginMaxSeconds
	default:
		return v
	}
}

// pendingOutboundReader is implemented by the production thorchain bridge; it lists
// the not-yet-observed outbound queue so the durable-nonce path can serialize one
// tx per vault (N2). Test bridges may not implement it (the gate then fails open).
type pendingOutboundReader interface {
	GetPendingOutbounds(height int64) ([]thorclient.PendingOutbound, error)
}

// durableSerializeAllows reports whether tx may take the durable-nonce path now
// under serialize-with-finality (N2): only the LOWEST-height not-yet-observed SOL
// outbound for its vault may sign durable, so at most one durable tx is in flight
// per vault and the stored nonce is stable during its keysign. Higher-height siblings,
// and same-height siblings later in the signer's (Height, Index) order, must WAIT
// until the earlier one is observed (drops from the pending queue) and the nonce
// advances.
//
// The queue is read AT THE KEYSIGN HEIGHT, so every signer of the vault decides from
// the same snapshot (no per-node timing split of the keysign party). A waiter is never
// released by that fixed snapshot; it is released because THORChain re-issues it at the
// height where the earlier sibling's nonce commits (reissueSupersededSolDurable), and at
// that new height the earlier sibling is no longer pending. A query error returns false
// (retain and retry), never a guess. A test bridge without the reader fails open.
func (c *SOLClient) durableSerializeAllows(tx stypes.TxOutItem, height int64) bool {
	r, ok := c.bridge.(pendingOutboundReader)
	if !ok {
		return true
	}
	pending, err := r.GetPendingOutbounds(height)
	if err != nil {
		c.logger.Err(err).Int64("height", height).Msg("fail to read pending outbounds for durable-nonce serialization; retaining")
		return false
	}
	sol := common.SOLChain.String()
	vault := tx.VaultPubKey.String()
	vaultEddsa := tx.VaultPubKeyEddsa.String()
	// Siblings issued below the vault's latest commit height are dead (see
	// committedNonceAdvancedSince); 0 when unknown, which skips nothing.
	deadBelow := c.latestCommittedNonceHeight(tx)
	// Siblings paying the vault's own address or nonce account sign on the
	// recent-blockhash path (durableDestinationExcluded) and never consume the nonce,
	// so they must not hold a durable outbound behind them.
	recentOnly := c.recentBlockhashDestinations(tx)

	// Same-height siblings must be ordered exactly as the signer orders them, or the
	// vault can deadlock: pipeline.SpawnSignings sorts by (Height, Index) and, holding
	// the vault/chain lock, attempts only the first item per vault each pass. If this
	// gate deferred that head-of-line item behind a sibling the signer never reaches,
	// the vault would never progress. /thorchain/queue/outbound lists heights ascending
	// and each height's items in TxArray order (the OutHash-empty filter keeps relative
	// order), so position within a height is the same order as Index. Locate this
	// outbound in the list; a same-height sibling waits only if it comes earlier. If
	// this outbound cannot be located, do not defer on same-height siblings at all —
	// waiting without a consistent order is what deadlocks.
	self := -1
	for i, p := range pending {
		if p.Height == height && pendingMatchesTx(p, tx) {
			self = i
			break
		}
	}
	for i, p := range pending {
		if i == self || p.Chain != sol {
			continue
		}
		// Belongs to this vault if EITHER non-empty pubkey matches; empty fields never
		// match (else two different vaults with an empty eddsa would collide).
		belongs := (vault != "" && p.VaultPubKey == vault) ||
			(vaultEddsa != "" && p.VaultPubKeyEddsa == vaultEddsa)
		if !belongs || c.durableRejected(p) {
			// A sibling this node's pre-sign filters refused to sign will not be
			// signed here, so it must not hold this one up.
			continue
		}
		if recentOnly[p.ToAddress] {
			continue
		}
		if deadBelow > 0 && p.Height < deadBelow {
			// The vault's nonce was committed after this sibling's keysign height, so
			// the only nonce it can be signed with is consumed: SignTx drops it and it
			// waits for LackSigning to re-issue it at a new height. It stays OutHash-
			// empty in the queue meanwhile, and must not hold later siblings behind it.
			continue
		}
		// A strictly-earlier sibling (lower height, or same height and earlier in the
		// signer's order) is still in flight -> this one waits.
		if p.Height < height || (p.Height == height && self >= 0 && i < self) {
			return false
		}
	}
	return true
}

// pendingMatchesTx reports whether a queued outbound is this outbound, by the fields
// the queue endpoint and the signer both carry. The coin is compared when the outbound
// carries exactly one (always true for a SOL outbound).
func pendingMatchesTx(p thorclient.PendingOutbound, tx stypes.TxOutItem) bool {
	if p.InHash != tx.InHash.String() || p.ToAddress != tx.ToAddress.String() || p.Memo != tx.Memo {
		return false
	}
	if len(tx.Coins) == 1 {
		return p.Coin.Asset == tx.Coins[0].Asset.String() && p.Coin.Amount == tx.Coins[0].Amount.String()
	}
	return true
}

// solVaultNonceReader is implemented by the production thorchain bridge; it reads a
// vault's durable nonce as committed on THORChain. Test bridges may not implement it,
// in which case no vault has a committed nonce (recent-blockhash path).
type solVaultNonceReader interface {
	GetSolVaultNonce(vaultPubKey string, height int64) (thorclient.SolVaultNonce, error)
}

// committedSolVaultNonce returns the vault's durable nonce as committed on THORChain
// at keysign height, whether one is committed (known), or an error when the value
// cannot be determined. The read is pinned to the consensus height (?height=), so
// every keysign party evaluating this outbound sees the identical value — the nonce
// is agreed by observer supermajority on-chain, not read per node from Solana.
func (c *SOLClient) committedSolVaultNonce(tx stypes.TxOutItem, height int64) (thorclient.SolVaultNonce, bool, error) {
	r, ok := c.bridge.(solVaultNonceReader)
	if !ok || tx.VaultPubKey.IsEmpty() {
		return thorclient.SolVaultNonce{}, false, nil
	}
	committed, err := r.GetSolVaultNonce(tx.VaultPubKey.String(), height)
	if err != nil {
		return thorclient.SolVaultNonce{}, false, err
	}
	if committed.IsEmpty() {
		return thorclient.SolVaultNonce{}, false, nil
	}
	return committed, true, nil
}

// latestCommittedNonceHeight returns the THORChain height at which the vault's latest
// durable nonce was committed, or 0 when there is none or it cannot be read.
func (c *SOLClient) latestCommittedNonceHeight(tx stypes.TxOutItem) int64 {
	r, ok := c.bridge.(solVaultNonceReader)
	if !ok || tx.VaultPubKey.IsEmpty() {
		return 0
	}
	latest, err := r.GetSolVaultNonce(tx.VaultPubKey.String(), 0)
	if err != nil || latest.IsEmpty() {
		return 0
	}
	return latest.UpdatedHeight
}

// committedNonceAdvancedSince reports whether the vault's LATEST committed durable
// nonce was committed after keysign height, i.e. the value committed at that height
// has since been consumed. Liveness-only (see SignTx): a read failure reports false.
func (c *SOLClient) committedNonceAdvancedSince(tx stypes.TxOutItem, height int64) bool {
	r, ok := c.bridge.(solVaultNonceReader)
	if !ok || tx.VaultPubKey.IsEmpty() {
		return false
	}
	latest, err := r.GetSolVaultNonce(tx.VaultPubKey.String(), 0)
	if err != nil {
		c.logger.Warn().Err(err).Msg("fail to read latest committed durable nonce; proceeding")
		return false
	}
	return !latest.IsEmpty() && latest.UpdatedHeight > height
}

// committedNonceKeys resolves the nonce account and authority to sign a durable tx
// with, re-checking that the committed account is the vault's own seed-derived one
// (THORChain already enforces this at commit; this is defence in depth). The vault is
// the nonce authority in the signing model, so it signs AdvanceNonceAccount itself.
func (c *SOLClient) committedNonceKeys(tx stypes.TxOutItem, committed thorclient.SolVaultNonce) (nonceAccount, nonceAuthority types.PublicKey, err error) {
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return types.PublicKey{}, types.PublicKey{}, fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return types.PublicKey{}, types.PublicKey{}, fmt.Errorf("fail to parse vault pubkey: %w", err)
	}
	expected, err := c.nonceAddressForVault(vaultPubKey)
	if err != nil {
		return types.PublicKey{}, types.PublicKey{}, fmt.Errorf("fail to derive nonce address: %w", err)
	}
	if expected.String() != committed.NonceAccount {
		return types.PublicKey{}, types.PublicKey{}, fmt.Errorf("committed nonce account %s is not the vault's %s", committed.NonceAccount, expected)
	}
	return expected, vaultPubKey, nil
}

// CreateTxWithNonce builds an outbound SOL transfer anchored on a durable nonce
// rather than a recent blockhash. It mirrors CreateTx's validation; the
// difference is the transaction it emits (NewNonceTransferTransaction, with
// AdvanceNonceAccount as instruction[0] and the message's recentBlockhash field set
// to nonceValue). Because a durable nonce does not expire, the SignTx nonce path
// skips the recent-blockhash derivation and expiry gating entirely.
func (c *SOLClient) CreateTxWithNonce(tx stypes.TxOutItem, memoForParsing, nonceValue string, nonceAccount, nonceAuthority types.PublicKey) (types.Transaction, error) {
	if nonceValue == "" {
		return types.Transaction{}, errors.New("durable nonce value is empty")
	}

	// Validation - identical to CreateTx: memoForParsing may be OriginalMemo for
	// memoless outbounds; truly memoless defaults to TxOutbound.
	var memo mem.Memo
	if memoForParsing == "" {
		memo = mem.NewOutboundMemo(tx.InHash)
	} else {
		var err error
		memo, err = mem.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return types.Transaction{}, fmt.Errorf("fail to parse memo(%s):%w", memoForParsing, err)
		}
		if memo.IsInbound() {
			return types.Transaction{}, errors.New("inbound memo should not be used for outbound tx")
		}
	}
	if len(tx.Coins) != 1 {
		return types.Transaction{}, fmt.Errorf("only support one coin per transaction, received: %d", len(tx.Coins))
	}

	solTx := types.Transaction{}

	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return types.Transaction{}, fmt.Errorf("fail to get vault address: %w", err)
	}
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return solTx, fmt.Errorf("fail to create vault public key: %w", err)
	}
	toPubkey, err := types.PublicKeyFromString(tx.ToAddress.String())
	if err != nil {
		return solTx, fmt.Errorf("fail to create to public key: %w", err)
	}

	solValue := convertToLamports(tx.Coins[0].Amount)
	if solValue.Int64() == 0 {
		return solTx, errors.New("sol value is zero")
	}

	// The vault->self keep-alive under a durable nonce needs a builder that lists the
	// vault once (as NewSelfTransferTransaction does for the blockhash path); that is
	// not required for scheduled outbounds and is out of scope for the initial draft.
	if toPubkey == vaultPubKey {
		return solTx, errors.New("durable-nonce self-transfer not supported yet")
	}

	return types.NewNonceTransferTransaction(vaultPubKey, toPubkey, tx.Coins[0].Amount.Uint64(), tx.Memo, nonceValue, nonceAccount, nonceAuthority), nil
}

// --------------------------------- sign ---------------------------------

func convertToLamports(amount cosmos.Uint) *big.Int {
	// thorchain amounts in 1e8
	// multiply by 10 to get 1e9 (lamports)
	return new(big.Int).Mul(amount.BigInt(), big.NewInt(10))
}

// --------------------------------- broadcast ---------------------------------

// BroadcastTx broadcasts the transaction and returns the transaction hash.
func (c *SOLClient) BroadcastTx(txOutItem stypes.TxOutItem, rawTx []byte) (string, error) {
	base64Tx := base64.StdEncoding.EncodeToString(rawTx)
	txSig, err := c.rpcClient.BroadcastTx(base64Tx)
	if err != nil {
		c.logger.Err(err).Str("memo", txOutItem.Memo).Msg("broadcast error, checking if tx landed on chain")

		// Wait for the transaction to reach confirmed status before querying. Without this,
		// the lookup races against block production and likely misses on the first attempt.
		time.Sleep(broadcastLookupDelay)

		// The RPC call may have errored after the transaction was already accepted and
		// included on-chain (e.g. network timeout on the response). Extract the signature
		// from the raw bytes and look it up on chain; if found, treat the broadcast as
		// successful so the signer cache is written and the transaction is not re-signed on
		// a future reschedule.
		if knownSig, ok := txSigFromRawTx(rawTx); ok {
			if result, lookupErr := c.rpcClient.GetTransactionConfirmed(knownSig); lookupErr == nil && result.Slot > 0 {
				c.logger.Warn().Str("memo", txOutItem.Memo).Str("txid", knownSig).
					Msg("tx found on chain despite broadcast error, updating signer cache")
				expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
				if cacheErr := c.signerCacheManager.SetSignedWithExpiry(txOutItem.CacheHash(), txOutItem.CacheVault(c.GetChain()), knownSig, expiry); cacheErr != nil {
					c.logger.Err(cacheErr).Interface("txOutItem", txOutItem).Msg("fail to mark tx out item as signed")
				}
				return knownSig, nil
			}
		}
		// A lagging backend can reject a still-valid hash. Keep the signed bytes
		// unless a caught-up processing bank confirms that their hash expired.
		if isBlockhashNotFound(err) {
			// A durable-nonce tx reports "Blockhash not found" once its nonce has been
			// consumed. Its baked value is the committed nonce, not a consensus anchor,
			// so the recent-blockhash expiry check below cannot confirm it and would
			// retain the dead bytes forever (head-of-line blocking the vault). If these
			// bytes carry the nonce committed at the signing height and the committed
			// nonce has since advanced, they are dead: hand back for re-sign, where
			// SignTx drops the outbound for reschedule (B5) — never re-signing it.
			if c.durableTxConsumed(txOutItem, rawTx) {
				c.logger.Warn().Err(err).Str("memo", txOutItem.Memo).
					Msg("SOL durable nonce consumed; dropping signed tx for reschedule")
				return "", fmt.Errorf("%w: %v", stypes.ErrResignRequired, err)
			}
			blockhash, expiryErr := c.confirmedExpiredBlockhash(txOutItem, rawTx)
			switch {
			case expiryErr != nil:
				// This includes errAnchorExpired: an expired anchor for the signing
				// height says nothing about the hash these bytes carry. Signed txs
				// persisted before the consensus-anchor upgrade used the scanner's
				// recent hash, which can be newer and still live, so retain them.
				c.logger.Warn().Err(expiryErr).Str("memo", txOutItem.Memo).
					Msg("cannot confirm SOL blockhash expiry; retaining signed tx for retry")
			case blockhash != "":
				c.logger.Warn().Err(err).Str("memo", txOutItem.Memo).
					Msg("SOL blockhash expiry confirmed; dropping signed tx for reschedule")
				c.markResignDead(blockhash)
				return "", fmt.Errorf("%w: %v", stypes.ErrResignRequired, err)
			}
		}
		return "", fmt.Errorf("failed to broadcast tx: %w", err)
	}
	c.logger.Info().Str("memo", txOutItem.Memo).Str("txid", txSig).Msg("broadcast tx")

	// Record the broadcast in the signer cache with its expiry. SignTx will respect
	// the cache unconditionally until the expiry passes; past expiry it verifies on
	// chain before re-signing, so a dropped broadcast eventually recovers without
	// risking a double-spend against a lagging observation.
	expiry := time.Now().Add(c.getSignerCacheExpiry()).UnixMilli()
	if err := c.signerCacheManager.SetSignedWithExpiry(txOutItem.CacheHash(), txOutItem.CacheVault(c.GetChain()), txSig, expiry); err != nil {
		c.logger.Err(err).Interface("txOutItem", txOutItem).Msg("fail to mark tx out item as signed")
	}

	return txSig, nil
}

// durableTxConsumed reports whether rawTx is a durable-nonce tx signed with the
// vault's committed nonce at its signing height, and that committed nonce has since
// advanced (so the nonce these bytes carry is consumed and they can never land).
func (c *SOLClient) durableTxConsumed(txOutItem stypes.TxOutItem, rawTx []byte) bool {
	baked, ok := txBlockhashFromRawTx(rawTx)
	if !ok || txOutItem.Height <= 0 {
		return false
	}
	committed, known, err := c.committedSolVaultNonce(txOutItem, txOutItem.Height)
	if err != nil || !known || committed.Nonce != baked {
		return false
	}
	return c.committedNonceAdvancedSince(txOutItem, txOutItem.Height)
}

// confirmedExpiredBlockhash returns the signed transaction's hash only when
// expiry is confirmed. Derive its anchor from the persisted signing height so
// saved signed transactions remain verifiable after a Bifrost restart.
func (c *SOLClient) confirmedExpiredBlockhash(txOutItem stypes.TxOutItem, rawTx []byte) (string, error) {
	blockhash, ok := txBlockhashFromRawTx(rawTx)
	if !ok || txOutItem.Height <= 0 {
		return "", errors.New("signed SOL transaction is missing its blockhash or signing height")
	}
	expectedHash, anchorSlot, err := c.cachedConsensusBlockHash(txOutItem.Height)
	if err != nil {
		return "", fmt.Errorf("fail to derive signed SOL blockhash anchor: %w", err)
	}
	if expectedHash != blockhash || anchorSlot <= 0 {
		return "", errors.New("signed SOL blockhash does not match its consensus anchor")
	}
	valid, err := c.rpcClient.IsBlockhashValid(blockhash, "processed", uint64(anchorSlot))
	if err != nil {
		return "", err
	}
	if valid {
		return "", nil
	}
	return blockhash, nil
}

// isBlockhashNotFound matches the preflight rejection that needs an expiry
// check. The error itself does not distinguish expiry from a lagging RPC.
func isBlockhashNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "blockhash not found")
}

// txBlockhashFromRawTx reads the hash from the single-signature legacy
// transactions Bifrost creates. Reject truncated or unsupported encodings so
// they cannot provide expiry evidence for clearing a persisted signed tx.
func txBlockhashFromRawTx(rawTx []byte) (string, bool) {
	const messageOffset = 1 + ed25519.SignatureSize
	const accountsOffset = messageOffset + 3 // legacy message header
	if len(rawTx) <= accountsOffset || rawTx[0] != 1 || rawTx[messageOffset] != 1 {
		return "", false
	}
	accountCount, prefixLen := binary.Uvarint(rawTx[accountsOffset:])
	if prefixLen <= 0 || prefixLen > 3 || accountCount == 0 || accountCount > 256 {
		return "", false
	}
	const hashSize = 32
	hashOffset := accountsOffset + prefixLen + int(accountCount)*ed25519.PublicKeySize
	// A serialized message also contains an instruction-count prefix after the hash.
	if hashOffset+hashSize >= len(rawTx) {
		return "", false
	}
	return base58.Encode(rawTx[hashOffset : hashOffset+hashSize]), true
}

// txSigFromRawTx extracts the base58-encoded transaction signature from a
// serialized Solana transaction. The wire format is:
//
//	[compact-u16 sig count = 0x01][64-byte signature][message...]
//
// All THORChain outbounds have exactly one signature, so the count prefix is
// always the single byte 0x01 and the signature occupies bytes [1:65].
func txSigFromRawTx(rawTx []byte) (string, bool) {
	const sigOffset = 1 // 1-byte compact-u16 for count=1
	const sigLen = 64
	if len(rawTx) < sigOffset+sigLen {
		return "", false
	}
	return base58.Encode(rawTx[sigOffset : sigOffset+sigLen]), true
}

// --------------------------------- observe ---------------------------------

// OnObservedTxIn is called when a new observed tx is received.
func (c *SOLClient) OnObservedTxIn(txIn stypes.TxInItem, blockHeight int64) {
	m, err := mem.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// Debug log only as ParseMemo error is expected for THORName inbounds.
		c.logger.Debug().Err(err).Str("memo", txIn.Memo).Msg("fail to parse memo")
		return
	}
	if !m.IsOutbound() {
		return
	}
	if m.GetTxID().IsEmpty() {
		return
	}
	if err = c.signerCacheManager.SetSigned(txIn.CacheHash(c.GetChain(), m.GetTxID().String()), txIn.CacheVault(c.GetChain()), txIn.Tx); err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
}

// GetConfirmationCount returns the confirmation count for the given tx.
func (c *SOLClient) GetConfirmationCount(txIn stypes.TxIn) int64 {
	// Solana has instant finality since we query everything with commitment "finalized"
	return 0
}

// ConfirmationCountReady returns true if the confirmation count is ready.
func (c *SOLClient) ConfirmationCountReady(txIn stypes.TxIn) bool {
	// Solana has instant finality since we query everything with commitment "finalized"
	return true
}
