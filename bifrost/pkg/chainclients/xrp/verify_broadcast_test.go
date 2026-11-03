package xrp

import (
	"testing"

	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/rpc/testutil"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestXRPVerifyBroadcast covers the signer-cache verifyFn: a validated tx →
// respect cache (present). Everything else is fail-closed ambiguous — notably
// txnNotFound, which the xrpl-go client surfaces as a bare error with a nil
// response, so searched_all is unavailable and we cannot safely treat it as
// definitive absence (a pruned online_delete node would double-spend). XRP thus
// has no auto-recovery via verify; it recovers via a manual/operator cache clear.
func TestXRPVerifyBroadcast(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     signercache.LookupResult
	}{
		{"validated", `{"result":{"validated":true,"hash":"ABC","status":"success"}}`, signercache.LookupFound},
		{"txnNotFound (bare error, nil res)", `{"result":{"error":"txnNotFound","searched_all":true,"status":"error"}}`, signercache.LookupAmbiguous},
		{"txnNotFound pruned", `{"result":{"error":"txnNotFound","searched_all":false,"status":"error"}}`, signercache.LookupAmbiguous},
		{"in-flight, not validated", `{"result":{"validated":false,"status":"success"}}`, signercache.LookupAmbiguous},
	}
	for _, tc := range cases {
		mc := &testutil.JSONRPCMockClient{}
		mc.DoFunc = testutil.MockResponse(tc.response, 200, mc)
		cfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
		if err != nil {
			t.Fatalf("%s: config: %v", tc.name, err)
		}
		c := &Client{
			cfg:       config.BifrostChainConfiguration{ChainID: common.XRPChain},
			rpcClient: rpc.NewClient(cfg),
		}
		if got := c.verifyBroadcast("ABC"); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
