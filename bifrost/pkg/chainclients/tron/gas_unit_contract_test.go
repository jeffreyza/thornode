package tron

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/gascontract"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
)

// TestGasUnitContract runs the scanner's fee estimate through the whole
// THORNode <-> bifrost gas-unit contract with the real code on both sides:
// updateFees from the chain parameters, THORNode's GasMgr, and SignTx. The
// scanner posts the whole cost of a TRC20 transfer in sun (size 1); a TRC20
// outbound must set a fee_limit that covers it, up to THORNode's 1.5x buffer,
// or the transfer runs out of energy on chain. TRX transfers set no fee limit.
// !3925 shipped THORNode reading TransactionFeeRate in GetGasUnits units
// while bifrost still posted 1e8 units.
func (s *TronTestSuite) TestGasUnitContract(c *C) {
	usdtContract := "TG3XXyExBkPp9nzdajDZsozEu4BkaSJozs"
	scanner := s.client.tronScanner
	savedScanner := struct {
		chain      common.Chain
		whitelist  map[string]tokenlist.ERC20Token
		refAddress string
		feeQueue   chan common.NetworkFee
	}{scanner.config.ChainID, scanner.whitelist, scanner.refAddress, scanner.globalNetworkFeeQueue}
	savedAPI := s.client.api
	defer func() {
		scanner.config.ChainID = savedScanner.chain
		scanner.whitelist = savedScanner.whitelist
		scanner.refAddress = savedScanner.refAddress
		scanner.globalNetworkFeeQueue = savedScanner.feeQueue
		scanner.currentFee.Store(0)
		s.client.api = savedAPI
	}()

	// 1. bifrost scanner: the network fee for a TRC20 transfer at the mock
	// node's chain parameters and energy estimate
	scanner.config.ChainID = common.TRONChain // as config/default.yaml sets it
	scanner.whitelist = map[string]tokenlist.ERC20Token{usdtContract: s.client.whitelist[usdtContract]}
	scanner.refAddress = "TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW"
	scanner.currentFee.Store(0)
	feeQueue := make(chan common.NetworkFee, 1)
	scanner.globalNetworkFeeQueue = feeQueue
	scanner.updateFees(updateGasInterval)
	c.Assert(feeQueue, HasLen, 1)
	networkFee := <-feeQueue
	estimatedFee := networkFee.TransactionRate // sun for the whole transfer

	// 2. THORNode: GasMgr turns the stored network fee into MaxGas and GasRate
	maxGas, gasRate, err := gascontract.GasDetails(networkFee)
	c.Assert(err, IsNil)

	// 3. bifrost client: sign a TRC20 outbound THORNode scheduled with them,
	// recording the fee_limit it asks the node to build the transfer with
	var mu sync.Mutex
	var feeLimits []uint64
	target, err := url.Parse(s.api.URL)
	c.Assert(err, IsNil)
	proxy := httputil.NewSingleHostReverseProxy(target)
	recorder := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, readErr := io.ReadAll(req.Body)
		c.Check(readErr, IsNil)
		if strings.HasSuffix(req.URL.Path, "/triggersmartcontract") {
			var params struct {
				FeeLimit uint64 `json:"fee_limit"`
			}
			c.Check(json.Unmarshal(body, &params), IsNil)
			mu.Lock()
			feeLimits = append(feeLimits, params.FeeLimit)
			mu.Unlock()
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(rw, req)
	}))
	defer recorder.Close()
	s.client.api = api.NewTronApi(recorder.URL, s.client.config.BlockScanner.HTTPRequestTimeout)

	signed, _, _, err := s.client.SignTx(types.TxOutItem{
		Chain:       common.TRONChain,
		ToAddress:   "TWfHpcbBVTcQaakccHt3d1d41iNgzaPwow",
		VaultPubKey: s.client.localKeyManager.Pubkey(),
		Coins: common.Coins{{
			Asset:    common.Asset{Chain: common.TRONChain, Symbol: "USDT-TG3XXYEXBKPP9NZDAJDZSOZEU4BKASJOZS", Ticker: "USDT"},
			Amount:   cosmos.NewUint(100 * common.One),
			Decimals: 6,
		}},
		Memo:    "OUT:" + strings.Repeat("C", 64),
		MaxGas:  common.Gas{maxGas},
		GasRate: gasRate,
		Height:  1,
	}, 1)
	c.Assert(err, IsNil)
	c.Assert(signed, NotNil)

	// 4. the fee limit covers the scanner's estimate, up to THORNode's 1.5x buffer
	detail := fmt.Sprintf("posted rate %d (size %d), THORNode GasRate %d MaxGas %s",
		networkFee.TransactionRate, networkFee.TransactionSize, gasRate, maxGas)
	mu.Lock()
	defer mu.Unlock()
	c.Assert(feeLimits, HasLen, 1, Commentf("triggersmartcontract calls: %s", detail))
	c.Check(feeLimits[0] >= estimatedFee && feeLimits[0]*2 <= estimatedFee*3, Equals, true,
		Commentf("fee_limit %d sun, want [%d, %d]: %s", feeLimits[0], estimatedFee, estimatedFee*3/2, detail))
}
