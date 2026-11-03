package signer

import (
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type xmrKeygenRequest struct {
	height int64
	keygen ttypes.Keygen
	retry  bool
}

// xmrKeygenSlot retains at most one active ceremony and the newest replacement.
// The high-water mark rejects old retries, but is not a completion cache: the
// same ceremony may run sequentially to resubmit a result not accepted on chain.
type xmrKeygenSlot struct {
	mu           sync.Mutex
	active       *xmrKeygenRequest
	pending      *xmrKeygenRequest
	latestHeight int64
	latestID     common.TxID
}

// admit returns work only to the execution owner. Duplicate/stale contenders
// return immediately; newer work replaces the single pending slot. For distinct
// IDs at the same height, the first admitted ID wins, including after it exits.
func (s *xmrKeygenSlot) admit(height int64, keygen ttypes.Keygen, retry bool) (*xmrKeygenRequest, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if height < s.latestHeight {
		return nil, "stale"
	}
	if height == s.latestHeight && !s.latestID.IsEmpty() && !keygen.ID.Equals(s.latestID) {
		return nil, "different ceremony at same height"
	}
	if s.active != nil && height == s.active.height {
		return nil, "duplicate active ceremony"
	}
	if s.pending != nil && height <= s.pending.height {
		return nil, "duplicate or superseded pending ceremony"
	}
	// Own the member slice because pending work outlives the delivering call.
	keygen.Members = append([]string(nil), keygen.Members...)
	request := &xmrKeygenRequest{height: height, keygen: keygen, retry: retry}
	if s.active != nil {
		s.pending = request
		return nil, "retained newest replacement"
	}
	s.active = request
	s.latestHeight, s.latestID = height, keygen.ID
	return request, "admitted"
}

// finishAndPromote transfers ownership under one lock. No contender can acquire
// an empty slot between finishing the old ceremony and starting its replacement.
// The owning goroutine runs promoted work, or transfers it on a policy error.
func (s *xmrKeygenSlot) finishAndPromote() *xmrKeygenRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active, s.pending = s.pending, nil
	if s.active != nil {
		s.latestHeight, s.latestID = s.active.height, s.active.keygen.ID
	}
	return s.active
}

// keygenStillValid distinguishes expiry from an unreadable acceptance window.
func (s *Signer) keygenStillValid(block ttypes.KeygenBlock, retry bool) (bool, error) {
	var validity int64
	var err error
	if retry {
		var interval int64
		interval, validity, err = s.keygenRetrySettings(block)
		if err == nil && interval <= 0 {
			return false, nil
		}
	} else {
		validity, err = s.keygenValidityBlocks(block)
	}
	if err != nil {
		return false, err
	}
	if validity < 0 {
		return false, nil
	}
	height, err := s.keygenCurrentHeight(block.Height)
	if err != nil {
		return false, err
	}
	return keygenHeightInWindow(height, block.Height, validity), nil
}

// runAdmittedXMRKeygen returns the still-owned request only when policy cannot
// be read. The caller must resume this continuation, never admit it again.
// No slot mutex is held during policy reads, DKG, or result submission.
func (s *Signer) runAdmittedXMRKeygen(current *xmrKeygenRequest) *xmrKeygenRequest {
	for ; current != nil; current = s.xmrKeygen.finishAndPromote() {
		if s.isStopped() {
			continue
		}
		allowed, err := s.xmrMonovaultKeygenAllowed(current.height)
		block := singleKeygenBlock(current.height, current.keygen)
		// Catch-up can deliver an already-expired ceremony for the first time.
		// Check every admission, not just promoted or policy-retained work,
		// before DKG can occupy the serial consumer ahead of an Asgard keygen.
		if err == nil && allowed {
			allowed, err = s.keygenStillValid(block, current.retry)
		}
		if err != nil {
			s.logger.Warn().Err(err).Int64("keygen_height", current.height).
				Msg("retaining admitted XMR keygen while policy is unavailable")
			return current // Ownership handoff: do not finish or promote here.
		}
		if allowed {
			s.runKeygenRequest(block, current.keygen)
		}
	}
	return nil
}

// xmrKeygenRecheckInterval is how often a retained XMR keygen rechecks its
// policy. A variable so tests can skip the real waiting.
var xmrKeygenRecheckInterval = constants.ThorchainBlockTime

// retainXMRKeygen uses one worker for repeated errors and promoted requests.
// Initial work does not require retries to be enabled; scheduled retries do.
func (s *Signer) retainXMRKeygen(current *xmrKeygenRequest) {
	ticker := time.NewTicker(xmrKeygenRecheckInterval)
	defer ticker.Stop()
	for current != nil {
		select {
		case <-s.stopChan:
		case <-ticker.C:
		}
		current = s.runAdmittedXMRKeygen(current)
	}
}
