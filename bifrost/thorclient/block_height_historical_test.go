package thorclient

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"gitlab.com/thorchain/thornode/v3/common"
	. "gopkg.in/check.v1"
)

func (s *BlockHeightSuite) TestGetLastObservedInHeightAtHeight(c *C) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       int64
	}{
		{"historical", `[{"chain":"ZEC","last_observed_in":3500000,"thorchain":1237}]`, 200, 3500000},
		{"latest state rejected", `[{"chain":"ZEC","last_observed_in":3500010,"thorchain":1238}]`, 200, 0},
		{"missing chain", `[{"chain":"BTC","last_observed_in":3500000,"thorchain":1237}]`, 200, 0},
		{"empty response", `[]`, 200, 0},
		{"zero observed height", `[{"chain":"ZEC","last_observed_in":0,"thorchain":1237}]`, 200, 0},
		{"negative observed height", `[{"chain":"ZEC","last_observed_in":-1,"thorchain":1237}]`, 200, 0},
		{"malformed", `{`, 200, 0},
		{"pruned", `{"error":"height has been pruned"}`, 500, 0},
	} {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Path != "/thorchain/lastblock/ZEC" || r.URL.RawQuery != "height=1237" {
				http.Error(w, "expected a pinned height", 400)
				return
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		for _, host := range []string{server.Listener.Addr().String(), server.URL} {
			originalHost := s.bridge.cfg.ChainHost
			s.bridge.cfg.ChainHost = host
			height, err := s.bridge.GetLastObservedInHeightAtHeight(common.ZECChain, 1237)
			s.bridge.cfg.ChainHost = originalHost
			if tc.want > 0 {
				c.Check(err, IsNil, Commentf(tc.name))
			} else {
				c.Check(err, NotNil, Commentf(tc.name))
			}
			c.Check(height, Equals, tc.want, Commentf(tc.name))
		}
		server.Close()
		c.Assert(requests.Load(), Equals, int64(2), Commentf(tc.name))
	}
	for _, height := range []int64{0, -1} {
		_, err := s.bridge.GetLastObservedInHeightAtHeight(common.ZECChain, height)
		c.Assert(err, NotNil)
	}
	_, err := s.bridge.GetLastObservedInHeightAtHeight(common.EmptyChain, 1237)
	c.Assert(err, NotNil)
}
