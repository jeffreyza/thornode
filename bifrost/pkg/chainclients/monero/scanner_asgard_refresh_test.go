package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"

	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestRefreshAsgardRegistersFrostKeyAtNextScanHeight validates that
// refreshAsgardLookup registers the frost key at Bifrost's next scan height.
//
// The bug: if the eddsa key is registered, the sidecar returns 500 (conflict
// with the existing frost key registration). refreshAsgardLookup returns an
// error. fetchTransfersByHeight previously returned (nil, nil) on this error,
// causing the bifrost to silently advance past the block without scanning it.
// Outputs in that block are lost forever.
//
// This test verifies both registration inputs received by the sidecar.
func TestRefreshAsgardRegistersFrostKeyAtNextScanHeight(t *testing.T) {
	t.Parallel()

	const (
		vaultCreationHeight = int64(568)
		nextScanHeight      = int64(542)
	)
	frostKey := testVaultPubKey(t, "frost-scanner-reg")
	eddsaKey := testVaultPubKey(t, "eddsa-scanner-reg")

	var registered []sidecar.RegisterVaultReq

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			var req sidecar.RegisterVaultReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			registered = append(registered, req)

			if req.VaultPubKey == eddsaKey.String() {
				t.Errorf("BUG: eddsa key %s was sent to sidecar register-vault — "+
					"this will cause a 500 conflict and block the scanner", eddsaKey.String())
				http.Error(w, `{"error":"already registered with different mapping"}`, http.StatusInternalServerError)
				return
			}

			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(uint64(req.VaultCreationHeight)))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKey:                 testVaultPubKey(t, "ecdsa-scanner-reg"),
				PubKeyEddsa:            eddsaKey,
				PubKeyFrost:            frostKey,
				VaultCreationHeightXmr: vaultCreationHeight,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	require.NoError(t, scanner.refreshAsgardLookup(nextScanHeight), "frost key registration should succeed")
	require.Len(t, registered, 1)

	assert.Equal(t, frostKey.String(), registered[0].VaultPubKey,
		"only the frost key should be registered on the sidecar")
	assert.Equal(t, nextScanHeight, registered[0].VaultCreationHeight,
		"joining scanner must not start at vault height 568 while Bifrost still requests 542")
}

func TestRefreshAsgardSkipsVaultWithoutPubKeyFrost(t *testing.T) {
	t.Parallel()

	eddsaKey := testVaultPubKey(t, "eddsa-scanner-fallback")

	var registerCalls int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			registerCalls++
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				ExpectedNextHeight: uint64Ptr(0),
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{{
				PubKey:      testVaultPubKey(t, "ecdsa-scanner-fallback"),
				PubKeyEddsa: eddsaKey,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	require.NoError(t, scanner.refreshAsgardLookup(0), "vaults without frost keys should be ignored, not registered")

	assert.Equal(t, 0, registerCalls, "eddsa-only xmr vaults should not hit sidecar registration")
}

func TestFetchTxsObservesInactiveFrostVaultFromPubKeys(t *testing.T) {
	t.Parallel()

	secpKey := testVaultPubKey(t, "inactive-xmr-secp")
	frostKey, frostAddr := testVaultPubKeyAndAddress(t)
	txID := strings.Repeat("9", 64)

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{{
			TxID:        txID,
			Address:     frostAddr.String(),
			VaultPubKey: frostKey.String(),
			Amount:      uint64(2 * moneroAtomicToThorchain),
			Height:      100,
			Note:        "SWAP:BTC.BTC:bc1qrefundtest",
		}},
		nil,
		[]string{frostAddr.String()},
	)
	defer frostServer.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, frostServer.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{},
			pubkeyVaults: ttypes.Vaults{{
				PubKey:      secpKey,
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_InactiveVault,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 150)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	assert.Equal(t, frostKey, txIn.TxArray[0].ObservedVaultPubKey)
	assert.Equal(t, frostAddr.String(), txIn.TxArray[0].To)
}

func TestFetchTxsObservesInactiveFrostVaultByAddressLookup(t *testing.T) {
	t.Parallel()

	secpKey := testVaultPubKey(t, "inactive-xmr-secp-by-address")
	frostKey, frostAddr := testVaultPubKeyAndAddress(t)
	txID := strings.Repeat("8", 64)

	frostServer := newFrostTransfersServer(
		t,
		[]sidecar.Transfer{{
			TxID:    txID,
			Address: frostAddr.String(),
			Amount:  uint64(2 * moneroAtomicToThorchain),
			Height:  100,
			Note:    "SWAP:BTC.BTC:bc1qrefundtest",
		}},
		nil,
		[]string{frostAddr.String()},
	)
	defer frostServer.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, frostServer.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		log.Logger,
		signer,
		&scannerMemoBridge{
			asgards: ttypes.Vaults{},
			pubkeyVaults: ttypes.Vaults{{
				PubKey:      secpKey,
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_InactiveVault,
			}},
		},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)

	txIn, err := scanner.FetchTxs(100, 150)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	assert.Equal(t, frostKey, txIn.TxArray[0].ObservedVaultPubKey)
	assert.Equal(t, frostAddr.String(), txIn.TxArray[0].To)
}
