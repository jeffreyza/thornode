package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
)

func TestPackage(t *testing.T) { TestingT(t) }

type TssHttpServerTestSuite struct{}

var _ = Suite(&TssHttpServerTestSuite{})

const normalKeygenRequest = `{"keys":["thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3", "thorpub1addwnpepqtspqyy6gk22u37ztra4hq3hdakc0w0k60sfy849mlml2vrpfr0wvm6uz09", "thorpub1addwnpepq2ryyje5zr09lq7gqptjwnxqsy2vcdngvwd6z7yt5yjcnyj8c8cn559xe69", "thorpub1addwnpepqfjcw5l4ay5t00c32mmlky7qrppepxzdlkcwfs2fd5u73qrwna0vzag3y4j"]}`

func (TssHttpServerTestSuite) TestNewTssHttpServer(c *C) {
	tssServer := &MockTssServer{}
	s := NewTssHttpServer("127.0.0.1:0", tssServer)
	c.Assert(s, NotNil)
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Start()
		c.Assert(err, IsNil)
	}()
	time.Sleep(time.Second)
	c.Assert(s.Stop(), IsNil)
	tssServer.failToStart = true
	c.Assert(s.Start(), NotNil)
}

func (TssHttpServerTestSuite) TestLoopbackBindAccepted(c *C) {
	for _, addr := range []string{
		"127.0.0.1:8080",
		"localhost:8080",
		"[::1]:8080",
	} {
		c.Log(addr)
		c.Assert(validateLoopbackListenAddr(addr), IsNil)
	}
}

func (TssHttpServerTestSuite) TestNonLoopbackBindRejected(c *C) {
	for _, addr := range []string{
		"0.0.0.0:8080",
		":8080",
		"10.0.0.1:8080",
		"192.168.1.1:8080",
		"8.8.8.8:8080",
	} {
		c.Log(addr)
		c.Assert(validateLoopbackListenAddr(addr), NotNil)
	}

	s := NewTssHttpServer("0.0.0.0:0", &MockTssServer{})
	c.Assert(s.Start(), ErrorMatches, ".*loopback.*")
}

func (TssHttpServerTestSuite) TestPingHandler(c *C) {
	tssServer := &MockTssServer{}
	s := NewTssHttpServer("127.0.0.1:8080", tssServer)
	c.Assert(s, NotNil)
	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	res := httptest.NewRecorder()
	s.pingHandler(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
}

func (TssHttpServerTestSuite) TestGetP2pIDHandler(c *C) {
	tssServer := &MockTssServer{}
	s := NewTssHttpServer("127.0.0.1:8080", tssServer)
	c.Assert(s, NotNil)
	req := httptest.NewRequest(http.MethodGet, "/p2pid", nil)
	res := httptest.NewRecorder()
	s.getP2pIDHandler(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
}

func (TssHttpServerTestSuite) TestKeygenHandler(c *C) {
	testCases := []struct {
		name          string
		reqProvider   func() *http.Request
		setter        func(s *MockTssServer)
		resultChecker func(c *C, w *httptest.ResponseRecorder)
	}{
		{
			name: "method get should return status method not allowed",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/keygen", nil)
			},
			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusMethodNotAllowed)
			},
		},
		{
			name: "nil request body should return status bad request",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/keygen", nil)
			},
			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusBadRequest)
			},
		},
		{
			name: "fail to keygen should return status internal server error",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/keygen",
					bytes.NewBufferString(normalKeygenRequest))
			},
			setter: func(s *MockTssServer) {
				s.failToKeyGen = true
			},
			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusOK)
			},
		},
		{
			name: "normal",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/keygen",
					bytes.NewBufferString(normalKeygenRequest))
			},

			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusOK)
				var resp keygen.Response
				c.Assert(json.Unmarshal(w.Body.Bytes(), &resp), IsNil)
			},
		},
	}
	for _, tc := range testCases {
		c.Log(tc.name)
		tssServer := &MockTssServer{}
		s := NewTssHttpServer("127.0.0.1:8080", tssServer)
		c.Assert(s, NotNil)
		if tc.setter != nil {
			tc.setter(tssServer)
		}
		req := tc.reqProvider()
		res := httptest.NewRecorder()
		s.keygenHandler(res, req)
		tc.resultChecker(c, res)
	}
}

func (TssHttpServerTestSuite) TestPostBodyTooLargeRejected(c *C) {
	tooLargeValue := strings.Repeat("a", int(tssHTTPMaxRequestBodyBytes))
	tooLargeKeygenRequest := `{"keys":["` + tooLargeValue + `"]}`
	tooLargeKeysignRequest := `{"pool_pub_key":"pool","messages":["` + tooLargeValue + `"],"signer_pub_keys":["signer"]}`

	testCases := []struct {
		name    string
		path    string
		body    string
		handler func(*TssHttpServer, http.ResponseWriter, *http.Request)
	}{
		{
			name:    "keygen",
			path:    "/keygen",
			body:    tooLargeKeygenRequest,
			handler: (*TssHttpServer).keygenHandler,
		},
		{
			name:    "keygenall",
			path:    "/keygenall",
			body:    tooLargeKeygenRequest,
			handler: (*TssHttpServer).keygenAllHandler,
		},
		{
			name:    "keysign",
			path:    "/keysign",
			body:    tooLargeKeysignRequest,
			handler: (*TssHttpServer).keySignHandler,
		},
	}
	for _, tc := range testCases {
		c.Log(tc.name)
		s := NewTssHttpServer("127.0.0.1:8080", &MockTssServer{})
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(tc.body))
		res := httptest.NewRecorder()
		tc.handler(s, res, req)
		c.Assert(res.Code, Equals, http.StatusRequestEntityTooLarge)
	}
}

func (TssHttpServerTestSuite) TestAuthRequiredWhenSecretConfigured(c *C) {
	testCases := []struct {
		name   string
		header string
	}{
		{
			name: "missing auth",
		},
		{
			name:   "invalid auth",
			header: "Bearer wrong-secret",
		},
	}
	for _, tc := range testCases {
		c.Log(tc.name)
		s := NewTssHttpServerWithAuth("127.0.0.1:8080", &MockTssServer{}, "test-secret")
		req := httptest.NewRequest(http.MethodPost, "/keygen", bytes.NewBufferString(normalKeygenRequest))
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		res := httptest.NewRecorder()
		s.keygenHandler(res, req)
		c.Assert(res.Code, Equals, http.StatusUnauthorized)
	}
}

func (TssHttpServerTestSuite) TestValidAuthAcceptedWhenSecretConfigured(c *C) {
	s := NewTssHttpServerWithAuth("127.0.0.1:8080", &MockTssServer{}, "test-secret")
	req := httptest.NewRequest(http.MethodPost, "/keygen", bytes.NewBufferString(normalKeygenRequest))
	req.Header.Set("Authorization", "Bearer test-secret")
	res := httptest.NewRecorder()
	s.keygenHandler(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
}

func (TssHttpServerTestSuite) TestAuthDisabledLoopbackModeWorks(c *C) {
	s := NewTssHttpServer("127.0.0.1:8080", &MockTssServer{})
	req := httptest.NewRequest(http.MethodPost, "/keygen", bytes.NewBufferString(normalKeygenRequest))
	res := httptest.NewRecorder()
	s.tssNewHandler().ServeHTTP(res, req)
	c.Assert(res.Code, Equals, http.StatusOK)
}

func (TssHttpServerTestSuite) TestKeysignHandler(c *C) {
	var normalKeySignRequest string = `{
    "pool_pub_key": "thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3",
    "message": "helloworld",
    "signer_pub_keys": [
        "thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3",
        "thorpub1addwnpepqtspqyy6gk22u37ztra4hq3hdakc0w0k60sfy849mlml2vrpfr0wvm6uz09",
        "thorpub1addwnpepq2ryyje5zr09lq7gqptjwnxqsy2vcdngvwd6z7yt5yjcnyj8c8cn559xe69",
        "thorpub1addwnpepqfjcw5l4ay5t00c32mmlky7qrppepxzdlkcwfs2fd5u73qrwna0vzag3y4j"
    ]
}`
	testCases := []struct {
		name          string
		reqProvider   func() *http.Request
		setter        func(s *MockTssServer)
		resultChecker func(c *C, w *httptest.ResponseRecorder)
	}{
		{
			name: "method get should return status method not allowed",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/keysign", nil)
			},
			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusMethodNotAllowed)
			},
		},
		{
			name: "nil request body should return status bad request",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/keysign", nil)
			},
			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusBadRequest)
			},
		},
		{
			name: "fail to keygen should return status internal server error",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/keysign",
					bytes.NewBufferString(normalKeySignRequest))
			},
			setter: func(s *MockTssServer) {
				s.failToKeySign = true
			},
			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusInternalServerError)
			},
		},
		{
			name: "normal",
			reqProvider: func() *http.Request {
				return httptest.NewRequest(http.MethodPost, "/keysign",
					bytes.NewBufferString(normalKeySignRequest))
			},

			resultChecker: func(c *C, w *httptest.ResponseRecorder) {
				c.Assert(w.Code, Equals, http.StatusOK)
				var resp keygen.Response
				c.Assert(json.Unmarshal(w.Body.Bytes(), &resp), IsNil)
			},
		},
	}
	for _, tc := range testCases {
		c.Log(tc.name)
		tssServer := &MockTssServer{}
		s := NewTssHttpServer("127.0.0.1:8080", tssServer)
		c.Assert(s, NotNil)
		if tc.setter != nil {
			tc.setter(tssServer)
		}
		req := tc.reqProvider()
		res := httptest.NewRecorder()
		s.keySignHandler(res, req)
		tc.resultChecker(c, res)
	}
}
