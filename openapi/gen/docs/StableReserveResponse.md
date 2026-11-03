# StableReserveResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Enabled** | **bool** |  | 
**RuneBalance** | **string** |  | 
**DepegToleranceBps** | **string** |  | 
**PolShareBps** | **string** |  | 
**Inventory** | [**[]StableReserveInventory**](StableReserveInventory.md) |  | 
**DeploymentStreamQuantity** | **string** |  | 
**DeploymentStreamInterval** | **string** |  | 
**ActiveSwaps** | [**[]StableReserveStreamingSwap**](StableReserveStreamingSwap.md) |  | 

## Methods

### NewStableReserveResponse

`func NewStableReserveResponse(enabled bool, runeBalance string, depegToleranceBps string, polShareBps string, inventory []StableReserveInventory, deploymentStreamQuantity string, deploymentStreamInterval string, activeSwaps []StableReserveStreamingSwap, ) *StableReserveResponse`

NewStableReserveResponse instantiates a new StableReserveResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewStableReserveResponseWithDefaults

`func NewStableReserveResponseWithDefaults() *StableReserveResponse`

NewStableReserveResponseWithDefaults instantiates a new StableReserveResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetEnabled

`func (o *StableReserveResponse) GetEnabled() bool`

GetEnabled returns the Enabled field if non-nil, zero value otherwise.

### GetEnabledOk

`func (o *StableReserveResponse) GetEnabledOk() (*bool, bool)`

GetEnabledOk returns a tuple with the Enabled field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEnabled

`func (o *StableReserveResponse) SetEnabled(v bool)`

SetEnabled sets Enabled field to given value.


### GetRuneBalance

`func (o *StableReserveResponse) GetRuneBalance() string`

GetRuneBalance returns the RuneBalance field if non-nil, zero value otherwise.

### GetRuneBalanceOk

`func (o *StableReserveResponse) GetRuneBalanceOk() (*string, bool)`

GetRuneBalanceOk returns a tuple with the RuneBalance field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRuneBalance

`func (o *StableReserveResponse) SetRuneBalance(v string)`

SetRuneBalance sets RuneBalance field to given value.


### GetDepegToleranceBps

`func (o *StableReserveResponse) GetDepegToleranceBps() string`

GetDepegToleranceBps returns the DepegToleranceBps field if non-nil, zero value otherwise.

### GetDepegToleranceBpsOk

`func (o *StableReserveResponse) GetDepegToleranceBpsOk() (*string, bool)`

GetDepegToleranceBpsOk returns a tuple with the DepegToleranceBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDepegToleranceBps

`func (o *StableReserveResponse) SetDepegToleranceBps(v string)`

SetDepegToleranceBps sets DepegToleranceBps field to given value.


### GetPolShareBps

`func (o *StableReserveResponse) GetPolShareBps() string`

GetPolShareBps returns the PolShareBps field if non-nil, zero value otherwise.

### GetPolShareBpsOk

`func (o *StableReserveResponse) GetPolShareBpsOk() (*string, bool)`

GetPolShareBpsOk returns a tuple with the PolShareBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPolShareBps

`func (o *StableReserveResponse) SetPolShareBps(v string)`

SetPolShareBps sets PolShareBps field to given value.


### GetInventory

`func (o *StableReserveResponse) GetInventory() []StableReserveInventory`

GetInventory returns the Inventory field if non-nil, zero value otherwise.

### GetInventoryOk

`func (o *StableReserveResponse) GetInventoryOk() (*[]StableReserveInventory, bool)`

GetInventoryOk returns a tuple with the Inventory field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetInventory

`func (o *StableReserveResponse) SetInventory(v []StableReserveInventory)`

SetInventory sets Inventory field to given value.


### GetDeploymentStreamQuantity

`func (o *StableReserveResponse) GetDeploymentStreamQuantity() string`

GetDeploymentStreamQuantity returns the DeploymentStreamQuantity field if non-nil, zero value otherwise.

### GetDeploymentStreamQuantityOk

`func (o *StableReserveResponse) GetDeploymentStreamQuantityOk() (*string, bool)`

GetDeploymentStreamQuantityOk returns a tuple with the DeploymentStreamQuantity field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDeploymentStreamQuantity

`func (o *StableReserveResponse) SetDeploymentStreamQuantity(v string)`

SetDeploymentStreamQuantity sets DeploymentStreamQuantity field to given value.


### GetDeploymentStreamInterval

`func (o *StableReserveResponse) GetDeploymentStreamInterval() string`

GetDeploymentStreamInterval returns the DeploymentStreamInterval field if non-nil, zero value otherwise.

### GetDeploymentStreamIntervalOk

`func (o *StableReserveResponse) GetDeploymentStreamIntervalOk() (*string, bool)`

GetDeploymentStreamIntervalOk returns a tuple with the DeploymentStreamInterval field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDeploymentStreamInterval

`func (o *StableReserveResponse) SetDeploymentStreamInterval(v string)`

SetDeploymentStreamInterval sets DeploymentStreamInterval field to given value.


### GetActiveSwaps

`func (o *StableReserveResponse) GetActiveSwaps() []StableReserveStreamingSwap`

GetActiveSwaps returns the ActiveSwaps field if non-nil, zero value otherwise.

### GetActiveSwapsOk

`func (o *StableReserveResponse) GetActiveSwapsOk() (*[]StableReserveStreamingSwap, bool)`

GetActiveSwapsOk returns a tuple with the ActiveSwaps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetActiveSwaps

`func (o *StableReserveResponse) SetActiveSwaps(v []StableReserveStreamingSwap)`

SetActiveSwaps sets ActiveSwaps field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


