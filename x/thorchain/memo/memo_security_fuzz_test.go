package thorchain

import (
	"errors"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func FuzzParseMemo(f *testing.F) {
	for _, seed := range []string{"", "SWAP:BTC.BTC:bc1qxyz", "=:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a", "+:BTC.BTC", "r:12345", "r:", "MAINT:1", "SWAP:BTC.BTC:bad!", "reference:BTC.BTC:=:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a", strings.Repeat(":", 200)} {
		f.Add(seed)
	}
	version := types.GetCurrentVersion()
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 512 {
			t.Skip()
		}
		first, err1 := ParseMemo(version, raw)
		second, err2 := ParseMemo(version, raw)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("parse outcome changed for %q: %v, %v", raw, err1, err2)
		}
		if err1 != nil {
			// ParseMemo has no keeper, and reference memos validate their
			// embedded memo through it; that panic is known and recovered.
			if errors.Is(err1, errMemoPanic) && !strings.HasPrefix(strings.ToLower(raw), "reference:") {
				t.Fatalf("memo parser panicked for %q: %v", raw, err1)
			}
			return
		}
		if first.GetType() != second.GetType() || first.GetAsset() != second.GetAsset() || first.GetAddress() != second.GetAddress() {
			t.Fatalf("parse semantics changed for %q", raw)
		}
	})
}

func TestParseMemoPanicClassification(t *testing.T) {
	// A validation error that echoes the panic text must not look like a panic.
	_, err := ParseMemo(types.GetCurrentVersion(), "panicked parsing memo(x), err: y")
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if errors.Is(err, errMemoPanic) {
		t.Fatalf("validation error classified as a panic: %v", err)
	}
}
