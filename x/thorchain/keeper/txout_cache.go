package keeper

import (
	"bytes"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type txOutCacheKey struct{}

// TxOutCache is a read-through snapshot of the stored TxOut bytes by height,
// absent heights included, scoped by a cosmos.Context. One swap quote walks
// the pending txout window several times (vault security sorts, pending
// outbounds) before it adds its own outbound; the cache lets those walks
// share one store read per height. Like MimirCache, any txout write through
// a context carrying the cache disables it for good: the pointer is shared
// with cache contexts that may be discarded, so a refill there could snapshot
// an uncommitted value.
//
// Only attach it to queries that are not run on behalf of a contract: store
// reads count gas, and gas inside a tx (including CosmWasm queries) is
// consensus-visible.
type TxOutCache struct {
	mu       sync.Mutex
	parent   *TxOutCache
	entries  map[int64]txOutCacheEntry
	disabled bool
}

type txOutCacheEntry struct {
	bz      []byte
	present bool
}

// WithTxOutCache attaches a fresh cache to ctx, unless ctx already carries
// one (which it then keeps using, disabled or not).
//
// Invariant: every txout write made while this cache is live must go through
// a ctx derived from the returned one, or the cache will not be disabled and
// reads will keep returning the stale snapshot.
func WithTxOutCache(ctx cosmos.Context) cosmos.Context {
	if TxOutCacheFromContext(ctx) != nil {
		return ctx
	}
	return ctx.WithValue(txOutCacheKey{}, &TxOutCache{entries: make(map[int64]txOutCacheEntry)})
}

// ForkTxOutCache gives ctx a view of its cache that a txout write through the
// returned ctx disables without disabling the original. While the view is
// live it reads and fills the original's snapshot.
//
// Only use it for a store branch that is never written back (e.g. a swap
// simulation's discarded CacheMultiStore), and do not write txouts through
// the original ctx while the view is in use: until its first txout write the
// branch sees exactly the original's txouts, after it the view is disabled.
// Without a cache on ctx it returns ctx unchanged.
func ForkTxOutCache(ctx cosmos.Context) cosmos.Context {
	parent := TxOutCacheFromContext(ctx)
	if parent == nil {
		return ctx
	}
	return ctx.WithValue(txOutCacheKey{}, &TxOutCache{parent: parent})
}

func TxOutCacheFromContext(ctx cosmos.Context) *TxOutCache {
	cache, _ := ctx.Value(txOutCacheKey{}).(*TxOutCache)
	return cache
}

// Get returns the stored bytes of height (present=false: no TxOut stored) and
// whether the cache held them. The bytes must not be modified.
func (c *TxOutCache) Get(height int64) (bz []byte, present, ok bool) {
	if c == nil {
		return nil, false, false
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return nil, false, false
	}
	if c.parent != nil {
		c.mu.Unlock()
		return c.parent.Get(height)
	}
	entry, ok := c.entries[height]
	c.mu.Unlock()
	return entry.bz, entry.present, ok
}

// Set records the store result for height, copying bz so later changes by
// the caller do not leak in.
func (c *TxOutCache) Set(height int64, bz []byte, present bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.disabled {
		c.mu.Unlock()
		return
	}
	if c.parent != nil {
		c.mu.Unlock()
		c.parent.Set(height, bz, present)
		return
	}
	c.entries[height] = txOutCacheEntry{bz: bytes.Clone(bz), present: present}
	c.mu.Unlock()
}

// Disable drops the snapshot (a fork only stops using its parent) and makes
// every later Get miss.
func (c *TxOutCache) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disabled = true
	c.parent = nil
	c.entries = nil
	c.mu.Unlock()
}
