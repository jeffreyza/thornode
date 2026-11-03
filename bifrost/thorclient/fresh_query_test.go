package thorclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"

	. "gopkg.in/check.v1"
)

type FreshQuerySuite struct{}

var _ = Suite(&FreshQuerySuite{})

func newFreshQueryBridge(c *C, server *httptest.Server) *thorchainBridge {
	cfg, _, kb := SetupThorchainForTest(c)
	cfg.ChainHost = strings.TrimPrefix(server.URL, "http://")
	cfg.ChainRPC = cfg.ChainHost

	bridge, err := NewThorchainBridge(cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	c.Assert(err, IsNil)
	concrete, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	concrete.httpClient.RetryMax = 0
	return concrete
}

func (s *FreshQuerySuite) TestFreshSpentRefsBypassesLockedCacheWithoutReplacingIt(c *C) {
	var requests atomic.Uint32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"spent_output_refs":[{"output_index":%d}]}`, requests.Add(1))
	}))
	defer server.Close()
	bridge := newFreshQueryBridge(c, server)
	vault := common.PubKey("spent-ref-vault")
	cached, err := bridge.GetXMRSpentOutputRefs(vault)
	c.Assert(err, IsNil)
	c.Assert(cached, HasLen, 1)
	c.Assert(cached[0].OutputIndex, Equals, uint32(1))

	endpoint := bridge.getThorChainURL(fmt.Sprintf(XMRSpentOutputRefsEndpoint, vault.String()))
	httpResponseCachesMu.Lock()
	cache := httpResponseCaches[endpoint]
	httpResponseCachesMu.Unlock()
	c.Assert(cache, NotNil)
	func() {
		c.Assert(cache.lock(context.Background()), IsNil)
		defer cache.unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		type response struct {
			refs []*common.OutputRef
			err  error
		}
		result := make(chan response, 1)
		go func() {
			refs, requestErr := bridge.GetXMRSpentOutputRefsUncached(ctx, vault)
			result <- response{refs: refs, err: requestErr}
		}()
		select {
		case fresh := <-result:
			c.Assert(fresh.err, IsNil)
			c.Assert(fresh.refs, HasLen, 1)
			c.Assert(fresh.refs[0].OutputIndex, Equals, uint32(2))
		case <-time.After(2 * time.Second):
			c.Fatal("fresh spent-ref lookup waited for the shared cache lock")
		}
	}()

	stillCached, err := bridge.GetXMRSpentOutputRefs(vault)
	c.Assert(err, IsNil)
	c.Assert(stillCached, HasLen, 1)
	c.Assert(stillCached[0].OutputIndex, Equals, uint32(1))
	c.Assert(requests.Load(), Equals, uint32(2))
}

func (s *FreshQuerySuite) TestFreshSpentRefsCancelsStalledHTTP(c *C) {
	testFreshQueryCancelsStalledHTTP(c, func(ctx context.Context, bridge *thorchainBridge) error {
		_, err := bridge.GetXMRSpentOutputRefsUncached(ctx, common.PubKey("stalled-ref-vault"))
		return err
	})
}

func (s *FreshQuerySuite) TestFreshOutboundVoterCancelsStalledHTTP(c *C) {
	testFreshQueryCancelsStalledHTTP(c, func(ctx context.Context, bridge *thorchainBridge) error {
		_, _, err := bridge.GetObservedTxOutVoterUncached(ctx, strings.Repeat("A", 64))
		return err
	})
}

func testFreshQueryCancelsStalledHTTP(c *C, query func(context.Context, *thorchainBridge) error) {
	for _, tc := range []struct {
		name        string
		flushHeader bool
		useDeadline bool
	}{
		{name: "cancel before headers"},
		{name: "cancel during body", flushHeader: true},
		{name: "deadline before headers", useDeadline: true},
		{name: "deadline during body", flushHeader: true, useDeadline: true},
	} {
		func() {
			started := make(chan struct{})
			stopped := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if tc.flushHeader {
					w.WriteHeader(http.StatusOK)
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					} else {
						c.Error("test HTTP response writer does not support flushing")
					}
				}
				close(started)
				select {
				case <-req.Context().Done():
				case <-release:
				}
				close(stopped)
			}))
			defer server.Close()
			defer close(release)
			bridge := newFreshQueryBridge(c, server)
			ctx, cancel := context.WithCancel(context.Background())
			wantErr := context.Canceled
			if tc.useDeadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
				wantErr = context.DeadlineExceeded
			}
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- query(ctx, bridge)
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				c.Fatalf("%s: HTTP request did not start", tc.name)
			}
			if !tc.useDeadline {
				cancel()
			}
			select {
			case requestErr := <-result:
				c.Check(errors.Is(requestErr, wantErr), Equals, true, Commentf("%s: %v", tc.name, requestErr))
			case <-time.After(time.Second):
				c.Fatalf("%s: stalled HTTP request ignored caller cancellation", tc.name)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				c.Fatalf("%s: request returned without canceling the HTTP connection", tc.name)
			}
		}()
	}
}

func (s *FreshQuerySuite) TestFreshSpentRefsCapsRequestDeadline(c *C) {
	testFreshQueryCapsRequestDeadline(c, func(ctx context.Context, bridge *thorchainBridge) error {
		_, err := bridge.GetXMRSpentOutputRefsUncached(ctx, common.PubKey("deadline-ref-vault"))
		return err
	})
}

func (s *FreshQuerySuite) TestFreshOutboundVoterCapsRequestDeadline(c *C) {
	testFreshQueryCapsRequestDeadline(c, func(ctx context.Context, bridge *thorchainBridge) error {
		_, _, err := bridge.GetObservedTxOutVoterUncached(ctx, strings.Repeat("A", 64))
		return err
	})
}

func testFreshQueryCapsRequestDeadline(c *C, query func(context.Context, *thorchainBridge) error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	bridge := newFreshQueryBridge(c, server)
	transport := bridge.httpClient.HTTPClient.Transport
	var seenDeadline time.Time
	bridge.httpClient.HTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var hasDeadline bool
		seenDeadline, hasDeadline = req.Context().Deadline()
		c.Assert(hasDeadline, Equals, true)
		return transport.RoundTrip(req)
	})
	for _, parentTimeout := range []time.Duration{0, 2 * constants.ThorchainBlockTime, constants.ThorchainBlockTime / 2} {
		func() {
			ctx := context.Background()
			if parentTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, parentTimeout)
				defer cancel()
			}
			before := time.Now()
			err := query(ctx, bridge)
			after := time.Now()
			c.Assert(err, IsNil)
			if parentTimeout > 0 && parentTimeout < constants.ThorchainBlockTime {
				parentDeadline, _ := ctx.Deadline()
				c.Check(seenDeadline.Equal(parentDeadline), Equals, true)
			} else {
				c.Check(seenDeadline.Before(before.Add(constants.ThorchainBlockTime)), Equals, false)
				c.Check(seenDeadline.After(after.Add(constants.ThorchainBlockTime)), Equals, false)
			}
		}()
	}
}

func (s *FreshQuerySuite) TestFreshVoterBypassesButDoesNotReplaceCache(c *C) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requests++
		reverted := requests > 1
		_, err := fmt.Fprintf(rw, `{"tx_id":"%s","reverted":%t}`, strings.Repeat("A", 64), reverted)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	txID := strings.Repeat("A", 64)

	cached, err := bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(cached.Reverted, Equals, false)

	fresh, found, err := bridge.GetObservedTxVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(fresh, NotNil)
	c.Assert(fresh.Reverted, Equals, true)

	stillCached, err := bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(stillCached.Reverted, Equals, false)
	c.Assert(requests, Equals, 2)
}

func (s *FreshQuerySuite) TestOutboundVoterUsesSeparateCacheAndFreshLookup(c *C) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		height := 11
		if req.URL.Query().Get("outbound") == "true" {
			height = requests * 11
		}
		_, err := fmt.Fprintf(w, `{"tx_id":"%s","consensus_height":%d}`, strings.Repeat("A", 64), height)
		c.Assert(err, IsNil)
	}))
	defer server.Close()
	bridge := newFreshQueryBridge(c, server)
	txID := strings.Repeat("A", 64)
	inbound, err := bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(inbound.Height, Equals, int64(11))
	outbound, err := bridge.GetObservedTxOutVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(outbound.Height, Equals, int64(22))
	fresh, found, err := bridge.GetObservedTxOutVoterUncached(context.Background(), txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Assert(fresh.Height, Equals, int64(33))
	stillCached, err := bridge.GetObservedTxOutVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(stillCached.Height, Equals, int64(22))
	inbound, err = bridge.GetObservedTxVoter(txID)
	c.Assert(err, IsNil)
	c.Assert(inbound.Height, Equals, int64(11))
	c.Assert(requests, Equals, 3)
}

func (s *FreshQuerySuite) TestOutboundVoterQueryFailures(c *C) {
	txID := strings.Repeat("B", 64)
	for _, test := range []struct {
		status    int
		body      string
		wantError bool
	}{
		{http.StatusInternalServerError, fmt.Sprintf(`{"message":"tx: %s doesn't exist"}`, txID), false},
		{http.StatusNotFound, "", true},
		{http.StatusServiceUnavailable, "unavailable", true},
		{http.StatusOK, "invalid json", true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			c.Assert(req.URL.Query().Get("outbound"), Equals, "true")
			w.WriteHeader(test.status)
			_, _ = w.Write([]byte(test.body))
		}))
		bridge := newFreshQueryBridge(c, server)
		voter, found, err := bridge.GetObservedTxOutVoterUncached(context.Background(), txID)
		c.Assert(err != nil, Equals, test.wantError)
		c.Assert(found, Equals, false)
		c.Assert(voter, IsNil)
		server.Close()
	}
}

func (s *FreshQuerySuite) TestFreshVoterReportsBareNotFoundAsFailure(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(strings.Repeat("B", 64))
	c.Assert(err, NotNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshVoterReportsTransactionSpecificNotFoundAsAbsent(c *C) {
	txID := strings.Repeat("B", 64)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusNotFound)
		_, err := fmt.Fprintf(rw, `{"message":"tx: %s doesn't exist"}`, txID)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshVoterReportsCosmosMissingQueryAsAbsent(c *C) {
	txID := strings.Repeat("D", 64)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
		_, err := fmt.Fprintf(rw, `{"code":13,"message":"tx: %s doesn't exist"}`, txID)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(txID)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshVoterReportsServerFailure(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	voter, found, err := bridge.GetObservedTxVoterUncached(strings.Repeat("C", 64))
	c.Assert(err, NotNil)
	c.Assert(found, Equals, false)
	c.Assert(voter, IsNil)
}

func (s *FreshQuerySuite) TestFreshBlockHeightBypassesButDoesNotReplaceCache(c *C) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		requests++
		_, err := fmt.Fprintf(rw, `[{"chain":"THOR","thorchain":%d}]`, 40+requests)
		c.Assert(err, IsNil)
	}))
	defer server.Close()

	bridge := newFreshQueryBridge(c, server)
	cached, err := bridge.GetBlockHeight()
	c.Assert(err, IsNil)
	c.Assert(cached, Equals, int64(41))

	fresh, err := bridge.GetBlockHeightUncached()
	c.Assert(err, IsNil)
	c.Assert(fresh, Equals, int64(42))

	stillCached, err := bridge.GetBlockHeight()
	c.Assert(err, IsNil)
	c.Assert(stillCached, Equals, int64(41))
	c.Assert(requests, Equals, 2)
}
