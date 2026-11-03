package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
)

// -------------------------------------------------------------------------------------
// FormatUSD
// -------------------------------------------------------------------------------------

func (t *Test) TestFormatUSD(c *C) {
	tests := []struct {
		value    float64
		expected string
	}{
		{0, "$0.00"},
		{1.23, "$1.23"},
		{100, "$100.00"},
		{1234, "$1,234.00"},
		{-1234, "-$1,234.00"},
		{0.01, "$0.01"},
	}
	for _, tt := range tests {
		c.Logf("case: %v", tt.value)
		c.Assert(FormatUSD(tt.value), Equals, tt.expected)
	}
}

// -------------------------------------------------------------------------------------
// IsThorchainModule
// -------------------------------------------------------------------------------------

func (t *Test) TestIsThorchainModule(c *C) {
	c.Assert(IsThorchainModule("thor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38cg2q6y"), Equals, true)
	c.Assert(IsThorchainModule("sthor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38v3kkv6"), Equals, true)
	c.Assert(IsThorchainModule("thor1xxxxxxxxx"), Equals, false)
	c.Assert(IsThorchainModule(""), Equals, false)
}

// -------------------------------------------------------------------------------------
// Moneybags
// -------------------------------------------------------------------------------------

func (t *Test) TestMoneybags(c *C) {
	usdPerBag := config.Get().Styles.USDPerMoneyBag
	c.Assert(Moneybags(0), Equals, "")
	c.Assert(Moneybags(usdPerBag-1), Equals, "")
	c.Assert(Moneybags(usdPerBag), Equals, config.EmojiMoneybag)
	c.Assert(Moneybags(usdPerBag*2), Equals, config.EmojiMoneybag+config.EmojiMoneybag)
}

// -------------------------------------------------------------------------------------
// CoinToCommon
// -------------------------------------------------------------------------------------

func (t *Test) TestCoinToCommon(c *C) {
	coin := CoinToCommon(openapi.Coin{
		Asset:  "BTC.BTC",
		Amount: "100000000",
	})
	c.Assert(coin.Asset, Equals, common.BTCAsset)
	c.Assert(coin.Amount, DeepEquals, cosmos.NewUint(100000000))
}

// -------------------------------------------------------------------------------------
// OrderedMap
// -------------------------------------------------------------------------------------

func (t *Test) TestOrderedMap(c *C) {
	om := NewOrderedMap()
	c.Assert(om.Keys(), DeepEquals, []string{})

	om.Set("b", 2)
	om.Set("a", 1)
	om.Set("c", 3)

	c.Assert(om.Keys(), DeepEquals, []string{"b", "a", "c"})

	v, ok := om.Get("a")
	c.Assert(ok, Equals, true)
	c.Assert(v, Equals, 1)

	v, ok = om.Get("x")
	c.Assert(ok, Equals, false)
	c.Assert(v, IsNil)

	// Update existing key - order preserved
	om.Set("b", 20)
	c.Assert(om.Keys(), DeepEquals, []string{"b", "a", "c"})
	v, _ = om.Get("b")
	c.Assert(v, Equals, 20)

	// Delete
	om.Delete("a")
	c.Assert(om.Keys(), DeepEquals, []string{"b", "c"})
	_, ok = om.Get("a")
	c.Assert(ok, Equals, false)

	// Delete non-existent key
	om.Delete("x")
	c.Assert(om.Keys(), DeepEquals, []string{"b", "c"})

	// Nil map Keys()
	var nilMap *OrderedMap
	c.Assert(nilMap.Keys(), DeepEquals, []string{})
}

// -------------------------------------------------------------------------------------
// Retry
// -------------------------------------------------------------------------------------

func (t *Test) TestRetrySuccess(c *C) {
	calls := 0
	err := Retry(3, func() error {
		calls++
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(calls, Equals, 1)
}

func (t *Test) TestRetryFailsThenSucceeds(c *C) {
	calls := 0
	err := Retry(3, func() error {
		calls++
		if calls < 3 {
			return errors.New("fail")
		}
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(calls, Equals, 3)
}

func (t *Test) TestRetryExhausted(c *C) {
	calls := 0
	err := Retry(0, func() error {
		calls++
		return errors.New("fail")
	})
	c.Assert(err, NotNil)
	c.Assert(calls, Equals, 1)
}

func (t *Test) TestRetryBackoffCap(c *C) {
	// With 1 retry, the backoff doubles once (1s -> 2s), but we just need it
	// to exercise the retry path including the backoff logic.
	calls := 0
	err := Retry(1, func() error {
		calls++
		if calls < 2 {
			return errors.New("fail")
		}
		return nil
	})
	c.Assert(err, IsNil)
	c.Assert(calls, Equals, 2)
}

// -------------------------------------------------------------------------------------
// Store / Load
// -------------------------------------------------------------------------------------

func (t *Test) TestStoreAndLoad(c *C) {
	// Use the default config StoragePath (/tmp/events)
	testFile := fmt.Sprintf("test_store_load_%d.json", os.Getpid())
	defer os.Remove(filepath.Join(config.Get().StoragePath, testFile))

	// Store
	data := map[string]string{"key": "value"}
	err := Store(testFile, data)
	c.Assert(err, IsNil)

	// Load
	var loaded map[string]string
	err = Load(testFile, &loaded)
	c.Assert(err, IsNil)
	c.Assert(loaded, DeepEquals, data)
}

func (t *Test) TestLoadNotExists(c *C) {
	var data map[string]string
	err := Load("nonexistent_file.json", &data)
	c.Assert(err, NotNil)
}

// -------------------------------------------------------------------------------------
// Prune
// -------------------------------------------------------------------------------------

func (t *Test) TestPruneNonExistent(c *C) {
	// Should not panic on non-existent path
	Prune("nonexistent_prune_dir")
}

func (t *Test) TestPruneWithFiles(c *C) {
	// Create a temp dir under StoragePath
	pruneDir := fmt.Sprintf("test_prune_%d", os.Getpid())
	fullDir := filepath.Join(config.Get().StoragePath, pruneDir)
	c.Assert(os.MkdirAll(fullDir, 0o755), IsNil)
	defer os.RemoveAll(fullDir)

	// Create a test file (recent, won't be deleted by prune)
	testFile := filepath.Join(fullDir, "recent.json")
	c.Assert(os.WriteFile(testFile, []byte("test"), 0o600), IsNil)

	// Prune should walk files without error
	Prune(pruneDir)
}

func (t *Test) TestStoreErrorBadPath(c *C) {
	// Try to store to a path with null bytes which is invalid
	err := Store("\x00/invalid", "data")
	c.Assert(err, NotNil)
}

func (t *Test) TestLoadBadJSON(c *C) {
	// Store invalid JSON and try to load
	testFile := fmt.Sprintf("test_bad_json_%d.json", os.Getpid())
	fullPath := filepath.Join(config.Get().StoragePath, testFile)
	c.Assert(os.MkdirAll(config.Get().StoragePath, 0o755), IsNil)
	c.Assert(os.WriteFile(fullPath, []byte("not json"), 0o600), IsNil)
	defer os.Remove(fullPath)

	var data map[string]string
	err := Load(testFile, &data)
	c.Assert(err, NotNil)
}

// -------------------------------------------------------------------------------------
// RetryGet
// -------------------------------------------------------------------------------------

func (t *Test) TestRetryGetSuccess(c *C) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"key": "value"})
	}))
	defer ts.Close()

	var result map[string]string
	err := RetryGet(ts.URL, &result)
	c.Assert(err, IsNil)
	c.Assert(result["key"], Equals, "value")
}

func (t *Test) TestRetryBadStatus(c *C) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	// RetryGet uses config.Get().MaxRetries which defaults to 10 with exponential backoff.
	// We can't override config, so we test with Retry directly at 0 retries.
	var result map[string]string
	err := Retry(0, func() error {
		res, getErr := http.Get(ts.URL)
		if getErr != nil {
			return getErr
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("status code %d", res.StatusCode)
		}
		return json.NewDecoder(res.Body).Decode(&result)
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "500"), Equals, true)
}

// -------------------------------------------------------------------------------------
// ThornodeCachedRetryGet
// -------------------------------------------------------------------------------------

// ThornodeCachedRetryGet requires config.Get().Endpoints.Thornode to point to
// a real server and cannot be overridden in tests without modifying production code.
// We only test InitCache here.

func (t *Test) TestInitCache(c *C) {
	// Should not panic
	InitCache()
}

// -------------------------------------------------------------------------------------
// USDValueString (integration - depends on HTTP)
// -------------------------------------------------------------------------------------

// We skip functions that require live HTTP endpoints (RuneValue, USDValue,
// ExternalUSDValue, Clout, GetConfigInt64, USDValueString) as they depend
// on config.Get().Endpoints.Thornode pointing to a real server and the cache
// being initialized.
