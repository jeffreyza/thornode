# TCYProtocolOwnedResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**Amount** | **string** | protocol-owned TCY in 1e8 base units (claim module tcy balance minus the sum of outstanding tcy_claimer entitlements) | 

## Methods

### NewTCYProtocolOwnedResponse

`func NewTCYProtocolOwnedResponse(amount string, ) *TCYProtocolOwnedResponse`

NewTCYProtocolOwnedResponse instantiates a new TCYProtocolOwnedResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewTCYProtocolOwnedResponseWithDefaults

`func NewTCYProtocolOwnedResponseWithDefaults() *TCYProtocolOwnedResponse`

NewTCYProtocolOwnedResponseWithDefaults instantiates a new TCYProtocolOwnedResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetAmount

`func (o *TCYProtocolOwnedResponse) GetAmount() string`

GetAmount returns the Amount field if non-nil, zero value otherwise.

### GetAmountOk

`func (o *TCYProtocolOwnedResponse) GetAmountOk() (*string, bool)`

GetAmountOk returns a tuple with the Amount field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetAmount

`func (o *TCYProtocolOwnedResponse) SetAmount(v string)`

SetAmount sets Amount field to given value.



[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


