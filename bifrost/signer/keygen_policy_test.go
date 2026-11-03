package signer

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// An outage before the retry worker exists must not discard the retry intent.
func TestKeygenRetryRetainsInitialLookupFailure(t *testing.T) {
	signer, bridge, _ := replacementTestSigner(t, nil)
	bridge.mimirErrs = map[string]error{
		constants.ChurnRetryInterval.String(): errors.New("temporary policy outage"),
	}
	block := newSingleFlightXMRKeygen(t, 2700)
	if !signer.scheduleKeygenRetry(block, block.Keygens[0]) {
		t.Fatal("temporary lookup failure discarded the retry before a worker existed")
	}
}

func (b *singleFlightKeygenBridge) setLookupFailure(key string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key == "height" {
		b.heightErr = err
		return
	}
	if b.mimirErrs == nil {
		b.mimirErrs = make(map[string]error)
	}
	b.mimirErrs[key] = err
}

// Exercise the admitted continuation directly: repeated failures must return
// the same owner, then recovery either executes once or retires known policy.
func TestXMRKeygenPolicyContinuation(t *testing.T) {
	for _, lookup := range []string{constants.MimirKeyEnableMonovaultKeygen, constants.MimirKeyHaltMonovaultKeygen, "height", constants.ChurnRetryInterval.String()} {
		for _, outcome := range []string{"resume", "disabled", "halted", "expired", "shutdown"} {
			t.Run(lookup+"/"+outcome, func(t *testing.T) {
				signer, bridge, client := replacementTestSigner(t, nil)
				client.releaseFirst()
				bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, 4000)
				block := newSingleFlightXMRKeygen(t, 2700)
				current, _ := signer.xmrKeygen.admit(block.Height, block.Keygens[0], false)
				// Initial work must check validity, but must not depend
				// on KeygenRetryInterval (which remains disabled in this test).
				bridge.setLookupFailure(lookup, errors.New("temporary lookup failure"))
				for tick := 0; tick < 3; tick++ {
					if retained := signer.runAdmittedXMRKeygen(current); retained != current {
						t.Fatal("unknown policy lost or replaced the admitted owner")
					}
					if client.callCount() != 0 {
						t.Fatal("unknown policy started DKG")
					}
				}
				bridge.setLookupFailure(lookup, nil)
				switch outcome {
				case "disabled":
					bridge.setMimir(constants.MimirKeyEnableMonovaultKeygen, 0)
				case "halted":
					bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, 2800)
				case "expired":
					bridge.setHeight(3701)
				case "shutdown":
					close(signer.stopChan)
				}
				if signer.runAdmittedXMRKeygen(current) != nil || signer.xmrKeygen.active != nil {
					t.Fatal("known policy or shutdown did not release ownership")
				}
				want := 0
				if outcome == "resume" {
					want = 1
				}
				if client.callCount() != want || bridge.broadcastCount() != want {
					t.Fatalf("DKG/broadcast counts %d/%d, want %d", client.callCount(), bridge.broadcastCount(), want)
				}
			})
		}
	}
}

func TestXMRKeygenPolicyWorkerHandoff(t *testing.T) {
	// Registered before the signer's cleanup, so it runs after the workers stop.
	recheck := xmrKeygenRecheckInterval
	xmrKeygenRecheckInterval = 10 * time.Millisecond
	t.Cleanup(func() { xmrKeygenRecheckInterval = recheck })
	signer, bridge, client := replacementTestSigner(t, nil)
	signer.tssKeygen = &tss.KeyGen{}
	block := newSingleFlightXMRKeygen(t, 2700)
	block.Keygens = append(block.Keygens, ttypes.Keygen{Type: ttypes.KeygenType_AsgardKeygen})
	bridge.setLookupFailure(constants.MimirKeyEnableMonovaultKeygen, errors.New("API unavailable"))
	delivered := make(chan struct{})
	go func() {
		defer close(delivered)
		signer.processKeygenBlock(block)
	}()
	waitForSingleFlightReturn(t, delivered, "consumer while XMR policy is unavailable")
	// The empty Asgard key returns through ordinary retry scheduling, proving
	// that it ran while XMR was retained, without accessing any XMR gate itself.
	if bridge.mimirReadCount(constants.ChurnRetryInterval.String()) != 1 || client.callCount() != 0 {
		t.Fatal("policy wait blocked the same-block Asgard keygen")
	}
	for i := 0; i < 5; i++ {
		signer.processKeygenRequest(block, block.Keygens[0])
	}
	pending := newSingleFlightXMRKeygen(t, 2701)
	signer.processKeygenBlock(pending)
	bridge.setLookupFailure(constants.MimirKeyEnableMonovaultKeygen, nil)
	// The worker resumes on its recheck interval, well inside one block time.
	select {
	case <-client.firstStarted:
	case <-time.After(constants.ThorchainBlockTime / 2):
		t.Fatal("retained worker did not resume its admitted request on its recheck interval")
	}
	client.releaseFirst()
	finished := make(chan struct{})
	go func() { signer.keygenWorkerWG.Wait(); close(finished) }()
	waitForSingleFlightReturn(t, finished, "worker completion and promotion")
	if got := client.calledHeights(); !slices.Equal(got, []int64{2700, 2701}) {
		t.Fatalf("handoff/promotion executed %v, want [2700 2701]", got)
	}
	if signer.xmrKeygen.active != nil || signer.xmrKeygen.pending != nil || bridge.broadcastCount() != 2 {
		t.Fatal("handoff did not finish each admitted attempt exactly once")
	}
	// No completion cache: a later sequential attempt may resubmit its result.
	signer.processKeygenBlock(pending)
	if client.callCount() != 3 {
		t.Fatal("worker permanently suppressed a sequential resubmission")
	}
}

func TestXMRKeygenRetainedRetryRechecksRetryPolicy(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	client.releaseFirst()
	block := newSingleFlightXMRKeygen(t, 2700)
	current, _ := signer.xmrKeygen.admit(block.Height, block.Keygens[0], true)
	bridge.setLookupFailure(constants.MimirKeyEnableMonovaultKeygen, errors.New("API unavailable"))
	if signer.runAdmittedXMRKeygen(current) != current {
		t.Fatal("lost retry ownership")
	}
	bridge.setLookupFailure(constants.MimirKeyEnableMonovaultKeygen, nil)
	// An initial request may run with retries disabled; this scheduled retry may not.
	if signer.runAdmittedXMRKeygen(current) != nil || client.callCount() != 0 {
		t.Fatal("retained retry ignored disabled retry policy")
	}
}

func TestXMRKeygenPromotedPolicyFailure(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	client.releaseFirst()
	active := newSingleFlightXMRKeygen(t, 2700)
	pending := newSingleFlightXMRKeygen(t, 2701)
	signer.xmrKeygen.admit(active.Height, active.Keygens[0], false)
	signer.xmrKeygen.admit(pending.Height, pending.Keygens[0], false)
	current := signer.xmrKeygen.finishAndPromote()
	bridge.setLookupFailure(constants.ChurnRetryInterval.String(), errors.New("temporary validity outage"))
	if signer.runAdmittedXMRKeygen(current) != current || client.callCount() != 0 {
		t.Fatal("promoted replacement was dropped or executed under unknown policy")
	}
	bridge.setLookupFailure(constants.ChurnRetryInterval.String(), nil)
	if signer.runAdmittedXMRKeygen(current) != nil || !slices.Equal(client.calledHeights(), []int64{2701}) {
		t.Fatal("promoted replacement did not resume exactly once")
	}
}

func TestXMRKeygenPolicyWorkerShutdown(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	bridge.setLookupFailure(constants.MimirKeyEnableMonovaultKeygen, errors.New("API unavailable"))
	signer.processKeygenBlock(newSingleFlightXMRKeygen(t, 2700))
	signer.processKeygenBlock(newSingleFlightXMRKeygen(t, 2701))
	close(signer.stopChan)
	signer.keygenWorkerWG.Wait()
	if signer.xmrKeygen.active != nil || signer.xmrKeygen.pending != nil || client.callCount() != 0 {
		t.Fatal("shutdown did not retire all retained work without DKG")
	}
	if signer.startKeygenWorker(func() { t.Error("worker started after shutdown") }) {
		t.Fatal("shutdown accepted new worker")
	}
}

// Only one worker may wait for a given ceremony, even when its initial policy
// lookup fails. Once readable, it uses the same grid as a healthy peer.
func TestKeygenRetryInitialFailureDeduplicatesAndRendezvous(t *testing.T) {
	for _, kind := range []ttypes.KeygenType{ttypes.KeygenType_AsgardKeygen, ttypes.KeygenType_XMRMonovaultKeygen} {
		for _, lookup := range []string{constants.ChurnRetryInterval.String(), constants.KeygenRetryInterval.String(), "height"} {
			t.Run(kind.String()+"/"+lookup, func(t *testing.T) {
				signer, bridge, _ := replacementTestSigner(t, nil)
				block := singleKeygenBlock(2700, ttypes.Keygen{Type: kind})
				bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
				bridge.setLookupFailure(lookup, errors.New("temporary API outage"))
				var wg sync.WaitGroup
				for i := 0; i < 10; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if !signer.scheduleKeygenRetry(block, block.Keygens[0]) {
							t.Error("retry intent lost")
						}
					}()
				}
				wg.Wait()
				if got := bridge.mimirReadCount(constants.ChurnRetryInterval.String()); got != 1 {
					t.Fatalf("duplicate scheduling performed %d policy reads", got)
				}
				bridge.setLookupFailure(lookup, nil)
				// Check the same step used by the waiting worker without advancing
				// its real ticker or racing its owned targetHeight.
				retained := &keygenRetry{block: block, keygen: block.Keygens[0]}
				bridge.setHeight(2817)
				if done, err := signer.prepareKeygenRetry(retained); done || err != nil {
					t.Fatalf("recovery: done=%t err=%v", done, err)
				}
				if retained.targetHeight != 2900 {
					t.Fatalf("target %d, want shared retry height 2900", retained.targetHeight)
				}
				if bridge.mimirReadCount(constants.MimirKeyEnableMonovaultKeygen) != 0 {
					t.Fatal("scheduling used XMR gate")
				}
				close(signer.stopChan)
				signer.keygenWorkerWG.Wait()
				if _, ok := signer.keygenRetries.Load(retryID(block, block.Keygens[0])); ok {
					t.Fatal("shutdown leaked retry claim")
				}
			})
		}
	}
}

type scannerPolicyKeygenBridge struct{ *singleFlightKeygenBridge }

func (b *scannerPolicyKeygenBridge) GetMimirWithRef(template, ref string) (int64, error) {
	return b.GetMimir(fmt.Sprintf(template, ref))
}

func TestXMRScannerPolicyDoesNotControlKeygen(t *testing.T) {
	for _, scenario := range []string{"scanner halted", "scanner policy unreadable", "keygen disabled"} {
		t.Run(scenario, func(t *testing.T) {
			signer, bridge, client := replacementTestSigner(t, nil)
			signer.thorchainBridge = &scannerPolicyKeygenBridge{bridge}
			client.releaseFirst()
			bridge.setMimir(constants.MimirKeyEnableMonovaultKeygen, 1)
			bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, 0)
			bridge.setMimir("HaltScannerXMR", 1)
			wantKeygens := 1
			wantPaused := true
			if scenario == "scanner policy unreadable" {
				bridge.setLookupFailure("HaltScannerXMR", errors.New("scanner policy unavailable"))
			}
			if scenario == "keygen disabled" {
				bridge.setMimir("HaltScannerXMR", 0)
				bridge.setMimir(constants.MimirKeyEnableMonovaultKeygen, 0)
				wantKeygens, wantPaused = 0, false
			}
			if paused := blockscanner.IsXMRScannerPaused(zerolog.Nop(), bridge); paused != wantPaused {
				t.Fatalf("scanner paused=%t, want %t", paused, wantPaused)
			}
			reads := bridge.mimirReadCount("HaltScannerXMR")
			signer.processKeygenBlock(newSingleFlightXMRKeygen(t, 2700))
			if client.callCount() != wantKeygens || bridge.broadcastCount() != wantKeygens {
				t.Fatalf("keygen execution/broadcast=%d/%d, want %d", client.callCount(), bridge.broadcastCount(), wantKeygens)
			}
			if bridge.mimirReadCount("HaltScannerXMR") != reads {
				t.Fatal("keygen consulted economic scanner policy")
			}
		})
	}
}
