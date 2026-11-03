package evm

import (
	"sync"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	thorcommon "gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type reconstructPubKeyValidator struct {
	*pubkeymanager.MockPoolAddressValidator
	pubkeys thorcommon.PubKeys
}

func (v reconstructPubKeyValidator) GetAlgoPubKeys(thorcommon.SigningAlgo, bool) thorcommon.PubKeys {
	return v.pubkeys
}

// TestReconstructLiabilitiesNonPositiveConcurrency: a 0 concurrency used to
// block on the first semaphore send (signing never became ready) and a
// negative one panicked making the channel.
func TestReconstructLiabilitiesNonPositiveConcurrency(t *testing.T) {
	setupMocknetThorConfig()
	for _, concurrency := range []int{0, -1} {
		stop := make(chan struct{})
		close(stop) // vault workers return at once
		c := &EVMClient{
			cfg: config.BifrostChainConfiguration{ChainID: thorcommon.AVAXChain},
			pubkeyMgr: reconstructPubKeyValidator{
				MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
				pubkeys:                  thorcommon.PubKeys{types.GetRandomPubKey(), types.GetRandomPubKey()},
			},
			wg:       &sync.WaitGroup{},
			stopchan: stop,
		}
		c.cfg.EVM.ReconstructLiabilitiesConcurrency = concurrency
		c.wg.Add(1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			c.reconstructLiabilities()
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("concurrency %d: reconstructLiabilities did not return", concurrency)
		}
	}
}
