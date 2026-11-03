package thorclient

import (
	"net/http"
	"net/http/httptest"
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeysignSuite struct {
	server     *httptest.Server
	bridge     *thorchainBridge
	cfg        config.BifrostClientConfiguration
	fixture    string
	requestURI string
}

var _ = Suite(&KeysignSuite{})

func (s *KeysignSuite) SetUpSuite(c *C) {
	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		s.requestURI = req.RequestURI
		if strings.HasPrefix(req.RequestURI, KeysignEndpoint) {
			httpTestHandler(c, rw, s.fixture)
		}
	}))

	cfg, _, kb := SetupThorchainForTest(c)
	s.cfg = cfg
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	var err error
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	var ok bool
	s.bridge, ok = bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	s.bridge.httpClient.RetryMax = 1
	c.Assert(err, IsNil)
	c.Assert(s.bridge, NotNil)
}

func (s *KeysignSuite) TearDownSuite(c *C) {
	s.server.Close()
}

// TestGetKeysign keeps the normal pubkey-scoped keysign endpoint behavior.
func (s *KeysignSuite) TestGetKeysign(c *C) {
	s.fixture = "../../test/fixtures/endpoints/keysign/template.json"
	pk := types.GetRandomPubKey()
	keysign, err := s.bridge.GetKeysign(1718, pk.String())
	c.Assert(err, IsNil)
	c.Assert(keysign, NotNil)
	c.Assert(keysign.Height, Equals, int64(1718))
	c.Assert(keysign.TxArray[0].Chain, Equals, common.ETHChain)
	c.Assert(s.requestURI, Equals, KeysignEndpoint+"/1718/"+pk.String())
}

// TestGetKeysignWithoutPubKeyUsesHeightEndpoint covers the public height lookup
// used by XMR peer spent-ref validation when the caller does not know the
// retiring vault pubkey.
func (s *KeysignSuite) TestGetKeysignWithoutPubKeyUsesHeightEndpoint(c *C) {
	s.fixture = "../../test/fixtures/endpoints/keysign/template.json"
	keysign, err := s.bridge.GetKeysign(1718, "")
	c.Assert(err, IsNil)
	c.Assert(keysign, NotNil)
	c.Assert(keysign.Height, Equals, int64(1718))
	// XMR peer spent-ref validation needs the public height endpoint because a
	// newly churned-in validator may not know the retiring vault pubkey.
	c.Assert(s.requestURI, Equals, KeysignEndpoint+"/1718")
}
