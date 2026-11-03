package main

import (
	"errors"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/notify"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	protov2 "google.golang.org/protobuf/proto"
)

type keysignFailureTestTx struct {
	msgs []sdk.Msg
}

func (tx keysignFailureTestTx) GetMsgs() []sdk.Msg {
	return tx.msgs
}

func (keysignFailureTestTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

type capturedKeysignFailureNotification struct {
	webhooks config.Webhooks
	title    string
	level    notify.Level
	fields   *util.OrderedMap
}

func newKeysignFailureTestDependencies() (keysignFailureDependencies, *[]capturedKeysignFailureNotification) {
	seen := map[string]bool{}
	captured := []capturedKeysignFailureNotification{}

	deps := keysignFailureDependencies{
		load: func(_ string, target any) error {
			dst, ok := target.(*map[string]bool)
			if !ok {
				return errors.New("unexpected load target")
			}
			for key, value := range seen {
				(*dst)[key] = value
			}
			return nil
		},
		store: func(_ string, value any) error {
			src, ok := value.(map[string]bool)
			if !ok {
				return errors.New("unexpected store value")
			}
			seen = make(map[string]bool, len(src))
			for key, isSeen := range src {
				seen[key] = isSeen
			}
			return nil
		},
		notify: func(webhooks config.Webhooks, title string, _ int64, _ []string, level notify.Level, fields *util.OrderedMap) {
			captured = append(captured, capturedKeysignFailureNotification{
				webhooks: webhooks,
				title:    title,
				level:    level,
				fields:   fields,
			})
		},
		usdValueString: func(_ int64, _ common.Coin) string {
			return "$0.00"
		},
		usesTssV3AtHeight: func(_ int64) (bool, error) {
			return true, nil
		},
		webhooksForAlertClass: func(class keysignFailureAlertClass) config.Webhooks {
			if class == keysignFailureAlertTerminal {
				return config.Webhooks{Category: "Security"}
			}
			return config.Webhooks{Category: "Info"}
		},
	}

	return deps, &captured
}

func newKeysignFailureTestBlock(code *int64, msgs ...sdk.Msg) *thorscan.BlockResponse {
	return &thorscan.BlockResponse{
		BlockResponse: openapi.BlockResponse{
			Header: openapi.BlockResponseHeader{Height: 123},
		},
		Txs: []thorscan.BlockTx{
			{
				BlockTx: openapi.BlockTx{
					Hash:   "TXHASH",
					Result: openapi.BlockTxResult{Code: code},
				},
				Tx: keysignFailureTestTx{msgs: msgs},
			},
		},
	}
}

func newKeysignFailureTestMsg(id, round string, coins common.Coins) *thorchain.MsgTssKeysignFail {
	return &thorchain.MsgTssKeysignFail{
		ID:    id,
		Blame: thorchain.Blame{Round: round},
		Memo:  "OUT:ABC123",
		Coins: coins,
	}
}

func keysignFailureTestCoins() common.Coins {
	return common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}
}

func TestLastRoundFailuresFailedRound7CannotSuppressTerminalAlert(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	failedCode := int64(1)
	successCode := int64(0)

	failedRound7 := newKeysignFailureTestMsg("shared-id", tssMessages.KEYSIGN7, keysignFailureTestCoins())
	lastRoundFailures(newKeysignFailureTestBlock(&failedCode, failedRound7), deps)
	if len(*captured) != 1 {
		t.Fatalf("failed transaction emitted %d notifications, want 1", len(*captured))
	}
	if got := (*captured)[0]; got.title != "Rejected TSS Failure Report" || got.webhooks.Category != "Info" {
		t.Fatalf("rejected notification = title %q category %q", got.title, got.webhooks.Category)
	}

	round9 := newKeysignFailureTestMsg("shared-id", tssMessages.KEYSIGN9, keysignFailureTestCoins())
	lastRoundFailures(newKeysignFailureTestBlock(&successCode, round9), deps)
	if len(*captured) != 2 {
		t.Fatalf("terminal transaction emitted %d total notifications, want 2", len(*captured))
	}
	if got := (*captured)[1]; got.title != "Last Round Failure" || got.webhooks.Category != "Security" {
		t.Fatalf("terminal notification = title %q category %q", got.title, got.webhooks.Category)
	}
}

func TestLastRoundFailuresRejectedReportsAreInfoAndBlockDeduplicatedWithoutPersistence(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	loadCalls := 0
	storeCalls := 0
	deps.load = func(_ string, _ any) error {
		loadCalls++
		return nil
	}
	deps.store = func(_ string, _ any) error {
		storeCalls++
		return nil
	}
	failedCode := int64(7)
	round9 := newKeysignFailureTestMsg("rejected-id", tssMessages.KEYSIGN9, keysignFailureTestCoins())

	block := newKeysignFailureTestBlock(&failedCode, round9)
	duplicateTx := block.Txs[0]
	duplicateTx.Hash = "SECOND-TX-HASH"
	block.Txs = append(block.Txs, duplicateTx)
	lastRoundFailures(block, deps)

	if len(*captured) != 1 {
		t.Fatalf("same-block rejected reports emitted %d notifications, want 1", len(*captured))
	}
	got := (*captured)[0]
	if got.title != "Rejected TSS Failure Report" || got.webhooks.Category != "Info" {
		t.Fatalf("rejected notification = title %q category %q", got.title, got.webhooks.Category)
	}
	if got.level != notify.Warning {
		t.Fatalf("rejected notification level = %d, want %d", got.level, notify.Warning)
	}
	resultCode, ok := got.fields.Get("Result Code")
	if !ok || resultCode != failedCode {
		t.Fatalf("rejected notification result code = %v, want %d", resultCode, failedCode)
	}
	if loadCalls != 0 || storeCalls != 0 {
		t.Fatalf("rejected reports invoked storage: load=%d store=%d, want 0/0", loadCalls, storeCalls)
	}

	lastRoundFailures(newKeysignFailureTestBlock(&failedCode, round9), deps)
	if len(*captured) != 2 {
		t.Fatalf("next-block rejected report emitted %d total notifications, want 2", len(*captured))
	}
	if loadCalls != 0 || storeCalls != 0 {
		t.Fatalf("next-block rejected report invoked storage: load=%d store=%d, want 0/0", loadCalls, storeCalls)
	}
}

func TestKeysignFailureWebhooksNeverRoutesRejectedReportsToSecurity(t *testing.T) {
	cfg := config.Config{}
	cfg.Notifications.Info = config.Webhooks{Category: "Info"}
	cfg.Notifications.Security = config.Webhooks{Category: "Security", Slack: "security-webhook"}

	got := keysignFailureWebhooksForConfig(keysignFailureAlertRejected, cfg)
	if got.Category != "Info" || got.Slack != "" {
		t.Fatalf("rejected report webhooks = category %q slack %q, want unconfigured Info webhooks", got.Category, got.Slack)
	}

	got = keysignFailureWebhooksForConfig(keysignFailureAlertOperational, cfg)
	if got.Category != "Security" || got.Slack != "security-webhook" {
		t.Fatalf("operational fallback webhooks = category %q slack %q, want Security webhooks", got.Category, got.Slack)
	}
}

func TestLastRoundFailuresScopesDedupByAlertClass(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	successCode := int64(0)

	round7 := newKeysignFailureTestMsg("shared-id", tssMessages.KEYSIGN7, keysignFailureTestCoins())
	lastRoundFailures(newKeysignFailureTestBlock(&successCode, round7), deps)
	round9 := newKeysignFailureTestMsg("shared-id", tssMessages.KEYSIGN9, keysignFailureTestCoins())
	lastRoundFailures(newKeysignFailureTestBlock(&successCode, round9), deps)

	if len(*captured) != 2 {
		t.Fatalf("cross-class failures emitted %d notifications, want 2", len(*captured))
	}
	if (*captured)[0].webhooks.Category != "Info" || (*captured)[1].webhooks.Category != "Security" {
		t.Fatalf("notification categories = %q, %q", (*captured)[0].webhooks.Category, (*captured)[1].webhooks.Category)
	}
}

func TestLastRoundFailuresDeduplicatesEquivalentTerminalReports(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	successCode := int64(0)
	round9 := newKeysignFailureTestMsg("terminal-id", tssMessages.KEYSIGN9, keysignFailureTestCoins())

	lastRoundFailures(newKeysignFailureTestBlock(&successCode, round9), deps)
	lastRoundFailures(newKeysignFailureTestBlock(&successCode, round9), deps)

	if len(*captured) != 1 {
		t.Fatalf("equivalent terminal reports emitted %d notifications, want 1", len(*captured))
	}
}

func TestLastRoundFailuresClassifiesRound7ByHistoricalVersion(t *testing.T) {
	tests := []struct {
		name         string
		usesTssV3    bool
		versionErr   error
		wantTitle    string
		wantCategory string
	}{
		{name: "v3 round 7 is operational", usesTssV3: true, wantTitle: "TSS Keysign Failure (Round 7)", wantCategory: "Info"},
		{name: "pre-v3 round 7 is terminal", usesTssV3: false, wantTitle: "Last Round Failure", wantCategory: "Security"},
		{name: "unknown version fails closed", versionErr: errors.New("version unavailable"), wantTitle: "Last Round Failure", wantCategory: "Security"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, captured := newKeysignFailureTestDependencies()
			deps.usesTssV3AtHeight = func(_ int64) (bool, error) {
				return tt.usesTssV3, tt.versionErr
			}
			successCode := int64(0)
			round7 := newKeysignFailureTestMsg("round-7-id", tssMessages.KEYSIGN7, keysignFailureTestCoins())

			lastRoundFailures(newKeysignFailureTestBlock(&successCode, round7), deps)

			if len(*captured) != 1 {
				t.Fatalf("round-7 failure emitted %d notifications, want 1", len(*captured))
			}
			got := (*captured)[0]
			if got.title != tt.wantTitle || got.webhooks.Category != tt.wantCategory {
				t.Fatalf("notification = title %q category %q, want title %q category %q", got.title, got.webhooks.Category, tt.wantTitle, tt.wantCategory)
			}
			if got.level != notify.Warning {
				t.Fatalf("notification level = %d, want %d", got.level, notify.Warning)
			}
		})
	}
}

func TestVersionUsesTssV3(t *testing.T) {
	tests := []struct {
		version string
		want    bool
		wantErr bool
	}{
		{version: "3.18.2", want: false},
		{version: "3.19.0", want: true},
		{version: "3.19.3", want: true},
		{version: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, err := versionUsesTssV3(tt.version)
			if (err != nil) != tt.wantErr {
				t.Fatalf("versionUsesTssV3(%q) error = %v, wantErr %v", tt.version, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("versionUsesTssV3(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestLastRoundFailuresCanonicalizesHistoricalRoundNames(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	successCode := int64(0)
	round7 := newKeysignFailureTestMsg(
		"round-7-id",
		"thorchain.tsslib.ecdsa.signing.SignRound7Message",
		keysignFailureTestCoins(),
	)

	lastRoundFailures(newKeysignFailureTestBlock(&successCode, round7), deps)

	if len(*captured) != 1 {
		t.Fatalf("prefixed round emitted %d notifications, want 1", len(*captured))
	}
	round, ok := (*captured)[0].fields.Get("Round")
	if !ok || round != tssMessages.KEYSIGN7 {
		t.Fatalf("notification round = %v, want %q", round, tssMessages.KEYSIGN7)
	}
}

func TestLastRoundFailuresSkipsMalformedSuccessfulMessages(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	successCode := int64(0)

	emptyCoins := newKeysignFailureTestMsg("empty-coins", tssMessages.KEYSIGN9, nil)
	emptyID := newKeysignFailureTestMsg("", tssMessages.KEYSIGN9, keysignFailureTestCoins())
	lastRoundFailures(newKeysignFailureTestBlock(&successCode, emptyCoins, emptyID), deps)

	if len(*captured) != 0 {
		t.Fatalf("malformed messages emitted %d notifications, want 0", len(*captured))
	}
}

func TestLastRoundFailuresSkipsTransactionsWithoutResultCode(t *testing.T) {
	deps, captured := newKeysignFailureTestDependencies()
	round9 := newKeysignFailureTestMsg("terminal-id", tssMessages.KEYSIGN9, keysignFailureTestCoins())

	lastRoundFailures(newKeysignFailureTestBlock(nil, round9), deps)

	if len(*captured) != 0 {
		t.Fatalf("transaction without result code emitted %d notifications, want 0", len(*captured))
	}
}
