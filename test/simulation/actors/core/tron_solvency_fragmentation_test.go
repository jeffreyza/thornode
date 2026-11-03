package core

import (
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestPreviousTRONSolvencyReportHeight(t *testing.T) {
	tests := []struct {
		name            string
		lastChainHeight int64
		want            int64
		ok              bool
	}{
		{name: "not ready", lastChainHeight: 19, ok: false},
		{name: "exact bucket", lastChainHeight: 20, want: 10, ok: true},
		{name: "inside bucket", lastChainHeight: 29, want: 10, ok: true},
		{name: "next bucket", lastChainHeight: 30, want: 20, ok: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := previousTRONSolvencyReportHeight(test.lastChainHeight)
			if ok != test.ok || got != test.want {
				t.Fatalf("previous report height for %d: got (%d, %t), want (%d, %t)", test.lastChainHeight, got, ok, test.want, test.ok)
			}
			if ok && (got%10 != 0 || got >= test.lastChainHeight) {
				t.Fatalf("report height %d must be canonical and stale relative to %d", got, test.lastChainHeight)
			}
		})
	}
}

func TestTRONSolvencyFragmentationPayloadsIncludeNativeAndTRC20(t *testing.T) {
	majority, minority, err := tronSolvencyFragmentationPayloads(123)
	if err != nil {
		t.Fatalf("build payloads: %v", err)
	}
	usdt, err := common.NewAsset(tronUSDTAssetString)
	if err != nil {
		t.Fatalf("parse USDT asset: %v", err)
	}

	majorityTRX := majority.GetCoin(common.TRXAsset)
	minorityTRX := minority.GetCoin(common.TRXAsset)
	if majorityTRX.IsEmpty() || minorityTRX.IsEmpty() || !majorityTRX.Amount.Equal(minorityTRX.Amount) {
		t.Fatalf("payloads must contain the same native TRX balance: majority=%s minority=%s", majorityTRX, minorityTRX)
	}
	majorityUSDT := majority.GetCoin(usdt)
	minorityUSDT := minority.GetCoin(usdt)
	if majorityUSDT.IsEmpty() || minorityUSDT.IsEmpty() {
		t.Fatalf("payloads must contain supported TRC-20 USDT: majority=%s minority=%s", majorityUSDT, minorityUSDT)
	}
	if !minorityUSDT.Amount.Add(cosmos.OneUint()).Equal(majorityUSDT.Amount) {
		t.Fatalf("minority USDT balance must differ by one unit: majority=%s minority=%s", majorityUSDT.Amount, minorityUSDT.Amount)
	}
}

func TestValidateSlashPointDeltas(t *testing.T) {
	before := map[string]int64{"dog": 10, "cat": 20}

	if err := validateSlashPointDeltas(before, map[string]int64{"dog": 10, "cat": 20}, nil); err != nil {
		t.Fatalf("unchanged slash points rejected: %v", err)
	}
	if err := validateSlashPointDeltas(before, map[string]int64{"dog": 11, "cat": 20}, map[string]int64{"dog": 1}); err != nil {
		t.Fatalf("expected duplicate delta rejected: %v", err)
	}
	if err := validateSlashPointDeltas(before, map[string]int64{"dog": 12, "cat": 20}, map[string]int64{"dog": 1}); err == nil || !strings.Contains(err.Error(), "got 2, want 1") {
		t.Fatalf("unexpected delta should fail with diagnostic, got %v", err)
	}
	if err := validateSlashPointDeltas(before, map[string]int64{"dog": 10}, nil); err == nil || !strings.Contains(err.Error(), "cat") {
		t.Fatalf("missing node should fail with diagnostic, got %v", err)
	}
}

func TestRetryTRONSolvencySlashPoints(t *testing.T) {
	log := zerolog.Nop()
	before := map[string]int64{"dog": 10, "cat": 20}
	measurements := func(afters ...map[string]int64) (func(int) (map[string]int64, map[string]int64, error), *int) {
		calls := 0
		return func(attempt int) (map[string]int64, map[string]int64, error) {
			calls++
			if attempt != calls {
				t.Fatalf("attempt %d reported on call %d", attempt, calls)
			}
			return before, afters[min(calls, len(afters))-1], nil
		}, &calls
	}

	// A Bifrost observation charging "cat" during the first measurement is
	// not attributable to the injected TRON messages.
	measure, calls := measurements(map[string]int64{"dog": 10, "cat": 21}, map[string]int64{"dog": 10, "cat": 20})
	if err := retryTRONSolvencySlashPoints(&log, "observations", nil, measure); err != nil || *calls != 2 {
		t.Fatalf("unrelated observation update must be retried: err=%v calls=%d", err, *calls)
	}

	// A late-observer refund to the duplicate signer is retried too.
	measure, calls = measurements(map[string]int64{"dog": 9, "cat": 20}, map[string]int64{"dog": 11, "cat": 20})
	if err := retryTRONSolvencySlashPoints(&log, "duplicate", map[string]int64{"dog": 1}, measure); err != nil || *calls != 2 {
		t.Fatalf("unrelated duplicate-signer update must be retried: err=%v calls=%d", err, *calls)
	}

	// Ordinary observations that slash the signer on every attempt are a regression.
	measure, calls = measurements(map[string]int64{"dog": 11, "cat": 20})
	err := retryTRONSolvencySlashPoints(&log, "observations", nil, measure)
	if err == nil || !strings.Contains(err.Error(), "all 5 attempts") || !strings.Contains(err.Error(), "got 1, want 0") || *calls != tronSolvencySlashPointAttempts {
		t.Fatalf("persistent slash-point change must fail after every attempt: err=%v calls=%d", err, *calls)
	}

	// Broadcast failures are not retried: the message may already be delivered.
	calls = new(int)
	broadcastErr := errors.New("broadcast failed")
	err = retryTRONSolvencySlashPoints(&log, "observations", nil, func(int) (map[string]int64, map[string]int64, error) {
		*calls++
		return nil, nil, broadcastErr
	})
	if !errors.Is(err, broadcastErr) || *calls != 1 {
		t.Fatalf("measurement error must be returned without retrying: err=%v calls=%d", err, *calls)
	}
}
