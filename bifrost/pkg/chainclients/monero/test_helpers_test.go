package monero

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"filippo.io/edwards25519"
	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestMain(m *testing.M) {
	cfg := cosmos.GetConfig()
	cfg.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg.SetBech32PrefixForValidator("tthorv", "tthorvpub")
	cfg.SetBech32PrefixForConsensusNode("tthorc", "tthorcpub")
	cfg.SetCoinType(cmd.THORChainCoinType)
	cfg.SetPurpose(cmd.THORChainCoinPurpose)
	// The stores fsync every write so a crashed node keeps its scan state.
	// No unit test can observe an OS crash, and fsync made the package's
	// scan-heavy tests disk-bound (a churn rescan: 14s on disk, 0.7s on
	// tmpfs), so tests write without it unless they check the syncs
	// themselves (withProductionFsync). Logic and write order are unchanged.
	syncScannerWrite, signedTxPayloadWriteOptions, syncInternalInboundReplayWrite = testNoSync, testNoSync, testNoSync
	os.Exit(m.Run())
}

var (
	testNoSync = &opt.WriteOptions{}
	// the stores' production write options, saved before TestMain replaces them
	productionScannerWrite, productionSignedTxPayloadWrite, productionInternalInboundWrite = syncScannerWrite,
		signedTxPayloadWriteOptions, syncInternalInboundReplayWrite
)

// withProductionFsync restores the stores' production (fsync) write options
// for a test that counts syncs. Only non-parallel tests may call it: parallel
// tests run after every sequential one has finished.
func withProductionFsync(t *testing.T) {
	t.Helper()
	syncScannerWrite, signedTxPayloadWriteOptions, syncInternalInboundReplayWrite = productionScannerWrite,
		productionSignedTxPayloadWrite, productionInternalInboundWrite
	t.Cleanup(func() {
		syncScannerWrite, signedTxPayloadWriteOptions, syncInternalInboundReplayWrite = testNoSync, testNoSync, testNoSync
	})
}

func testVaultPubKey(t *testing.T, seed string) common.PubKey {
	t.Helper()
	hasher := sha512.New()
	hasher.Write([]byte("thorchain xmr test vault pubkey"))
	hasher.Write([]byte(seed))
	sum := hasher.Sum(nil)
	scalar, err := new(edwards25519.Scalar).SetUniformBytes(sum)
	if err != nil {
		t.Fatalf("test xmr scalar: %v", err)
	}
	point := new(edwards25519.Point).ScalarBaseMult(scalar)
	pk, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(point.Bytes()))
	if err != nil {
		t.Fatalf("test xmr pubkey: %v", err)
	}
	return pk
}

func testOutboundXMRDestination() common.Address {
	if common.CurrentChainNetwork == common.MockNet {
		return common.Address(common.XMRChecksummedDummyAddress('8'))
	}
	return common.Address(common.XMRChecksummedDummyAddress('4'))
}

func testXMRHash(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func testXMROutputRef(label string, outputIndex uint32, spendTxHash string) *common.OutputRef {
	return &common.OutputRef{
		TxHash:      testXMRHash(label + ":tx"),
		OutputIndex: outputIndex,
		KeyImage:    testXMRHash(label + ":key-image"),
		SpendTxHash: spendTxHash,
	}
}

var testVaultAddressCounter atomic.Uint64

func testVaultPubKeyAndAddress(t *testing.T) (common.PubKey, common.Address) {
	t.Helper()

	seed := fmt.Sprintf("vault-address:%s:%d", t.Name(), testVaultAddressCounter.Add(1))
	pubKey := testVaultPubKey(t, seed)
	address, err := pubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive monero address: %v", err)
	}
	return pubKey, address
}

func testRegisterVaultResponse(nextHeight uint64) sidecar.RegisterVaultResp {
	return sidecar.RegisterVaultResp{
		Registered:         true,
		EffectiveBirthday:  nextHeight,
		ResetEpoch:         0,
		ExpectedNextHeight: &nextHeight,
	}
}

func writeTestScannerState(t *testing.T, w http.ResponseWriter, nextHeight uint64) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(sidecar.ScannerState{
		ResetEpoch:       0,
		NextHeight:       &nextHeight,
		StrictAfterReset: false,
	}); err != nil {
		t.Errorf("encode scanner state: %v", err)
	}
}

type testScannerContinuity struct {
	mu                   sync.Mutex
	nextHeight           uint64
	resetEpoch           uint64
	lastHeight           *uint64
	lastBlockHash        string
	lastParentHash       string
	lastRewindPrevious   uint64
	lastRewindTarget     uint64
	lastRewindResetEpoch uint64
	rewindCalls          int
	syncHeights          []int64
	onRewind             func(sidecar.ScannerRewindReq)
}

func newTestScannerContinuity(nextHeight uint64) *testScannerContinuity {
	return &testScannerContinuity{nextHeight: nextHeight}
}

func (s *testScannerContinuity) handle(t *testing.T, w http.ResponseWriter, r *http.Request) bool {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/scanner/state":
		_ = json.NewEncoder(w).Encode(sidecar.ScannerState{
			ResetEpoch:       s.resetEpoch,
			NextHeight:       &s.nextHeight,
			LastHeight:       s.lastHeight,
			LastBlockHash:    s.lastBlockHash,
			LastParentHash:   s.lastParentHash,
			StrictAfterReset: s.resetEpoch > 0,
		})
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/rewind":
		var req sidecar.ScannerRewindReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		if req.Network != "mocknet" {
			http.Error(w, "unexpected scanner rewind network", http.StatusBadRequest)
			return true
		}
		s.rewindCalls++
		if s.onRewind != nil {
			s.onRewind(req)
		}
		if s.nextHeight == req.TargetNextHeight && s.lastHeight != nil &&
			*s.lastHeight == req.AnchorHeight && strings.EqualFold(s.lastBlockHash, req.AnchorHash) {
			_ = json.NewEncoder(w).Encode(sidecar.ScannerRewindResp{
				Rewound:            false,
				PreviousNextHeight: s.lastRewindPrevious,
				NextHeight:         s.nextHeight,
				ResetEpoch:         s.resetEpoch,
			})
			return true
		}
		if req.ExpectedResetEpoch != s.resetEpoch || req.ExpectedNextHeight != s.nextHeight ||
			req.TargetNextHeight == 0 || req.TargetNextHeight >= s.nextHeight ||
			req.AnchorHeight+1 != req.TargetNextHeight {
			http.Error(w, "scanner rewind compare-and-set conflict", http.StatusConflict)
			return true
		}
		previous := s.nextHeight
		s.resetEpoch++
		s.nextHeight = req.TargetNextHeight
		anchorHeight := req.AnchorHeight
		s.lastHeight = &anchorHeight
		s.lastBlockHash = strings.ToLower(req.AnchorHash)
		s.lastParentHash = testXMRHash(fmt.Sprintf("anchor-parent-%d", req.AnchorHeight))
		s.lastRewindPrevious = previous
		s.lastRewindTarget = req.TargetNextHeight
		s.lastRewindResetEpoch = s.resetEpoch
		_ = json.NewEncoder(w).Encode(sidecar.ScannerRewindResp{
			Rewound:            true,
			PreviousNextHeight: previous,
			NextHeight:         s.nextHeight,
			ResetEpoch:         s.resetEpoch,
		})
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
		var req sidecar.SyncReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		if s.lastHeight != nil && uint64(req.Height) == *s.lastHeight &&
			strings.EqualFold(req.BlockHash, s.lastBlockHash) && strings.EqualFold(req.ParentHash, s.lastParentHash) {
			_ = json.NewEncoder(w).Encode(sidecar.Transfers{})
			return true
		}
		if req.Height < 0 || uint64(req.Height) != s.nextHeight {
			http.Error(w, "scanner sync out of order", http.StatusConflict)
			return true
		}
		s.syncHeights = append(s.syncHeights, req.Height)
		if s.resetEpoch > 0 && (req.ResetEpoch == nil || *req.ResetEpoch != s.resetEpoch ||
			req.ExpectedNextHeight == nil || *req.ExpectedNextHeight != s.nextHeight) {
			http.Error(w, "scanner sync continuity mismatch", http.StatusConflict)
			return true
		}
		height := uint64(req.Height)
		s.lastHeight = &height
		s.lastBlockHash = strings.ToLower(req.BlockHash)
		s.lastParentHash = strings.ToLower(req.ParentHash)
		s.nextHeight = height + 1
		_ = json.NewEncoder(w).Encode(sidecar.Transfers{})
		return true
	default:
		return false
	}
}

// expectedXMRVaultAddress is the pure helper behind expectedXMRVaultAddressFromKeygenRequest.
func expectedXMRVaultAddress(vaultPubKeyFrost common.PubKey) (common.Address, error) {
	if vaultPubKeyFrost.IsEmpty() {
		return common.NoAddress, fmt.Errorf("xmr keygen requires a non-empty frost vault pubkey")
	}
	expected, err := vaultPubKeyFrost.GetAddress(common.XMRChain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to derive monero address from vault pubkey %s: %w", vaultPubKeyFrost.String(), err)
	}
	if expected.IsEmpty() {
		return common.NoAddress, fmt.Errorf("monero vault pubkey %s derived an empty address", vaultPubKeyFrost.String())
	}
	return expected, nil
}

// expectedXMRVaultAddressFromKeygenRequest is exercised by client_keygen_test.go and
// scanner_test.go to verify that the keygen handshake computes the same address the
// chain expects from a vault pubkey.
func expectedXMRVaultAddressFromKeygenRequest(vaultPubKeyFrost string) (common.Address, error) {
	value := strings.TrimSpace(vaultPubKeyFrost)
	if value == "" {
		return expectedXMRVaultAddress(common.EmptyPubKey)
	}
	pubKey, err := common.NewPubKey(value)
	if err != nil {
		return common.NoAddress, fmt.Errorf("invalid vault pubkey %q: %w", value, err)
	}
	return expectedXMRVaultAddress(pubKey)
}

// testBlockHash builds a deterministic, height-keyed 64-hex block hash. The
// sidecar client validates block_hash/parent_hash as 32-byte hex, matching what
// monerod actually returns, so fixtures must look like real hashes.
func testBlockHash(height int64) string {
	return fmt.Sprintf("%064x", height)
}

// testStaleBlockHash is a valid but different 64-hex hash for the same height:
// the block a reorg discarded.
func testStaleBlockHash(height int64) string {
	return fmt.Sprintf("ff%062x", height)
}
