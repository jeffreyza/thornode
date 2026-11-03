# Pool

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Asset** | **string** |  | 
**ShortCode** | Pointer to **string** |  | [optional] 
**Status** | **string** |  | 
**Decimals** | Pointer to **int64** |  | [optional] 
**PendingInboundAsset** | **string** |  | 
**PendingInboundRune** | **string** |  | 
**BalanceAsset** | **string** |  | 
**BalanceRune** | **string** |  | 
**AssetTorPrice** | **string** | the USD (TOR) price of the asset in 1e8 | 
**PoolUnits** | **string** | the total pool units, this is the sum of LP and synth units | 
**LPUnits** | **string** | the total pool liquidity provider units | 
**SynthUnits** | **string** | the total synth units in the pool | 
**SynthSupply** | **string** | the total supply of synths for the asset | 
**SaversDepth** | **string** | the balance of L1 asset deposited into the Savers Vault | 
**SaversUnits** | **string** | the number of units owned by Savers | 
**SaversFillBps** | **string** | the filled savers capacity in basis points, 4500/10000 &#x3D; 45% | 
**SaversCapacityRemaining** | **string** | amount of remaining capacity in asset | 
**SynthMintPaused** | **bool** | whether additional synths cannot be minted | 
**SynthSupplyRemaining** | **string** | the amount of synth supply remaining before the current max supply is reached | 
**DerivedDepthBps** | **string** | the depth of the derived virtual pool relative to L1 pool (in basis points) | 
**TradingHalted** | Pointer to **bool** | indicates if the pool can be used for swaps | [optional] 
**VolumeAsset** | Pointer to **string** | 24h volume in asset | [optional] 
**VolumeRune** | Pointer to **string** | 24h volume in rune | [optional] 
**PolReserveRuneDeposited** | Pointer to **string** | cumulative RUNE deposited by POL Reserve into this pool | [optional] 
**RollingPoolLiquidityFeeRune** | Pointer to **string** | rolling liquidity fees accumulated (in RUNE) since the last pool cycle reset; used as the numerator of the POL Reserve deployment score | [optional] 
**Luvi** | **string** | Liquidity Unit Value Index as the chain values a pool unit, sqrt(balance_asset * balance_rune) / pool_units (synth-unit clamp included) | 
**LpEquity** | **string** | LP equity net of the synth liability in sqrt(asset * rune) units, sqrt(balance_asset * balance_rune) * (2 * balance_asset - synth_supply) / (2 * balance_asset); zero or negative when synth_overhang is true | 
**LpEquityPerUnit** | **string** | lp_equity / LP_units; equals luvi (up to rounding) unless synth_overhang is true | 
**SynthPerPoolDepthBps** | **string** | synth supply relative to twice the asset depth, in basis points | 
**SynthOverhang** | **bool** | whether synth supply is at or above twice the asset depth; synth units are clamped and LP units can redeem almost nothing while this is true | 
**LuviG** | Pointer to **string** | LUVI accumulator, the accumulated change in hedged LP equity G in sqrt(asset * rune) units since tracking began; absent until the pool has been observed at the end of a block | [optional] 
**LuviC** | Pointer to **string** | accumulated synth repricing term C of LUVI* | [optional] 
**LuviStar** | Pointer to **string** | synth-aware index LUVI* &#x3D; lp_equity_per_unit + luvi_c as of luvi_height; only meaningful when synth_overhang is false | [optional] 
**LuviLastNormalizedDeltaG** | Pointer to **string** | the last accumulator step&#39;s change in G divided by the previous sqrt(asset * rune) | [optional] 
**LuviHeight** | Pointer to **int64** | height of the last LUVI accumulator step | [optional] 
**LpUnitFairValueRune** | Pointer to **string** | fair LP-side value per LP unit in RUNE at the oracle price P of the asset in RUNE, (2 * sqrt(balance_asset * balance_rune * P) - synth_supply * P) / LP_units, clamped at zero; a layer-1 swap cannot lower it (fees raise it), while synth mints and burns move it unless P equals the pool price. Only present when the oracle has a current price for the asset and RUNE | [optional] 
**LpUnitFairValueAsset** | Pointer to **string** | lp_unit_fair_value_rune / P, in the asset. Only present with an oracle price | [optional] 
**LpUnitSpotValueRune** | Pointer to **string** | spot LP-side value per LP unit in RUNE, (2 * balance_rune - synth_supply * balance_rune / balance_asset) / LP_units, clamped at zero. Only present with an oracle price | [optional] 
**LpFairSpotDeviationBps** | Pointer to **string** | (lp_unit_spot_value_rune - lp_unit_fair_value_rune) / lp_unit_fair_value_rune in basis points, a pool price manipulation signal. Absent without an oracle price or when lp_fair_value_overhang is true | [optional] 
**FairValueOraclePriceRune** | Pointer to **string** | the oracle price P of one unit of the asset in RUNE (oracle USD price of the asset / oracle USD price of RUNE) used for the fair values. Only present with an oracle price | [optional] 
**LpFairValueOverhang** | Pointer to **bool** | whether the synth liability is at or above the pool&#39;s fair value, so the LP side is worth nothing at the oracle price. Only present (true) with an oracle price | [optional] 

## Methods

### NewPool

`func NewPool(asset string, status string, pendingInboundAsset string, pendingInboundRune string, balanceAsset string, balanceRune string, assetTorPrice string, poolUnits string, lPUnits string, synthUnits string, synthSupply string, saversDepth string, saversUnits string, saversFillBps string, saversCapacityRemaining string, synthMintPaused bool, synthSupplyRemaining string, derivedDepthBps string, luvi string, lpEquity string, lpEquityPerUnit string, synthPerPoolDepthBps string, synthOverhang bool, ) *Pool`

NewPool instantiates a new Pool object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewPoolWithDefaults

`func NewPoolWithDefaults() *Pool`

NewPoolWithDefaults instantiates a new Pool object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAsset

`func (o *Pool) GetAsset() string`

GetAsset returns the Asset field if non-nil, zero value otherwise.

### GetAssetOk

`func (o *Pool) GetAssetOk() (*string, bool)`

GetAssetOk returns a tuple with the Asset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAsset

`func (o *Pool) SetAsset(v string)`

SetAsset sets Asset field to given value.


### GetShortCode

`func (o *Pool) GetShortCode() string`

GetShortCode returns the ShortCode field if non-nil, zero value otherwise.

### GetShortCodeOk

`func (o *Pool) GetShortCodeOk() (*string, bool)`

GetShortCodeOk returns a tuple with the ShortCode field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetShortCode

`func (o *Pool) SetShortCode(v string)`

SetShortCode sets ShortCode field to given value.

### HasShortCode

`func (o *Pool) HasShortCode() bool`

HasShortCode returns a boolean if a field has been set.

### GetStatus

`func (o *Pool) GetStatus() string`

GetStatus returns the Status field if non-nil, zero value otherwise.

### GetStatusOk

`func (o *Pool) GetStatusOk() (*string, bool)`

GetStatusOk returns a tuple with the Status field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetStatus

`func (o *Pool) SetStatus(v string)`

SetStatus sets Status field to given value.


### GetDecimals

`func (o *Pool) GetDecimals() int64`

GetDecimals returns the Decimals field if non-nil, zero value otherwise.

### GetDecimalsOk

`func (o *Pool) GetDecimalsOk() (*int64, bool)`

GetDecimalsOk returns a tuple with the Decimals field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDecimals

`func (o *Pool) SetDecimals(v int64)`

SetDecimals sets Decimals field to given value.

### HasDecimals

`func (o *Pool) HasDecimals() bool`

HasDecimals returns a boolean if a field has been set.

### GetPendingInboundAsset

`func (o *Pool) GetPendingInboundAsset() string`

GetPendingInboundAsset returns the PendingInboundAsset field if non-nil, zero value otherwise.

### GetPendingInboundAssetOk

`func (o *Pool) GetPendingInboundAssetOk() (*string, bool)`

GetPendingInboundAssetOk returns a tuple with the PendingInboundAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingInboundAsset

`func (o *Pool) SetPendingInboundAsset(v string)`

SetPendingInboundAsset sets PendingInboundAsset field to given value.


### GetPendingInboundRune

`func (o *Pool) GetPendingInboundRune() string`

GetPendingInboundRune returns the PendingInboundRune field if non-nil, zero value otherwise.

### GetPendingInboundRuneOk

`func (o *Pool) GetPendingInboundRuneOk() (*string, bool)`

GetPendingInboundRuneOk returns a tuple with the PendingInboundRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPendingInboundRune

`func (o *Pool) SetPendingInboundRune(v string)`

SetPendingInboundRune sets PendingInboundRune field to given value.


### GetBalanceAsset

`func (o *Pool) GetBalanceAsset() string`

GetBalanceAsset returns the BalanceAsset field if non-nil, zero value otherwise.

### GetBalanceAssetOk

`func (o *Pool) GetBalanceAssetOk() (*string, bool)`

GetBalanceAssetOk returns a tuple with the BalanceAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBalanceAsset

`func (o *Pool) SetBalanceAsset(v string)`

SetBalanceAsset sets BalanceAsset field to given value.


### GetBalanceRune

`func (o *Pool) GetBalanceRune() string`

GetBalanceRune returns the BalanceRune field if non-nil, zero value otherwise.

### GetBalanceRuneOk

`func (o *Pool) GetBalanceRuneOk() (*string, bool)`

GetBalanceRuneOk returns a tuple with the BalanceRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBalanceRune

`func (o *Pool) SetBalanceRune(v string)`

SetBalanceRune sets BalanceRune field to given value.


### GetAssetTorPrice

`func (o *Pool) GetAssetTorPrice() string`

GetAssetTorPrice returns the AssetTorPrice field if non-nil, zero value otherwise.

### GetAssetTorPriceOk

`func (o *Pool) GetAssetTorPriceOk() (*string, bool)`

GetAssetTorPriceOk returns a tuple with the AssetTorPrice field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAssetTorPrice

`func (o *Pool) SetAssetTorPrice(v string)`

SetAssetTorPrice sets AssetTorPrice field to given value.


### GetPoolUnits

`func (o *Pool) GetPoolUnits() string`

GetPoolUnits returns the PoolUnits field if non-nil, zero value otherwise.

### GetPoolUnitsOk

`func (o *Pool) GetPoolUnitsOk() (*string, bool)`

GetPoolUnitsOk returns a tuple with the PoolUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPoolUnits

`func (o *Pool) SetPoolUnits(v string)`

SetPoolUnits sets PoolUnits field to given value.


### GetLPUnits

`func (o *Pool) GetLPUnits() string`

GetLPUnits returns the LPUnits field if non-nil, zero value otherwise.

### GetLPUnitsOk

`func (o *Pool) GetLPUnitsOk() (*string, bool)`

GetLPUnitsOk returns a tuple with the LPUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLPUnits

`func (o *Pool) SetLPUnits(v string)`

SetLPUnits sets LPUnits field to given value.


### GetSynthUnits

`func (o *Pool) GetSynthUnits() string`

GetSynthUnits returns the SynthUnits field if non-nil, zero value otherwise.

### GetSynthUnitsOk

`func (o *Pool) GetSynthUnitsOk() (*string, bool)`

GetSynthUnitsOk returns a tuple with the SynthUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthUnits

`func (o *Pool) SetSynthUnits(v string)`

SetSynthUnits sets SynthUnits field to given value.


### GetSynthSupply

`func (o *Pool) GetSynthSupply() string`

GetSynthSupply returns the SynthSupply field if non-nil, zero value otherwise.

### GetSynthSupplyOk

`func (o *Pool) GetSynthSupplyOk() (*string, bool)`

GetSynthSupplyOk returns a tuple with the SynthSupply field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthSupply

`func (o *Pool) SetSynthSupply(v string)`

SetSynthSupply sets SynthSupply field to given value.


### GetSaversDepth

`func (o *Pool) GetSaversDepth() string`

GetSaversDepth returns the SaversDepth field if non-nil, zero value otherwise.

### GetSaversDepthOk

`func (o *Pool) GetSaversDepthOk() (*string, bool)`

GetSaversDepthOk returns a tuple with the SaversDepth field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSaversDepth

`func (o *Pool) SetSaversDepth(v string)`

SetSaversDepth sets SaversDepth field to given value.


### GetSaversUnits

`func (o *Pool) GetSaversUnits() string`

GetSaversUnits returns the SaversUnits field if non-nil, zero value otherwise.

### GetSaversUnitsOk

`func (o *Pool) GetSaversUnitsOk() (*string, bool)`

GetSaversUnitsOk returns a tuple with the SaversUnits field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSaversUnits

`func (o *Pool) SetSaversUnits(v string)`

SetSaversUnits sets SaversUnits field to given value.


### GetSaversFillBps

`func (o *Pool) GetSaversFillBps() string`

GetSaversFillBps returns the SaversFillBps field if non-nil, zero value otherwise.

### GetSaversFillBpsOk

`func (o *Pool) GetSaversFillBpsOk() (*string, bool)`

GetSaversFillBpsOk returns a tuple with the SaversFillBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSaversFillBps

`func (o *Pool) SetSaversFillBps(v string)`

SetSaversFillBps sets SaversFillBps field to given value.


### GetSaversCapacityRemaining

`func (o *Pool) GetSaversCapacityRemaining() string`

GetSaversCapacityRemaining returns the SaversCapacityRemaining field if non-nil, zero value otherwise.

### GetSaversCapacityRemainingOk

`func (o *Pool) GetSaversCapacityRemainingOk() (*string, bool)`

GetSaversCapacityRemainingOk returns a tuple with the SaversCapacityRemaining field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSaversCapacityRemaining

`func (o *Pool) SetSaversCapacityRemaining(v string)`

SetSaversCapacityRemaining sets SaversCapacityRemaining field to given value.


### GetSynthMintPaused

`func (o *Pool) GetSynthMintPaused() bool`

GetSynthMintPaused returns the SynthMintPaused field if non-nil, zero value otherwise.

### GetSynthMintPausedOk

`func (o *Pool) GetSynthMintPausedOk() (*bool, bool)`

GetSynthMintPausedOk returns a tuple with the SynthMintPaused field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthMintPaused

`func (o *Pool) SetSynthMintPaused(v bool)`

SetSynthMintPaused sets SynthMintPaused field to given value.


### GetSynthSupplyRemaining

`func (o *Pool) GetSynthSupplyRemaining() string`

GetSynthSupplyRemaining returns the SynthSupplyRemaining field if non-nil, zero value otherwise.

### GetSynthSupplyRemainingOk

`func (o *Pool) GetSynthSupplyRemainingOk() (*string, bool)`

GetSynthSupplyRemainingOk returns a tuple with the SynthSupplyRemaining field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthSupplyRemaining

`func (o *Pool) SetSynthSupplyRemaining(v string)`

SetSynthSupplyRemaining sets SynthSupplyRemaining field to given value.


### GetDerivedDepthBps

`func (o *Pool) GetDerivedDepthBps() string`

GetDerivedDepthBps returns the DerivedDepthBps field if non-nil, zero value otherwise.

### GetDerivedDepthBpsOk

`func (o *Pool) GetDerivedDepthBpsOk() (*string, bool)`

GetDerivedDepthBpsOk returns a tuple with the DerivedDepthBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDerivedDepthBps

`func (o *Pool) SetDerivedDepthBps(v string)`

SetDerivedDepthBps sets DerivedDepthBps field to given value.


### GetTradingHalted

`func (o *Pool) GetTradingHalted() bool`

GetTradingHalted returns the TradingHalted field if non-nil, zero value otherwise.

### GetTradingHaltedOk

`func (o *Pool) GetTradingHaltedOk() (*bool, bool)`

GetTradingHaltedOk returns a tuple with the TradingHalted field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTradingHalted

`func (o *Pool) SetTradingHalted(v bool)`

SetTradingHalted sets TradingHalted field to given value.

### HasTradingHalted

`func (o *Pool) HasTradingHalted() bool`

HasTradingHalted returns a boolean if a field has been set.

### GetVolumeAsset

`func (o *Pool) GetVolumeAsset() string`

GetVolumeAsset returns the VolumeAsset field if non-nil, zero value otherwise.

### GetVolumeAssetOk

`func (o *Pool) GetVolumeAssetOk() (*string, bool)`

GetVolumeAssetOk returns a tuple with the VolumeAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVolumeAsset

`func (o *Pool) SetVolumeAsset(v string)`

SetVolumeAsset sets VolumeAsset field to given value.

### HasVolumeAsset

`func (o *Pool) HasVolumeAsset() bool`

HasVolumeAsset returns a boolean if a field has been set.

### GetVolumeRune

`func (o *Pool) GetVolumeRune() string`

GetVolumeRune returns the VolumeRune field if non-nil, zero value otherwise.

### GetVolumeRuneOk

`func (o *Pool) GetVolumeRuneOk() (*string, bool)`

GetVolumeRuneOk returns a tuple with the VolumeRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVolumeRune

`func (o *Pool) SetVolumeRune(v string)`

SetVolumeRune sets VolumeRune field to given value.

### HasVolumeRune

`func (o *Pool) HasVolumeRune() bool`

HasVolumeRune returns a boolean if a field has been set.

### GetPolReserveRuneDeposited

`func (o *Pool) GetPolReserveRuneDeposited() string`

GetPolReserveRuneDeposited returns the PolReserveRuneDeposited field if non-nil, zero value otherwise.

### GetPolReserveRuneDepositedOk

`func (o *Pool) GetPolReserveRuneDepositedOk() (*string, bool)`

GetPolReserveRuneDepositedOk returns a tuple with the PolReserveRuneDeposited field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPolReserveRuneDeposited

`func (o *Pool) SetPolReserveRuneDeposited(v string)`

SetPolReserveRuneDeposited sets PolReserveRuneDeposited field to given value.

### HasPolReserveRuneDeposited

`func (o *Pool) HasPolReserveRuneDeposited() bool`

HasPolReserveRuneDeposited returns a boolean if a field has been set.

### GetRollingPoolLiquidityFeeRune

`func (o *Pool) GetRollingPoolLiquidityFeeRune() string`

GetRollingPoolLiquidityFeeRune returns the RollingPoolLiquidityFeeRune field if non-nil, zero value otherwise.

### GetRollingPoolLiquidityFeeRuneOk

`func (o *Pool) GetRollingPoolLiquidityFeeRuneOk() (*string, bool)`

GetRollingPoolLiquidityFeeRuneOk returns a tuple with the RollingPoolLiquidityFeeRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRollingPoolLiquidityFeeRune

`func (o *Pool) SetRollingPoolLiquidityFeeRune(v string)`

SetRollingPoolLiquidityFeeRune sets RollingPoolLiquidityFeeRune field to given value.

### HasRollingPoolLiquidityFeeRune

`func (o *Pool) HasRollingPoolLiquidityFeeRune() bool`

HasRollingPoolLiquidityFeeRune returns a boolean if a field has been set.

### GetLuvi

`func (o *Pool) GetLuvi() string`

GetLuvi returns the Luvi field if non-nil, zero value otherwise.

### GetLuviOk

`func (o *Pool) GetLuviOk() (*string, bool)`

GetLuviOk returns a tuple with the Luvi field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLuvi

`func (o *Pool) SetLuvi(v string)`

SetLuvi sets Luvi field to given value.


### GetLpEquity

`func (o *Pool) GetLpEquity() string`

GetLpEquity returns the LpEquity field if non-nil, zero value otherwise.

### GetLpEquityOk

`func (o *Pool) GetLpEquityOk() (*string, bool)`

GetLpEquityOk returns a tuple with the LpEquity field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpEquity

`func (o *Pool) SetLpEquity(v string)`

SetLpEquity sets LpEquity field to given value.


### GetLpEquityPerUnit

`func (o *Pool) GetLpEquityPerUnit() string`

GetLpEquityPerUnit returns the LpEquityPerUnit field if non-nil, zero value otherwise.

### GetLpEquityPerUnitOk

`func (o *Pool) GetLpEquityPerUnitOk() (*string, bool)`

GetLpEquityPerUnitOk returns a tuple with the LpEquityPerUnit field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpEquityPerUnit

`func (o *Pool) SetLpEquityPerUnit(v string)`

SetLpEquityPerUnit sets LpEquityPerUnit field to given value.


### GetSynthPerPoolDepthBps

`func (o *Pool) GetSynthPerPoolDepthBps() string`

GetSynthPerPoolDepthBps returns the SynthPerPoolDepthBps field if non-nil, zero value otherwise.

### GetSynthPerPoolDepthBpsOk

`func (o *Pool) GetSynthPerPoolDepthBpsOk() (*string, bool)`

GetSynthPerPoolDepthBpsOk returns a tuple with the SynthPerPoolDepthBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthPerPoolDepthBps

`func (o *Pool) SetSynthPerPoolDepthBps(v string)`

SetSynthPerPoolDepthBps sets SynthPerPoolDepthBps field to given value.


### GetSynthOverhang

`func (o *Pool) GetSynthOverhang() bool`

GetSynthOverhang returns the SynthOverhang field if non-nil, zero value otherwise.

### GetSynthOverhangOk

`func (o *Pool) GetSynthOverhangOk() (*bool, bool)`

GetSynthOverhangOk returns a tuple with the SynthOverhang field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSynthOverhang

`func (o *Pool) SetSynthOverhang(v bool)`

SetSynthOverhang sets SynthOverhang field to given value.


### GetLuviG

`func (o *Pool) GetLuviG() string`

GetLuviG returns the LuviG field if non-nil, zero value otherwise.

### GetLuviGOk

`func (o *Pool) GetLuviGOk() (*string, bool)`

GetLuviGOk returns a tuple with the LuviG field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLuviG

`func (o *Pool) SetLuviG(v string)`

SetLuviG sets LuviG field to given value.

### HasLuviG

`func (o *Pool) HasLuviG() bool`

HasLuviG returns a boolean if a field has been set.

### GetLuviC

`func (o *Pool) GetLuviC() string`

GetLuviC returns the LuviC field if non-nil, zero value otherwise.

### GetLuviCOk

`func (o *Pool) GetLuviCOk() (*string, bool)`

GetLuviCOk returns a tuple with the LuviC field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLuviC

`func (o *Pool) SetLuviC(v string)`

SetLuviC sets LuviC field to given value.

### HasLuviC

`func (o *Pool) HasLuviC() bool`

HasLuviC returns a boolean if a field has been set.

### GetLuviStar

`func (o *Pool) GetLuviStar() string`

GetLuviStar returns the LuviStar field if non-nil, zero value otherwise.

### GetLuviStarOk

`func (o *Pool) GetLuviStarOk() (*string, bool)`

GetLuviStarOk returns a tuple with the LuviStar field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLuviStar

`func (o *Pool) SetLuviStar(v string)`

SetLuviStar sets LuviStar field to given value.

### HasLuviStar

`func (o *Pool) HasLuviStar() bool`

HasLuviStar returns a boolean if a field has been set.

### GetLuviLastNormalizedDeltaG

`func (o *Pool) GetLuviLastNormalizedDeltaG() string`

GetLuviLastNormalizedDeltaG returns the LuviLastNormalizedDeltaG field if non-nil, zero value otherwise.

### GetLuviLastNormalizedDeltaGOk

`func (o *Pool) GetLuviLastNormalizedDeltaGOk() (*string, bool)`

GetLuviLastNormalizedDeltaGOk returns a tuple with the LuviLastNormalizedDeltaG field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLuviLastNormalizedDeltaG

`func (o *Pool) SetLuviLastNormalizedDeltaG(v string)`

SetLuviLastNormalizedDeltaG sets LuviLastNormalizedDeltaG field to given value.

### HasLuviLastNormalizedDeltaG

`func (o *Pool) HasLuviLastNormalizedDeltaG() bool`

HasLuviLastNormalizedDeltaG returns a boolean if a field has been set.

### GetLuviHeight

`func (o *Pool) GetLuviHeight() int64`

GetLuviHeight returns the LuviHeight field if non-nil, zero value otherwise.

### GetLuviHeightOk

`func (o *Pool) GetLuviHeightOk() (*int64, bool)`

GetLuviHeightOk returns a tuple with the LuviHeight field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLuviHeight

`func (o *Pool) SetLuviHeight(v int64)`

SetLuviHeight sets LuviHeight field to given value.

### HasLuviHeight

`func (o *Pool) HasLuviHeight() bool`

HasLuviHeight returns a boolean if a field has been set.

### GetLpUnitFairValueRune

`func (o *Pool) GetLpUnitFairValueRune() string`

GetLpUnitFairValueRune returns the LpUnitFairValueRune field if non-nil, zero value otherwise.

### GetLpUnitFairValueRuneOk

`func (o *Pool) GetLpUnitFairValueRuneOk() (*string, bool)`

GetLpUnitFairValueRuneOk returns a tuple with the LpUnitFairValueRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpUnitFairValueRune

`func (o *Pool) SetLpUnitFairValueRune(v string)`

SetLpUnitFairValueRune sets LpUnitFairValueRune field to given value.

### HasLpUnitFairValueRune

`func (o *Pool) HasLpUnitFairValueRune() bool`

HasLpUnitFairValueRune returns a boolean if a field has been set.

### GetLpUnitFairValueAsset

`func (o *Pool) GetLpUnitFairValueAsset() string`

GetLpUnitFairValueAsset returns the LpUnitFairValueAsset field if non-nil, zero value otherwise.

### GetLpUnitFairValueAssetOk

`func (o *Pool) GetLpUnitFairValueAssetOk() (*string, bool)`

GetLpUnitFairValueAssetOk returns a tuple with the LpUnitFairValueAsset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpUnitFairValueAsset

`func (o *Pool) SetLpUnitFairValueAsset(v string)`

SetLpUnitFairValueAsset sets LpUnitFairValueAsset field to given value.

### HasLpUnitFairValueAsset

`func (o *Pool) HasLpUnitFairValueAsset() bool`

HasLpUnitFairValueAsset returns a boolean if a field has been set.

### GetLpUnitSpotValueRune

`func (o *Pool) GetLpUnitSpotValueRune() string`

GetLpUnitSpotValueRune returns the LpUnitSpotValueRune field if non-nil, zero value otherwise.

### GetLpUnitSpotValueRuneOk

`func (o *Pool) GetLpUnitSpotValueRuneOk() (*string, bool)`

GetLpUnitSpotValueRuneOk returns a tuple with the LpUnitSpotValueRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpUnitSpotValueRune

`func (o *Pool) SetLpUnitSpotValueRune(v string)`

SetLpUnitSpotValueRune sets LpUnitSpotValueRune field to given value.

### HasLpUnitSpotValueRune

`func (o *Pool) HasLpUnitSpotValueRune() bool`

HasLpUnitSpotValueRune returns a boolean if a field has been set.

### GetLpFairSpotDeviationBps

`func (o *Pool) GetLpFairSpotDeviationBps() string`

GetLpFairSpotDeviationBps returns the LpFairSpotDeviationBps field if non-nil, zero value otherwise.

### GetLpFairSpotDeviationBpsOk

`func (o *Pool) GetLpFairSpotDeviationBpsOk() (*string, bool)`

GetLpFairSpotDeviationBpsOk returns a tuple with the LpFairSpotDeviationBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpFairSpotDeviationBps

`func (o *Pool) SetLpFairSpotDeviationBps(v string)`

SetLpFairSpotDeviationBps sets LpFairSpotDeviationBps field to given value.

### HasLpFairSpotDeviationBps

`func (o *Pool) HasLpFairSpotDeviationBps() bool`

HasLpFairSpotDeviationBps returns a boolean if a field has been set.

### GetFairValueOraclePriceRune

`func (o *Pool) GetFairValueOraclePriceRune() string`

GetFairValueOraclePriceRune returns the FairValueOraclePriceRune field if non-nil, zero value otherwise.

### GetFairValueOraclePriceRuneOk

`func (o *Pool) GetFairValueOraclePriceRuneOk() (*string, bool)`

GetFairValueOraclePriceRuneOk returns a tuple with the FairValueOraclePriceRune field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFairValueOraclePriceRune

`func (o *Pool) SetFairValueOraclePriceRune(v string)`

SetFairValueOraclePriceRune sets FairValueOraclePriceRune field to given value.

### HasFairValueOraclePriceRune

`func (o *Pool) HasFairValueOraclePriceRune() bool`

HasFairValueOraclePriceRune returns a boolean if a field has been set.

### GetLpFairValueOverhang

`func (o *Pool) GetLpFairValueOverhang() bool`

GetLpFairValueOverhang returns the LpFairValueOverhang field if non-nil, zero value otherwise.

### GetLpFairValueOverhangOk

`func (o *Pool) GetLpFairValueOverhangOk() (*bool, bool)`

GetLpFairValueOverhangOk returns a tuple with the LpFairValueOverhang field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLpFairValueOverhang

`func (o *Pool) SetLpFairValueOverhang(v bool)`

SetLpFairValueOverhang sets LpFairValueOverhang field to given value.

### HasLpFairValueOverhang

`func (o *Pool) HasLpFairValueOverhang() bool`

HasLpFairValueOverhang returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


