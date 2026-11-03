package blockscanner

import (
	"fmt"
	"io"

	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/bifrost/db"
	"gitlab.com/thorchain/thornode/v3/config"
)

// ScannerStorage define the method need to be used by scanner
type ScannerStorage interface {
	GetScanPos() (int64, error)
	SetScanPos(block int64) error
	GetInternalDb() *leveldb.DB
	io.Closer
}

// BlockScannerStorage
type BlockScannerStorage struct {
	*LevelDBScannerStorage
	db *leveldb.DB
}

func NewBlockScannerStorage(levelDbFolder string, opts config.LevelDBOptions) (*BlockScannerStorage, error) {
	return newBlockScannerStorage(levelDbFolder, opts, false)
}

// NewBlockScannerStorageWithScanPosSync creates scanner storage whose cursor
// updates are fsynced. XMR uses this with its durable reorg journal.
func NewBlockScannerStorageWithScanPosSync(levelDbFolder string, opts config.LevelDBOptions) (*BlockScannerStorage, error) {
	return newBlockScannerStorage(levelDbFolder, opts, true)
}

func newBlockScannerStorage(levelDbFolder string, opts config.LevelDBOptions, syncScanPos bool) (*BlockScannerStorage, error) {
	ldb, err := db.NewLevelDB(levelDbFolder, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create level db: %w", err)
	}

	levelDbStorage, err := newLevelDBScannerStorage(ldb, syncScanPos)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}
	return &BlockScannerStorage{
		LevelDBScannerStorage: levelDbStorage,
		db:                    ldb,
	}, nil
}

func (s *BlockScannerStorage) GetInternalDb() *leveldb.DB {
	return s.db
}
