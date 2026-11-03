# DynamicL1FeesByThornameResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Thorname** | **string** |  | 
**WhitelistState** | **int64** | see DynamicL1FeeEntry.whitelist_state | 
**Pairs** | [**[]DynamicL1FeePairDetail**](DynamicL1FeePairDetail.md) |  | 

## Methods

### NewDynamicL1FeesByThornameResponse

`func NewDynamicL1FeesByThornameResponse(thorname string, whitelistState int64, pairs []DynamicL1FeePairDetail, ) *DynamicL1FeesByThornameResponse`

NewDynamicL1FeesByThornameResponse instantiates a new DynamicL1FeesByThornameResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeesByThornameResponseWithDefaults

`func NewDynamicL1FeesByThornameResponseWithDefaults() *DynamicL1FeesByThornameResponse`

NewDynamicL1FeesByThornameResponseWithDefaults instantiates a new DynamicL1FeesByThornameResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetThorname

`func (o *DynamicL1FeesByThornameResponse) GetThorname() string`

GetThorname returns the Thorname field if non-nil, zero value otherwise.

### GetThornameOk

`func (o *DynamicL1FeesByThornameResponse) GetThornameOk() (*string, bool)`

GetThornameOk returns a tuple with the Thorname field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetThorname

`func (o *DynamicL1FeesByThornameResponse) SetThorname(v string)`

SetThorname sets Thorname field to given value.


### GetWhitelistState

`func (o *DynamicL1FeesByThornameResponse) GetWhitelistState() int64`

GetWhitelistState returns the WhitelistState field if non-nil, zero value otherwise.

### GetWhitelistStateOk

`func (o *DynamicL1FeesByThornameResponse) GetWhitelistStateOk() (*int64, bool)`

GetWhitelistStateOk returns a tuple with the WhitelistState field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWhitelistState

`func (o *DynamicL1FeesByThornameResponse) SetWhitelistState(v int64)`

SetWhitelistState sets WhitelistState field to given value.


### GetPairs

`func (o *DynamicL1FeesByThornameResponse) GetPairs() []DynamicL1FeePairDetail`

GetPairs returns the Pairs field if non-nil, zero value otherwise.

### GetPairsOk

`func (o *DynamicL1FeesByThornameResponse) GetPairsOk() (*[]DynamicL1FeePairDetail, bool)`

GetPairsOk returns a tuple with the Pairs field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPairs

`func (o *DynamicL1FeesByThornameResponse) SetPairs(v []DynamicL1FeePairDetail)`

SetPairs sets Pairs field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


