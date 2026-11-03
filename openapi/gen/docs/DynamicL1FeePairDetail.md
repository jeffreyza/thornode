# DynamicL1FeePairDetail

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Pair** | **string** |  | 
**DynamicBps** | **int32** |  | 
**LastActiveEpoch** | **int32** |  | 
**History** | [**[]DynamicL1FeeHistoryEntry**](DynamicL1FeeHistoryEntry.md) |  | 

## Methods

### NewDynamicL1FeePairDetail

`func NewDynamicL1FeePairDetail(pair string, dynamicBps int32, lastActiveEpoch int32, history []DynamicL1FeeHistoryEntry, ) *DynamicL1FeePairDetail`

NewDynamicL1FeePairDetail instantiates a new DynamicL1FeePairDetail object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeePairDetailWithDefaults

`func NewDynamicL1FeePairDetailWithDefaults() *DynamicL1FeePairDetail`

NewDynamicL1FeePairDetailWithDefaults instantiates a new DynamicL1FeePairDetail object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPair

`func (o *DynamicL1FeePairDetail) GetPair() string`

GetPair returns the Pair field if non-nil, zero value otherwise.

### GetPairOk

`func (o *DynamicL1FeePairDetail) GetPairOk() (*string, bool)`

GetPairOk returns a tuple with the Pair field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPair

`func (o *DynamicL1FeePairDetail) SetPair(v string)`

SetPair sets Pair field to given value.


### GetDynamicBps

`func (o *DynamicL1FeePairDetail) GetDynamicBps() int32`

GetDynamicBps returns the DynamicBps field if non-nil, zero value otherwise.

### GetDynamicBpsOk

`func (o *DynamicL1FeePairDetail) GetDynamicBpsOk() (*int32, bool)`

GetDynamicBpsOk returns a tuple with the DynamicBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDynamicBps

`func (o *DynamicL1FeePairDetail) SetDynamicBps(v int32)`

SetDynamicBps sets DynamicBps field to given value.


### GetLastActiveEpoch

`func (o *DynamicL1FeePairDetail) GetLastActiveEpoch() int32`

GetLastActiveEpoch returns the LastActiveEpoch field if non-nil, zero value otherwise.

### GetLastActiveEpochOk

`func (o *DynamicL1FeePairDetail) GetLastActiveEpochOk() (*int32, bool)`

GetLastActiveEpochOk returns a tuple with the LastActiveEpoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLastActiveEpoch

`func (o *DynamicL1FeePairDetail) SetLastActiveEpoch(v int32)`

SetLastActiveEpoch sets LastActiveEpoch field to given value.


### GetHistory

`func (o *DynamicL1FeePairDetail) GetHistory() []DynamicL1FeeHistoryEntry`

GetHistory returns the History field if non-nil, zero value otherwise.

### GetHistoryOk

`func (o *DynamicL1FeePairDetail) GetHistoryOk() (*[]DynamicL1FeeHistoryEntry, bool)`

GetHistoryOk returns a tuple with the History field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHistory

`func (o *DynamicL1FeePairDetail) SetHistory(v []DynamicL1FeeHistoryEntry)`

SetHistory sets History field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


