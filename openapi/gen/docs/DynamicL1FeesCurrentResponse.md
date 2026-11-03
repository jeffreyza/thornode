# DynamicL1FeesCurrentResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Epoch** | **int32** | current epoch number derived from block height | 
**Entries** | [**[]DynamicL1FeeCurrentEntry**](DynamicL1FeeCurrentEntry.md) |  | 

## Methods

### NewDynamicL1FeesCurrentResponse

`func NewDynamicL1FeesCurrentResponse(epoch int32, entries []DynamicL1FeeCurrentEntry, ) *DynamicL1FeesCurrentResponse`

NewDynamicL1FeesCurrentResponse instantiates a new DynamicL1FeesCurrentResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeesCurrentResponseWithDefaults

`func NewDynamicL1FeesCurrentResponseWithDefaults() *DynamicL1FeesCurrentResponse`

NewDynamicL1FeesCurrentResponseWithDefaults instantiates a new DynamicL1FeesCurrentResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetEpoch

`func (o *DynamicL1FeesCurrentResponse) GetEpoch() int32`

GetEpoch returns the Epoch field if non-nil, zero value otherwise.

### GetEpochOk

`func (o *DynamicL1FeesCurrentResponse) GetEpochOk() (*int32, bool)`

GetEpochOk returns a tuple with the Epoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEpoch

`func (o *DynamicL1FeesCurrentResponse) SetEpoch(v int32)`

SetEpoch sets Epoch field to given value.


### GetEntries

`func (o *DynamicL1FeesCurrentResponse) GetEntries() []DynamicL1FeeCurrentEntry`

GetEntries returns the Entries field if non-nil, zero value otherwise.

### GetEntriesOk

`func (o *DynamicL1FeesCurrentResponse) GetEntriesOk() (*[]DynamicL1FeeCurrentEntry, bool)`

GetEntriesOk returns a tuple with the Entries field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetEntries

`func (o *DynamicL1FeesCurrentResponse) SetEntries(v []DynamicL1FeeCurrentEntry)`

SetEntries sets Entries field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


