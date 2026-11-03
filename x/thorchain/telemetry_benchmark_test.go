package thorchain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/telemetry"
	gometrics "github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Mainnet-ish scale. These are the loop bounds that actually drive per-block
// telemetry cost, so the benchmark is only meaningful if they match production.
const (
	benchPools   = 100
	benchNodes   = 120
	benchVaults  = 12
	benchTxOuts  = 900 // 3 x SigningTransactionPeriod, the overdue-outbound window
	benchMimirs  = 200
	benchStreams = 50

	// The unbounded stores walked only inside the IsPeriodLastBlock gates. These
	// dominate the worst (all-gates-open) block, so a fixture without them measures
	// the steady state twice and the worst block never.
	benchLPsPerPool  = 200 // ~20k LPs, PoolsInvariant walks every one
	benchTCYClaimers = 11_600
	benchTCYStakers  = 3_000
	benchRUNEProvs   = 2_500
	benchAffiliates  = 200
	benchCredits     = 50 // stable-reserve credits: empty on mainnet, non-empty here so the parser runs
)

// enableBenchTelemetry turns the SDK telemetry sink on. Without this every
// telemetry.* call early-returns on !IsTelemetryEnabled() and the benchmark
// would silently measure only the keeper reads, hiding label-join and lock cost.
var benchTelemetryOnce sync.Once

func enableBenchTelemetry(b testing.TB) {
	b.Helper()
	// telemetry.New registers a process-global Prometheus collector, so it must
	// run exactly once - Go re-invokes a benchmark body while calibrating b.N.
	benchTelemetryOnce.Do(func() {
		if _, err := telemetry.New(telemetry.Config{
			Enabled:                 true,
			ServiceName:             "thornode-bench",
			EnableHostname:          false,
			PrometheusRetentionTime: 600,
		}); err != nil {
			b.Fatalf("enable telemetry: %v", err)
		}
	})
	if !telemetry.IsTelemetryEnabled() {
		b.Fatal("telemetry sink did not enable; benchmark would measure nothing")
	}
}

// benchETHAddress is a random ETH address in the lowercase form
// GetRandomETHAddress returns. That one derives each address from a fresh
// secp256k1 key, which at the seed's ~34k addresses was most of its cost, and
// nothing here uses the key.
func benchETHAddress(b testing.TB) common.Address {
	b.Helper()
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		b.Fatal(err)
	}
	return common.Address("0x" + hex.EncodeToString(raw[:]))
}

// seedTelemetryState populates the stores the per-block telemetry path walks.
func seedTelemetryState(b testing.TB, ctx cosmos.Context, mgr *Mgrs) {
	b.Helper()
	k := mgr.Keeper()

	assets := make([]common.Asset, 0, benchPools)
	for i := 0; i < benchPools; i++ {
		asset, err := common.NewAsset(fmt.Sprintf("ETH.TKN%d-0X%040d", i, i))
		if err != nil {
			b.Fatalf("asset %d: %v", i, err)
		}
		assets = append(assets, asset)
		pool := NewPool()
		pool.Asset = asset
		pool.BalanceRune = cosmos.NewUint(uint64(1_000+i) * common.One)
		pool.BalanceAsset = cosmos.NewUint(uint64(500+i) * common.One)
		pool.LPUnits = cosmos.NewUint(uint64(900+i) * common.One)
		pool.Status = PoolAvailable
		if err := k.SetPool(ctx, pool); err != nil {
			b.Fatalf("set pool %d: %v", i, err)
		}
		for j := 0; j < benchLPsPerPool; j++ {
			k.SetLiquidityProvider(ctx, LiquidityProvider{
				Asset:        asset,
				RuneAddress:  GetRandomRUNEAddress(),
				AssetAddress: benchETHAddress(b),
				Units:        cosmos.NewUint(uint64(j+1) * common.One),
				PendingRune:  cosmos.ZeroUint(),
				PendingAsset: cosmos.ZeroUint(),
			})
		}
		// AddTo* goes through the lazily-writing getter, so this is exactly the
		// on-disk shape GasMgr leaves behind. The last pool gets no record at all:
		// GetOutboundFeeWithheldRune writes on a zero read, so a fixture where every
		// pool has a record would let that accessor back into the telemetry path
		// without TestTelemetryWritesNoState noticing.
		if i < benchPools-1 {
			if err := k.AddToOutboundFeeWithheldRune(ctx, asset, cosmos.NewUint(uint64(i+1)*common.One)); err != nil {
				b.Fatalf("withheld %d: %v", i, err)
			}
			if err := k.AddToOutboundFeeSpentRune(ctx, asset, cosmos.NewUint(uint64(i)*common.One)); err != nil {
				b.Fatalf("spent %d: %v", i, err)
			}
		}
		if i%10 == 0 {
			k.SetSecuredAsset(ctx, SecuredAsset{Asset: asset.GetSecuredAsset(), Depth: cosmos.NewUint(uint64(i+1) * common.One)})
		}
	}

	for i := 0; i < benchNodes; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(uint64(300_000+i) * common.One)
		if err := k.SetNodeAccount(ctx, na); err != nil {
			b.Fatalf("set node %d: %v", i, err)
		}
		for _, chain := range common.AllChains {
			if err := k.SetLastObserveHeight(ctx, chain, na.NodeAddress, ctx.BlockHeight()-int64(i)); err != nil {
				b.Fatalf("set observe height: %v", err)
			}
		}
	}

	// One standby node: the preflight branch of emitChainDetailTelemetry is the
	// only manager call on the telemetry path and runs for non-active nodes only.
	standby := GetRandomValidatorNode(NodeStandby)
	if err := k.SetNodeAccount(ctx, standby); err != nil {
		b.Fatalf("set standby node: %v", err)
	}
	// A router and a pending upgrade with one vote, so the router_info and
	// upgrade_proposal branches execute.
	k.SetChainContract(ctx, ChainContract{Chain: common.ETHChain, Router: benchETHAddress(b)})
	if err := k.ProposeUpgrade(ctx, "bench", types.UpgradeProposal{Height: ctx.BlockHeight() + 1000, Info: "bench", Proposer: standby.NodeAddress}); err != nil {
		b.Fatalf("propose upgrade: %v", err)
	}
	k.ApproveUpgrade(ctx, standby.NodeAddress, "bench")

	for i := 0; i < benchVaults; i++ {
		vault := GetRandomVault()
		switch {
		case i < 2:
			vault.Status = ActiveVault
			// Flag both active vaults so the compromised and insolvent filters of the
			// inbound-availability block take their true branch.
			if i == 0 {
				k.SetMimir(ctx, compromisedVaultMimirKeyPrefix+vault.PubKey.String(), 1)
			} else {
				k.SetMimir(ctx, solvencyHaltVaultKey(vault.PubKey, common.ETHChain), 1)
			}
		case i < 4:
			vault.Status = RetiringVault
		default:
			vault.Status = InactiveVault
		}
		coins := common.Coins{}
		for j := 0; j < 20 && j < len(assets); j++ {
			coins = append(coins, common.NewCoin(assets[j], cosmos.NewUint(uint64(10+j)*common.One)))
		}
		vault.Coins = coins
		if err := k.SetVault(ctx, vault); err != nil {
			b.Fatalf("set vault %d: %v", i, err)
		}
	}

	for _, chain := range common.AllChains {
		if err := k.SetLastChainHeight(ctx, chain, ctx.BlockHeight()); err != nil {
			b.Fatalf("set chain height: %v", err)
		}
		if err := k.SaveNetworkFee(ctx, chain, NetworkFee{
			Chain: chain, TransactionSize: 1000, TransactionFeeRate: 10,
		}); err != nil {
			b.Fatalf("save network fee: %v", err)
		}
	}

	// The overdue-outbound window: one txout per height, each with a few items.
	for h := ctx.BlockHeight() - benchTxOuts; h <= ctx.BlockHeight(); h++ {
		if h < 1 {
			continue
		}
		txOut := NewTxOut(h)
		for j := 0; j < 3; j++ {
			txOut.TxArray = append(txOut.TxArray, TxOutItem{
				Chain:       common.ETHChain,
				ToAddress:   benchETHAddress(b),
				VaultPubKey: GetRandomPubKey(),
				Coin:        common.NewCoin(assets[j%len(assets)], cosmos.NewUint(common.One)),
				Memo:        "OUT:" + GetRandomTxHash().String(),
				InHash:      GetRandomTxHash(),
			})
		}
		if err := k.SetTxOut(ctx, txOut); err != nil {
			b.Fatalf("set txout %d: %v", h, err)
		}
	}

	for i := 0; i < benchMimirs; i++ {
		k.SetMimir(ctx, fmt.Sprintf("BenchKey%d", i), int64(i))
	}

	for i := 0; i < benchStreams; i++ {
		k.SetStreamingSwap(ctx, StreamingSwap{
			TxID:     GetRandomTxHash(),
			Quantity: 10,
			Interval: 1,
			Count:    uint64(i % 10),
			Deposit:  cosmos.NewUint(uint64(i+1) * common.One),
			In:       cosmos.NewUint(uint64(i) * common.One),
			Out:      cosmos.NewUint(uint64(i) * common.One),
		})
	}

	// The gated stores.
	for i := 0; i < benchTCYClaimers; i++ {
		if err := k.SetTCYClaimer(ctx, TCYClaimer{
			Asset: assets[i%len(assets)], L1Address: benchETHAddress(b), Amount: cosmos.NewUint(uint64(i + 1)),
		}); err != nil {
			b.Fatalf("set tcy claimer %d: %v", i, err)
		}
	}
	for i := 0; i < benchTCYStakers; i++ {
		if err := k.SetTCYStaker(ctx, TCYStaker{Address: GetRandomRUNEAddress(), Amount: cosmos.NewUint(uint64(i + 1))}); err != nil {
			b.Fatalf("set tcy staker %d: %v", i, err)
		}
	}
	for i := 0; i < benchRUNEProvs; i++ {
		k.SetRUNEProvider(ctx, RUNEProvider{
			RuneAddress:    GetRandomBech32Addr(),
			DepositAmount:  cosmos.NewUint(uint64(i+1) * common.One),
			WithdrawAmount: cosmos.NewUint(uint64(i) * common.One),
			Units:          cosmos.NewUint(uint64(i+1) * common.One),
		})
	}
	for i := 0; i < benchAffiliates; i++ {
		if err := k.SetAffiliateCollector(ctx, AffiliateFeeCollector{Name: fmt.Sprintf("affiliate%d", i), RuneAmount: cosmos.NewUint(uint64(i + 1))}); err != nil {
			b.Fatalf("set affiliate collector %d: %v", i, err)
		}
	}
	for i := 0; i < benchCredits; i++ {
		if err := k.SetStableReserveCredit(ctx, assets[i%len(assets)], benchETHAddress(b), cosmos.NewUint(uint64(i+1)*common.One)); err != nil {
			b.Fatalf("set credit %d: %v", i, err)
		}
	}

	// Commit the seed. On an uncommitted mutable tree every IAVL iterator takes the
	// slow path (no fast nodes), which inflated the gated blocks ~2-3x; a node's
	// EndBlock reads a committed tree, so measure that.
	cms, ok := ctx.MultiStore().(storetypes.CommitMultiStore)
	if !ok {
		b.Fatalf("multistore %T cannot commit", ctx.MultiStore())
	}
	cms.Commit()
}

// steadyHeight is a block where every IsPeriodLastBlock gate is CLOSED, i.e. the
// common case. gatedHeight opens all of them (10/50/100/600) - the worst case.
const (
	steadyHeight = 1_000_001
	gatedHeight  = 1_200_000 // divisible by 10, 50, 100 and 600
)

func benchTelemetry(b *testing.B, height int64, fn func(cosmos.Context, Manager)) {
	enableBenchTelemetry(b)
	ctx, mgr := setupBenchmarkManager(b)
	ctx = ctx.WithBlockHeight(height)
	seedTelemetryState(b, ctx, mgr)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fn(ctx, mgr)
	}
}

func BenchmarkTelemetryAll_Steady(b *testing.B) {
	benchTelemetry(b, steadyHeight, func(ctx cosmos.Context, mgr Manager) {
		if err := emitEndBlockTelemetry(ctx, mgr); err != nil {
			b.Fatal(err)
		}
	})
}

func BenchmarkTelemetryAll_Gated(b *testing.B) {
	benchTelemetry(b, gatedHeight, func(ctx cosmos.Context, mgr Manager) {
		if err := emitEndBlockTelemetry(ctx, mgr); err != nil {
			b.Fatal(err)
		}
	})
}

// Per-block cost is now STAGGERED across height%5 residues, so a single fixed
// height measures only one of the five groups. These sweep all five with the
// coarser 10/50/100/600 gates all closed (base is not divisible by 10), which is
// what 9 out of every 10 mainnet blocks actually look like. The spread between
// them is the staggering working - or not.
func BenchmarkTelemetryResidue0(b *testing.B) { benchAll(b, 1_000_005) }
func BenchmarkTelemetryResidue1(b *testing.B) { benchAll(b, 1_000_001) }
func BenchmarkTelemetryResidue2(b *testing.B) { benchAll(b, 1_000_002) }
func BenchmarkTelemetryResidue3(b *testing.B) { benchAll(b, 1_000_003) }
func BenchmarkTelemetryResidue4(b *testing.B) { benchAll(b, 1_000_004) }

// The residue sweep above deliberately never lands on height%10==0, so it covers
// only 9 of every 10 blocks. These are the periodic heavy blocks.
func BenchmarkTelemetryEvery10(b *testing.B)  { benchAll(b, 1_000_010) } // %10 only
func BenchmarkTelemetryEvery30(b *testing.B)  { benchAll(b, 1_000_020) } // %10 + %30 observe loop
func BenchmarkTelemetryEvery100(b *testing.B) { benchAll(b, 1_000_100) } // %10 + %50 + %100

func benchAll(b *testing.B, height int64) {
	benchTelemetry(b, height, func(ctx cosmos.Context, mgr Manager) {
		if err := emitEndBlockTelemetry(ctx, mgr); err != nil {
			b.Fatal(err)
		}
	})
}

// Per-emitter, so the profile attributes cost to a specific file rather than to
// one 400-line function.
func BenchmarkTelemetryChain(b *testing.B) {
	benchTelemetry(b, steadyHeight, emitChainDetailTelemetry)
}

func BenchmarkTelemetryChain_Gated(b *testing.B) {
	benchTelemetry(b, gatedHeight, emitChainDetailTelemetry)
}

func BenchmarkTelemetryVault(b *testing.B) {
	benchTelemetry(b, steadyHeight, emitVaultDetailTelemetry)
}

func BenchmarkTelemetryQueue(b *testing.B) {
	benchTelemetry(b, steadyHeight, emitQueueDetailTelemetry)
}

func BenchmarkTelemetryQueue_Gated(b *testing.B) {
	benchTelemetry(b, gatedHeight, emitQueueDetailTelemetry)
}

func BenchmarkTelemetrySolvency(b *testing.B) {
	benchTelemetry(b, steadyHeight, emitSolvencyTelemetry)
}

func BenchmarkTelemetryTCY(b *testing.B) {
	benchTelemetry(b, steadyHeight, emitTCYTelemetry)
}

func BenchmarkTelemetryMimir(b *testing.B) {
	benchTelemetry(b, steadyHeight, emitMimirTelemetry)
}

// recordingSink captures every metric write keyed by "name{label=value,...}":
// the last value per gauge and the running sum per counter. It is the assertion
// surface for the telemetry tests in telemetry_test.go - a metric that is not
// written simply is not in the map, so a "healthy zero" cannot hide there.
type recordingSink struct {
	gauges   map[string]float32
	counters map[string]float32
	samples  int
}

func newRecordingSink() *recordingSink {
	return &recordingSink{gauges: map[string]float32{}, counters: map[string]float32{}}
}

func metricKey(key []string, labels []gometrics.Label) string {
	var sb strings.Builder
	sb.WriteString(strings.Join(key, "."))
	if len(labels) > 0 {
		sb.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(l.Name)
			sb.WriteByte('=')
			sb.WriteString(l.Value)
		}
		sb.WriteByte('}')
	}
	return sb.String()
}

func (s *recordingSink) SetGauge(key []string, val float32) { s.gauges[metricKey(key, nil)] = val }
func (s *recordingSink) SetGaugeWithLabels(key []string, val float32, labels []gometrics.Label) {
	s.gauges[metricKey(key, labels)] = val
}
func (s *recordingSink) EmitKey(key []string, val float32) {}
func (s *recordingSink) IncrCounter(key []string, val float32) {
	s.counters[metricKey(key, nil)] += val
}

func (s *recordingSink) IncrCounterWithLabels(key []string, val float32, labels []gometrics.Label) {
	s.counters[metricKey(key, labels)] += val
}
func (s *recordingSink) AddSample(key []string, val float32) { s.samples++ }
func (s *recordingSink) AddSampleWithLabels(key []string, val float32, labels []gometrics.Label) {
	s.samples++
}

// gaugesWithPrefix returns how many recorded gauges start with prefix, e.g.
// "thornode.pool." for the whole per-pool family.
func (s *recordingSink) gaugesWithPrefix(prefix string) int {
	n := 0
	for k := range s.gauges {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

// installRecordingSink makes a fresh sink the process-global go-metrics sink and
// returns it. The cosmos telemetry wrappers only forward when
// telemetry.IsTelemetryEnabled(), hence enableBenchTelemetry first.
func installRecordingSink(tb testing.TB) *recordingSink {
	tb.Helper()
	enableBenchTelemetry(tb)
	sink := newRecordingSink()
	cfg := gometrics.DefaultConfig("")
	cfg.EnableHostname = false
	cfg.EnableRuntimeMetrics = false // else a goroutine writes runtime.* gauges into the sink
	if _, err := gometrics.NewGlobal(cfg, sink); err != nil {
		tb.Fatalf("install recording sink: %v", err)
	}
	return sink
}
