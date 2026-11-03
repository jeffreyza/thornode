# Memoless Swaps (Bitcoin and Limited Memo Chains)

For chains with limited memo space (like Bitcoin's 80-byte OP_RETURN), THORChain supports memoless transactions using reference memos. This guide provides a quick start for implementing memoless swaps.

```admonish info
For complete technical details on memoless transactions, see [Memos - Reference Memo](../concepts/memos.md#reference-memo---memoless-transactions).
```

## Overview

Memoless swaps allow you to:

- Register a swap memo once, use multiple times
- Encode reference numbers in transaction amounts
- Avoid memo length restrictions on UTXO and other memo-limited chains

## Memoless Swap Checklist

### Phase 1: Register Reference Memo

1. **Send Registration Transaction**: Send RUNE to inbound address with memo:

   ```text
   REFERENCE:BTC.BTC:=:ETH.ETH:0x86d526d6624AbC0178cF7296cD538Ecc080A95F1
   ```

   See [REFERENCE memo format](../concepts/memos.md#registering-a-reference-memo) for details.

2. **Wait for Confirmation**: Monitor the registration transaction hash

3. **Get Reference Number**: Call `GET /thorchain/memo/{registration_hash}` once block is final

   ```json
   {
     "reference": "20002",
     "asset": "BTC.BTC",
     "memo": "=:ETH.ETH:0x86d526d6624AbC0178cF7296cD538Ecc080A95F1",
     "height": "12345678"
   }
   ```

### Phase 2: Execute Memoless Swap

1. **Validate Reference**: Call `GET /thorchain/memo/BTC.BTC/20002` before broadcasting

   - Ensure response is valid and not expired
   - Check usage count within limits
   - Verify [reference lifecycle state](../concepts/memos.md#reference-memo-lifecycle)

2. **Encode Amount**:

   ```text
   Desired amount: 0.05 BTC = 5,000,000 sats
   Reference: 20002
   Final amount: 5,020,002 sats
   ```

   See [Encoding Algorithm](../concepts/memos.md#use-reference-memo-amount-encoded) for detailed steps.

3. **Send Bitcoin Transaction**:

   - **Amount**: 5,020,002 sats
   - **To**: BTC inbound address from [`/thorchain/inbound_addresses`](../concepts/querying-thorchain.md#getting-the-asgard-vault)
   - **Memo**: Empty (reference encoded in amount) or `R:20002` (explicit reference)

   See [Sending Transactions - UTXO Chains](../concepts/sending-transactions.md#utxo-chains) for transaction structure.

4. **Monitor Results**: Reference will be marked as used and require re-registration for future swaps

   Check usage via `GET /thorchain/memo/BTC.BTC/20002`

## Memoless Error Conditions

| **Condition**        | **Resolution**                                                       |
| -------------------- | -------------------------------------------------------------------- |
| Reference Not Found  | Re-register or check reference number                                |
| Reference Expired    | Re-register with new reference (TTL expired)                         |
| Usage Limit Exceeded | Re-register for additional uses                                      |
| Same Block Usage     | Wait for next block after registration                               |
| Memoless Halted      | Check [HaltMemoless mimir](../mimir.md#memoless-transactions) status |

## Memoless Transaction Errors

### Reference Not Found Error

```json
{ "error": "reference memo not found for asset BTC.BTC reference 20002" }
```

**Resolution**: Re-register the reference memo or verify reference number

### Reference Expired Error

```json
{ "error": "reference memo expired for asset BTC.BTC reference 20002" }
```

**Resolution**: Register a new reference memo with same parameters. References expire after `MemolessTxnTTL` blocks (default: 3600 blocks ≈ 6 hours).

### Usage Limit Exceeded Error

```json
{ "error": "reference memo usage limit exceeded for reference 20002" }
```

**Resolution**: Register a new reference memo for additional uses. Default limit is `MemolessTxnMaxUse` = 1 use per reference.

### Same Height Usage Error

```json
{ "error": "transaction observed before reference memo creation" }
```

**Resolution**: Wait for next block after registration before using reference. Transactions must be observed **after** the reference registration height.

### Memoless Halted Error

```json
{ "error": "memoless transactions are currently halted" }
```

**Resolution**: Check THORChain status - emergency halt is active. Monitor [HaltMemoless mimir](../concepts/network-halts.md#memoless-transactions) and wait for unhalt before attempting memoless transactions.

## Pre-Flight Validation API

Always validate before broadcasting:

```bash
# Check if reference exists and is valid
GET /thorchain/memo/{asset}/{reference}

# Preview reference extraction from amount
GET /thorchain/memo/check/{asset}/{amount}
```

```admonish info
Reference memos expire after ~6 hours (`MemolessTxnTTL`). If your inbound transaction remains unconfirmed beyond the TTL, THORChain can no longer resolve the reference and the swap will not execute. You are responsible for ensuring confirmation before expiry.
fees and confirmation times appropriately.
```

### Best practice

- **Use RBF/fee-bump** to accelerate confirmation if mempool congestion increases.
- If confirmation before TTL looks unlikely, **RBF-cancel (double-spend to self)** and **re-register** a fresh reference, then resend.
- Don’t broadcast close to expiry; choose a feerate that targets confirmation **well within** the TTL (with buffer).

```admonish warning
Transactions that arrive after TTL may be unrecoverable without manual intervention. Proceed only if you can manage fees and confirmation times appropriately.
```

See [API Endpoints](../concepts/memos.md#use-reference-memo-amount-encoded) for full documentation.

## Related Documentation

- [Memos - Reference Memo Technical Details](../concepts/memos.md#reference-memo---memoless-transactions)
- [Reference Memo Lifecycle States](../concepts/memos.md#reference-memo-lifecycle)
- [Amount Encoding Algorithm](../concepts/memos.md#use-reference-memo-amount-encoded)
- [Sending Transactions - UTXO Chains](../concepts/sending-transactions.md#utxo-chains)
- [Memoless Transaction Mimirs](../mimir.md#memoless-transactions)
- [Network Halts - Memoless](../concepts/network-halts.md#memoless-transactions)
