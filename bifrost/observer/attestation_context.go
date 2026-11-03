package observer

import (
	"context"
	"sync"
	"time"
)

// lockAttestationContext preserves the ordinary blocking mutex path for callers
// without cancellation. A cancellable waiter owns one timer, no worker goroutine,
// and never acquires the lock after returning. Keep the existing mutexes and lock
// order: replacing every pooled state's lock would add a channel per state.
func lockAttestationContext(ctx context.Context, mu *sync.Mutex) error {
	return acquireAttestationContext(ctx, mu.Lock, mu.TryLock, mu.Unlock)
}

func readLockAttestationContext(ctx context.Context, mu *sync.RWMutex) error {
	return acquireAttestationContext(ctx, mu.RLock, mu.TryRLock, mu.RUnlock)
}

// Ordinary delivery retains blocking lock acquisition, even with an RPC timeout.
// Only state-sync lock waits inherit the transfer's cancellation.
// Query and RPC calls keep their original caller context.
func attestationLockContext(ctx context.Context, stateSync bool) context.Context {
	if stateSync {
		return ctx
	}
	return context.Background()
}

func acquireAttestationContext(ctx context.Context, lock func(), tryLock func() bool, unlock func()) error {
	if ctx.Done() == nil {
		lock()
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tryLock() {
		if err := ctx.Err(); err != nil {
			unlock()
			return err
		}
		return nil
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if tryLock() {
				if err := ctx.Err(); err != nil {
					unlock()
					return err
				}
				return nil
			}
		}
	}
}
