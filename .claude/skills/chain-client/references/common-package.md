# Common Package Changes for New Chains and Assets

When adding a new chain or asset, the `common/` package requires updates in many locations. This is the most error-prone step because missing a single switch case will cause runtime panics or silent failures. Use this as a checklist.

## chain.go — Chain Definition (14+ locations)

Every new chain requires updates to ALL of these locations in `common/chain.go`:

### 1. Chain Constant

```go
XxxChain = Chain("XXX")
```

### 2. AllChains Array

Add to the `AllChains` slice when the chain is production-ready. Chains NOT in AllChains are defined but disabled (e.g., SUIChain, ADAChain are defined but not in AllChains).

### 3. Chain Family Functions

- `GetEVMChains()` — add if EVM-compatible
- `GetUTXOChains()` — add if UTXO-based

### 4. GetSigningAlgo()

```go
case SOLChain, SUIChain, ADAChain:
    return SigningAlgoEd25519
// default: return SigningAlgoSecp256k1
```

### 5. GetGasAsset()

Map the chain to its native gas asset:

```go
case XxxChain:
    return XxxAsset
```

### 6. GetGasUnits()

Return the gas unit name and decimal multiplier:

```go
case XxxChain:
    return "unit-name", cosmos.NewUint(1e6)  // adjust decimals
```

### 7. GetGasAssetDecimal()

Return the number of decimal places for the gas asset. Common values:

- 6 decimals: GAIA, NOBLE, TRON, ADA, XRP
- 8 decimals: BTC, ETH, BSC, AVAX, BASE, POL (THORChain standard)
- 9 decimals: SOL, SUI

### 8. AddressPrefix()

Return address prefixes per network type (MockNet, MainNet, StageNet). Required for bech32-style or prefix-based address chains:

```go
case XxxChain:
    switch net {
    case MockNet:
        return "xxx_test"
    default:
        return "xxx"
    }
```

Not all chains need this (EVM chains use 0x prefix universally).

### 9. DustThreshold()

Minimum amount for withdraw transactions. Must be set to avoid dust attacks:

```go
case XxxChain:
    return cosmos.NewUint(10_000_000)  // in 1e8 THORChain units
```

### 10. P2WPKHOutputValue()

**UTXO chains only** — value for P2WPKH memo data outputs.

### 11. MaxMemoLength()

**UTXO chains only** — maximum memo length in bytes.

### 12. DefaultCoinbase()

**UTXO chains only** — default block coinbase emission value.

### 13. ApproximateBlockMilliseconds()

Approximate block time in milliseconds. Used for timing calculations:

```go
case XxxChain:
    return 5_000  // 5 seconds
```

### 14. InboundNotes()

User-facing notes about inbound transaction requirements:

```go
case XxxChain:
    return "Use the memo field for transaction details"
```

### 15. OutboundNotes()

User-facing notes about outbound transaction behavior (only if chain has special requirements).

---

## asset.go — Asset Definition (5-6 locations)

### 1. Asset Constant

```go
XxxAsset = Asset{Chain: XxxChain, Symbol: "XXX", Ticker: "XXX", Synth: false}
```

### 2. ShortCode()

Short code for compact memo encoding (1-2 characters):

```go
case "XXX.XXX":
    return "xx"
```

### 3. NewAssetWithShortCodesV3_1_0()

Register the short code in the versioned mapping:

```go
shorts[XxxAsset.ShortCode()] = XxxAsset.String()
```

### 4. Native()

**Only for whitelisted THORChain native assets** — maps asset to a native denom string.

### 5. IsWhitelisted()

**Only for base layer pool assets** — add to the whitelisted assets map.

---

## address.go — Address Validation (4 locations)

### 1. Chain-Specific Validator Function

Create an `IsValidXxxAddress()` function:

```go
func IsValidXxxAddress(addr string) bool {
    // Chain-specific address validation
}
```

Existing validators for reference:

- `IsValidXRPAddress()`
- `IsValidTRONAddress()`
- `IsValidSOLAddress()`
- `IsValidZECAddress()`
- `IsValidSUIAddress()`
- `IsValidADAAddress()`

### 2. NewAddress()

Wire the new validator into address creation. This function uses a mix of prefix checks and chain-specific validators.

### 3. IsChain()

Add chain detection logic based on address format:

```go
case XxxChain:
    return IsValidXxxAddress(addr.String())
```

### 4. GetChain()

Add to the chain detection loop array:

```go
for _, chain := range []Chain{..., XxxChain} {
```

**Note**: This array does not cover every chain — `BSCChain`, `BASEChain`, `POLChain`, and `NOBLEChain` are absent.
For the three EVM chains that is by design (per the function's own doc comment, any EVM address resolves to
`ETHChain`); `NOBLEChain` is simply unchecked.

### 5. GetNetwork()

Add network detection from address format (MockNet vs MainNet vs StageNet):

```go
case XxxChain:
    if strings.HasPrefix(addr, "xxx_test") {
        return MockNet
    }
    return MainNet
```

---

## pubkey.go — Public Key to Address Conversion (1 location, critical)

### GetAddress()

This is the **most critical and complex** update. Each chain needs custom public key → address derivation logic:

```go
case XxxChain:
    // 1. Decode the public key bytes
    // 2. Apply chain-specific address derivation
    // 3. Return the address
```

Patterns by key type:

- **Secp256k1 chains** (BTC, ETH, XRP, TRON): Use compressed public key, apply chain-specific hashing
- **Ed25519 chains** (SOL, SUI, ADA): Use Ed25519 public key bytes, apply chain-specific encoding (base58 for SOL, bech32 for ADA, hex for SUI)
- **EVM chains** (ETH, AVAX, BSC, BASE, POL): All use the same Ethereum-style address derivation (Keccak256 of uncompressed key)

---

## tokenlist/ — EVM Token Lists (for EVM chains only)

If the new chain is EVM-compatible and supports tokens:

### 1. Create token list file: `common/tokenlist/<chain>.go`

```go
func GetXxxTokenList() EVMTokenList {
    // Load from embedded JSON files
}
```

### 2. Create token data directory: `tokenlist/xxxTokens/`

- `xxx_mainnet_latest.json` — mainnet token list
- `xxx_mocknet_latest.json` — mocknet token list
- Corresponding `.go` files with `//go:embed` directives

### 3. Update GetEVMTokenList() in `common/tokenlist/token_list.go`

```go
case common.XxxChain:
    return GetXxxTokenList()
```

---

## Complete Checklist

Use this when adding a new chain. Check off each item:

### chain.go (required for all chains)

- [ ] Chain constant defined
- [ ] Added to AllChains (if production-ready)
- [ ] Added to GetEVMChains() or GetUTXOChains() (if applicable)
- [ ] GetSigningAlgo() case added
- [ ] GetGasAsset() case added
- [ ] GetGasUnits() case added
- [ ] GetGasAssetDecimal() case added
- [ ] AddressPrefix() cases added (if applicable)
- [ ] DustThreshold() case added
- [ ] P2WPKHOutputValue() case added (UTXO only)
- [ ] MaxMemoLength() case added (UTXO only)
- [ ] DefaultCoinbase() case added (UTXO only)
- [ ] ApproximateBlockMilliseconds() case added
- [ ] InboundNotes() case added
- [ ] OutboundNotes() case added (if needed)

### asset.go (required for all chains)

- [ ] Asset constant defined
- [ ] ShortCode() case added
- [ ] NewAssetWithShortCodesV3_1_0() entry added
- [ ] Native() case added (if whitelisted)
- [ ] IsWhitelisted() entry added (if base layer)

### address.go (required for all chains)

- [ ] IsValidXxxAddress() function created
- [ ] NewAddress() updated to call validator
- [ ] IsChain() case added
- [ ] GetChain() array updated
- [ ] GetNetwork() case added

### pubkey.go (required for all chains)

- [ ] GetAddress() case added with chain-specific key→address derivation

### tokenlist/ (EVM chains only)

- [ ] Token list file created
- [ ] Token data directory with JSON files created
- [ ] GetEVMTokenList() case added

### Tests

- [ ] chain_test.go updated
- [ ] asset_test.go updated
- [ ] address_test.go updated
- [ ] pubkey_test.go updated
