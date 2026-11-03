package common

import (
	"math/rand"
	"strings"
	"testing"
)

func TestEqualFoldUpperMatchesOriginal(t *testing.T) {
	orig := func(a, b string) bool { return strings.EqualFold(strings.ToUpper(a), strings.ToUpper(b)) }
	cases := []string{
		"", "a", "A", "btc", "BTC", "BTC.BTC", "btc.btc", "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
		"eth.usdc-0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", "THOR", "thor", "RUNE", "rune", "k", "K",
		"K", "s", "S", "ſ", "i", "I", "ı", "İ", "ß", "SS", "é", "É", "ǅ", "ǆ", "Ǆ",
		"xKy", "XKY", "\xff", "a\xffb", "A\xffB", "AB", "ab",
	}
	for _, a := range cases {
		for _, b := range cases {
			if got, want := equalFoldUpper(a, b), orig(a, b); got != want {
				t.Fatalf("equalFoldUpper(%q, %q) = %v, want %v", a, b, got, want)
			}
		}
	}
	alphabet := []rune("aAbBkKsSiIzZ09.-/~_Kſıİßé")
	rng := rand.New(rand.NewSource(1)) // nolint:gosec // deterministic, reproducible test input
	gen := func() string {
		r := make([]rune, rng.Intn(6))
		for i := range r {
			r[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(r)
	}
	for i := 0; i < 200000; i++ {
		a, b := gen(), gen()
		if rng.Intn(3) == 0 {
			b = strings.ToLower(a)
		}
		if got, want := equalFoldUpper(a, b), orig(a, b); got != want {
			t.Fatalf("equalFoldUpper(%q, %q) = %v, want %v", a, b, got, want)
		}
	}
}
