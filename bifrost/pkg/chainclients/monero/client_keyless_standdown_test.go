package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func newVaultKeysClient(t *testing.T, vaults []sidecar.VaultKey, status int) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/keys" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "sidecar unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Vaults []sidecar.VaultKey `json:"vaults"`
		}{Vaults: vaults})
	}))
	t.Cleanup(server.Close)

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	return &Client{
		logger:  log.Logger,
		chain:   common.XMRChain,
		sidecar: frostSigner,
	}
}

func TestSidecarHasVaultKeys(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	otherPubKey, _ := testVaultPubKeyAndAddress(t)
	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatalf("group key hex: %v", err)
	}

	tests := []struct {
		name    string
		vaults  []sidecar.VaultKey
		status  int
		want    bool
		wantErr bool
	}{
		{
			name:   "bech32 keyed vault with keys",
			vaults: []sidecar.VaultKey{{VaultPubKey: vaultPubKey.String(), GroupKey: groupKeyHex, HasFrostKeys: true, Signable: true}},
			status: http.StatusOK,
			want:   true,
		},
		{
			name:   "bech32 keyed vault without keys",
			vaults: []sidecar.VaultKey{{VaultPubKey: vaultPubKey.String(), GroupKey: groupKeyHex, HasFrostKeys: false}},
			status: http.StatusOK,
			want:   false,
		},
		{
			name:   "hex keyed vault with keys",
			vaults: []sidecar.VaultKey{{VaultPubKey: groupKeyHex, GroupKey: groupKeyHex, HasFrostKeys: true, Signable: true}},
			status: http.StatusOK,
			want:   true,
		},
		{
			name:   "vault unknown to sidecar",
			vaults: []sidecar.VaultKey{{VaultPubKey: otherPubKey.String(), HasFrostKeys: true}},
			status: http.StatusOK,
			want:   false,
		},
		{
			name:    "sidecar error is surfaced for fail closed call site",
			status:  http.StatusServiceUnavailable,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newVaultKeysClient(t, tc.vaults, tc.status)
			got, err := c.sidecarHasVaultKeys(vaultPubKey)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("sidecarHasVaultKeys: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %t, got %t", tc.want, got)
			}
		})
	}
}

func TestSidecarHasVaultKeysRequiresSidecar(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	c := &Client{logger: log.Logger, chain: common.XMRChain}
	if _, err := c.sidecarHasVaultKeys(vaultPubKey); err == nil {
		t.Fatal("expected error when sidecar is not configured")
	}
}
