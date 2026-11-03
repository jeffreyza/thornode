package types

import (
	"fmt"
)

// String implements fmt.Stringer
func (m *SolVaultNonce) String() string {
	return fmt.Sprintf("vault:%s nonce_account:%s nonce:%s slot:%d updated_height:%d set_by_tx:%s",
		m.VaultPubKey, m.NonceAccount, m.Nonce, m.Slot, m.UpdatedHeight, m.SetByTx)
}

// IsEmpty reports whether no durable nonce is recorded.
func (m *SolVaultNonce) IsEmpty() bool {
	return m == nil || m.Nonce == "" || m.NonceAccount == ""
}

// Valid checks a committed record (e.g. one imported from genesis).
func (m *SolVaultNonce) Valid() error {
	if m.VaultPubKey.IsEmpty() {
		return fmt.Errorf("sol vault nonce: empty vault pubkey")
	}
	if m.IsEmpty() {
		return fmt.Errorf("sol vault nonce for %s: empty nonce or nonce account", m.VaultPubKey)
	}
	if m.Slot <= 0 {
		return fmt.Errorf("sol vault nonce for %s: invalid slot %d", m.VaultPubKey, m.Slot)
	}
	return nil
}

// String implements fmt.Stringer
func (m *SolNonceAttestVoter) String() string {
	return fmt.Sprintf("vault:%s nonce_account:%s nonce:%s last_tx:%s slot:%d signers:%d committed_height:%d",
		m.VaultPubKey, m.NonceAccount, m.Nonce, m.LastTx, m.Slot, len(m.Signers), m.CommittedHeight)
}

// Add records signer's attestation once; it reports whether it was new.
func (m *SolNonceAttestVoter) Add(signer string) bool {
	for _, s := range m.Signers {
		if s == signer {
			return false
		}
	}
	m.Signers = append(m.Signers, signer)
	return true
}
