//go:build !mocknet
// +build !mocknet

package tss

// frostKeygenTestRound3TimeoutMS is the production stub of the mocknet-only
// keygen fault-injection hook: always 0, meaning no override, so the round-3
// wait can only ever use the configured coordinator timeout. See
// frost_keygen_faultinject_mocknet.go for the simulation implementation.
func frostKeygenTestRound3TimeoutMS() int {
	return 0
}
