//go:build mocknet
// +build mocknet

package solana

import "time"

// init shortens the mocknet signer-cache window so simulations can retry
// dropped Solana broadcasts without waiting for the mainnet cache duration.
func init() {
	// Mocknet block production is much faster than mainnet, and simulation actors
	// time out quickly. Keep the same chain-side verification gate, but shorten
	// the unconditional cache window so dropped mocknet broadcasts can be retried.
	signerCacheExpiryOverride = 20 * time.Second
}
