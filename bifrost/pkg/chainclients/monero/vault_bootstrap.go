package monero

import (
	"encoding/hex"
	"fmt"
	"strings"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

// vaultBootstrap carries the local node's Ed25519 spend key so we can register
// a single-member vault with the sidecar without running DKG. If the local
// node is not the sole member, the view-key-only path is used instead.
type vaultBootstrap struct {
	vaultPubKey common.PubKey
	spendKeyHex string
}

func deriveVaultBootstrap(thorKeys *thorclient.Keys) (*vaultBootstrap, error) {
	if thorKeys == nil {
		return nil, nil
	}

	privKey, err := thorKeys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get local ed25519 private key: %w", err)
	}

	privBytes := privKey.Bytes()
	if len(privBytes) < 32 {
		return nil, fmt.Errorf("invalid local ed25519 private key length %d", len(privBytes))
	}

	tmPubKey, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to convert local ed25519 pubkey: %w", err)
	}
	vaultPubKey, err := common.NewPubKeyFromCrypto(tmPubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to encode local ed25519 pubkey: %w", err)
	}

	return &vaultBootstrap{
		vaultPubKey: vaultPubKey,
		spendKeyHex: hex.EncodeToString(privBytes[:32]),
	}, nil
}

func (b *vaultBootstrap) spendKeyHexFor(vaultPubKey common.PubKey) (string, bool) {
	if b == nil || b.vaultPubKey.IsEmpty() || vaultPubKey.IsEmpty() {
		return "", false
	}
	if !b.vaultPubKey.Equals(vaultPubKey) {
		return "", false
	}

	spendKeyHex := strings.TrimSpace(b.spendKeyHex)
	if spendKeyHex == "" {
		return "", false
	}

	return spendKeyHex, true
}
