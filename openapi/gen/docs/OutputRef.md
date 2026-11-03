# OutputRef

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**TxHash** | Pointer to **string** | Source transaction hash for the spent XMR output. | [optional] 
**OutputIndex** | Pointer to **int64** | Output index in the source transaction. | [optional] 
**KeyImage** | Pointer to **string** | XMR key image proving the referenced output was spent. | [optional] 
**SpendTxHash** | Pointer to **string** | XMR outbound transaction hash that spends this output. | [optional] 

## Methods

### NewOutputRef

`func NewOutputRef() *OutputRef`

NewOutputRef instantiates a new OutputRef object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewOutputRefWithDefaults

`func NewOutputRefWithDefaults() *OutputRef`

NewOutputRefWithDefaults instantiates a new OutputRef object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetTxHash

`func (o *OutputRef) GetTxHash() string`

GetTxHash returns the TxHash field if non-nil, zero value otherwise.

### GetTxHashOk

`func (o *OutputRef) GetTxHashOk() (*string, bool)`

GetTxHashOk returns a tuple with the TxHash field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetTxHash

`func (o *OutputRef) SetTxHash(v string)`

SetTxHash sets TxHash field to given value.

### HasTxHash

`func (o *OutputRef) HasTxHash() bool`

HasTxHash returns a boolean if a field has been set.

### GetOutputIndex

`func (o *OutputRef) GetOutputIndex() int64`

GetOutputIndex returns the OutputIndex field if non-nil, zero value otherwise.

### GetOutputIndexOk

`func (o *OutputRef) GetOutputIndexOk() (*int64, bool)`

GetOutputIndexOk returns a tuple with the OutputIndex field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetOutputIndex

`func (o *OutputRef) SetOutputIndex(v int64)`

SetOutputIndex sets OutputIndex field to given value.

### HasOutputIndex

`func (o *OutputRef) HasOutputIndex() bool`

HasOutputIndex returns a boolean if a field has been set.

### GetKeyImage

`func (o *OutputRef) GetKeyImage() string`

GetKeyImage returns the KeyImage field if non-nil, zero value otherwise.

### GetKeyImageOk

`func (o *OutputRef) GetKeyImageOk() (*string, bool)`

GetKeyImageOk returns a tuple with the KeyImage field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetKeyImage

`func (o *OutputRef) SetKeyImage(v string)`

SetKeyImage sets KeyImage field to given value.

### HasKeyImage

`func (o *OutputRef) HasKeyImage() bool`

HasKeyImage returns a boolean if a field has been set.

### GetSpendTxHash

`func (o *OutputRef) GetSpendTxHash() string`

GetSpendTxHash returns the SpendTxHash field if non-nil, zero value otherwise.

### GetSpendTxHashOk

`func (o *OutputRef) GetSpendTxHashOk() (*string, bool)`

GetSpendTxHashOk returns a tuple with the SpendTxHash field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSpendTxHash

`func (o *OutputRef) SetSpendTxHash(v string)`

SetSpendTxHash sets SpendTxHash field to given value.

### HasSpendTxHash

`func (o *OutputRef) HasSpendTxHash() bool`

HasSpendTxHash returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


