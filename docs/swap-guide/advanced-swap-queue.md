# Advanced Swap Queue

## Overview

The Advanced Swap Queue is THORChain's enhanced swap processing system that provides improved performance, better price discovery, and support for limit swaps. It represents a significant upgrade from the legacy swap queue, offering multiple swap types and advanced processing capabilities.

```admonish info
The advanced swap queue is controlled by the `EnableAdvSwapQueue` Mimir value. Check the current value on the [Constants and Mimirs](../mimir.md) page. A value of 0 means disabled, 1 means enabled, and 2 means market-only mode (limit swaps are skipped).
```

## Key Features

### Swap Types

The advanced queue supports three distinct swap types:

#### 1. Market Swaps (`=` prefix)

- **Immediate Execution**: Attempt to execute as soon as possible at current market prices
- **Price Protection**: Can include trade limits - swap is refunded if minimum output not achievable
- **No Queue Persistence**: Execute immediately or refund, don't wait in queue
- **Example**: `=:BTC.BTC:bc1qaddress:50000000` (with minimum output protection)

#### 2. Limit Swaps (`=<` prefix)

- **Conditional Execution**: Only execute when price conditions are met
- **Queue Persistence**: Remain in queue until executed or expired
- **Price Waiting**: Will wait for favorable conditions rather than immediate refund
- **Trade Target Required**: The trade target (minimum output) must be non-zero for a swap to be treated as a limit swap. If the target is zero, it is treated as a market swap regardless of the prefix.
- **Example**: `=<:ETH.ETH:0xaddress:2000000000` (wait until at least 20 ETH output is achievable)

#### 3. Rapid Swaps

- **Multiple Iterations**: Process multiple swaps per block for improved throughput
- **Configurable**: Controlled by `AdvSwapQueueRapidSwapMax` mimir setting
- **Performance Optimization**: Reduces queue congestion during high activity

### Queue Modes

The advanced swap queue operates in different modes controlled by the `EnableAdvSwapQueue` mimir setting:

| Mode            | Value | Description                                                   |
| --------------- | ----- | ------------------------------------------------------------- |
| **Disabled**    | 0     | Uses legacy swap queue system                                 |
| **Enabled**     | 1     | Full advanced queue with market and limit swaps               |
| **Market-only** | 2     | Advanced queue for market swaps only, limit swaps are skipped |

## Behavioral Differences

### Default Streaming Behavior

**IMPORTANT CHANGE**: The advanced swap queue changes the default swap behavior:

- **Legacy Queue**: Simple swaps by default (no streaming)
- **Advanced Queue**: **Streaming swaps by default** when no interval/quantity specified

When you omit interval and quantity parameters (e.g., `=:ETH.ETH:0xaddress`), the advanced queue will:

1. **Calculate optimal streaming parameters** based on swap size and pool conditions
2. **Execute as streaming swap** with rapid processing (interval = 0)
3. **Maximize price efficiency** through multiple sub-swaps

**To disable streaming**: Explicitly set `/1/1` (1 sub-swap every 1 block)

**Examples**:

```bash
# Advanced queue default behavior (auto-streaming)
=:ETH.ETH:0xaddress:250000000

# Force single swap (disable streaming)
=:ETH.ETH:0xaddress:250000000/1/1

# Legacy behavior (when advanced queue disabled)
SWAP:ETH.ETH:0xaddress:250000000  # Single swap
```

### Processing Order

The advanced queue processes swaps using a rapid iteration framework with two phases per iteration:

1. **Market Swaps Phase**: All market swaps are fetched and processed for immediate execution
2. **Limit Swaps Phase**: Limit swaps are evaluated per trading pair for conditional execution

**Rapid Processing Framework**: The entire two-phase process can be repeated multiple times per block (controlled by `AdvSwapQueueRapidSwapMax` setting), enabling higher throughput when there are swaps ready to execute.

### Key Behavioral Differences

#### Market Swaps vs Limit Swaps

| Aspect               | Market Swaps (`=`)                 | Limit Swaps (`=<`)               |
| -------------------- | ---------------------------------- | -------------------------------- |
| **Execution**        | Immediate attempt                  | Wait for conditions              |
| **Price Protection** | Refund if target not met           | Wait until target achievable     |
| **Queue Behavior**   | Execute or refund immediately      | Persist until executed/expired   |
| **Use Case**         | "Swap now at this price or better" | "Swap when price reaches target" |

### Price Discovery

The advanced queue uses sophisticated price discovery mechanisms:

#### Fee-less Swap Validation

- Initial price check without considering fees
- Determines if a limit swap can potentially execute
- More efficient than full swap simulation

#### Fee-inclusive Validation

- Secondary check including all fees (swap fees, outbound fees)
- Ensures profitable execution after all costs
- Prevents failed transactions

#### Ratio-based Indexing

- Limit swaps are indexed by their target price ratios
- Enables efficient discovery of executable swaps
- Sorted processing from most favorable to least favorable ratios

### Streaming Integration

Advanced queue seamlessly integrates with streaming swaps with enhanced interval behavior:

#### Interval Behavior Changes

The advanced queue introduces important changes to how streaming intervals work:

**Interval = 0 (Rapid Streaming)**:

- **Multiple sub-swaps per block**: Can execute several streaming sub-swaps in a single block
- **Rapid execution**: Maximizes throughput when `AdvSwapQueueRapidSwapMax` > 1
- **Use case**: Fast completion of streaming swaps when conditions are favorable

**Interval ≥ 1 (Traditional Streaming)**:

- **One sub-swap per interval**: Executes exactly one streaming sub-swap every X blocks
- **Block spacing**: Waits for the specified interval between executions
- **Use case**: Time-distributed execution for better price averaging

#### Streaming Types

- **Streaming Market Swaps**: Execute over multiple blocks with configurable intervals
- **Streaming Limit Swaps**: Combine price conditions with streaming execution
- **Optimized Scheduling**: Intelligent scheduling based on pool conditions and interval settings

## Boundaries and Limits

### Time-to-Live (TTL) Management

**Default TTL**: Controlled by the [`StreamingLimitSwapMaxAge`](../mimir.md) mimir (default 43,200 blocks, ~3 days)

```bash
=<:BTC.BTC:bc1qaddress:50000000  # Uses default TTL from StreamingLimitSwapMaxAge
```

**Custom TTL**: For limit swaps, the second `/` value in the memo is treated as a custom TTL in blocks (not a streaming interval). It is capped to `StreamingLimitSwapMaxAge`.

```bash
=<:BTC.BTC:bc1qaddress:50000000/21600/0  # Custom 21,600 block TTL (~1.5 days)
```

```admonish info
While the memo format looks the same for both swap types (`tradeTarget/interval/quantity`), the second parameter behaves differently. For market swaps (`=`) it controls the streaming interval between sub-swaps. For limit swaps (`=<`) it sets a custom TTL in blocks.
```

**Expiration Behavior**:

- Expired limit swaps are automatically cleaned up during `EndBlock` processing
- Any amount already swapped (partial fills) is sent to the destination address
- The unswapped remainder is refunded to the original sender
- No manual intervention required

### Processing Constraints

**Block-level Constraints**:

- Pool cycle blocks skip all swap processing
- Trading halts prevent swap execution
- Chain-specific ragnarok modes block swaps

**Streaming Constraints**:

- Minimum swap sizes apply (controlled by `StreamingSwapMinBPFee`)
- Maximum streaming length limits
- Interval-based execution timing

**Rapid Swap Limits**:

- Maximum iterations per block (default: 1)
- Configurable via `AdvSwapQueueRapidSwapMax` mimir
- Prevents excessive processing overhead

### Partial Fills and Settlement

Limit swaps are broken into multiple sub-swaps and can be partially filled when they expire or are cancelled. Settlement follows the same process as [streaming swaps](./streaming-swaps.md) — any amount already swapped is sent to the destination address, and the unswapped remainder is refunded to the sender.

### Rapid Swap Direction Protection

When rapid swap iterations are enabled (`AdvSwapQueueRapidSwapMax` > 1), the advanced queue tracks the swap direction per pool during each block. Market swaps going in the same direction on the same pool as the previous swap are skipped on subsequent iterations. This prevents compounding slip from multiple sub-swaps pushing the pool in the same direction within a single block, and allows arbs to restore the pool ratio between blocks.

Limit swaps are not subject to direction protection — they are already validated against the current pool ratio before execution (see [Price Discovery](#price-discovery)).

## Advanced Features

### Swap Modification

Limit swaps can be modified or cancelled using the modify memo (see [Transaction Memos](../concepts/memos.md#limit-swap-modification)):

```bash
m=<:SOURCE:TARGET:MODIFIEDTARGETAMOUNT
```

The handler searches for a matching limit swap by the sender's address and the source/target asset pair. It iterates through matching swaps up to a configurable maximum ([`ModifyLimitSwapMaxIterations`](../mimir.md)) to prevent DoS.

**Examples**:

```bash
m=<:1234BTC.BTC:5678ETH.ETH:2500000000  # Modify target amount
m=<:1234BTC.BTC:5678ETH.ETH:0           # Cancel swap (set to 0)
```

**Security**:

- Only the original swap creator can modify
- Modification transaction funds are donated to pools
- Full validation of source and target assets

### Telemetry and Monitoring

The advanced queue provides comprehensive metrics:

**Core Metrics**:

- Swap processing rates (market vs limit)
- Queue depths per trading pair
- Iteration counts and processing time
- Completion and expiration rates

**Trading Pair Metrics**:

- Per-pair limit swap counts
- Total value locked in limit swaps
- Average execution ratios

**Performance Metrics**:

- Rapid swap utilization
- Processing efficiency
- Queue congestion indicators

## Migration from Legacy Queue

### Compatibility

- **Memo Compatibility**: Legacy `SWAP:` syntax still supported
- **Gradual Migration**: Networks can enable advanced queue progressively
- **Fallback Support**: Automatic fallback to legacy queue when disabled

### Benefits of Migration

1. **Better Performance**: Rapid swap processing reduces queue congestion
2. **Enhanced UX**: Limit swaps provide better price protection
3. **Improved Efficiency**: Smarter indexing and processing algorithms
4. **Comprehensive Monitoring**: Detailed telemetry for system health

### Migration Considerations

- **Node Operators**: Monitor telemetry for performance insights
- **Applications**: Update to use new memo prefixes for optimal experience
- **Users**: Leverage limit swaps for better price execution

## Best Practices

### For Users

1. **Use Appropriate Swap Types**:

   - Market swaps (`=`) for "swap now at this price or better"
   - Limit swaps (`=<`) for "swap when price reaches my target"

2. **Set Realistic Limits**:

   - Consider current pool depths and volatility
   - Account for fees in limit calculations

3. **Monitor Expiration**:
   - Track limit swap TTL
   - Consider custom TTL for longer-term swaps

### For Developers

1. **Leverage API Endpoints**:

   - Use `/thorchain/queue/limit_swaps` for monitoring (no filters required)
   - Query `/thorchain/queue/limit_swaps/summary` for statistics
   - Filter by `source_asset`, `target_asset`, or `sender` as needed

2. **Handle Expiration**:

   - Implement client-side TTL tracking
   - Provide user notifications for approaching expiry

3. **Optimize for Advanced Queue**:
   - Use new memo prefixes (`=`, `=<`)
   - Implement retry logic for failed limit swaps

## Example Use Cases

### Immediate Execution with Price Protection

```bash
# Market swap: Convert 0.1 BTC to ETH immediately, refund if less than 2.5 ETH
=:ETH.ETH:0x742d35Cc891C0b8ee825C4645b3175d2346a3c85:250000000
```

### Price-Protected Swap (Wait for Conditions)

```bash
# Limit swap: Wait until can get at least 2.5 ETH, then execute
=<:ETH.ETH:0x742d35Cc891C0b8ee825C4645b3175d2346a3c85:250000000
```

### Rapid Streaming (Interval = 0)

```bash
# Rapid streaming: 5 sub-swaps as fast as possible (multiple per block)
=:ETH.ETH:0x742d35Cc891C0b8ee825C4645b3175d2346a3c85:250000000/0/5
```

### Traditional Streaming (Interval ≥ 1)

```bash
# Traditional streaming: 5 sub-swaps every 10 blocks (one sub-swap per 10-block period)
=<:ETH.ETH:0x742d35Cc891C0b8ee825C4645b3175d2346a3c85:250000000/10/5
```

### Custom TTL Limit Swap

```bash
# 1-day limit swap (14,400 blocks)
=<:ETH.ETH:0x742d35Cc891C0b8ee825C4645b3175d2346a3c85:250000000/14400/0
```

## API Integration

Query all limit swaps:

```bash
curl "https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/limit_swaps"
```

Query limit swaps by source asset:

```bash
curl "https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/limit_swaps?source_asset=BTC.BTC&limit=50"
```

Query limit swaps by target asset:

```bash
curl "https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/limit_swaps?target_asset=ETH.ETH"
```

Query limit swaps by sender:

```bash
curl "https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/limit_swaps?sender=thor1abc123"
```

Get limit swap summary:

```bash
curl "https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/limit_swaps/summary"
```

Check specific swap details:

```bash
curl "https://gateway.liquify.com/chain/thorchain_api/thorchain/queue/swap/details/{tx_id}"
```

For detailed API documentation, see the [OpenAPI specification](../../openapi/openapi.yaml).
