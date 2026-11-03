# DynamicL1FeeHistoryEntry

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Epoch** | **int32** |  | 
**VolumeTor** | **string** | TOR-denominated (1e8-precision USD value) swap volume in this epoch  | 
**FeesTor** | **string** | TOR-denominated (1e8-precision USD value) fees accrued in this epoch  | 
**BpsAtClose** | **int32** | dynamic_bps value at the close of the epoch | 

## Methods

### NewDynamicL1FeeHistoryEntry

`func NewDynamicL1FeeHistoryEntry(epoch int32, volumeTor string, feesTor string, bpsAtClose int32, ) *DynamicL1FeeHistoryEntry`

NewDynamicL1FeeHistoryEntry instantiates a new DynamicL1FeeHistoryEntry object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeeHistoryEntryWithDefaults

`func NewDynamicL1FeeHistoryEntryWithDefaults() *DynamicL1FeeHistoryEntry`

NewDynamicL1FeeHistoryEntryWithDefaults instantiates a new DynamicL1FeeHistoryEntry object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetEpoch

`func (o *DynamicL1FeeHistoryEntry) GetEpoch() int32`

GetEpoch returns the Epoch field if non-nil, zero value otherwise.

### GetEpochOk

`func (o *DynamicL1FeeHistoryEntry) GetEpochOk() (*int32, bool)`

GetEpochOk returns a tuple with the Epoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEpoch

`func (o *DynamicL1FeeHistoryEntry) SetEpoch(v int32)`

SetEpoch sets Epoch field to given value.


### GetVolumeTor

`func (o *DynamicL1FeeHistoryEntry) GetVolumeTor() string`

GetVolumeTor returns the VolumeTor field if non-nil, zero value otherwise.

### GetVolumeTorOk

`func (o *DynamicL1FeeHistoryEntry) GetVolumeTorOk() (*string, bool)`

GetVolumeTorOk returns a tuple with the VolumeTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetVolumeTor

`func (o *DynamicL1FeeHistoryEntry) SetVolumeTor(v string)`

SetVolumeTor sets VolumeTor field to given value.


### GetFeesTor

`func (o *DynamicL1FeeHistoryEntry) GetFeesTor() string`

GetFeesTor returns the FeesTor field if non-nil, zero value otherwise.

### GetFeesTorOk

`func (o *DynamicL1FeeHistoryEntry) GetFeesTorOk() (*string, bool)`

GetFeesTorOk returns a tuple with the FeesTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetFeesTor

`func (o *DynamicL1FeeHistoryEntry) SetFeesTor(v string)`

SetFeesTor sets FeesTor field to given value.


### GetBpsAtClose

`func (o *DynamicL1FeeHistoryEntry) GetBpsAtClose() int32`

GetBpsAtClose returns the BpsAtClose field if non-nil, zero value otherwise.

### GetBpsAtCloseOk

`func (o *DynamicL1FeeHistoryEntry) GetBpsAtCloseOk() (*int32, bool)`

GetBpsAtCloseOk returns a tuple with the BpsAtClose field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBpsAtClose

`func (o *DynamicL1FeeHistoryEntry) SetBpsAtClose(v int32)`

SetBpsAtClose sets BpsAtClose field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


