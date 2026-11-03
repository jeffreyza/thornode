package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
)

// testKeysignFailureSeq distinguishes consensus ids across generated test
// events — the scanner dedupes records by id within the window.
var testKeysignFailureSeq int

func keysignFailureEvent(vault, round string, blame ...string) map[string]string {
	testKeysignFailureSeq++
	return map[string]string{
		"type":   "tss_keysign_failure",
		"pubkey": vault,
		"round":  round,
		"reason": "tss timeout",
		"height": "42",
		"id":     fmt.Sprintf("%064d", testKeysignFailureSeq),
		"blame":  strings.Join(blame, ", "),
	}
}

func newKeysignFailureBlock(height int64, events ...map[string]string) *thorscan.BlockResponse {
	block := &thorscan.BlockResponse{
		BlockResponse: openapi.BlockResponse{
			Header: openapi.BlockResponseHeader{Height: height},
		},
	}
	if len(events) > 0 {
		block.Txs = []thorscan.BlockTx{
			{
				BlockTx: openapi.BlockTx{
					Result: openapi.BlockTxResult{Events: events},
				},
			},
		}
	}
	return block
}

func TestTssKeysignFailure(t *testing.T) {
	// setup isolated storage
	tmpDir, err := os.MkdirTemp("", "tss_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Thresholds.TssKeysignFailure.MinFailures = 0
	cfg.Console = true
	config.Set(cfg)

	// capture logs — notify logs "sending notifications" with the title
	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	// Scenario 1: basic failure below threshold — recorded with round and
	// blame granularity, no alert.
	// Seed 3 snapshots with 2 failures each: mean=2, stddev=0, threshold=2.
	history := TssKeysignFailureHistory{
		VaultPastCounts:   map[string][]int{vault: {2, 2, 2}},
		NetworkPastCounts: []int{2, 2, 2},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	TssKeysignFailure(newKeysignFailureBlock(100, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))

	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 1)
	require.Equal(t, tssMessages.KEYSIGN5, history.VaultFailures[vault][0].Round)
	require.Equal(t, []string{blamed}, history.VaultFailures[vault][0].Blame)
	require.NotContains(t, logBuf.String(), "sending notifications")
	logBuf.Reset()

	// Scenario 2: threshold crossing — 3 failures > threshold of 2 alerts once.
	TssKeysignFailure(newKeysignFailureBlock(101, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))
	require.NotContains(t, logBuf.String(), "sending notifications")

	TssKeysignFailure(newKeysignFailureBlock(102, keysignFailureEvent(vault, tssMessages.KEYSIGN1aUnicast, blamed)))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 3)
	require.Contains(t, logBuf.String(), "sending notifications", "expected alert when threshold crossed")
	require.Contains(t, logBuf.String(), "High TSS Keysign Failure Rate")
	logBuf.Reset()

	// Scenario 3: moving window pruning — window is 10 blocks, all failures
	// fall out by height 115.
	TssKeysignFailure(newKeysignFailureBlock(115))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Empty(t, history.VaultFailures[vault])
	logBuf.Reset()

	// Scenario 4: network fallback for a vault with no snapshot history —
	// network baseline of 1 failure/window is used as the threshold.
	history = TssKeysignFailureHistory{
		VaultPastCounts:   make(map[string][]int),
		NetworkPastCounts: []int{1, 1, 1},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	newVault := "tthorpub1newvaultnewvaultnewvaultnewvaultnewCCCC"
	TssKeysignFailure(newKeysignFailureBlock(200, keysignFailureEvent(newVault, tssMessages.EDDSAKEYSIGN1, blamed)))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[newVault], 1)
	logBuf.Reset()

	TssKeysignFailure(newKeysignFailureBlock(201, keysignFailureEvent(newVault, tssMessages.EDDSAKEYSIGN1, blamed)))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[newVault], 2)
	require.Contains(t, logBuf.String(), "sending notifications", "expected alert for new vault via network threshold")
	require.Empty(t, history.VaultPastCounts[newVault], "new vault should have no snapshots yet")
	logBuf.Reset()
}

// TestTssKeysignFailureNoImmediateTerminalAlert pins the division of labor
// with security.go: per-report terminal-round alerting is owned by
// LastRoundFailures, so the rate scanner records terminal rounds without
// alerting on its own until a statistical threshold is crossed.
func TestTssKeysignFailureNoImmediateTerminalAlert(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tss_test_terminal")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Thresholds.TssKeysignFailure.MinFailures = 0
	cfg.Console = true
	config.Set(cfg)

	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	for _, round := range []string{
		tssMessages.KEYSIGN9,
		tssMessages.EDDSAKEYSIGN3,
		tssMessages.FROSTKEYSIGNSHARE,
		tssMessages.FROSTKEYSIGNRESULT,
	} {
		logBuf.Reset()
		TssKeysignFailure(newKeysignFailureBlock(300, keysignFailureEvent(vault, round, blamed)))
		require.NotContains(t, logBuf.String(), "sending notifications", "round %s must not alert without a rate threshold", round)
	}

	history := TssKeysignFailureHistory{}
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 4, "terminal rounds must still be recorded for rate statistics")
}

// TestTssKeysignFailureReplayIsIdempotent covers block re-scans after a crash:
// history is persisted before the scan height cursor, so the same block can be
// processed twice. The consensus id dedup must keep one record and one alert.
func TestTssKeysignFailureReplayIsIdempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tss_test_replay")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Thresholds.TssKeysignFailure.MinFailures = 0
	cfg.Console = true
	config.Set(cfg)

	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	// zero-failure vault baseline: any single failure crosses the vault
	// threshold; network history is left below MinSnapshots so only the
	// vault alert can fire
	history := TssKeysignFailureHistory{
		VaultPastCounts: map[string][]int{vault: {0, 0, 0}},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	// the same block object is processed twice, as after a crash-replay
	block := newKeysignFailureBlock(400, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed))
	TssKeysignFailure(block)
	TssKeysignFailure(block)

	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 1, "replayed failure must be recorded once")
	require.Equal(t, 1, strings.Count(logBuf.String(), "sending notifications"), "replayed failure must alert once")
}

// TestTssKeysignFailureMinFailures covers the absolute floor: a near-zero
// baseline must not alert on a couple of failures, only once the window holds
// at least MinFailures, and then only once.
func TestTssKeysignFailureMinFailures(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tss_test_min_failures")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 100
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Thresholds.TssKeysignFailure.MinFailures = 3
	cfg.Console = true
	config.Set(cfg)

	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	// zero baseline: the statistical threshold alone would alert on the first
	// failure; network history is left below MinSnapshots
	history := TssKeysignFailureHistory{
		VaultPastCounts: map[string][]int{vault: {0, 0, 0}},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	TssKeysignFailure(newKeysignFailureBlock(500, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))
	TssKeysignFailure(newKeysignFailureBlock(501, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))
	require.NotContains(t, logBuf.String(), "sending notifications", "failures below the floor must not alert")

	TssKeysignFailure(newKeysignFailureBlock(502, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))
	require.Equal(t, 1, strings.Count(logBuf.String(), "sending notifications"), "reaching the floor must alert")

	TssKeysignFailure(newKeysignFailureBlock(503, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))
	require.Equal(t, 1, strings.Count(logBuf.String(), "sending notifications"), "alert is edge triggered")
}

func TestChurnOutLabels(t *testing.T) {
	node := func(suffix string, bond string, slashPoints, statusSince, missingBlocks int64) openapi.Node {
		return openapi.Node{
			NodeAddress:   "thor1" + suffix,
			Status:        "Active",
			TotalBond:     bond,
			SlashPoints:   slashPoints,
			StatusSince:   statusSince,
			MissingBlocks: missingBlocks,
		}
	}
	params := churnOutParams{
		MinSlashPointsForBadValidator: 100,
		BadValidatorRedline:           20,
		DesiredValidatorSet:           6,
		MissingBlockChurnOut:          10,
		MaxMissingBlockChurnOut:       3,
	}

	// mirrors the mainnet churn at height 28203871
	worst := node("seqc", "700", 11472, 300, 0)
	oldest := node("plcw", "1151", 2954, 100, 0)
	missing := node("fwle", "1156", 6087, 400, 341)
	lowest := node("l3k7", "300", 5604, 400, 0)
	other := node("wqml", "956", 3609, 200, 0)
	active := []openapi.Node{lowest, missing, other, oldest, worst}

	t.Run("low bond is not marked below the desired validator set", func(t *testing.T) {
		labels, unknown := churnOutLabels(active, []openapi.Node{oldest, worst, missing}, params)
		require.Equal(t, []string{"`fwle` (missing)", "`plcw` (oldest)", "`seqc` (worst)"}, labels)
		require.Empty(t, unknown)
	})

	t.Run("low bond is marked at the desired validator set", func(t *testing.T) {
		full := append([]openapi.Node{node("full", "2000", 0, 500, 0)}, active...)
		labels, unknown := churnOutLabels(full, []openapi.Node{lowest, oldest, worst}, params)
		require.Equal(t, []string{"`l3k7` (lowest bond)", "`plcw` (oldest)", "`seqc` (worst)"}, labels)
		require.Empty(t, unknown)
	})

	t.Run("removed node is not labeled when another node holds the mark", func(t *testing.T) {
		labels, unknown := churnOutLabels(active, []openapi.Node{other}, params)
		require.Empty(t, labels)
		require.Equal(t, []openapi.Node{other}, unknown)
	})

	t.Run("removed node is preferred on ties", func(t *testing.T) {
		tied := node("aaaa", "1151", 2954, 100, 0)
		labels, _ := churnOutLabels(append([]openapi.Node{tied}, active...), []openapi.Node{oldest}, params)
		require.Equal(t, []string{"`plcw` (oldest)"}, labels)
	})

	t.Run("all nodes over the redline are marked", func(t *testing.T) {
		redlined := node("badd", "900", 2000000, 350, 0)
		labels, _ := churnOutLabels(append([]openapi.Node{redlined}, active...), []openapi.Node{redlined}, params)
		require.Equal(t, []string{"`badd` (worst)"}, labels)
	})

	t.Run("low version is marked before age", func(t *testing.T) {
		lowVersion := node("lowv", "1000", 2000, 50, 0)
		lowVersion.Version = "3.19.0"
		nodes := append([]openapi.Node{lowVersion}, active...)
		for i := range nodes {
			if nodes[i].Version == "" {
				nodes[i].Version = "3.20.3"
			}
		}
		versionParams := params
		versionParams.MinJoinVersion = "3.20.3"
		versionParams.MaxNodeToChurnOutForLowVersion = 3
		labels, unknown := churnOutLabels(nodes, []openapi.Node{lowVersion, oldest}, versionParams)
		require.Equal(t, []string{"`lowv` (version)", "`plcw` (oldest)"}, labels)
		require.Empty(t, unknown)

		// only the first low version nodes in address order are marked, so the
		// remaining low version node is the one marked for age
		other := node("aaaa", "1000", 2000, 60, 0)
		other.Version = "3.19.0"
		versionParams.MaxNodeToChurnOutForLowVersion = 1
		labels, unknown = churnOutLabels(append([]openapi.Node{other}, nodes...), []openapi.Node{other, lowVersion, oldest}, versionParams)
		require.Equal(t, []string{"`aaaa` (version)", "`lowv` (oldest)"}, labels)
		require.Len(t, unknown, 1)
		require.Equal(t, oldest.NodeAddress, unknown[0].NodeAddress)
	})

	t.Run("requested to leave", func(t *testing.T) {
		leaving := node("leav", "100", 0, 50, 0)
		leaving.RequestedToLeave = true
		labels, _ := churnOutLabels(append([]openapi.Node{leaving}, active...), []openapi.Node{leaving, oldest}, params)
		require.Equal(t, []string{"`leav` (leave)", "`plcw` (oldest)"}, labels)
	})
}
