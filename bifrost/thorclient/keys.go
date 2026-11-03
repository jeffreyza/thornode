package thorclient

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sync/atomic"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptokeysed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptokeyssecp256k1 "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
)

const (
	// folder name for thorchain thorcli
	thorchainCliFolderName = `.thornode`
)

type Keyring interface {
	ckeys.Keyring
}

// Keys manages all the keys used by thorchain
type Keys struct {
	signerName string
	password   string // TODO this is a bad way , need to fix it
	kb         Keyring

	// signerInfo caches the signer's keyring record after the first
	// successful read; reading it from the file keyring decrypts the entry.
	signerInfo atomic.Pointer[ckeys.Record]
}

// NewKeysWithKeybase create a new instance of Keys
func NewKeysWithKeybase(kb ckeys.Keyring, name, password string) *Keys {
	return &Keys{
		signerName: name,
		password:   password,
		kb:         kb,
	}
}

// GetKeyringKeybase return keyring and key info
func GetKeyringKeybase(chainHomeFolder, signerName, password string) (ckeys.Keyring, *ckeys.Record, error) {
	if len(signerName) == 0 {
		return nil, nil, fmt.Errorf("signer name is empty")
	}
	if len(password) == 0 {
		return nil, nil, fmt.Errorf("password is empty")
	}

	buf := bytes.NewBufferString(password)
	// the library used by keyring is using ReadLine , which expect a new line
	buf.WriteByte('\n')
	kb, err := getKeybase(chainHomeFolder, buf)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get keybase,err:%w", err)
	}
	// the keyring library which used by cosmos sdk , will use interactive terminal if it detect it has one
	// this will temporary trick it think there is no interactive terminal, thus will read the password from the buffer provided
	oldStdIn := os.Stdin
	defer func() {
		os.Stdin = oldStdIn
	}()
	os.Stdin = nil
	si, err := kb.Key(signerName)
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get signer info(%s): %w", signerName, err)
	}
	return kb, si, nil
}

// getKeybase will create an instance of Keybase
func getKeybase(thorchainHome string, reader io.Reader) (ckeys.Keyring, error) {
	cliDir := thorchainHome
	if len(thorchainHome) == 0 {
		usr, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("fail to get current user,err:%w", err)
		}
		cliDir = filepath.Join(usr.HomeDir, thorchainCliFolderName)
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	return ckeys.New(sdk.KeyringServiceName(), ckeys.BackendFile, cliDir, reader, cdc)
}

// GetSignerInfo return signer info. The record is read from the keyring once
// and then cached: every caller only reads its address or public key, and a
// file keyring decrypts (PBKDF2) the entry on every read.
func (k *Keys) GetSignerInfo() *ckeys.Record {
	if record := k.signerInfo.Load(); record != nil {
		return record
	}
	record, err := k.kb.Key(k.signerName)
	if err != nil {
		panic(err)
	}
	k.signerInfo.Store(record)
	return record
}

// privKeyExporter is implemented by the SDK keyring. Its ExportPrivKeyArmor is
// ExportPrivateKeyObject plus an argon2 encryption that unarmoring only undoes.
type privKeyExporter interface {
	ExportPrivateKeyObject(uid string) (cryptotypes.PrivKey, error)
}

// exportPrivKey return the private key of the given key name
func (k *Keys) exportPrivKey(name string) (cryptotypes.PrivKey, error) {
	if exporter, ok := k.kb.(privKeyExporter); ok {
		return exporter.ExportPrivateKeyObject(name)
	}
	privKeyArmor, err := k.kb.ExportPrivKeyArmor(name, k.password)
	if err != nil {
		return nil, err
	}
	priKey, _, err := crypto.UnarmorDecryptPrivKey(privKeyArmor, k.password)
	if err != nil {
		return nil, fmt.Errorf("fail to unarmor private key: %w", err)
	}
	return priKey, nil
}

// GetPrivateKey return the ecdsa private key
func (k *Keys) GetPrivateKey() (*cryptokeyssecp256k1.PrivKey, error) {
	priKey, err := k.exportPrivKey(k.signerName)
	if err != nil {
		return nil, err
	}
	secpKey, ok := priKey.(*cryptokeyssecp256k1.PrivKey)
	if !ok {
		return nil, fmt.Errorf("fail to cast private key to secp256k1 private key")
	}
	return secpKey, nil
}

// GetPrivateKeyEDDSA return the eddsa private key
func (k *Keys) GetPrivateKeyEDDSA() (*cryptokeysed25519.PrivKey, error) {
	signerNameEDDSA := ed25519.SignerNameEDDSA(k.signerName)
	priKey, err := k.exportPrivKey(signerNameEDDSA)
	if err != nil {
		return nil, err
	}
	eddsaKey, ok := priKey.(*cryptokeysed25519.PrivKey)
	if !ok {
		return nil, fmt.Errorf("fail to cast private key to eddsa private key")
	}
	return eddsaKey, nil
}

// GetKeybase return the keybase
func (k *Keys) GetKeybase() ckeys.Keyring {
	return k.kb
}
