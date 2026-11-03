package keeperv1

import (
	"bytes"
	"math/big"
	"slices"
	"sync"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// decodeMemo maps a store key to the last bytes decoded for it and their
// decoding. It is a pure cache: an entry is used only when the bytes read from
// the store are equal to the stored copy, and decoding is a function of the
// bytes alone, so the result is the same whichever store, height or context
// the bytes came from. Store access is unchanged, since the bytes are still
// read to compare them. Only successful decodings are memoized.
type decodeMemo[T any] struct {
	maxEntries int
	// decode decodes bz into *v exactly as the codec would; bz is never
	// written afterwards, so the decoding may point into it.
	decode func(k KVStore, bz []byte, v *T) error
	// clone returns a copy of v sharing nothing mutable with it.
	clone func(v T) T

	mu sync.RWMutex
	m  map[string]*decodeMemoEntry[T]
}

type decodeMemoEntry[T any] struct {
	raw []byte // stored bytes v was decoded from; never written
	v   T
}

func newDecodeMemo[T any](maxEntries int, decode func(KVStore, []byte, *T) error, clone func(T) T) *decodeMemo[T] {
	return &decodeMemo[T]{
		maxEntries: maxEntries,
		decode:     decode,
		clone:      clone,
		m:          make(map[string]*decodeMemoEntry[T]),
	}
}

// get sets *out to the decoding of bz, the value stored under key, and returns
// true. *out is a copy of the memoized value, which the caller may modify
// freely. It returns false, leaving *out untouched, if bz does not decode; the
// caller then decodes bz itself to get the error.
func (m *decodeMemo[T]) get(k KVStore, key, bz []byte, out *T) bool {
	v := m.shared(k, key, bz)
	if v == nil {
		return false
	}
	*out = m.clone(*v)
	return true
}

// shared is get without the copy: it returns the memoized decoding of bz, the
// value stored under key, or nil if bz does not decode. The value is shared
// with the memo and every other caller, so it must only be read; hand out a
// clone of it, never the value itself.
func (m *decodeMemo[T]) shared(k KVStore, key, bz []byte) *T {
	m.mu.RLock()
	e := m.m[string(key)]
	m.mu.RUnlock()
	if e != nil && bytes.Equal(e.raw, bz) {
		return &e.v
	}

	e = &decodeMemoEntry[T]{raw: bytes.Clone(bz)}
	if err := m.decode(k, e.raw, &e.v); err != nil {
		return nil
	}
	m.mu.Lock()
	if _, ok := m.m[string(key)]; !ok && len(m.m) >= m.maxEntries {
		clear(m.m)
	}
	m.m[string(key)] = e
	m.mu.Unlock()
	return &e.v
}

// len returns the number of memoized entries.
func (m *decodeMemo[T]) len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.m)
}

// cloneUint returns a copy of u sharing no big.Int with it; the zero value
// (nil, as the codec leaves an absent field) stays nil.
func cloneUint(u cosmos.Uint) cosmos.Uint {
	if u.IsNil() {
		return u
	}
	return cosmos.NewUintFromBigInt(u.BigIntMut())
}

// nodeAccountMemoMaxEntries bounds the node account memo (mainnet has ~200
// node accounts). An entry holds the record's stored bytes plus its decoded
// fields, ~10 KiB for a node in ~100 vaults, so the memo stays under ~20 MiB.
const nodeAccountMemoMaxEntries = 2048

// nodeAccountDecodeMemo memoizes node account decodes by store key. The memoized
// signer-membership strings point into the immutable byte copy.
var nodeAccountDecodeMemo = newDecodeMemo(nodeAccountMemoMaxEntries,
	func(k KVStore, bz []byte, na *NodeAccount) error { return k.decodeNodeAccount(bz, na, true) },
	keeper.CloneNodeAccount)

// memoDecodeNodeAccount is nodeAccountDecodeMemo.get.
func (k KVStore) memoDecodeNodeAccount(key, bz []byte, na *NodeAccount) bool {
	return nodeAccountDecodeMemo.get(k, key, bz, na)
}

// vaultMemoMaxEntries bounds the vault memo. Mainnet keeps a handful of
// asgard vaults; an entry holds the stored bytes and the decoding of a vault
// of ~100 members and ~60 coins, ~30 KiB, so the memo stays under ~16 MiB.
const vaultMemoMaxEntries = 512

// vaultDecodeMemo memoizes vault decodes by store key.
var vaultDecodeMemo = newDecodeMemo(vaultMemoMaxEntries,
	func(k KVStore, bz []byte, v *Vault) error { return k.cdc.Unmarshal(bz, v) },
	cloneVault)

// cloneVault returns a copy of v sharing no slice or big.Int with it. Every
// other field of a Vault, and of its Coins and Routers, is a value or an
// immutable string. nil slices stay nil.
func cloneVault(v Vault) Vault {
	words := make(uintWords, 0, coinsWordCount(v.Coins)+coinsWordCount(v.Deficit))
	v.Coins = words.cloneCoins(v.Coins)
	v.Deficit = words.cloneCoins(v.Deficit)
	v.Membership = slices.Clone(v.Membership)
	v.Chains = slices.Clone(v.Chains)
	v.PendingTxBlockHeights = slices.Clone(v.PendingTxBlockHeights)
	v.Routers = slices.Clone(v.Routers)
	v.Frozen = slices.Clone(v.Frozen)
	return v
}

// uintWords is a slab that cloned Uints take their words from: copying a
// record's amounts with cloneUint costs a big.Int and a word slice each,
// through the slab only the big.Int (a Uint can only wrap one it allocated).
// Each clone's words are clipped to their length, so growing one in place
// reallocates instead of running into the next. Sized with wordCount, the
// slab is one allocation.
type uintWords []big.Word

// wordCount is the number of words cloning u takes from a uintWords.
func wordCount(u cosmos.Uint) int {
	if u.IsNil() {
		return 0
	}
	return len(u.BigIntMut().Bits())
}

// coinsWordCount is the number of words cloning cs's amounts takes from a
// uintWords.
func coinsWordCount(cs common.Coins) int {
	n := 0
	for i := range cs {
		n += wordCount(cs[i].Amount)
	}
	return n
}

// clone is cloneUint with u's words carved out of w.
func (w *uintWords) clone(u cosmos.Uint) cosmos.Uint {
	if u.IsNil() {
		return u
	}
	c := cosmos.ZeroUint()
	if bits := u.BigIntMut().Bits(); len(bits) > 0 {
		start := len(*w)
		*w = append(*w, bits...)
		c.BigIntMut().SetBits((*w)[start:len(*w):len(*w)])
	}
	return c
}

// cloneCoins returns a copy of cs sharing no memory with it, its amounts'
// words carved out of w; nil stays nil.
func (w *uintWords) cloneCoins(cs common.Coins) common.Coins {
	cs = slices.Clone(cs)
	for i := range cs {
		cs[i].Amount = w.clone(cs[i].Amount)
	}
	return cs
}

// memoDecodeVault is vaultDecodeMemo.get.
func (k KVStore) memoDecodeVault(key, bz []byte, v *Vault) bool {
	return vaultDecodeMemo.get(k, key, bz, v)
}

// poolMemoMaxEntries bounds the pool memo. Mainnet has ~50 pools; an entry
// holds a pool's ~200 stored bytes and its decoding, well under 1 KiB.
const poolMemoMaxEntries = 1024

// poolDecodeMemo memoizes pool decodes by store key for GetPools. Decoding a
// pool parses six decimal Uints, and EndBlock and every deposit scan all pools
// (GetAnchors, volume, liquidity checks) while few of them change.
var poolDecodeMemo = newDecodeMemo(poolMemoMaxEntries,
	func(k KVStore, bz []byte, p *Pool) error { return k.cdc.Unmarshal(bz, p) },
	clonePool)

// clonePool returns a copy of p sharing no big.Int with it. Its Asset holds
// only strings and every other field is a value.
func clonePool(p Pool) Pool {
	words := make(uintWords, 0, wordCount(p.BalanceRune)+wordCount(p.BalanceAsset)+
		wordCount(p.LPUnits)+wordCount(p.SynthUnits)+
		wordCount(p.PendingInboundRune)+wordCount(p.PendingInboundAsset))
	p.BalanceRune = words.clone(p.BalanceRune)
	p.BalanceAsset = words.clone(p.BalanceAsset)
	p.LPUnits = words.clone(p.LPUnits)
	p.SynthUnits = words.clone(p.SynthUnits)
	p.PendingInboundRune = words.clone(p.PendingInboundRune)
	p.PendingInboundAsset = words.clone(p.PendingInboundAsset)
	return p
}

// memoDecodePool is poolDecodeMemo.get.
func (k KVStore) memoDecodePool(key, bz []byte, p *Pool) bool {
	return poolDecodeMemo.get(k, key, bz, p)
}
