package observer

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// pendingStreamResets matches the production goroutine dump: 2,321 streams
// waiting out closeStream's 10 s delay before Reset.
const pendingStreamResets = 2321

// BenchmarkGCWithPendingStreamResets measures a GC cycle while the production
// number of closeStream resets are pending, plus the memory they hold.
func BenchmarkGCWithPendingStreamResets(b *testing.B) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < pendingStreamResets; i++ {
		closeStream(zerolog.Nop(), &MockStream{mu: &sync.Mutex{}})
	}
	runtime.ReadMemStats(&after)
	// b.Loop runs this function once, so the setup and the drain below do too
	for b.Loop() {
		runtime.GC()
	}
	b.ReportMetric(float64(after.StackInuse+after.HeapInuse-before.StackInuse-before.HeapInuse)/pendingStreamResets, "pending-B/stream")
	// let the delayed resets drain so the next -count run starts clean
	time.Sleep(10*time.Second + 500*time.Millisecond)
}
