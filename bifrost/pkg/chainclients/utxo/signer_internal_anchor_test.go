package utxo

import "testing"

// TestAnchorConsolidateHeightConverges asserts the property the consolidate
// keysign relies on: any two vault members whose THORChain tips fall in the same
// cadence window derive the identical signing height, so they batch into one
// go-tss keysign session and elect one leader — closing the consolidate
// leader-blame race. Tips on opposite sides of a boundary intentionally diverge.
func TestAnchorConsolidateHeightConverges(t *testing.T) {
	const c = consolidateKeysignBlockCadence

	cases := []struct {
		name   string
		height int64
		want   int64
	}{
		{"on boundary", 100 * c, 100 * c},
		{"just after boundary", 100*c + 1, 100 * c},
		{"just before next boundary", 101*c - 1, 100 * c},
		{"next boundary", 101 * c, 101 * c},
		{"below one cadence returns height", c - 1, c - 1},
		{"zero", 0, 0},
	}
	for _, tc := range cases {
		if got := anchorConsolidateHeight(tc.height); got != tc.want {
			t.Errorf("%s: anchorConsolidateHeight(%d) = %d, want %d", tc.name, tc.height, got, tc.want)
		}
	}

	// The driver: three members a few blocks apart within one window must agree.
	base := int64(28037861) // the tcy 03:08 BCH consolidate height from the incident
	boundary := base / c * c
	for _, skew := range []int64{0, 1, 2, 3, 7} {
		h := boundary + skew
		if h/c*c != boundary { // skew kept within the window for this assertion
			continue
		}
		if got := anchorConsolidateHeight(h); got != boundary {
			t.Errorf("member at tip %d anchored to %d, want shared boundary %d", h, got, boundary)
		}
	}

	// Anchored height is never above the tip, so it stays a valid signing_height
	// (the keysign-party query rejects future heights).
	for _, h := range []int64{1, c, c + 1, 123456, 28037861} {
		if got := anchorConsolidateHeight(h); got > h {
			t.Errorf("anchorConsolidateHeight(%d) = %d exceeds tip", h, got)
		}
	}
}
