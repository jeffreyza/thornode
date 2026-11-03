package signing_test

import (
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signing"
	"gitlab.com/thorchain/thornode/v3/common"
)

type fakeBridge struct {
	height    int64
	heightErr error
	mimirs    map[string]int64
	mimirErr  error
}

func (b *fakeBridge) GetBlockHeight() (int64, error) {
	return b.height, b.heightErr
}

func (b *fakeBridge) GetMimir(key string) (int64, error) {
	if b.mimirErr != nil {
		return 0, b.mimirErr
	}
	if v, ok := b.mimirs[key]; ok {
		return v, nil
	}
	return 0, nil
}

func discardLogger() zerolog.Logger {
	return zerolog.New(io.Discard)
}

// ----------------------------------------------------------------------------
// CheckSigningHalt — pure low-level check
// ----------------------------------------------------------------------------

func TestCheckSigningHalt_NotHalted(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{}}
	halted, global, err := signing.CheckSigningHalt(b, common.BTCChain, 100)
	if err != nil || halted || global {
		t.Fatalf("expected not halted, no error; got halted=%v global=%v err=%v", halted, global, err)
	}
}

func TestCheckSigningHalt_GlobalActive(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{"HALTSIGNING": 50}}
	halted, global, err := signing.CheckSigningHalt(b, common.BTCChain, 100)
	if err != nil || !halted || !global {
		t.Fatalf("expected halted+global; got halted=%v global=%v err=%v", halted, global, err)
	}
}

func TestCheckSigningHalt_GlobalFuture(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{"HALTSIGNING": 200}}
	halted, _, err := signing.CheckSigningHalt(b, common.BTCChain, 100)
	if err != nil || halted {
		t.Fatalf("expected not halted (future halt); got halted=%v err=%v", halted, err)
	}
}

func TestCheckSigningHalt_ChainActive(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{"HaltSigningBTC": 1}}
	halted, global, err := signing.CheckSigningHalt(b, common.BTCChain, 100)
	if err != nil || !halted || global {
		t.Fatalf("expected halted, chain-specific (global=false); got halted=%v global=%v err=%v", halted, global, err)
	}
}

func TestCheckSigningHalt_ChainKeyDifferentForOtherChains(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{"HaltSigningBTC": 1}}
	halted, _, err := signing.CheckSigningHalt(b, common.ETHChain, 100)
	if err != nil || halted {
		t.Fatalf("HaltSigningBTC must not affect ETH; got halted=%v err=%v", halted, err)
	}
}

func TestCheckSigningHalt_MimirFetchError(t *testing.T) {
	b := &fakeBridge{mimirErr: errors.New("rpc timeout")}
	halted, global, err := signing.CheckSigningHalt(b, common.BTCChain, 100)
	if err == nil || halted || global {
		t.Fatalf("expected error propagated; got halted=%v global=%v err=%v", halted, global, err)
	}
}

func TestCheckSigningHalt_ZeroMimirIsNotHalted(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{"HALTSIGNING": 0, "HaltSigningBTC": 0}}
	halted, _, err := signing.CheckSigningHalt(b, common.BTCChain, 100)
	if err != nil || halted {
		t.Fatalf("mimir 0 means unset; expected not halted, got halted=%v err=%v", halted, err)
	}
}

func TestCheckSigningHalt_MimirValueOne_AlwaysHalts(t *testing.T) {
	b := &fakeBridge{mimirs: map[string]int64{"HALTSIGNING": 1}}
	halted, global, err := signing.CheckSigningHalt(b, common.BTCChain, 1)
	if err != nil || !halted || !global {
		t.Fatalf("mimir=1 at height>=1 should halt globally; got halted=%v global=%v err=%v", halted, global, err)
	}
}

// ----------------------------------------------------------------------------
// IsSigningHalted — convenience wrapper used by retry loops
// ----------------------------------------------------------------------------

func TestIsSigningHalted_NotHalted(t *testing.T) {
	b := &fakeBridge{height: 100, mimirs: map[string]int64{}}
	if signing.IsSigningHalted(b, common.BTCChain, discardLogger()) {
		t.Fatalf("expected not halted when both mimirs are 0")
	}
}

func TestIsSigningHalted_GlobalActive(t *testing.T) {
	b := &fakeBridge{height: 100, mimirs: map[string]int64{"HALTSIGNING": 50}}
	if !signing.IsSigningHalted(b, common.BTCChain, discardLogger()) {
		t.Fatalf("expected halted via global mimir")
	}
}

func TestIsSigningHalted_ChainActive(t *testing.T) {
	b := &fakeBridge{height: 100, mimirs: map[string]int64{"HaltSigningBTC": 1}}
	if !signing.IsSigningHalted(b, common.BTCChain, discardLogger()) {
		t.Fatalf("expected halted via chain mimir")
	}
}

func TestIsSigningHalted_HeightFetchError_FailsClosed(t *testing.T) {
	b := &fakeBridge{heightErr: errors.New("network down")}
	if !signing.IsSigningHalted(b, common.BTCChain, discardLogger()) {
		t.Fatalf("expected fail-closed (halted=true) when GetBlockHeight errors")
	}
}

func TestIsSigningHalted_MimirFetchError_FailsClosed(t *testing.T) {
	b := &fakeBridge{height: 100, mimirErr: errors.New("rpc timeout")}
	if !signing.IsSigningHalted(b, common.BTCChain, discardLogger()) {
		t.Fatalf("expected fail-closed (halted=true) when GetMimir errors")
	}
}
