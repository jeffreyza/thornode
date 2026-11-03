package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/blang/semver"
	"github.com/rs/zerolog/log"
	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/notify"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Scan Security
////////////////////////////////////////////////////////////////////////////////////////

func ScanSecurity(block *thorscan.BlockResponse) {
	SecurityEvents(block)
	ErrataTransactions(block)
	LastRoundFailures(block)
}

////////////////////////////////////////////////////////////////////////////////////////
// Security Events
////////////////////////////////////////////////////////////////////////////////////////

func SecurityEvents(block *thorscan.BlockResponse) {
	// transaction security events
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] != types.SecurityEventType {
				continue
			}

			// notify security event
			title := "Security Event"
			data, err := json.MarshalIndent(event, "", "  ")
			if err != nil {
				log.Error().Err(err).Msg("unable to marshal security event")
			}
			lines := []string{"```" + string(data) + "```"}
			fields := util.NewOrderedMap()
			fields.Set("Hash", tx.Hash)
			fields.Set(
				"Links",
				fmt.Sprintf("[Explorer](%s/tx/%s)", config.Get().Links.Explorer, tx.BlockTx.Hash),
			)
			notify.Notify(config.Get().Notifications.Security, title, block.Header.Height, lines, notify.Warning, fields)
		}
	}

	// block security events
	for _, event := range append(block.EndBlockEvents, block.FinalizeBlockEvents...) {
		if event["type"] != types.SecurityEventType {
			continue
		}

		// notify security event
		title := "Security Event"
		data, err := json.MarshalIndent(event, "", "  ")
		if err != nil {
			log.Error().Err(err).Msg("unable to marshal security event")
		}
		lines := []string{"```" + string(data) + "```"}
		notify.Notify(config.Get().Notifications.Security, title, block.Header.Height, lines, notify.Warning, nil)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Errata Transactions
////////////////////////////////////////////////////////////////////////////////////////

func ErrataTransactions(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] != types.ErrataEventType {
				continue
			}

			// build the notification
			title := "Errata Tx"
			fields := util.NewOrderedMap()
			fields.Set(
				"Links",
				fmt.Sprintf("[Details](%s/thorchain/tx/details/%s)", config.Get().Links.Thornode, event["tx_id"]),
			)

			// notify errata transaction
			notify.Notify(config.Get().Notifications.Security, title, block.Header.Height, nil, notify.Warning, fields)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Last Round Failures
////////////////////////////////////////////////////////////////////////////////////////

type keysignFailureAlertClass uint8

const (
	keysignFailureAlertIgnored keysignFailureAlertClass = iota
	keysignFailureAlertOperational
	keysignFailureAlertTerminal
	keysignFailureAlertRejected
)

var tssV3MinimumVersion = semver.MustParse("3.19.0")

// classifyKeysignFailureRound separates mid-protocol liveness failures from
// failures where signature material may already have escaped. Under tss-lib v3,
// ECDSA round 7 broadcasts a commitment while round 9 broadcasts the signature
// share. Keep round 7 visible to operators, but reserve the security alert for
// the terminal boundary.
func classifyKeysignFailureRound(round string, usesTssV3 bool) keysignFailureAlertClass {
	switch round {
	case tssMessages.KEYSIGN7:
		if !usesTssV3 {
			return keysignFailureAlertTerminal
		}
		return keysignFailureAlertOperational
	case tssMessages.KEYSIGN9,
		tssMessages.EDDSAKEYSIGN3,
		tssMessages.FROSTKEYSIGNSHARE,
		tssMessages.FROSTKEYSIGNRESULT:
		return keysignFailureAlertTerminal
	default:
		return keysignFailureAlertIgnored
	}
}

func networkUsesTssV3AtHeight(height int64) (bool, error) {
	versionResponse := openapi.VersionResponse{}
	if err := util.ThornodeCachedRetryGet("thorchain/version", height, &versionResponse); err != nil {
		return false, fmt.Errorf("failed to get network version: %w", err)
	}
	return versionUsesTssV3(versionResponse.Current)
}

func versionUsesTssV3(rawVersion string) (bool, error) {
	version, err := semver.Parse(rawVersion)
	if err != nil {
		return false, fmt.Errorf("failed to parse network version %q: %w", rawVersion, err)
	}
	return version.GTE(tssV3MinimumVersion), nil
}

func keysignFailureAlertDetails(class keysignFailureAlertClass) (string, notify.Level) {
	switch class {
	case keysignFailureAlertOperational:
		return "TSS Keysign Failure (Round 7)", notify.Warning
	case keysignFailureAlertTerminal:
		return "Last Round Failure", notify.Warning
	case keysignFailureAlertRejected:
		return "Rejected TSS Failure Report", notify.Warning
	default:
		return "", notify.Info
	}
}

func keysignFailureDedupKey(class keysignFailureAlertClass, msg *thorchain.MsgTssKeysignFail) string {
	// The deterministic message ID includes the round, outbound height, memo,
	// vault, coins, and blamed nodes, but not the reporter. Equivalent reports
	// deduplicate. The class namespace prevents an operational report from ever
	// consuming the terminal alert's dedup entry, even if an invalid transaction
	// carries a forged ID.
	prefix := "operational"
	switch class {
	case keysignFailureAlertTerminal:
		prefix = "terminal"
	case keysignFailureAlertRejected:
		prefix = "rejected"
	}
	return prefix + "|" + strings.ToLower(msg.ID)
}

func keysignFailureWebhooks(class keysignFailureAlertClass) config.Webhooks {
	return keysignFailureWebhooksForConfig(class, config.Get())
}

func keysignFailureWebhooksForConfig(class keysignFailureAlertClass, cfg config.Config) config.Webhooks {
	if class == keysignFailureAlertTerminal {
		return cfg.Notifications.Security
	}

	info := cfg.Notifications.Info
	if cfg.Console || info.Slack != "" || info.Discord != "" || info.PagerDuty != "" {
		return info
	}
	if class == keysignFailureAlertRejected {
		// Rejected reports have not passed on-chain validation and must never be
		// allowed to reach the Security webhook. Keep the missing Info webhook
		// observable in logs instead.
		log.Warn().Msg("info webhooks are not configured; rejected TSS failure report will only be logged")
		return info
	}

	// Round 7 should remain visible even when a deployment only configured the
	// Security webhook. Its distinct title still identifies it as non-terminal.
	log.Warn().Msg("info webhooks are not configured; routing round-7 TSS warning to security")
	return cfg.Notifications.Security
}

type keysignFailureDependencies struct {
	load                  func(string, any) error
	store                 func(string, any) error
	notify                func(config.Webhooks, string, int64, []string, notify.Level, *util.OrderedMap)
	usdValueString        func(int64, common.Coin) string
	usesTssV3AtHeight     func(int64) (bool, error)
	webhooksForAlertClass func(keysignFailureAlertClass) config.Webhooks
}

func defaultKeysignFailureDependencies() keysignFailureDependencies {
	return keysignFailureDependencies{
		load:                  util.Load,
		store:                 util.Store,
		notify:                notify.Notify,
		usdValueString:        util.USDValueString,
		usesTssV3AtHeight:     networkUsesTssV3AtHeight,
		webhooksForAlertClass: keysignFailureWebhooks,
	}
}

func LastRoundFailures(block *thorscan.BlockResponse) {
	lastRoundFailures(block, defaultKeysignFailureDependencies())
}

func lastRoundFailures(block *thorscan.BlockResponse, deps keysignFailureDependencies) {
	seen := map[string]bool{}
	seenLoaded := false
	rejectedSeen := map[string]bool{}

	for _, tx := range block.Txs {
		if tx.Tx == nil { // transaction failed decode
			continue
		}
		code, hasCode := tx.Result.GetCodeOk()
		missingCodeLogged := false
		for _, msg := range tx.Tx.GetMsgs() {
			if msgKeysignFail, ok := msg.(*thorchain.MsgTssKeysignFail); ok {
				if !hasCode {
					if !missingCodeLogged {
						log.Warn().Str("tx_hash", tx.Hash).Msg("skipping keysign failure with missing transaction result code")
						missingCodeLogged = true
					}
					continue
				}
				// skip migrate transactions
				if reMemoMigration.MatchString(msgKeysignFail.Memo) {
					continue
				}
				if msgKeysignFail.ID == "" {
					log.Warn().Str("tx_hash", tx.Hash).Msg("skipping keysign failure with empty ID")
					continue
				}
				if len(msgKeysignFail.Coins) == 0 {
					log.Warn().Str("tx_hash", tx.Hash).Msg("skipping keysign failure with no coins")
					continue
				}

				round := types.CanonicalTssRound(msgKeysignFail.Blame.Round, "")
				alertClass := classifyKeysignFailureRound(round, true)
				if alertClass == keysignFailureAlertIgnored {
					continue
				}
				if *code != 0 {
					// Failed transactions have not passed on-chain validation. Keep
					// them visible for wide-blame and churned-signer diagnosis, but
					// isolate them from trusted Security alerts and their dedup keys.
					alertClass = keysignFailureAlertRejected
				} else if round == tssMessages.KEYSIGN7 {
					usesTssV3, err := deps.usesTssV3AtHeight(block.Header.Height)
					if err != nil {
						// Fail closed: before v3 round 7 was terminal, so an unknown
						// historical version must retain the Security classification.
						usesTssV3 = false
						log.Error().Err(err).Int64("height", block.Header.Height).Msg("unable to classify round-7 keysign failure by version")
					}
					alertClass = classifyKeysignFailureRound(round, usesTssV3)
				}

				failureKey := keysignFailureDedupKey(alertClass, msgKeysignFail)
				if alertClass == keysignFailureAlertRejected {
					// Rejected reports contain untrusted IDs. Deduplicate them only
					// within this block so forged IDs cannot grow persistent storage.
					if rejectedSeen[failureKey] {
						continue
					}
					rejectedSeen[failureKey] = true
				} else {
					if !seenLoaded {
						err := deps.load("keysignFailuresV2", &seen)
						if err != nil && !os.IsNotExist(err) {
							log.Error().Err(err).Msg("unable to load keysign failures")
						}
						seenLoaded = true
					}
					if seen[failureKey] {
						continue
					}
				}

				// Build the notification. Round 7 remains warning-level because a
				// mid-protocol failure can still indicate an unhealthy or malicious
				// signer even though no signature share has escaped yet.
				title, level := keysignFailureAlertDetails(alertClass)
				webhooks := deps.webhooksForAlertClass(alertClass)
				fields := util.NewOrderedMap()
				fields.Set("Amount", fmt.Sprintf(
					"%f %s (%s)",
					float64(msgKeysignFail.Coins[0].Amount.Uint64())/common.One,
					msgKeysignFail.Coins[0].Asset,
					deps.usdValueString(block.Header.Height, msgKeysignFail.Coins[0]),
				))
				fields.Set("Round", round)
				fields.Set("Memo", msgKeysignFail.Memo)
				if alertClass == keysignFailureAlertRejected {
					fields.Set("Result Code", *code)
				}
				fields.Set("Transaction", fmt.Sprintf("%s/cosmos/tx/v1beta1/txs/%s", config.Get().Links.Thornode, tx.Hash))
				deps.notify(webhooks, title, block.Header.Height, nil, level, fields)

				if alertClass == keysignFailureAlertRejected {
					continue
				}

				// Persist deduplication only for reports that passed validation.
				seen[failureKey] = true
				err := deps.store("keysignFailuresV2", seen)
				if err != nil {
					log.Error().Err(err).Msg("unable to save keysign failures")
				}
			}
		}
	}
}
