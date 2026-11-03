package log

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sdklog "cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

type testEntry struct {
	Height int64             `json:"height"`
	Level  string            `json:"level"`
	Module string            `json:"module"`
	Msg    string            `json:"msg"`
	Fields map[string]string `json:"fields"`
}

func readEntries(t *testing.T, start, end int64, level string) []testEntry {
	t.Helper()
	raw, err := ReadBlockLogs(context.Background(), start, end, level, nil)
	if err != nil {
		t.Fatalf("ReadBlockLogs(context.Background(), %d,%d,%s): %v", start, end, level, err)
	}
	var out []testEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("response is not a JSON array: %v\n%s", err, raw)
	}
	return out
}

func TestBlockLogsCaptureFilterPrune(t *testing.T) {
	dir := t.TempDir()
	InitBlockLogs(dir, 2)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	for h := int64(1); h <= 4; h++ {
		l := WithBlockCapture(sdklog.NewNopLogger(), h).With("k", "v")
		l.Debug("dbg")
		l.Info("inf", "err", errors.New("boom"))
		l.Warn("wrn")
		l.Error("err")
		EndBlock(h)
	}

	// retention 2 at height 4 keeps 3 and 4 only
	for h, want := range map[string]bool{"1": false, "2": false, "3": true, "4": true} {
		_, err := os.Stat(filepath.Join(dir, h+".jsonl"))
		if got := err == nil; got != want {
			t.Errorf("file %s.jsonl exists=%v want %v", h, got, want)
		}
	}

	info := readEntries(t, 3, 4, "info")
	if len(info) != 6 {
		t.Fatalf("info: want 6 entries, got %d", len(info))
	}
	if info[0].Height != 3 || info[0].Level != "info" || info[0].Msg != "inf" {
		t.Errorf("unexpected first entry %+v", info[0])
	}
	if info[0].Fields["k"] != "v" || info[0].Fields["err"] != "boom" {
		t.Errorf("fields not captured: %+v", info[0].Fields)
	}
	for _, e := range info {
		if e.Level == "debug" {
			t.Errorf("debug entry leaked through info filter: %+v", e)
		}
	}

	if got := readEntries(t, 3, 4, "error"); len(got) != 2 || got[0].Level != "error" {
		t.Errorf("error filter: want 2 error entries, got %+v", got)
	}

	// debug returns everything; range clamps to what retention can hold
	if got := readEntries(t, 1, 4, "debug"); len(got) != 8 {
		t.Errorf("debug over pruned range: want 8, got %d", len(got))
	}

	if _, err := ReadBlockLogs(context.Background(), 3, 4, "bogus", nil); err == nil {
		t.Error("unknown level accepted")
	}

	// disabled store is a pure pass-through
	InitBlockLogs("", 0)
	inner := sdklog.NewNopLogger()
	if WithBlockCapture(inner, 5) != inner {
		t.Error("disabled capture should return inner logger unchanged")
	}
}

// Reads must not serialize against capture writes on the block-execution path.
// Run with -race: this fails if ReadBlockLogs ever races the writer's state.
func TestBlockLogsConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	InitBlockLogs(dir, 50)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for h := int64(1); h <= 200; h++ {
			l := WithBlockCapture(sdklog.NewNopLogger(), h)
			l.Info("writing", "h", h)
			EndBlock(h)
		}
	}()
	for i := 0; i < 200; i++ {
		// alternate the live tail with a range EndBlock is pruning underneath us
		end := int64(200)
		if i%2 == 1 {
			end = 60
		}
		if _, err := ReadBlockLogs(context.Background(), 1, end, "debug", nil); err != nil {
			t.Errorf("concurrent read: %v", err)
			break
		}
	}
	<-done
}

func TestBlockLogsResponseCap(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	l := WithBlockCapture(sdklog.NewNopLogger(), 1)
	big := strings.Repeat("x", 1<<20)
	for i := 0; i <= MaxResponseBytes>>20; i++ {
		l.Info(big)
	}
	EndBlock(1)

	if _, err := ReadBlockLogs(context.Background(), 1, 1, "info", nil); err == nil {
		t.Error("oversized response should be rejected")
	}
	if _, err := ReadBlockLogs(context.Background(), 1, 1, "error", nil); err != nil {
		t.Errorf("filtered read should fit: %v", err)
	}
}

// Input is bounded too: a filter that drops everything must not scan the
// whole retention window. The error names the height so the caller can
// narrow the range.
func TestBlockLogsScanCap(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	line := append([]byte(`{"level":"info","msg":"`), append(bytes.Repeat([]byte("x"), 1<<20), []byte("\"}\n")...)...)
	if err := os.WriteFile(store.path(2), bytes.Repeat(line, MaxScanBytes>>20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadBlockLogs(context.Background(), 2, 2, "error", nil)
	if err == nil || !strings.Contains(err.Error(), "height 2") {
		t.Errorf("oversized scan should be rejected naming the height, got %v", err)
	}
}

// A crashed run leaves a partial file for the height CometBFT replays next;
// the replay must replace it, not append to it.
func TestBlockLogsReplayReplacesStaleFile(t *testing.T) {
	dir := t.TempDir()
	InitBlockLogs(dir, 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	WithBlockCapture(sdklog.NewNopLogger(), 7).Info("before crash")
	InitBlockLogs(dir, 10) // restart
	WithBlockCapture(sdklog.NewNopLogger(), 7).Info("replayed")
	EndBlock(7)

	out, err := ReadBlockLogs(context.Background(), 7, 7, "info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "before crash") || !strings.Contains(string(out), "replayed") {
		t.Errorf("replay should replace stale lines, got %s", out)
	}
}

// A line the scanner cannot buffer is an error, not a silently short array.
func TestBlockLogsScannerErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	InitBlockLogs(dir, 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	if err := os.WriteFile(store.path(3), append([]byte(`{"level":"info","msg":"`), append(bytes.Repeat([]byte("x"), 17<<20), []byte("\"}\n")...)...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadBlockLogs(context.Background(), 3, 3, "info", nil)
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("expected ErrTooLong, got %v", err)
	}
	// Node-side, so the handler owes a 5xx rather than a 400.
	if !errors.Is(err, ErrBlockRead) {
		t.Errorf("expected ErrBlockRead, got %v", err)
	}
}

// The first EndBlock after a restart sweeps files that a previous run left
// outside the current retention window.
func TestBlockLogsRestartSweep(t *testing.T) {
	dir := t.TempDir()
	for h := int64(1); h <= 10; h++ {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(h, 10)+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	InitBlockLogs(dir, 3)
	t.Cleanup(func() { InitBlockLogs("", 0) })
	EndBlock(10)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{"10.jsonl", "8.jsonl", "9.jsonl"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("sweep left %v, want %v", names, want)
	}
}

// The module tag is lifted out of fields and drives the modules filter; an
// empty filter means every module. Untagged lines file under DefaultModule.
func TestBlockLogsModuleFilter(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	l := WithBlockCapture(sdklog.NewNopLogger(), 5)
	l.With("module", "x/bank").Debug("burned")
	l.With("module", "x/thorchain").Info("swap", "k", "v")
	l.Info("untagged")
	EndBlock(5)

	read := func(mods ...string) []testEntry {
		raw, err := ReadBlockLogs(context.Background(), 5, 5, "debug", mods)
		if err != nil {
			t.Fatal(err)
		}
		var out []testEntry
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := read(); len(got) != 3 {
		t.Errorf("no filter should return all 3, got %v", got)
	}
	got := read("x/bank")
	if len(got) != 1 || got[0].Module != "x/bank" || got[0].Msg != "burned" {
		t.Errorf("x/bank filter: %v", got)
	}
	if _, dup := got[0].Fields["module"]; dup {
		t.Error("module should be lifted out of fields")
	}
	if got := read("x/thorchain", "x/bank"); len(got) != 3 {
		t.Errorf("two modules: %v", got)
	}
	if got := read(DefaultModule); len(got) != 2 || got[1].Msg != "untagged" {
		t.Errorf("untagged should file under %s: %v", DefaultModule, got)
	}
	if got := read("x/wasm"); len(got) != 0 {
		t.Errorf("unknown module: %v", got)
	}
}

// A constructor-time logger (x/bank) records only inside the block window.
func TestBlockLogsWindowCapture(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	l := WithWindowCapture(sdklog.NewNopLogger()).With("module", "x/bank")
	l.Info("between blocks")
	BeginBlock(8)
	l.Info("in block")
	EndBlock(8)
	l.Info("after")

	got := readEntries(t, 1, 8, "info")
	if len(got) != 1 || got[0].Msg != "in block" || got[0].Height != 8 || got[0].Module != "x/bank" {
		t.Errorf("window capture: %+v", got)
	}
}

// Only finalize mode is captured: check and simulate run the same code
// against the last committed height and must not be recorded.
func TestCaptureContextExecMode(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	ctx := sdk.NewContext(nil, cmtproto.Header{Height: 9}, false, sdklog.NewNopLogger())
	CaptureContext(ctx.WithExecMode(sdk.ExecModeSimulate)).Logger().Info("simulated")
	CaptureContext(ctx.WithExecMode(sdk.ExecModeCheck)).Logger().Info("checked")
	CaptureContext(ctx.WithExecMode(sdk.ExecModeFinalize)).Logger().Info("finalized")
	EndBlock(9)

	got := readEntries(t, 9, 9, "info")
	if len(got) != 1 || got[0].Msg != "finalized" {
		t.Errorf("only finalize should be captured: %+v", got)
	}
}

// A block whose file cannot be written is skipped after one failure rather
// than retried on every log line of the block.
func TestBlockLogsFailedHeightSkipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	InitBlockLogs(dir, 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	l := WithBlockCapture(sdklog.NewNopLogger(), 3)
	l.Info("first")
	if store.failed != 3 {
		t.Fatalf("failed height not recorded: %d", store.failed)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l.Info("second") // dir writable again, but this height stays skipped
	if _, err := os.Stat(store.path(3)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("height 3 should not be retried, stat: %v", err)
	}
	WithBlockCapture(sdklog.NewNopLogger(), 4).Info("next block")
	EndBlock(4)
	if got := readEntries(t, 4, 4, "info"); len(got) != 1 {
		t.Errorf("next block should capture normally: %v", got)
	}
}

// The directory is capped in bytes, oldest block first, and the accounting
// survives a restart.
func TestBlockLogsByteCeiling(t *testing.T) {
	dir := t.TempDir()
	old := maxDirBytes
	maxDirBytes = 2500
	t.Cleanup(func() { maxDirBytes = old; InitBlockLogs("", 0) })

	// a previous run left blocks 1-3 of ~1KiB each
	for h := int64(1); h <= 3; h++ {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(h, 10)+".jsonl"), bytes.Repeat([]byte("x"), 1000), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	InitBlockLogs(dir, 100)
	WithBlockCapture(sdklog.NewNopLogger(), 4).Info(strings.Repeat("y", 900))
	EndBlock(4) // 4 files, ~4KiB > cap: 1 and 2 go
	WithBlockCapture(sdklog.NewNopLogger(), 5).Info(strings.Repeat("y", 900))
	EndBlock(5) // 3, 4, 5 ~3KiB > cap: 3 goes

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := "4.jsonl,5.jsonl"; strings.Join(names, ",") != want {
		t.Errorf("byte ceiling left %v, want %s", names, want)
	}
}

// In-flight reads are bounded so the unauthenticated endpoint cannot fan
// out disk reads against the state disk.
func TestBlockLogsReadConcurrencyLimit(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	oldWait := readWait
	readWait = 10 * time.Millisecond
	t.Cleanup(func() { readWait = oldWait })

	for i := 0; i < cap(readSem); i++ {
		readSem <- struct{}{}
	}
	_, err := ReadBlockLogs(context.Background(), 1, 1, "info", nil)
	for i := 0; i < cap(readSem); i++ {
		<-readSem
	}
	if !errors.Is(err, ErrTooManyReads) {
		t.Errorf("saturated reads should be refused as retryable, got %v", err)
	}
	if _, err := ReadBlockLogs(context.Background(), 1, 1, "info", nil); err != nil {
		t.Errorf("read after drain: %v", err)
	}
}

// Warn and error lines flush the buffer so a crash right after one does not
// lose it, or the debug lines before it.
func TestBlockLogsFlushOnWarn(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	l := WithBlockCapture(sdklog.NewNopLogger(), 6)
	l.Debug("before")
	l.Error("boom")
	// no EndBlock: read the file as a crash would leave it
	raw, err := os.ReadFile(store.path(6))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("before")) || !bytes.Contains(raw, []byte("boom")) {
		t.Errorf("error line should flush everything so far, got %s", raw)
	}
}

type nilStringer struct{ s string }

func (n *nilStringer) String() string { return n.s }

// Capture calls String() on log values, which zerolog never did; a typed nil
// there must not take the block-execution goroutine down.
func TestBlockLogsCapturePanicIsSwallowed(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	l := WithBlockCapture(sdklog.NewNopLogger(), 7)
	l.Info("typed nil", "v", (*nilStringer)(nil))
	l.With("v", (*nilStringer)(nil)).Info("typed nil in With")
	EndBlock(7)
	got := readEntries(t, 7, 7, "info")
	if len(got) != 2 || got[0].Fields["v"] != "<nil>" || got[1].Fields["v"] != "<nil>" {
		t.Errorf("typed nil should stringify as <nil> on both paths: %+v", got)
	}
}

// A sweep whose directory listing failed is retried next block, and the
// retry recounts bytes rather than adding to the total closeFile kept.
func TestBlockLogsSweepRetryRecounts(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	for h := int64(1); h <= 3; h++ {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(h, 10)+".jsonl"), bytes.Repeat([]byte("x"), 1000), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	InitBlockLogs(dir, 100)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	if err := os.Chmod(dir, 0o300); err != nil { // writable, not listable
		t.Fatal(err)
	}
	WithBlockCapture(sdklog.NewNopLogger(), 4).Info("four")
	EndBlock(4)
	if store.swept {
		t.Fatal("sweep should be deferred when the listing fails")
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	WithBlockCapture(sdklog.NewNopLogger(), 5).Info("five")
	EndBlock(5)
	if !store.swept {
		t.Fatal("sweep should run once the listing works")
	}
	var onDisk int64
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		info, _ := e.Info()
		onDisk += info.Size()
	}
	if store.bytes != onDisk {
		t.Errorf("byte total %d, on disk %d", store.bytes, onDisk)
	}
}

// A windowed line (x/bank's constructor-time logger) can resolve its height
// just before EndBlock closes the block and reach the store afterwards. It
// must be discarded, not reopen the file and truncate the block's logs away.
func TestBlockLogsLateWindowLineDoesNotTruncate(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	l := WithWindowCapture(sdklog.NewNopLogger()).With("module", "x/bank")
	BeginBlock(11)
	l.Info("in block")
	EndBlock(11)

	// the state the racing goroutine is in once it takes the lock: height 11
	// resolved from the window, window since cleared
	store.write(11, true, "info", "late", nil)

	got := readEntries(t, 11, 11, "info")
	if len(got) != 1 || got[0].Msg != "in block" {
		t.Errorf("late windowed line should be dropped without clearing the block: %+v", got)
	}
}

// zlyzol on !5049: a range whose blocks were pruned long ago must not cost one
// os.Open per height. The walk is clamped to what the writer says is on disk,
// so a range past both ends is bounded rather than attempted.
func TestBlockLogsReadClampsToRetained(t *testing.T) {
	InitBlockLogs(t.TempDir(), 3)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	for h := int64(1); h <= 5; h++ {
		WithBlockCapture(sdklog.NewNopLogger(), h).Info("line")
		EndBlock(h)
	}

	// end past the tip and start below the retention floor: without the clamp
	// this range is 2^31 heights and trips MaxHeightsPerRead.
	got := readEntries(t, 1, math.MaxInt32, "info")
	if len(got) != 3 || got[0].Height != 3 || got[2].Height != 5 {
		t.Fatalf("want the 3 retained blocks, got %+v", got)
	}

	// entirely below the oldest retained block: empty, not an error
	raw, err := ReadBlockLogs(context.Background(), 1, 2, "info", nil)
	if err != nil {
		t.Fatalf("pruned range should be empty, not an error: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("pruned range: want [], got %s", raw)
	}
}

// Retention itself can be set wider than one request should walk, so a span
// that really is on disk is refused, naming the height to stop at.
func TestBlockLogsReadHeightCap(t *testing.T) {
	dir := t.TempDir()
	top := int64(MaxHeightsPerRead + 5)
	for _, h := range []int64{1, top} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(h, 10)+".jsonl"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	InitBlockLogs(dir, top*2)
	t.Cleanup(func() { InitBlockLogs("", 0) })
	EndBlock(top) // sweep: oldest = 1, lastClosed = top

	_, err := ReadBlockLogs(context.Background(), 1, top, "info", nil)
	if err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized span should be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(MaxHeightsPerRead)) {
		t.Errorf("error should name where to stop: %v", err)
	}
}

// A client that hangs up stops the scan instead of reading out its whole range.
func TestBlockLogsReadHonoursCancellation(t *testing.T) {
	InitBlockLogs(t.TempDir(), 100)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	for h := int64(1); h <= 10; h++ {
		WithBlockCapture(sdklog.NewNopLogger(), h).Info("line")
		EndBlock(h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadBlockLogs(ctx, 1, 10, "info", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
}

// Callers that merely arrive together must not be turned away: the regression
// suite prefetches its checks concurrently, and a fast 503 there is a flake,
// not a signal. Concurrency is still capped -- they queue.
func TestBlockLogsConcurrentReadsQueue(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	for h := int64(1); h <= 5; h++ {
		WithBlockCapture(sdklog.NewNopLogger(), h).Info("line")
		EndBlock(h)
	}

	const readers = 3 * 2 // comfortably over cap(readSem)
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		go func() {
			_, err := ReadBlockLogs(context.Background(), 1, 5, "info", nil)
			errs <- err
		}()
	}
	for i := 0; i < readers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("reader %d refused while under the cap: %v", i, err)
		}
	}
}

// huginn on !5049: before the first EndBlock nothing clamps the upper bound, and
// `for h := start; h <= end; h++` wraps at MaxInt64 and never terminates. The
// endpoint is unauthenticated, so that was a free way to pin a read slot.
func TestBlockLogsReadHeightBoundaries(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	// no EndBlock yet: lastClosed and oldest are both zero
	done := make(chan error, 1)
	go func() {
		_, err := ReadBlockLogs(context.Background(), math.MaxInt64-5, math.MaxInt64, "info", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("boundary range should read empty, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadBlockLogs did not terminate at the int64 boundary")
	}

	// a startup read with no arguments resolves to nothing captured yet
	raw, err := ReadBlockLogs(context.Background(), 0, 0, "info", nil)
	if err != nil {
		t.Fatalf("startup read should be empty, not an error: %v", err)
	}
	if string(raw) != "[]" {
		t.Errorf("startup read: want [], got %s", raw)
	}
}

// One runaway block must not sit above the directory ceiling until the next
// EndBlock prunes it, and the cut must be visible rather than look complete.
func TestBlockLogsPerBlockBudget(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	oldBudget := maxBlockBytes
	maxBlockBytes = 4096
	t.Cleanup(func() { maxBlockBytes = oldBudget; InitBlockLogs("", 0) })

	l := WithBlockCapture(sdklog.NewNopLogger(), 1)
	for i := 0; i < 200; i++ {
		l.Info(strings.Repeat("x", 200))
	}
	EndBlock(1)

	info, err := os.Stat(store.path(1))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxBlockBytes {
		t.Errorf("block file %d bytes, budget %d: capture did not stop in time", info.Size(), maxBlockBytes)
	}
	raw, err := ReadBlockLogs(context.Background(), 1, 1, "error", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "block log truncated") {
		t.Errorf("truncation should be recorded in the block, got %s", raw)
	}
}

// A gap in the retained heights must not be crawled a stat at a time inside
// EndBlock: the walk is bounded and the next sweep skips the gap outright.
func TestBlockLogsPruneBoundedOverGap(t *testing.T) {
	dir := t.TempDir()
	oldMax := maxDirBytes
	maxDirBytes = 2000
	t.Cleanup(func() { maxDirBytes = oldMax; InitBlockLogs("", 0) })

	// two live blocks far above a long-pruned region
	top := int64(maxPruneScan * 5)
	for _, h := range []int64{top, top + 1} {
		if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(h, 10)+".jsonl"), bytes.Repeat([]byte("x"), 1500), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	InitBlockLogs(dir, top*10) // retention wide enough that nothing ages out
	EndBlock(top + 1)          // sweep sets oldest to the real minimum

	start := time.Now()
	EndBlock(top + 2) // over budget: prune walks, bounded
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("pruneBytes took %v: it is crawling the gap", d)
	}
	if store.oldest > top+2 {
		t.Errorf("oldest ran past the live blocks: %d", store.oldest)
	}
}

// The top-level height dedup must not eat a call site's own "height": handlers
// log external chain heights under that key, and merge lets the call-site value
// win over the block height inherited from the module logger.
func TestBlockLogsHeightDedupKeepsForeignHeights(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	const block = int64(500)
	l := WithBlockCapture(sdklog.NewNopLogger(), block).With("height", block)
	l.Info("duplicate dropped")
	l.Info("external chain height kept", "height", int64(42))
	EndBlock(block)

	raw, err := ReadBlockLogs(context.Background(), block, block, "info", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Height int64          `json:"height"`
		Msg    string         `json:"msg"`
		Fields map[string]any `json:"fields"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("bad response %s: %v", raw, err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d: %s", len(got), raw)
	}
	if _, dup := got[0].Fields["height"]; dup {
		t.Errorf("duplicate height should be dropped: %+v", got[0].Fields)
	}
	if got[1].Fields["height"] != float64(42) {
		t.Errorf("external height should survive, got %+v", got[1].Fields)
	}
	if got[1].Height != block {
		t.Errorf("top-level height should still be the block: %d", got[1].Height)
	}
}

// A single entry bigger than the whole budget must be refused before it is
// written, not after: the file has to land inside maxBlockBytes so the
// truncation marker is still reachable by a read, whose scan cap is the same
// number. The marker must also fit the reserve the budget check holds back.
func TestBlockLogsOversizedEntryStaysInBudget(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	oldBudget := maxBlockBytes
	maxBlockBytes = 4096
	t.Cleanup(func() { maxBlockBytes = oldBudget; InitBlockLogs("", 0) })

	WithBlockCapture(sdklog.NewNopLogger(), 1).Info(strings.Repeat("x", 3*4096))
	EndBlock(1)

	info, err := os.Stat(store.path(1))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxBlockBytes {
		t.Errorf("block file %d bytes, above budget %d", info.Size(), maxBlockBytes)
	}
	raw, err := ReadBlockLogs(context.Background(), 1, 1, "error", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "block log truncated") {
		t.Errorf("truncation marker should be readable, got %s", raw)
	}
	if int64(len(raw)) > truncateReserve {
		t.Errorf("marker is %d bytes, reserve is %d: the budget check cannot guarantee room for it", len(raw), truncateReserve)
	}
}

// An entry longer than the reader's scanner buffer is dropped rather than
// stored: keeping it would fail every read of the whole block. The block goes
// on capturing.
func TestBlockLogsOverlongEntryDropped(t *testing.T) {
	InitBlockLogs(t.TempDir(), 10)
	oldLine := maxLineBytes
	maxLineBytes = 512
	t.Cleanup(func() { maxLineBytes = oldLine; InitBlockLogs("", 0) })

	l := WithBlockCapture(sdklog.NewNopLogger(), 1)
	l.Info(strings.Repeat("x", 2048))
	l.Info("kept")
	EndBlock(1)

	raw, err := ReadBlockLogs(context.Background(), 1, 1, "info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), strings.Repeat("x", 2048)) {
		t.Error("an unreadable entry should not be stored")
	}
	if !strings.Contains(string(raw), "entry exceeds line limit") {
		t.Errorf("the drop should be recorded in the block, got %s", raw)
	}
	if !strings.Contains(string(raw), "kept") {
		t.Errorf("capture should continue after a dropped entry, got %s", raw)
	}
}

// A stale file the sweep cannot unlink stays in the byte total and leaves
// swept clear: dropping it would hide those bytes from the directory ceiling
// for good, since nothing revisits a height below the cutoff. The next block
// retries it.
func TestBlockLogsSweepKeepsUndeletedFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	stale := filepath.Join(dir, "1.jsonl")
	if err := os.WriteFile(stale, bytes.Repeat([]byte("x"), 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	InitBlockLogs(dir, 2)
	t.Cleanup(func() { InitBlockLogs("", 0) })

	if err := os.Chmod(dir, 0o500); err != nil { // listable, not writable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	EndBlock(5) // cutoff 3, so 1.jsonl is stale but cannot be unlinked
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("stale file should still be on disk: %v", err)
	}
	if store.bytes != 1000 {
		t.Errorf("undeleted file should stay in the byte total, got %d", store.bytes)
	}
	if store.swept {
		t.Error("a failed delete should clear swept so the next block retries")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	EndBlock(6)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the retry should have removed the stale file: %v", err)
	}
	if store.bytes != 0 {
		t.Errorf("byte total should be 0 once the file is gone, got %d", store.bytes)
	}
}
