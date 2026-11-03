package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-peerstore/addr"
	maddr "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/curvelock"
)

// KeygenLocalState is a structure used to represent the data we saved locally for different keygen
type KeygenLocalState struct {
	PubKey          string   `json:"pub_key"`
	LocalData       []byte   `json:"local_data"`
	ParticipantKeys []string `json:"participant_keys"` // the participant of last key gen
	LocalPartyKey   string   `json:"local_party_key"`
}

// keygenLocalStateV1 is the pre-EDDSA format necessary for migration.
// TODO: remove after network has churned with new format.
type keygenLocalStateV1 struct {
	PubKey          string                    `json:"pub_key"`
	LocalData       keygen.LocalPartySaveData `json:"local_data"`
	ParticipantKeys []string                  `json:"participant_keys"`
	LocalPartyKey   string                    `json:"local_party_key"`
}

// UnmarshalJSON implements custom JSON unmarshaling to handle format migration.
// TODO: remove after network has churned with new format.
func (kls *KeygenLocalState) UnmarshalJSON(data []byte) error {
	// attempt unmarshal as the new format
	type Alias KeygenLocalState
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(kls),
	}

	if err := json.Unmarshal(data, aux); err == nil {
		return nil
	}

	// if that fails, attempt unmarshal as the old format. Pin the global curve
	// to secp256k1 under the shared curvelock so v3's ECPoint.UnmarshalJSON
	// cannot race with a concurrent EdDSA load flipping tss-lib's global mid-
	// unmarshal — legacy keyshares lack the embedded "Curve" field and fall
	// back to tss.EC().
	var oldState keygenLocalStateV1
	if err := curvelock.WithCurve(tss.S256(), func() error {
		return json.Unmarshal(data, &oldState)
	}); err != nil {
		return fmt.Errorf("failed to unmarshal both new and old KeygenLocalState formats: %w", err)
	}

	// marshal the local data to bytes for the new format
	localDataBytes, err := json.Marshal(oldState.LocalData)
	if err != nil {
		return fmt.Errorf("failed to marshal LocalPartySaveData: %w", err)
	}

	kls.PubKey = oldState.PubKey
	kls.LocalData = localDataBytes
	kls.ParticipantKeys = oldState.ParticipantKeys
	kls.LocalPartyKey = oldState.LocalPartyKey

	return nil
}

// LocalStateManager provide necessary methods to manage the local state, save it , and read it back
// LocalStateManager doesn't have any opinion in regards to where it should be persistent to
type LocalStateManager interface {
	SaveLocalState(state KeygenLocalState) error
	GetLocalState(pubKey string) (KeygenLocalState, error)
	SaveAddressBook(addressBook map[peer.ID]addr.AddrList) error
	RetrieveP2PAddresses() (addr.AddrList, error)
}

// FileStateMgr save the local state to file
type FileStateMgr struct {
	folder    string
	writeLock *sync.RWMutex
	atRestKey []byte // XChaCha20-Poly1305 key for at-rest encryption; nil = disabled
}

// FileStateMgrOption is a functional option for NewFileStateMgr.
type FileStateMgrOption func(*FileStateMgr)

// WithAtRestEncryption enables at-rest encryption of keyshare files using a key
// derived from the signer seed phrase.
func WithAtRestEncryption(seedPhrase string) FileStateMgrOption {
	return func(fsm *FileStateMgr) {
		if seedPhrase != "" {
			fsm.atRestKey = DeriveAtRestKey(seedPhrase)
		}
	}
}

// NewFileStateMgr create a new instance of the FileStateMgr which implements LocalStateManager
func NewFileStateMgr(folder string, opts ...FileStateMgrOption) (*FileStateMgr, error) {
	if len(folder) > 0 {
		_, err := os.Stat(folder)
		if err != nil && os.IsNotExist(err) {
			if err := os.MkdirAll(folder, os.ModePerm); err != nil {
				return nil, err
			}
		}
	}
	fsm := &FileStateMgr{
		folder:    folder,
		writeLock: &sync.RWMutex{},
	}
	for _, opt := range opts {
		opt(fsm)
	}
	return fsm, nil
}

func (fsm *FileStateMgr) getFilePathName(pubKey string) (string, error) {
	ret, err := conversion.CheckKeyOnCurve(pubKey)
	if err != nil {
		return "", err
	}
	if !ret {
		return "", errors.New("invalid pubkey for file name")
	}

	localFileName := fmt.Sprintf("localstate-%s.json", pubKey)
	if len(fsm.folder) > 0 {
		return filepath.Join(fsm.folder, localFileName), nil
	}
	return localFileName, nil
}

// SaveLocalState save the local state to file
func (fsm *FileStateMgr) SaveLocalState(state KeygenLocalState) error {
	buf, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("fail to marshal KeygenLocalState to json: %w", err)
	}
	if fsm.atRestKey != nil {
		buf, err = EncryptAtRest(fsm.atRestKey, buf)
		if err != nil {
			return fmt.Errorf("fail to encrypt keyshare at rest: %w", err)
		}
	}
	filePathName, err := fsm.getFilePathName(state.PubKey)
	if err != nil {
		return err
	}
	return atomicWriteFile(filePathName, buf, 0o600)
}

// GetLocalState read the local state from file system
func (fsm *FileStateMgr) GetLocalState(pubKey string) (KeygenLocalState, error) {
	if len(pubKey) == 0 {
		return KeygenLocalState{}, errors.New("pub key is empty")
	}
	filePathName, err := fsm.getFilePathName(pubKey)
	if err != nil {
		return KeygenLocalState{}, err
	}
	if _, err = os.Stat(filePathName); os.IsNotExist(err) {
		return KeygenLocalState{}, err
	}

	buf, err := os.ReadFile(filePathName)
	if err != nil {
		return KeygenLocalState{}, fmt.Errorf("file to read from file(%s): %w", filePathName, err)
	}

	jsonBytes := buf
	if IsEncryptedAtRest(buf) {
		if fsm.atRestKey == nil {
			return KeygenLocalState{}, errors.New("keyshare file is encrypted but no at-rest key is configured")
		}
		jsonBytes, err = DecryptAtRest(fsm.atRestKey, buf)
		if err != nil {
			return KeygenLocalState{}, fmt.Errorf("fail to decrypt keyshare at rest: %w", err)
		}
	} else if fsm.atRestKey != nil {
		if encrypted, encErr := EncryptAtRest(fsm.atRestKey, buf); encErr != nil {
			log.Warn().Err(encErr).Str("file", filePathName).Msg("failed to encrypt keyshare for migration")
		} else if writeErr := atomicWriteFile(filePathName, encrypted, 0o600); writeErr != nil {
			log.Warn().Err(writeErr).Str("file", filePathName).Msg("failed to write migrated keyshare")
		}
	}

	var localState KeygenLocalState
	if err := json.Unmarshal(jsonBytes, &localState); nil != err {
		return KeygenLocalState{}, fmt.Errorf("fail to unmarshal KeygenLocalState: %w", err)
	}
	return localState, nil
}

func (fsm *FileStateMgr) SaveAddressBook(address map[peer.ID]addr.AddrList) error {
	if len(fsm.folder) < 1 {
		return errors.New("base file path is invalid")
	}
	filePathName := filepath.Join(fsm.folder, "address_book.seed")
	var buf bytes.Buffer

	for peer, addrs := range address {
		for _, addr := range addrs {
			// we do not save the loopback addr
			if strings.Contains(addr.String(), "127.0.0.1") {
				continue
			}
			record := addr.String() + "/p2p/" + peer.String() + "\n"
			_, err := buf.WriteString(record)
			if err != nil {
				return errors.New("fail to write the record to buffer")
			}
		}
	}
	fsm.writeLock.Lock()
	defer fsm.writeLock.Unlock()
	return os.WriteFile(filePathName, buf.Bytes(), 0o600)
}

func (fsm *FileStateMgr) RetrieveP2PAddresses() (addr.AddrList, error) {
	if len(fsm.folder) < 1 {
		return nil, errors.New("base file path is invalid")
	}
	filePathName := filepath.Join(fsm.folder, "address_book.seed")

	_, err := os.Stat(filePathName)
	if err != nil {
		return nil, err
	}
	fsm.writeLock.RLock()
	input, err := os.ReadFile(filePathName)
	if err != nil {
		fsm.writeLock.RUnlock()
		return nil, err
	}
	fsm.writeLock.RUnlock()
	data := strings.Split(string(input), "\n")
	var peerAddresses []maddr.Multiaddr
	for _, el := range data {
		// we skip the empty entry
		if len(el) == 0 {
			continue
		}
		addr, err := maddr.NewMultiaddr(el)
		if err != nil {
			return nil, fmt.Errorf("invalid address in address book %w", err)
		}
		peerAddresses = append(peerAddresses, addr)
	}
	return peerAddresses, nil
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		_ = tmp.Close()
		if !success {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	success = true
	return nil
}
