package types

import (
	"reflect"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestObservationMinedHeightPreservesExistingWireFields(t *testing.T) {
	nonce := &common.SolNonceUpdate{NonceAccount: "a", Nonce: "n", Slot: 1}
	nonceWire := []byte{14<<3 | 2, 8, 10, 1, 'a', 18, 1, 'n', 24, 1}
	heightWire := []byte{15 << 3, 99}
	for _, tc := range []struct {
		name   string
		wire   []byte
		height int64
		nonce  *common.SolNonceUpdate
	}{
		{name: "nonce only", wire: nonceWire, nonce: nonce},
		{name: "mined height only", wire: heightWire, height: 99},
		{name: "both fields", wire: append(append([]byte(nil), nonceWire...), heightWire...), height: 99, nonce: nonce},
		{name: "reverse field order", wire: append(append([]byte(nil), heightWire...), nonceWire...), height: 99, nonce: nonce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed common.ObservedTx
			if err := observed.Unmarshal(tc.wire); err != nil {
				t.Fatal(err)
			}
			var query QueryObservedTx
			if err := query.Unmarshal(tc.wire); err != nil {
				t.Fatal(err)
			}
			if observed.XmrMinedHeight != tc.height || query.XmrMinedHeight != tc.height {
				t.Fatalf("mined heights = %d/%d, want %d", observed.XmrMinedHeight, query.XmrMinedHeight, tc.height)
			}
			if !reflect.DeepEqual(observed.SolNonce, tc.nonce) {
				t.Fatalf("nonce = %v, want %v", observed.SolNonce, tc.nonce)
			}
			encoded, err := observed.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			var restored common.ObservedTx
			if err := restored.Unmarshal(encoded); err != nil {
				t.Fatal(err)
			}
			if restored.XmrMinedHeight != tc.height {
				t.Fatalf("round-trip mined height = %d, want %d", restored.XmrMinedHeight, tc.height)
			}
			if !reflect.DeepEqual(restored.SolNonce, tc.nonce) {
				t.Fatalf("round-trip nonce = %v, want %v", restored.SolNonce, tc.nonce)
			}
		})
	}
}
