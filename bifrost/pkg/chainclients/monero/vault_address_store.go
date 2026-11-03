package monero

import (
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const vaultAddressPrefix = "xmr-vault-address-v2-"

type VaultAddressStore struct {
	db *leveldb.DB
}

func NewVaultAddressStore(db *leveldb.DB) *VaultAddressStore {
	return &VaultAddressStore{db: db}
}

func (s *VaultAddressStore) Set(vaultPubKey, address string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("vault address store is not initialized")
	}
	if vaultPubKey == "" || address == "" {
		return fmt.Errorf("vault pubkey and address cannot be empty")
	}
	return s.db.Put([]byte(vaultAddressPrefix+vaultPubKey), []byte(address), nil)
}

func (s *VaultAddressStore) Get(vaultPubKey string) (string, error) {
	if s == nil || s.db == nil {
		return "", fmt.Errorf("vault address store is not initialized")
	}
	if vaultPubKey == "" {
		return "", fmt.Errorf("vault pubkey cannot be empty")
	}
	value, err := s.db.Get([]byte(vaultAddressPrefix+vaultPubKey), nil)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func (s *VaultAddressStore) List() (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("vault address store is not initialized")
	}
	result := make(map[string]string)
	iter := s.db.NewIterator(util.BytesPrefix([]byte(vaultAddressPrefix)), nil)
	defer iter.Release()

	for iter.Next() {
		key := string(iter.Key())
		if len(key) <= len(vaultAddressPrefix) {
			continue
		}
		vaultPubKey := key[len(vaultAddressPrefix):]
		result[vaultPubKey] = string(iter.Value())
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}
