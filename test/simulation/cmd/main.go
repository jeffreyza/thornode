package main

import (
	crypto_rand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "net/http/pprof"

	"github.com/rivo/tview"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/suites"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/cli"
	pkgcosmos "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/dag"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/evm"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/monero"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/solana"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/tron"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/xrp"
	"gitlab.com/thorchain/thornode/v3/test/simulation/watchers"
)

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

const (
	DefaultParallelism = "8"
	// Clean mocknet-cluster boots can take a few minutes before the TRON fullnode
	// starts relaying stable block production from the supernode.
	tronRPCReadinessTimeout   = 4 * time.Minute
	solanaRPCReadinessTimeout = 4 * time.Minute
	zecRPCReadinessTimeout    = 8 * time.Minute
	xmrSimulationWaitTimeout  = 30 * time.Second
	// Required coverage must allow the initial FROST ceremony to finish.
	xmrRequiredSimulationWaitTimeout = 8 * time.Minute
)

var bifrostReadinessEndpoints = []string{
	"http://localhost:6040/ping",
	"http://localhost:6040/p2pid",
	"http://localhost:9000/metrics",
}

var liteClientConstructors = map[common.Chain]LiteChainClientConstructor{
	common.BTCChain:   utxo.NewConstructor(chainRPCs[common.BTCChain]),
	common.LTCChain:   utxo.NewConstructor(chainRPCs[common.LTCChain]),
	common.BCHChain:   utxo.NewConstructor(chainRPCs[common.BCHChain]),
	common.DOGEChain:  utxo.NewConstructor(chainRPCs[common.DOGEChain]),
	common.DASHChain:  utxo.NewConstructor(chainRPCs[common.DASHChain]),
	common.ETHChain:   evm.NewConstructor(chainRPCs[common.ETHChain]),
	common.AVAXChain:  evm.NewConstructor(chainRPCs[common.AVAXChain]),
	common.GAIAChain:  pkgcosmos.NewConstructor(chainRPCs[common.GAIAChain]),
	common.BASEChain:  evm.NewConstructor(chainRPCs[common.BASEChain]),
	common.TRONChain:  tron.NewConstructor(chainRPCs[common.TRONChain]),
	common.XRPChain:   xrp.NewConstructor(chainRPCs[common.XRPChain]),
	common.NOBLEChain: pkgcosmos.NewConstructor(chainRPCs[common.NOBLEChain]),
	common.SOLChain:   solana.NewConstructor(chainRPCs[common.SOLChain]),
	common.XMRChain:   monero.NewConstructor(chainRPCs[common.XMRChain]),
	common.POLChain:   evm.NewConstructor(chainRPCs[common.POLChain]),
	common.ZECChain:   utxo.NewConstructor(chainRPCs[common.ZECChain]),
}

type simulationStage struct {
	name        string
	defaultOn   bool
	constructor func(rng *rand.Rand) *Actor
}

func simulationStages() []simulationStage {
	return []simulationStage{
		{name: "seed", defaultOn: true},
		{name: "bootstrap", defaultOn: true, constructor: suites.Bootstrap},
		{name: "xmr-coverage", defaultOn: true, constructor: suites.XMRCoverage},
		{name: "arb", defaultOn: true, constructor: core.NewArbActor},
		{name: "swaps", defaultOn: true, constructor: suites.Swaps},
		{name: "memoless-swaps", defaultOn: true, constructor: suites.MemolessSwaps},
		{name: "consolidate", defaultOn: true, constructor: features.Consolidate},
		{name: "dash-instantsend", defaultOn: true, constructor: features.DashInstantSend},
		// memoless-refunds runs before every churn-triggering stage so the refund
		// actors exercise the pre-churn vault set, per issue #2839 Section B.
		{name: "memoless-refunds", defaultOn: false, constructor: features.MemolessRefunds},
		{name: "cluster-churn", defaultOn: false, constructor: suites.ClusterChurn},
		{name: "churn", defaultOn: false, constructor: suites.Churn},
		{name: "tron-solvency-fragmentation", defaultOn: false, constructor: core.NewTRONSolvencyFragmentationActor},
		{name: "xmr-keygen-recovery", defaultOn: false, constructor: suites.XMRKeygenRecovery},
		{name: "xmr-spent-refs", defaultOn: false, constructor: suites.XMRSpentRefTracking},
		{name: "forced-to-leave-keygen", defaultOn: false, constructor: core.NewForcedToLeaveKeygenActor},
		{name: "forced-to-leave-keysign", defaultOn: false, constructor: core.NewForcedToLeaveKeysignActor},
		{name: "jailed-keysign-fallback", defaultOn: false, constructor: core.NewJailedKeysignFallbackActor},
		{name: "churn-balance", defaultOn: false, constructor: suites.ChurnBalance},
		{name: "xmr-historical-refund", defaultOn: false, constructor: features.XMRHistoricalRefund},
		{name: "inactive-vault-refunds", defaultOn: false, constructor: features.InactiveVaultRefunds},
		{name: "solvency", defaultOn: true, constructor: core.NewSolvencyCheckActor},
		{name: "xmr-post-churn", defaultOn: false, constructor: suites.XMRPostChurn},
		{name: "ragnarok", defaultOn: true, constructor: suites.Ragnarok},
	}
}

func simulationStageOptions() []cli.Option {
	stages := simulationStages()
	options := make([]cli.Option, 0, len(stages))
	for _, stage := range stages {
		options = append(options, cli.Option{Name: stage.name, Default: stage.defaultOn})
	}
	return options
}

func appendSimulationStages(root *Actor, enabledStages map[string]bool, rng *rand.Rand) {
	for _, stage := range selectedSimulationStages(enabledStages) {
		root.Append(stage.constructor(rng))
	}
}

func selectedSimulationStages(enabledStages map[string]bool) []simulationStage {
	var selected []simulationStage
	// Keep the old STAGES selector as an alias without registering a second
	// suite, so selecting both names (or all) still performs one churn sequence.
	enabled := func(name string) bool {
		return enabledStages[name] || enabledStages["all"] ||
			(name == "cluster-churn" && enabledStages["bond-yield"])
	}
	for _, stage := range simulationStages() {
		if stage.constructor == nil || !enabled(stage.name) {
			continue
		}
		switch stage.name {
		case "xmr-historical-refund", "xmr-keygen-recovery":
			// These need dedicated fault-injection environments; generic "all"
			// runs must explicitly request the scenario as well.
			if !enabledStages[stage.name] {
				continue
			}
		case "churn", "tron-solvency-fragmentation":
			// ClusterChurn already activates the cluster and runs selected TRON
			// assertions before requesting a validator leave.
			if enabled("cluster-churn") {
				continue
			}
		case "cluster-churn":
			if enabled("xmr-post-churn") {
				stage.constructor = suites.ClusterChurnWithXMRPostChurn
			}
			if enabled("tron-solvency-fragmentation") {
				verifyXMR := enabled("xmr-post-churn")
				stage.constructor = func(rng *rand.Rand) *Actor {
					tron := core.NewTRONSolvencyFragmentationActor(rng)
					if verifyXMR {
						return suites.ClusterChurnWithXMRPostChurnActors(rng, tron)
					}
					return suites.ClusterChurnWithPostChurnActors(rng, tron)
				}
			}
		case "xmr-post-churn":
			if enabled("cluster-churn") {
				continue
			}
		}
		selected = append(selected, stage)
	}
	return selected
}

// configureXMRSimulationProfile permits optional XMR coverage to fall back,
// but required runs must establish the inbound address before building the DAG.
func configureXMRSimulationProfile(required bool, wait func(time.Duration) (common.Address, error)) error {
	timeout := xmrSimulationWaitTimeout
	if required {
		timeout = xmrRequiredSimulationWaitTimeout
	}
	inboundAddress, err := wait(timeout)
	if err == nil && !inboundAddress.IsEmpty() {
		acommon.SetXMRChainEnabled(true)
		log.Info().
			Stringer("inbound_address", inboundAddress).
			Msg("XMR inbound address available; enabling XMR simulation coverage")
		return nil
	}
	if err == nil {
		err = fmt.Errorf("empty XMR inbound address")
	}
	acommon.SetXMRChainEnabled(false)
	if required {
		return fmt.Errorf("required XMR inbound address unavailable after %s: %w", timeout, err)
	}
	log.Warn().
		Err(err).
		Dur("timeout", timeout).
		Msg("XMR inbound address unavailable; skipping XMR simulation coverage")
	return nil
}

type inboundAddressGetter func(common.Chain) (common.Address, *common.Address, error)

// waitForXMRSimulationInbound waits for the XMR inbound address required by XMR simulation actors.
func waitForXMRSimulationInbound(timeout time.Duration, get inboundAddressGetter, sleep func(time.Duration)) (common.Address, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		inboundAddress, _, err := get(common.XMRChain)
		if err == nil && !inboundAddress.IsEmpty() {
			return inboundAddress, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("empty XMR inbound address")
		}

		if timeout <= 0 || !time.Now().Before(deadline) {
			return common.NoAddress, lastErr
		}

		delay := time.Second
		if remaining := time.Until(deadline); remaining < delay {
			delay = remaining
		}
		if delay <= 0 {
			return common.NoAddress, lastErr
		}
		sleep(delay)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Init
////////////////////////////////////////////////////////////////////////////////////////

func init() {
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.TimeOnly,
	}).With().Caller().Logger()
}

////////////////////////////////////////////////////////////////////////////////////////
// Main
////////////////////////////////////////////////////////////////////////////////////////

func main() {
	// start pprof server
	pprofAddress := os.Getenv("SIMULATION_PPROF_ADDRESS")
	if pprofAddress == "" {
		pprofAddress = ":6061"
	}
	if !strings.EqualFold(pprofAddress, "off") {
		go func() {
			log.Info().Str("address", pprofAddress).Msg("starting pprof server")
			if err := http.ListenAndServe(pprofAddress, nil); err != nil {
				log.Fatal().Err(err).Msg("pprof server failed")
			}
		}()
	} else {
		log.Info().Msg("pprof server disabled")
	}

	// prompt to filter run stages if connected to a terminal
	enabledStages := map[string]bool{}
	stages := simulationStageOptions()
	if os.Getenv("STAGES") != "" {
		for _, stage := range strings.Split(os.Getenv("STAGES"), ",") {
			enabledStages[stage] = true
		}
	} else if term.IsTerminal(int(os.Stdout.Fd())) {
		app := tview.NewApplication()
		opts := cli.NewOptions(app, stages)
		opts.SetBorder(true).SetTitle(" Select Stages ")
		app.SetRoot(opts, true)
		if err := app.Run(); err != nil {
			panic(err)
		}
		enabledStages = opts.Selected()
	} else {
		for _, stage := range stages {
			enabledStages[stage.Name] = stage.Default
		}
	}

	waitForBifrostReadiness()

	if err := waitForTronRPCReadiness(chainRPCs[common.TRONChain]); err != nil {
		log.Fatal().Err(err).Msg("failed waiting for tron rpc readiness")
	}
	if err := waitForSolanaRPCReadiness(chainRPCs[common.SOLChain]); err != nil {
		log.Fatal().Err(err).Msg("failed waiting for solana rpc readiness")
	}
	if err := waitForZecRPCReadiness(chainRPCs[common.ZECChain]); err != nil {
		log.Fatal().Err(err).Msg("failed waiting for zec rpc readiness")
	}

	for _, chain := range []common.Chain{
		common.ETHChain,
		common.AVAXChain,
		common.BASEChain,
		common.POLChain,
	} {
		waitForEVMRPCReadiness(chain, chainRPCs[chain])
	}

	var randSeed [32]byte
	randSeedHex := os.Getenv("RANDOM_SEED_HEX")
	if randSeedHex == "" {
		_, err := io.ReadFull(crypto_rand.Reader, randSeed[:])
		if err != nil {
			log.Fatal().Err(err).Msg("failed to generate random seed")
		}
	} else {
		randSeedBytes, err := hex.DecodeString(randSeedHex)
		if err != nil || len(randSeedBytes) > 32 {
			log.Fatal().Err(err).Msg("failed to parse RANDOM_SEED_HEX")
		}
		for byte_idx, byte_value := range randSeedBytes {
			randSeed[32-len(randSeedBytes)+byte_idx] = byte_value
		}
	}
	log.Info().Msgf("Random seed set to `%s`", new(big.Int).SetBytes(randSeed[:]).Text(16))
	rng := rand.New(rand.NewChaCha8(randSeed))

	// gather config from the environment
	parallelism := os.Getenv("PARALLELISM")
	if parallelism == "" {
		parallelism = DefaultParallelism
	}
	parallelismInt, err := strconv.Atoi(parallelism)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse PARALLELISM")
	}

	cfg := InitConfig(parallelismInt, enabledStages["seed"] || enabledStages["all"], rng)
	if err := configureXMRSimulationProfile(os.Getenv("XMR_POST_CHURN_REQUIRED") == "1", func(timeout time.Duration) (common.Address, error) {
		return waitForXMRSimulationInbound(timeout, thornode.GetInboundAddress, time.Sleep)
	}); err != nil {
		log.Fatal().Err(err).Msg("failed waiting for required XMR simulation readiness")
	}

	// combine all actor dags for the complete test run
	root := NewActor("Root", rng)
	appendSimulationStages(root, enabledStages, rng)

	// Check before actors start and again after they finish. Periodic checks
	// alone can be skipped entirely by a short simulation.
	invariants := watchers.NewInvariants()
	if err := runWithInvariantChecks(cfg, invariants, func() {
		for _, w := range []*Watcher{invariants, watchers.NewSolvencyHalt(), watchers.NewSecurityEvents(), watchers.NewLuvi()} {
			log.Info().Str("watcher", w.Name).Msg("starting watcher")
			go func(w *Watcher) {
				if err := w.Execute(cfg, log.Output(os.Stderr)); err != nil {
					log.Fatal().Err(err).Str("watcher", w.Name).Msg("watcher failed")
				}
			}(w)
		}
		dag.Execute(cfg, root, parallelismInt, rng)
	}); err != nil {
		log.Fatal().Err(err).Msg("invariant verification failed")
	}
}

// runWithInvariantChecks makes both boundary checks mandatory even if the
// simulation finishes before the first watcher interval.
func runWithInvariantChecks(cfg *OpConfig, invariant *Watcher, run func()) error {
	if err := invariant.Fn(cfg); err != nil {
		return fmt.Errorf("initial invariant verification: %w", err)
	}
	run()
	if err := invariant.Fn(cfg); err != nil {
		return fmt.Errorf("final invariant verification: %w", err)
	}
	return nil
}

func waitForBifrostReadiness() {
	waitForBifrostReadinessWithSleep(bifrostReadinessEndpoints, http.Get, time.Sleep)
}

func waitForBifrostReadinessWithSleep(
	endpoints []string,
	get func(string) (*http.Response, error),
	sleep func(time.Duration),
) {
	for {
		for _, endpoint := range endpoints {
			res, err := get(endpoint)
			if err == nil && res.StatusCode == http.StatusOK {
				res.Body.Close()
				return
			}
			if res != nil {
				res.Body.Close()
			}
		}

		log.Info().Msg("waiting for bifrost to be ready")
		sleep(time.Second)
	}
}

func waitForEVMRPCReadiness(chain common.Chain, endpoint string) {
	for {
		res, err := http.Post(
			endpoint,
			"application/json",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`),
		)
		if err == nil && res.StatusCode == http.StatusOK {
			body, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if readErr == nil && strings.Contains(string(body), `"result"`) {
				return
			}
		} else if res != nil {
			res.Body.Close()
		}

		log.Info().Stringer("chain", chain).Msg("waiting for evm chain to be ready")
		time.Sleep(time.Second)
	}
}

func waitForTronRPCReadiness(endpoint string) error {
	return waitForTronRPCReadinessWithClock(endpoint, getTronBlockHeight, time.Now, time.Sleep, tronRPCReadinessTimeout)
}

func waitForTronRPCReadinessWithClock(
	endpoint string,
	getHeight func(string) (int64, error),
	now func() time.Time,
	sleep func(time.Duration),
	timeout time.Duration,
) error {
	const requiredHeight int64 = 20
	const requiredAdvances = 3

	lastHeight := int64(-1)
	advances := 0
	deadline := now().Add(timeout)

	for {
		if now().After(deadline) {
			return fmt.Errorf("tron readiness timed out after %s with height=%d advances=%d", timeout, lastHeight, advances)
		}

		height, err := getHeight(endpoint)
		switch {
		case err == nil && height >= requiredHeight && lastHeight >= 0 && height > lastHeight:
			advances++
			lastHeight = height
			if advances >= requiredAdvances {
				return nil
			}
		case err == nil && lastHeight < 0:
			lastHeight = height
		case err == nil && height < lastHeight:
			lastHeight = height
			advances = 0
		case err == nil:
			lastHeight = height
		}

		log.Info().
			Int64("height", lastHeight).
			Int("advances", advances).
			Msg("waiting for tron to be ready")
		sleep(time.Second)
	}
}

func getTronBlockHeight(endpoint string) (int64, error) {
	res, err := http.Post(
		endpoint+"/wallet/getnowblock",
		"application/json",
		strings.NewReader(`{}`),
	)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected tron status code: %d", res.StatusCode)
	}

	var response struct {
		Header struct {
			RawData struct {
				Number int64 `json:"number"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}

	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return 0, err
	}

	return response.Header.RawData.Number, nil
}

func waitForSolanaRPCReadiness(endpoint string) error {
	return waitForSolanaRPCReadinessWithClock(endpoint, getSolanaSlot, time.Now, time.Sleep, solanaRPCReadinessTimeout)
}

func waitForSolanaRPCReadinessWithClock(
	endpoint string,
	getSlot func(string) (uint64, error),
	now func() time.Time,
	sleep func(time.Duration),
	timeout time.Duration,
) error {
	const requiredAdvances = 2

	lastSlot := uint64(0)
	haveSlot := false
	advances := 0
	deadline := now().Add(timeout)

	for {
		if now().After(deadline) {
			return fmt.Errorf("solana readiness timed out after %s with slot=%d advances=%d", timeout, lastSlot, advances)
		}

		slot, err := getSlot(endpoint)
		switch {
		case err == nil && !haveSlot:
			lastSlot = slot
			haveSlot = true
		case err == nil && slot > lastSlot:
			advances++
			lastSlot = slot
			if advances >= requiredAdvances {
				return nil
			}
		case err == nil && slot < lastSlot:
			lastSlot = slot
			advances = 0
		case err == nil:
			lastSlot = slot
		}

		log.Info().
			Uint64("slot", lastSlot).
			Int("advances", advances).
			Msg("waiting for solana to be ready")
		sleep(time.Second)
	}
}

func getSolanaSlot(endpoint string) (uint64, error) {
	res, err := http.Post(
		endpoint,
		"application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`),
	)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected solana status code: %d", res.StatusCode)
	}

	var response struct {
		Result uint64 `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return 0, err
	}
	if response.Error != nil {
		return 0, fmt.Errorf("solana rpc error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}

func waitForZecRPCReadiness(endpoint string) error {
	return waitForZecRPCReadinessWithClock(endpoint, getZecBlockCount, time.Now, time.Sleep, zecRPCReadinessTimeout)
}

func waitForZecRPCReadinessWithClock(
	endpoint string,
	getBlockCount func(string) (int64, error),
	now func() time.Time,
	sleep func(time.Duration),
	timeout time.Duration,
) error {
	// The container is the sole miner. Wait past its 400-block warm-up and
	// observe continued block production before funding simulation users.
	const initialBlocks int64 = 400
	lastHeight := int64(-1)
	deadline := now().Add(timeout)
	for now().Before(deadline) {
		height, err := getBlockCount(endpoint)
		if err == nil {
			if height > initialBlocks && lastHeight >= 0 && height > lastHeight {
				return nil
			}
			lastHeight = height
		}
		log.Info().Err(err).Int64("height", lastHeight).Msg("waiting for zec to be ready")
		sleep(time.Second)
	}
	return fmt.Errorf("zec readiness timed out after %s with height=%d", timeout, lastHeight)
}

var zecReadinessHTTPClient = &http.Client{Timeout: 5 * time.Second}

func getZecBlockCount(endpoint string) (int64, error) {
	res, err := zecReadinessHTTPClient.Post(
		endpoint,
		"application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"getblockcount","params":[]}`),
	)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected zec status code: %d", res.StatusCode)
	}

	var response struct {
		Result int64 `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return 0, err
	}
	if response.Error != nil {
		return 0, fmt.Errorf("zec rpc error %d: %s", response.Error.Code, response.Error.Message)
	}

	return response.Result, nil
}
