package ethereum

import (
	"errors"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestSolvencyHookPreservesIntervalAndFlexibility(t *testing.T) {
	client := &Client{cfg: config.BifrostChainConfiguration{SolvencyBlocks: 20}}
	var reported []int64
	scanner := &ETHScanner{
		cfg: config.BifrostBlockScannerConfiguration{ObservationFlexibilityBlocks: 2},
		solvencyReporter: func(height int64) error {
			if client.ShouldReportSolvency(height) {
				reported = append(reported, height)
			}
			return nil
		},
	}
	for _, pair := range [][2]int64{{20, 23}, {19, 20}, {20, 22}} {
		if err := scanner.ReportSolvency(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if len(reported) != 1 || reported[0] != 20 {
		t.Fatalf("unexpected scanned solvency heights: %v", reported)
	}
	failure := errors.New("solvency RPC unavailable")
	scanner.solvencyReporter = func(int64) error { return failure }
	if err := scanner.ReportSolvency(20, 20); !errors.Is(err, failure) {
		t.Fatalf("lost reporting error: %v", err)
	}
	scanner.solvencyReporter = nil
	if err := scanner.ReportSolvency(20, 20); err != nil {
		t.Fatal(err)
	}
}
