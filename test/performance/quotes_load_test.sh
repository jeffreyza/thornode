#!/bin/bash
# quotes_load_test.sh - HTTP load testing for the quotes API endpoint
#
# This script performs load testing on a running THORNode instance's quote API
# to simulate real-world usage patterns and identify performance bottlenecks.
#
# Requirements: curl, jq (optional for JSON parsing)
#
# Usage:
#   ./quotes_load_test.sh <thornode_url> [duration_seconds] [concurrent_requests]
#
# Example:
#   ./quotes_load_test.sh http://localhost:1317 60 10

set -e

# Configuration
THORNODE_URL="${1:-http://localhost:1317}"
DURATION="${2:-30}"  # seconds
CONCURRENT="${3:-5}" # concurrent requests

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_DIR="$SCRIPT_DIR/results/load_test_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$OUTPUT_DIR"

# Colors
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

log_info() {
  echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
  echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
  echo -e "${RED}[ERROR]${NC} $1"
}

# Test cases with various swap scenarios
declare -a TEST_CASES=(
  # Format: "name|from_asset|to_asset|amount"
  "BTC_to_RUNE|BTC.BTC|THOR.RUNE|100000000"
  "RUNE_to_BTC|THOR.RUNE|BTC.BTC|5000000000000"
  "ETH_to_BTC|ETH.ETH|BTC.BTC|1000000000000000000"
  "Small_BTC|BTC.BTC|THOR.RUNE|10000000"
  "ETH_to_RUNE|ETH.ETH|THOR.RUNE|500000000000000000"
)

# Check if thornode is accessible
log_info "Checking THORNode availability at $THORNODE_URL..."
if ! curl -sf "$THORNODE_URL/thorchain/health" >/dev/null 2>&1; then
  log_error "THORNode is not accessible at $THORNODE_URL"
  log_info "Make sure your THORNode instance is running and accessible"
  exit 1
fi

log_info "THORNode is accessible"

# Function to make a quote request and measure timing
make_quote_request() {
  local name=$1
  local from_asset=$2
  local to_asset=$3
  local amount=$4
  local output_file=$5

  local start_time
  local http_code
  local end_time
  local duration
  start_time=$(date +%s%3N)
  http_code=$(curl -s -w "%{http_code}" -o "$output_file" \
    "$THORNODE_URL/thorchain/quote/swap?from_asset=$from_asset&to_asset=$to_asset&amount=$amount")
  end_time=$(date +%s%3N)
  duration=$((end_time - start_time))

  echo "$name,$http_code,$duration" >>"$OUTPUT_DIR/timing.csv"
}

# Initialize results file
echo "test_case,http_code,duration_ms" >"$OUTPUT_DIR/timing.csv"

# Initialize error tracking file
ERROR_FILE="$OUTPUT_DIR/errors.count"
echo "0" >"$ERROR_FILE"

log_info "Starting load test..."
log_info "  URL: $THORNODE_URL"
log_info "  Duration: ${DURATION}s"
log_info "  Concurrent requests: $CONCURRENT"
log_info "  Output: $OUTPUT_DIR"
echo ""

# Run load test
START_TIME=$(date +%s)
REQUEST_COUNT=0

while [ $(($(date +%s) - START_TIME)) -lt "$DURATION" ]; do
  # Run concurrent requests
  for i in $(seq 1 "$CONCURRENT"); do
    # Select random test case
    RANDOM_IDX=$((RANDOM % ${#TEST_CASES[@]}))
    TEST_CASE="${TEST_CASES[$RANDOM_IDX]}"

    IFS='|' read -r NAME FROM TO AMOUNT <<<"$TEST_CASE"

    (
      OUTPUT_FILE="$OUTPUT_DIR/response_${REQUEST_COUNT}_${i}.json"
      make_quote_request "$NAME" "$FROM" "$TO" "$AMOUNT" "$OUTPUT_FILE"

      # Check if request was successful
      if [ -s "$OUTPUT_FILE" ]; then
        # Check for error in response
        if command -v jq >/dev/null 2>&1; then
          if jq -e '.error' "$OUTPUT_FILE" >/dev/null 2>&1; then
            log_warn "Request failed: $(jq -r '.error' "$OUTPUT_FILE")"
            echo "1" >>"$ERROR_FILE"
          fi
        fi
      else
        echo "1" >>"$ERROR_FILE"
      fi
    ) &
  done

  # Wait for concurrent requests to complete
  wait

  REQUEST_COUNT=$((REQUEST_COUNT + CONCURRENT))

  # Print progress
  ELAPSED=$(($(date +%s) - START_TIME))
  RPS=$(echo "scale=2; $REQUEST_COUNT / $ELAPSED" | bc)
  ERROR_COUNT=$(wc -l <"$ERROR_FILE" 2>/dev/null || echo "0")
  printf "\r${GREEN}Progress:${NC} %d requests in %ds (%.2f req/s, %d errors)" \
    "$REQUEST_COUNT" "$ELAPSED" "$RPS" "$ERROR_COUNT"
done

echo ""
log_info "Load test complete!"
echo ""

# Analyze results
log_info "Analyzing results..."

if command -v python3 >/dev/null 2>&1; then
  python3 - <<EOF
import csv
import statistics

# Read timing data
timings = {'all': []}
with open('$OUTPUT_DIR/timing.csv', 'r') as f:
    reader = csv.DictReader(f)
    for row in reader:
        test_case = row['test_case']
        duration = int(row['duration_ms'])
        http_code = row['http_code']

        if test_case not in timings:
            timings[test_case] = []
        timings[test_case].append(duration)
        timings['all'].append(duration)

# Print statistics
print("\n${GREEN}Performance Statistics:${NC}")
print("=" * 70)

for test_case, durations in sorted(timings.items()):
    if len(durations) == 0:
        continue

    print(f"\n{test_case}:")
    print(f"  Requests: {len(durations)}")
    print(f"  Min:      {min(durations)} ms")
    print(f"  Max:      {max(durations)} ms")
    print(f"  Mean:     {statistics.mean(durations):.2f} ms")
    print(f"  Median:   {statistics.median(durations):.2f} ms")
    if len(durations) > 1:
        print(f"  StdDev:   {statistics.stdev(durations):.2f} ms")

    # Percentiles
    sorted_durations = sorted(durations)
    p50 = sorted_durations[len(sorted_durations) * 50 // 100]
    p95 = sorted_durations[len(sorted_durations) * 95 // 100]
    p99 = sorted_durations[len(sorted_durations) * 99 // 100]
    print(f"  P50:      {p50} ms")
    print(f"  P95:      {p95} ms")
    print(f"  P99:      {p99} ms")

EOF
else
  log_warn "Python 3 not found. Skipping detailed analysis."
  log_info "Raw timing data available in: $OUTPUT_DIR/timing.csv"
fi

echo ""
log_info "Results saved to: $OUTPUT_DIR"
log_info "  - Timing data: $OUTPUT_DIR/timing.csv"
log_info "  - Response samples: $OUTPUT_DIR/response_*.json"
echo ""
log_info "Summary:"
ERROR_COUNT=$(wc -l <"$ERROR_FILE" 2>/dev/null || echo "0")
echo "  - Total requests: $REQUEST_COUNT"
echo "  - Errors: $ERROR_COUNT"
echo "  - Duration: ${DURATION}s"
echo "  - Average RPS: $(echo "scale=2; $REQUEST_COUNT / $DURATION" | bc)"
