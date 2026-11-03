// Regenerate bifrost/tss/go-tss/test_data/preParam_test.data with fresh
// LocalPreParams that satisfy tss-lib-private v3's tighter ValidateWithProof
// check (requires PaillierSK.P, PaillierSK.Q, Alpha, Beta, P, Q to be set).
// Run from repo root: go run ./tools/gen-preparams 4 > bifrost/tss/go-tss/test_data/preParam_test.data
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
)

func main() {
	n := 4
	if len(os.Args) > 1 {
		v, err := strconv.Atoi(os.Args[1])
		if err != nil || v <= 0 {
			fmt.Fprintf(os.Stderr, "usage: gen-preparams [n]\n")
			os.Exit(2)
		}
		n = v
	}

	for i := 0; i < n; i++ {
		start := time.Now()
		pp, err := keygen.GeneratePreParams(20 * time.Minute)
		if err != nil {
			fmt.Fprintf(os.Stderr, "party %d: %v\n", i, err)
			os.Exit(1)
		}
		if !pp.ValidateWithProof() {
			fmt.Fprintf(os.Stderr, "party %d: generated preParams failed ValidateWithProof\n", i)
			os.Exit(1)
		}
		bz, err := json.Marshal(pp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "party %d marshal: %v\n", i, err)
			os.Exit(1)
		}
		fmt.Println(hex.EncodeToString(bz))
		fmt.Fprintf(os.Stderr, "party %d generated in %s\n", i, time.Since(start))
	}
}
