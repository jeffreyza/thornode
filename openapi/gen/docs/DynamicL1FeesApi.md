# \DynamicL1FeesApi

All URIs are relative to *http://localhost*

Method | HTTP request | Description
------------- | ------------- | -------------
[**DynamicL1Fees**](DynamicL1FeesApi.md#DynamicL1Fees) | **Get** /thorchain/dynamic_l1_fees | 
[**DynamicL1FeesByThorname**](DynamicL1FeesApi.md#DynamicL1FeesByThorname) | **Get** /thorchain/dynamic_l1_fees/{thorname} | 
[**DynamicL1FeesCurrent**](DynamicL1FeesApi.md#DynamicL1FeesCurrent) | **Get** /thorchain/dynamic_l1_fees_current | 



## DynamicL1Fees

> DynamicL1FeesResponse DynamicL1Fees(ctx).Height(height).Execute()





### Example

```go
package main

import (
    "context"
    "fmt"
    "os"
    openapiclient "./openapi"
)

func main() {
    height := int64(789) // int64 | optional block height, defaults to current tip (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.DynamicL1FeesApi.DynamicL1Fees(context.Background()).Height(height).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `DynamicL1FeesApi.DynamicL1Fees``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `DynamicL1Fees`: DynamicL1FeesResponse
    fmt.Fprintf(os.Stdout, "Response from `DynamicL1FeesApi.DynamicL1Fees`: %v\n", resp)
}
```

### Path Parameters



### Other Parameters

Other parameters are passed through a pointer to a apiDynamicL1FeesRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **height** | **int64** | optional block height, defaults to current tip | 

### Return type

[**DynamicL1FeesResponse**](DynamicL1FeesResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)


## DynamicL1FeesByThorname

> DynamicL1FeesByThornameResponse DynamicL1FeesByThorname(ctx, thorname).Height(height).Execute()





### Example

```go
package main

import (
    "context"
    "fmt"
    "os"
    openapiclient "./openapi"
)

func main() {
    thorname := "thorname_example" // string | the thorname to look up dynamic fee state for
    height := int64(789) // int64 | optional block height, defaults to current tip (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.DynamicL1FeesApi.DynamicL1FeesByThorname(context.Background(), thorname).Height(height).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `DynamicL1FeesApi.DynamicL1FeesByThorname``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `DynamicL1FeesByThorname`: DynamicL1FeesByThornameResponse
    fmt.Fprintf(os.Stdout, "Response from `DynamicL1FeesApi.DynamicL1FeesByThorname`: %v\n", resp)
}
```

### Path Parameters


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
**ctx** | **context.Context** | context for authentication, logging, cancellation, deadlines, tracing, etc.
**thorname** | **string** | the thorname to look up dynamic fee state for | 

### Other Parameters

Other parameters are passed through a pointer to a apiDynamicL1FeesByThornameRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------

 **height** | **int64** | optional block height, defaults to current tip | 

### Return type

[**DynamicL1FeesByThornameResponse**](DynamicL1FeesByThornameResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)


## DynamicL1FeesCurrent

> DynamicL1FeesCurrentResponse DynamicL1FeesCurrent(ctx).Height(height).Execute()





### Example

```go
package main

import (
    "context"
    "fmt"
    "os"
    openapiclient "./openapi"
)

func main() {
    height := int64(789) // int64 | optional block height, defaults to current tip (optional)

    configuration := openapiclient.NewConfiguration()
    apiClient := openapiclient.NewAPIClient(configuration)
    resp, r, err := apiClient.DynamicL1FeesApi.DynamicL1FeesCurrent(context.Background()).Height(height).Execute()
    if err != nil {
        fmt.Fprintf(os.Stderr, "Error when calling `DynamicL1FeesApi.DynamicL1FeesCurrent``: %v\n", err)
        fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
    }
    // response from `DynamicL1FeesCurrent`: DynamicL1FeesCurrentResponse
    fmt.Fprintf(os.Stdout, "Response from `DynamicL1FeesApi.DynamicL1FeesCurrent`: %v\n", resp)
}
```

### Path Parameters



### Other Parameters

Other parameters are passed through a pointer to a apiDynamicL1FeesCurrentRequest struct via the builder pattern


Name | Type | Description  | Notes
------------- | ------------- | ------------- | -------------
 **height** | **int64** | optional block height, defaults to current tip | 

### Return type

[**DynamicL1FeesCurrentResponse**](DynamicL1FeesCurrentResponse.md)

### Authorization

No authorization required

### HTTP request headers

- **Content-Type**: Not defined
- **Accept**: application/json

[[Back to top]](#) [[Back to API list]](../README.md#documentation-for-api-endpoints)
[[Back to Model list]](../README.md#documentation-for-models)
[[Back to README]](../README.md)

