package core

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestWithdrawLPMemo(t *testing.T) {
	got := withdrawLPMemo(common.XMRAsset)
	want := "-:XMR.XMR:5000:XMR.XMR"
	if got != want {
		t.Fatalf("expected %s, got %s", want, got)
	}
}
