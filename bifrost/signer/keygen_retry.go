package signer

import (
	"fmt"
	"math"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// keygenRetryValidityBlocks mirrors THORNode's keygenResultValidityBlocks and
// keygenResultValidityBlocksForBlock. Zero disables retries, negative uses the
// default, and XMR (including its same-block Asgard companion) gets six minutes.
// Keep this Bifrost mirror aligned when THORNode changes its acceptance window.
func keygenRetryValidityBlocks(churnRetryInterval int64, block ttypes.KeygenBlock) int64 {
	if churnRetryInterval == 0 {
		return 0
	}
	if churnRetryInterval < 0 {
		churnRetryInterval = constants.NewConstantValue().GetInt64Value(constants.ChurnRetryInterval)
	}
	for _, keygen := range block.Keygens {
		if keygen.Type == ttypes.KeygenType_XMRMonovaultKeygen {
			minimum := int64((6*time.Minute + constants.ThorchainBlockTime - 1) / constants.ThorchainBlockTime)
			churnRetryInterval = max(churnRetryInterval, minimum)
			break
		}
	}
	// The window ends one block before the retry tick — mirror of THORNode's
	// keygenResultValidityBlocks. A keygen scheduled at H is valid for ages
	// 0..N-1 and expires at age N, the same block its churn retry fires, so a
	// stale result is rejected on the block the retry proceeds and cannot
	// double-trigger a live keygen. Returning the full interval kept the keygen
	// valid AT the first tick, so the retry was suppressed and the effective
	// interval doubled (the off-by-one). Subtracting one here keeps Bifrost's
	// acceptance window identical to THORNode's.
	return churnRetryInterval - 1
}

func (s *Signer) keygenValidityBlocks(block ttypes.KeygenBlock) (int64, error) {
	value, err := s.thorchainBridge.GetMimir(constants.ChurnRetryInterval.String())
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get churn retry mimir")
		return 0, err
	}
	return keygenRetryValidityBlocks(value, block), nil
}

// keygenRetrySettings is shared by scheduling and firing, so a delayed retry
// cannot keep using an obsolete Mimir snapshot or a disabled retry policy.
// A zero interval with no error means retries are disabled by known settings;
// lookup errors are separate so an already-waiting retry can poll again.
func (s *Signer) keygenRetrySettings(block ttypes.KeygenBlock) (interval, validity int64, err error) {
	validity, err = s.keygenValidityBlocks(block)
	if err != nil || validity <= 0 {
		return 0, 0, err
	}
	interval, err = s.thorchainBridge.GetMimir(constants.KeygenRetryInterval.String())
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get keygen retries mimir")
		return 0, 0, err
	}
	if interval <= 0 || interval > math.MaxInt64/int64(constants.ThorchainBlockTime) {
		return 0, 0, nil
	}
	retryDuration := time.Duration(interval) * constants.ThorchainBlockTime
	if retryDuration <= s.cfg.Signer.KeygenTimeout*3/2 {
		s.logger.Error().Dur("retry_interval", retryDuration).
			Dur("keygen_timeout", s.cfg.Signer.KeygenTimeout).Msg("retry interval too short")
		return 0, 0, nil
	}
	return interval, validity, nil
}

func keygenHeightInWindow(height, keygenHeight, validity int64) bool {
	// THORNode accepts the boundary inclusively, including the delivery
	// block when validity is zero. Retry settings separately disable retries.
	return validity >= 0 && keygenHeight > 0 && height >= keygenHeight && height-keygenHeight <= validity
}

// keygenCurrentHeight requires an endpoint at least as current as the delivered
// request. A lagging/cached response cannot prove expiry or that a height-based
// halt is still in the future. Treat it like a lookup failure and retain work.
func (s *Signer) keygenCurrentHeight(keygenHeight int64) (int64, error) {
	height, err := s.thorchainBridge.GetBlockHeight()
	if err == nil && (height <= 0 || height < keygenHeight) {
		err = fmt.Errorf("THORChain height %d is not current for keygen height %d", height, keygenHeight)
	}
	return height, err
}

// startKeygenWorker serializes WaitGroup admission with Stop, without delaying
// the normal consumer on a background policy wait.
func (s *Signer) startKeygenWorker(work func()) bool {
	s.keygenWorkerMu.Lock()
	defer s.keygenWorkerMu.Unlock()
	if s.isStopped() {
		return false
	}
	s.keygenWorkerWG.Add(1)
	go func() {
		defer s.keygenWorkerWG.Done()
		work()
	}()
	return true
}

type keygenRetryID struct {
	height int64
	id     common.TxID
	kind   ttypes.KeygenType
}

type keygenRetry struct {
	block        ttypes.KeygenBlock
	keygen       ttypes.Keygen
	targetHeight int64
}

func retryID(block ttypes.KeygenBlock, keygen ttypes.Keygen) keygenRetryID {
	return keygenRetryID{height: block.Height, id: keygen.ID, kind: keygen.Type}
}

// prepareKeygenRetry leaves targetHeight unset on lookup failure. Once policy
// recovers, choose the next shared height-grid boundary, retaining one interval
// of completion headroom. Unknown policy is not a confirmed disabled policy.
func (s *Signer) prepareKeygenRetry(retry *keygenRetry) (finished bool, err error) {
	interval, validity, err := s.keygenRetrySettings(retry.block)
	if err != nil {
		return false, err
	}
	if interval <= 0 || s.isStopped() {
		return true, nil
	}
	height, err := s.keygenCurrentHeight(retry.block.Height)
	if err != nil {
		return false, err
	}
	elapsed := height - retry.block.Height
	delay := interval - elapsed%interval
	if elapsed > validity-interval || delay > validity-interval-elapsed || height > math.MaxInt64-delay {
		return true, nil
	}
	retry.targetHeight = height + delay
	return false, nil
}

// tryKeygenRetry returns whether the waiting worker is finished. Height and
// Mimir lookup failures remain pollable; a known disabled/expired policy retires
// the retry without executing keygen.
// Keep the original block for the Asgard/XMR shared validity window, but execute
// only the request that failed instead of replaying its successful companions.
func (s *Signer) tryKeygenRetry(retry *keygenRetry) bool {
	block, keygen := retry.block, retry.keygen
	if s.isStopped() {
		return true
	}
	height, err := s.keygenCurrentHeight(block.Height)
	if err != nil {
		s.logger.Error().Err(err).Msg("fail to get last chain height")
		return false
	}
	if height < retry.targetHeight {
		return false
	}
	interval, validity, err := s.keygenRetrySettings(block)
	if err != nil {
		// Fail closed for execution, not for retry ownership. The same worker
		// will re-read policy on its next block tick, without spawning work.
		return false
	}
	if interval <= 0 || !keygenHeightInWindow(height, block.Height, validity) {
		s.logger.Warn().Int64("keygen_height", block.Height).Int64("current_height", height).
			Msg("skipping disabled or expired keygen retry")
		return true
	}
	s.logger.Info().Int64("keygen_height", block.Height).Int64("current_height", height).Msg("retrying keygen")
	// Only this token's owner may execute. Release its waiting claim before
	// execution so failure can schedule a successor; neither a stale firing
	// worker nor its deferred cleanup may remove that successor's claim.
	if !s.keygenRetries.CompareAndDelete(retryID(block, keygen), retry) {
		return true
	}
	if keygen.Type == ttypes.KeygenType_XMRMonovaultKeygen {
		s.processXMRKeygenRequest(block, keygen, true)
	} else {
		s.processKeygenRequest(block, keygen)
	}
	return true
}
