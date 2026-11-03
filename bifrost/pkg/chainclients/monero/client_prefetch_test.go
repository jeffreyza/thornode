package monero

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestNewClientRejectsOutOfOrderXMRPrefetch(t *testing.T) {
	_, err := NewClient(nil, config.BifrostChainConfiguration{
		ChainID: common.XMRChain,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			PrefetchBlocks: 2,
		},
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "prefetch") {
		t.Fatalf("expected XMR prefetch rejection, got %v", err)
	}
}
