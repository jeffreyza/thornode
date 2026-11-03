package ethereum

import (
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// within fails the test if f does not return in time.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return", what)
	}
}

// TestExtractTxsNonPositiveConcurrency: a semaphore of weight 0 (or less)
// never grants Acquire(1), so every tx worker used to wait forever.
func TestExtractTxsNonPositiveConcurrency(t *testing.T) {
	// contract creations return right after taking the semaphore
	var txs etypes.Transactions
	for i := 0; i < 3; i++ {
		txs = append(txs, etypes.NewContractCreation(uint64(i), big.NewInt(0), 53000, big.NewInt(1), nil))
	}
	block := etypes.NewBlockWithHeader(&etypes.Header{Number: big.NewInt(7)}).
		WithBody(etypes.Body{Transactions: txs})
	// the block receipts fetch fails, so extractTxs falls back to per-tx lookups
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	ethClient, err := ethclient.Dial(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, concurrency := range []int64{0, -1} {
		e := &ETHScanner{
			cfg:    config.BifrostBlockScannerConfiguration{Concurrency: concurrency, HTTPRequestTimeout: time.Second},
			client: ethClient,
		}
		within(t, 10*time.Second, "extractTxs", func() {
			txIn, err := e.extractTxs(block)
			if err != nil || len(txIn.TxArray) != 0 {
				t.Errorf("concurrency %d: got %v, %v", concurrency, txIn, err)
			}
		})
	}
}

type reconstructPubKeyValidator struct {
	*pubkeymanager.MockPoolAddressValidator
	pubkeys common.PubKeys
}

func (v reconstructPubKeyValidator) GetAlgoPubKeys(common.SigningAlgo, bool) common.PubKeys {
	return v.pubkeys
}

// TestReconstructLiabilitiesNonPositiveConcurrency: a 0 concurrency used to
// block on the first semaphore send (signing never became ready) and a
// negative one panicked making the channel.
func TestReconstructLiabilitiesNonPositiveConcurrency(t *testing.T) {
	for _, concurrency := range []int{0, -1} {
		stop := make(chan struct{})
		close(stop) // vault workers return at once
		c := &Client{
			pubkeyMgr: reconstructPubKeyValidator{
				MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
				pubkeys:                  common.PubKeys{types.GetRandomPubKey(), types.GetRandomPubKey()},
			},
			wg:       &sync.WaitGroup{},
			stopchan: stop,
		}
		c.cfg.EVM.ReconstructLiabilitiesConcurrency = concurrency
		c.wg.Add(1)
		within(t, 10*time.Second, "reconstructLiabilities", c.reconstructLiabilities)
	}
}
