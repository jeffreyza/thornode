package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	golog "github.com/ipfs/go-log"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	flag "github.com/spf13/pflag"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/observer"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/signer"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	btss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/cmd"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

const (
	serverIdentity = "bifrost"
)

func printVersion() {
	fmt.Printf("%s v%s, rev %s\n", serverIdentity, constants.Version, constants.GitCommit)
}

func main() {
	showVersion := flag.Bool("version", false, "Shows version")
	logLevel := flag.StringP("log-level", "l", "info", "Log Level")
	pretty := flag.BoolP("pretty-log", "p", false, "Enables unstructured prettified logging. This is useful for local debugging")
	deckDump := flag.String("deck-dump", "", "Path to a deck dump file")
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	initPrefix()
	initLog(*logLevel, *pretty)
	config.Init()
	config.InitBifrost()
	cfg := config.GetBifrost()

	// metrics
	m, err := metrics.NewMetrics(cfg.Metrics)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create metric instance")
	}
	if err = m.Start(); err != nil {
		log.Fatal().Err(err).Msg("fail to start metric collector")
	}
	if len(cfg.Thorchain.SignerName) == 0 {
		log.Fatal().Msg("signer name is empty")
	}
	if len(cfg.Thorchain.SignerPasswd) == 0 {
		log.Fatal().Msg("signer password is empty")
	}
	kb, _, err := thorclient.GetKeyringKeybase(cfg.Thorchain.ChainHomeFolder, cfg.Thorchain.SignerName, cfg.Thorchain.SignerPasswd)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to get keyring keybase")
	}

	k := thorclient.NewKeysWithKeybase(kb, cfg.Thorchain.SignerName, cfg.Thorchain.SignerPasswd)
	// thorchain bridge
	thorchainBridge, err := thorclient.NewThorchainBridge(cfg.Thorchain, m, k)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create new thorchain bridge")
	}
	if err = thorchainBridge.EnsureNodeWhitelistedWithTimeout(); err != nil {
		log.Fatal().Err(err).Msg("node account is not whitelisted, can't start")
	}
	// PubKey Manager
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(thorchainBridge, m)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create pubkey manager")
	}
	if err = pubkeyMgr.Start(); err != nil {
		log.Fatal().Err(err).Msg("fail to start pubkey manager")
	}

	// Legacy shares back the process-wide TSS stack and remain a startup
	// prerequisite. XMR FROST shares are isolated by exact-vault readiness, so a
	// missing historical XMR vault is retried without disabling healthy chains.
	// A definitively-unrecoverable signing-vault share does not block startup:
	// bifrost starts degraded (that vault's keysigns fail, everything else —
	// observations included — keeps working) while recovery retries in the
	// background.
	startBifrost, degraded := recoverLegacyKeysharesBeforeStartup(cfg, thorchainBridge)
	if !startBifrost {
		return
	}
	recoveryCtx, cancelRecovery := context.WithCancel(context.Background())
	if degraded {
		go superviseLegacyKeyshareRecovery(recoveryCtx, cfg, thorchainBridge)
	}
	go superviseFrostKeyshareRecovery(recoveryCtx, cfg, thorchainBridge)

	// setup TSS signing
	priKey, err := k.GetPrivateKey()
	if err != nil {
		log.Fatal().Err(err).Msg("fail to get secp256k1 private key")
	}

	tmPrivateKey := tcommon.CosmosPrivateKeyToTMPrivateKey(priKey)

	priKeyEddsa, err := k.GetPrivateKeyEDDSA()
	if err != nil {
		log.Fatal().Err(err).Msg("fail to get eddsa private key")
	}

	tmPrivateKeyEddsa := tcommon.CosmosPrivateKeyToTMPrivateKey(priKeyEddsa)

	consts := constants.NewConstantValue()
	jailTimeKeygen := time.Duration(consts.GetInt64Value(constants.JailTimeKeygen)) * constants.ThorchainBlockTime
	jailTimeKeysign := time.Duration(consts.GetInt64Value(constants.JailTimeKeysign)) * constants.ThorchainBlockTime
	if cfg.Signer.KeygenTimeout >= jailTimeKeygen {
		log.Fatal().
			Stringer("keygenTimeout", cfg.Signer.KeygenTimeout).
			Stringer("keygenJail", jailTimeKeygen).
			Msg("keygen timeout must be shorter than jail time")
	}
	// FrostKeygenTimeout is intentionally NOT bound by jailTimeKeygen.
	// FROST DKG is a 3-round p2p protocol whose round-2 message exchange
	// can legitimately exceed the aggressive jail times used in test
	// environments. The 60s
	// floor inside NewFrostKeygenCoordinator + per-environment overrides
	// in docker-compose are the relevant safety nets here.
	if cfg.Signer.KeysignTimeout >= jailTimeKeysign {
		log.Fatal().
			Stringer("keysignTimeout", cfg.Signer.KeysignTimeout).
			Stringer("keysignJail", jailTimeKeysign).
			Msg("keysign timeout must be shorter than jail time")
	}

	// Start P2P with bonded node gating enabled
	comm, stateManager, err := p2p.StartP2PWithBridge(
		cfg.TSS,
		tmPrivateKey,
		app.DefaultNodeHome,
		thorchainBridge,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to start p2p")
	}

	tssIns, err := tss.NewTss(
		comm,
		stateManager,
		tmPrivateKey,
		tmPrivateKeyEddsa,
		common.TssConfig{
			EnableMonitor:      true,
			KeyGenTimeout:      cfg.Signer.KeygenTimeout,
			FrostKeygenTimeout: cfg.Signer.FrostKeygenTimeout,
			KeySignTimeout:     cfg.Signer.KeysignTimeout,
			PartyTimeout:       cfg.Signer.PartyTimeout,
			PreParamTimeout:    cfg.Signer.PreParamTimeout,
		},
		nil,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create tss instance")
	}

	if err = tssIns.Start(); err != nil {
		log.Err(err).Msg("fail to start tss instance")
	}

	cfgChains := cfg.GetChains()

	// ensure we have a protocol for chain RPC Hosts
	for _, chainCfg := range cfgChains {
		if chainCfg.Disabled {
			continue
		}
		if len(chainCfg.RPCHost) == 0 {
			log.Fatal().Err(err).Stringer("chain", chainCfg.ChainID).Msg("missing chain RPC host")
			return
		}
		if !strings.HasPrefix(chainCfg.RPCHost, "http") {
			chainCfg.RPCHost = fmt.Sprintf("http://%s", chainCfg.RPCHost)
		}
	}
	poolMgr := thorclient.NewPoolMgr(thorchainBridge)
	chains, restart := chainclients.LoadChains(k, cfgChains, tssIns, thorchainBridge, m, pubkeyMgr, poolMgr)
	if len(chains) == 0 {
		log.Fatal().Msg("fail to load any chains")
	}
	tssKeysignMetricMgr := metrics.NewTssKeysignMetricMgr()
	healthServer := NewHealthServer(cfg.TSS.InfoAddress, tssIns, chains)
	go func() {
		defer log.Info().Msg("health server exit")
		if err = healthServer.Start(); err != nil {
			log.Error().Err(err).Msg("fail to start health server")
		}
	}()

	ctx := context.Background()

	// start observer notifier
	ag, err := observer.NewAttestationGossip(comm.GetHost(), k, cfg.Thorchain.ChainEBifrost, thorchainBridge, m, cfg.AttestationGossip)

	// start observer
	obs, err := observer.NewObserver(pubkeyMgr, chains, thorchainBridge, m, cfgChains[tcommon.BTCChain].BlockScanner.DBPath, tssKeysignMetricMgr, ag, *deckDump)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create observer")
	}
	// Register committed-event callbacks before Start launches the gossip event
	// client. NewObserver also does this defensively for non-main call sites.
	ag.SetObserverHandleObservedTxCommitted(obs)
	ag.SetObserverHandleErrataTxCommitted(obs)
	if xmrClient, ok := chains[tcommon.XMRChain].(*monero.Client); ok {
		ag.SetObserverApplySpentOutputRefs(xmrClient.ApplySpentOutputRefsWithOutcome)
		ag.SetObserverApplyCommittedSpentRefs(xmrClient.ApplyCommittedSpentOutputRefs)
	}
	if err = obs.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("fail to start observer")
	}

	// start signer
	sign, err := signer.NewSigner(cfg, thorchainBridge, k, pubkeyMgr, tssIns, chains, m, tssKeysignMetricMgr, obs)
	if err != nil {
		log.Fatal().Err(err).Msg("fail to create instance of signer")
	}
	if err = sign.Start(); err != nil {
		log.Fatal().Err(err).Msg("fail to start signer")
	}

	// wait....
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ch:
	case <-restart:
	}
	log.Info().Msg("stop signal received")
	cancelRecovery()
	// stop observer
	if err = obs.Stop(); err != nil {
		log.Fatal().Err(err).Msg("fail to stop observer")
	}
	// stop signer
	if err = sign.Stop(); err != nil {
		log.Fatal().Err(err).Msg("fail to stop signer")
	}
	// stop go tss
	tssIns.Stop()
	if err = healthServer.Stop(); err != nil {
		log.Fatal().Err(err).Msg("fail to stop health server")
	}
}

// recoverLegacyKeysharesBeforeStartup returns (start, degraded). start is false
// only when a shutdown signal interrupts the retry loop. degraded is true when
// startup should proceed WITHOUT complete keyshares: recovery failed with an
// error foreground retries can never fix (source blocks pruned below the node's
// floor, or a TssPool message found with an empty backup) — blocking here would
// take down observations for every chain, which is strictly worse than degraded
// signing, so the caller must hand recovery to superviseLegacyKeyshareRecovery.
// Transient errors (RPC flake, message not yet found) keep the foreground retry
// loop, which usually resolves within a few blocks.
func recoverLegacyKeysharesBeforeStartup(cfg config.Bifrost, bridge thorclient.ThorchainBridge) (start, degraded bool) {
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	for attempt := 1; ; attempt++ {
		err := btss.RecoverLegacyKeyShares(cfg, bridge)
		if err == nil {
			return true, false
		}
		if btss.IsUnrecoverableKeyshareError(err) {
			// The joined error may also carry transient failures for other vaults;
			// the background supervisor re-runs the full recovery, so those are
			// retried there too.
			log.Error().Err(err).Msg("legacy keyshares for a signing vault are unrecoverable by retrying; starting bifrost DEGRADED and retrying recovery in the background")
			return true, true
		}
		log.Error().Err(err).Int("attempt", attempt).Msg("legacy keyshare recovery incomplete; retrying before TSS startup")
		timer := time.NewTimer(constants.ThorchainBlockTime)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false, false
		}
	}
}

const (
	// legacyRecoveryRetryInitial/Max bound the background recovery backoff. The
	// doubling cap keeps the worst case (a found-but-empty backup, where each
	// re-attempt rescans blocks for the TssPool message) to ~48 scans/day while
	// still noticing an operator fix — restored localstate short-circuits before
	// any scan — within a minute at first.
	legacyRecoveryRetryInitial = time.Minute
	legacyRecoveryRetryMax     = 30 * time.Minute
)

// superviseLegacyKeyshareRecovery re-attempts legacy keyshare recovery in the
// background after bifrost started degraded. The moment an attempt succeeds —
// the operator restores localstate-<pubkey>.json from a backup, points the
// THORNode RPC at an archive node with deeper history, or block backfill passes
// the prune floor — the shares land on disk and the very next keysign picks them
// up, because go-tss reads keyshares from disk per keysign; no restart needed.
// Until then it screams on every cycle so the degraded state cannot be missed.
func superviseLegacyKeyshareRecovery(ctx context.Context, cfg config.Bifrost, bridge thorclient.ThorchainBridge) {
	recovered := retryRecoveryUntilDone(ctx, legacyRecoveryRetryInitial, legacyRecoveryRetryMax,
		func() error { return btss.RecoverLegacyKeyShares(cfg, bridge) },
		func(err error, attempt int, next time.Duration) {
			log.Error().Err(err).Int("attempt", attempt).Dur("next_retry_in", next).Msg(
				"BIFROST RUNNING DEGRADED: keyshares for a signing vault are missing, its keysigns fail — restore localstate-<pubkey>.json from backup or point THORNode RPC at an archive node; recovery keeps retrying in the background")
		})
	if recovered {
		log.Info().Msg("legacy keyshare recovery completed in the background; this node signs for the recovered vault from the next keysign")
	}
}

// retryRecoveryUntilDone runs attempt on a doubling backoff capped at maxDelay,
// waiting BEFORE each attempt (the caller just failed one). onFailure observes
// every failed attempt along with the delay chosen for the next. It returns true
// once attempt succeeds, or false when ctx ends first.
func retryRecoveryUntilDone(ctx context.Context, initial, maxDelay time.Duration, attempt func() error, onFailure func(err error, attempt int, next time.Duration)) bool {
	delay := initial
	for n := 1; ; n++ {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return false
		}
		err := attempt()
		if err == nil {
			return true
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
		onFailure(err, n, delay)
	}
}

func superviseFrostKeyshareRecovery(ctx context.Context, cfg config.Bifrost, bridge thorclient.ThorchainBridge) {
	for attempt := 1; ; attempt++ {
		if err := btss.RecoverFrostKeyShares(cfg, bridge); err == nil {
			return
		} else {
			log.Error().Err(err).Int("attempt", attempt).Msg("XMR FROST keyshare recovery incomplete; healthy vaults remain available while retrying")
		}

		timer := time.NewTimer(constants.ThorchainBlockTime)
		select {
		case <-timer.C:
		case <-ctx.Done():
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

func initPrefix() {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount(cmd.Bech32PrefixAccAddr, cmd.Bech32PrefixAccPub)
	cosmosSDKConfg.Seal()
}

func initLog(level string, pretty bool) {
	l, err := zerolog.ParseLevel(level)
	if err != nil {
		log.Warn().Msgf("%s is not a valid log-level, falling back to 'info'", level)
	}
	var out io.Writer = os.Stdout
	if pretty {
		out = zerolog.ConsoleWriter{Out: os.Stdout}
	}
	zerolog.SetGlobalLevel(l)
	log.Logger = log.Output(out).With().Caller().Str("service", serverIdentity).Logger()

	logLevel := golog.LevelInfo
	switch l {
	case zerolog.DebugLevel:
		logLevel = golog.LevelDebug
	case zerolog.InfoLevel:
		logLevel = golog.LevelInfo
	case zerolog.ErrorLevel:
		logLevel = golog.LevelError
	case zerolog.FatalLevel:
		logLevel = golog.LevelFatal
	case zerolog.PanicLevel:
		logLevel = golog.LevelPanic
	}
	golog.SetAllLoggers(logLevel)
	if err = golog.SetLogLevel("tss-lib", level); err != nil {
		log.Fatal().Err(err).Msg("fail to set tss-lib loglevel")
	}
}
