package signer

import (
	"errors"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// A response behind an already-delivered keygen is not evidence of expiry.
// Retain initial, promoted, and retry ownership until the endpoint catches up.
func TestXMRKeygenRetainsLaggingHeight(t *testing.T) {
	for _, source := range []string{"initial", "promoted", "retry"} {
		for _, outcome := range []string{"resume", "expired", "halted"} {
			t.Run(source+"/"+outcome, func(t *testing.T) {
				signer, bridge, client := replacementTestSigner(t, nil)
				client.releaseFirst()
				block := newSingleFlightXMRKeygen(t, 2700)
				if source == "retry" {
					bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
				}
				if source == "promoted" {
					old := newSingleFlightXMRKeygen(t, 2690)
					signer.xmrKeygen.admit(old.Height, old.Keygens[0], false)
				}
				current, _ := signer.xmrKeygen.admit(block.Height, block.Keygens[0], source == "retry")
				if source == "promoted" {
					current = signer.xmrKeygen.finishAndPromote()
				}
				for _, height := range []int64{-1, 2699, 2699} {
					bridge.setHeight(height)
					if signer.runAdmittedXMRKeygen(current) != current || client.callCount() != 0 {
						t.Fatalf("height %d dropped or executed retained keygen", height)
					}
				}
				bridge.setHeight(2700)
				switch outcome {
				case "expired":
					bridge.setHeight(3701)
				case "halted":
					bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, 2700)
				}
				if signer.runAdmittedXMRKeygen(current) != nil || signer.xmrKeygen.active != nil {
					t.Fatal("readable policy did not finish the admitted request")
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

// A stale token must neither execute DKG nor erase another worker's claim.
func TestKeygenRetryRequiresOwningToken(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	client.releaseFirst()
	bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
	block := newSingleFlightXMRKeygen(t, 2700)
	old := registerTestKeygenRetry(signer, block, block.Keygens[0], 2800)
	owner := registerTestKeygenRetry(signer, block, block.Keygens[0], 2800)
	if !signer.tryKeygenRetry(old) || client.callCount() != 0 {
		t.Fatal("stale retry token executed DKG")
	}
	if value, _ := signer.keygenRetries.Load(retryID(block, block.Keygens[0])); value != owner {
		t.Fatal("stale retry deleted the current owner's claim")
	}
	if !signer.tryKeygenRetry(owner) || client.callCount() != 1 {
		t.Fatal("owning token failed to execute once")
	}
	if !signer.tryKeygenRetry(owner) || client.callCount() != 1 {
		t.Fatal("consumed token executed again")
	}
}

func TestKeygenRetryCleanupPreservesSuccessor(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, errors.New("first DKG failed"))
	client.releaseFirst()
	bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
	block := newSingleFlightXMRKeygen(t, 2700)
	old := registerTestKeygenRetry(signer, block, block.Keygens[0], 2800)
	if !signer.tryKeygenRetry(old) || client.callCount() != 1 {
		t.Fatal("first retry did not execute")
	}
	id := retryID(block, block.Keygens[0])
	value, ok := signer.keygenRetries.Load(id)
	if !ok || value == old {
		t.Fatal("failed DKG could not register its successor")
	}
	if signer.keygenRetries.CompareAndDelete(id, old) {
		t.Fatal("old cleanup deleted the successor")
	}
	if !signer.tryKeygenRetry(old) || client.callCount() != 1 {
		t.Fatal("old firing token executed after succession")
	}
	if valueAfter, _ := signer.keygenRetries.Load(id); valueAfter != value {
		t.Fatal("old firing token erased successor")
	}
}

func TestXMRKeygenLaggingHeightCannotBypassHalt(t *testing.T) {
	for _, halt := range []int64{2700, 2701} {
		signer, bridge, _ := replacementTestSigner(t, nil)
		bridge.setMimir(constants.MimirKeyHaltMonovaultKeygen, halt)
		bridge.setHeight(2699)
		if allowed, err := signer.xmrMonovaultKeygenAllowed(2700); allowed || err == nil {
			t.Fatalf("lagging height decided halt %d: allowed=%t err=%v", halt, allowed, err)
		}
		bridge.setHeight(2700)
		if allowed, err := signer.xmrMonovaultKeygenAllowed(2700); err != nil || allowed != (halt > 2700) {
			t.Fatalf("current height misclassified halt %d: allowed=%t err=%v", halt, allowed, err)
		}
	}
}

func TestKeygenRetryRetainsLaggingHeight(t *testing.T) {
	for _, kind := range []ttypes.KeygenType{ttypes.KeygenType_AsgardKeygen, ttypes.KeygenType_XMRMonovaultKeygen} {
		t.Run(kind.String(), func(t *testing.T) {
			signer, bridge, _ := replacementTestSigner(t, nil)
			block := singleKeygenBlock(2700, ttypes.Keygen{Type: kind})
			bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
			bridge.setHeight(2699)
			if !signer.scheduleKeygenRetry(block, block.Keygens[0]) {
				t.Fatal("lagging height discarded retry intent")
			}
			// Drive an independent copy of the worker step without sharing its
			// mutable targetHeight with the real waiting worker.
			retry := &keygenRetry{block: block, keygen: block.Keygens[0]}
			for _, height := range []int64{-1, 2699, 2699} {
				bridge.setHeight(height)
				if done, err := signer.prepareKeygenRetry(retry); done || err == nil || retry.targetHeight != 0 {
					t.Fatalf("lagging height %d became terminal: done=%t err=%v", height, done, err)
				}
			}
			bridge.setHeight(2700)
			if done, err := signer.prepareKeygenRetry(retry); done || err != nil || retry.targetHeight != 2800 {
				t.Fatalf("recovered scheduling: done=%t err=%v target=%d", done, err, retry.targetHeight)
			}
		})
	}
}

// Drive the waiting worker's preparation and firing steps through recovery,
// including the real ordinary-TSS failure path, without a timer or network DKG.
func TestKeygenRetryLaggingHeightRecoversOnce(t *testing.T) {
	for _, kind := range []ttypes.KeygenType{ttypes.KeygenType_AsgardKeygen, ttypes.KeygenType_XMRMonovaultKeygen} {
		t.Run(kind.String(), func(t *testing.T) {
			signer, bridge, client := replacementTestSigner(t, nil)
			client.releaseFirst()
			block := newSingleFlightXMRKeygen(t, 2700)
			if kind == ttypes.KeygenType_AsgardKeygen {
				signer.tssKeygen = &tss.KeyGen{}
				block = singleKeygenBlock(2700, ttypes.Keygen{Type: kind})
			}
			bridge.setMimir(constants.KeygenRetryInterval.String(), 100)
			retry := registerTestKeygenRetry(signer, block, block.Keygens[0], 0)
			bridge.setHeight(2699)
			if done, err := signer.prepareKeygenRetry(retry); done || err == nil {
				t.Fatal("lagging retry was not retained")
			}
			bridge.setHeight(2700)
			if done, err := signer.prepareKeygenRetry(retry); done || err != nil || retry.targetHeight != 2800 {
				t.Fatal("recovery did not select shared retry boundary")
			}
			if signer.tryKeygenRetry(retry) {
				t.Fatal("retry fired before target height")
			}
			// Execute at the last valid height (churn-1): an empty Asgard key
			// cannot create another retry here, so reads identify exactly one
			// execution.
			bridge.setHeight(3699)
			before := bridge.mimirReadCount(constants.ChurnRetryInterval.String())
			if !signer.tryKeygenRetry(retry) {
				t.Fatal("recovered retry did not finish")
			}
			// Both paths re-read validity: XMR at admission, Asgard while
			// checking whether its empty-key failure can schedule a retry.
			wantXMR := 1
			if kind == ttypes.KeygenType_AsgardKeygen {
				wantXMR = 0
			}
			if bridge.mimirReadCount(constants.ChurnRetryInterval.String())-before != 2 || client.callCount() != wantXMR {
				t.Fatal("wrong execution path after recovery")
			}
			before = bridge.mimirReadCount(constants.ChurnRetryInterval.String())
			if !signer.tryKeygenRetry(retry) || client.callCount() != wantXMR || bridge.mimirReadCount(constants.ChurnRetryInterval.String())-before != 1 {
				t.Fatal("consumed retry token executed a second time")
			}
			if kind == ttypes.KeygenType_AsgardKeygen && bridge.mimirReadCount(constants.MimirKeyEnableMonovaultKeygen) != 0 {
				t.Fatal("ordinary retry checked XMR policy")
			}
		})
	}
}
