# Sending Transactions

Confirm you have:

- [ ] Connected to Midgard or THORNode
- [ ] Located the latest vault (and router) for the chain
- [ ] Prepared the transaction details (and memo)
- [ ] Checked the network is not halted for your transaction

You are ready to make the transaction and swap via THORChain.

## UTXO Chains

### Memo of 80 bytes or fewer

For UTXO-based chains (e.g., BTC, BCH, LTC, DOGE, DASH), transactions must follow a specific structure to be processed by THORChain. Memo lengths refer to UTF-8 bytes, not characters. Ensure the following steps are completed to avoid transaction failures or loss of funds.

#### Checklist for UTXO Transactions

- [ ] **Verify supported address type**: Ensure the address type (e.g., P2PKH, P2SH) is supported by THORChain. Check supported formats in [Querying THORChain](./querying-thorchain.md#supported-address-formats). Note: LTC MWEB addresses (ltcmweb1) and peg-in transactions are not supported.
- [ ] **Set Asgard vault as VOUT0**: Send the transaction amount to the current Asgard vault address as the first output (VOUT0), obtainable from the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint.
- [ ] **Return change to VIN0**: Direct all change back to the input address (VIN0) in a subsequent output, e.g., VOUT1, as THORChain identifies the user by VIN0 for refunds.
- [ ] **Include memo in OP_RETURN**: Add the transaction memo as an OP_RETURN output, typically in VOUT2, to specify the user’s intent (e.g., swap, add liquidity). Refer to [Memos](./memos.md) for format details.
- [ ] **Use sufficient gas rate**: Set a `gas_rate` high enough to ensure inclusion in the next block, as specified in the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint.
- [ ] **Exceed dust threshold**: Ensure the transaction amount exceeds the chain’s dust threshold. Verify the latest values in the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint or [Dust Thresholds and Transaction Validation](#dust-thresholds-and-transaction-validation).
- [ ] **Limit to 10 outputs**: Ensure the transaction has no more than 10 outputs to comply with THORChain’s processing limits.
- [ ] Do not send funds that are part of a transaction with more than 10 outputs

### Memo longer than 80 bytes

- [ ] Ensure the [address type](./querying-thorchain.md#supported-address-formats) is supported
- [ ] Send the transaction with Asgard vault as VOUT0
- [ ] Pass all change back to the VIN0 address in a subsequent VOUT e.g. VOUT1
- [ ] Take the first 79 bytes of the memo, append `^`, and use those 80 bytes as an OP_RETURN in a subsequent VOUT, e.g. VOUT2
- [ ] Add the remaining memo bytes as subsequent continuation outputs
  - [ ] Split the remaining bytes into 20-byte chunks and pad the final chunk with zero bytes to reach 20 bytes
  - [ ] Hex encode each chunk to obtain 40 hex characters
  - [ ] Use the chain's continuation script and output amount from the examples below. BTC/LTC use P2WPKH (`0014<chunk>`). Dash uses P2PKH (`76a914<chunk>88ac`) with 546 duffs per output; do not use the P2WPKH shape for Dash.
- [ ] Use a high enough `gas_rate` to be included
- [ ] Do not send below the dust threshold (10k Sats BTC, BCH, LTC, 1m DOGE), exhaustive values can be found on the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint

### Examples using dummy txs

#### P2WPKH (BTC, LTC)

Memo:
`SWAP:GAIA.ATOM:cosmos1fegapd4jc3ejqeg0eu3jk4hvr74hg66076gyyd/bc1qnfw0gkk05qxl38mslc69hc6vc64mksyw6zzxhg`

1. Put first 79 bytes + `^` into `OP_RETURN`: `SWAP:GAIA.ATOM:cosmos1fegapd4jc3ejqeg0eu3jk4hvr74hg66076gyyd/bc1qnfw0gkk05qxl38^`
2. Hex encode remaining string `mslc69hc6vc64mksyw6zzxhg` and split it into chunks of 40 characters (fill the last chunk with zeros): `6d736c633639686336766336346d6b737977367a`, `7a78686700000000000000000000000000000000`
3. Create two subsequent VOUTs (prepend `0014`):
   1. send 294sats (2940sats on LTC) to: `00146d736c633639686336766336346d6b737977367a`
      - BTC: bc1qd4ekccek895xxdnkvvmrgmttwduhwdn622k6gj
      - LTC: ltc1qd4ekccek895xxdnkvvmrgmttwduhwdn6wkv7sz
   2. send 294sats (2940sats on LTC) to: `00147a78686700000000000000000000000000000000`
      - BTC: bc1q0fuxsecqqqqqqqqqqqqqqqqqqqqqqqqq2alhdv
      - LTC: ltc1q0fuxsecqqqqqqqqqqqqqqqqqqqqqqqqqwp9n4u

#### P2PKH (BCH, DOGE)

Memo:
`SWAP:GAIA.ATOM:cosmos1fegapd4jc3ejqeg0eu3jk4hvr74hg66076gyyd/bc1qnfw0gkk05qxl38mslc69hc6vc64mksyw6zzxhg`

1. Put first 79 bytes + `^` into `OP_RETURN`: `SWAP:GAIA.ATOM:cosmos1fegapd4jc3ejqeg0eu3jk4hvr74hg66076gyyd/bc1qnfw0gkk05qxl38^`
2. Hex encode remaining string `mslc69hc6vc64mksyw6zzxhg` and split it into chunks of 40 characters (fill the last chunk with zeros): `6d736c633639686336766336346d6b737977367a`, `7a78686700000000000000000000000000000000`
3. Create two subsequent VOUTs (prepend `76a914` & append `88ac`):
   1. send 546sats to: `76a9146d736c633639686336766336346d6b737977367a88ac`
      - BCH: qpkhxmrrxcukscekwe3nvdrdddehjaek0gldczy2mv
      - DOGE: DF7pTvdzyY2zoVJ3AFQr8oSYDiqw3m6hCy
   2. send 546sats to: `76a9147a7868670000000000000000000000000000000088ac`
      - BCH: qpa8s6r8qqqqqqqqqqqqqqqqqqqqqqqqqq99h3g9e2
      - DOGE: DGJfDk6cwyNpzebP7MyzueRtXEWGEaaHz9

#### P2PKH (DASH)

Using the same 103-byte memo above:

1. Put the first 79 bytes plus `^` into `OP_RETURN`: `SWAP:GAIA.ATOM:cosmos1fegapd4jc3ejqeg0eu3jk4hvr74hg66076gyyd/bc1qnfw0gkk05qxl38^`
2. Split the remaining 24 bytes, `mslc69hc6vc64mksyw6zzxhg`, into 20-byte chunks, padding the last chunk with zero bytes.
3. Add these two continuation outputs, in this order, each with **546 duffs (0.00000546 DASH)**:
   - `76a9146d736c633639686336766336346d6b737977367a88ac`
   - `76a9147a7868670000000000000000000000000000000088ac`

The 546-duff amount is for memo continuation outputs. The deposit sent to the vault must still exceed the separate chain dust threshold from the Inbound Addresses endpoint.

```admonish warning
Inbound transactions must not be delayed to avoid sending funds to an outdated Asgard vault address, which may be unreachable. Use standard transactions, verify the latest Asgard vault address via the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint, and use the recommended `gas_rate` to ensure confirmation in the next block.
```

```admonish info
Memo limited to 80 bytes in `OP_RETURN` on BTC, BCH, LTC, DOGE and DASH. Use [abbreviated options](./memo-length-reduction.md) where possible. When shortening is not enough, or for reusable memos, see [Memoless Swaps](../swap-guide/memoless-swaps.md).
```

```admonish warning
Do not use HD wallets that forward the change to a new address, because THORChain IDs the user as the address in VIN0. The user must keep their VIN0 address funded for refunds.
```

```admonish danger
Override randomised VOUT ordering; THORChain requires specific output ordering. Funds using wrong ordering are very likely to be lost.
```

### EVM Chains

To perform a transaction on EVM-based chains (e.g., ETH, BSC, AVAX, BASE), use the `depositWithExpiry` function on the THORChain Router contract (version 4.1). The contract source is available at [THORChain_Router.sol](https://gitlab.com/thorchain/thornode/-/blob/develop/chain/ethereum/contracts/THORChain_Router.sol). Ensure the following steps are completed to avoid transaction failures or loss of funds.

#### Checklist for EVM Transactions

- [ ] **Approve ERC-20 tokens (if applicable)**: For ERC-20 tokens, call `approve` on the token contract to allow the THORChain Router to spend the specified amount. This step is not required for native assets (e.g., ETH on Ethereum, AVAX on Avalanche, BNB on BSC).
- [ ] **Target the Asgard vault**: Set the `vault` parameter to the current Asgard vault address, obtainable from the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint.
- [ ] **Specify the asset**: Use the token contract address for ERC-20 tokens or `0x0000000000000000000000000000000000000000` for the chain’s native asset (e.g., ETH, AVAX, BNB).
- [ ] **Include the memo**: Provide the transaction memo as a UTF-8 encoded string to specify the user’s intent (e.g., swap, add liquidity). Refer to [Memos](./memos.md) for format details.
- [ ] **Set an expiry**: Use a Unix timestamp (in seconds) at least 60 minutes in the future for the `expiry` parameter. Transactions delayed beyond this timestamp will be refunded.
- [ ] **Use sufficient gas**: Set a `gas_rate` high enough to ensure inclusion in the next block, as specified in the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint.
- [ ] **Exceed dust threshold**: Ensure the transaction amount exceeds the chain’s dust threshold. See [Dust Thresholds and Transaction Validation](#dust-thresholds-and-transaction-validation) for details.
- [ ] **Call `depositWithExpiry`**: Execute the `depositWithExpiry` function on the THORChain Router contract, passing the vault address, asset, amount, memo, and expiry. For native assets, include the amount in the transaction’s `value` field.

#### Calling `depositWithExpiry`

The `depositWithExpiry` function on the THORChain Router contract is defined as:

```solidity
function depositWithExpiry(
    address payable vault,
    address asset,
    uint256 amount,
    string memory memo,
    uint256 expiry
) external payable;
```

```admonish info
For native assets like ETH, set `asset` to `0x0000000000000000000000000000000000000000` and include the `amount` in the transaction’s `value` field.
```

```admonish warning
Ensure the transaction’s `gas_rate` is sufficient for inclusion in the next block. Check the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint for the recommended `gas_rate`.
```

```admonish danger
ETH is sent and received as an internal transaction. Your wallet may not be set to read internal balances and transactions.
```

```admonish info
EIP-7702 (Prague upgrade) is supported on all EVM chains. THORChain uses the Prague signer which supports type-4 transactions and EIP-7702 account abstraction features.
```

### BFT Chains

- [ ] Send the transaction to the Asgard vault
- [ ] Include the memo
- [ ] Only use the base asset as the choice for gas asset

### XRP Ledger

- [ ] Send the transaction to the Asgard vault
- [ ] Include the `DestinationTag` in the memo field if sending to a centralised exchange or shared address
- [ ] Include the [memo](./memos.md) with user intent in the transaction
- [ ] Use a high enough `gas_rate` to be included, as specified in the [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint
- [ ] Ensure the transaction amount exceeds the dust threshold; see [Dust Thresholds and Transaction Validation](#dust-thresholds-and-transaction-validation)
- [ ] Ensure the transaction is a Payment type, as THORChain only processes XRP Payment transactions
- [ ] Use the correct sequence number for the sending account, obtainable via the `GetAccount` method

## Dust Thresholds and Transaction Validation

THORChain enforces dust thresholds to prevent dust attacks, where negligible amounts are sent to clog the network. The dust threshold is the minimum transaction amount required for a Layer 1 (L1) chain to ensure THORChain processes the transaction and its associated memo. Transactions with amounts **equal to or below the dust threshold** are ignored by the network.

### Dust Threshold Rules

```admonish warning
Ensure transaction amounts exceed the dust threshold for the chain to avoid being ignored. Verify the latest dust threshold at [Inbound Addresses](https://gateway.liquify.com/chain/thorchain_api/thorchain/inbound_addresses) endpoint before sending to ensure the amount is sufficient to trigger the desired action on THORChain.
```

See [the Dust Thresholds Section](../bifrost/vault-behaviors.md#dust-thresholds) for full information.

## THORChain

To initiate a $RUNE -> $ASSET swap a `MsgDeposit` must be broadcasted to the THORChain blockchain. The `MsgDeposit` does not have a destination address, and has the following properties. The full definition can be found [here](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/types/msg_deposit.go).

```go
MsgDeposit{
    Coins:  coins,
    Memo:   memo,
    Signer: signer,
}
```

If you are using Javascript, [CosmJS](https://github.com/cosmos/cosmjs) is the recommended package to build and broadcast custom message types. [Here is a walkthrough](https://github.com/cosmos/cosmjs/blob/main/packages/stargate/CUSTOM_PROTOBUF_CODECS.md).

### Code Examples (Javascript)

1. **Generate codec files.** To build/broadcast native transactions in Javascript/Typescript, the protobuf files need to be generated into js types. The below script uses `pbjs` and `pbts` to generate the types using the relevant files from the THORNode repo. Alternatively, the .`js` and `.d.ts` files can be downloaded directly from the [XChainJS repo](https://github.com/xchainjs/xchainjs-lib/tree/master/packages/xchain-thorchain/src/types/proto).

   ```bash
   #!/bin/bash

   # this script checks out thornode master and generates the proto3 typescript buindings for MsgDeposit and MsgSend

   MSG_COMPILED_OUTPUTFILE=src/types/proto/MsgCompiled.js
   MSG_COMPILED_TYPES_OUTPUTFILE=src/types/proto/MsgCompiled.d.ts

   TMP_DIR=$(mktemp -d)

   tput setaf 2; echo "Checking out https://gitlab.com/thorchain/thornode to $TMP_DIR";tput sgr0
   (cd $TMP_DIR && git clone https://gitlab.com/thorchain/thornode)

   # Generate msgs
   tput setaf 2; echo "Generating $MSG_COMPILED_OUTPUTFILE";tput sgr0
   yarn run pbjs -w commonjs -t static-module $TMP_DIR/thornode/proto/thorchain/v1/common/common.proto $TMP_DIR/thornode/proto/thorchain/v1/x/thorchain/types/msg_deposit.proto $TMP_DIR/thornode/proto/thorchain/v1/x/thorchain/types/msg_send.proto $TMP_DIR/thornode/third_party/proto/cosmos/base/v1beta1/coin.proto -o $MSG_COMPILED_OUTPUTFILE

   tput setaf 2; echo "Generating $MSG_COMPILED_TYPES_OUTPUTFILE";tput sgr0
   yarn run pbts $MSG_COMPILED_OUTPUTFILE -o $MSG_COMPILED_TYPES_OUTPUTFILE

   tput setaf 2; echo "Removing $TMP_DIR/thornode";tput sgr0
   rm -rf $TMP_DIR
   ```

2. **Using @cosmjs build/broadcast the TX.**

   ```javascript
   const {
     DirectSecp256k1HdWallet,
     Registry,
   } = require("@cosmjs/proto-signing");
   const {
     defaultRegistryTypes: defaultStargateTypes,
     SigningStargateClient,
   } = require("@cosmjs/stargate");
   const { stringToPath } = require("@cosmjs/crypto");
   const bech32 = require("bech32-buffer");

   const { MsgDeposit } = require("./types/MsgCompiled").types;

   async function main() {
     const myRegistry = new Registry(defaultStargateTypes);
     myRegistry.register("/types.MsgDeposit", MsgDeposit);

     const signerMnemonic = "mnemonic here";
     const signerAddr = "thor1...";

     const signer = await DirectSecp256k1HdWallet.fromMnemonic(signerMnemonic, {
       prefix: "thor", // THORChain prefix
       hdPaths: [stringToPath("m/44'/931'/0'/0/0")], // THORChain HD Path
     });

     const client = await SigningStargateClient.connectWithSigner(
       "https://gateway.liquify.com/chain/thorchain_rpc/",
       signer,
       { registry: myRegistry },
     );

     const memo = `=:ETH/ETH:${signerAddr}`; // THORChain memo

     const msg = {
       coins: [
         {
           asset: {
             chain: "THOR",
             symbol: "RUNE",
             ticker: "RUNE",
           },
           amount: "100000000", // Value in 1e8 (100000000 = 1 RUNE)
         },
       ],
       memo: memo,
       signer: bech32.decode(signerAddr).data,
     };

     const depositMsg = {
       typeUrl: "types.MsgDeposit",
       value: MsgDeposit.fromObject(msg),
     };

     const fee = {
       amount: [],
       gas: "50000000", // Set arbitrarily high gas limit; this is not actually deducted from user account.
     };

     const response = await client.signAndBroadcast(
       signerAddr,
       [depositMsg],
       fee,
       memo,
     );
     console.log("response: ", response);

     if (response.code !== 0) {
       console.log("Error: ", response.rawLog);
     } else {
       console.log("Success!");
     }
   }

   main();
   ```

### Native Transaction Fee

As of [ADR-009](https://gitlab.com/thorchain/thornode/-/blob/develop/docs/architecture/adr-009-reserve-income-fee-overhaul.md), the native transaction fee for $RUNE transfers or inbound swaps is USD-denominated, but ultimately paid in $RUNE, which means the fee is dynamic. Interfaces should pull the native transaction fee from THORNode before each new transaction is built/broadcasted.

**THORNode Network Endpoint**: [/thorchain/network](https://gateway.liquify.com/chain/thorchain_api/thorchain/network)

```json
{
  "native_outbound_fee_rune": "2000000", // (1e8) Outbound fee for $Asset -> $RUNE swaps
  "native_tx_fee_rune": "2000000", // (1e8) Fee for $RUNE transfers or $RUNE -> $Asset swaps
  "rune_price_in_tor": "354518918" // (1e8) Current $RUNE price in USD
}
```

The native transaction fee is automatically deducted from the user's account for $RUNE transfers and inbound swaps. Ensure the user's balance exceeds `tx amount + native_tx_fee_rune` before broadcasting the transaction.

### Error Handling

When sending transactions to THORChain, several common errors can occur. Always implement proper error handling:

```javascript
try {
  const txResponse = await client.signAndBroadcast(signerAddress, [msg], fee);

  // Check if transaction was successful
  if (txResponse.code !== 0) {
    console.error("Transaction failed:", txResponse.rawLog);
    // Handle specific error codes
    if (txResponse.code === 5) {
      throw new Error("Insufficient funds");
    } else if (txResponse.code === 7) {
      throw new Error("Invalid memo format");
    }
    // Add more specific error handling as needed
  }

  console.log("Transaction successful:", txResponse.transactionHash);
} catch (error) {
  console.error("Failed to broadcast transaction:", error.message);
  // Implement retry logic or user notification
}
```

**Common Error Scenarios:**

- **Insufficient Balance**: Ensure account has enough RUNE for transaction + fees
- **Invalid Memo**: Check memo format against [transaction memo guidelines](memos.md)
- **Network Issues**: Implement retry logic with exponential backoff
- **Gas Estimation**: Use current gas rates from [inbound addresses endpoint](querying-thorchain.md)
