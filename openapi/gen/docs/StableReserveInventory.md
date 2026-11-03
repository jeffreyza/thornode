# StableReserveInventory

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Asset** | **string** |  | 
**Amount** | **string** |  | 
**Healthy** | **bool** |  | 
**Reason** | **string** |  | 
**OraclePrice** | **string** |  | 

## Methods

### NewStableReserveInventory

`func NewStableReserveInventory(asset string, amount string, healthy bool, reason string, oraclePrice string, ) *StableReserveInventory`

NewStableReserveInventory instantiates a new StableReserveInventory object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewStableReserveInventoryWithDefaults

`func NewStableReserveInventoryWithDefaults() *StableReserveInventory`

NewStableReserveInventoryWithDefaults instantiates a new StableReserveInventory object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAsset

`func (o *StableReserveInventory) GetAsset() string`

GetAsset returns the Asset field if non-nil, zero value otherwise.

### GetAssetOk

`func (o *StableReserveInventory) GetAssetOk() (*string, bool)`

GetAssetOk returns a tuple with the Asset field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAsset

`func (o *StableReserveInventory) SetAsset(v string)`

SetAsset sets Asset field to given value.


### GetAmount

`func (o *StableReserveInventory) GetAmount() string`

GetAmount returns the Amount field if non-nil, zero value otherwise.

### GetAmountOk

`func (o *StableReserveInventory) GetAmountOk() (*string, bool)`

GetAmountOk returns a tuple with the Amount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAmount

`func (o *StableReserveInventory) SetAmount(v string)`

SetAmount sets Amount field to given value.


### GetHealthy

`func (o *StableReserveInventory) GetHealthy() bool`

GetHealthy returns the Healthy field if non-nil, zero value otherwise.

### GetHealthyOk

`func (o *StableReserveInventory) GetHealthyOk() (*bool, bool)`

GetHealthyOk returns a tuple with the Healthy field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHealthy

`func (o *StableReserveInventory) SetHealthy(v bool)`

SetHealthy sets Healthy field to given value.


### GetReason

`func (o *StableReserveInventory) GetReason() string`

GetReason returns the Reason field if non-nil, zero value otherwise.

### GetReasonOk

`func (o *StableReserveInventory) GetReasonOk() (*string, bool)`

GetReasonOk returns a tuple with the Reason field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetReason

`func (o *StableReserveInventory) SetReason(v string)`

SetReason sets Reason field to given value.


### GetOraclePrice

`func (o *StableReserveInventory) GetOraclePrice() string`

GetOraclePrice returns the OraclePrice field if non-nil, zero value otherwise.

### GetOraclePriceOk

`func (o *StableReserveInventory) GetOraclePriceOk() (*string, bool)`

GetOraclePriceOk returns a tuple with the OraclePrice field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOraclePrice

`func (o *StableReserveInventory) SetOraclePrice(v string)`

SetOraclePrice sets OraclePrice field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


