package features

import (
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

// waitForReadiness waits for local Bifrost admission before acquiring an account.
// The actor retries this operation once per second, for at most two minutes.
func (a *DashInstantSendActor) waitForReadiness(config *OpConfig) OpResult {
	if config == nil || len(config.NodeUsers) == 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend readiness requires a node THORChain bridge")}
	}
	node := config.NodeUsers[0]
	if node == nil || node.Thorchain == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend readiness requires a node THORChain bridge")}
	}
	// The per-key endpoint includes mocknet environment defaults; the bulk endpoint does not.
	a.getMimir = node.Thorchain.GetMimir
	if a.readyDeadline.IsZero() {
		a.readyDeadline = time.Now().Add(2 * time.Minute)
	}
	if !time.Now().Before(a.readyDeadline) {
		return a.readinessTimeout()
	}
	// GetMimir has no cancellation parameter. Keep only one check in flight and
	// bound the wait even if that bridge call stalls. No account is held here.
	result := make(chan OpResult, 1)
	go func() {
		retry, err := a.checkReadiness(dashInstantSendSmallPayment, dashInstantSendLargePayment)
		result <- OpResult{Continue: err == nil, Finish: err != nil && !retry, Error: err}
	}()
	timer := time.NewTimer(time.Until(a.readyDeadline))
	defer timer.Stop()
	select {
	case check := <-result:
		a.readyError = check.Error
		return check
	case <-timer.C:
		return a.readinessTimeout()
	}
}

// readinessTimeout retains the last completed check's failure for diagnosis.
func (a *DashInstantSendActor) readinessTimeout() OpResult {
	if a.readyError != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("timed out waiting for InstantSend readiness: %w", a.readyError)}
	}
	return OpResult{Finish: true, Error: fmt.Errorf("timed out waiting for InstantSend readiness: Mimir or metrics lookup did not finish")}
}

// checkReadiness validates the payment range and local Bifrost allowance.
// Only lookup errors and temporary admission states are retryable during startup.
func (a *DashInstantSendActor) checkReadiness(minPayment, maxPayment uint64) (bool, error) {
	if a.getMimir == nil {
		return false, fmt.Errorf("InstantSend Mimir reader unavailable")
	}
	enabled, err := a.getMimir("DashInstantSendEnabled")
	if err != nil {
		return true, fmt.Errorf("read InstantSend enabled setting: %w", err)
	}
	if enabled == 0 {
		return false, fmt.Errorf("DashInstantSendEnabled explicitly disables InstantSend")
	}
	// An unset enabled Mimir retains the mocknet Bifrost configuration, which enables InstantSend.
	minAmount, err := a.getMimir("DashInstantSendMinAmount")
	if err != nil {
		return true, fmt.Errorf("read InstantSend minimum amount: %w", err)
	}
	if minAmount < 0 {
		minAmount = common.One / 100 // client default: 0.01 DASH
	}
	if uint64(minAmount) > minPayment {
		return false, fmt.Errorf("DashInstantSendMinAmount=%d exceeds payment %d duffs", minAmount, minPayment)
	}
	maxAmount, err := a.getMimir("DashInstantSendMaxAmount")
	if err != nil {
		return true, fmt.Errorf("read InstantSend maximum amount: %w", err)
	}
	if maxAmount <= 0 {
		return false, fmt.Errorf("DashInstantSendMaxAmount must be positive (got %d)", maxAmount)
	}
	if uint64(maxAmount) < maxPayment {
		return false, fmt.Errorf("DashInstantSendMaxAmount=%d cannot cover payment %d duffs", maxAmount, maxPayment)
	}
	state, err := a.readMetrics()
	if err != nil {
		return true, fmt.Errorf("read InstantSend metrics: %w", err)
	}
	if state.warmingUp {
		return true, fmt.Errorf("InstantSend allowance is warming up")
	}
	if state.available < float64(maxPayment) {
		return true, fmt.Errorf("InstantSend available allowance %.0f is below payment %d duffs", state.available, maxPayment)
	}
	return false, nil
}
