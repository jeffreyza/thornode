# Advanced Swap Queue Monitoring

## Overview

The Advanced Swap Queue provides comprehensive telemetry and monitoring capabilities through Prometheus metrics, API endpoints, and detailed logging. This guide covers all available monitoring tools and best practices for observing queue performance.

## Prometheus Metrics

The advanced swap queue emits detailed Prometheus metrics for real-time monitoring and alerting.

### Core Queue Metrics

#### Processing Metrics

- **`thornode_adv_swap_queue_iterations_per_block`**: Number of processing iterations executed per block
- **`thornode_adv_swap_queue_total_swaps_per_block`**: Total number of swaps processed per block
- **`thornode_adv_swap_queue_market_swaps_per_block`**: Market swaps processed per block
- **`thornode_adv_swap_queue_limit_swaps_per_block`**: Limit swaps processed per block
- **`thornode_adv_swap_queue_completed_swaps_per_block`**: Completed swaps per block

#### Cumulative Counters

- **`thornode_adv_swap_queue_market_swaps_total`**: Total market swaps processed (counter)
- **`thornode_adv_swap_queue_limit_swaps_total`**: Total limit swaps processed (counter)
- **`thornode_adv_swap_queue_swaps_completed_total`**: Total swaps completed (counter)

#### Queue Depth Metrics

- **`thornode_adv_swap_queue_total_limit_swaps`**: Current number of limit swaps in queue
- **`thornode_adv_swap_queue_market_swaps_queued`**: Current number of market swaps queued
- **`thornode_adv_swap_queue_total_limit_swap_value`**: Total value of limit swaps (RUNE and USD)

### Trading Pair Metrics

The queue provides detailed per-trading-pair metrics with labels:

#### Per-Pair Queue Depth

- **`thornode_adv_swap_queue_limit_swaps_by_pair`**: Limit swaps per trading pair
  - Labels: `source_asset`, `target_asset`

#### Per-Pair Value Metrics

- **`thornode_adv_swap_queue_limit_swap_value_by_pair_rune`**: RUNE value per trading pair

  - Labels: `source_asset`, `target_asset`

- **`thornode_adv_swap_queue_limit_swap_value_by_pair_usd`**: USD value per trading pair
  - Labels: `source_asset`, `target_asset`

### Example Prometheus Queries

#### Queue Performance

```promql
# Average processing iterations per block (5-minute rate)
rate(thornode_adv_swap_queue_iterations_per_block[5m])

# Market vs Limit swap ratio
thornode_adv_swap_queue_market_swaps_per_block / thornode_adv_swap_queue_limit_swaps_per_block

# Queue completion rate
rate(thornode_adv_swap_queue_swaps_completed_total[5m])
```

#### Queue Depth Analysis

```promql
# Total value locked in limit swaps (USD)
thornode_adv_swap_queue_total_limit_swap_value{currency="usd"}

# Largest trading pairs by limit swap count
topk(10, thornode_adv_swap_queue_limit_swaps_by_pair)

# Most valuable trading pairs by USD volume
topk(10, thornode_adv_swap_queue_limit_swap_value_by_pair_usd)
```

#### Performance Monitoring

```promql
# Rapid swap utilization (iterations per block > 1)
histogram_quantile(0.95, rate(thornode_adv_swap_queue_iterations_per_block[5m]))

# Processing efficiency (swaps per iteration)
thornode_adv_swap_queue_total_swaps_per_block / thornode_adv_swap_queue_iterations_per_block
```

## API Endpoints

### Limit Swaps Query API

#### List Limit Swaps

```bash
GET /thorchain/queue/limit_swaps
```

**Query Parameters:**

- `offset` (integer): Number of items to skip (default: 0)
- `limit` (integer): Number of items to return (default: 100, max: 1000)
- `source_asset` (string): Filter by source asset (e.g., "BTC.BTC")
- `target_asset` (string): Filter by target asset (e.g., "ETH.ETH")
- `sender` (string): Filter by sender address
- `sort_by` (string): Sort field - "ratio", "age", "amount", "created_height" (default: "ratio")
- `sort_order` (string): Sort order - "asc", "desc" (default: "asc")

**Example Response:**

```json
{
  "limit_swaps": [
    {
      "swap": {
        "tx": {
          "id": "ABC123...",
          "coins": [{ "asset": "BTC.BTC", "amount": "50000000" }]
        },
        "target_asset": "ETH.ETH",
        "trade_target": "2500000000"
      },
      "ratio": "50000000",
      "blocks_since_created": 1200,
      "time_to_expiry_blocks": 42000,
      "created_timestamp": 1693840200
    }
  ],
  "pagination": {
    "offset": 0,
    "limit": 100,
    "total": "15",
    "has_next": false,
    "has_prev": false
  }
}
```

#### Limit Swaps Summary

```bash
GET /thorchain/queue/limit_swaps/summary
```

**Query Parameters:**

- `source_asset` (string): Filter by source asset
- `target_asset` (string): Filter by target asset

**Example Response:**

```json
{
  "total_limit_swaps": "42",
  "total_value_usd": "75000000",
  "oldest_swap_blocks": "12500",
  "average_age_blocks": "8200",
  "asset_pairs": [
    {
      "source_asset": "BTC.BTC",
      "target_asset": "ETH.ETH",
      "count": "15",
      "total_value_usd": "30000000"
    }
  ]
}
```

#### Swap Details

```bash
GET /thorchain/queue/swap/details/{tx_id}
```

**Example Response:**

```json
{
  "swap": {
    "tx": {...},
    "state": {
      "count": 1,
      "quantity": 5,
      "in": "10000000",
      "deposit": "50000000"
    }
  },
  "swap_type": "limit",
  "is_streaming": true,
  "blocks_remaining": 41800
}
```

## Monitoring Best Practices

### Queue Health Monitoring

#### Key Metrics to Watch

1. **Queue Depth**: Monitor `thornode_adv_swap_queue_total_limit_swaps` for queue congestion
2. **Processing Rate**: Track `thornode_adv_swap_queue_swaps_completed_total` rate for throughput
3. **Iteration Utilization**: Monitor rapid swap usage for performance optimization
4. **Value at Risk**: Watch total USD value in limit swaps for economic exposure

#### Recommended Alerts

##### Queue Congestion

```promql
thornode_adv_swap_queue_total_limit_swaps > 1000
```

##### Low Processing Rate

```promql
rate(thornode_adv_swap_queue_swaps_completed_total[10m]) < 0.1
```

##### High Value Concentration

```promql
thornode_adv_swap_queue_total_limit_swap_value{currency="usd"} > 10000000
```

### Performance Optimization

#### Rapid Swap Configuration

Monitor iteration metrics to optimize `AdvSwapQueueRapidSwapMax`:

```promql
# If consistently high, consider increasing AdvSwapQueueRapidSwapMax
avg_over_time(thornode_adv_swap_queue_iterations_per_block[1h]) > 0.8
```

#### Trading Pair Analysis

Identify busy trading pairs for targeted optimization:

```promql
# Top 5 most active trading pairs
topk(5, increase(thornode_adv_swap_queue_limit_swaps_by_pair[1h]))
```

### Dashboarding

#### Essential Dashboard Panels

1. **Queue Overview**

   - Total limit swaps in queue
   - Market vs limit swap processing rates
   - Total value locked (RUNE/USD)

2. **Processing Performance**

   - Swaps processed per block
   - Iteration utilization
   - Completion rates

3. **Trading Pair Heatmap**

   - Limit swap counts by trading pair
   - Value distribution across pairs
   - Processing times by pair

4. **Historical Trends**
   - Queue depth over time
   - Processing efficiency trends
   - Value at risk evolution

## Logging and Debugging

### Log Levels

The advanced swap queue uses structured logging with different levels:

#### Info Level

- Successful swap completions
- Queue processing summaries
- Configuration changes

#### Debug Level

- Individual swap processing details
- Price discovery calculations
- Queue state transitions

#### Error Level

- Failed swap processing
- Invalid swap modifications
- System configuration errors

### Example Log Queries

#### Queue Processing Issues

```bash
# Find failed swap processing
grep "fail to fetch adv swap item" thornode.log

# Monitor limit swap expirations
grep "swap has been completed" thornode.log | grep "limit swap has expired"
```

#### Performance Analysis

```bash
# Track rapid swap iterations
grep "rapid swap iteration" thornode.log

# Monitor queue depth changes
grep "queue depth" thornode.log
```

## Integration Examples

### Grafana Dashboard Configuration

```json
{
  "dashboard": {
    "title": "THORChain Advanced Swap Queue",
    "panels": [
      {
        "title": "Queue Depth",
        "type": "stat",
        "targets": [
          {
            "expr": "thornode_adv_swap_queue_total_limit_swaps"
          }
        ]
      },
      {
        "title": "Processing Rate",
        "type": "graph",
        "targets": [
          {
            "expr": "rate(thornode_adv_swap_queue_swaps_completed_total[5m])"
          }
        ]
      }
    ]
  }
}
```

### Alertmanager Rules

```yaml
groups:
  - name: thornode_swap_queue
    rules:
      - alert: SwapQueueBacklog
        expr: thornode_adv_swap_queue_total_limit_swaps > 500
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High number of limit swaps in queue"
          description: "{{ $value }} limit swaps are currently queued"

      - alert: LowProcessingRate
        expr: rate(thornode_adv_swap_queue_swaps_completed_total[10m]) < 0.05
        for: 10m
        labels:
          severity: critical
        annotations:
          summary: "Swap processing rate is critically low"
          description: "Only {{ $value }} swaps processed per second"
```

### Custom Monitoring Scripts

#### Queue Health Check

```bash
#!/bin/bash
# Check queue health and alert if issues found

QUEUE_DEPTH=$(curl -s "http://localhost:1317/thorchain/queue/limit_swaps/summary" | jq -r '.total_limit_swaps')
PROCESSING_RATE=$(curl -s "http://localhost:9090/api/v1/query?query=rate(thornode_adv_swap_queue_swaps_completed_total[5m])" | jq -r '.data.result[0].value[1]')

if (( $(echo "$QUEUE_DEPTH > 1000" | bc -l) )); then
    echo "WARNING: High queue depth: $QUEUE_DEPTH"
fi

if (( $(echo "$PROCESSING_RATE < 0.1" | bc -l) )); then
    echo "CRITICAL: Low processing rate: $PROCESSING_RATE swaps/sec"
fi
```

This comprehensive monitoring setup provides full visibility into advanced swap queue performance, enabling proactive maintenance and optimization.
