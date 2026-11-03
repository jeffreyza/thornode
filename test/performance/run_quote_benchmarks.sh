#!/bin/bash
# run_quote_benchmarks.sh - Comprehensive quote API performance testing with profiling
#
# This script runs benchmarks on the quotes API with advanced swap queue enabled/disabled
# and captures CPU, memory, block, and trace profiles for analysis.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OUTPUT_DIR="$REPO_ROOT/test/performance/results"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)

# Colors for output
RED='\033[0:31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
  echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
  echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
  echo -e "${RED}[ERROR]${NC} $1"
}

# Create output directory
mkdir -p "$OUTPUT_DIR/$TIMESTAMP"

cd "$REPO_ROOT/x/thorchain"

log_info "Starting quote API benchmarks with profiling..."
log_info "Output directory: $OUTPUT_DIR/$TIMESTAMP"

# Run benchmarks with different profile types
log_info "Running benchmarks with CPU profiling..."
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
  -benchtime=10x \
  -cpuprofile="$OUTPUT_DIR/$TIMESTAMP/cpu.prof" \
  -benchmem \
  . 2>&1 | tee "$OUTPUT_DIR/$TIMESTAMP/benchmark.txt"

log_info "Running benchmarks with memory profiling..."
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
  -benchtime=10x \
  -memprofile="$OUTPUT_DIR/$TIMESTAMP/mem.prof" \
  -benchmem \
  . 2>&1 | tee -a "$OUTPUT_DIR/$TIMESTAMP/benchmark.txt"

log_info "Running benchmarks with block profiling..."
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
  -benchtime=10x \
  -blockprofile="$OUTPUT_DIR/$TIMESTAMP/block.prof" \
  . 2>&1 | tee -a "$OUTPUT_DIR/$TIMESTAMP/benchmark.txt"

log_info "Running benchmarks with mutex profiling..."
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
  -benchtime=10x \
  -mutexprofile="$OUTPUT_DIR/$TIMESTAMP/mutex.prof" \
  . 2>&1 | tee -a "$OUTPUT_DIR/$TIMESTAMP/benchmark.txt"

log_info "Running benchmarks with execution trace..."
go test -run='^$' -bench='^BenchmarkQuoteSwapComparison$' \
  -benchtime=5x \
  -trace="$OUTPUT_DIR/$TIMESTAMP/trace.out" \
  . 2>&1 | tee -a "$OUTPUT_DIR/$TIMESTAMP/benchmark.txt"

# Run all benchmark variants for detailed comparison
log_info "Running all benchmark variants..."
go test -run='^$' -bench=. \
  -benchtime=5x \
  -benchmem \
  . 2>&1 | tee "$OUTPUT_DIR/$TIMESTAMP/all_benchmarks.txt"

log_info "Benchmarks complete!"
echo ""
log_info "Profile files generated:"
echo "  - CPU Profile:    $OUTPUT_DIR/$TIMESTAMP/cpu.prof"
echo "  - Memory Profile: $OUTPUT_DIR/$TIMESTAMP/mem.prof"
echo "  - Block Profile:  $OUTPUT_DIR/$TIMESTAMP/block.prof"
echo "  - Mutex Profile:  $OUTPUT_DIR/$TIMESTAMP/mutex.prof"
echo "  - Trace:          $OUTPUT_DIR/$TIMESTAMP/trace.out"
echo ""
log_info "Benchmark results saved to:"
echo "  - $OUTPUT_DIR/$TIMESTAMP/benchmark.txt"
echo "  - $OUTPUT_DIR/$TIMESTAMP/all_benchmarks.txt"
echo ""
log_info "Next steps:"
echo "  1. Analyze CPU profile:    go tool pprof -http=:8080 $OUTPUT_DIR/$TIMESTAMP/cpu.prof"
echo "  2. Analyze memory profile: go tool pprof -http=:8080 $OUTPUT_DIR/$TIMESTAMP/mem.prof"
echo "  3. View execution trace:   go tool trace $OUTPUT_DIR/$TIMESTAMP/trace.out"
echo "  4. Run analysis script:    $SCRIPT_DIR/analyze_profiles.sh $OUTPUT_DIR/$TIMESTAMP"
