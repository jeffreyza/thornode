package common

import (
	"strings"
	"unicode/utf8"
)

// equalFoldUpper reports strings.EqualFold(strings.ToUpper(a), strings.ToUpper(b)),
// the comparison Chain, Symbol and Ticker Equals have always made, without
// building the upper-cased strings. ASCII input is compared in one pass that
// stops at the first difference; anything else takes the original path.
func equalFoldUpper(a, b string) bool {
	if len(a) == len(b) {
		for i := 0; i < len(a); i++ {
			ca, cb := a[i], b[i]
			if ca|cb >= utf8.RuneSelf {
				return strings.EqualFold(strings.ToUpper(a), strings.ToUpper(b))
			}
			if ca == cb {
				continue
			}
			if 'a' <= ca && ca <= 'z' {
				ca -= 'a' - 'A'
			}
			if 'a' <= cb && cb <= 'z' {
				cb -= 'a' - 'A'
			}
			if ca != cb {
				// the runes before i are ASCII on both sides, so these are the
				// i-th runes of both upper-cased strings, and two different
				// ASCII upper-case runes never fold together
				return false
			}
		}
		return true
	}
	// different lengths can only fold together through multi-byte runes
	for i := 0; i < len(a); i++ {
		if a[i] >= utf8.RuneSelf {
			return strings.EqualFold(strings.ToUpper(a), strings.ToUpper(b))
		}
	}
	for i := 0; i < len(b); i++ {
		if b[i] >= utf8.RuneSelf {
			return strings.EqualFold(strings.ToUpper(a), strings.ToUpper(b))
		}
	}
	return false
}
