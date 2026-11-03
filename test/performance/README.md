# Quote API Performance Testing Framework

This directory contains a comprehensive performance testing framework for the THORChain quotes API, specifically designed to identify bottlenecks when the advanced swap queue is enabled.

## Overview

The framework provides three main testing approaches:

1. **Benchmark Tests** - Go benchmark tests with pprof profiling
2. **Load Testing** - HTTP load testing against a running node
3. **Profile Analysis** - Automated analysis of profiling data

## Quick Start

### Using Make Commands (Recommended)

The easiest way to run performance tests:

```bash
# Quick performance check (10 iterations, ~2 seconds)
make test-performance-quick

# Comprehensive benchmarks with profiling (recommended for analysis)
make test-performance

# Run all benchmark variants
make test-performance-all

# Analyze latest results
make test-performance-analyze

# Open interactive CPU profile viewer
make test-performance-profile-cpu

# Open interactive memory profile viewer
make test-performance-profile-mem

# View execution trace
make test-performance-trace

# Clean all performance results
make test-performance-clean
```

### Using Scripts Directly

Alternatively, run scripts directly:

```bash
# Run all benchmarks with profiling
./test/performance/run_quote_benchmarks.sh

# Or run manually with specific profiles
cd x/thorchain
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
    -benchtime=10x \
    -cpuprofile=cpu.prof \
    -memprofile=mem.prof \
    -benchmem
```

## Make Commands Reference

### Performance Testing Commands

| Command                       | Description                                                 | Duration     |
| ----------------------------- | ----------------------------------------------------------- | ------------ |
| `make test-performance-quick` | Quick benchmark comparison (Queue ON/OFF)                   | ~2 seconds   |
| `make test-performance`       | Full benchmarks with CPU/memory/block/mutex/trace profiling | ~2-3 minutes |
| `make test-performance-all`   | All benchmark variants without profiling                    | ~2 seconds   |

### Profile Analysis Commands

| Command                             | Description                                              |
| ----------------------------------- | -------------------------------------------------------- |
| `make test-performance-analyze`     | Analyze latest results and generate reports              |
| `make test-performance-profile-cpu` | Open interactive CPU profile at http://localhost:8080    |
| `make test-performance-profile-mem` | Open interactive memory profile at http://localhost:8080 |
| `make test-performance-trace`       | View execution trace timeline                            |

### Utility Commands

| Command                       | Description                         |
| ----------------------------- | ----------------------------------- |
| `make test-performance-clean` | Remove all performance test results |

### 2. Analyze Profiling Data

After running benchmarks, analyze the results:

```bash
# Automated analysis
./test/performance/analyze_profiles.sh test/performance/results/<timestamp>

# Or manually view interactive profiles
go tool pprof -http=:8080 test/performance/results/<timestamp>/cpu.prof
```

### 3. Load Test a Running Node

Test against a live mocknet instance:

```bash
# Start mocknet
make run-mocknet

# In another terminal, run load test
./test/performance/quotes_load_test.sh http://localhost:1317 60 10

# Stop mocknet when done
make stop-mocknet
```

## Benchmark Tests

### Available Benchmarks

- **BenchmarkQuoteSwapDisabled** - Baseline with advanced swap queue disabled
- **BenchmarkQuoteSwapEnabled** - Performance with advanced swap queue enabled
- **BenchmarkQuoteSwapEnabledWithQueue** - With pre-populated swap queue
- **BenchmarkQuoteSwapStreaming** - Streaming swap quotes
- **BenchmarkQuoteSwapComparison** - Side-by-side comparison (recommended)

### Running Individual Benchmarks

```bash
cd x/thorchain

# Run specific benchmark
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' -benchtime=10x

# Run all benchmarks
go test -run='^$' -bench=. -benchtime=5x -benchmem

# With CPU profiling
go test -run='^$' -bench=. -cpuprofile=cpu.prof -benchmem

# With memory profiling
go test -run='^$' -bench=. -memprofile=mem.prof -benchmem

# With execution trace
go test -run='^$' -bench=. -trace=trace.out
```

### Understanding Benchmark Output

```text
BenchmarkQuoteSwapComparison/Queue_Disabled-12    3    538083 ns/op
BenchmarkQuoteSwapComparison/Queue_Enabled-12     3  41054306 ns/op
```

- **First number** (3): Number of iterations
- **Second number** (538083/41054306): Nanoseconds per operation
- **-12**: Number of CPUs used

In this example, Queue_Enabled is **76x slower** than Queue_Disabled!

## Profile Analysis

### CPU Profile

Identifies which functions consume the most CPU time:

```bash
# Text report (top functions)
go tool pprof -text -flat cpu.prof

# Interactive web UI (recommended)
go tool pprof -http=:8080 cpu.prof

# Call graph
go tool pprof -dot cpu.prof > callgraph.dot
dot -Tpng callgraph.dot -o callgraph.png
```

### Memory Profile

Identifies memory allocation hotspots:

```bash
# Allocation sites
go tool pprof -text -alloc_space mem.prof

# In-use memory
go tool pprof -text -inuse_space mem.prof

# Interactive
go tool pprof -http=:8080 mem.prof
```

### Block Profile

Identifies goroutine blocking:

```bash
go tool pprof -http=:8080 block.prof
```

### Execution Trace

Detailed timeline of execution:

```bash
go tool trace trace.out
```

This opens a web UI showing:

- Goroutine timelines
- CPU utilization
- Synchronization blocking
- System calls

## Load Testing

### Basic Usage

```bash
./quotes_load_test.sh <thornode_url> [duration_seconds] [concurrent_requests]
```

### Examples

```bash
# Test local mocknet for 60 seconds with 10 concurrent requests
./quotes_load_test.sh http://localhost:1317 60 10

# Quick 30-second test
./quotes_load_test.sh http://localhost:1317 30 5

# Stress test with high concurrency
./quotes_load_test.sh http://localhost:1317 120 50
```

### Load Test Output

The script provides:

- Real-time progress with requests/second
- Per-test-case statistics (min, max, mean, median, p50/p95/p99)
- Error tracking
- Response samples saved for inspection

## Finding Performance Bottlenecks

### Step-by-Step Analysis

1. **Run the comparison benchmark:**

   ```bash
   cd x/thorchain
   go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
       -benchtime=10x -cpuprofile=cpu.prof -memprofile=mem.prof
   ```

2. **Check the speedup/slowdown:**

   ```text
   Queue_Disabled:  ~0.5 ms/op  (baseline)
   Queue_Enabled:  ~41.0 ms/op  (76x slower!)
   ```

3. **Analyze CPU profile:**

   ```bash
   go tool pprof -http=:8080 cpu.prof
   ```

4. **Look for hotspots in the web UI:**

   - View → Top
   - View → Flame Graph (shows call hierarchy)
   - View → Source (shows specific lines of code)

5. **Check for common issues:**
   - Database operations (GetAdvSwapQueue*, SetAdvSwapQueue*)
   - JSON marshaling/unmarshaling
   - Reflection usage
   - Inefficient algorithms (O(n²) loops, etc.)

### Common Bottlenecks to Look For

#### Advanced Swap Queue Operations

```bash
# Search for queue operations in profile
go tool pprof -text cpu.prof | grep -i "AdvSwap"
```

Potential issues:

- `GetAdvSwapQueueIterator` - Full queue iteration
- `SetAdvSwapQueueItem` - Individual item writes
- `FetchQueue` - Queue fetching logic
- `applyPartnerMatching` - O(n²) matching algorithm

#### Database/KVStore Operations

```bash
go tool pprof -text cpu.prof | grep -E "(Iterator|KVStore|Get|Set)"
```

Look for:

- Excessive Iterator usage
- Repeated Get/Set calls in loops
- Missing caching

#### Serialization Overhead

```bash
go tool pprof -text cpu.prof | grep -E "(json|proto|Marshal|Unmarshal)"
```

## Testing Scenarios

### Scenario 1: Empty Queue vs Full Queue

```bash
# Run benchmarks that test both scenarios
go test -run='^$' -bench='BenchmarkQuoteSwap(Enabled|EnabledWithQueue)' -benchtime=10x
```

### Scenario 2: Different Swap Types

The benchmarks test various swap scenarios:

- Simple swaps (BTC → RUNE)
- Double swaps (ETH → BTC)
- Small amounts vs large amounts
- Streaming swaps

### Scenario 3: Load Testing Under Realistic Conditions

```bash
# Start mocknet with advanced swap queue enabled
make run-mocknet

# Set mimir value
# ... (use thornode CLI or API to set EnableAdvSwapQueue=1)

# Run load test
./quotes_load_test.sh http://localhost:1317 300 20
```

## Interpreting Results

### CPU Profile Metrics

- **flat %**: Time spent in this function only (not callees)
- **flat**: Absolute time in this function only
- **sum %**: Cumulative percentage
- **cum %**: Time spent in this function and callees
- **cum**: Absolute time in this function and callees

### Memory Profile Metrics

- **alloc_space**: Total memory allocated (includes freed memory)
- **alloc_objects**: Total objects allocated
- **inuse_space**: Memory currently in use
- **inuse_objects**: Objects currently in use

### What to Optimize

Focus on:

1. **High flat %** - Direct optimization target
2. **High cum % with low flat %** - Caller of expensive functions
3. **Frequent allocations** - Consider object pooling
4. **Large allocations** - Consider streaming or chunking

## Tips and Best Practices

### Benchmark Testing

- Run benchmarks multiple times to ensure consistency
- Use `-benchtime=Nx` for N iterations (e.g., 10x, 100x)
- Profile on representative hardware
- Disable CPU frequency scaling for consistent results
- Close other applications to reduce noise

### Profile Analysis Tips

- Start with CPU profile (usually the main bottleneck)
- Check memory profile if you see GC pressure
- Use execution trace for concurrency issues
- Compare profiles before/after optimizations

### Load Testing Tips

- Test against a mocknet that matches production config
- Ramp up load gradually
- Monitor system resources (CPU, memory, disk I/O)
- Test different time-of-day scenarios
- Include error rate analysis

## Troubleshooting

### "bad version" Error in Benchmarks

This indicates version initialization issues. The framework handles this automatically, but if you see it:

```bash
# Ensure mocknet is properly configured
export NET=mocknet
```

### Empty or Missing Profiles

If profiles are empty:

```bash
# Ensure you're running enough iterations
go test -bench=. -benchtime=100x -cpuprofile=cpu.prof

# Check that the profile was actually generated
ls -lh *.prof
```

### Load Test Connection Errors

If load tests can't connect:

```bash
# Check if mocknet is running
curl http://localhost:1317/thorchain/health

# Check logs
make logs-mocknet
```

## Next Steps

After identifying bottlenecks:

1. **Document findings** - Note specific functions and line numbers
2. **Propose optimizations** - Consider caching, batching, algorithm changes
3. **Implement fixes** - Make targeted optimizations
4. **Re-run benchmarks** - Verify improvements
5. **Compare profiles** - Use `pprof -base` to compare before/after

## Example Workflow

Complete workflow for investigating quote API slowness:

```bash
# 1. Run benchmarks and capture profiles
cd x/thorchain
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
    -benchtime=20x \
    -cpuprofile=cpu.prof \
    -memprofile=mem.prof \
    -benchmem | tee results.txt

# 2. Analyze results
cat results.txt
# Note: Queue_Enabled is 76x slower!

# 3. View CPU profile
go tool pprof -http=:8080 cpu.prof
# Navigate to Flame Graph view
# Identify hotspots (e.g., GetAdvSwapQueueIterator at 45% CPU)

# 4. Check specific functions
go tool pprof -text cpu.prof | grep -i "advswap" | head -20

# 5. Examine source code at identified line numbers
# ... implement optimizations ...

# 6. Re-run benchmarks
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
    -benchtime=20x \
    -cpuprofile=cpu_after.prof

# 7. Compare before/after
go tool pprof -base=cpu.prof cpu_after.prof
```

## Additional Resources

- [Go Profiling Documentation](https://go.dev/doc/diagnostics)
- [pprof User Guide](https://github.com/google/pprof/blob/main/doc/README.md)
- [High Performance Go Workshop](https://dave.cheney.net/high-performance-go-workshop/gopherchina-2019.html)

## Perf iteration (mainnet-weighted)

A second framework targets the code mainnet actually runs, for the behavior-preserving
speedup loop driven by the `perf-iterate` Claude skill (`.claude/skills/perf-iterate/SKILL.md`).

- `tools/perf/mainnet-mix` samples recent mainnet blocks (message types, memo classes, gas
  share, EndBlock events) into `mainnet-mix.json`, and snapshots the network's state shape
  (pools, mimir, node and vault counts) into `testdata/mainnet-shape.json`.
  Run with `LIQUIFY_API_KEY=... make perf-mix`.
- `x/thorchain/perf_bench_test.go` holds the `BenchmarkPerf*` benchmarks: the quorum
  observation, keysign-fail, price-feed and deposit handlers and EndBlock, on a deterministic
  fixture seeded from the shape. Each reports `gas/op`. In the transaction benchmarks that
  is the handler's gas: it is part of the tx `gas_used` (hashed into `LastResultsHash`), so a
  change there is consensus-visible, but the benchmark calls the handler directly and skips
  the ante decorators (tx size, signature verification), so unchanged handler gas does not by
  itself show the whole tx's gas is unchanged; the regtest guard compares real `gas_used`.
  The block-level `BenchmarkPerfEndBlock` meters store access with the fixture's gas meter;
  that is not any tx's `gas_used`, so `verdict.py` reports its changes as informational.
  So are the API query benchmarks (`BenchmarkPerfQuoteSwap`, `BenchmarkPerfQueryBlock`):
  API queries run outside any tx. The `BenchmarkPerfQuoteSwap/wasm/` cases run the same
  quotes as a contract (`CtxWASMQuery`), inside a tx, so their gas is checked like tx gas.
- `scripts/perf/`:
  - `bench.sh <label> [re]` benchmarks the working tree (`make perf-bench`).
  - `ab.sh <label> [re] [base]` runs base and working tree interleaved and prints a KEEP/REVERT
    verdict (`verdict.py`: >= 5% faster or fewer allocs with p < 0.05, no > 5% regression, reproduced on a confirmation pass).
  - `profile.sh <label> [re]` writes CPU/alloc profiles and top tables. The CPU tables cover
    only the measured loops; the alloc table covers the whole run, fixture setup included (heap
    profiles carry no pprof labels to filter on).
  - `guard.sh <label> [base]` builds, unit-tests touched packages, and diffs unfiltered regtest
    runs (exports, events, app_hash, gas_used) of base vs working tree (`regtest_diff.py`).
  - `test-times.sh <label> [unit regtest sim]` records suite wall times and regtest per-block latency.

Benchmarks build with `PERF_TAGS=mainnet` (the release tag) by default and run pinned to
`PERF_CPUS` (8-15) with `GOMAXPROCS=8`. Regtest supports two opt-in env vars for this:
`REGTEST_BLOCK_TIMES=<csv>` (per-block latency) and `REGTEST_CPUPROFILE_DIR=<dir>` (CPU
profile of each node under test).
