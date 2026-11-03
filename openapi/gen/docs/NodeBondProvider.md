# NodeBondProvider

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**BondAddress** | Pointer to **string** |  | [optional] 
**Bond** | Pointer to **string** |  | [optional] 
**Bps** | Pointer to **int64** | the share of provider rewards to pay out automatically - &#x60;0&#x60; means no reward split | [optional] 
**PayoutTarget** | Pointer to **string** | either empty, a THORChain address, or a THORName | [optional] 

## Methods

### NewNodeBondProvider

`func NewNodeBondProvider() *NodeBondProvider`

NewNodeBondProvider instantiates a new NodeBondProvider object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewNodeBondProviderWithDefaults

`func NewNodeBondProviderWithDefaults() *NodeBondProvider`

NewNodeBondProviderWithDefaults instantiates a new NodeBondProvider object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetBondAddress

`func (o *NodeBondProvider) GetBondAddress() string`

GetBondAddress returns the BondAddress field if non-nil, zero value otherwise.

### GetBondAddressOk

`func (o *NodeBondProvider) GetBondAddressOk() (*string, bool)`

GetBondAddressOk returns a tuple with the BondAddress field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBondAddress

`func (o *NodeBondProvider) SetBondAddress(v string)`

SetBondAddress sets BondAddress field to given value.

### HasBondAddress

`func (o *NodeBondProvider) HasBondAddress() bool`

HasBondAddress returns a boolean if a field has been set.

### GetBond

`func (o *NodeBondProvider) GetBond() string`

GetBond returns the Bond field if non-nil, zero value otherwise.

### GetBondOk

`func (o *NodeBondProvider) GetBondOk() (*string, bool)`

GetBondOk returns a tuple with the Bond field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBond

`func (o *NodeBondProvider) SetBond(v string)`

SetBond sets Bond field to given value.

### HasBond

`func (o *NodeBondProvider) HasBond() bool`

HasBond returns a boolean if a field has been set.

### GetBps

`func (o *NodeBondProvider) GetBps() int64`

GetBps returns the Bps field if non-nil, zero value otherwise.

### GetBpsOk

`func (o *NodeBondProvider) GetBpsOk() (*int64, bool)`

GetBpsOk returns a tuple with the Bps field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetBps

`func (o *NodeBondProvider) SetBps(v int64)`

SetBps sets Bps field to given value.

### HasBps

`func (o *NodeBondProvider) HasBps() bool`

HasBps returns a boolean if a field has been set.

### GetPayoutTarget

`func (o *NodeBondProvider) GetPayoutTarget() string`

GetPayoutTarget returns the PayoutTarget field if non-nil, zero value otherwise.

### GetPayoutTargetOk

`func (o *NodeBondProvider) GetPayoutTargetOk() (*string, bool)`

GetPayoutTargetOk returns a tuple with the PayoutTarget field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPayoutTarget

`func (o *NodeBondProvider) SetPayoutTarget(v string)`

SetPayoutTarget sets PayoutTarget field to given value.

### HasPayoutTarget

`func (o *NodeBondProvider) HasPayoutTarget() bool`

HasPayoutTarget returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


