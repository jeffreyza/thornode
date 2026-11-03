package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testInfoID = "e1cd4454d71d8973e89155bc2c2a91aa56fd470c8ce64547ce8c69c789c21f0c"
)

// newCountingServer serves gettransactioninfobyblocknum from batchInfos (or
// 404 when batchStatus is non-200) and gettransactioninfobyid from byIDInfos
// (404 for unknown ids), counting the calls to each endpoint.
func newCountingServer(
	t *testing.T,
	batchStatus int,
	batchInfos []TransactionInfo,
	byIDInfos map[string]TransactionInfo,
	batchCalls, byIDCalls *int32,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyblocknum":
			atomic.AddInt32(batchCalls, 1)
			if batchStatus != http.StatusOK {
				w.WriteHeader(batchStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(batchInfos)
		case "/wallet/gettransactioninfobyid":
			atomic.AddInt32(byIDCalls, 1)
			id, _ := params["value"].(string)
			info, ok := byIDInfos[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(info)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// The batch endpoint result must parse and index by transaction id.
func TestGetTransactionInfoByBlockNum(t *testing.T) {
	srv := NewMockServer()
	defer srv.Close()
	tronApi := NewTronApi(srv.URL, time.Second)

	infos, err := tronApi.GetTransactionInfoByBlockNum(69351239)
	if err != nil {
		t.Fatalf("expected batch infos, got error: %s", err)
	}
	if len(infos) != 1 {
		t.Fatalf("expected 1 info, got %d", len(infos))
	}
	if infos[0].ID != testInfoID {
		t.Fatalf("unexpected id: %s", infos[0].ID)
	}
	if infos[0].Fee != 2100000 {
		t.Fatalf("unexpected fee: %d", infos[0].Fee)
	}

	// unknown height errors
	if _, err := tronApi.GetTransactionInfoByBlockNum(1); err == nil {
		t.Fatal("expected an error for an unknown height")
	}
}

// One batch call resolves every transaction present in the response; the
// resolver is lazy, so constructing it costs nothing.
func TestTxInfoResolverBatchHit(t *testing.T) {
	var batchCalls, byIDCalls int32
	batch := []TransactionInfo{
		{ID: "aa", Fee: 1, BlockNumber: 123},
		{ID: "bb", Fee: 2, BlockNumber: 123},
	}
	srv := newCountingServer(t, http.StatusOK, batch, nil, &batchCalls, &byIDCalls)
	defer srv.Close()

	resolver := NewTronApi(srv.URL, time.Second).NewTxInfoResolver(123, true)
	if got := atomic.LoadInt32(&batchCalls); got != 0 {
		t.Fatalf("resolver must be lazy; batch called %d times before first Get", got)
	}

	for _, id := range []string{"aa", "bb", "aa"} {
		info, err := resolver.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %s", id, err)
		}
		if info.ID != id {
			t.Fatalf("Get(%s) returned info for %s", id, info.ID)
		}
	}

	if got := atomic.LoadInt32(&batchCalls); got != 1 {
		t.Fatalf("expected exactly 1 batch call, got %d", got)
	}
	if got := atomic.LoadInt32(&byIDCalls); got != 0 {
		t.Fatalf("expected no per-transaction calls, got %d", got)
	}
}

// A transaction missing from the batch response falls back to the
// per-transaction endpoint.
func TestTxInfoResolverMissFallsBack(t *testing.T) {
	var batchCalls, byIDCalls int32
	batch := []TransactionInfo{{ID: "aa", Fee: 1, BlockNumber: 123}}
	byID := map[string]TransactionInfo{"cc": {ID: "cc", Fee: 3}}
	srv := newCountingServer(t, http.StatusOK, batch, byID, &batchCalls, &byIDCalls)
	defer srv.Close()

	resolver := NewTronApi(srv.URL, time.Second).NewTxInfoResolver(123, true)

	info, err := resolver.Get("cc")
	if err != nil {
		t.Fatalf("expected fallback to resolve, got: %s", err)
	}
	if info.Fee != 3 {
		t.Fatalf("unexpected fee: %d", info.Fee)
	}

	// a transaction known to neither endpoint surfaces the error
	if _, err := resolver.Get("dd"); err == nil {
		t.Fatal("expected an error for a transaction unknown to both endpoints")
	}

	if got := atomic.LoadInt32(&batchCalls); got != 1 {
		t.Fatalf("expected exactly 1 batch call, got %d", got)
	}
	if got := atomic.LoadInt32(&byIDCalls); got != 2 {
		t.Fatalf("expected 2 per-transaction calls, got %d", got)
	}
}

// An unavailable batch endpoint degrades to the previous per-transaction
// behavior, and the batch is attempted only once per block.
func TestTxInfoResolverBatchUnavailableFallsBack(t *testing.T) {
	var batchCalls, byIDCalls int32
	byID := map[string]TransactionInfo{
		"aa": {ID: "aa", Fee: 1},
		"bb": {ID: "bb", Fee: 2},
	}
	srv := newCountingServer(t, http.StatusNotFound, nil, byID, &batchCalls, &byIDCalls)
	defer srv.Close()

	resolver := NewTronApi(srv.URL, time.Second).NewTxInfoResolver(123, true)

	for _, id := range []string{"aa", "bb"} {
		info, err := resolver.Get(id)
		if err != nil {
			t.Fatalf("Get(%s): %s", id, err)
		}
		if info.ID != id {
			t.Fatalf("Get(%s) returned info for %s", id, info.ID)
		}
	}

	if got := atomic.LoadInt32(&batchCalls); got != 1 {
		t.Fatalf("expected the batch to be attempted exactly once, got %d", got)
	}
	if got := atomic.LoadInt32(&byIDCalls); got != 2 {
		t.Fatalf("expected 2 per-transaction calls, got %d", got)
	}
}

// With batching disabled, the batch endpoint is never called.
func TestTxInfoResolverBatchDisabled(t *testing.T) {
	var batchCalls, byIDCalls int32
	batch := []TransactionInfo{{ID: "aa", Fee: 1, BlockNumber: 123}}
	byID := map[string]TransactionInfo{
		"aa": {ID: "aa", Fee: 1},
		"bb": {ID: "bb", Fee: 2},
	}
	srv := newCountingServer(t, http.StatusOK, batch, byID, &batchCalls, &byIDCalls)
	defer srv.Close()

	resolver := NewTronApi(srv.URL, time.Second).NewTxInfoResolver(123, false)

	for _, id := range []string{"aa", "bb"} {
		if _, err := resolver.Get(id); err != nil {
			t.Fatalf("Get(%s): %s", id, err)
		}
	}

	if got := atomic.LoadInt32(&batchCalls); got != 0 {
		t.Fatalf("expected no batch calls, got %d", got)
	}
	if got := atomic.LoadInt32(&byIDCalls); got != 2 {
		t.Fatalf("expected 2 per-transaction calls, got %d", got)
	}
}

// Batch entries are matched case-insensitively, and an entry recorded at
// another height is ignored in favor of the per-transaction lookup.
func TestTxInfoResolverIgnoresOtherHeights(t *testing.T) {
	var batchCalls, byIDCalls int32
	batch := []TransactionInfo{
		{ID: "AA", Fee: 1, BlockNumber: 123},
		{ID: "bb", Fee: 99, BlockNumber: 122},
	}
	byID := map[string]TransactionInfo{"bb": {ID: "bb", Fee: 2, BlockNumber: 123}}
	srv := newCountingServer(t, http.StatusOK, batch, byID, &batchCalls, &byIDCalls)
	defer srv.Close()

	resolver := NewTronApi(srv.URL, time.Second).NewTxInfoResolver(123, true)

	info, err := resolver.Get("aa")
	if err != nil || info.Fee != 1 {
		t.Fatalf("expected batch entry for aa, got %+v, %v", info, err)
	}
	info, err = resolver.Get("bb")
	if err != nil || info.Fee != 2 {
		t.Fatalf("expected per-transaction info for bb, got %+v, %v", info, err)
	}

	if got := atomic.LoadInt32(&batchCalls); got != 1 {
		t.Fatalf("expected exactly 1 batch call, got %d", got)
	}
	if got := atomic.LoadInt32(&byIDCalls); got != 1 {
		t.Fatalf("expected 1 per-transaction call, got %d", got)
	}
}

// java-tron reports application errors as an HTTP 200 JSON object; on the
// batch endpoint that must degrade to per-transaction lookups, not to an empty
// result.
func TestTxInfoResolverBatchErrorObjectFallsBack(t *testing.T) {
	var batchCalls, byIDCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/wallet/gettransactioninfobyblocknum":
			atomic.AddInt32(&batchCalls, 1)
			_, _ = w.Write([]byte(`{"Error":"block info unavailable"}`))
		case "/wallet/gettransactioninfobyid":
			atomic.AddInt32(&byIDCalls, 1)
			_, _ = w.Write([]byte(`{"id":"aa","fee":1,"blockNumber":123}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	resolver := NewTronApi(srv.URL, time.Second).NewTxInfoResolver(123, true)

	info, err := resolver.Get("aa")
	if err != nil || info.ID != "aa" {
		t.Fatalf("expected per-transaction fallback, got %+v, %v", info, err)
	}
	if got := atomic.LoadInt32(&batchCalls); got != 1 {
		t.Fatalf("expected exactly 1 batch call, got %d", got)
	}
	if got := atomic.LoadInt32(&byIDCalls); got != 1 {
		t.Fatalf("expected 1 per-transaction call, got %d", got)
	}
}
