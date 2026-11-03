package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

func TestInvariantChecksWrapFastSimulation(t *testing.T) {
	calls := 0
	run := false
	watcher := &types.Watcher{Interval: time.Hour, Fn: func(_ *types.OpConfig) error {
		calls++
		if calls == 1 && run {
			t.Fatal("initial check ran after simulation")
		}
		if calls == 2 && !run {
			t.Fatal("final check ran before simulation")
		}
		return nil
	}}
	if err := runWithInvariantChecks(nil, watcher, func() { run = true }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected initial and final checks, got %d", calls)
	}
}

func TestInvariantBoundaryFailuresAbortSimulation(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			calls, ran := 0, false
			watcher := &types.Watcher{Fn: func(_ *types.OpConfig) error {
				calls++
				if calls == failAt {
					return errors.New("unavailable")
				}
				return nil
			}}
			err := runWithInvariantChecks(nil, watcher, func() { ran = true })
			if err == nil || !strings.Contains(err.Error(), "unavailable") {
				t.Fatalf("boundary failure was accepted: %v", err)
			}
			if ran != (failAt == 2) {
				t.Fatalf("simulation run=%t for check %d", ran, failAt)
			}
		})
	}
}
