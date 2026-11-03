package signer

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func replacementTestSigner(t *testing.T, firstErr error) (*Signer, *singleFlightKeygenBridge, *controlledXMRKeygenClient) {
	t.Helper()
	result := chainclienttypes.CustomKeygenResult{PoolPubKeyFrost: ttypes.GetRandomPubKey()}
	client := &controlledXMRKeygenClient{
		MockChainClient: &MockChainClient{},
		firstStarted:    make(chan struct{}),
		firstRelease:    make(chan struct{}),
		results:         []chainclienttypes.CustomKeygenResult{result, result, result},
		errs:            []error{firstErr, nil, nil},
	}
	signer, bridge := newSingleFlightTestSigner(client)
	bridge.setHeight(2800)
	t.Cleanup(func() {
		if !signer.isStopped() {
			close(signer.stopChan)
		}
		signer.keygenWorkerWG.Wait()
	})
	t.Cleanup(client.releaseFirst)
	return signer, bridge, client
}

// registerTestKeygenRetry represents a waiting worker without starting a timer.
// Tests can advance its firing step deterministically while preserving ownership.
func registerTestKeygenRetry(signer *Signer, block ttypes.KeygenBlock, keygen ttypes.Keygen, target int64) *keygenRetry {
	retry := &keygenRetry{block: block, keygen: keygen, targetHeight: target}
	signer.keygenRetries.Store(retryID(block, keygen), retry)
	return retry
}

// The normal consumer can deliver a replacement while a retry owns the DKG.
// It must return promptly, retain just the newest ceremony, then execute it
// exactly once after the active attempt returns on success or failure.
func TestSignerXMRKeygenPromotesNewestReplacement(t *testing.T) {
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "retry scheduled"}[failFirst], func(t *testing.T) {
			var firstErr error
			if failFirst {
				firstErr = errors.New("DKG failed")
			}
			signer, bridge, client := replacementTestSigner(t, firstErr)
			bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
			active := newSingleFlightXMRKeygen(t, 2700)
			pending := newSingleFlightXMRKeygen(t, 2701)
			newest := newSingleFlightXMRKeygen(t, 2702)
			sameHeight := newSingleFlightXMRKeygen(t, 2702)
			old := newSingleFlightXMRKeygen(t, 2699)
			done := make(chan struct{})
			go func() {
				defer close(done)
				signer.processKeygenRequest(active, active.Keygens[0])
			}()
			waitForSingleFlightReturn(t, client.firstStarted, "active DKG")
			contenderDone := make(chan struct{})
			go func() {
				defer close(contenderDone)
				for _, block := range []ttypes.KeygenBlock{active, pending, newest, sameHeight, old, pending, newest} {
					signer.processKeygenBlock(block)
				}
			}()
			waitForSingleFlightReturn(t, contenderDone, "replacement admission")
			if got := client.callCount(); got != 1 {
				t.Fatalf("overlap started %d DKGs, want 1", got)
			}
			client.releaseFirst()
			waitForSingleFlightReturn(t, done, "active completion and replacement promotion")
			if got := client.calledHeights(); !slices.Equal(got, []int64{2700, 2702}) {
				t.Fatalf("executed heights %v, want [2700 2702]", got)
			}
			if got := bridge.mimirReadCount(constants.MimirKeyEnableMonovaultKeygen); got != 2 {
				t.Fatalf("execution gate read %d times, want 2", got)
			}
			// A delayed old retry and a distinct same-height ID cannot take over
			// after the replacement exits. The same ID can still resubmit.
			signer.tryKeygenRetry(registerTestKeygenRetry(signer, active, active.Keygens[0], 2800))
			signer.processKeygenBlock(sameHeight)
			signer.processKeygenBlock(newest)
			if got := client.calledHeights(); !slices.Equal(got, []int64{2700, 2702, 2702}) {
				t.Fatalf("retry sequence %v, want [2700 2702 2702]", got)
			}
		})
	}
}

func TestSignerXMRKeygenPromotionRechecksGateAndExpiry(t *testing.T) {
	for _, cause := range []string{"halt", "disable", "expired", "zero window", "shutdown"} {
		t.Run(cause, func(t *testing.T) {
			signer, bridge, client := replacementTestSigner(t, nil)
			active := newSingleFlightXMRKeygen(t, 2700)
			pending := newSingleFlightXMRKeygen(t, 2701)
			done := make(chan struct{})
			go func() {
				defer close(done)
				signer.processKeygenBlock(active)
			}()
			waitForSingleFlightReturn(t, client.firstStarted, "active DKG")
			signer.processKeygenBlock(pending)
			switch cause {
			case "halt":
				bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, 2800)
			case "disable":
				bridge.setMimir(constants.MimirKeyEnableMonovaultKeygen, 0)
			case "expired":
				bridge.setHeight(3800)
			case "zero window":
				bridge.setMimir(constants.ChurnRetryInterval.String(), 0)
			case "shutdown":
				close(signer.stopChan)
			}
			client.releaseFirst()
			waitForSingleFlightReturn(t, done, "pending rejection")
			if got := client.callCount(); got != 1 {
				t.Fatalf("rejected pending DKG ran: %d hook calls", got)
			}
			if signer.xmrKeygen.active != nil || signer.xmrKeygen.pending != nil {
				t.Fatal("rejected replacement retained ownership")
			}
			if cause == "halt" || cause == "disable" {
				bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, 0)
				bridge.setMimir(constants.MimirKeyEnableMonovaultKeygen, 1)
				signer.processKeygenBlock(pending)
				if got := client.callCount(); got != 2 {
					t.Fatal("reopened gate did not admit later sequential attempt")
				}
			}
		})
	}
}

func TestSignerXMRKeygenConcurrentReplacements(t *testing.T) {
	signer, _, client := replacementTestSigner(t, nil)
	active := newSingleFlightXMRKeygen(t, 2700)
	done := make(chan struct{})
	go func() {
		defer close(done)
		signer.processKeygenBlock(active)
	}()
	waitForSingleFlightReturn(t, client.firstStarted, "active DKG")
	var contenders sync.WaitGroup
	for height := int64(2701); height <= 2732; height++ {
		block := newSingleFlightXMRKeygen(t, height)
		contenders.Add(1)
		go func() {
			defer contenders.Done()
			signer.processKeygenBlock(block)
		}()
	}
	contenders.Wait()
	client.releaseFirst()
	waitForSingleFlightReturn(t, done, "newest concurrent replacement")
	if got := client.calledHeights(); !slices.Equal(got, []int64{2700, 2732}) {
		t.Fatalf("executed heights %v, want [2700 2732]", got)
	}
}

// Schedule with an open policy, then change it before the retry becomes due.
// Calling the worker's firing step directly makes delayed-height and Mimir
// races deterministic without sleeping for a chain block or adding a timer seam.
func TestKeygenRetryRevalidatesAtFire(t *testing.T) {
	for _, test := range []struct {
		name     string
		height   int64
		churn    int64
		interval int64
		wantRuns int
	}{
		{"last valid before tick", 3699, 1000, 100, 1},
		{"expired at retry tick", 3700, 1000, 100, 0},
		{"churn disabled", 2800, 0, 100, 0},
		{"keygen retry disabled", 2800, 1000, 0, 0},
		{"window shrunk", 3100, 1, 100, 0},
		{"negative uses default", 2800, -1, 100, 1},
		{"interval too short", 2800, 1000, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			signer, bridge, client := replacementTestSigner(t, nil)
			// Both one-second mocknet blocks and six-second mainnet blocks
			// allow interval 100 and reject interval 1 at this test timeout.
			signer.cfg.Signer.KeygenTimeout = 10 * time.Second
			client.releaseFirst()
			block := newSingleFlightXMRKeygen(t, 2700)
			bridge.setHeight(2700)
			bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
			if !signer.scheduleKeygenRetry(block, block.Keygens[0]) {
				t.Fatal("initial open policy did not schedule retry")
			}
			value, _ := signer.keygenRetries.Load(retryID(block, block.Keygens[0]))
			retry, ok := value.(*keygenRetry)
			if !ok {
				t.Fatal("scheduled retry has no owning token")
			}
			bridge.setMimir(constants.ChurnRetryInterval.String(), test.churn)
			bridge.setMimir(constants.KeygenRetryInterval.String(), test.interval)
			bridge.setHeight(test.height)
			if !signer.tryKeygenRetry(retry) {
				t.Fatal("due retry did not retire or execute")
			}
			if got := client.callCount(); got != test.wantRuns {
				t.Fatalf("hook calls %d, want %d", got, test.wantRuns)
			}
		})
	}
}

func TestXMRKeygenSlotOwnsPendingMembers(t *testing.T) {
	var slot xmrKeygenSlot
	active, _ := slot.admit(1, ttypes.Keygen{ID: common.TxID("active")}, false)
	if active == nil {
		t.Fatal("initial admission failed")
	}
	members := []string{"member"}
	slot.admit(2, ttypes.Keygen{ID: common.TxID("pending"), Members: members}, false)
	members[0] = "mutated by caller"
	promoted := slot.finishAndPromote()
	if promoted == nil || promoted.keygen.Members[0] != "member" {
		t.Fatal("pending request did not own its member slice")
	}
}

// Race a finishing owner against a newer arrival while a replacement is
// already retained. Admission must never observe an empty slot and create a
// second owner during the handoff.
func TestXMRKeygenSlotAtomicPromotion(t *testing.T) {
	for range 100 {
		var slot xmrKeygenSlot
		slot.admit(1, ttypes.Keygen{ID: common.TxID("active")}, false)
		slot.admit(2, ttypes.Keygen{ID: common.TxID("pending")}, false)
		start := make(chan struct{})
		promoted := make(chan *xmrKeygenRequest, 1)
		contender := make(chan *xmrKeygenRequest, 1)
		go func() {
			<-start
			promoted <- slot.finishAndPromote()
		}()
		go func() {
			<-start
			request, _ := slot.admit(3, ttypes.Keygen{ID: common.TxID("newest")}, false)
			contender <- request
		}()
		close(start)
		current := <-promoted
		if other := <-contender; other != nil {
			t.Fatal("contender became a second execution owner during promotion")
		}
		if current == nil {
			t.Fatal("finishing owner lost retained replacement")
		}
		if current.height == 2 {
			current = slot.finishAndPromote()
		}
		if current == nil || current.height != 3 {
			t.Fatal("newest replacement was not promoted")
		}
		if slot.finishAndPromote() != nil {
			t.Fatal("finished slot retained unexpected work")
		}
	}
}

type blockedKeygenBroadcastBridge struct {
	*singleFlightKeygenBridge
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedKeygenBroadcastBridge) Broadcast(msgs ...sdktypes.Msg) (common.TxID, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.singleFlightKeygenBridge.Broadcast(msgs...)
}

func TestSignerXMRKeygenOwnsSlotThroughBroadcast(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	client.releaseFirst()
	blocked := &blockedKeygenBroadcastBridge{
		singleFlightKeygenBridge: bridge,
		started:                  make(chan struct{}),
		release:                  make(chan struct{}),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(blocked.release) }) }
	t.Cleanup(release)
	signer.thorchainBridge = blocked
	active := newSingleFlightXMRKeygen(t, 2700)
	pending := newSingleFlightXMRKeygen(t, 2701)
	done := make(chan struct{})
	go func() {
		defer close(done)
		signer.processKeygenBlock(active)
	}()
	waitForSingleFlightReturn(t, blocked.started, "active result broadcast")
	signer.processKeygenBlock(pending)
	if got := client.callCount(); got != 1 {
		t.Fatalf("replacement started during result broadcast: %d calls", got)
	}
	release()
	waitForSingleFlightReturn(t, done, "broadcast completion and promotion")
	if got := client.calledHeights(); !slices.Equal(got, []int64{2700, 2701}) {
		t.Fatalf("executed heights %v, want [2700 2701]", got)
	}
}

func TestKeygenRetryFireLookupFailuresAndShutdown(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	client.releaseFirst()
	block := newSingleFlightXMRKeygen(t, 2700)
	retry := registerTestKeygenRetry(signer, block, block.Keygens[0], 2800)
	bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
	bridge.setHeight(2799)
	if signer.tryKeygenRetry(retry) {
		t.Fatal("retry fired before target height")
	}
	bridge.heightErr = errors.New("height unavailable")
	if signer.tryKeygenRetry(retry) {
		t.Fatal("height lookup failure must remain pollable")
	}
	bridge.heightErr = nil
	bridge.setHeight(2800)
	bridge.mimirErrs = map[string]error{constants.ChurnRetryInterval.String(): errors.New("mimir unavailable")}
	if signer.tryKeygenRetry(retry) {
		t.Fatal("unknown retry window must remain pollable without executing")
	}
	close(signer.stopChan)
	if !signer.tryKeygenRetry(retry) {
		t.Fatal("shutdown must retire retry")
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("ineligible retry executed %d DKGs", got)
	}
}

// An API outage is not a disabled policy. Exercise the worker's existing tick
// step directly so recovery and policy changes need no real timers or DKG peers.
func TestKeygenRetrySurvivesMimirLookupFailure(t *testing.T) {
	for _, keygenType := range []ttypes.KeygenType{ttypes.KeygenType_AsgardKeygen, ttypes.KeygenType_XMRMonovaultKeygen} {
		for _, mimir := range []constants.ConstantName{constants.ChurnRetryInterval, constants.KeygenRetryInterval} {
			for _, outcome := range []string{"resume", "disabled", "expired", "shutdown"} {
				t.Run(keygenType.String()+"/"+mimir.String()+"/"+outcome, func(t *testing.T) {
					signer, bridge, client := replacementTestSigner(t, nil)
					client.releaseFirst()
					signer.cfg.Signer.KeygenTimeout = 10 * time.Second
					block := newSingleFlightXMRKeygen(t, 2700)
					if keygenType == ttypes.KeygenType_AsgardKeygen {
						// Empty membership makes ordinary TSS return an empty key
						// and re-enter retry scheduling without a network ceremony.
						signer.tssKeygen = &tss.KeyGen{}
						block = singleKeygenBlock(2700, ttypes.Keygen{Type: keygenType})
					}
					bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
					retry := registerTestKeygenRetry(signer, block, block.Keygens[0], 2800)
					bridge.mimirErrs = map[string]error{mimir.String(): errors.New("temporary API outage")}
					for tick := 0; tick < 3; tick++ {
						if signer.tryKeygenRetry(retry) {
							t.Fatal("Mimir lookup failure retired the waiting retry")
						}
						if got := client.callCount(); got != 0 {
							t.Fatalf("unknown policy executed %d XMR keygens", got)
						}
					}
					bridge.mimirErrs = nil
					// The last valid height (churn-1) allows execution but leaves
					// no headroom to spawn another retry after the empty Asgard key.
					bridge.setHeight(3699)
					switch outcome {
					case "disabled":
						bridge.setMimir(mimir.String(), 0)
					case "expired":
						bridge.setHeight(3700)
					case "shutdown":
						bridge.mimirErrs = map[string]error{mimir.String(): errors.New("still unavailable")}
						close(signer.stopChan)
					}
					before := bridge.mimirReadCount(constants.ChurnRetryInterval.String())
					if !signer.tryKeygenRetry(retry) {
						t.Fatal("known policy or shutdown did not finish the waiting retry")
					}
					wantRuns, wantReads := 0, 1
					if outcome == "shutdown" {
						wantReads = 0
					} else if outcome == "resume" {
						// One read to fire, then another at XMR admission or
						// Asgard's empty-key failure trying to reschedule.
						wantReads = 2
						if keygenType == ttypes.KeygenType_XMRMonovaultKeygen {
							wantRuns = 1
						}
					}
					if got := client.callCount(); got != wantRuns {
						t.Fatalf("XMR hook calls %d, want %d", got, wantRuns)
					}
					if got := bridge.mimirReadCount(constants.ChurnRetryInterval.String()) - before; got != wantReads {
						t.Fatalf("retry path policy reads %d, want %d", got, wantReads)
					}
				})
			}
		}
	}
}
