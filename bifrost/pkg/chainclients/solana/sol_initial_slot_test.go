package solana

import (
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

type fakeSlotGetter struct {
	calls    int
	failures int
	slot     uint64
}

func (f *fakeSlotGetter) GetSlot() (uint64, error) {
	f.calls++
	if f.calls <= f.failures {
		return 0, errors.New("connection refused")
	}
	return f.slot, nil
}

// An immediately-available RPC returns on the first call with no delay.
func TestWaitForInitialSlotImmediate(t *testing.T) {
	g := &fakeSlotGetter{slot: 42}
	start := time.Now()
	slot, ok := waitForInitialSlot(g, nil, time.Hour, zerolog.Nop())
	if !ok || slot != 42 {
		t.Fatalf("expected (42, true), got (%d, %v)", slot, ok)
	}
	if g.calls != 1 {
		t.Fatalf("expected 1 call, got %d", g.calls)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("no retry should have occurred; took %s", elapsed)
	}
}

// Transient startup failures are retried until the RPC answers — the fix for
// the scanner previously dying permanently on the first failed call.
func TestWaitForInitialSlotRetriesUntilSuccess(t *testing.T) {
	g := &fakeSlotGetter{failures: 3, slot: 7}
	slot, ok := waitForInitialSlot(g, nil, time.Millisecond, zerolog.Nop())
	if !ok || slot != 7 {
		t.Fatalf("expected (7, true), got (%d, %v)", slot, ok)
	}
	if g.calls != 4 {
		t.Fatalf("expected 4 calls (3 failures + 1 success), got %d", g.calls)
	}
}

// A stop signal during the retry backoff aborts promptly instead of sleeping
// out the interval, and reports ok=false.
func TestWaitForInitialSlotStops(t *testing.T) {
	g := &fakeSlotGetter{failures: 1 << 30}
	stop := make(chan struct{})
	done := make(chan struct{})
	var slot uint64
	var ok bool
	go func() {
		slot, ok = waitForInitialSlot(g, stop, time.Hour, zerolog.Nop())
		close(done)
	}()
	time.Sleep(50 * time.Millisecond) // let it fail once and enter the backoff
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop signal did not abort the backoff")
	}
	if ok || slot != 0 {
		t.Fatalf("expected (0, false) on stop, got (%d, %v)", slot, ok)
	}
}
