package evm

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
)

func newBenchBlockMetaAccessor(tb testing.TB) *LevelDBBlockMetaAccessor {
	tb.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	accessor, err := NewLevelDBBlockMetaAccessor(PrefixBlockMeta, PrefixSignedTxItem, db)
	if err != nil {
		tb.Fatal(err)
	}
	return accessor
}

func benchTxHash(n uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	sum := sha256.Sum256(b[:])
	return "0x" + hex.EncodeToString(sum[:])
}

// BenchmarkSignedTxScan mirrors an EVM scanner over 50 blocks of 200 txs: a
// RemoveSignedTxItem for every tx (almost none were signed by this node) and
// the unstuck check's GetSignedTxItems after each block.
func BenchmarkSignedTxScan(b *testing.B) {
	const blocks, txsPerBlock = 50, 200
	var n uint64
	for b.Loop() {
		b.StopTimer()
		accessor := newBenchBlockMetaAccessor(b)
		for i := 0; i < 3; i++ { // a few outbounds in flight
			if err := accessor.AddSignedTxItem(evmtypes.SignedTxItem{Hash: benchTxHash(1<<40 + uint64(i)), Height: 1}); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		for blk := 0; blk < blocks; blk++ {
			for t := 0; t < txsPerBlock; t++ {
				n++
				if err := accessor.RemoveSignedTxItem(benchTxHash(n)); err != nil {
					b.Fatal(err)
				}
			}
			items, err := accessor.GetSignedTxItems()
			if err != nil || len(items) != 3 {
				b.Fatalf("got %d items, err %v", len(items), err)
			}
		}
	}
}
