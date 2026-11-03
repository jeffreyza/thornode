# Swapper Clout

Swapper Clout allows traders to have immediate and faster swaps while maintaining the security features of [delayed outbounds](./delays.md#outbound-delay).

Clout is established by fees paid (in RUNE) for swaps/trades. The more fees paid, the higher the clout. Swappers with a high clout have proven themselves to be highly aligned to the project and therefore can reap the rewards by getting faster trade execution. The higher the clout, the less their outbound transactions are delayed or removed entirely if their score is high enough.

By reducing traders' outbound delay time, Swapper Clout reduces the outbound queue allowing for a better UX for normal users.

## Implementation Detail

When the outbound delay is calculated, the clout score is subtracted from the outbound value (in RUNE), causing the delay amount to be reduced (maybe even eliminated). If there is already a scheduled outbound with the same address, the value of the clout applied reduces (or removes) the clout applied to this outbound txn (increasing delay). This is to ensure that clout is collectively applied to all current outbounds, not on a one-by-one basis. This is to ensure that an individual cannot have a clout score of 100 RUNE, and make infinite zero-delay swaps of 100 RUNE value simultaneously.

Swapper Clout is stored per address and accumulates as users pay swap fees. The clout score for each address is tracked, restored, and spent as follows:

### Clout Tracking

- Every swap fees paid (in RUNE) are used to increment the clout score of:

  - The inbound sender address
  - The outbound recipient address

- These scores are stored in `SwapperClout` records, tracked by height and available balance.

### Clout Decay and Reset

- Clout regenerates over time.
- If an address has not spent clout in `CloutReset` blocks, it will fully regenerate to its original available value.
- This means that traders who pause their activity will eventually regain their full clout score over time.

### Clout Utilisation

When calculating outbound delay (`CalcTxOutHeight`), the system performs the following steps:

1. **Retrieve clout for both sender and recipient** involved in the swap.
2. **Restore** clout if it has been idle for long enough (`CloutReset`).
3. **Determine spendable clout**:

   - Total clout applied is limited by `CloutLimit` and split between the inbound and outbound addresses.
   - The applied clout is proportionally split using the `splitClout` function.
   - The swap’s effective RUNE value is reduced by the applied clout.

4. **Spend clout** from the involved addresses and record it at the current block height.

If clout is high enough to completely offset the swap value, then the outbound can be scheduled immediately. Otherwise, the reduced value is used to calculate delay based on system load and queue volume.

### Delay Calculation

To calculate delay:

- ov: Outbound transaction value in RUNE
- c: Current address clout score
- cu: Current Clout Utilisation

Then:

$$
\text{delay} = delaycalc(ov - (c - cu))
$$

Delays are still bounded by `TxOutDelayMax` and `MaxTxOutOffset` values.

## Benefits

This feature rewards active users, particularly power traders and arbitrageurs, who:

- Help maintain pool balance
- Pay frequent swap fees
- Operate efficiently

Because clout is shared between sender and recipient and decays across transactions, it is not possible to game the system by making multiple simultaneous zero-delay swaps. The system ensures fairness by distributing clout impact across all pending outbound txs involving an address.

The net result is:

- Faster execution for power users
- No increased delay for casual users
- Reduced outbound congestion overall
