package xrp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Peersyst/xrpl-go/xrpl/rpc"
	"github.com/Peersyst/xrpl-go/xrpl/rpc/testutil"
	"github.com/stretchr/testify/require"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

const lgrNotFoundResponse = `{"result":{"error":"lgrNotFound","error_code":21,"error_message":"ledgerNotFound","status":"error"}}`

func testLgrNotFoundScanner(t *testing.T, doFunc func(*http.Request) (*http.Response, error)) *XrpBlockScanner {
	t.Helper()
	mc := &testutil.JSONRPCMockClient{DoFunc: doFunc}
	cfg, err := rpc.NewClientConfig("http://testnode/", rpc.WithHTTPClient(mc))
	require.NoError(t, err)
	return &XrpBlockScanner{
		cfg:       config.BifrostBlockScannerConfiguration{ChainID: common.XRPChain},
		rpcClient: rpc.NewClient(cfg),
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte(body)))}
}

// requireEscalatesAfterLimit drives FetchTxs at height until the lgrNotFound
// streak crosses LedgerNotFoundRetryLimit, and checks it stays escalated.
func requireEscalatesAfterLimit(t *testing.T, s *XrpBlockScanner, height int64) {
	t.Helper()
	for i := 1; i < LedgerNotFoundRetryLimit; i++ {
		_, err := s.FetchTxs(height, height+10)
		require.ErrorIs(t, err, btypes.ErrUnavailableBlock, "attempt %d", i)
	}
	// at and past the limit the raw error is returned, so the generic scanner's
	// stall clock runs and skipAheadIfStalled can fire
	for i := 0; i < 3; i++ {
		_, err := s.FetchTxs(height, height+10)
		require.Error(t, err)
		require.False(t, errors.Is(err, btypes.ErrUnavailableBlock))
		require.True(t, isLedgerNotFound(err), "escalated error must still identify lgrNotFound: %v", err)
	}
}

func TestFetchTxsLedgerNotFoundEscalates(t *testing.T) {
	s := testLgrNotFoundScanner(t, func(*http.Request) (*http.Response, error) {
		return jsonResponse(lgrNotFoundResponse), nil
	})

	requireEscalatesAfterLimit(t, s, 100)

	// a different height starts a fresh streak
	_, err := s.FetchTxs(101, 110)
	require.ErrorIs(t, err, btypes.ErrUnavailableBlock)

	// and going back to the old height does not inherit its count
	_, err = s.FetchTxs(100, 110)
	require.ErrorIs(t, err, btypes.ErrUnavailableBlock)
}

// The expanded ledger request is a second lgrNotFound site; it must share the
// same per-height streak.
func TestFetchTxsLedgerNotFoundOnExpandedRequest(t *testing.T) {
	hashOnly := 0
	s := testLgrNotFoundScanner(t, func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if strings.Contains(string(body), `"expand":true`) {
			return jsonResponse(lgrNotFoundResponse), nil
		}
		hashOnly++
		return jsonResponse(`{"result":{"ledger":{"transactions":[]},"ledger_hash":"AB","ledger_index":100,"validated":true,"status":"success"}}`), nil
	})

	requireEscalatesAfterLimit(t, s, 100)
	require.Equal(t, LedgerNotFoundRetryLimit+2, hashOnly, "every attempt must reach the expanded request")
}

func TestLedgerNotFoundResetOnSuccess(t *testing.T) {
	s := &XrpBlockScanner{}
	notFound := &rpc.ClientError{ErrorString: "lgrNotFound"}

	for i := 1; i < LedgerNotFoundRetryLimit; i++ {
		require.ErrorIs(t, s.ledgerNotFound(100, notFound), btypes.ErrUnavailableBlock)
	}

	// a success for another height leaves the streak alone
	s.resetLedgerNotFound(101)
	err := s.ledgerNotFound(100, notFound)
	require.False(t, errors.Is(err, btypes.ErrUnavailableBlock))

	// a success for the tracked height clears it
	s.resetLedgerNotFound(100)
	require.ErrorIs(t, s.ledgerNotFound(100, notFound), btypes.ErrUnavailableBlock)
}
