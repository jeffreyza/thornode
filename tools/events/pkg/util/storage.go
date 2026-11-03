package util

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
)

// Store will serialize an object to storage.
func Store(path string, obj any) error {
	path = filepath.Join(config.Get().StoragePath, path)

	dir := filepath.Dir(path)
	err := os.MkdirAll(dir, 0o755)
	if err != nil {
		return err
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	err = encoder.Encode(obj)
	if err != nil {
		return err
	}

	return nil
}

// Load will load an object from storage.
func Load(path string, obj any) error {
	path = filepath.Join(config.Get().StoragePath, path)

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	err = decoder.Decode(obj)
	if err != nil {
		return err
	}

	return nil
}

// Prune will recursively delete all files older than a week from the provided path.
func Prune(path string) {
	path = filepath.Join(config.Get().StoragePath, path)
	err := filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		oneWeek := 7 * 24 * time.Hour
		if time.Since(info.ModTime()) > oneWeek {
			os.Remove(path)
		}
		log.Info().Str("path", path).Msg("pruned")

		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		log.Error().Err(err).Msg("prune failed")
	}
}
