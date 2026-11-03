package thorchain

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	sdklog "cosmossdk.io/log"
	"github.com/gorilla/mux"
	. "gopkg.in/check.v1"

	thorlog "gitlab.com/thorchain/thornode/v3/log"
)

type LogsRouteSuite struct{}

var _ = Suite(&LogsRouteSuite{})

func (s *LogsRouteSuite) get(c *C, query string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	RegisterLogsRoute(router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thorchain/logs"+query, nil))
	return w
}

func (s *LogsRouteSuite) entries(c *C, w *httptest.ResponseRecorder) []map[string]any {
	c.Assert(w.Code, Equals, http.StatusOK, Commentf("body: %s", w.Body.String()))
	var out []map[string]any
	c.Assert(json.Unmarshal(w.Body.Bytes(), &out), IsNil)
	return out
}

func (s *LogsRouteSuite) TestLogsRoute(c *C) {
	// disabled by default
	w := s.get(c, "")
	c.Assert(w.Code, Equals, http.StatusBadRequest)
	c.Assert(strings.Contains(w.Body.String(), "disabled"), Equals, true)

	thorlog.InitBlockLogs(c.MkDir(), 10)
	defer thorlog.InitBlockLogs("", 0)

	const last = int64(12)
	l := thorlog.WithBlockCapture(sdklog.NewNopLogger(), last)
	l.Debug("noise")
	l.With("module", "x/thorchain").Info("hello", "k", "v")
	l.With("module", "x/bank").Info("burned")
	thorlog.EndBlock(last)

	// defaults: start = end = last captured block, level = info, thorchain only
	got := s.entries(c, s.get(c, ""))
	c.Assert(got, HasLen, 1)
	c.Assert(got[0]["msg"], Equals, "hello")
	c.Assert(got[0]["height"], Equals, float64(last))

	// explicit debug includes everything the module emitted
	c.Assert(s.entries(c, s.get(c, "?start=12&end=12&level=debug")), HasLen, 2)

	// modules selects sdk modules
	got = s.entries(c, s.get(c, "?modules=x/bank"))
	c.Assert(got, HasLen, 1)
	c.Assert(got[0]["msg"], Equals, "burned")
	c.Assert(s.entries(c, s.get(c, "?modules=x/bank&modules=x/thorchain")), HasLen, 2)

	// an empty modules value falls back to the default rather than selecting
	// the module named "", which would answer an empty array for a typo
	got = s.entries(c, s.get(c, "?modules="))
	c.Assert(got, HasLen, 1)
	c.Assert(got[0]["msg"], Equals, "hello")

	// "*" reaches every module, without naming each one up front
	c.Assert(s.entries(c, s.get(c, "?modules=*")), HasLen, 2)

	// blocks past the tip are clamped away, not an error
	c.Assert(s.entries(c, s.get(c, "?start=1&end=99999&level=debug")), HasLen, 2)
}

// A saturated read is retryable, so it must not look like a bad request: a
// caller resuming a capped range by height would otherwise give up on it. A
// cancelled request has nobody left to answer. A block file that would not
// read is the node's fault, not the caller's, and must not land on a 4xx an
// operator would read as a client typo.
func (s *LogsRouteSuite) TestLogsErrorStatus(c *C) {
	status, ok := logsErrorStatus(thorlog.ErrTooManyReads)
	c.Assert(ok, Equals, true)
	c.Assert(status, Equals, http.StatusServiceUnavailable)

	_, ok = logsErrorStatus(context.Canceled)
	c.Assert(ok, Equals, false)

	status, ok = logsErrorStatus(fmt.Errorf("%w: block 3: %w", thorlog.ErrBlockRead, bufio.ErrTooLong))
	c.Assert(ok, Equals, true)
	c.Assert(status, Equals, http.StatusInternalServerError)

	status, ok = logsErrorStatus(errors.New("start 5 is after end 4"))
	c.Assert(ok, Equals, true)
	c.Assert(status, Equals, http.StatusBadRequest)
}

// An empty modules value is a typo, not a selection of the module named "":
// every stored entry carries a module, so honouring it would answer an empty
// array. "*" is the one way to ask for every module.
func (s *LogsRouteSuite) TestLogsModulesParam(c *C) {
	modules, all := modulesParam([]string{""})
	c.Assert(all, Equals, false)
	c.Assert(modules, HasLen, 0)

	modules, all = modulesParam([]string{"x/bank", ""})
	c.Assert(all, Equals, false)
	c.Assert(modules, DeepEquals, []string{"x/bank"})

	modules, all = modulesParam([]string{"x/bank", "*"})
	c.Assert(all, Equals, true)
	c.Assert(modules, HasLen, 0)
}

// Every rejected request is a 400 naming what was wrong, not a 200 over the
// wrong blocks: callers resume a capped range by height and must not loop.
func (s *LogsRouteSuite) TestLogsRouteRejects(c *C) {
	thorlog.InitBlockLogs(c.MkDir(), 10)
	defer thorlog.InitBlockLogs("", 0)
	thorlog.EndBlock(5)

	for _, tc := range []struct{ query, want string }{
		{"?start=5&end=4", "is after end"},
		{"?start=abc", "invalid height param"},
		{"?end=abc", "invalid height param"},
		{"?start=-1", "invalid height param"},
		{"?start=0", "invalid height param"},
		{"?level=loud", "unknown level"},
	} {
		w := s.get(c, tc.query)
		c.Assert(w.Code, Equals, http.StatusBadRequest, Commentf("%s: %s", tc.query, w.Body.String()))
		c.Assert(strings.Contains(w.Body.String(), tc.want), Equals, true,
			Commentf("%s: want %q, got %q", tc.query, tc.want, w.Body.String()))
	}
}

// thorlog.DefaultModule is the endpoint's default `modules` filter and must
// keep matching the tag module.go/msg_server.go put on their loggers. The log
// package cannot import this one, so the tie is asserted here.
func (s *LogsRouteSuite) TestDefaultModuleMatchesModuleName(c *C) {
	c.Assert(thorlog.DefaultModule, Equals, "x/"+ModuleName)
}
