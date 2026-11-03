package thorclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	btypes "gitlab.com/thorchain/thornode/v3/bifrost/blockscanner/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
)

var ErrNotFound = fmt.Errorf("not found")

type QueryKeysign struct {
	Keysign   types.TxOut `json:"keysign"`
	Signature string      `json:"signature"`
}

// GetKeysign retrieves txout from this block height from thorchain
func (b *thorchainBridge) GetKeysign(blockHeight int64, pk string) (types.TxOut, error) {
	return b.getKeysign(blockHeight, pk, b.getWithPath)
}

// GetKeysignWithContext retrieves a txout without allowing the request or its
// retries to outlive ctx.
func (b *thorchainBridge) GetKeysignWithContext(ctx context.Context, blockHeight int64, pk string) (types.TxOut, error) {
	return b.getKeysign(blockHeight, pk, func(path string) ([]byte, int, error) {
		return b.getWithPathWithContext(ctx, path)
	})
}

func (b *thorchainBridge) getKeysign(
	blockHeight int64,
	pk string,
	get func(string) ([]byte, int, error),
) (types.TxOut, error) {
	path := fmt.Sprintf("%s/%d", KeysignEndpoint, blockHeight)
	if pk != "" {
		// Keep the pubkey-specific endpoint for the normal signing path. XMR
		// peer spent-ref validation passes an empty pk so newly churned-in
		// validators can load the full public txout by height.
		path = fmt.Sprintf("%s/%s", path, pk)
	}
	body, status, err := get(path)
	if err != nil {
		if status == http.StatusNotFound {
			return types.TxOut{}, btypes.ErrUnavailableBlock
		}
		return types.TxOut{}, fmt.Errorf("failed to get tx from a block height: %w", err)
	}
	var query QueryKeysign
	if err = json.Unmarshal(body, &query); err != nil {
		return types.TxOut{}, fmt.Errorf("failed to unmarshal TxOut: %w", err)
	}
	// there is no txout item , thus no need to validate signature either
	if len(query.Keysign.TxArray) == 0 {
		return query.Keysign, nil
	}
	if query.Signature == "" {
		return types.TxOut{}, errors.New("invalid keysign signature: empty")
	}
	buf, err := json.Marshal(query.Keysign)
	if err != nil {
		return types.TxOut{}, fmt.Errorf("fail to marshal keysign block to json: %w", err)
	}
	pubKey, err := b.keys.GetSignerInfo().GetPubKey()
	if err != nil {
		return types.TxOut{}, fmt.Errorf("fail to get signer pub key: %w", err)
	}
	s, err := base64.StdEncoding.DecodeString(query.Signature)
	if err != nil {
		return types.TxOut{}, errors.New("invalid keysign signature: cannot decode signature")
	}
	if !pubKey.VerifySignature(buf, s) {
		return types.TxOut{}, errors.New("invalid keysign signature: bad signature")
	}

	// ensure the block height received is the one requested. Without this
	// check, an attacker could use a replay attack to steal funds
	if query.Keysign.Height != blockHeight {
		return types.TxOut{}, fmt.Errorf("invalid keysign: block height mismatch (%d vs %d)", query.Keysign.Height, blockHeight)
	}

	return query.Keysign, nil
}
