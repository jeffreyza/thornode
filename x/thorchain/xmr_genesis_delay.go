//go:build !mocknet
// +build !mocknet

package thorchain

// xmrMonovaultGenesisBootstrapDelayBlocks is how many blocks after genesis the
// first XMR monovault keygen is scheduled.
const xmrMonovaultGenesisBootstrapDelayBlocks int64 = 128
