package blockscanner

import (
	"encoding/binary"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// LevelDBScannerStorage is a scanner storage backed by level db
type LevelDBScannerStorage struct {
	db          *leveldb.DB
	syncScanPos bool
}

const ScanPosKey = "blockscanpos"

// NewLevelDBScannerStorage create a new instance of LevelDBScannerStorage
func NewLevelDBScannerStorage(db *leveldb.DB) (*LevelDBScannerStorage, error) {
	return newLevelDBScannerStorage(db, false)
}

func newLevelDBScannerStorage(db *leveldb.DB, syncScanPos bool) (*LevelDBScannerStorage, error) {
	return &LevelDBScannerStorage{db: db, syncScanPos: syncScanPos}, nil
}

// GetScanPos get current Scan Pos
func (ldbss *LevelDBScannerStorage) GetScanPos() (int64, error) {
	buf, err := ldbss.db.Get([]byte(ScanPosKey), nil)
	if err != nil {
		return 0, err
	}
	pos, _ := binary.Varint(buf)
	return pos, nil
}

// SetScanPos save current scan pos
func (ldbss *LevelDBScannerStorage) SetScanPos(block int64) error {
	buf := make([]byte, 8)
	n := binary.PutVarint(buf, block)
	var writeOptions *opt.WriteOptions
	if ldbss.syncScanPos {
		writeOptions = &opt.WriteOptions{Sync: true}
	}
	return ldbss.db.Put([]byte(ScanPosKey), buf[:n], writeOptions)
}

func (ldbss *LevelDBScannerStorage) Close() error {
	return ldbss.db.Close()
}
