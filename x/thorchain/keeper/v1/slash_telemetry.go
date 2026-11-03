package keeperv1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/hashicorp/go-metrics"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

var (
	slashTelemetryEnc  *json.Encoder
	slashTelemetryOnce sync.Once
	slashTelemetryMu   sync.Mutex
)

// initSlashTelemetry is lazy so importing the keeper never loads process
// configuration. Its callers only reach this function when telemetry is enabled.
func initSlashTelemetry() {
	slashTelemetryOnce.Do(func() {
		path := os.ExpandEnv("${HOME}/.thornode/slash_telemetry.json")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			panic(err)
		}
		slashTelemetryEnc = json.NewEncoder(file)
	})
}

func getSlashCaller(fn string) string {
	stackBuf := make([]byte, 4096)
	written := runtime.Stack(stackBuf, false)
	return getSlashCallerFromStack(stackBuf[:written], fn)
}

func getSlashCallerFromStack(stack []byte, fn string) string {
	lines := strings.Split(string(stack), "\n")
	if caller := findSlashCaller(lines, fn); caller != "" {
		return caller
	}

	// Most call sites route slash-point accounting through SlasherImpl's
	// IncSlashPoints/DecSlashPoints wrappers. Consensus handlers which must
	// propagate keeper persistence errors call the keeper directly instead.
	// Retain caller attribution for those error-aware paths by falling back to
	// the corresponding concrete keeper frame.
	keeperFn := strings.Replace(fn, "SlashPoints", "NodeAccountSlashPoints", 1)
	if keeperFn == fn {
		return ""
	}
	return findSlashCaller(lines, keeperFn)
}

func findSlashCaller(lines []string, fn string) string {
	for i, line := range lines {
		if strings.Contains(line, fn) && len(lines) > i+3 {
			return strings.Fields(strings.TrimSpace(lines[i+3]))[0]
		}
	}
	return ""
}

func slashTelemetry(ctx cosmos.Context, pts int64, addr cosmos.AccAddress, slashFnName string) {
	initSlashTelemetry()

	rawHash := sha256.Sum256(ctx.TxBytes())
	hash := hex.EncodeToString(rawHash[:])
	slash := map[string]any{
		"height":  ctx.BlockHeight(),
		"txid":    hash,
		"points":  pts,
		"address": addr.String(),
		"caller":  getSlashCaller(slashFnName),
	}
	metricLabels, _ := ctx.Context().Value(constants.CtxMetricLabels).([]metrics.Label)
	for _, label := range metricLabels {
		slash[label.Name] = label.Value
	}
	if ctx.Context().Value(constants.CtxObservedTx) != nil {
		slash["observed_txid"], _ = ctx.Context().Value(constants.CtxObservedTx).(string)
	}
	slashTelemetryMu.Lock()
	defer slashTelemetryMu.Unlock()
	_ = slashTelemetryEnc.Encode(slash)
}
