# Quick Start Guide

[Lending](https://docs.thorchain.org/thornodes/archived/lending) allows users to deposit native collateral, and then create a debt at a collateralization ratio `CR` (collateralization ratio). The debt is always denominated in USD (aka `TOR`) regardless of what L1 asset the user receives.

```admonish info
Lending will be moved to the App Layer; new loans within THORChain can no longer be opened.
```

## Open a Loan Quote

Lending Quote endpoints have been created to simplify the implementation process.

**Request:** Loan quote using 1 BTC as collateral, target debt asset is USDT at 0XDAC17F958D2EE523A2206206994597C13D831EC7

[https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/loan/open?from_asset=BTC.BTC\&amount=10000000\&to_asset=ETH.USDT-0xdac17f958d2ee523a2206206994597c13d831ec7\&destination=0xe7062003a7be4df3a86127293a0d6b1f54c04220](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/loan/open?from_asset=BTC.BTC&amount=10000000&to_asset=ETH.USDT-0xdac17f958d2ee523a2206206994597c13d831ec7&destination=0xe7062003a7be4df3a86127293a0d6b1f54c04220)

**Response:**

```json
{
  "dust_threshold": "10000",
  "expected_amount_out": "112302802900",
  "expected_collateral_deposited": "9997829",
  "expected_collateralization_ratio": "31467",
  "expected_debt_issued": "112887730000",
  "expiry": 1698901398,
  "fees": {
    "asset": "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
    "liquidity": "114988700",
    "outbound": "444599700",
    "slippage_bps": 10,
    "total": "559588400",
    "total_bps": 49
  },
  "inbound_address": "bc1qmed4v5am2hcg8furkeff2pczdnt0qu4flke420",
  "inbound_confirmation_blocks": 1,
  "inbound_confirmation_seconds": 600,
  "memo": "$+:ETH.USDT:0xe7062003a7be4df3a86127293a0d6b1f54c04220",
  "notes": "First output should be to inbound_address, second output should be change back to self, third output should be OP_RETURN, limited to 80 bytes. Do not send below the dust threshold. Do not use exotic spend scripts, locks or address formats (P2WSH with Bech32 address format preferred).",
  "outbound_delay_blocks": 3,
  "outbound_delay_seconds": 18,
  "recommended_min_amount_in": "156000",
  "warning": "Do not cache this response. Do not send funds after the expiry."
}
```

_If you send 1 BTC to `bc1q2hldv0pmy9mcpddj2qrvdgcx6pw6h6h7gqytwy` with the_ [_memo_](../../concepts/memos.md) _`$+:ETH.USDT:0xe7062003a7be4df3a86127293a0d6b1f54c04220` you will receive approx. 1128.8773 USDT debt sent to `0xe7062003a7be4df3a86127293a0d6b1f54c04220` with a CR of 314.6% and will incur 49 basis points (0.49%) slippage._

```admonish danger
The `Inbound_Address` changes regularly, do not cache!
```

```admonish warning
Loans cannot be repaid until a minimum time has passed, as determined by [LOANREPAYMENTMATURITY](https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir), which is currently set as the current block height plus LOANREPAYMENTMATURITY. Currently, LOANREPAYMENTMATURITY is set to 432,000 blocks, equivalent to 30 days. Increasing the collateral on an existing loan to obtain additional debt resets the period.
```

## **Close a Loan**

**Request**: Repay a loan using USDT where BTC.BTC was used as collateral. Note any asset can be used to repay a loan. [https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/loan/close?from_asset=ETH.USDT&repay_bps=100&to_asset=BTC.BTC&loan_owner=bc1qfj7q8segu8wqvgahyvxnx8wgxpkev8f4dfswd2&min_out=862898890000](https://gateway.liquify.com/chain/thorchain_api/thorchain/quote/loan/close?from_asset=ETH.USDT&repay_bps=100&to_asset=BTC.BTC&loan_owner=bc1qfj7q8segu8wqvgahyvxnx8wgxpkev8f4dfswd2&min_out=862898890000)

**Response:**

```json
{
  "inbound_address": "0xd6bc2385ced3da50bcb4eaf70d8f6fa7f5e826e5",
  "inbound_confirmation_blocks": 2,
  "inbound_confirmation_seconds": 24,
  "outbound_delay_blocks": 57,
  "outbound_delay_seconds": 342,
  "fees": {
    "asset": "BTC.BTC",
    "outbound": "1554",
    "liquidity": "39246",
    "total": "40800",
    "slippage_bps": 1,
    "total_bps": 15
  },
  "router": "0xD37BbE5744D730a1d98d8DC97c42F0Ca46aD7146",
  "expiry": 1722577225,
  "warning": "Do not cache this response. Do not send funds after the expiry.",
  "notes": "Base Asset: Send the inbound_address the asset with the memo encoded in hex in the data field. Tokens: First approve router to spend tokens from user: asset.approve(router, amount). Then call router.depositWithExpiry(inbound_address, asset, amount, memo, expiry). Asset is the token contract address. Amount should be in native asset decimals (eg 1e18 for most tokens). Do not send to or from contract addresses.",
  "recommended_min_amount_in": "1472610400",
  "recommended_gas_rate": "1",
  "gas_rate_units": "gwei",
  "memo": "$-:BTC.BTC:bc1qfj7q8segu8wqvgahyvxnx8wgxpkev8f4dfswd2",
  "expected_amount_out": "25776996",
  "expected_amount_in": "866067076971",
  "expected_collateral_withdrawn": "25822467",
  "expected_debt_repaid": "863717538819",
  "streaming_swap_blocks": 20,
  "streaming_swap_seconds": 120,
  "total_repay_seconds": 486
}
```

_If you send 8637.17 USDT with a memo `$-:BTC.BTC:bc1q089j003xwj07uuavt2as5r45a95k5zzrhe4ac3` you will repay your loan off and cancel out the debt of 862898890000 TOR. This includes a 15 bps slip._

### **Borrowers Position**

**Request:**\
Get borrower's position in the BTC pool who took out a loan from `bc1q089j003xwj07uuavt2as5r45a95k5zzrhe4ac3`\
[https://gateway.liquify.com/chain/thorchain_api/thorchain/pool/BTC.BTC/borrower/bc1q089j003xwj07uuavt2as5r45a95k5zzrhe4ac3](https://gateway.liquify.com/chain/thorchain_api/thorchain/pool/BTC.BTC/borrower/bc1q089j003xwj07uuavt2as5r45a95k5zzrhe4ac3)\

**Response:**

```json
{
  "owner": "bc1q089j003xwj07uuavt2as5r45a95k5zzrhe4ac3",
  "asset": "BTC.BTC",
  "debt_issued": "114947930000",
  "debt_repaid": "115003339808",
  "debt_current": "0",
  "collateral_deposited": "9997123",
  "collateral_withdrawn": "9997123",
  "collateral_current": "0",
  "last_open_height": 12252923,
  "last_repay_height": 16443647
}
```

_The borrower has provided 0.0997 BTC and had a TOR debt of $1149.78. The loan has been fully repaid._

All Borrowers position can be seen at [https://gateway.liquify.com/chain/thorchain_api/thorchain/pool/BTC.BTC/borrowers](https://gateway.liquify.com/chain/thorchain_api/thorchain/pool/BTC.BTC/borrowers)

## More Information

1. [Lending Documentation](https://docs.thorchain.org/thornodes/archived/lending)
1. [Lending Module](https://gateway.liquify.com/chain/thorchain_api/thorchain/balance/module/lending)
1. [Lending Mint and Burn Chart](https://thorcharts.org/thorchain_lending_rune_burned)
1. [Lending Health Dashboard](https://dashboards.ninerealms.com/#lending)
1. [Other Lending Resources](https://docs.thorchain.org/thornodes/archived/lending#historical-resources)

### Support

Developers experiencing issues with these APIs can go to the THORChain Dev Discord for assistance. Interface developers should subscribe to the #interface-alerts channel for information pertinent to the endpoints and functionality discussed here.
