package keeper

import (
	"math/big"
	"slices"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type activeValidatorsCacheKey struct{}

// ActiveValidatorsCache is a read-through snapshot of the active validator
// list scoped by a cosmos.Context, so the many ListActiveValidators calls of
// one BeginBlock or EndBlock decode the node accounts once. Like MimirCache,
// any node-account write through a context carrying the cache disables it for
// good: the pointer is shared with cache contexts that may be discarded, so a
// refill there could snapshot an uncommitted value.
//
// Only attach it outside transactions: store reads inside a tx count towards
// gas_used, which is consensus-visible.
type ActiveValidatorsCache struct {
	mu       sync.Mutex
	nodes    types.NodeAccounts
	loaded   bool
	disabled bool
}

// WithActiveValidatorsCache attaches a fresh cache to ctx.
//
// Invariant: every node-account write made while this cache is live must go
// through a ctx derived from the returned one, or the cache will not be
// disabled and reads will keep returning the stale snapshot.
func WithActiveValidatorsCache(ctx cosmos.Context) cosmos.Context {
	return ctx.WithValue(activeValidatorsCacheKey{}, &ActiveValidatorsCache{})
}

func ActiveValidatorsCacheFromContext(ctx cosmos.Context) *ActiveValidatorsCache {
	cache, _ := ctx.Value(activeValidatorsCacheKey{}).(*ActiveValidatorsCache)
	return cache
}

// Get returns a copy of the cached list, which the caller may modify freely.
func (c *ActiveValidatorsCache) Get() (types.NodeAccounts, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled || !c.loaded {
		return nil, false
	}
	return cloneNodeAccounts(c.nodes), true
}

// Set stores a copy of nodes, so later changes by the caller do not leak in.
func (c *ActiveValidatorsCache) Set(nodes types.NodeAccounts) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.disabled {
		c.nodes = cloneNodeAccounts(nodes)
		c.loaded = true
	}
	c.mu.Unlock()
}

// Disable drops the snapshot and makes every later Get miss.
func (c *ActiveValidatorsCache) Disable() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.disabled = true
	c.nodes = nil
	c.mu.Unlock()
}

// cloneNodeAccounts returns CloneNodeAccount of each element.
func cloneNodeAccounts(src types.NodeAccounts) types.NodeAccounts {
	out := make(types.NodeAccounts, len(src))
	copy(out, src)
	DetachNodeAccounts(out)
	return out
}

// CloneNodeAccount deep-copies the mutable parts of a NodeAccount (byte and
// string slices, the Bond big.Int) so the copy shares nothing a caller can
// change. Strings are immutable and are shared; every other field is a value.
func CloneNodeAccount(na types.NodeAccount) types.NodeAccount {
	na.NodeAddress = slices.Clone(na.NodeAddress)
	na.SignerMembership = slices.Clone(na.SignerMembership)
	if !na.Bond.IsNil() {
		na.Bond = cosmos.NewUintFromBigInt(na.Bond.BigIntMut())
	}
	return na
}

// DetachNodeAccounts replaces each element of nas with CloneNodeAccount of
// it, except that the copies' NodeAddress, SignerMembership and Bond words are
// carved out of one byte, one string and one word allocation for the whole
// list instead of three per element (a listing copies ~100 nodes of ~100
// members each). Each carved slice is clipped to its length, so appending to
// one, or growing a Bond in place, reallocates instead of running into the
// next; nil stays nil and empty stays empty, and a zero Bond has no words, as
// NewUintFromBigInt leaves it. Each Bond still gets a big.Int of its own: a
// Uint can only wrap one it allocated.
func DetachNodeAccounts(nas types.NodeAccounts) {
	addrBytes, members, bondWords := 0, 0, 0
	for i := range nas {
		addrBytes += len(nas[i].NodeAddress)
		members += len(nas[i].SignerMembership)
		if !nas[i].Bond.IsNil() {
			bondWords += len(nas[i].Bond.BigIntMut().Bits())
		}
	}
	addrs := make([]byte, 0, addrBytes)
	strs := make([]string, 0, members)
	words := make([]big.Word, 0, bondWords)
	for i := range nas {
		na := &nas[i]
		if na.NodeAddress != nil {
			start := len(addrs)
			addrs = append(addrs, na.NodeAddress...)
			na.NodeAddress = addrs[start:len(addrs):len(addrs)]
		}
		if na.SignerMembership != nil {
			start := len(strs)
			strs = append(strs, na.SignerMembership...)
			na.SignerMembership = strs[start:len(strs):len(strs)]
		}
		if !na.Bond.IsNil() {
			bond := cosmos.ZeroUint()
			if bits := na.Bond.BigIntMut().Bits(); len(bits) > 0 {
				start := len(words)
				words = append(words, bits...)
				bond.BigIntMut().SetBits(words[start:len(words):len(words)])
			}
			na.Bond = bond
		}
	}
}
