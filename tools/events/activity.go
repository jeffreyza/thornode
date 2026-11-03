package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	ctypes "github.com/cosmos/cosmos-sdk/types"
	bank "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/notify"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Scan Activity
////////////////////////////////////////////////////////////////////////////////////////

func ScanActivity(block *thorscan.BlockResponse) {
	LargeUnconfirmedInbounds(block)
	LargeStreamingSwaps(block)
	ScheduledOutbounds(block)
	LargeTransfers(block)
	InactiveVaultInbounds(block)
	NewNode(block)
	Bond(block)
	FailedTransactions(block)
	LargeHighSlipSwaps(block)
	FailedRefunds(block)

	// THORNameRegistrations(block) (disable per community request)
}

////////////////////////////////////////////////////////////////////////////////////////
// Large Unconfirmed Inbounds
////////////////////////////////////////////////////////////////////////////////////////

func LargeUnconfirmedInbounds(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		// skip failed transactions
		if *tx.Result.Code != 0 {
			continue
		}

		// skip failed decode transactions
		if tx.Tx == nil {
			continue
		}

		for _, msg := range tx.Tx.GetMsgs() {
			// skip anything other than observed transactions
			msgObservedTx, ok := msg.(*types.MsgObservedTxQuorum)
			if !ok {
				continue
			}
			if !msgObservedTx.QuoTx.Inbound {
				continue
			}

			tx := msgObservedTx.QuoTx.ObsTx

			// skip migrate inbounds
			if reMemoMigration.MatchString(tx.Tx.Memo) {
				continue
			}

			// skip consolidate inbounds and trade/secured asset deposits
			if tx.Tx.Memo != "" { // skip memoless
				memoParts := strings.Split(tx.Tx.Memo, ":")
				memoType, err := memo.StringToTxType(memoParts[0])
				if err != nil {
					log.Error().Err(err).
						Int64("height", block.Header.Height).
						Str("memo", tx.Tx.Memo).
						Msg("failed to parse memo type")
				}
				switch memoType {
				case memo.TxConsolidate, memo.TxTradeAccountDeposit, memo.TxSecuredAssetDeposit:
					continue
				}
			}

			// since this is checked often, only update cached price every 10 blocks
			priceHeight := block.Header.Height / 10 * 10

			// skip simulation test inbounds (mocknet only)
			if isTestMemo(tx.Tx.Memo) {
				continue
			}

			// skip if below usd threshold
			usdValue := util.USDValue(priceHeight, tx.Tx.Coins[0])
			if uint64(usdValue) < config.Get().Thresholds.USDValue {
				continue
			}

			// check threshold with clout
			fromClout := util.Clout(priceHeight, tx.Tx.FromAddress.String())
			fromCloutUSD := util.USDValue(priceHeight, fromClout)
			if uint64(usdValue) < config.Get().Thresholds.USDValue+uint64(fromCloutUSD) {
				continue
			}

			// skip if under 2 minutes until confirmation
			confirmBlocks := tx.FinaliseHeight - tx.BlockHeight
			blockMs := tx.Tx.Chain.GetGasAsset().Chain.ApproximateBlockMilliseconds()
			confirmDuration := time.Duration(confirmBlocks*blockMs) * time.Millisecond
			if confirmDuration < time.Minute*2 {
				continue
			}

			// skip if previously seen
			seen := false
			seenKey := fmt.Sprintf("seen-large-unconfirmed-inbound/%s", tx.Tx.ID.String())
			err := util.Load(seenKey, &seen)
			if err != nil {
				log.Debug().Err(err).Msg("unable to load seen large unconfirmed inbound")
			}
			if seen {
				continue
			}

			// mark this inbound as seen
			err = util.Store(seenKey, true)
			if err != nil {
				log.Panic().Err(err).Msg("unable to store seen large unconfirmed inbound")
			}

			// denormalize memoless reference
			formattedMemo := fmt.Sprintf("`%s`", tx.Tx.Memo)
			if tx.Tx.Memo == "" {
				// get decimals to determine if we need to normalize
				decimals := int64(common.THORChainDecimals)
				if tx.Tx.Coins[0].Asset.IsGasAsset() {
					decimals = tx.Tx.Coins[0].Asset.Chain.GetGasAssetDecimal()
				} else {
					pool := openapi.Pool{}
					err = util.ThornodeCachedRetryGet(fmt.Sprintf("thorchain/pool/%s", tx.Tx.Coins[0].Asset), block.Header.Height, &pool)
					if err != nil {
						log.Panic().Err(err).Msg("failed to get pool")
					}
					if pool.Decimals != nil {
						decimals = *pool.Decimals
					}
				}

				// normalize the amount to extract the reference
				amount := tx.Tx.Coins[0].Amount.Uint64()
				if decimals < int64(common.THORChainDecimals) {
					divisor := int64(1)
					for i := decimals; i < int64(common.THORChainDecimals); i++ {
						divisor *= 10
					}
					amount /= uint64(divisor)
				}

				refCount := util.GetConfigInt64(block.Header.Height, constants.MemolessTxnRefCount)
				txnRefLength := len(fmt.Sprintf("%d", refCount))
				modulus := uint64(1)
				for i := 0; i < txnRefLength; i++ {
					modulus *= 10
				}

				// extract reference from amount
				refNum := amount % modulus

				if refNum > 0 && refCount > 0 {
					registration := openapi.ReferenceMemoResponse{}
					err = util.ThornodeCachedRetryGet(
						fmt.Sprintf("thorchain/memo/%s/%d", tx.Tx.Coins[0].Asset, refNum),
						block.Header.Height,
						&registration,
					)
					if err != nil {
						log.Warn().Err(err).Msg("failed to get memo reference")
					}
					if registration.Memo != "" {
						formattedMemo = fmt.Sprintf("`%s` (memoless: %d)", registration.Memo, refNum)
					}
				}
			}

			// build notification
			title := "Large Unconfirmed Inbound"
			fields := util.NewOrderedMap()
			fields.Set("Chain", tx.Tx.Chain.String())
			fields.Set("Hash", tx.Tx.ID.String())
			fields.Set("Memo", formattedMemo)
			fields.Set("Confirmation Time", util.FormatDuration(confirmDuration))
			fields.Set(
				"Amount", fmt.Sprintf(
					"%f %s (%s)",
					float64(tx.Tx.Coins[0].Amount.Uint64())/common.One,
					tx.Tx.Coins[0].Asset,
					util.USDValueString(priceHeight, tx.Tx.Coins[0]),
				),
			)
			fields.Set(
				fmt.Sprintf("Clout (%s)", tx.Tx.FromAddress),
				fmt.Sprintf(
					"%f RUNE (%s)",
					float64(fromClout.Amount.Uint64())/common.One,
					util.FormatUSD(fromCloutUSD),
				),
			)

			// notify
			level := notify.Warning
			if uint64(usdValue) > config.Get().Thresholds.Security.USDValue {
				level = notify.Danger
			}
			notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, level, fields)

			// notify security if over security threshold
			if level == notify.Danger {
				notify.Notify(config.Get().Notifications.Security, title, block.Header.Height, nil, notify.Warning, fields)
			}
		}

	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Large Streaming Swap
////////////////////////////////////////////////////////////////////////////////////////

func LargeStreamingSwaps(block *thorscan.BlockResponse) {
	for _, event := range append(block.EndBlockEvents, block.FinalizeBlockEvents...) {
		if event["type"] != types.SwapEventType {
			continue
		}

		// only alert on the first sub swap
		if event["streaming_swap_count"] != "1" {
			continue
		}

		// only alert when there are multiple swaps
		if event["streaming_swap_quantity"] == "1" {
			continue
		}

		// parse the quantity
		quantity, err := strconv.Atoi(event["streaming_swap_quantity"])
		if err != nil {
			log.Panic().Err(err).Msg("unable to parse streaming swap quantity")
		}

		// check first the approximate USD value before fetching the inbound
		coin, err := common.ParseCoin(event["coin"])
		if err != nil {
			log.Panic().Str("coin", event["coin"]).Err(err).Msg("unable to parse streaming swap coin")
		}
		usdValue := util.USDValue(block.Header.Height, coin)
		if uint64(usdValue*float64(quantity)) < config.Get().Thresholds.USDValue {
			continue
		}

		// skip previously seen streaming swaps
		seen := false
		seenKey := fmt.Sprintf("seen-large-streaming-swap/%s", event["id"])
		err = util.Load(seenKey, &seen)
		if err != nil {
			log.Debug().Err(err).Msg("unable to load seen large streaming swap")
		}
		if seen {
			continue
		}

		// get the tx for the precise value
		tx := struct {
			ObservedTx openapi.ObservedTx `json:"observed_tx"`
		}{}
		url := fmt.Sprintf("thorchain/tx/%s", event["id"])
		err = util.ThornodeCachedRetryGet(url, block.Header.Height, &tx)
		if err != nil {
			log.Panic().Err(err).Msg("failed to get tx")
		}

		cloutFields := util.NewOrderedMap()
		fromClout := util.Clout(block.Header.Height, event["from"])
		fromCloutUSD := util.USDValue(block.Header.Height, fromClout)
		totalCloutUSD := uint64(fromCloutUSD)
		cloutFields.Set(
			fmt.Sprintf("Clout (%s)", event["from"]),
			fmt.Sprintf(
				"%f RUNE (%s)",
				float64(fromClout.Amount.Uint64())/common.One,
				util.FormatUSD(fromCloutUSD),
			),
		)

		// get to address from memo ("to" field in event is asgard)
		if event["memo"] != "noop" { // streaming loans send "noop"
			memoParts := strings.Split(event["memo"], ":")
			toAddress := memoParts[2]
			toClout := util.Clout(block.Header.Height, toAddress)
			toCloutUSD := util.USDValue(block.Header.Height, toClout)
			totalCloutUSD += uint64(toCloutUSD)
			cloutFields.Set(
				fmt.Sprintf("Clout (%s)", toAddress),
				fmt.Sprintf(
					"%f RUNE (%s)",
					float64(toClout.Amount.Uint64())/common.One,
					util.FormatUSD(toCloutUSD),
				),
			)
		}

		// verify precise amount
		coinStr := fmt.Sprintf("%s %s", tx.ObservedTx.Tx.Coins[0].Amount, tx.ObservedTx.Tx.Coins[0].Asset)
		coin, err = common.ParseCoin(coinStr)
		if err != nil {
			log.Panic().Str("coin", coinStr).Err(err).Msg("unable to parse coin")
		}
		usdValue = util.USDValue(block.Header.Height, coin)
		if uint64(usdValue) < config.Get().Thresholds.USDValue+totalCloutUSD {
			continue
		}

		// mark this swap as seen
		err = util.Store(seenKey, true)
		if err != nil {
			log.Panic().Err(err).Msg("unable to store seen large streaming swap")
		}

		// build notification
		title := "Streaming Swap"
		lines := []string{}
		if uint64(usdValue) > config.Get().Styles.USDPerMoneyBag {
			lines = append(lines, util.Moneybags(uint64(usdValue)))
		}
		fields := util.NewOrderedMap()
		fields.Set("Chain", event["chain"])
		fields.Set("Hash", event["id"])
		fields.Set("Amount", fmt.Sprintf(
			"%f %s (%s)",
			float64(coin.Amount.Uint64())/common.One,
			coin.Asset,
			util.USDValueString(block.Header.Height, coin),
		))
		fields.Set("Memo", fmt.Sprintf("`%s`", event["memo"]))
		fields.Set("Quantity", fmt.Sprintf("%s swaps", event["streaming_swap_quantity"]))

		// attempt adding interval and expected time
		args := strings.Split(event["memo"], ":")
		if len(args) > 3 {
			limitParams := strings.Split(args[3], "/")
			var interval int
			if len(limitParams) > 1 {
				interval, err = strconv.Atoi(limitParams[1])
				if err != nil {
					log.Error().Err(err).Msg("unable to parse streaming swap interval")
				}
			}
			if quantity > 0 && interval > 0 {
				ms := quantity * interval * int(common.THORChain.ApproximateBlockMilliseconds())
				swapDuration := time.Duration(ms) * time.Millisecond
				fields.Set("Interval", fmt.Sprintf("%d blocks", interval))
				fields.Set("Expected Swap Time", util.FormatDuration(swapDuration))
			}
		}

		// add clout fields
		for _, field := range cloutFields.Keys() {
			val, _ := cloutFields.Get(field)
			fields.Set(field, val)
		}

		links := []string{
			fmt.Sprintf("[Tracker](%s/%s)", config.Get().Links.Track, event["id"]),
			fmt.Sprintf("[Transaction](%s/tx/%s)", config.Get().Links.Explorer, event["id"]),
		}
		fields.Set("Links", strings.Join(links, " | "))

		// notify
		notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, lines, notify.Warning, fields)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Scheduled Outbounds
////////////////////////////////////////////////////////////////////////////////////////

// rescheduledOutbounds alerts on rescheduled outbounds and returns true if rescheduled.
func rescheduledOutbounds(height int64, event map[string]string) bool {
	// skip null in hash
	if event["in_hash"] == common.BlankTxID.String() {
		return false
	}

	// the key must be unique for refunds and multi-output outbounds
	key := fmt.Sprintf(
		"scheduled-outbound/%s-%s-%s-%s",
		event["memo"], event["coin_asset"], event["coin_amount"], event["to_address"],
	)

	// store this as the last seen event on return
	defer func() {
		err := util.Store(key, event)
		if err != nil {
			log.Panic().
				Err(err).
				Str("key", key).
				Msg("unable to store last seen height")
		}
	}()

	// load the last seen event for this key
	lastSeen := map[string]string{}
	err := util.Load(key, &lastSeen)
	if err != nil {
		return false
	}

	// build the notification
	title := "Rescheduled Outbound"
	fields := util.NewOrderedMap()
	links := []string{
		fmt.Sprintf("[Explorer](%s/tx/%s)", config.Get().Links.Explorer, event["in_hash"]),
	}
	lines := []string{}

	// get value
	asset, err := common.NewAsset(event["coin_asset"])
	if err != nil {
		log.Panic().
			Err(err).
			Str("asset", event["coin_asset"]).
			Msg("failed to parse asset")
	}
	amount := cosmos.NewUintFromString(event["coin_amount"])
	coin := common.NewCoin(asset, amount)
	usdValue := util.USDValue(height, coin)
	if uint64(usdValue) > config.Get().Styles.USDPerMoneyBag {
		lines = append(lines, util.Moneybags(uint64(usdValue)))
	}
	fields.Set("Coin", fmt.Sprintf(
		"%f %s (%s)",
		float64(coin.Amount.Uint64())/common.One, coin.Asset, util.FormatUSD(usdValue),
	))

	// get the transaction status if this was not a ragnarok outbound
	if !reMemoRagnarok.MatchString(event["memo"]) {
		statusURL := fmt.Sprintf("thorchain/tx/status/%s", event["in_hash"])
		status := openapi.TxStatusResponse{}
		err = util.ThornodeCachedRetryGet(statusURL, height, &status)
		if err != nil {
			log.Panic().
				Err(err).
				Str("txid", event["in_hash"]).
				Int64("height", height).
				Msg("failed to get transaction status")
		}

		if status.Stages.OutboundSigned == nil {
			log.Error().
				Str("txid", event["in_hash"]).
				Int64("height", height).
				Msg("outbound signed stage is nil")
			return true
		}

		// skip if older than the max reschedule age
		blockAge := status.Stages.OutboundSigned.GetBlocksSinceScheduled()
		ageDuration := time.Duration(blockAge*common.THORChain.ApproximateBlockMilliseconds()) * time.Millisecond
		if ageDuration > config.Get().Thresholds.MaxRescheduledAge {
			return true
		}

		// set age field
		fields.Set("Age", fmt.Sprintf("%s (%d blocks)", util.FormatDuration(ageDuration), blockAge))

		// add track link for swaps
		if status.Tx != nil && status.Tx.Memo != nil {
			memoParts := strings.Split(*status.Tx.Memo, ":")
			var memoType memo.TxType
			memoType, err = memo.StringToTxType(memoParts[0])
			if err != nil {
				log.Error().Err(err).Str("txid", event["in_hash"]).Msg("failed to parse memo type")
			}
			if memoType == thorchain.TxSwap {
				links = append(links, fmt.Sprintf("[Track](%s/%s)", config.Get().Links.Track, event["in_hash"]))
			}

			// include the inbound memo
			fields.Set("Inbound Memo", fmt.Sprintf("`%s`", *status.Tx.Memo))
		} else {
			// handle nil memo case
			fields.Set("Inbound Memo", "`<no memo>`")
		}
	}

	// add the outbound data
	fields.Set("Outbound Memo", fmt.Sprintf("`%s`", event["memo"]))
	vaultStr := fmt.Sprintf(
		"`%s` -> `%s`",
		lastSeen["vault_pub_key"][len(lastSeen["vault_pub_key"])-4:],
		event["vault_pub_key"][len(event["vault_pub_key"])-4:],
	)
	if event["vault_pub_key"] != lastSeen["vault_pub_key"] {
		vaultStr = config.EmojiRotatingLight + " " + vaultStr + " " + config.EmojiRotatingLight
	}
	fields.Set("Vault", vaultStr)
	fields.Set("Gas Rate", fmt.Sprintf("%s -> %s", lastSeen["gas_rate"], event["gas_rate"]))
	fields.Set("Max Gas", fmt.Sprintf("%s -> %s", lastSeen["max_gas_amount_0"], event["max_gas_amount_0"]))
	fields.Set("Links", strings.Join(links, " | "))

	// send notifications
	notify.Notify(config.Get().Notifications.Activity, title, height, lines, notify.Warning, fields)

	return true
}

// scheduledOutbound is called for scheduled_outbound block and tx events. It assumes
// all events are for multi-output outbounds corresponding to the same inbound.
func scheduledOutbound(height int64, events []map[string]string) {
	// skip migrate outbounds
	if reMemoMigration.MatchString(events[0]["memo"]) {
		return
	}

	// check for reschedule
	rescheduled := rescheduledOutbounds(height, events[0])

	// skip ragnarok transactions
	if reMemoRagnarok.MatchString(events[0]["memo"]) {
		return
	}

	// extract memo and coins
	var coins []common.Coin
	toAddresses := []string{}
	for _, event := range events {
		asset, err := common.NewAsset(event["coin_asset"])
		if err != nil {
			log.Panic().Str("asset", event["coin_asset"]).Err(err).Msg("unable to parse asset")
		}
		amount := cosmos.NewUintFromString(event["coin_amount"])
		coin := common.NewCoin(asset, amount)
		coins = append(coins, coin)
		toAddresses = append(toAddresses, event["to_address"])
	}

	// skip small outbounds, delta value is lower, but only fires if basis points threshold met
	usdValue := 0.0
	for _, coin := range coins {
		usdValue += util.USDValue(height, coin)
	}
	if uint64(usdValue) < config.Get().Thresholds.USDValue && uint64(usdValue) < config.Get().Thresholds.SwapDelta.USDValue {
		return
	}

	// get to address clout and skip if below threshold with clout
	cloutFields := util.NewOrderedMap()
	totalCloutUSD := uint64(0)
	for _, addr := range toAddresses {
		clout := util.Clout(height, addr)
		cloutUSD := util.USDValue(height, clout)
		totalCloutUSD += uint64(cloutUSD)
		cloutFields.Set(
			fmt.Sprintf("Clout (%s)", addr),
			fmt.Sprintf(
				"%f RUNE (%s)",
				float64(clout.Amount.Uint64())/common.One,
				util.FormatUSD(cloutUSD),
			),
		)
	}
	if uint64(usdValue) < config.Get().Thresholds.USDValue+totalCloutUSD && uint64(usdValue) < config.Get().Thresholds.SwapDelta.USDValue {
		return
	}

	// determine if the outbound value is a security alert
	level := notify.Warning
	if uint64(usdValue) > config.Get().Thresholds.Security.USDValue {
		level = notify.Danger
	}

	// skip rescheduled outbound alerts, unless over the security threshold
	if rescheduled && level < notify.Danger {
		return
	}

	// get the inbound status
	statusURL := fmt.Sprintf("thorchain/tx/status/%s", events[0]["in_hash"])
	status := openapi.TxStatusResponse{}
	err := util.ThornodeCachedRetryGet(statusURL, height, &status)
	if err != nil {
		log.Panic().
			Err(err).
			Str("txid", events[0]["in_hash"]).
			Int64("height", height).
			Msg("failed to get transaction status")
	}
	memoType := memo.TxUnknown
	if status.Tx != nil && status.Tx.Memo != nil {
		// skip simulation test outbounds (mocknet only)
		if isTestMemo(*status.Tx.Memo) {
			return
		}
		memoParts := strings.Split(*status.Tx.Memo, ":")
		memoType, err = memo.StringToTxType(memoParts[0])
		if err != nil {
			log.Error().Err(err).Msg("failed to parse memo type")
		}
	}

	// consider from address clout for swaps and trade/secured asset withdraws
	switch memoType {
	case memo.TxSwap, memo.TxTradeAccountWithdrawal, memo.TxSecuredAssetWithdraw:
		if status.Tx != nil && status.Tx.FromAddress != nil {
			clout := util.Clout(height, *status.Tx.FromAddress)
			cloutUSD := util.USDValue(height, clout)
			totalCloutUSD += uint64(cloutUSD)
			cloutFields.Set(
				fmt.Sprintf("Clout (%s)", *status.Tx.FromAddress),
				fmt.Sprintf(
					"%f RUNE (%s)",
					float64(clout.Amount.Uint64())/common.One,
					util.FormatUSD(cloutUSD),
				),
			)
		}
	}
	if uint64(usdValue) < config.Get().Thresholds.USDValue+totalCloutUSD && uint64(usdValue) < config.Get().Thresholds.SwapDelta.USDValue {
		return
	}

	// build the notification
	title := "Scheduled Outbound"
	if len(events) > 1 {
		title = fmt.Sprintf("Scheduled Outbounds (%d)", len(events))
		for _, event := range events {
			if reMemoRefund.MatchString(event["memo"]) {
				title += " _(partial fill)_"
				break
			}
		}
	}

	lines := []string{}
	if uint64(usdValue) > config.Get().Styles.USDPerMoneyBag {
		lines = append(lines, util.Moneybags(uint64(usdValue)))
	}
	fields := util.NewOrderedMap()
	if status.Tx != nil && status.Tx.Memo != nil {
		fields.Set("Inbound Memo", fmt.Sprintf("`%s`", *status.Tx.Memo))
	}

	links := []string{
		fmt.Sprintf("[Explorer](%s/tx/%s)", config.Get().Links.Explorer, events[0]["in_hash"]),
		fmt.Sprintf("[Live Outbounds](%s)", config.Get().Links.Track),
	}

	// add the inbound coins for inbound swap or outbound refund
	if (memoType == thorchain.TxSwap || reMemoRefund.MatchString(events[0]["memo"])) && status.Tx != nil && len(status.Tx.Coins) > 0 {
		inboundCoin := util.CoinToCommon(status.Tx.Coins[0])
		inboundUSDValue := util.USDValue(height, inboundCoin)
		fields.Set("Inbound Amount", fmt.Sprintf(
			"%f %s (%s)",
			float64(inboundCoin.Amount.Uint64())/common.One,
			inboundCoin.Asset,
			util.USDValueString(height, inboundCoin),
		))

		// add the delta
		delta := usdValue - inboundUSDValue
		deltaBasisPoints := int64(float64(delta) / inboundUSDValue * 10000)
		deltaStr := fmt.Sprintf("%s (%.02f%%)", util.FormatUSD(delta), float64(delta)/inboundUSDValue*100)
		if int(delta) > 0 {
			// red triangle if perceived value increased
			deltaStr = config.EmojiSmallRedTriangle + " " + deltaStr
		}

		// skip if delta below threshold and below the broader usd value threshold
		if deltaBasisPoints < config.Get().Thresholds.SwapDelta.BasisPoints &&
			uint64(inboundUSDValue) < config.Get().Thresholds.USDValue+totalCloutUSD {
			return
		}

		if deltaBasisPoints > config.Get().Thresholds.SwapDelta.BasisPoints {
			// rotating light and tag @here if delta
			deltaStr = config.EmojiRotatingLight + " " + deltaStr + " " + config.EmojiRotatingLight
			level = notify.Danger
		}
		fields.Set("Delta", deltaStr)
	} else if uint64(usdValue) < config.Get().Thresholds.USDValue+totalCloutUSD {
		// skip when no delta and below the broader usd value threshold
		return
	}

	// extra fields for swap alerts
	if memoType == thorchain.TxSwap {
		links = append(links, fmt.Sprintf("[Track](%s/%s)", config.Get().Links.Track, events[0]["in_hash"]))

		// add streaming swap durations
		lastStatus := openapi.TxStatusResponse{}
		err = util.ThornodeCachedRetryGet(statusURL, height-1, &lastStatus)
		if err != nil {
			log.Error().Err(err).Msg("failed to get last transaction status")
		} else if lastStatus.Stages.SwapStatus != nil &&
			lastStatus.Stages.SwapStatus.Streaming != nil &&
			lastStatus.Stages.SwapStatus.Streaming.Interval > 0 &&
			lastStatus.Stages.SwapStatus.Streaming.Quantity > 0 &&
			status.Tx != nil &&
			len(status.Tx.Coins) > 0 {

			interval := lastStatus.Stages.SwapStatus.Streaming.Interval
			quantity := lastStatus.Stages.SwapStatus.Streaming.Quantity
			ms := quantity * interval * common.THORChain.ApproximateBlockMilliseconds()
			swapDuration := time.Duration(ms) * time.Millisecond
			fields.Set("Stream Duration", util.FormatDuration(swapDuration))

			// add the price delta for both swap assets
			inCoin := util.CoinToCommon(status.Tx.Coins[0])
			outCoin := coins[0]
			beginHeight := height - quantity*interval
			beginInValue := util.USDValue(beginHeight, inCoin)
			beginOutValue := util.USDValue(beginHeight, outCoin)
			endInValue := util.USDValue(height, inCoin)
			endOutValue := util.USDValue(height, outCoin)
			deltaIn := 1 - beginInValue/endInValue
			deltaOut := 1 - beginOutValue/endOutValue
			key := fmt.Sprintf("Stream Price Shift (%s)", strings.Split(inCoin.Asset.String(), "-")[0])
			fields.Set(key, fmt.Sprintf("%.02f%%", deltaIn*100))
			key = fmt.Sprintf("Stream Price Shift (%s)", strings.Split(outCoin.Asset.String(), "-")[0])
			fields.Set(key, fmt.Sprintf("%.02f%%", deltaOut*100))
		}
	}

	// add the outbound data
	for i, coin := range coins {
		amountField := "Outbound Amount"
		memoField := "Outbound Memo"
		if len(coins) > 1 {
			amountField = fmt.Sprintf("Outbound %d Amount", i+1)
			memoField = fmt.Sprintf("Outbound %d Memo", i+1)
		}
		fields.Set(amountField, fmt.Sprintf(
			"%f %s (%s)",
			float64(coin.Amount.Uint64())/common.One,
			coin.Asset,
			util.USDValueString(height, coin),
		))
		fields.Set(memoField, fmt.Sprintf("`%s`", events[i]["memo"]))
	}

	// add clout fields
	for _, field := range cloutFields.Keys() {
		val, _ := cloutFields.Get(field)
		fields.Set(field, val)
	}

	// determine the expected delay
	outboundDelay := status.Stages.GetOutboundDelay()
	delayDuration := time.Duration((&outboundDelay).GetRemainingDelaySeconds()) * time.Second
	fields.Set("Expected Delay", util.FormatDuration(delayDuration))
	fields.Set("Links", strings.Join(links, " | "))

	// send notifications
	notify.Notify(config.Get().Notifications.Activity, title, height, lines, level, fields)
	if level == notify.Danger {
		notify.Notify(config.Get().Notifications.Security, title, height, lines, level, fields)
	}
}

func ScheduledOutbounds(block *thorscan.BlockResponse) {
	events := []map[string]string{}

	// gather block events
	for _, event := range append(block.EndBlockEvents, block.FinalizeBlockEvents...) {
		if event["type"] != types.ScheduledOutboundEventType {
			continue
		}
		events = append(events, event)
	}

	// gather transaction events
	for _, tx := range block.Txs {
		// skip failed decode transactions
		if tx.Tx == nil {
			continue
		}
		for _, event := range tx.Result.Events {
			if event["type"] != types.ScheduledOutboundEventType {
				continue
			}
			events = append(events, event)
		}
	}

	// coalesce scheduled outbounds by inbound hash
	scheduledOutbounds := map[string][]map[string]string{}
	for _, event := range events {
		if _, ok := scheduledOutbounds[event["in_hash"]]; !ok {
			scheduledOutbounds[event["in_hash"]] = []map[string]string{}
		}
		scheduledOutbounds[event["in_hash"]] = append(scheduledOutbounds[event["in_hash"]], event)
	}

	// send notifications for each in hash scheduled outbounds
	for _, events := range scheduledOutbounds {
		scheduledOutbound(block.Header.Height, events)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Large Transfers
////////////////////////////////////////////////////////////////////////////////////////

func LargeTransfers(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		// skip failed transactions
		if *tx.Result.Code != 0 {
			continue
		}

		// skip failed decode transactions
		if tx.Tx == nil {
			continue
		}

		for _, msg := range tx.Tx.GetMsgs() {
			amount := uint64(0)
			var fromAddr, toAddr string
			switch m := msg.(type) {
			case *thorchain.MsgSend:
				for _, coin := range m.Amount {
					if coin.Denom == "rune" {
						amount = coin.Amount.Uint64()
					}
				}
				fromAddr = m.FromAddress.String()
				toAddr = m.ToAddress.String()
			case *bank.MsgSend:
				for _, coin := range m.Amount {
					if coin.Denom == "rune" {
						amount = coin.Amount.Uint64()
					}
				}
				fromAddr = m.FromAddress
				toAddr = m.ToAddress
			default:
				continue
			}

			// skip small transfers
			if amount < config.Get().Thresholds.RuneTransferValue*common.One {
				continue
			}

			fields := util.NewOrderedMap()

			// determine if this is an external migration
			txWithMemo, ok := tx.Tx.(ctypes.TxWithMemo)
			if !ok {
				log.Panic().Msg("failed to cast tx to TxWithMemo")
			}
			matches := reMemoMigration.FindStringSubmatch(txWithMemo.GetMemo())
			if len(matches) > 0 {
				title := fmt.Sprintf(
					"External Migration `%s` (%s RUNE)",
					txWithMemo.GetMemo(), util.FormatLocale(amount/common.One),
				)
				fields.Set(
					"Links",
					fmt.Sprintf("[Transaction](%s/tx/%s)", config.Get().Links.Explorer, tx.Hash),
				)
				notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Info, fields)
				continue
			}

			// otherwise this is just a large transfer
			title := fmt.Sprintf(
				"Large Transfer >> %s RUNE (%s)",
				util.FormatLocale(amount/common.One),
				util.USDValueString(block.Header.Height, common.NewCoin(common.RuneAsset(), cosmos.NewUint(amount))),
			)

			// use known address labels in alert
			fromAddrLabel := fromAddr
			toAddrLabel := toAddr
			if _, ok = config.Get().LabeledAddresses[fromAddr]; ok {
				fromAddrLabel = config.Get().LabeledAddresses[fromAddr]
			}
			if _, ok = config.Get().LabeledAddresses[toAddr]; ok {
				toAddrLabel = config.Get().LabeledAddresses[toAddr]
			}

			links := []string{
				fmt.Sprintf("[Transaction](%s/tx/%s)", config.Get().Links.Explorer, tx.BlockTx.Hash),
				fmt.Sprintf("[%s](%s/address/%s)", fromAddrLabel, config.Get().Links.Explorer, fromAddr),
				fmt.Sprintf("[%s](%s/address/%s)", toAddrLabel, config.Get().Links.Explorer, toAddr),
			}
			fields.Set("Hash", tx.BlockTx.Hash)
			fields.Set("From", fromAddr)
			fields.Set("To", toAddr)
			fields.Set("Links", strings.Join(links, " | "))
			notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Warning, fields)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Inactive Vault Inbounds
////////////////////////////////////////////////////////////////////////////////////////

type Vaults struct {
	Active   map[string]bool
	Retiring map[string]bool
	Height   int64
}

var vaults *Vaults

func init() {
	_ = util.Load("vaults", vaults)
}

func InactiveVaultInbounds(block *thorscan.BlockResponse) {
	// update our active vault set any time there is an active vault event
	update := false
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] == types.VaultStatus_ActiveVault.String() {
				update = true
				break
			}
		}
	}

	if vaults == nil || update {
		vaults = &Vaults{
			Active:   make(map[string]bool),
			Retiring: make(map[string]bool),
			Height:   block.Header.Height,
		}
		vaultsRes := []openapi.Vault{}
		err := util.ThornodeCachedRetryGet("thorchain/vaults/asgard", block.Header.Height, &vaultsRes)
		if err != nil {
			log.Panic().Err(err).Msg("failed to get vaults")
		}
		for _, vault := range vaultsRes {
			if vault.Status == types.VaultStatus_ActiveVault.String() {
				if vault.PubKey != nil {
					vaults.Active[*vault.PubKey] = true
				}
				if vault.PubKeyEddsa != nil {
					vaults.Active[*vault.PubKeyEddsa] = true
				}
			}
			if vault.Status == types.VaultStatus_RetiringVault.String() {
				if vault.PubKey != nil {
					vaults.Retiring[*vault.PubKey] = true
				}
				if vault.PubKeyEddsa != nil {
					vaults.Retiring[*vault.PubKeyEddsa] = true
				}
			}
		}
		err = util.Store("vaults", vaults)
		if err != nil {
			log.Panic().Err(err).Msg("failed to store vaults")
		}
	}

	// check for inactive vault inbounds
	for _, tx := range block.Txs {
		// skip failed decode transactions
		if tx.Tx == nil {
			continue
		}

		for _, msg := range tx.Tx.GetMsgs() {
			// skip anything other than observed transactions
			msgObservedTx, ok := msg.(*types.MsgObservedTxQuorum)
			if !ok {
				continue
			}
			if !msgObservedTx.QuoTx.Inbound {
				continue
			}

			tx := msgObservedTx.QuoTx.ObsTx

			// skip inbounds to active vaults
			if vaults.Active[tx.ObservedPubKey.String()] {
				continue
			}

			// skip inbounds to retiring vaults within 12 hours
			if vaults.Retiring[tx.ObservedPubKey.String()] &&
				block.Header.Height-vaults.Height < 7200 {
				continue
			}

			// skip if this is late observation for inbound with consensus already reached
			txResp := openapi.TxResponse{}
			err := util.ThornodeCachedRetryGet(
				fmt.Sprintf("thorchain/tx/%s", tx.Tx.ID),
				block.Header.Height-1,
				&txResp,
			)
			if err != nil {
				log.Warn().Err(err).
					Str("hash", tx.Tx.ID.String()).
					Msg("unable to load tx for inactive vault inbound consensus check")
			} else if txResp.ConsensusHeight != nil {
				continue
			}

			// skip previously seen inactive inbounds
			seen := false
			seenKey := fmt.Sprintf("seen-inactive-inbound/%s", tx.Tx.ID.String())
			err = util.Load(seenKey, &seen)
			if err != nil {
				log.Debug().Err(err).Msg("unable to load seen inactive inbound")
			}
			if seen {
				continue
			}

			// mark this inbound as seen
			err = util.Store(seenKey, true)
			if err != nil {
				log.Panic().Err(err).Msg("unable to store seen inactive inbound")
			}

			// gather links
			links := []string{
				fmt.Sprintf("[Transaction](%s/tx/%s)", config.Get().Links.Explorer, tx.Tx.ID),
				fmt.Sprintf("[Track](%s/%s)", config.Get().Links.Track, tx.Tx.ID),
			}

			// build notification
			title := "Inbound to Non-Active Vault"
			fields := util.NewOrderedMap()
			fields.Set("Chain", tx.Tx.Chain.String())
			fields.Set("Vault", tx.ObservedPubKey.String())
			fields.Set("Vault Address", tx.Tx.ToAddress.String())
			fields.Set("Memo", fmt.Sprintf("`%s`", tx.Tx.Memo))
			fields.Set("Links", strings.Join(links, " | "))

			// notify
			notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Warning, fields)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// New Node
////////////////////////////////////////////////////////////////////////////////////////

func NewNode(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] != "new_node" {
				continue
			}

			for _, msg := range tx.Tx.GetMsgs() {
				amount := uint64(0)
				operator := ""
				switch msg := msg.(type) {
				case *thorchain.MsgDeposit:
					for _, coin := range msg.Coins {
						if coin.Asset.Equals(common.RuneAsset()) {
							amount = coin.Amount.Uint64()
						}
					}
					operator = msg.Signer.String()
				case *thorchain.MsgSend:
					if !util.IsThorchainModule(msg.ToAddress.String()) {
						continue
					}

					for _, coin := range msg.Amount {
						if coin.Denom == "rune" {
							amount = coin.Amount.Uint64()
						}
					}
					operator = msg.FromAddress.String()

				case *bank.MsgSend:
					if !util.IsThorchainModule(msg.ToAddress) {
						continue
					}

					for _, coin := range msg.Amount {
						if coin.Denom == "rune" {
							amount = coin.Amount.Uint64()
						}
					}
					operator = msg.FromAddress
				default:
					continue
				}

				title := "New Node"
				fields := util.NewOrderedMap()
				operator = operator[len(operator)-4:]
				fields.Set("Hash", tx.Hash)
				fields.Set("Operator", fmt.Sprintf("`%s`", operator))
				fields.Set("Node", fmt.Sprintf("`%s`", event["address"][len(event["address"])-4:]))
				fields.Set("Amount", fmt.Sprintf("%s RUNE", util.FormatLocale(float64(amount)/common.One)))
				notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Info, fields)
			}
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Bond
////////////////////////////////////////////////////////////////////////////////////////

func Bond(block *thorscan.BlockResponse) {
txs:
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			// skip if this was an initial node bond (picked up by new node alert)
			if event["type"] == "new_node" {
				continue txs
			}

			if event["type"] != types.BondEventType {
				continue
			}

			for _, msg := range tx.Tx.GetMsgs() {
				amount := uint64(0)
				provider := ""
				memo := ""
				switch msg := msg.(type) {
				case *thorchain.MsgDeposit:
					for _, coin := range msg.Coins {
						if coin.Asset.Equals(common.RuneAsset()) {
							amount = coin.Amount.Uint64()
						}
					}
					provider = msg.Signer.String()
					memo = msg.Memo
				case *thorchain.MsgSend:
					if !util.IsThorchainModule(msg.ToAddress.String()) {
						continue
					}

					for _, coin := range msg.Amount {
						if coin.Denom == "rune" {
							amount = coin.Amount.Uint64()
						}
					}
					provider = msg.FromAddress.String()
					if mTx, ok := tx.Tx.(ctypes.TxWithMemo); ok {
						memo = mTx.GetMemo()
					} else {
						log.Panic().Msg("failed to cast tx to TxWithMemo")
					}
				case *bank.MsgSend:
					if !util.IsThorchainModule(msg.ToAddress) {
						continue
					}

					for _, coin := range msg.Amount {
						if coin.Denom == "rune" {
							amount = coin.Amount.Uint64()
						}
					}
					provider = msg.FromAddress
					if mTx, ok := tx.Tx.(ctypes.TxWithMemo); ok {
						memo = mTx.GetMemo()
					} else {
						log.Panic().Msg("failed to cast tx to TxWithMemo")
					}
				default:
					continue
				}

				title := "Bond"
				fields := util.NewOrderedMap()
				provider = provider[len(provider)-4:]
				fields.Set("Hash", tx.Hash)
				fields.Set("Provider", fmt.Sprintf("`%s`", provider))
				fields.Set("Memo", fmt.Sprintf("`%s`", memo))
				fields.Set("Amount", util.FormatLocale(float64(amount)/common.One))

				// extract node address from memo
				m, err := thorchain.ParseMemo(common.LatestVersion, memo)
				if err != nil {
					log.Panic().Str("memo", memo).Err(err).Msg("failed to parse memo")
				}

				addNodeInfo := func(nodeAddress string) {
					fields.Set("Node", fmt.Sprintf("`%s`", nodeAddress[len(nodeAddress)-4:]))

					// lookup node to determine operator
					nodes := []openapi.Node{}
					err = util.ThornodeCachedRetryGet("thorchain/nodes", block.Header.Height, &nodes)
					if err != nil {
						log.Panic().Err(err).Msg("failed to get nodes")
					}
					for _, node := range nodes {
						if node.NodeAddress == nodeAddress {
							fields.Set("Operator", fmt.Sprintf("`%s`", node.NodeOperatorAddress[len(node.NodeOperatorAddress)-4:]))
							break
						}
					}
				}

				switch memo := m.(type) {
				case thorchain.BondMemo:
					addNodeInfo(memo.NodeAddress.String())
				case thorchain.UnbondMemo:
					addNodeInfo(memo.NodeAddress.String())
					unbondAmount := cosmos.NewUintFromString(event["amount"]).Uint64()
					fields.Set("Unbond Amount", util.FormatLocale(float64(unbondAmount)/common.One))
				}

				notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Info, fields)
			}
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Failed Transactions
////////////////////////////////////////////////////////////////////////////////////////

// failedTxLimiter rate-limits all failed transaction alerts using config values.
var failedTxLimiter = func() *rate.Limiter {
	cfg := config.Get().RateLimits.FailedTransactions
	return rate.NewLimiter(rate.Every(cfg.Interval), cfg.Burst)
}()

func FailedTransactions(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		// skip successful transactions and failed gas or sequence
		switch *tx.Result.Code {
		case 0: // success
			continue
		case 5: // insufficient funds
			continue
		case 32: // bad sequence
			continue
		case 99: // internal, avoid noise
			continue
		}

		// alert fields
		fields := util.NewOrderedMap()
		fields.Set("Code", fmt.Sprintf("%d", *tx.Result.Code))
		fields.Set(
			"Transaction",
			fmt.Sprintf("%s/cosmos/tx/v1beta1/txs/%s", config.Get().Links.Thornode, tx.BlockTx.Hash),
		)

		// determine if the transaction failed to decode
		if tx.Tx == nil {
			fields.Set("Failed Decode", "true")
		}
		if tx.Result.Codespace != nil {
			fields.Set("Codespace", fmt.Sprintf("`%s`", *tx.Result.Codespace))
		}
		if tx.Result.Log != nil {
			fields.Set("Log", fmt.Sprintf("`%s`", *tx.Result.Log))
		}

		title := "Failed Transaction"

		// rate-limit all failed transaction alerts
		if !failedTxLimiter.Allow() {
			log.Warn().
				Str("txid", tx.Hash).
				Int64("height", block.Header.Height).
				Stringer("fields", fields).
				Msg("skipping failed transaction alert due to rate limit")
			continue
		}

		// notify failed transaction
		notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Warning, fields)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Failed Refunds
////////////////////////////////////////////////////////////////////////////////////////

func FailedRefunds(block *thorscan.BlockResponse) {
	for _, event := range block.EndBlockEvents {
		if event["type"] != types.RefundEventType {
			continue
		}

		// only failed refunds
		if !strings.Contains(event["reason"], "fail to refund") {
			continue
		}

		coin, err := common.ParseCoin(event["coin"])
		if err != nil {
			log.Panic().Str("coin", event["coin"]).Err(err).Msg("unable to parse refund coin")
		}

		// skip refunds for less than the native transaction fee (failed affiliate swaps)
		txFee := float64(constants.NewConstantValue().GetInt64Value(constants.NativeTransactionFee)) / common.One
		// analyze-ignore(float-comparison)
		if util.RuneValue(block.Header.Height, coin) < txFee {
			continue
		}

		fields := util.NewOrderedMap()
		fields.Set("Chain", event["chain"])
		fields.Set("Hash", event["id"])
		fields.Set("Inbound From Address", fmt.Sprintf("`%s`", event["from"]))
		fields.Set("Inbound Memo", fmt.Sprintf("`%s`", event["memo"]))
		fields.Set("Amount", fmt.Sprintf(
			"%f %s (%s)",
			float64(coin.Amount.Uint64())/common.One,
			coin.Asset,
			util.USDValueString(block.Header.Height, coin),
		))
		reason := event["reason"]
		reason = regexp.MustCompile(`\s+`).ReplaceAllString(reason, " ")
		fields.Set("Reason", fmt.Sprintf("`%s`", reason))

		links := []string{
			fmt.Sprintf("[Transaction](%s/tx/%s)", config.Get().Links.Explorer, event["id"]),
			fmt.Sprintf("[Track](%s/%s)", config.Get().Links.Track, event["id"]),
		}
		fields.Set("Links", strings.Join(links, " | "))

		title := "Failed Refund"
		notify.Notify(config.Get().Notifications.FailedRefunds, title, block.Header.Height, nil, notify.Info, fields)
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// THORName Registrations
////////////////////////////////////////////////////////////////////////////////////////

func THORNameRegistrations(block *thorscan.BlockResponse) {
	for _, tx := range block.Txs {
		for _, event := range tx.Result.Events {
			if event["type"] != types.THORNameEventType {
				continue
			}

			// extract values
			registrationFee := cosmos.NewUintFromString(event["registration_fee"]).Uint64()
			fundAmount := cosmos.NewUintFromString(event["fund_amount"]).Uint64()
			expire := cosmos.NewUintFromString(event["expire"]).Uint64()
			expireDuration := time.Duration(expire-uint64(block.Header.Height)) * constants.ThorchainBlockTime

			// alert fields
			fields := util.NewOrderedMap()
			fields.Set("THORName", fmt.Sprintf("`%s`", event["name"]))
			fields.Set("Expiration", fmt.Sprintf("`%d` (%s)", expire, util.FormatDuration(expireDuration)))
			fields.Set("Registration Fee", util.FormatLocale(float64(registrationFee)/common.One))
			fields.Set("Fund Amount", util.FormatLocale(float64(fundAmount)/common.One))

			for _, msg := range tx.Tx.GetMsgs() {
				switch m := msg.(type) {
				case *thorchain.MsgDeposit:
					fields.Set("Memo", fmt.Sprintf("`%s`", m.Memo))
				case *thorchain.MsgSend:
					if !util.IsThorchainModule(m.ToAddress.String()) {
						continue
					}

					if mTx, ok := tx.Tx.(ctypes.TxWithMemo); ok {
						fields.Set("Memo", fmt.Sprintf("`%s`", mTx.GetMemo()))
					} else {
						log.Panic().Msg("failed to cast tx to TxWithMemo")
					}
				case *bank.MsgSend:
					if !util.IsThorchainModule(m.ToAddress) {
						continue
					}

					if mTx, ok := tx.Tx.(ctypes.TxWithMemo); ok {
						fields.Set("Memo", fmt.Sprintf("`%s`", mTx.GetMemo()))
					} else {
						log.Panic().Msg("failed to cast tx to TxWithMemo")
					}
				}
			}

			fields.Set(
				"Transaction",
				fmt.Sprintf("%s/cosmos/tx/v1beta1/txs/%s", config.Get().Links.Thornode, tx.BlockTx.Hash),
			)

			// notify thorname registration
			title := "THORName Registration"
			notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, nil, notify.Info, fields)
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Large High Slip Swaps
////////////////////////////////////////////////////////////////////////////////////////

func LargeHighSlipSwaps(block *thorscan.BlockResponse) {
	for _, event := range append(block.EndBlockEvents, block.FinalizeBlockEvents...) {
		if event["type"] != types.SwapEventType {
			continue
		}

		// skip swap events below the slip threshold
		poolSlip := cosmos.NewUintFromString(event["pool_slip"]).Uint64()
		swapSlip := cosmos.NewUintFromString(event["swap_slip"]).Uint64()
		if swapSlip < config.Get().Thresholds.SwapSlipBasisPoints && poolSlip < config.Get().Thresholds.SwapSlipBasisPoints {
			continue
		}

		// check first the approximate USD value before fetching the inbound
		coin, err := common.ParseCoin(event["coin"])
		if err != nil {
			log.Panic().Str("coin", event["coin"]).Err(err).Msg("unable to parse streaming swap coin")
		}
		usdValue := util.USDValue(block.Header.Height, coin)
		if uint64(usdValue) < config.Get().Thresholds.USDValue {
			continue
		}

		// build notification
		title := "High Slip Swap"
		lines := []string{}
		if uint64(usdValue) > config.Get().Styles.USDPerMoneyBag {
			lines = append(lines, util.Moneybags(uint64(usdValue)))
		}
		fields := util.NewOrderedMap()
		fields.Set("Chain", event["chain"])
		fields.Set("Hash", event["id"])
		fields.Set("Amount", fmt.Sprintf(
			"%f %s (%s)",
			float64(coin.Amount.Uint64())/common.One,
			coin.Asset,
			util.USDValueString(block.Header.Height, coin),
		))
		fields.Set("Memo", fmt.Sprintf("`%s`", event["memo"]))
		fields.Set("Swap Slip", fmt.Sprintf("%.2f%%", float64(swapSlip)/100))
		fields.Set("Pool Slip", fmt.Sprintf("%.2f%%", float64(poolSlip)/100))

		links := []string{
			fmt.Sprintf("[Tracker](%s/%s)", config.Get().Links.Track, event["id"]),
			fmt.Sprintf("[Transaction](%s/tx/%s)", config.Get().Links.Explorer, event["id"]),
		}
		fields.Set("Links", strings.Join(links, " | "))

		// notify
		notify.Notify(config.Get().Notifications.Activity, title, block.Header.Height, lines, notify.Warning, fields)
	}
}
