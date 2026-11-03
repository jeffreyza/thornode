package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestVaultRegistrationFailurePropagatesError validates the bug where a sidecar
// vault registration failure (HTTP 500) is silently swallowed, causing the
// bifrost to skip the block entirely.
//
// Scenario:
//  1. Sidecar has vault registered under frost key
//  2. Bifrost calls ensureVaultRegistered with eddsa key (bug)
//  3. Sidecar returns 500 ("already registered with different mapping")
//  4. refreshAsgardLookup returns error
//  5. OLD CODE: fetchTransfersByHeight returns (nil, nil) → bifrost advances
//     past block → output at that block is PERMANENTLY LOST
//  6. NEW CODE: fetchTransfersByHeight returns (nil, ErrUnavailableBlock) →
//     bifrost retries the block
//
// This test validates that the registration failure IS an error (step 4),
// proving that the old (nil, nil) return was wrong.
func TestVaultRegistrationFailurePropagatesError(t *testing.T) {
	t.Parallel()

	eddsaKey := testVaultPubKey(t, "eddsa-reg-fail-test")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			// Sidecar rejects: this vault_pub_key was already registered
			// with a different group_key (the frost key's group_key).
			http.Error(
				w,
				`{"error":"vault is already registered with a different mapping"}`,
				http.StatusInternalServerError,
			)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	// ensureVaultRegistered with eddsa key → sidecar returns 500.
	// This error MUST propagate — not be swallowed.
	_, regErr := ensureVaultRegistered(
		testLogger(),
		signer,
		common.XMRChain,
		eddsaKey,
		nil,
		"test",
		0,
	)

	// The registration must fail — this is the error that refreshAsgardLookup
	// propagates. If this were silently swallowed, fetchTransfersByHeight would
	// return (nil, nil) and the bifrost would advance past the block.
	require.Error(t, regErr, "registration failure must propagate as an error")
	assert.Contains(t, regErr.Error(), "register",
		"error should indicate registration failure")
}

// A partial address lookup must not be published when any nonempty FROST vault
// cannot be enrolled; doing so could silently skip an inbound to that vault.
func TestRegistrationFailureFailsAsgardRefreshClosed(t *testing.T) {
	t.Parallel()

	const requestedBirthday = int64(559)
	frostKey := testVaultPubKey(t, "frost-refresh-fail")
	badKey := testVaultPubKey(t, "bad-refresh-fail")
	previousKey := testVaultPubKey(t, "previous-refresh-lookup")

	var registered []sidecar.RegisterVaultReq

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/vaults/register" {
			var req sidecar.RegisterVaultReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			registered = append(registered, req)

			if req.VaultPubKey == badKey.String() {
				http.Error(
					w,
					`{"error":"already registered with different mapping"}`,
					http.StatusInternalServerError,
				)
				return
			}
			_ = json.NewEncoder(w).Encode(sidecar.RegisterVaultResp{
				Registered:         true,
				EffectiveBirthday:  504,
				ExpectedNextHeight: uint64Ptr(600),
			})
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		testLogger(),
		signer,
		&scannerMemoBridge{asgards: ttypes.Vaults{
			{
				PubKey:                 testVaultPubKey(t, "secp-refresh-success"),
				PubKeyFrost:            frostKey,
				VaultCreationHeightXmr: requestedBirthday,
			},
			{
				PubKey:                 testVaultPubKey(t, "secp-refresh-fail"),
				PubKeyFrost:            badKey,
				VaultCreationHeightXmr: requestedBirthday,
			},
		}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	scanner.asgardLookup = map[string]common.PubKey{"previous-address": previousKey}

	refreshErr := scanner.refreshAsgardLookup(requestedBirthday)
	require.Error(t, refreshErr, "one failed FROST enrollment must fail the whole refresh")
	assert.Contains(t, refreshErr.Error(), badKey.String())
	require.Len(t, registered, 2)
	assert.Equal(t, frostKey.String(), registered[0].VaultPubKey)
	assert.Equal(t, requestedBirthday, registered[0].VaultCreationHeight)
	assert.Equal(t, badKey.String(), registered[1].VaultPubKey)

	scanner.asgardMu.RLock()
	defer scanner.asgardMu.RUnlock()
	assert.Equal(t, map[string]common.PubKey{"previous-address": previousKey}, scanner.asgardLookup,
		"failed refresh must not replace the last complete lookup")
}
