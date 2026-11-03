# DynamicL1FeeEntry

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Thorname** | **string** | the registered thorname this record is attributed to | 
**Pair** | **string** | normalized asset pair encoded as \&quot;assetA|assetB\&quot; | 
**DynamicBps** | **int32** | current dynamic outbound fee floor in basis points | 
**WhitelistState** | **int64** | DYNAMICFEE-WHITELIST-{thorname} Mimir value. &#x60;0&#x60;/absent &#x3D; not enrolled, &#x60;1&#x60; &#x3D; active (applied at swap time), &#x60;2&#x60; &#x3D; monitor (computed but not applied).  | 
**LastActiveEpoch** | **int32** | last epoch index in which this pair accrued activity | 
**LatestFeesTor** | Pointer to **string** | fees in TOR (1e8-precision USD value) recorded in the most recent sealed history entry, if any  | [optional] 

## Methods

### NewDynamicL1FeeEntry

`func NewDynamicL1FeeEntry(thorname string, pair string, dynamicBps int32, whitelistState int64, lastActiveEpoch int32, ) *DynamicL1FeeEntry`

NewDynamicL1FeeEntry instantiates a new DynamicL1FeeEntry object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewDynamicL1FeeEntryWithDefaults

`func NewDynamicL1FeeEntryWithDefaults() *DynamicL1FeeEntry`

NewDynamicL1FeeEntryWithDefaults instantiates a new DynamicL1FeeEntry object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetThorname

`func (o *DynamicL1FeeEntry) GetThorname() string`

GetThorname returns the Thorname field if non-nil, zero value otherwise.

### GetThornameOk

`func (o *DynamicL1FeeEntry) GetThornameOk() (*string, bool)`

GetThornameOk returns a tuple with the Thorname field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetThorname

`func (o *DynamicL1FeeEntry) SetThorname(v string)`

SetThorname sets Thorname field to given value.


### GetPair

`func (o *DynamicL1FeeEntry) GetPair() string`

GetPair returns the Pair field if non-nil, zero value otherwise.

### GetPairOk

`func (o *DynamicL1FeeEntry) GetPairOk() (*string, bool)`

GetPairOk returns a tuple with the Pair field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPair

`func (o *DynamicL1FeeEntry) SetPair(v string)`

SetPair sets Pair field to given value.


### GetDynamicBps

`func (o *DynamicL1FeeEntry) GetDynamicBps() int32`

GetDynamicBps returns the DynamicBps field if non-nil, zero value otherwise.

### GetDynamicBpsOk

`func (o *DynamicL1FeeEntry) GetDynamicBpsOk() (*int32, bool)`

GetDynamicBpsOk returns a tuple with the DynamicBps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetDynamicBps

`func (o *DynamicL1FeeEntry) SetDynamicBps(v int32)`

SetDynamicBps sets DynamicBps field to given value.


### GetWhitelistState

`func (o *DynamicL1FeeEntry) GetWhitelistState() int64`

GetWhitelistState returns the WhitelistState field if non-nil, zero value otherwise.

### GetWhitelistStateOk

`func (o *DynamicL1FeeEntry) GetWhitelistStateOk() (*int64, bool)`

GetWhitelistStateOk returns a tuple with the WhitelistState field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetWhitelistState

`func (o *DynamicL1FeeEntry) SetWhitelistState(v int64)`

SetWhitelistState sets WhitelistState field to given value.


### GetLastActiveEpoch

`func (o *DynamicL1FeeEntry) GetLastActiveEpoch() int32`

GetLastActiveEpoch returns the LastActiveEpoch field if non-nil, zero value otherwise.

### GetLastActiveEpochOk

`func (o *DynamicL1FeeEntry) GetLastActiveEpochOk() (*int32, bool)`

GetLastActiveEpochOk returns a tuple with the LastActiveEpoch field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLastActiveEpoch

`func (o *DynamicL1FeeEntry) SetLastActiveEpoch(v int32)`

SetLastActiveEpoch sets LastActiveEpoch field to given value.


### GetLatestFeesTor

`func (o *DynamicL1FeeEntry) GetLatestFeesTor() string`

GetLatestFeesTor returns the LatestFeesTor field if non-nil, zero value otherwise.

### GetLatestFeesTorOk

`func (o *DynamicL1FeeEntry) GetLatestFeesTorOk() (*string, bool)`

GetLatestFeesTorOk returns a tuple with the LatestFeesTor field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetLatestFeesTor

`func (o *DynamicL1FeeEntry) SetLatestFeesTor(v string)`

SetLatestFeesTor sets LatestFeesTor field to given value.

### HasLatestFeesTor

`func (o *DynamicL1FeeEntry) HasLatestFeesTor() bool`

HasLatestFeesTor returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


