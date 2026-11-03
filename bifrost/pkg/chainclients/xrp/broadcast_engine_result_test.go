package xrp

import (
	"testing"

	binarycodec "github.com/Peersyst/xrpl-go/binary-codec"
	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/rpc/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// broadcastTx must treat engine results meaning "this tx is already on the
// ledger" as success, and anything else as a failed submit.
func TestBroadcastTxEngineResults(t *testing.T) {
	// Submit decodes the blob client-side and requires a signature field, so the
	// request actually reaches the mock server only with a well-formed blob.
	blob, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"SigningPubKey":   "02" + "11223344556677889900AABBCCDDEEFF11223344556677889900AABBCCDDEEFF",
		"TxnSignature":    "3045022100AA",
	})
	require.NoError(t, err)

	cases := []struct {
		engineResult string
		wantErr      bool
	}{
		{"tesSUCCESS", false},
		{"tefPAST_SEQ", false},
		{"tefALREADY", false},
		{"tecUNFUNDED_PAYMENT", true},
		{"terQUEUED", true},
	}
	for _, tc := range cases {
		t.Run(tc.engineResult, func(t *testing.T) {
			mc := &testutil.JSONRPCMockClient{}
			mc.DoFunc = testutil.MockResponse(
				`{"result":{"engine_result":"`+tc.engineResult+`","status":"success","tx_blob":"`+blob+`"}}`, 200, mc)
			cfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
			require.NoError(t, err)
			c := &Client{
				cfg:       config.BifrostChainConfiguration{ChainID: common.XRPChain},
				rpcClient: rpc.NewClient(cfg),
			}

			err = c.broadcastTx(blob)
			require.NotNil(t, mc.Spy, "submit must reach the server")
			if tc.wantErr {
				require.ErrorContains(t, err, tc.engineResult)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
