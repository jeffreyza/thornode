package common

import (
	"strings"
	"testing"
)

func FuzzNewAddress(f *testing.F) {
	for _, seed := range []string{"", "bogus", "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a", "0x90f2b1ae50e6018230e90a33f98c7844a0ab63zz", "thor1kljxxccrheghavaw97u78le6yy3sdj7h696nl4", " T ", "4abcDEF", strings.Repeat("a", 257)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 256 {
			t.Skip()
		}
		addr, err := NewAddress(input)
		if err != nil {
			if addr != NoAddress {
				t.Fatalf("rejected address returned %q", addr)
			}
			return
		}
		if addr.String() != input {
			t.Fatalf("address changed on parse: %q -> %q", input, addr)
		}
		if _, err := NewAddress(addr.String()); err != nil {
			t.Fatalf("accepted address rejected on repeat: %q: %v", addr, err)
		}
		if strings.ContainsAny(input, " !@#$%^&*()\n\t") {
			t.Fatalf("accepted forbidden character in %q", input)
		}
	})
}
