package features

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
)

const (
	dashInstantSendSmallPayment = 18 * common.One / 10
	dashInstantSendLargePayment = 22 * common.One / 10
	dashInstantSendMaxAttempts  = 10
	dashInstantSendLowFee       = 600
)

type DashInstantSendActor struct {
	Actor

	account *User
	inbound common.Address
	dash    *utxo.Client

	getMimir      func(string) (int64, error)
	readMetrics   func() (instantSendMetrics, error)
	readyDeadline time.Time
	readyError    error
}

// DashInstantSend wires up a feature actor that exercises different InstantSend paths.
func DashInstantSend(rng *rand.Rand) *Actor {
	a := &DashInstantSendActor{
		Actor:       *NewActor("Feature-DASH-InstantSend", rng),
		readMetrics: readInstantSendMetrics,
	}
	a.Timeout = 5 * time.Minute
	a.Interval = time.Second

	a.Ops = []Op{
		a.waitForReadiness,
		a.acquireUser,
		a.initContext,
		a.happyPath,
		a.largerPayment,
		a.lowFee,
		a.releaseAccount,
	}

	return &a.Actor
}

// requiredBalance covers every scenario's full retry budget, including fees.
func (a *DashInstantSendActor) requiredBalance(defaultFee uint64) uint64 {
	const payments = 2*dashInstantSendSmallPayment + dashInstantSendLargePayment
	fees := 2*defaultFee + dashInstantSendLowFee
	return dashInstantSendMaxAttempts * (payments + fees)
}

// acquireUser reserves an available Dash account that can fund every retry.
func (a *DashInstantSendActor) acquireUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		client, ok := user.ChainClients[common.DASHChain].(*utxo.Client)
		if !ok {
			continue
		}
		if !user.Acquire() {
			continue
		}

		account, err := client.GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Str("user", user.Name()).Msg("failed to read DASH balance")
			user.Release()
			continue
		}
		required := cosmos.NewUint(a.requiredBalance(client.GetGasSats()))
		balance := account.Coins.GetCoin(common.DASHAsset).Amount
		if balance.LT(required) {
			a.Log().Info().Str("user", user.Name()).Stringer("balance", balance).
				Stringer("required", required).Msg("insufficient DASH for InstantSend retry budget")
			user.Release()
			continue
		}

		a.account = user
		a.dash = client
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
		break
	}

	if a.account == nil {
		return OpResult{Error: fmt.Errorf("no available DASH account can fund the InstantSend retry budget")}
	}
	return OpResult{Continue: true}
}

// initContext fetches the inbound address for the scenarios.
func (a *DashInstantSendActor) initContext(_ *OpConfig) OpResult {
	inboundAddr, _, err := thornode.GetInboundAddress(common.DASHChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to fetch DASH inbound address")
		return OpResult{Continue: false}
	}
	if inboundAddr.IsEmpty() {
		return OpResult{Finish: true, Error: fmt.Errorf("DASH inbound address missing")}
	}
	a.inbound = inboundAddr

	a.Log().Info().
		Stringer("inbound", a.inbound).
		Msg("dash instantsend context loaded")

	return OpResult{Continue: true}
}

// happyPath checks that a standard deposit is finalised before mining.
func (a *DashInstantSendActor) happyPath(_ *OpConfig) OpResult {
	return a.observePayment("happy-path", dashInstantSendSmallPayment, 0)
}

// observePayment requires THORChain to finalise this transaction while Dash
// still reports it as InstantSend locked and unconfirmed in the mempool. The Dash
// height must remain unchanged since broadcast so a mined observation cannot pass.
func (a *DashInstantSendActor) observePayment(name string, amount, feeSats uint64) (result OpResult) {
	// A failed scenario has no later release operation; release its account here.
	defer func() {
		if result.Finish {
			a.releaseAccount(nil)
		}
	}()
	if a.dash == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("Dash client not initialized")}
	}

	const (
		thornodeWaitMax  = 25 * time.Second
		blockSyncTimeout = 30 * time.Second
	)

	for attempt := 1; attempt <= dashInstantSendMaxAttempts; attempt++ {
		// Broadcast right after a new DASH block to maximize time before mining.
		_, broadcastHeight, err := a.dash.WaitForBlockAdvance(blockSyncTimeout)
		if err != nil {
			a.Log().Debug().Err(err).Msg("failed to sync to next DASH block before broadcast")
			broadcastHeight, err = a.dash.GetBlockCount()
			if err != nil {
				return OpResult{Finish: true, Error: fmt.Errorf("read Dash height before broadcast: %w", err)}
			}
		}

		txID, err := a.broadcastTx(name, amount, feeSats)
		if err != nil {
			return OpResult{Finish: true, Error: err}
		}

		thornodeDeadline := time.Now().Add(thornodeWaitMax)
		for time.Now().Before(thornodeDeadline) {
			currentHeight, err := a.dash.GetBlockCount()
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if currentHeight != broadcastHeight {
				break
			}

			stages, err := thornode.GetTxStages(txID)
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if stages.InboundFinalised == nil || !stages.InboundFinalised.Completed {
				time.Sleep(250 * time.Millisecond)
				continue
			}

			// Check Dash after THORChain finalisation, for this exact transaction.
			txInfo, err := a.dash.GetRawTransactionVerbose(txID)
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if txInfo.Confirmations != 0 {
				break
			}
			if !txInfo.InstantSendLocked() {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			_, err = a.dash.GetMempoolEntry(txID)
			if err != nil {
				break
			}
			currentHeight, err = a.dash.GetBlockCount()
			if err != nil || currentHeight != broadcastHeight {
				break
			}

			a.Log().Info().
				Int("attempt", attempt).
				Str("txid", txID).
				Int64("dash_height", broadcastHeight).
				Msg("InstantSend finalised by THORChain while tx remained locked in Dash mempool")
			return OpResult{Continue: true}
		}

		a.Log().Info().Int("attempt", attempt).Str("txid", txID).
			Msg("could not verify InstantSend finalisation before mining; retrying")
		time.Sleep(time.Second)
	}

	return OpResult{Finish: true, Error: fmt.Errorf("failed to finalise inbound on THORChain while InstantSend locked in Dash mempool")}
}

// largerPayment checks a payment above the former $100 simulation limit
// at the fixed $50 DASH price.
func (a *DashInstantSendActor) largerPayment(_ *OpConfig) OpResult {
	return a.observePayment("larger-payment", dashInstantSendLargePayment, 0)
}

// lowFee checks a fee below the former InstantSend floor while still paying for relay.
func (a *DashInstantSendActor) lowFee(_ *OpConfig) OpResult {
	return a.observePayment("low-fee", dashInstantSendSmallPayment, dashInstantSendLowFee)
}

// releaseAccount makes the account available to other actors and finishes this actor.
func (a *DashInstantSendActor) releaseAccount(_ *OpConfig) OpResult {
	if a.account != nil {
		a.account.Release()
	}
	a.account = nil
	a.dash = nil
	return OpResult{Finish: true}
}

// broadcastTx signs and broadcasts a NOOP deposit with the scenario's amount and fee override.
func (a *DashInstantSendActor) broadcastTx(scenario string, amount uint64, feeSats uint64) (string, error) {
	// Recheck after block synchronization, before spending funds on this attempt.
	_, err := a.checkReadiness(amount, amount)
	if err != nil {
		return "", fmt.Errorf("InstantSend not ready for %s: %w", scenario, err)
	}
	// Build memo and tx with a fixed fee override tuned for the scenario.
	// Use NOOP to avoid pool-availability issues.
	// This actor is focused on InstantSend finality paths, not swap execution.
	tx := SimTx{
		Chain:     common.DASHChain,
		ToAddress: a.inbound,
		Coin:      common.NewCoin(common.DASHAsset, cosmos.NewUint(amount)),
		Memo:      "NOOP",
		FeeSats:   feeSats,
	}

	payload, err := a.account.ChainClients[common.DASHChain].SignTx(tx)
	if err != nil {
		return "", fmt.Errorf("failed to sign %s tx: %w", scenario, err)
	}

	txID, err := a.account.ChainClients[common.DASHChain].BroadcastTx(payload)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast %s tx: %w", scenario, err)
	}

	a.Log().Info().
		Str("scenario", scenario).
		Str("txid", txID).
		Uint64("fee_sats", feeSats).
		Str("amount_dash", fmt.Sprintf("%.8f", float64(amount)/float64(common.One))).
		Msg("broadcasted DASH tx")

	return txID, nil
}
