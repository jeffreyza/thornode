# Mimir Keys and Constants

User-facing catalogue of mimirs, with their meaning: `docs/mimir.md`. Don't
duplicate it here. Update it whenever you add a key.

## Two kinds of key

| Kind                     | Defined in                                                              | Read with                                                                                         |
| ------------------------ | ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| Constant-backed          | `ConstantName` enum in constants/constant_values.go                     | `k.GetConfigInt64(ctx, constants.X)` (mimir, else default)                                        |
| Free-standing / template | constants/mimir_strings.go (`MimirKey*`, `MimirTemplate*`, `MimirRef*`) | `k.GetMimir(ctx, constants.MimirKeyX)` or `k.GetMimirWithRef(ctx, constants.MimirTemplateX, ref)` |

For a constant-backed key, the mimir key is the stringer name of the constant
(`constants.X.String()`, e.g. `MultipleAffiliatesMaxCount`). Keys are upper-cased
before storage, so on the chain and in the API they appear as
`MULTIPLEAFFILIATESMAXCOUNT`.

## Constant-backed key: files

1. `constants/constant_values.go`: add the name to the `ConstantName` iota block.
   Names after the comment "implicitly-0 Constants undisplayed in the API
   endpoint" have no default and read 0 unless a mimir is set.
2. `constants/constants_v1.go`: add the default to `NewConstantValue()`
   `int64values` (or the bool/string maps), with a trailing comment saying what it
   does.
3. `constants/constantname_string.go` is generated. Run `make go-generate`,
   which installs stringer@v0.28.0 and runs `go generate ./...`. Don't hand-edit
   it.
4. Optional per-network override: the `int64Overrides` map in `init()` of
   constants_mocknet.go, constants_stagenet.go or constants_chainnet.go. Each
   file is selected by build tag.

`GetConstantValues(_ semver.Version)` ignores its version. There is no
constants_v2/v3 and no per-version constant set.

## Template keys

Templates take a chain, asset or ref via `fmt.Sprintf`. Examples (all in
mimir_strings.go):

| Template                                                                                           | Example key                 |
| -------------------------------------------------------------------------------------------------- | --------------------------- |
| `MimirTemplateHaltTrading` `Halt%sTrading`                                                         | `HaltETHTrading`            |
| `MimirTemplateHaltSigning` `HaltSigning%s`                                                         | `HaltSigningBTC`            |
| `MimirTemplateMaxGas` `MaxGas-%s`                                                                  | `MaxGas-ETH`                |
| `MimirTemplateRevShareThorName` `REVSHARE-%s`                                                      | `REVSHARE-foobar`           |
| `MimirTemplatePauseLPDeposit` `PauseLPDeposit-%s`                                                  | `PauseLPDeposit-BTC-BTC`    |
| `MimirTemplateSwapSlipBasisPointsMin` with `MimirRefL1` / `MimirRefSynth` / `MimirRefTradeAccount` | `SwapSlipBasisPointsMin-L1` |

Template keys must still satisfy `MimirKeyRegex` (`^[a-zA-Z0-9-]+$`) and
`MaxMimirLength` (128, constants/constants.go:94). Assets contain `.`, so asset
templates use `Asset.MimirString()` (`CHAIN-SYMBOL`, common/asset.go:468).
Some comments in mimir_strings.go still say "64 char limit"; the enforced
limit is 128.

## Mocknet env fallback

In a mocknet build, an unset mimir is read from the environment. For a key `Foo-Bar`
the variable is `CamelToSnakeUpper("Foo-Bar")` with `-` replaced by `_`
(keeper_mimir_mocknet.go). Separately, constants_mocknet.go `init()` reads env
overrides for constants. Both explain "it works on mocknet but not stagenet".

## Keys that do not exist

Older docs and skills mention `EnableSavings`, `EnableStreamingSwaps`,
`PoolDepthForYggFund` and `MAXSYNTHPERPOOLPEDPTH`. None of them is in non-test Go
code on develop. The real spelling is `MaxSynthPerPoolDepth`. Grep before you
cite a key:

```
grep -rn "<Key>" constants/ x/thorchain --include='*.go' | grep -v _test
```
