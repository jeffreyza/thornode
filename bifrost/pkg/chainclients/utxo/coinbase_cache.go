package utxo

import "sync"

// coinbaseCacheBlocks bounds the coinbase cache to the most recent heights.
const coinbaseCacheBlocks = 64

type coinbaseEntry struct {
	hash  string
	value int64
}

// coinbaseCache maps a block height to the coinbase value of the block with a
// given hash. A lookup only hits for the same hash, so a reorged height is
// fetched again.
type coinbaseCache struct {
	mu        sync.Mutex
	entries   map[int64]coinbaseEntry
	maxHeight int64
}

func (cc *coinbaseCache) get(height int64, hash string) (int64, bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	entry, ok := cc.entries[height]
	if !ok || entry.hash != hash {
		return 0, false
	}
	return entry.value, true
}

func (cc *coinbaseCache) put(height int64, hash string, value int64) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.entries == nil {
		cc.entries = make(map[int64]coinbaseEntry)
	}
	cc.entries[height] = coinbaseEntry{hash: hash, value: value}
	if height > cc.maxHeight {
		cc.maxHeight = height
	}
	for h := range cc.entries {
		if h <= cc.maxHeight-coinbaseCacheBlocks {
			delete(cc.entries, h)
		}
	}
}
