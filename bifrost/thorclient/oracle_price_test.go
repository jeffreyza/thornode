package thorclient

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

func (s *FreshQuerySuite) TestOraclePriceQueriesLatest(c *C) {
	for _, tc := range []struct {
		name      string
		body      string
		status    int
		wantError bool
	}{
		{"valid", `{"price":{"symbol":"DASH","price":"50.125"}}`, 200, false},
		{"missing feed", `{"code":5,"message":"Price not found: DASH"}`, 404, true},
		{"server failure", "unavailable", 503, true},
		{"missing price", `{"price":null}`, 200, true},
		{"wrong symbol", `{"price":{"symbol":"BTC","price":"50"}}`, 200, true},
		{"empty price", `{"price":{"symbol":"DASH","price":""}}`, 200, true},
		{"malformed JSON", "broken", 200, true},
	} {
		func() {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				requests.Add(1)
				c.Check(req.URL.Path, Equals, "/thorchain/oracle/price/DASH", Commentf(tc.name))
				c.Check(req.URL.RawQuery, Equals, "", Commentf(tc.name))
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			bridge := newFreshQueryBridge(c, server)
			got, err := bridge.GetOraclePrice("DASH")
			if tc.wantError {
				c.Assert(err, NotNil, Commentf(tc.name))
				c.Assert(got.IsZero(), Equals, true, Commentf(tc.name))
			} else {
				c.Assert(err, IsNil, Commentf(tc.name))
				c.Assert(got.Equal(cosmos.NewUint(5012500000)), Equals, true)
			}
			c.Assert(requests.Load(), Equals, int32(1), Commentf(tc.name))
		}()
	}
}

func (s *FreshQuerySuite) TestOraclePriceDoesNotReuseCachedFeed(c *C) {
	var priceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/thorchain/oracle/price/DASH":
			if priceCalls.Add(1) == 1 {
				// Use the actual protobuf response type, rather than the generated
				// OpenAPI model, which currently describes a different JSON shape.
				_ = json.NewEncoder(w).Encode(stypes.QueryOraclePriceResponse{
					Price: &stypes.OraclePrice{Symbol: "DASH", Price: "50"},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	bridge := newFreshQueryBridge(c, server)
	got, err := bridge.GetOraclePrice("DASH")
	c.Assert(err, IsNil)
	c.Assert(got.Equal(cosmos.NewUint(50*common.One)), Equals, true)
	got, err = bridge.GetOraclePrice("DASH")
	c.Assert(err, NotNil)
	c.Assert(got.IsZero(), Equals, true)
	c.Assert(priceCalls.Load(), Equals, int32(2))
}

func (s *FreshQuerySuite) TestOraclePriceValidatesValue(c *C) {
	maxUint := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	maxPrice := new(big.Rat).SetFrac(maxUint, big.NewInt(common.One)).FloatString(8)
	for _, tc := range []struct {
		price string
		want  string // Zero means the bridge must return an error.
	}{
		{"50", "5000000000"},
		{"50.125", "5012500000"},
		{"00050.125000000", "5012500000"},
		{"0.000000010", "1"},
		{"0.123456789", "12345679"}, // Round a fractional USD base unit up.
		{"0.000000001", "1"},
		{"0." + strings.Repeat("0", 97) + "1", "1"}, // Smallest positive price within the length limit.
		{"0.00035000000000000000002", "35001"},      // Oracle decimals can exceed SDK decimal precision.
		{maxPrice, maxUint.String()},
		{maxPrice + "1", "0"}, // Rounding up past the largest Uint must fail.
		{"", "0"},
		{"0", "0"},
		{"0.000000000", "0"},
		{"-50", "0"},
		{"NaN", "0"},
		{"Inf", "0"},
		{"1/2", "0"},
		{"1e1000000", "0"},
		{"0x10", "0"},
		{strings.Repeat("9", 101), "0"},
		{strings.Repeat("9", 78), "0"}, // Fits the input length limit, but overflows Uint.
	} {
		func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/thorchain/oracle/price/BTC":
					_ = json.NewEncoder(w).Encode(stypes.QueryOraclePriceResponse{
						Price: &stypes.OraclePrice{Symbol: "BTC", Price: tc.price},
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			bridge := newFreshQueryBridge(c, server)
			got, err := bridge.GetOraclePrice("BTC")
			if tc.want == "0" {
				c.Assert(err, NotNil, Commentf("price: %q", tc.price))
			} else {
				c.Assert(err, IsNil, Commentf("price: %q", tc.price))
			}
			c.Assert(got.String(), Equals, tc.want, Commentf("price: %q", tc.price))
		}()
	}
}
