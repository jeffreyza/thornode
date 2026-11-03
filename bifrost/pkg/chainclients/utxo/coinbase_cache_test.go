package utxo

import (
	"fmt"
	"strings"
	"testing"
)

const coinbaseFixtureHeight = 1696761

func TestCoinbaseValueReusesScannedBlock(t *testing.T) {
	server := newCoinbaseRPCServer(t)
	want, err := newCoinbaseTestClient(t, server).getCoinbaseValue(coinbaseFixtureHeight)
	if err != nil || want <= 0 {
		t.Fatalf("uncached: %d, %v", want, err)
	}

	// a scanned block's coinbase is looked up without fetching it again
	c := newCoinbaseTestClient(t, server)
	if _, err = c.getBlock(coinbaseFixtureHeight); err != nil {
		t.Fatal(err)
	}
	calls := server.getblockCalls.Load()
	got, err := c.getCoinbaseValue(coinbaseFixtureHeight)
	if err != nil || got != want || server.getblockCalls.Load() != calls {
		t.Fatalf("cached: %d, %v (want %d); getblock calls %d -> %d", got, err, want, calls, server.getblockCalls.Load())
	}

	// a different block at that height (a reorg) is fetched again
	other := "00000000000000000000000000000000000000000000000000000000deadbeef"
	server.hash.Store(&other)
	if got, err = c.getCoinbaseValue(coinbaseFixtureHeight); err != nil || got != want || server.getblockCalls.Load() != calls+1 {
		t.Fatalf("reorged: %d, %v; getblock calls %d", got, err, server.getblockCalls.Load()-calls)
	}
	server.hash.Store(nil)

	// Behavior change: once scanned, a getblock failure no longer fails the
	// lookup (the old code then fell back to the chain's default coinbase).
	server.failGetblock.Store(true)
	if got, err = c.getCoinbaseValue(coinbaseFixtureHeight); err != nil || got != want {
		t.Fatalf("cached with failing getblock: %d, %v", got, err)
	}
	// an unscanned block still fails, with the same message as before
	_, err = newCoinbaseTestClient(t, server).getCoinbaseValue(coinbaseFixtureHeight)
	if err == nil || !strings.HasPrefix(err.Error(), "fail to get block verbose tx: ") || !strings.Contains(err.Error(), "getblock failed") {
		t.Fatalf("uncached with failing getblock: %v", err)
	}
}

func TestCoinbaseCacheBounded(t *testing.T) {
	var cc coinbaseCache
	for h := int64(1); h <= 500; h++ {
		cc.put(h, fmt.Sprint(h), h*10)
	}
	if len(cc.entries) > coinbaseCacheBlocks {
		t.Fatalf("%d entries", len(cc.entries))
	}
	if v, ok := cc.get(500, "500"); !ok || v != 5000 {
		t.Fatalf("latest: %d %v", v, ok)
	}
	if _, ok := cc.get(500, "other"); ok {
		t.Fatal("hit for a different hash")
	}
	if _, ok := cc.get(100, "100"); ok {
		t.Fatal("hit for an evicted height")
	}
	// an older height (re-scan after a reorg) is kept while within the window
	cc.put(480, "480b", 1)
	if v, ok := cc.get(480, "480b"); !ok || v != 1 {
		t.Fatalf("rescanned: %d %v", v, ok)
	}
}
