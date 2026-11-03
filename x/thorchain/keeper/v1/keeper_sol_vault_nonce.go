package keeperv1

import (
	"crypto/sha256"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// SetSolVaultNonce saves a Solana vault's committed durable nonce.
func (k KVStore) SetSolVaultNonce(ctx cosmos.Context, record SolVaultNonce) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixSolVaultNonce, record.VaultPubKey.String())
	store.Set(key, k.cdc.MustMarshal(&record))
}

// GetSolVaultNonce returns a Solana vault's committed durable nonce. found is false
// (and the record empty) when the vault has none.
func (k KVStore) GetSolVaultNonce(ctx cosmos.Context, vaultPubKey common.PubKey) (SolVaultNonce, bool, error) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixSolVaultNonce, vaultPubKey.String())
	var record SolVaultNonce
	if !store.Has(key) {
		return record, false, nil
	}
	if err := k.cdc.Unmarshal(store.Get(key), &record); err != nil {
		return SolVaultNonce{}, false, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, true, nil
}

// GetSolVaultNonceIterator iterates over every committed Solana vault durable nonce
// (e.g. for hard-fork GenesisState export).
func (k KVStore) GetSolVaultNonceIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixSolVaultNonce)
}

// solNonceAttestVoterKey hashes the attested value, slot included (votes must be
// identical to count together): nonce values and tx ids are case-sensitive base58,
// and store keys are case-folded.
func solNonceAttestVoterKey(vaultPubKey common.PubKey, nonceAccount, nonce string, lastTx common.TxID, slot int64) string {
	return fmt.Sprintf("%X", sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d", vaultPubKey, nonceAccount, nonce, lastTx, slot))))
}

// SetSolNonceAttestVoter saves the votes for one re-attested durable-nonce value.
func (k KVStore) SetSolNonceAttestVoter(ctx cosmos.Context, voter SolNonceAttestVoter) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixSolNonceAttestVoter, solNonceAttestVoterKey(voter.VaultPubKey, voter.NonceAccount, voter.Nonce, voter.LastTx, voter.Slot))
	store.Set(key, k.cdc.MustMarshal(&voter))
}

// GetSolNonceAttestVoter returns the votes for one re-attested durable-nonce value
// (an empty voter carrying the value when there are none yet).
func (k KVStore) GetSolNonceAttestVoter(ctx cosmos.Context, vaultPubKey common.PubKey, nonceAccount, nonce string, lastTx common.TxID, slot int64) (SolNonceAttestVoter, error) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixSolNonceAttestVoter, solNonceAttestVoterKey(vaultPubKey, nonceAccount, nonce, lastTx, slot))
	voter := SolNonceAttestVoter{VaultPubKey: vaultPubKey, NonceAccount: nonceAccount, Nonce: nonce, LastTx: lastTx, Slot: slot}
	if !store.Has(key) {
		return voter, nil
	}
	if err := k.cdc.Unmarshal(store.Get(key), &voter); err != nil {
		return SolNonceAttestVoter{}, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", voter, key), err)
	}
	return voter, nil
}
