package curvelock

import (
	"crypto/elliptic"
	"sync"
	"sync/atomic"
	"testing"

	btss "gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"
)

// TestWithCurve_ConcurrentECDSAvsEdDSA exercises the load-bearing invariant
// behind curve_lock.go: under concurrent fn invocations, the process-global
// curve observed inside fn must match the curve passed to WithCurve, even
// when other goroutines are simultaneously switching to a different curve.
//
// Without the mutex, parallel WithCurve(S256, ...) and WithCurve(Edwards, ...)
// would race on tss-lib's global, and an ECPoint UnmarshalJSON that fell
// back to btss.EC() could read the wrong curve. Run under `go test -race`
// to catch any future refactor that drops the mutex.
func TestWithCurve_ConcurrentECDSAvsEdDSA(t *testing.T) {
	const goroutinesPerCurve = 50
	const iterationsPerGoroutine = 20

	secp := btss.S256()
	edw := btss.Edwards()

	var mismatches atomic.Int64
	var wg sync.WaitGroup

	runCurve := func(want elliptic.Curve) {
		defer wg.Done()
		for i := 0; i < iterationsPerGoroutine; i++ {
			err := WithCurve(want, func() error {
				if got := btss.EC(); got != want {
					mismatches.Add(1)
				}
				return nil
			})
			if err != nil {
				t.Errorf("WithCurve returned err: %v", err)
				return
			}
		}
	}

	for i := 0; i < goroutinesPerCurve; i++ {
		wg.Add(2)
		go runCurve(secp)
		go runCurve(edw)
	}
	wg.Wait()

	if n := mismatches.Load(); n != 0 {
		t.Fatalf("observed %d curve mismatches inside WithCurve fn — the mutex is not serializing concurrent curve switches", n)
	}
}

// TestWithCurve_RestoresPreviousCurve asserts that WithCurve restores the
// process-global curve to its prior value once fn returns. A regression in
// the defer would mean a one-off mutation leaks across calls, breaking the
// next unrelated unmarshal.
func TestWithCurve_RestoresPreviousCurve(t *testing.T) {
	// Capture curve instances once — btss.Edwards() may return a fresh
	// pointer per call, so compare against pinned references.
	secp := btss.S256()
	edw := btss.Edwards()

	original := btss.EC()
	t.Cleanup(func() { btss.SetCurve(original) })

	btss.SetCurve(secp)
	err := WithCurve(edw, func() error {
		if got := btss.EC(); got != edw {
			t.Fatalf("inside fn: expected Edwards, got %v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithCurve returned err: %v", err)
	}
	if got := btss.EC(); got != secp {
		t.Fatalf("after WithCurve: expected secp256k1 restored, got %v", got)
	}
}
