package utxo

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/pivxutil"
)

func TestPIVXFeeRate(t *testing.T) {
	rate, ok := pivxutil.FeeRateSatsPerByte("0.00010000", 500)
	if !ok || rate != 10 {
		t.Fatalf("10000 sat/KB: got %d ok=%v", rate, ok)
	}
	if _, ok = pivxutil.FeeRateSatsPerByte("0.00010000", 0); ok {
		t.Fatal("accepted an empty window")
	}
	if _, ok = pivxutil.FeeRateSatsPerByte("0", 100); ok {
		t.Fatal("accepted a zero rate")
	}
	rate, ok = pivxutil.FeeRateSatsPerByte("0.00001500", 100)
	if !ok || rate != 2 {
		t.Fatalf("1500 sat/KB: got %d ok=%v", rate, ok)
	}
	// Recorded from pivxd 5.6.1 regtest after a 225-byte tx paid 0.0001 PIV.
	rate, ok = pivxutil.FeeRateSatsPerByte("0.00044444", 225)
	if !ok || rate != 45 {
		t.Fatalf("regtest feeperkb: got %d ok=%v", rate, ok)
	}
}
