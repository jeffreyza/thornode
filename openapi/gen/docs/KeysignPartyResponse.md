# KeysignPartyResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**PubKey** | **string** |  | 
**Signers** | **[]string** |  | 
**Height** | **int64** |  | 
**Threshold** | **int64** |  | 
**RequiredSigners** | **int64** |  | 
**NeverSigners** | **[]string** |  | 
**JailedFallbackSigners** | **[]string** |  | 
**ActiveSignerCount** | **int64** |  | 

## Methods

### NewKeysignPartyResponse

`func NewKeysignPartyResponse(pubKey string, signers []string, height int64, threshold int64, requiredSigners int64, neverSigners []string, jailedFallbackSigners []string, activeSignerCount int64, ) *KeysignPartyResponse`

NewKeysignPartyResponse instantiates a new KeysignPartyResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewKeysignPartyResponseWithDefaults

`func NewKeysignPartyResponseWithDefaults() *KeysignPartyResponse`

NewKeysignPartyResponseWithDefaults instantiates a new KeysignPartyResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetPubKey

`func (o *KeysignPartyResponse) GetPubKey() string`

GetPubKey returns the PubKey field if non-nil, zero value otherwise.

### GetPubKeyOk

`func (o *KeysignPartyResponse) GetPubKeyOk() (*string, bool)`

GetPubKeyOk returns a tuple with the PubKey field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPubKey

`func (o *KeysignPartyResponse) SetPubKey(v string)`

SetPubKey sets PubKey field to given value.


### GetSigners

`func (o *KeysignPartyResponse) GetSigners() []string`

GetSigners returns the Signers field if non-nil, zero value otherwise.

### GetSignersOk

`func (o *KeysignPartyResponse) GetSignersOk() (*[]string, bool)`

GetSignersOk returns a tuple with the Signers field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSigners

`func (o *KeysignPartyResponse) SetSigners(v []string)`

SetSigners sets Signers field to given value.


### GetHeight

`func (o *KeysignPartyResponse) GetHeight() int64`

GetHeight returns the Height field if non-nil, zero value otherwise.

### GetHeightOk

`func (o *KeysignPartyResponse) GetHeightOk() (*int64, bool)`

GetHeightOk returns a tuple with the Height field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetHeight

`func (o *KeysignPartyResponse) SetHeight(v int64)`

SetHeight sets Height field to given value.


### GetThreshold

`func (o *KeysignPartyResponse) GetThreshold() int64`

GetThreshold returns the Threshold field if non-nil, zero value otherwise.

### GetThresholdOk

`func (o *KeysignPartyResponse) GetThresholdOk() (*int64, bool)`

GetThresholdOk returns a tuple with the Threshold field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetThreshold

`func (o *KeysignPartyResponse) SetThreshold(v int64)`

SetThreshold sets Threshold field to given value.


### GetRequiredSigners

`func (o *KeysignPartyResponse) GetRequiredSigners() int64`

GetRequiredSigners returns the RequiredSigners field if non-nil, zero value otherwise.

### GetRequiredSignersOk

`func (o *KeysignPartyResponse) GetRequiredSignersOk() (*int64, bool)`

GetRequiredSignersOk returns a tuple with the RequiredSigners field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetRequiredSigners

`func (o *KeysignPartyResponse) SetRequiredSigners(v int64)`

SetRequiredSigners sets RequiredSigners field to given value.


### GetNeverSigners

`func (o *KeysignPartyResponse) GetNeverSigners() []string`

GetNeverSigners returns the NeverSigners field if non-nil, zero value otherwise.

### GetNeverSignersOk

`func (o *KeysignPartyResponse) GetNeverSignersOk() (*[]string, bool)`

GetNeverSignersOk returns a tuple with the NeverSigners field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetNeverSigners

`func (o *KeysignPartyResponse) SetNeverSigners(v []string)`

SetNeverSigners sets NeverSigners field to given value.


### GetJailedFallbackSigners

`func (o *KeysignPartyResponse) GetJailedFallbackSigners() []string`

GetJailedFallbackSigners returns the JailedFallbackSigners field if non-nil, zero value otherwise.

### GetJailedFallbackSignersOk

`func (o *KeysignPartyResponse) GetJailedFallbackSignersOk() (*[]string, bool)`

GetJailedFallbackSignersOk returns a tuple with the JailedFallbackSigners field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetJailedFallbackSigners

`func (o *KeysignPartyResponse) SetJailedFallbackSigners(v []string)`

SetJailedFallbackSigners sets JailedFallbackSigners field to given value.


### GetActiveSignerCount

`func (o *KeysignPartyResponse) GetActiveSignerCount() int64`

GetActiveSignerCount returns the ActiveSignerCount field if non-nil, zero value otherwise.

### GetActiveSignerCountOk

`func (o *KeysignPartyResponse) GetActiveSignerCountOk() (*int64, bool)`

GetActiveSignerCountOk returns a tuple with the ActiveSignerCount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetActiveSignerCount

`func (o *KeysignPartyResponse) SetActiveSignerCount(v int64)`

SetActiveSignerCount sets ActiveSignerCount field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


