package log

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	sdklog "cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog/log"
)

// Block logs capture module log lines emitted while finalizing a block into
// <dir>/<height>.jsonl so they can be served by /thorchain/logs. This is
// node-local, off-consensus state: every error here is swallowed.
//
// Capture is installed on the context logger at the finalize-mode entry
// points (pre/begin/end blockers and the ante handler), so any module that
// logs through ctx.Logger() is covered. x/bank keeps a logger from
// construction time instead; WithWindowCapture serves it using the height
// window opened by BeginBlock and closed by EndBlock.

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// DefaultModule is recorded for lines with no module tag. In finalize mode
// those come from thornode's own code outside the module (ante decorators,
// app hooks), so they belong with the thorchain module's lines.
const DefaultModule = "x/thorchain"

// MaxResponseBytes bounds one ReadBlockLogs result; retain_blocks bounds file
// count, not bytes, so an oversized range is rejected rather than buffered.
// Kept under the sdk's 10MiB grpc-gateway recv limit so the client sees this
// error instead of a resource-exhausted from the gateway.
const MaxResponseBytes = 8 << 20

// MaxScanBytes bounds input examined per read: MaxResponseBytes only counts
// lines that pass the level filter, so without this a high level over a large
// window would read the whole retention dir per request.
const MaxScanBytes = 64 << 20

// MaxHeightsPerRead bounds how many block files one request may attempt. The
// byte caps only count bytes actually read, so a range over absent heights is
// otherwise unbounded work.
const MaxHeightsPerRead = 10000

// maxBlockBytes caps one block's file. The directory ceiling only counts closed
// files and never the block being written, so without this a single pathological
// block can blow past it and stay there. Set to MaxScanBytes so any retained
// block is always readable in one request: the budget is enforced before each
// entry is written, so the file never lands above it and ReadBlockLogs, whose
// scan cap is the same number, can always reach the truncation marker.
var maxBlockBytes int64 = MaxScanBytes

// truncateReserve is held back from maxBlockBytes so the truncation marker
// always fits once the budget is refused. TestBlockLogsPerBlockBudget asserts
// the marker stays inside it.
const truncateReserve = 1 << 10

// maxLineBytes caps a single captured entry, on both sides of the file: the
// reader's scanner fails the whole block on a longer line, so an entry it
// could never read back must not be written in the first place.
// ponytail: var so tests need not build a 16MiB line.
var maxLineBytes = 16 << 20

// maxDirBytes caps the whole log directory: retain_blocks bounds file count
// but a busy block at debug level is megabytes, and the files share the data
// disk with the state store. Oldest blocks are pruned first once exceeded.
// ponytail: constant, make it config if an operator needs more than 1GiB.
var maxDirBytes int64 = 1 << 30

// ErrTooManyReads is returned when no read slot frees up within readWait.
// Retryable, like ErrBlockRead; every other error from ReadBlockLogs is the
// caller's to fix.
var ErrTooManyReads = errors.New("too many concurrent log reads, retry shortly")

// ErrBlockRead wraps a failure to read a block file: a filesystem error, or a
// captured line longer than the scanner's buffer. Node-side, so unlike the
// rest of ReadBlockLogs' errors it is not the caller's to fix, and repeating
// the identical request is the right response.
var ErrBlockRead = errors.New("block log read failed")

// readSem bounds in-flight ReadBlockLogs calls: each may read MaxScanBytes
// from the state disk and hold MaxResponseBytes, and the endpoint is
// unauthenticated.
var readSem = make(chan struct{}, 2)

// readWait is how long a read queues for a slot before giving up. Bounding
// concurrency is about disk I/O, not about refusing callers: a client that
// simply arrived alongside another should wait, not retry. Waiting costs a
// parked goroutine and nothing else, and a caller that loses patience first
// cancels its own request. ponytail: var so tests need not wait it out.
var readWait = 15 * time.Second

type blockLogStore struct {
	dir    string
	retain int64

	mu     sync.Mutex
	height int64
	f      *os.File
	bw     *bufio.Writer
	failed int64 // height whose file could not be written; skipped until the next block
	cur    int64 // bytes written for the open height, against maxBlockBytes
	swept  bool
	bytes  int64 // total size of closed files, maintained after the sweep
	oldest int64 // lowest height still on disk, for byte-cap pruning
	// lastClosed is the highest block whose file is complete: the read
	// default and upper bound, standing in for the last committed height
	// now that reads no longer go through an sdk context.
	lastClosed int64
}

// store is nil when the feature is disabled (retain_blocks = 0).
var store *blockLogStore

// window is the height being finalized, 0 between blocks. ABCI calls are
// serialized by the sdk's local client, so nothing else runs a block while
// it is set. Cleared before EndBlock takes the store lock, so a write that
// resolved its height from here can re-check under that lock whether the
// block is still open.
// ponytail: gRPC Simulate bypasses ABCI and can race a block; a bank debug
// line from such a simulation lands in the block file while the window is
// open. Scope via ctx if that ever matters.
var window atomic.Int64

// InitBlockLogs enables per-block log capture into dir, keeping retain blocks.
// retain <= 0 disables the feature.
func InitBlockLogs(dir string, retain int64) {
	if store != nil {
		store.mu.Lock()
		store.closeFile()
		store.mu.Unlock()
	}
	if retain <= 0 {
		store = nil
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Error().Err(err).Str("dir", dir).Msg("fail to create block log dir, capture disabled")
		store = nil
		return
	}
	store = &blockLogStore{dir: dir, retain: retain}
}

// WithBlockCapture wraps inner so every line is also recorded for height.
// When capture is disabled inner is returned unchanged.
func WithBlockCapture(inner sdklog.Logger, height int64) sdklog.Logger {
	if store == nil {
		return inner
	}
	return captureLogger{inner: inner, height: height}
}

// WithWindowCapture wraps inner so lines emitted while a block is being
// finalized are recorded for that block; lines outside the window are only
// forwarded. For loggers held from construction time with no ctx.
func WithWindowCapture(inner sdklog.Logger) sdklog.Logger {
	return WithBlockCapture(inner, 0)
}

// CaptureContext returns ctx with block capture on its logger when ctx is
// finalizing a block. Other exec modes (check, simulate) run the same code
// speculatively against the last committed height and are left alone.
//
// Each of PreBlocker/BeginBlocker/EndBlocker and the ante handler calls this
// on its own ctx rather than one wrapped context being threaded through them.
// That does not stack wrappers or leak capture between phases: baseapp holds
// finalizeBlockState's ctx itself and none of these callbacks' return values
// mutate it, so each phase starts from an unwrapped logger.
func CaptureContext(ctx sdk.Context) sdk.Context {
	if store == nil || ctx.ExecMode() != sdk.ExecModeFinalize {
		return ctx
	}
	return ctx.WithLogger(WithBlockCapture(ctx.Logger(), ctx.BlockHeight()))
}

// BeginBlock opens the capture window for height.
func BeginBlock(height int64) {
	window.Store(height)
}

// EndBlock closes the window and file for height and prunes the block that
// fell out of retention. The first call also sweeps stale files left by a
// restart or a retention change.
func EndBlock(height int64) {
	window.Store(0)
	if store == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.closeFile()
	store.lastClosed = height
	cutoff := height - store.retain
	if !store.swept {
		if store.sweep(height, cutoff) {
			store.pruneBytes(height)
			return
		}
		// listing failed: fall through to the steady-state prune, retry next block
	}
	if cutoff > 0 {
		store.remove(cutoff)
		if store.oldest <= cutoff {
			store.oldest = cutoff + 1
		}
	}
	store.pruneBytes(height)
}

// sweep removes files at or below cutoff and recounts the rest. Reports
// whether the dir was listed.
//
// A file it cannot delete stays in the byte total and clears swept, so the
// next EndBlock lists the directory again and retries it. Dropping it from
// the count instead would hide those bytes from the ceiling for good: nothing
// else ever revisits a height below cutoff.
// ponytail: the retry is one ReadDir per block for as long as the delete keeps
// failing, same ceiling as the listing-failure path below. Back it off if a
// node is ever seen stuck there.
func (s *blockLogStore) sweep(height, cutoff int64) bool {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		log.Error().Err(err).Str("dir", s.dir).Msg("fail to list block log dir, sweep deferred")
		return false
	}
	s.swept, s.oldest, s.bytes = true, height, 0 // recount: a deferred sweep would otherwise double count
	for _, e := range entries {
		h, err := strconv.ParseInt(trimExt(e.Name()), 10, 64)
		if err != nil {
			continue
		}
		if h <= cutoff {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err == nil {
				continue
			}
			s.swept = false // still on disk: count it below and retry next block
		}
		if info, err := e.Info(); err == nil {
			s.bytes += info.Size()
		}
		if h < s.oldest {
			s.oldest = h
		}
	}
	return true
}

// remove deletes the file for height and drops its size from the total. The
// size comes off only once the file is actually gone: a failed delete that
// still decremented would hide those bytes from the ceiling forever, since
// oldest advances past the height either way. A sweep retries the leftover.
func (s *blockLogStore) remove(height int64) {
	info, statErr := os.Stat(s.path(height))
	if err := os.Remove(s.path(height)); err != nil {
		return
	}
	if statErr == nil {
		s.bytes -= info.Size()
	}
}

// maxPruneScan bounds how many heights one EndBlock walks looking for bytes to
// free. Absent heights cost a stat apiece, and disabling capture then
// re-enabling it with a longer retention leaves gaps thousands of blocks wide.
const maxPruneScan = 1000

// pruneBytes drops the oldest blocks until the dir fits maxDirBytes, never
// touching height itself. This runs inside EndBlock under s.mu, so it walks a
// bounded number of heights: if that is not enough, the next block re-sweeps,
// which reads the directory once and skips the gap entirely instead of
// crawling it a stat at a time.
func (s *blockLogStore) pruneBytes(height int64) {
	for i := 0; s.bytes > maxDirBytes && s.oldest < height; i++ {
		if i == maxPruneScan {
			s.swept = false // recompute oldest from the directory next block
			return
		}
		s.remove(s.oldest)
		s.oldest++
	}
}

// ReadBlockLogs returns a JSON array of the captured entries for heights
// [start, end] at or above minLevel from the given modules (all when empty).
// Zero start or end means the last block whose file was closed. Heights with
// no file are skipped. ctx cancels the scan between heights.
func ReadBlockLogs(ctx context.Context, start, end int64, minLevel string, modules []string) ([]byte, error) {
	s := store // snapshot: InitBlockLogs may swap the global
	if s == nil {
		return nil, errors.New("block log capture is disabled")
	}
	minRank, ok := levelRank[minLevel]
	if !ok {
		return nil, fmt.Errorf("unknown level %q, expected one of debug, info, warn, error", minLevel)
	}
	// Brief lock: the writer's view of what is actually on disk. Released
	// before the scan below, which must not hold up block execution.
	s.mu.Lock()
	oldest, last := s.oldest, s.lastClosed
	s.mu.Unlock()

	if start == 0 {
		start = last
	}
	if end == 0 {
		end = last
	}
	if start > end {
		return nil, fmt.Errorf("start %d is after end %d", start, end)
	}
	want := make(map[string]bool, len(modules))
	for _, m := range modules {
		want[m] = true
	}
	// Bound the walk to heights that can still exist. Without this a caller
	// asking for a long-pruned range makes one os.Open per height, work that
	// neither byte cap counts because a failed open reads nothing.
	if last > 0 && end > last {
		end = last
	}
	if oldest > 0 && start < oldest {
		start = oldest
	}
	if lowest := end - s.retain + 1; start < lowest {
		start = lowest
	}
	if start < 1 {
		// Before the first EndBlock nothing is clamped, and heights below 1
		// make the span arithmetic below meaningless.
		start = 1
	}
	if start > end {
		return []byte("[]"), nil // nothing retained in the requested range
	}
	span := end - start + 1 // safe: start >= 1, so this cannot overflow
	if span > MaxHeightsPerRead {
		return nil, fmt.Errorf("range spans %d blocks, at most %d per request, end the range at height %d", span, MaxHeightsPerRead, start+MaxHeightsPerRead-1)
	}
	wait := time.NewTimer(readWait)
	defer wait.Stop()
	select {
	case readSem <- struct{}{}:
		defer func() { <-readSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wait.C:
		return nil, ErrTooManyReads
	}

	// No s.mu across the scan: it needs only dir/retain (immutable after init)
	// and its own file handles. Holding the writer's lock across this disk
	// scan would stall every module log line on the block-execution goroutine.
	// ponytail: whole range buffered in memory, MaxResponseBytes bounds it
	var buf bytes.Buffer
	buf.WriteByte('[')
	scanned := 0
	// Counted rather than `h <= end`: with end at MaxInt64 the increment wraps
	// and the loop never terminates, which before the first EndBlock (no upper
	// clamp) was reachable from the unauthenticated endpoint.
	for i := int64(0); i < span; i++ {
		h := start + i
		if err := ctx.Err(); err != nil {
			return nil, err // client hung up: stop reading its range
		}
		f, err := os.Open(s.path(h))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, maxLineBytes)
		for sc.Scan() {
			line := sc.Bytes()
			if scanned += len(line) + 1; scanned > MaxScanBytes {
				_ = f.Close()
				return nil, fmt.Errorf("range exceeds %d bytes of log at height %d, end the range before it", MaxScanBytes, h)
			}
			var e struct {
				Level  string `json:"level"`
				Module string `json:"module"`
			}
			if json.Unmarshal(line, &e) != nil || levelRank[e.Level] < minRank || (len(want) > 0 && !want[e.Module]) {
				continue // malformed (partial write) or filtered
			}
			if buf.Len() > 1 {
				buf.WriteString(",\n")
			}
			buf.Write(line)
			if buf.Len() > MaxResponseBytes {
				_ = f.Close()
				return nil, fmt.Errorf("response exceeds %d bytes at height %d, end the range before it or raise level", MaxResponseBytes, h)
			}
		}
		_ = f.Close()
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("%w: block %d: %w", ErrBlockRead, h, err)
		}
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

func (s *blockLogStore) path(height int64) string {
	return filepath.Join(s.dir, strconv.FormatInt(height, 10)+".jsonl")
}

func (s *blockLogStore) closeFile() {
	if s.f != nil {
		_ = s.bw.Flush()
		if info, err := s.f.Stat(); err == nil {
			s.bytes += info.Size()
		}
		_ = s.f.Close()
		s.f, s.bw, s.height = nil, nil, 0
	}
}

// windowed marks a line whose height came from the window rather than from a
// finalize context: it may have been emitted on another goroutine (gRPC
// Simulate) and arrive after EndBlock closed the height, where reopening the
// file would O_TRUNC the block's logs away. window is cleared before EndBlock
// takes this lock, so re-checking it here discards exactly the late lines.
func (s *blockLogStore) write(height int64, windowed bool, level, msg string, fields map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if windowed && window.Load() != height {
		return
	}
	if s.failed == height {
		return // reported once; this runs on the block-execution goroutine
	}
	if s.f == nil || s.height != height {
		s.closeFile()
		// O_TRUNC: a height is opened once per process, so a pre-existing file
		// is stale output from a crashed run that CometBFT is now replaying.
		f, err := os.OpenFile(s.path(height), os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			s.failed = height
			log.Error().Err(err).Int64("height", height).Msg("fail to open block log file, skipping block")
			return
		}
		// buffered: one write(2) per line on the block-execution goroutine is
		// not free. Flushed on warn/error lines and when the block ends, so a
		// process crash inside a blocker can lose the last few KiB of debug
		// lines of that block, never a warning or error.
		s.f, s.height, s.cur = f, height, 0
		s.bw = bufio.NewWriter(f)
	}
	module, _ := fields["module"].(string)
	delete(fields, "module")
	// height is stamped at the top level and the module loggers pass it as a
	// field too, so drop the duplicate on what is the highest-volume line in
	// the file. Only the duplicate: call sites log external chain heights under
	// this same key, and merge lets a call-site value win over the inherited
	// one, so an unconditional delete would silently discard the height the
	// message is actually about while the entry still shows the block height.
	if h, ok := fields["height"].(int64); ok && h == height {
		delete(fields, "height")
	}
	if module == "" {
		module = DefaultModule
	}
	entry := map[string]any{
		"height": height,
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
		"level":  level,
		"module": module,
		"msg":    msg,
		"fields": fields,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		s.fail(height, err, "fail to encode block log entry, skipping block")
		return
	}
	b = append(b, '\n')
	if len(b) > maxLineBytes {
		// Dropped rather than written: the reader's scanner errors on the
		// first line past its buffer, so storing this entry would cost the
		// whole block. The rest of the block is still worth capturing.
		_ = s.marker(height, "block log entry dropped: entry exceeds line limit", "limit_bytes", int64(maxLineBytes))
		log.Error().Int64("height", height).Int("entry_bytes", len(b)).
			Msg("block log entry exceeds line limit, dropping entry")
		return
	}
	if s.cur+int64(len(b))+truncateReserve > maxBlockBytes {
		// Stop this block rather than let one runaway block sit above the
		// directory ceiling until the next EndBlock prunes it. Refused before
		// the write, with the marker's room held back, so the file lands
		// inside the budget and the marker is still readable in one request.
		_ = s.marker(height, "block log truncated: block exceeded capture budget", "budget_bytes", maxBlockBytes)
		s.failed = height
		log.Error().Int64("height", height).Int64("budget", maxBlockBytes).
			Msg("block log exceeded per-block budget, truncating block")
		s.closeFile()
		return
	}
	if _, err := s.bw.Write(b); err != nil {
		s.fail(height, err, "fail to write block log entry, skipping block")
		return
	}
	// Counted on accept, not on flush: bytes sitting in bw are already spent
	// against the budget, and a count taken from the file would lag behind by
	// a buffer's worth and let the block overshoot.
	s.cur += int64(len(b))
	if levelRank[level] >= levelRank["warn"] {
		if err := s.bw.Flush(); err != nil {
			s.fail(height, err, "fail to flush block log entry, skipping block")
		}
	}
}

// marker records an operator-visible note in the block file. Markers bypass
// the budget check, which reserves truncateReserve for exactly this, but their
// bytes are still counted so repeated markers end the block rather than run
// past the cap.
func (s *blockLogStore) marker(height int64, msg, field string, value int64) error {
	b, err := json.Marshal(map[string]any{
		"height": height,
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
		"level":  "error",
		"module": DefaultModule,
		"msg":    msg,
		"fields": map[string]any{field: value},
	})
	if err != nil {
		return err
	}
	n, err := s.bw.Write(append(b, '\n'))
	s.cur += int64(n)
	return err
}

// fail gives up on height: the file is closed and the block skipped until the
// next one opens a fresh file.
func (s *blockLogStore) fail(height int64, err error, msg string) {
	s.failed = height
	log.Error().Err(err).Int64("height", height).Msg(msg)
	s.closeFile()
}

func trimExt(name string) string {
	return name[:len(name)-len(filepath.Ext(name))]
}

// captureLogger records every line to the block log store, then forwards it
// to the wrapped logger. Recording happens before the wrapped logger's level
// filter, so debug lines are stored even when the node runs at info.
// height 0 means "the block currently being finalized", see window.
type captureLogger struct {
	inner  sdklog.Logger
	height int64
	fields map[string]any
}

func (c captureLogger) Debug(msg string, kv ...any) {
	c.record("debug", msg, kv)
	c.inner.Debug(msg, kv...)
}

func (c captureLogger) Info(msg string, kv ...any) {
	c.record("info", msg, kv)
	c.inner.Info(msg, kv...)
}

func (c captureLogger) Warn(msg string, kv ...any) {
	c.record("warn", msg, kv)
	c.inner.Warn(msg, kv...)
}

func (c captureLogger) Error(msg string, kv ...any) {
	c.record("error", msg, kv)
	c.inner.Error(msg, kv...)
}
func (c captureLogger) Impl() any { return c.inner.Impl() }

func (c captureLogger) With(kv ...any) sdklog.Logger {
	return captureLogger{inner: c.inner.With(kv...), height: c.height, fields: c.merge(kv)}
}

func (c captureLogger) record(level, msg string, kv []any) {
	s := store // snapshot, see ReadBlockLogs
	if s == nil {
		return
	}
	h, windowed := c.height, false
	if h == 0 {
		windowed = true
		if h = window.Load(); h == 0 {
			return // not finalizing a block
		}
	}
	s.write(h, windowed, level, msg, c.merge(kv))
}

func (c captureLogger) merge(kv []any) map[string]any {
	out := make(map[string]any, len(c.fields)+len(kv)/2)
	for k, v := range c.fields {
		out[k] = v
	}
	for k, v := range getLogFields(kv...) {
		out[k] = stringify(v)
	}
	return out
}

// ponytail: non-scalar values are stringified so json encoding never fails on
// sdk types; keep typed values if a consumer ever needs them.
// Calling String()/Error() is new here (zerolog never did), so a typed-nil
// pointer must not take the block down: fall back to fmt, which prints <nil>.
func stringify(v any) (out any) {
	defer func() {
		if recover() != nil {
			out = fmt.Sprintf("%v", v)
		}
	}()
	switch x := v.(type) {
	case nil, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return x
	case error:
		return x.Error()
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprintf("%v", x)
	}
}
