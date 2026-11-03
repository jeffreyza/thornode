// wrap_legacy synthesises ecdsa_legacy fixtures from the existing v3
// fixtures by nulling out the v3-only Paillier safe primes.
//
// The deserialised in-memory struct shape is identical to that of a real
// binance-chain/tss-lib v0.1.6 keyshare read through v3 structs, so v3
// signing exercises the same code path. See
// docs/superpowers/specs/2026-05-22-tss-legacy-keyshare-keysign-design.md
// section "Test fixtures" for the equivalence argument.
//
// Run once; commit the output.
//
//	go run ./bifrost/tss/go-tss/test_data/keysign_data/ecdsa_legacy/wrap_legacy
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
)

const (
	srcDir = "bifrost/tss/go-tss/test_data/keysign_data/ecdsa"
	dstDir = "bifrost/tss/go-tss/test_data/keysign_data/ecdsa_legacy"
)

func main() {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		fatal(err)
	}
	for i := 0; i < 4; i++ {
		srcPath := filepath.Join(srcDir, fmt.Sprintf("%d.json", i))
		dstPath := filepath.Join(dstDir, fmt.Sprintf("%d.json", i))
		if err := convert(srcPath, dstPath); err != nil {
			fatal(fmt.Errorf("%s -> %s: %w", srcPath, dstPath, err))
		}
		fmt.Println("wrote", dstPath)
	}
}

func convert(srcPath, dstPath string) error {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	var state storage.KeygenLocalState
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("unmarshal wrapper: %w", err)
	}
	var local keygen.LocalPartySaveData
	if err := json.Unmarshal(state.LocalData, &local); err != nil {
		return fmt.Errorf("unmarshal local data: %w", err)
	}
	if local.PaillierSK == nil {
		return fmt.Errorf("source %s has nil PaillierSK; aborting", srcPath)
	}
	local.PaillierSK.P = nil
	local.PaillierSK.Q = nil
	encoded, err := json.Marshal(local)
	if err != nil {
		return err
	}
	state.LocalData = encoded
	out, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, out, 0o644)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "wrap_legacy:", err)
	os.Exit(1)
}
