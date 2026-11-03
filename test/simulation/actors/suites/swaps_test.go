package suites

import (
	rand "math/rand/v2"
	"strings"
	"testing"
)

func TestSwapsAlwaysScheduleBothZECAddressFormats(t *testing.T) {
	for seed := uint64(0); seed < 10; seed++ {
		names := actorNames(Swaps(rand.New(rand.NewPCG(seed, 42))))
		for _, format := range []string{"transparent", "TEX"} {
			found := false
			for name := range names {
				if strings.HasSuffix(name, " => ZEC.ZEC ("+format+")") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("seed %d has no required ZEC swap with %s destination", seed, format)
			}
		}
	}
}
