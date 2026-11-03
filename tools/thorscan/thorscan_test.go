package thorscan

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransportRoundTrip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "thorscan-go", r.Header.Get("X-Client-ID"))
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	transport := &Transport{Transport: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestSetAPIEndpoint(t *testing.T) {
	old := APIEndpoint
	defer func() { APIEndpoint = old }()

	SetAPIEndpoint("http://localhost:1234")
	require.Equal(t, "http://localhost:1234", APIEndpoint)
}

func TestGetBlock(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.URL.Path, "/thorchain/block")

		resp := BlockResponse{}
		resp.Header.Height = 100
		resp.Header.Time = "2024-01-01T00:00:00Z"
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	old := APIEndpoint
	oldClient := httpClient
	defer func() {
		APIEndpoint = old
		httpClient = oldClient
	}()
	APIEndpoint = ts.URL
	httpClient = ts.Client()

	block, err := getBlock(100)
	require.NoError(t, err)
	require.Equal(t, int64(100), block.Header.Height)
}

func TestGetBlockGzip(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		resp := BlockResponse{}
		resp.Header.Height = 200
		resp.Header.Time = "2024-01-01T00:00:00Z"
		_ = json.NewEncoder(gz).Encode(resp)
		gz.Close()
	}))
	defer ts.Close()

	old := APIEndpoint
	oldClient := httpClient
	defer func() {
		APIEndpoint = old
		httpClient = oldClient
	}()
	APIEndpoint = ts.URL
	httpClient = ts.Client()

	block, err := getBlock(200)
	require.NoError(t, err)
	require.Equal(t, int64(200), block.Header.Height)
}

func TestGetBlockNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	old := APIEndpoint
	oldClient := httpClient
	defer func() {
		APIEndpoint = old
		httpClient = oldClient
	}()
	APIEndpoint = ts.URL
	httpClient = ts.Client()

	_, err := getBlock(999)
	require.Error(t, err)
	require.Contains(t, err.Error(), "block not found")
}

func TestGetBlockInternalError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer ts.Close()

	old := APIEndpoint
	oldClient := httpClient
	defer func() {
		APIEndpoint = old
		httpClient = oldClient
	}()
	APIEndpoint = ts.URL
	httpClient = ts.Client()

	_, err := getBlock(999)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bad status code: 500")
}

func TestGetBlockFutureHeight(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("cannot query with height in the future"))
	}))
	defer ts.Close()

	old := APIEndpoint
	oldClient := httpClient
	defer func() {
		APIEndpoint = old
		httpClient = oldClient
	}()
	APIEndpoint = ts.URL
	httpClient = ts.Client()

	_, err := getBlock(999999)
	require.Error(t, err)
	require.Contains(t, err.Error(), "block not found")
}

func TestBlockTxUnmarshalJSON(t *testing.T) {
	// Test with nil tx
	data := `{"hash":"abc123"}`
	var bt BlockTx
	err := bt.UnmarshalJSON([]byte(data))
	require.NoError(t, err)
}

func TestBlockTxUnmarshalJSONBadJSON(t *testing.T) {
	var bt BlockTx
	err := bt.UnmarshalJSON([]byte("not json"))
	require.Error(t, err)
}
