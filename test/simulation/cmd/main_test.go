package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	bip39 "github.com/cosmos/go-bip39"
	thornodecmd "gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestMocknetValidatorsSupportChurnOut(t *testing.T) {
	minimum := constants.NewConstantValue().GetInt64Value(constants.MinimumNodesForBFT)
	if int64(len(mocknetValidatorMnemonics)-1) < minimum {
		t.Fatal("mocknet needs a spare validator so churn-out preserves the BFT minimum")
	}
	seen := make(map[string]bool)
	genesis := readRepoFile(t, "build/scripts/genesis.sh")
	for _, mnemonic := range mocknetValidatorMnemonics {
		if !bip39.IsMnemonicValid(mnemonic) || seen[mnemonic] {
			t.Fatal("mocknet validator mnemonics must be valid and distinct")
		}
		seen[mnemonic] = true
		derived, err := hd.Secp256k1.Derive()(mnemonic, "", thornodecmd.THORChainHDPath)
		if err != nil {
			t.Fatal(err)
		}
		address, err := bech32.ConvertAndEncode("tthor", hd.Secp256k1.Generate()(derived).PubKey().Address())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(genesis, "add_account "+address+" rune 200000000000000") {
			t.Fatalf("mocknet validator %s needs genesis funds for its bond and fees", address)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve caller path")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()

	path := filepath.Join(repoRoot(t), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", rel, err)
	}
	return string(data)
}

func TestLiteClientConstructorsCoverSimChains(t *testing.T) {
	for _, chain := range acommon.SimChains {
		if _, ok := liteClientConstructors[chain]; !ok {
			t.Fatalf("missing lite client constructor for sim chain %s", chain)
		}
	}
}

func TestChainRPCsCoverSimChains(t *testing.T) {
	for _, chain := range acommon.SimChains {
		if _, ok := chainRPCs[chain]; !ok {
			t.Fatalf("missing chain RPC entry for sim chain %s", chain)
		}
	}
}

func TestMocknetFundingSupportsXMR(t *testing.T) {
	mocknetGo := readRepoFile(t, "test/simulation/cmd/mocknet.go")
	if !strings.Contains(mocknetGo, "case common.XMRChain:") {
		t.Fatal("mocknet funding switch does not seed XMR simulation users")
	}
}

func TestThorUserFundingPlanKeepsExistingRuneUsersInLaterChainSeeding(t *testing.T) {
	seedThor, seedChains := thorUserFundingPlan(common.NewCoins(common.NewCoin(common.RuneAsset(), sdkmath.NewUint(common.One))))
	if seedThor {
		t.Fatal("users with existing RUNE should not receive another THOR seed transfer")
	}
	if !seedChains {
		t.Fatal("users with existing RUNE must still be included in later L1 chain seeding on reruns")
	}
}

// TestGetHeightDefaultsToOneWithoutProvider keeps the legacy mocknet
// behavior for chains that do not need a chain-specific network-fee height.
func TestGetHeightDefaultsToOneWithoutProvider(t *testing.T) {
	height, err := getHeight(common.GAIAChain, map[common.Chain]LiteChainClient{
		common.GAIAChain: &stubLiteChainClient{},
	})
	if err != nil {
		t.Fatalf("expected fallback network fee height without error, got %v", err)
	}
	if height != 1 {
		t.Fatalf("expected fallback network fee height 1, got %d", height)
	}
}

// TestGetHeightUsesProvider covers the XRP path where the mock daemon's
// current ledger must be used to avoid late scanner lgrNotFound failures.
func TestGetHeightUsesProvider(t *testing.T) {
	client := &stubGetHeightClient{height: 46}

	height, err := getHeight(common.XRPChain, map[common.Chain]LiteChainClient{
		common.XRPChain: client,
	})
	if err != nil {
		t.Fatalf("expected provider network fee height without error, got %v", err)
	}
	if height != 46 {
		t.Fatalf("expected provider network fee height 46, got %d", height)
	}
	if client.calls != 1 {
		t.Fatalf("expected provider to be called once, got %d", client.calls)
	}
}

// TestGetHeightRejectsNonPositiveProviderHeight prevents an opt-in chain
// client from silently reintroducing the stale height-1 class of scanner bugs.
func TestGetHeightRejectsNonPositiveProviderHeight(t *testing.T) {
	client := &stubGetHeightClient{height: 0}

	_, err := getHeight(common.XRPChain, map[common.Chain]LiteChainClient{
		common.XRPChain: client,
	})
	if err == nil {
		t.Fatal("expected non-positive provider network fee height to fail")
	}
}

func TestMocknetWaitsForXMRMasterAccountReadiness(t *testing.T) {
	mocknetGo := readRepoFile(t, "test/simulation/cmd/mocknet.go")

	if strings.Contains(mocknetGo, "account, err := master.ChainClients[chain].GetAccount(nil)") {
		t.Fatal("mocknet should not call GetAccount(nil) directly when logging master balances; XMR startup can transiently report block height 0")
	}
	if !strings.Contains(mocknetGo, "isRetryableXMRStartupError") {
		t.Fatal("mocknet should recognize and retry the transient XMR block-0 startup error before failing master account discovery")
	}
	if !strings.Contains(mocknetGo, "isRetryableZeroBalanceStartupState") {
		t.Fatal("mocknet should retry zero-balance startup states for chains whose master account must be pre-funded before simulation account seeding")
	}
	if !strings.Contains(mocknetGo, "isRetryableTransportStartupError") {
		t.Fatal("mocknet should retry transient transport failures while querying master-account balances during chain startup")
	}
	if !strings.Contains(mocknetGo, "chain.Equals(common.SOLChain)") {
		t.Fatal("mocknet should also wait for the Solana master account to receive its startup airdrop before funding simulation users")
	}
	if !strings.Contains(mocknetGo, "allocating spare simulation user for background actors") {
		t.Fatal("mocknet should provision at least one spare simulation user so background actors do not deadlock the DAG when execution parallelism is 1")
	}
}

func TestMocknetXRPBootstrapWaitsForRPCAndRetriesMasterFunding(t *testing.T) {
	entrypoint := readRepoFile(t, "build/scripts/xrp/start-xrp.sh")

	for _, snippet := range []string{
		`wait_for_rippled()`,
		`master_account_ready()`,
		`until "${RIPPLED_BIN}" server_state`,
		`while ! master_account_ready; do`,
		`fund_submitted=false`,
		`"engine_result" : "tesSUCCESS"`,
	} {
		if !strings.Contains(entrypoint, snippet) {
			t.Fatalf("xrp bootstrap entrypoint must contain %q so clean resets wait for rippled and retry master funding", snippet)
		}
	}
}

func TestMocknetMoneroDaemonRunsOffline(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/daemon-entrypoint.sh")
	if !strings.Contains(entrypoint, "--offline") {
		t.Fatal("mocknet monero daemon should run offline so clean resets do not attempt to sync public peers instead of mining local regtest blocks")
	}
}

func TestMocknetMoneroWalletMaintainsBlockGenerationAcrossDaemonRestarts(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/wallet-entrypoint.sh")

	for _, snippet := range []string{
		`current_height()`,
		`generate_blocks()`,
		`"generateblocks"`,
		`BOOTSTRAP_HEIGHT="${XMR_BOOTSTRAP_HEIGHT:-512}"`,
		`ensure_daemon_blocks &`,
		`kill "${block_pid}"`,
	} {
		if !strings.Contains(entrypoint, snippet) {
			t.Fatalf("mocknet monero wallet entrypoint must contain %q so block generation resumes when monerod restarts without restarting the wallet container", snippet)
		}
	}
}

func TestMocknetMoneroWalletAllowsFakechainDaemonVersionSkew(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/wallet-entrypoint.sh")
	if !strings.Contains(entrypoint, "--allow-mismatched-daemon-version") {
		t.Fatal("mocknet monero wallet-rpc should allow mismatched daemon version checks so regtest fakechain startup does not fail refresh with wallet_is_outdated || daemon_is_outdated")
	}
}

func TestMocknetMoneroWalletRescansExistingWalletAfterDaemonReset(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/monero/wallet-entrypoint.sh")

	if !strings.Contains(entrypoint, `wallet_request "rescan_blockchain" '{}'`) {
		t.Fatal("mocknet monero wallet entrypoint must rescan an existing wallet on startup so reruns recover from deep fakechain reorgs after daemon resets")
	}
}

func TestMocknetAvalancheDoesNotShutdownOnDockerDiskThreshold(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, "--system-tracker-disk-required-available-space-percentage=0") {
		t.Fatal("mocknet avalanche service should disable the default disk-space shutdown threshold; Docker Desktop often reports low internal free percentage even when the host has enough space for simulation")
	}
	if !strings.Contains(compose, "--system-tracker-disk-warning-available-space-percentage=0") {
		t.Fatal("mocknet avalanche service should also zero the disk-space warning threshold so the local node stays healthy under Docker Desktop")
	}
}

func TestMocknetBifrostDeclaresXMRChainNetwork(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, `BIFROST_CHAINS_XMR_CHAIN_NETWORK: "mocknet"`) {
		t.Fatal("mocknet bifrost must set BIFROST_CHAINS_XMR_CHAIN_NETWORK=mocknet so the XMR signer client can distinguish trusted mocknet sidecar traffic from public deployments")
	}
}

func TestMocknetBifrostConfiguresXMRSignerConnection(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	for _, snippet := range []string{
		`BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER: "${XMR_FROST_AUTH_BEARER:-xmr-frost-mocknet-token}"`,
		`BIFROST_CHAINS_XMR_SIGNER_EXPECTED_NETWORK: "mocknet"`,
		`BIFROST_CHAINS_XMR_SIGNER_ALLOW_INSECURE_HTTP: "true"`,
	} {
		if !strings.Contains(compose, snippet) {
			t.Fatalf("mocknet bifrost compose must contain %q so the XMR client can authenticate and connect to its mocknet HTTP signer", snippet)
		}
	}
	if strings.Contains(compose, "BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER:") {
		t.Fatal("mocknet bifrost must use the signer-specific XMR authorization setting")
	}
}

func TestMocknetExplicitlyEnablesXMRMonovaultKeygen(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	start := strings.Index(compose, "  _thornode: &thornode")
	if start < 0 {
		t.Fatal("failed to locate mocknet THORNode service anchor")
	}
	relativeEnd := strings.Index(compose[start:], "\n  thornode:")
	if relativeEnd < 0 {
		t.Fatal("failed to locate the end of the mocknet THORNode service anchor")
	}
	end := start + relativeEnd
	if !strings.Contains(compose[start:end], "ENABLE_MONOVAULT_KEYGEN: 1") {
		t.Fatal("mocknet THORNode must explicitly enable S00 XMR monovault keygen")
	}
}

func TestStandaloneXMRDeploymentsExplicitlyEnableScanner(t *testing.T) {
	stagenetCompose := readRepoFile(t, "build/docker/docker-compose.xmr-stagenet.yml")
	if !strings.Contains(stagenetCompose, `HALT_SCANNER_XMR: "${HALT_SCANNER_XMR:-0}"`) {
		t.Fatal("standalone XMR stagenet compose must explicitly enable the fail-closed XMR scanner")
	}

	chainnetRunbook := readRepoFile(t, "build/docker/xmr-chainnet-post-deploy.sh")
	enableScannerAt := strings.Index(chainnetRunbook, "mimir HaltScannerXMR 0")
	waitForObservationsAt := strings.Index(chainnetRunbook, "Waiting for dog bifrost to report XMR fee + chain tip")
	if enableScannerAt < 0 || waitForObservationsAt < 0 || enableScannerAt > waitForObservationsAt {
		t.Fatal("standalone XMR chainnet post-deploy must enable the scanner before waiting for XMR observations")
	}
}

func TestMocknetBifrostDoesNotWaitForSolanaRPC(t *testing.T) {
	entrypoint := readRepoFile(t, "build/scripts/bifrost.sh")
	// The SOL scanner owns initial-slot retries; its tests cover recovery and stop.
	// Waiting here would prevent every chain from starting while SOL RPC is down.
	if strings.Contains(entrypoint, "wait_for_solana_rpc") {
		t.Fatal("bifrost entrypoint must leave Solana RPC readiness to the SOL scanner")
	}
}

func TestMocknetSolanaRestartsValidatorInsideContainer(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/solana/entrypoint.sh")
	for _, snippet := range []string{
		`LEDGER_SHRED_LIMIT="${LEDGER_SHRED_LIMIT:-100000}"`,
		`start_validator()`,
		`Solana test validator exited with code`,
		`wait "$validator_wrapper_pid"`,
		`until solana airdrop`,
	} {
		if !strings.Contains(entrypoint, snippet) {
			t.Fatalf("solana mocknet entrypoint must contain %q so local OOM-kills restart the validator without restarting Bifrost during churn keygen", snippet)
		}
	}
}

func TestXMRSignerEntrypointSupportsRegistryAndLocalImages(t *testing.T) {
	for _, path := range []string{
		"build/docker/docker-compose.yml",
		"build/docker/docker-compose.xmr-chainnet.yml",
		"build/docker/docker-compose.xmr-stagenet.yml",
	} {
		compose := readRepoFile(t, path)
		for _, snippet := range []string{
			"/usr/local/bin/xmr-frost-signer",
			"/bin/service",
			`missing xmr-frost-signer entrypoint`,
		} {
			if !strings.Contains(compose, snippet) {
				t.Fatalf("%s must contain %q so the XMR signer entrypoint works with both the local dev image and the registry image shape", path, snippet)
			}
		}
	}
}

func TestMocknetClusterPublishesBifrostAndXMRFrostReadinessPorts(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	for _, snippet := range []string{
		"- 6041:6040",
		"- 6042:6040",
		"- 6043:6040",
		"- 6044:6040",
		`- "127.0.0.1:8285:8283"`,
		`- "127.0.0.1:8286:8283"`,
		`- "127.0.0.1:8287:8283"`,
		`- "127.0.0.1:8288:8283"`,
	} {
		if !strings.Contains(compose, snippet) {
			t.Fatalf("mocknet cluster compose must publish %q so simulation can gate churn on all Bifrost and XMR FROST readiness endpoints", snippet)
		}
	}
}

func TestMocknetDogecoinRaisesRPCWorkQueueDepth(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")
	if !strings.Contains(compose, `- "./mocknet:/mocknet"`) {
		t.Fatal("mocknet dogecoin service should mount the local mocknet scripts directory so the repo can override the default node-launcher entrypoint")
	}
	if !strings.Contains(compose, `entrypoint: "/mocknet/dogecoin-entrypoint.sh"`) {
		t.Fatal("mocknet dogecoin service should use the repo dogecoin entrypoint so local queue-depth fixes apply during simulation")
	}

	entrypoint := readRepoFile(t, "build/docker/mocknet/dogecoin-entrypoint.sh")
	if !strings.Contains(entrypoint, "-rpcworkqueue=256") {
		t.Fatal("mocknet dogecoin entrypoint should raise rpcworkqueue so startup importaddress bursts do not fail with work queue depth exceeded")
	}
}

func TestMocknetTronSupernodeDisablesParticipationThreshold(t *testing.T) {
	entrypoint := readRepoFile(t, "build/docker/mocknet/tron-entrypoint.sh")
	if !strings.Contains(entrypoint, "minParticipationRate = 0") {
		t.Fatal("mocknet tron supernode entrypoint should disable the default participation threshold so the single-witness local chain keeps producing blocks")
	}
}

func TestMocknetCosmosSidecarsRestartAfterDockerRestart(t *testing.T) {
	compose := readRepoFile(t, "build/docker/docker-compose.yml")

	for _, service := range []string{"gaia", "noble"} {
		pattern := regexp.MustCompile(`(?ms)^  ` + service + `:\n(?:    .*\n)*?    restart: unless-stopped\n`)
		if !pattern.MatchString(compose) {
			t.Fatalf("mocknet %s service should restart automatically so simulation reruns survive Docker Desktop restarts", service)
		}
	}
}

func TestSimulationUsesIPv4LoopbackForHostMappedRPCs(t *testing.T) {
	mocknetGo := readRepoFile(t, "test/simulation/cmd/mocknet.go")

	if strings.Contains(mocknetGo, "localhost:") {
		t.Fatal("simulation mocknet rpc endpoints should use explicit IPv4 loopback to avoid Docker Desktop localhost IPv6 resolution failures")
	}

	for _, snippet := range []string{
		`common.GAIAChain:  "127.0.0.1:9091"`,
		`common.NOBLEChain: "127.0.0.1:9092"`,
		`common.SOLChain:   "http://127.0.0.1:8899"`,
		`BIFROST_THORCHAIN_CHAIN_HOST", "127.0.0.1:1317"`,
		`BIFROST_THORCHAIN_CHAIN_RPC", "127.0.0.1:26657"`,
		`BIFROST_THORCHAIN_CHAIN_EBIFROST", "127.0.0.1:50051"`,
	} {
		if !strings.Contains(mocknetGo, snippet) {
			t.Fatalf("missing expected IPv4 loopback mocknet endpoint %q", snippet)
		}
	}
}

func TestSimulationWaitsForEVMRPCReadinessBeforeCreatingUsers(t *testing.T) {
	mainGo := readRepoFile(t, "test/simulation/cmd/main.go")
	if !strings.Contains(mainGo, "waitForEVMRPCReadiness") {
		t.Fatal("simulation startup should wait for local EVM RPCs before creating chain clients")
	}
	for _, chain := range []string{"common.ETHChain", "common.AVAXChain", "common.BASEChain", "common.POLChain"} {
		if !strings.Contains(mainGo, chain) {
			t.Fatalf("expected simulation startup to include %s in EVM readiness checks", chain)
		}
	}
}

func TestSimulationWaitsForBifrostReadinessBeforeCreatingUsers(t *testing.T) {
	mainGo := readRepoFile(t, "test/simulation/cmd/main.go")
	if !strings.Contains(mainGo, "waitForBifrostReadiness()") {
		t.Fatal("simulation startup should wait for bifrost readiness before creating chain clients")
	}
	if !strings.Contains(mainGo, "http://localhost:9000/metrics") {
		t.Fatal("bifrost readiness should fall back to the metrics endpoint when the legacy info port is unavailable")
	}
}

func TestSimulationWaitsForStableTronRPCReadinessBeforeCreatingUsers(t *testing.T) {
	mainGo := readRepoFile(t, "test/simulation/cmd/main.go")
	if !strings.Contains(mainGo, "waitForTronRPCReadiness(chainRPCs[common.TRONChain])") {
		t.Fatal("simulation startup should wait for stable TRON block production before creating chain clients")
	}
	if !strings.Contains(mainGo, "const requiredHeight int64 = 20") {
		t.Fatal("TRON readiness should require a meaningful block height before proceeding")
	}
	if !strings.Contains(mainGo, "const requiredAdvances = 3") {
		t.Fatal("TRON readiness should require multiple advancing block heights before proceeding")
	}
	if !strings.Contains(mainGo, "tron readiness timed out after") {
		t.Fatal("TRON readiness should time out with a clear error instead of hanging forever when block production stalls")
	}
}

func TestSimulationWaitsForStableSolanaRPCReadinessBeforeCreatingUsers(t *testing.T) {
	mainGo := readRepoFile(t, "test/simulation/cmd/main.go")
	if !strings.Contains(mainGo, "waitForSolanaRPCReadiness(chainRPCs[common.SOLChain])") {
		t.Fatal("simulation startup should wait for stable Solana slot production before creating chain clients")
	}
	if !strings.Contains(mainGo, `method":"getSlot"`) {
		t.Fatal("Solana readiness should poll getSlot before simulation users are created")
	}
	if !strings.Contains(mainGo, "const requiredAdvances = 2") {
		t.Fatal("Solana readiness should require advancing slots before proceeding")
	}
	if !strings.Contains(mainGo, "solana readiness timed out after") {
		t.Fatal("Solana readiness should time out with a clear error instead of hanging forever when block production stalls")
	}
}

func TestSimulationWaitsForZecInitialBlocksBeforeCreatingUsers(t *testing.T) {
	mainGo := readRepoFile(t, "test/simulation/cmd/main.go")
	if !strings.Contains(mainGo, "waitForZecRPCReadiness(chainRPCs[common.ZECChain])") {
		t.Fatal("simulation startup should wait for ZEC initial block generation before creating chain clients")
	}
	if !strings.Contains(mainGo, `method":"getblockcount"`) {
		t.Fatal("ZEC readiness should poll getblockcount before simulation users are created")
	}
	if !strings.Contains(mainGo, "const initialBlocks int64 = 400") {
		t.Fatal("ZEC readiness should wait past the 400-block warm-up")
	}
	if !strings.Contains(mainGo, "zec readiness timed out after") {
		t.Fatal("ZEC readiness should time out with a clear error instead of racing immature coinbase outputs")
	}
}

func TestZecFundingTransfersSplitWhatIsMissing(t *testing.T) {
	amount := sdkmath.NewUint(5)
	cases := []struct {
		name    string
		current uint64
		want    []uint64
	}{
		{"fresh user gets one utxo per tx", 0, []uint64{5, 5, 5, 5}},
		{"partial balance tops up the remainder", 7, []uint64{5, 5, 3}},
		{"exact target sends nothing", 20, nil},
		{"over target sends nothing", 23, nil},
	}
	for _, tc := range cases {
		got := zecFundingTransfers(sdkmath.NewUint(tc.current), amount, 4)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %d transfers %v, want %v", tc.name, len(got), got, tc.want)
		}
		for i := range got {
			if !got[i].Equal(sdkmath.NewUint(tc.want[i])) {
				t.Fatalf("%s: transfer %d = %s, want %d", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestWaitForTronRPCReadinessRecoversAfterStableAdvances(t *testing.T) {
	now := time.Unix(0, 0)
	heights := []int64{10, 20, 20, 21, 21, 22}
	call := 0

	err := waitForTronRPCReadinessWithClock(
		"http://tron",
		func(string) (int64, error) {
			if call >= len(heights) {
				return heights[len(heights)-1], nil
			}
			height := heights[call]
			call++
			return height, nil
		},
		func() time.Time { return now },
		func(d time.Duration) { now = now.Add(d) },
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("expected stable TRON block production to satisfy readiness, got %v", err)
	}
}

func TestWaitForTronRPCReadinessTimesOutWhenHeightStalls(t *testing.T) {
	now := time.Unix(0, 0)

	err := waitForTronRPCReadinessWithClock(
		"http://tron",
		func(string) (int64, error) { return 24, nil },
		func() time.Time { return now },
		func(d time.Duration) { now = now.Add(d) },
		3*time.Second,
	)
	if err == nil {
		t.Fatal("expected stalled TRON height to time out")
	}
	if !strings.Contains(err.Error(), "height=24") {
		t.Fatalf("expected timeout to report the stalled TRON height, got %v", err)
	}
}

func TestWaitForSolanaRPCReadinessRecoversAfterStableAdvances(t *testing.T) {
	now := time.Unix(0, 0)
	slots := []uint64{1, 1, 2, 2, 3}
	call := 0

	err := waitForSolanaRPCReadinessWithClock(
		"http://solana",
		func(string) (uint64, error) {
			if call >= len(slots) {
				return slots[len(slots)-1], nil
			}
			slot := slots[call]
			call++
			return slot, nil
		},
		func() time.Time { return now },
		func(d time.Duration) { now = now.Add(d) },
		10*time.Second,
	)
	if err != nil {
		t.Fatalf("expected stable Solana slot production to satisfy readiness, got %v", err)
	}
}

func TestWaitForSolanaRPCReadinessTimesOutWhenSlotStalls(t *testing.T) {
	now := time.Unix(0, 0)

	err := waitForSolanaRPCReadinessWithClock(
		"http://solana",
		func(string) (uint64, error) { return 42, nil },
		func() time.Time { return now },
		func(d time.Duration) { now = now.Add(d) },
		3*time.Second,
	)
	if err == nil {
		t.Fatal("expected stalled Solana slot to time out")
	}
	if !strings.Contains(err.Error(), "slot=42") {
		t.Fatalf("expected timeout to report the stalled Solana slot, got %v", err)
	}
}

func TestWaitForBifrostReadinessFallsBackToMetricsEndpoint(t *testing.T) {
	endpoints := []string{
		"http://localhost:6040/ping",
		"http://localhost:6040/p2pid",
		"http://localhost:9000/metrics",
	}

	var calls []string
	waitForBifrostReadinessWithSleep(
		endpoints,
		func(endpoint string) (*http.Response, error) {
			calls = append(calls, endpoint)
			if endpoint == "http://localhost:9000/metrics" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
				}, nil
			}
			return nil, errors.New("connection refused")
		},
		func(time.Duration) {
			t.Fatal("did not expect bifrost readiness to sleep when the fallback endpoint is already healthy")
		},
	)

	if strings.Join(calls, ",") != strings.Join(endpoints, ",") {
		t.Fatalf("expected readiness endpoints to be checked in order, got %v", calls)
	}
}

func TestWaitForBifrostReadinessRetriesUntilAnyEndpointIsHealthy(t *testing.T) {
	metricsPolls := 0
	sleepCalls := 0

	waitForBifrostReadinessWithSleep(
		[]string{
			"http://localhost:6040/ping",
			"http://localhost:9000/metrics",
		},
		func(endpoint string) (*http.Response, error) {
			switch endpoint {
			case "http://localhost:6040/ping":
				return nil, errors.New("connection refused")
			case "http://localhost:9000/metrics":
				metricsPolls++
				status := http.StatusServiceUnavailable
				if metricsPolls >= 3 {
					status = http.StatusOK
				}
				return &http.Response{
					StatusCode: status,
					Body:       io.NopCloser(strings.NewReader("ok")),
				}, nil
			default:
				t.Fatalf("unexpected endpoint %s", endpoint)
				return nil, nil
			}
		},
		func(time.Duration) {
			sleepCalls++
		},
	)

	if metricsPolls != 3 {
		t.Fatalf("expected 3 metrics polls before readiness, got %d", metricsPolls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps before readiness, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesTransientXMRStartupError(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, sdkmath.NewUint(42))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return nil, errors.New(`http 503: {"error":"failed to fetch scannable block 1: invalid node (response wasn't the expected json: {\r\n  \"error\": {\r\n    \"code\": -2,\r\n    \"message\": \"Requested block height: 1 greater than current top block height: 0\"\r\n  },\r\n  \"id\": 0,\r\n  \"jsonrpc\": \"2.0\"\r\n})"}`)
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithMinAmountAndSleep(common.XMRChain, client, sdkmath.ZeroUint(), masterAccountRetryAttempts, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient XMR startup error to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesTransientXMRDaemonHeightError(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.XMRAsset, sdkmath.NewUint(84))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return nil, errors.New(`http 503: {"error":"failed to query daemon height: daemon RPC get_info failed"}`)
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithMinAmountAndSleep(common.XMRChain, client, sdkmath.ZeroUint(), masterAccountRetryAttempts, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient XMR daemon-height error to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesZeroBalanceXRPStartupState(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.XRPAsset, sdkmath.NewUint(25*common.One))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return &common.Account{
				Coins: common.NewCoins(common.NewCoin(common.XRPAsset, sdkmath.ZeroUint())),
			}, nil
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithMinAmountAndSleep(common.XRPChain, client, sdkmath.ZeroUint(), masterAccountRetryAttempts, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient XRP zero-balance startup state to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered XRP account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 XRP account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesZeroBalanceSOLStartupState(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.SOLAsset, sdkmath.NewUint(25*common.One))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return &common.Account{
				Coins: common.NewCoins(common.NewCoin(common.SOLAsset, sdkmath.ZeroUint())),
			}, nil
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithMinAmountAndSleep(common.SOLChain, client, sdkmath.ZeroUint(), masterAccountRetryAttempts, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient Solana zero-balance startup state to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered Solana account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 Solana account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesTransientSOLRPCStartupError(t *testing.T) {
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.SOLAsset, sdkmath.NewUint(25*common.One))),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return nil, errors.New(`fail to get balance: failed to call getBalance: failed to perform request: Post "http://localhost:8899": read tcp [::1]:55433->[::1]:8899: read: connection reset by peer`)
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithMinAmountAndSleep(common.SOLChain, client, sdkmath.ZeroUint(), masterAccountRetryAttempts, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected transient Solana RPC startup error to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected recovered Solana account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 Solana account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestWaitForMasterAccountRetriesUntilRequiredFunding(t *testing.T) {
	required := sdkmath.NewUint(10 * common.One)
	wantAccount := &common.Account{
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, required)),
	}

	client := &stubLiteChainClient{}
	client.getAccount = func(_ *common.PubKey) (*common.Account, error) {
		client.calls++
		if client.calls < 3 {
			return &common.Account{
				Coins: common.NewCoins(common.NewCoin(common.DASHAsset, sdkmath.NewUint(uint64(client.calls)*common.One))),
			}, nil
		}
		return wantAccount, nil
	}

	sleepCalls := 0
	got, err := waitForMasterAccountWithMinAmountAndSleep(common.DASHChain, client, required, 4, func(time.Duration) {
		sleepCalls++
	})
	if err != nil {
		t.Fatalf("expected master funding wait to recover, got %v", err)
	}
	if got != wantAccount {
		t.Fatal("expected funded account to be returned")
	}
	if client.calls != 3 {
		t.Fatalf("expected 3 DASH account attempts, got %d", client.calls)
	}
	if sleepCalls != 2 {
		t.Fatalf("expected 2 retry sleeps, got %d", sleepCalls)
	}
}

func TestSimulationTargetsUseExplicitStageDefaults(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	wantSingle := "TEST_SIMULATION_STAGES ?= seed,bootstrap,xmr-coverage,arb,swaps,memoless-swaps,consolidate,memoless-refunds,solvency,ragnarok"
	if !strings.Contains(makefile, wantSingle) {
		t.Fatalf("missing expected single-node simulation stages: %s", wantSingle)
	}

	wantCluster := "TEST_SIMULATION_CLUSTER_STAGES ?= seed,bootstrap,xmr-coverage,xmr-spent-refs,swaps,memoless-swaps,dash-instantsend,inactive-vault-refunds,memoless-refunds,cluster-churn,tron-solvency-fragmentation,solvency,xmr-post-churn,ragnarok"
	if !strings.Contains(makefile, wantCluster) {
		t.Fatalf("missing expected cluster simulation stages: %s", wantCluster)
	}

	if !strings.Contains(makefile, "@STAGES=$(TEST_SIMULATION_STAGES) docker run") {
		t.Fatal("make test-simulation-no-reset should set STAGES from TEST_SIMULATION_STAGES")
	}
	if !strings.Contains(makefile, "@STAGES=$(TEST_SIMULATION_CLUSTER_STAGES) docker run") {
		t.Fatal("make test-simulation-cluster should set STAGES from TEST_SIMULATION_CLUSTER_STAGES")
	}
	if strings.Count(makefile, "-e PARALLELISM=$(PARALLELISM)") < 3 {
		t.Fatal("simulation docker targets should pass the explicit make PARALLELISM value into the sim container")
	}
	if strings.Count(makefile, "-e RANDOM_SEED_HEX") < 2 {
		t.Fatal("simulation docker targets should pass RANDOM_SEED_HEX into the sim container for reproducible failures")
	}
	if strings.Count(makefile, "-e SIMULATION_PPROF_ADDRESS") < 2 {
		t.Fatal("simulation docker targets should pass SIMULATION_PPROF_ADDRESS into the sim container so local reruns can avoid stale pprof ports")
	}
	if strings.Count(makefile, "@docker stop docker-midgard-1 docker-midgard-db-1 >/dev/null 2>&1 || true") < 2 {
		t.Fatal("make test-simulation and make test-simulation-cluster should both stop midgard services before running simulation workloads")
	}
}

func TestSimulationTargetsOnlyRequestDockerTTYInteractively(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")

	if !strings.Contains(makefile, "# docker tty args are disabled in CI and non-interactive shells") {
		t.Fatal("missing documentation that docker tty args must be disabled outside interactive shells")
	}
	if !strings.Contains(makefile, "ifneq ($(shell test -t 0 && test -t 1 && echo tty),)") {
		t.Fatal("docker tty args should only be enabled when stdin and stdout are both terminals")
	}
}

func TestSimulationStageRegistryOrder(t *testing.T) {
	stageNames := make([]string, 0, len(simulationStages()))
	for _, stage := range simulationStages() {
		stageNames = append(stageNames, stage.name)
	}

	assertStageOrder(t, stageNames, "bootstrap", "xmr-coverage")
	assertStageOrder(t, stageNames, "xmr-coverage", "arb")
	assertStageOrder(t, stageNames, "memoless-refunds", "cluster-churn")
	assertStageOrder(t, stageNames, "xmr-spent-refs", "jailed-keysign-fallback")
	assertStageOrder(t, stageNames, "churn", "tron-solvency-fragmentation")
	assertStageOrder(t, stageNames, "tron-solvency-fragmentation", "xmr-post-churn")
	assertStageOrder(t, stageNames, "tron-solvency-fragmentation", "ragnarok")
	assertStageOrder(t, stageNames, "xmr-post-churn", "ragnarok")
}

func TestSimulationStageNamesUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(simulationStages()))
	for _, stage := range simulationStages() {
		if _, exists := seen[stage.name]; exists {
			t.Fatalf("simulation stage %q is registered more than once", stage.name)
		}
		seen[stage.name] = struct{}{}
	}
}

func TestDockerignoreKeepsSimtestBuildContextLean(t *testing.T) {
	dockerignore := readRepoFile(t, "ci/Dockerfile.simtest.dockerignore")

	for _, snippet := range []string{
		".git",
		"thornode",
		"**/target",
	} {
		if !strings.Contains(dockerignore, snippet) {
			t.Fatalf("expected .dockerignore to contain %q so ci/Dockerfile.simtest does not send local build artifacts in the docker context", snippet)
		}
	}
}

func assertStageOrder(t *testing.T, stages []string, before string, after string) {
	t.Helper()

	beforeIdx := -1
	afterIdx := -1
	for i, stage := range stages {
		switch stage {
		case before:
			beforeIdx = i
		case after:
			afterIdx = i
		}
	}

	if beforeIdx == -1 || afterIdx == -1 {
		t.Fatalf("missing expected stages %s or %s in %v", before, after, stages)
	}
	if beforeIdx >= afterIdx {
		t.Fatalf("expected %s before %s, got %v", before, after, stages)
	}
}

type stubLiteChainClient struct {
	calls      int
	getAccount func(*common.PubKey) (*common.Account, error)
}

func (s *stubLiteChainClient) GetAccount(pk *common.PubKey) (*common.Account, error) {
	return s.getAccount(pk)
}

func (s *stubLiteChainClient) SignTx(SimTx) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (s *stubLiteChainClient) BroadcastTx([]byte) (string, error) {
	return "", errors.New("not implemented")
}

type stubGetHeightClient struct {
	stubLiteChainClient
	height int64
	calls  int
}

// GetHeight lets tests exercise the opt-in path without a live chain RPC.
func (s *stubGetHeightClient) GetHeight() (int64, error) {
	s.calls++
	return s.height, nil
}

func TestHistoricalXMRRefundStageRequiresExplicitSelection(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		stages := selectedSimulationStages(map[string]bool{"all": true, "xmr-historical-refund": explicit})
		found := false
		for _, stage := range stages {
			if stage.name == "xmr-historical-refund" {
				found = true
			}
		}
		if found != explicit {
			t.Fatalf("dedicated historical-refund stage: explicit=%v found=%v", explicit, found)
		}
	}
}

func TestXMRKeygenRecoveryStageRequiresExplicitSelection(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		stages := selectedSimulationStages(map[string]bool{"all": true, "xmr-keygen-recovery": explicit})
		found := false
		for _, stage := range stages {
			if stage.name == "xmr-keygen-recovery" {
				found = true
				if stage.defaultOn || stage.constructor == nil {
					t.Fatal("recovery requires an explicit, executable stage")
				}
			}
		}
		if found != explicit {
			t.Fatalf("recovery selection: explicit=%v found=%v", explicit, found)
		}
	}
}
