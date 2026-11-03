package keeperv1

import (
	"fmt"
	"strconv"

	storetypes "cosmossdk.io/store/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

func (k KVStore) setTxOut(ctx cosmos.Context, key []byte, record TxOut) {
	keeper.TxOutCacheFromContext(ctx).Disable()
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

// AppendTxOut - append the given item to txOut
func (k KVStore) AppendTxOut(ctx cosmos.Context, height int64, item TxOutItem) error {
	block, err := k.GetTxOut(ctx, height)
	if err != nil {
		return err
	}
	block.TxArray = append(block.TxArray, item)
	return k.SetTxOut(ctx, block)
}

// ClearTxOut - remove the txout of the given height from key value  store
func (k KVStore) ClearTxOut(ctx cosmos.Context, height int64) error {
	k.del(ctx, k.getKeyInt64(prefixTxOut, height))
	return nil
}

// SetTxOut - write the given txout information to key value store
func (k KVStore) SetTxOut(ctx cosmos.Context, blockOut *TxOut) error {
	if blockOut == nil || blockOut.IsEmpty() {
		return nil
	}
	k.setTxOut(ctx, k.getKeyInt64(prefixTxOut, blockOut.Height), *blockOut)
	return nil
}

// GetTxOutIterator iterate tx out
func (k KVStore) GetTxOutIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixTxOut)
}

// GetTxOut - write the given txout information to key value store
func (k KVStore) GetTxOut(ctx cosmos.Context, height int64) (*TxOut, error) {
	record := NewTxOut(height)
	key, bz, present := k.getTxOutBytes(ctx, height)
	if !present {
		return record, nil
	}
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

// getTxOutBytes makes GetTxOut's store reads (those of getTxOut: Has, then Get
// if present) and returns the stored bytes undecoded. With a TxOutCache on ctx
// the stored bytes (or their absence) of each height are shared between the
// reads of one query instead.
func (k KVStore) getTxOutBytes(ctx cosmos.Context, height int64) (key, bz []byte, present bool) {
	key = k.getKeyInt64(prefixTxOut, height)
	cache := keeper.TxOutCacheFromContext(ctx)
	var ok bool
	if bz, present, ok = cache.Get(height); ok {
		return key, bz, present
	}
	bz, present = readTxOutBytes(k.kvStore(ctx), cache, key, height)
	return key, bz, present
}

// readTxOutBytes reads the TxOut of height, stored under key, from store (Has,
// then Get if present) and records the result in cache, if any.
func readTxOutBytes(store storetypes.KVStore, cache *keeper.TxOutCache, key []byte, height int64) (bz []byte, present bool) {
	present = store.Has(key)
	if present {
		bz = store.Get(key)
	}
	cache.Set(height, bz, present)
	return bz, present
}

// txOutWindow makes getTxOutBytes' reads of the heights first..last for walks
// that read them many times over (getSortedVaultSecurity reads the window
// once per vault): each height's key is built, and the store and TxOutCache
// are looked up, once per window instead of once per read. Every read is
// still made, through the same gas-metered store and cache.
type txOutWindow struct {
	first int64
	keys  [][]byte
	store storetypes.KVStore
	cache *keeper.TxOutCache
}

// newTxOutWindow returns the window of heights first..last (empty if last <
// first) read through ctx.
func (k KVStore) newTxOutWindow(ctx cosmos.Context, first, last int64) txOutWindow {
	w := txOutWindow{first: first, store: k.kvStore(ctx), cache: keeper.TxOutCacheFromContext(ctx)}
	if last < first {
		return w
	}
	// getKeyInt64's keys, carved from one buffer and clipped to their length;
	// none is written again, as the store may keep the keys it is given
	var digits [20]byte // len("-9223372036854775808")
	width := max(len(strconv.AppendInt(digits[:0], first, 10)), len(strconv.AppendInt(digits[:0], last, 10)))
	w.keys = make([][]byte, last-first+1)
	buf := make([]byte, 0, len(w.keys)*(len(prefixTxOut)+1+width))
	for i := range w.keys {
		start := len(buf)
		buf = append(buf, prefixTxOut...)
		buf = append(buf, '/')
		buf = strconv.AppendInt(buf, first+int64(i), 10)
		w.keys[i] = buf[start:len(buf):len(buf)]
	}
	return w
}

// get is getTxOutBytes(ctx, height) for a height of the window.
func (w txOutWindow) get(height int64) (key, bz []byte, present bool) {
	key = w.keys[height-w.first]
	var ok bool
	if bz, present, ok = w.cache.Get(height); ok {
		return key, bz, present
	}
	bz, present = readTxOutBytes(w.store, w.cache, key, height)
	return key, bz, present
}

// txOutWalk makes GetTxOut's reads for a walk over consecutive heights
// (GetPendingOutbounds reads every height of its window, most of them empty):
// the store and TxOutCache are looked up once per walk instead of on every
// read, and the keys are carved from shared buffers. Every read is still made,
// in the same order, through the same gas-metered store and cache.
type txOutWalk struct {
	store storetypes.KVStore
	cache *keeper.TxOutCache
	buf   []byte // the unused tail of the current key buffer
}

func (k KVStore) newTxOutWalk(ctx cosmos.Context) *txOutWalk {
	return &txOutWalk{store: k.kvStore(ctx), cache: keeper.TxOutCacheFromContext(ctx)}
}

// key is getKeyInt64(prefixTxOut, height), clipped to its length. A key is
// never written again, as the store may keep the keys it is given.
func (w *txOutWalk) key(height int64) []byte {
	const maxKey = len(prefixTxOut) + 1 + 20 // len("-9223372036854775808")
	if cap(w.buf)-len(w.buf) < maxKey {
		w.buf = make([]byte, 0, 64*maxKey)
	}
	start := len(w.buf)
	w.buf = append(w.buf, prefixTxOut...)
	w.buf = append(w.buf, '/')
	w.buf = strconv.AppendInt(w.buf, height, 10)
	return w.buf[start:len(w.buf):len(w.buf)]
}

// getTxOutWalk is GetTxOut(ctx, height) read through w, except that a height
// with no TxOut stored returns nil rather than an empty TxOut.
func (k KVStore) getTxOutWalk(ctx cosmos.Context, w *txOutWalk, height int64) (*TxOut, error) {
	key := w.key(height)
	bz, present, ok := w.cache.Get(height)
	if !ok {
		bz, present = readTxOutBytes(w.store, w.cache, key, height)
	}
	if !present {
		return nil, nil
	}
	record := NewTxOut(height)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

func (k KVStore) GetTxOutValue(ctx cosmos.Context, height int64) (cosmos.Uint, cosmos.Uint, error) {
	txout, err := k.GetTxOut(ctx, height)
	if err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}
	runeValue, cloutValue := k.GetTOIsValue(ctx, txout.TxArray...)
	return runeValue, cloutValue, nil
}

func (k KVStore) GetTOIsValue(ctx cosmos.Context, tois ...TxOutItem) (cosmos.Uint, cosmos.Uint) {
	runeValue := cosmos.ZeroUint()
	cloutValue := cosmos.ZeroUint()
	poolCache := map[common.Asset]Pool{} // Cache the pools to avoid duplicated GetPool calls
	for i := range tois {
		for _, coin := range append(common.Coins{tois[i].Coin}, tois[i].MaxGas...) {
			if coin.IsRune() {
				runeValue = runeValue.Add(coin.Amount)
				continue
			}

			pool, ok := poolCache[coin.Asset]
			if !ok {
				var err error
				pool, err = k.GetPool(ctx, coin.Asset.GetLayer1Asset())
				if err != nil {
					_ = dbError(ctx, fmt.Sprintf("unable to get pool : %s", coin.Asset), err)
					continue
				}
				poolCache[coin.Asset] = pool
			}
			runeValue = runeValue.Add(pool.AssetValueInRune(coin.Amount))
		}

		if tois[i].CloutSpent != nil {
			cloutValue = cloutValue.Add(*tois[i].CloutSpent)
		}
	}

	return runeValue, cloutValue
}
