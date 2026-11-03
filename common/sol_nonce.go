package common

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/mr-tron/base58"
)

// SolDurableNonceSeed is the create_with_seed seed a vault's durable-nonce account
// is derived with. It must match bifrost's provisioning seed.
const SolDurableNonceSeed = "thornode-durable-nonce-v1"

// solSystemProgramID is the Solana System Program (the owner of a nonce account):
// base58 "11111111111111111111111111111111", i.e. 32 zero bytes.
var solSystemProgramID = make([]byte, 32)

// SolNonceAccountAddress derives a vault's durable-nonce account address from the
// vault's Solana address, exactly as Solana's create_with_seed does:
// base58(sha256(base || seed || owner)) with owner = System Program. It is a pure
// function, so THORChain and every bifrost node derive the same address.
func SolNonceAccountAddress(vaultSolAddress string) (string, error) {
	base, err := base58.Decode(vaultSolAddress)
	if err != nil {
		return "", fmt.Errorf("invalid vault sol address %q: %w", vaultSolAddress, err)
	}
	if len(base) != 32 {
		return "", fmt.Errorf("invalid vault sol address %q: %d bytes", vaultSolAddress, len(base))
	}
	h := sha256.New()
	h.Write(base)
	h.Write([]byte(SolDurableNonceSeed))
	h.Write(solSystemProgramID)
	return base58.Encode(h.Sum(nil)), nil
}

// ValidateSolNonceUpdate checks the structure of an attested durable-nonce update.
// It is only meaningful on a SOL observation.
func ValidateSolNonceUpdate(chain Chain, u *SolNonceUpdate) error {
	if u == nil {
		return nil
	}
	if !chain.Equals(SOLChain) {
		return errors.New("sol nonce update is only valid for SOL observed txs")
	}
	if err := validateSolBase58Key("nonce_account", u.NonceAccount); err != nil {
		return err
	}
	if err := validateSolBase58Key("nonce", u.Nonce); err != nil {
		return err
	}
	if u.Slot <= 0 {
		return errors.New("sol nonce update slot must be positive")
	}
	return nil
}

func validateSolBase58Key(name, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("sol nonce update %s cannot be empty", name)
	}
	b, err := base58.Decode(v)
	if err != nil || len(b) != 32 {
		return fmt.Errorf("sol nonce update %s must be a 32-byte base58 value", name)
	}
	return nil
}

// SolNonceUpdatesEqual reports exact equality; both absent is equal. There is no
// "absent matches anything" rule: the committed nonce must be strictly agreed.
func SolNonceUpdatesEqual(a, b *SolNonceUpdate) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.NonceAccount == b.NonceAccount && a.Nonce == b.Nonce && a.Slot == b.Slot
}

// solNonceUniqueHash folds an attested durable-nonce update into the attestation
// gossip key, so observers carrying different values land in different buckets
// instead of the first one seen shutting the rest out.
func solNonceUniqueHash(base string, chain Chain, u *SolNonceUpdate) string {
	if u == nil || !chain.Equals(SOLChain) {
		return base
	}
	return fmt.Sprintf("%X", sha256.Sum256([]byte(base+"|sol_nonce|"+u.NonceAccount+"|"+u.Nonce+"|"+fmt.Sprint(u.Slot))))
}
