# SwapQueuePaginatedResponse

## Properties

Name | Type | Description | Notes
------------ | ------------- | ------------- | -------------
**SwapQueue** | Pointer to [**[]MsgSwap**](MsgSwap.md) | Array of queued swaps | [optional] 
**Pagination** | Pointer to [**PaginationMeta**](PaginationMeta.md) |  | [optional] 

## Methods

### NewSwapQueuePaginatedResponse

`func NewSwapQueuePaginatedResponse() *SwapQueuePaginatedResponse`

NewSwapQueuePaginatedResponse instantiates a new SwapQueuePaginatedResponse object
This constructor will assign default values to properties that have it defined,
and makes sure properties required by API are set, but the set of arguments
will change when the set of required properties is changed

### NewSwapQueuePaginatedResponseWithDefaults

`func NewSwapQueuePaginatedResponseWithDefaults() *SwapQueuePaginatedResponse`

NewSwapQueuePaginatedResponseWithDefaults instantiates a new SwapQueuePaginatedResponse object
This constructor will only assign default values to properties that have it defined,
but it doesn't guarantee that properties required by API are set

### GetSwapQueue

`func (o *SwapQueuePaginatedResponse) GetSwapQueue() []MsgSwap`

GetSwapQueue returns the SwapQueue field if non-nil, zero value otherwise.

### GetSwapQueueOk

`func (o *SwapQueuePaginatedResponse) GetSwapQueueOk() (*[]MsgSwap, bool)`

GetSwapQueueOk returns a tuple with the SwapQueue field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetSwapQueue

`func (o *SwapQueuePaginatedResponse) SetSwapQueue(v []MsgSwap)`

SetSwapQueue sets SwapQueue field to given value.

### HasSwapQueue

`func (o *SwapQueuePaginatedResponse) HasSwapQueue() bool`

HasSwapQueue returns a boolean if a field has been set.

### GetPagination

`func (o *SwapQueuePaginatedResponse) GetPagination() PaginationMeta`

GetPagination returns the Pagination field if non-nil, zero value otherwise.

### GetPaginationOk

`func (o *SwapQueuePaginatedResponse) GetPaginationOk() (*PaginationMeta, bool)`

GetPaginationOk returns a tuple with the Pagination field if it's non-nil, zero value otherwise
and a boolean to check if the value has been set.

### SetPagination

`func (o *SwapQueuePaginatedResponse) SetPagination(v PaginationMeta)`

SetPagination sets Pagination field to given value.

### HasPagination

`func (o *SwapQueuePaginatedResponse) HasPagination() bool`

HasPagination returns a boolean if a field has been set.


[[Back to Model list]](../README.md#documentation-for-models) [[Back to API list]](../README.md#documentation-for-api-endpoints) [[Back to README]](../README.md)


