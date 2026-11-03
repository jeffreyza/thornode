package keeper

import (
	"sync"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type mimirCacheKey struct{}

// MimirCache is a read-through snapshot cache scoped by a cosmos.Context.
// Repeated halt/readiness checks share the first store result for each key.
// Any Mimir write through a context carrying the cache disables it for good,
// so later reads fall back to the reading context's own store. Per-key
// eviction is not enough: the pointer is shared with cache contexts that may
// be discarded, and a re-read there would snapshot an uncommitted value.
type MimirCache struct {
	mu       sync.Mutex
	values   map[string]int64
	disabled bool
}

// WithMimirCache attaches a fresh cache to ctx. Writes made later through
// a context without this cache are deliberately not visible through it.
//
// Invariant: every Mimir write (SetMimir/DeleteMimir) made while this cache
// is live must go through a ctx derived from the returned one, or the cache
// will not be disabled and reads will keep returning the stale snapshot.
func WithMimirCache(ctx cosmos.Context) cosmos.Context {
	return ctx.WithValue(mimirCacheKey{}, &MimirCache{values: make(map[string]int64)})
}

func MimirCacheFromContext(ctx cosmos.Context) *MimirCache {
	cache, _ := ctx.Value(mimirCacheKey{}).(*MimirCache)
	return cache
}

func (c *MimirCache) Get(key string) (int64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled {
		return 0, false
	}
	value, ok := c.values[key]
	return value, ok
}

func (c *MimirCache) Set(key string, value int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.disabled {
		c.values[key] = value
	}
	c.mu.Unlock()
}

// Disable drops the snapshot and makes every later Get miss.
func (c *MimirCache) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disabled = true
	c.values = nil
	c.mu.Unlock()
}
