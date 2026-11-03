# ADR 023: $RUNE Supply Restructure

## Changelog

- 2025: Initial proposal

## Status

Accepted

## Context

$RUNE's tokenomics have accumulated artifacts over time, resulting in confusing distinctions between Maximum Supply (500M), Total Supply (~425M), and Circulating Supply (~351M).

### Problem 1: Maximum Supply is Meaningless

With the removal of ThorFi/Lending, the minting function no longer exists, making Maximum Supply functionally equivalent to Total Supply. The 500M cap served as a circuit breaker for lending-related minting, but since that feature has been discontinued, the cap is now obsolete.

### Problem 2: Reserve Causes Confusion

The Reserve wallet currently holds ~74.21M tokens. These tokens were created ex nihilo and never circulated — they represent a claim against everyone who holds circulating $RUNE. Despite serving necessary protocol functions (operations and exploit coverage), the Reserve causes confusion and routine calls to "spend" it, which would dilute holders.

### Problem 3: Complex Supply Structure Discourages Investors

The nuanced distinctions between Maximum, Total, and Circulating Supply are not intuitive to potential investors, creating unnecessary friction.

### Problem 4: Burn Effectiveness is Diluted

When burns are calculated against a larger supply figure, their optical and mathematical impact is diminished.

## Decision

### Proposal 1: Burn ~87% of the Reserve

Burn approximately 64.9M $RUNE from the Reserve, leaving only 9.3M for protocol operations and exploit coverage buffer.

This:

- Reduces Market Cap vs FDV gap
- Discourages calls to spend the Reserve (which dilutes holders)
- Retains sufficient buffer for protocol operations and potential exploit coverage

### Proposal 2: Reduce Maximum Supply to 360M

After the Reserve burn, reduce the max supply cap from 500M to 360M to match the new total supply.

This:

- Removes complexity in $RUNE supply structure
- Reduces the potential impact of an infinite mint hack/bug
- The max supply can be revised lower as the burn continues

## Implementation

The changes are implemented as a consensus migration (version 13 to 14):

1. During the migration, the Reserve module balance is reduced to 9.3M RUNE by burning the excess.
2. The `MaxRuneSupply` constant and Mimir value are set to 360M (360,000,000 RUNE).
3. A `BurnSupplyType` event is emitted for the reserve burn to maintain accurate supply tracking.

## Consequences

### Positive

- Simplified supply structure improves investor clarity
- Reduced FDV/Market Cap gap
- Smaller max supply limits potential damage from mint exploits
- Burns become more impactful against smaller supply

### Negative

- If a future exploit or need arises requiring more $RUNE than the remaining Reserve, a new ADR and node vote would be required to mint the shortfall

### Neutral

- These are largely optics changes that do not materially affect protocol functioning
- The ongoing system income burn (ADR-017) continues to reduce supply over time
