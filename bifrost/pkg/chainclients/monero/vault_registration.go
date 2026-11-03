package monero

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
)

type vaultSpendKeyProvider interface {
	spendKeyHexFor(vaultPubKey common.PubKey) (string, bool)
}

func groupKeyHexFromVaultPubKey(vaultPubKey common.PubKey) (string, error) {
	if vaultPubKey.IsEmpty() {
		return "", fmt.Errorf("vault pubkey is empty")
	}

	pk, err := vaultPubKey.Decode()
	if err != nil {
		return "", fmt.Errorf("fail to decode vault pubkey %s: %w", vaultPubKey.String(), err)
	}
	raw := pk.Bytes()
	if len(raw) != 32 {
		return "", fmt.Errorf("unexpected Ed25519 vault pubkey length %d", len(raw))
	}

	return hex.EncodeToString(raw), nil
}

func vaultPubKeyFromGroupKeyHex(groupKeyHex string) (common.PubKey, error) {
	groupKeyHex = strings.TrimSpace(groupKeyHex)
	if groupKeyHex == "" {
		return common.EmptyPubKey, fmt.Errorf("group key is empty")
	}

	raw, err := hex.DecodeString(groupKeyHex)
	if err != nil {
		return common.EmptyPubKey, fmt.Errorf("fail to decode group key %q: %w", groupKeyHex, err)
	}
	if len(raw) != 32 {
		return common.EmptyPubKey, fmt.Errorf("unexpected group key length %d", len(raw))
	}

	return common.NewPubKeyFromCrypto(cometed25519.PubKey(raw))
}

// ensureVaultRegistered registers the given vault with the sidecar, attaching
// spend-key material if the local node owns the vault alone, or a deterministic
// view key otherwise.
func ensureVaultRegistered(
	logger zerolog.Logger,
	client *sidecar.Client,
	chain common.Chain,
	vaultPubKey common.PubKey,
	bootstrap vaultSpendKeyProvider,
	source string,
	birthday int64,
) (common.Address, error) {
	return ensureVaultRegisteredContext(context.Background(), logger, client, chain, vaultPubKey, bootstrap, source, birthday)
}

func ensureVaultRegisteredContext(
	ctx context.Context,
	logger zerolog.Logger,
	client *sidecar.Client,
	chain common.Chain,
	vaultPubKey common.PubKey,
	bootstrap vaultSpendKeyProvider,
	source string,
	birthday int64,
) (common.Address, error) {
	if client == nil {
		return common.NoAddress, fmt.Errorf("sidecar client is not initialized")
	}
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, fmt.Errorf("vault pubkey is empty")
	}

	address, err := vaultPubKey.GetAddress(chain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to derive %s vault address for %s: %w", chain, vaultPubKey.String(), err)
	}
	if address.IsEmpty() {
		return common.NoAddress, fmt.Errorf("derived %s vault address is empty for %s", chain, vaultPubKey.String())
	}

	groupKeyHex, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		return common.NoAddress, err
	}

	req := sidecar.RegisterVaultReq{
		VaultPubKey:         vaultPubKey.String(),
		GroupKey:            groupKeyHex,
		Address:             address.String(),
		Source:              source,
		VaultCreationHeight: birthday,
	}
	if bootstrap != nil {
		if spendKeyHex, ok := bootstrap.spendKeyHexFor(vaultPubKey); ok {
			req.SpendKeyHex = strings.TrimSpace(spendKeyHex)
		}
	}

	// Non-member nodes lack a spend key but can still independently scan for
	// incoming stealth outputs using the deterministic view key.
	if req.SpendKeyHex == "" {
		if vk, vkErr := common.XMRViewKeyHex(); vkErr == nil {
			req.ViewKeyHex = vk
		}
	}

	if _, err = client.RegisterVaultContext(ctx, req); err != nil {
		logger.Debug().
			Err(err).
			Str("vault_pubkey", vaultPubKey.String()).
			Str("address", address.String()).
			Msg("failed to register xmr vault with sidecar")
		return common.NoAddress, err
	}

	return address, nil
}
