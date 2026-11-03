package monero

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestFetchMemPoolDisabledPreservesMinedScanning(t *testing.T) {
	t.Parallel()

	txID := "1111111111111111111111111111111111111111111111111111111111111111"
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	destinationAddress := common.XMRChecksummedDummyAddress('8')

	frostServer := newFrostTransfersServer(
		t,
		nil,
		[]sidecar.Transfer{
			{
				TxID:        txID,
				Address:     destinationAddress,
				FromAddress: vaultAddress,
				VaultPubKey: vaultPubKey.String(),
				Amount:      200000,
				Fee:         10000,
				Height:      100,
			},
		},
		[]string{vaultAddress},
	)
	defer frostServer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mempool") == "true" {
			t.Error("disabled mempool scanner must not call the sidecar mempool endpoint")
		}
		frostServer.Config.Handler.ServeHTTP(w, r)
	}))
	defer server.Close()

	cfg := config.BifrostChainConfiguration{}
	frostSigner, err := sidecar.NewClientWithEndpoint(cfg, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	bridge := &scannerMemoBridge{
		asgards: ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}},
	}

	scanner := NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)

	first, err := scanner.FetchMemPool(100)
	if err != nil {
		t.Fatalf("first mempool fetch: %v", err)
	}
	if len(first.TxArray) != 0 {
		t.Fatalf("expected no mempool observations, got %d", len(first.TxArray))
	}

	confirmed, err := scanner.FetchTxs(100, 100)
	if err != nil {
		t.Fatalf("confirmed fetch: %v", err)
	}
	if len(confirmed.TxArray) != 1 {
		t.Fatalf("expected 1 confirmed tx item, got %d", len(confirmed.TxArray))
	}

	afterMined, err := scanner.FetchMemPool(102)
	if err != nil {
		t.Fatalf("mempool fetch after mined scan: %v", err)
	}
	if len(afterMined.TxArray) != 0 {
		t.Fatalf("expected no mempool observations after mined scan, got %d", len(afterMined.TxArray))
	}
}
