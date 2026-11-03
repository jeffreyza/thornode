package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	uberatomic "go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestGasUnitContract runs a market fee rate through the whole THORNode <->
// bifrost gas-unit contract with the real code on both sides: the scanner's
// network fee, THORNode's GasMgr, and signTx up to the FROST request and the
// MaxGas check on its result. The scanner posts piconero per weight for a
// typical vault outbound; the FROST signer must be asked for about that rate,
// and a typical outbound at that rate must fit the MaxGas THORNode reserved.
// !3925 shipped THORNode reading TransactionFeeRate in GetGasUnits units while
// bifrost still posted 1e8 units.
func TestGasUnitContract(t *testing.T) {
	// Market fee rates in piconero per weight: the scanner's default and a busy market.
	for _, feeRate := range []uint64{20_000, 400_000} {
		// 1. bifrost scanner: the fee it last observed, as the network fee the
		// block scanner posts from GetNetworkFee
		scanner := &Scanner{lastFee: uberatomic.NewUint64(feeRate * typicalVaultOutboundWeight)}
		size, rate := scanner.GetNetworkFee()
		networkFee := common.NetworkFee{Chain: common.XMRChain, Height: 1, TransactionSize: size, TransactionRate: rate}

		// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
		maxGas, gasRate, err := gascontract.GasDetails(networkFee)
		if err != nil {
			t.Fatalf("THORNode gas details for %d piconero/weight: %v", feeRate, err)
		}

		// 3. bifrost client: sign the outbound THORNode scheduled with them. The
		// FROST signer charges the requested rate for a typical vault outbound.
		tssServer := &mockFrostTssServer{}
		tssServer.fn = func(req tssp.FrostKeySignRequest) (tssp.FrostKeySignResponse, error) {
			resp := successfulFrostResponseForReq(t, req)
			resp.FeeAtomic = req.FeeRate * typicalVaultOutboundWeight
			planHash, planErr := tssp.FrostPlanHash(req, resp.MsgID)
			if planErr != nil {
				return resp, planErr
			}
			resp.Eventuality, planErr = tssp.BuildFrostEventuality(planHash, resp.TxHash, resp.TxHex, resp.TxKey, resp.FeeAtomic)
			return resp, planErr
		}
		c, tx, server, _ := newFrostSignTxCleanupFixture(t, tssServer, 0)
		funded := fundedSidecar(t, c, server.URL)
		tx.MaxGas = common.Gas{maxGas}
		tx.GasRate = gasRate
		signed, _, _, err := c.signTx(tx, 12345)
		funded.Close()
		server.Close()

		// 4. the signer is asked for about the observed rate, and a typical
		// outbound at that rate fits the MaxGas THORNode reserved
		req := tssServer.lastReq
		t.Logf("XMR at %d piconero/weight: posted rate %d (size %d), THORNode GasRate %d MaxGas %s, FROST fee_rate %d max_gas_atomic %d",
			feeRate, networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas, req.FeeRate, req.MaxGasAtomic)
		if err != nil || len(signed) == 0 {
			t.Errorf("XMR at %d piconero/weight: outbound not signed: %v", feeRate, err)
			continue
		}
		if req.FeeRate < feeRate || req.FeeRate*2 > feeRate*3 {
			t.Errorf("XMR at %d piconero/weight: FROST fee_rate %d, want [%d, %d]", feeRate, req.FeeRate, feeRate, feeRate*3/2)
		}
		if typicalFee := req.FeeRate * typicalVaultOutboundWeight; req.MaxGasAtomic < typicalFee {
			t.Errorf("XMR at %d piconero/weight: max_gas_atomic %d does not cover a typical outbound (%d atomic)", feeRate, req.MaxGasAtomic, typicalFee)
		}
	}
}

// fundedSidecar points c at a sidecar that reports a 1000 XMR vault balance
// and forwards everything else to the fixture sidecar at fixtureURL, so the
// pre-sign amount + MaxGas balance check passes for any sane MaxGas.
func fundedSidecar(t *testing.T, c *Client, fixtureURL string) *httptest.Server {
	t.Helper()
	target, err := url.Parse(fixtureURL)
	if err != nil {
		t.Fatalf("parse fixture sidecar url: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	balance := uint64(1_000 * common.One * moneroAtomicToThorchain)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/monero/balance/") {
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sidecar.Balance{
			Balance:          balance,
			UnlockedBalance:  balance,
			SpendableBalance: uint64Ptr(balance),
		})
	}))
	c.sidecar, err = sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("new funded sidecar client: %v", err)
	}
	return server
}
