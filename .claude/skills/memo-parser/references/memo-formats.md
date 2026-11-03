# Memo Formats

Source of truth: `stringToTxTypeMap` in `x/thorchain/memo/memo.go:61` and each
`Parse*Memo` in `x/thorchain/memo/memo_<type>.go`. Field positions below are the
`:`-separated part index the parser reads (`p.get(n)`). User-facing wording lives
in `docs/concepts/memos.md`; this file is for code work.

## Prefix to type

Prefixes are matched case-insensitively (`StringToTxType` lowercases).

| Prefix(es)                                        | TxType                    | File                                                                                     | Format (after the prefix)                                                          |
| ------------------------------------------------- | ------------------------- | ---------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| `swap` `s` `=`                                    | TxSwap                    | memo_swap.go                                                                             | `:ASSET:DEST[/REFUND]:LIMIT[/INTERVAL/QUANTITY]:AFF:BPS:DEXAGG:DEXTARGET:DEXLIMIT` |
| `=<`                                              | TxLimitSwap               | memo_swap.go                                                                             | same as swap; a nonzero LIMIT makes `SwapType_limit`                               |
| `m=<`                                             | TxModifyLimitSwap         | memo_modify_limit_swap.go                                                                | `:SOURCECOIN:TARGETCOIN:NEWTARGETAMOUNT`                                           |
| `add` `+`                                         | TxAdd                     | memo_add.go                                                                              | `:ASSET:PAIREDADDR:AFFILIATE:BPS`                                                  |
| `withdraw` `wd` `-`                               | TxWithdraw                | memo_withdraw.go                                                                         | `:ASSET:BPS:WITHDRAWASSET` (BPS defaults to 10000)                                 |
| `donate` `d`                                      | TxDonate                  | memo_donate.go                                                                           | `:ASSET`                                                                           |
| `bond`                                            | TxBond                    | memo_bond.go                                                                             | `:NODE:PROVIDER:OPERATORFEE` (fee int64, -1 = unset)                               |
| `unbond`                                          | TxUnbond                  | memo_unbond.go                                                                           | `:NODE:AMOUNT:PROVIDER` (AMOUNT required, 1e8 units)                               |
| `rebond`                                          | TxRebond                  | memo_rebond.go                                                                           | `:NODE:NEWPROVIDER:AMOUNT`                                                         |
| `bondyield`                                       | TxBondYield               | memo_bond_yield.go                                                                       | `:NODE:BPS:PAYOUTTARGET`                                                           |
| `leave`                                           | TxLeave                   | memo_leave.go                                                                            | `:NODE`                                                                            |
| `maint`                                           | TxMaint                   | memo_maint.go                                                                            | `:NODE`                                                                            |
| `operator`                                        | TxOperatorRotate          | memo_rotate.go                                                                           | `:OPERATORADDR`                                                                    |
| `name` `n` `~`                                    | TxTHORName                | memo_thorname.go                                                                         | `:NAME:CHAIN:ADDR:OWNER:PREFERREDASSET:EXPIRE:MULTIPLIER`                          |
| `trade+` / `trade-`                               | TxTradeAccount\*          | memo_trade_account.go                                                                    | `trade+:THORADDR` / `trade-:L1ADDR`                                                |
| `secure+` / `secure-`                             | TxSecuredAsset\*          | memo_secured_asset.go                                                                    | `secure+:THORADDR` / `secure-:L1ADDR`                                              |
| `stable+` / `stable-`                             | TxStableReserve\*         | memo_stable_reserve.go                                                                   | `stable+` / `stable-:AMOUNT`                                                       |
| `pool+` / `pool-`                                 | TxRunePool\*              | memo_rune_pool.go                                                                        | `pool+` / `pool-:BPS:AFFILIATE:AFFBPS`                                             |
| `tcy`                                             | TxTCYClaim                | memo_tcy_claim.go                                                                        | `:THORADDR` (must be a THOR address)                                               |
| `tcy+` / `tcy-`                                   | TxTCYStake / TxTCYUnstake | memo_tcy_stake.go / memo_tcy_unstake.go                                                  | `tcy+` / `tcy-:BPS`                                                                |
| `switch`                                          | TxSwitch                  | memo_switch.go                                                                           | `:THORADDR`                                                                        |
| `reference`                                       | TxReferenceWriteMemo      | memo_reference.go                                                                        | `:ASSET:<memo>` (embedded memo must itself parse)                                  |
| `r`                                               | TxReferenceReadMemo       | memo_reference.go                                                                        | `:REF`                                                                             |
| `reserve`                                         | TxReserve                 | memo_reserve.go                                                                          | none                                                                               |
| `noop`                                            | TxNoOp                    | memo_noop.go                                                                             | `[:action]`                                                                        |
| `out` `refund` `migrate` `ragnarok` `consolidate` | system types              | memo_outbound.go, memo_refund.go, memo_migrate.go, memo_ragnarok.go, memo_consolidate.go | `:TXID` (vault-generated, never user-sent)                                         |

There is no `exec`/`x`, `a:`, `$+`/`$-`, or `loan+`/`loan-` prefix on develop. A
memo using one fails with `invalid tx type: <prefix>`.

## Swap field details

- DEST may be a THORName (resolved per target chain) and may carry `/REFUND`.
- LIMIT accepts scientific notation (`1e8`, exponent at most 77). It is parsed by
  `getUintWithScientificNotation`, or by `parseTradeTarget` when the field contains `/`.
- Streaming order is `LIMIT/INTERVAL/QUANTITY`. `0/100/10` means no limit,
  interval 100 blocks, quantity 10 sub-swaps. Empty sub-fields become `0`.
- AFF and BPS are `/`-separated lists. A single BPS applies to every affiliate.
  The count is capped by the `MultipleAffiliatesMaxCount` mimir, and total BPS must be at most
  `constants.MaxBasisPts` (10000).
- DEXAGG / DEXTARGET / DEXLIMIT are passed through; the handler resolves the
  target with `externalAssetMatch` (handler.go:388).

## Add details

- On chains where `RequiresExplicitRefundAddress()` is true (XMR), PAIREDADDR is
  `addr/assetAddr`.
- For synth assets the affiliate is resolved on the L1 chain.

## Modify limit swap coins

Coins are written `<amount><asset>` such as `100btc~btc`. Trade, synth and
secured assets go through `common.ParseCoin` (`getCoin`, memo_parser.go:472).
There is no TXID field.

## Asset short codes

`getAsset` calls `common.NewAssetWithShortCodes` (common/asset.go:127). That
function ignores its version argument and uses the fixed map `assetShortCodes`
(common/asset.go:133), built from each gas asset's `ShortCode()`.
Fuzzy pool matching (`=:ETH.USDC:...` resolving a full contract symbol) is not done
in the parser. It is `fuzzyAssetMatch` in handler.go:326, applied to add,
withdraw, swap and donate.

## String round-trip gaps

`TxType.String()` reads `txToStringMap` (memo.go:107). TxTHORName gives
`thorname`, which `StringToTxType` rejects. TxRunePoolDeposit/Withdraw and
TxOperatorRotate have no entry, so they return `""`. Don't build memos from
`TxType.String()`.
