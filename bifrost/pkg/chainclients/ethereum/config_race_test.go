package ethereum

import (
	"strings"
	"sync"
	"testing"

	"github.com/cometbft/cometbft/crypto/secp256k1"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestMocknetThorConfigConcurrentPubKeyEncoding(t *testing.T) {
	setupMocknetThorConfig()
	pubkey := secp256k1.GenPrivKey().PubKey()

	// A request from a previous test can still be encoding a public key when
	// the next test sets up its mocknet configuration.
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for range 1000 {
			setupMocknetThorConfig()
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for range 1000 {
			encoded, err := common.NewPubKeyFromCrypto(pubkey)
			if err != nil {
				t.Errorf("encode public key: %v", err)
				return
			}
			if !strings.HasPrefix(encoded.String(), "tthorpub1") {
				t.Errorf("expected mocknet public key prefix, got %s", encoded)
				return
			}
		}
	}()
	close(start)
	workers.Wait()
}
