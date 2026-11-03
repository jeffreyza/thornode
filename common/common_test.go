package common

import (
	"reflect"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type CommonSuite struct{}

var _ = Suite(&CommonSuite{})

func (s CommonSuite) TestGetUncappedShare(c *C) {
	part := cosmos.NewUint(149506590)
	total := cosmos.NewUint(50165561086)
	alloc := cosmos.NewUint(50000000)
	share := GetUncappedShare(part, total, alloc)
	c.Assert(share.Equal(cosmos.NewUint(149013)), Equals, true)
}

func (s CommonSuite) TestGetSafeShare(c *C) {
	part := cosmos.NewUint(14950659000000000)
	total := cosmos.NewUint(50165561086)
	alloc := cosmos.NewUint(50000000)
	share := GetSafeShare(part, total, alloc)
	c.Assert(share.Equal(cosmos.NewUint(50000000)), Equals, true)
}

func (s CommonSuite) TestQuorumTxEqualsIncludesAllowFutureObservation(c *C) {
	tx := ObservedTx{
		Tx: Tx{
			ID:    "tx1",
			Chain: BTCChain,
		},
	}
	qtx := &QuorumTx{ObsTx: tx, Inbound: false}
	futureQtx := &QuorumTx{ObsTx: tx, Inbound: false, AllowFutureObservation: true}

	c.Assert(qtx.Equals(futureQtx), Equals, false)
}

// TestObservedTxSignablePayloadFieldCoverage verifies that GetSignablePayload
// covers exactly the fields we expect. For every field on ObservedTx, the test
// either mutates it and asserts the payload changes (signature-covered) or
// mutates it and asserts the payload is unchanged (deliberately excluded).
//
// The reflection check at the bottom ensures any future field addition fails
// this test until a deliberate decision is recorded — preventing a silent
// recurrence of the KeysignMs regression where a per-observer field crept
// into the signed payload.
func (s CommonSuite) TestObservedTxSignablePayloadFieldCoverage(c *C) {
	limit := cosmos.NewUint(1000)
	base := ObservedTx{
		Tx: Tx{
			ID:          "tx1",
			Chain:       BTCChain,
			FromAddress: "from",
			ToAddress:   "to",
			Memo:        "memo",
		},
		Status:                Status_done,
		OutHashes:             []string{"out1"},
		BlockHeight:           100,
		Signers:               []string{"signer1"},
		ObservedPubKey:        "pubkey",
		KeysignMs:             250,
		FinaliseHeight:        106,
		Aggregator:            "agg",
		AggregatorTarget:      "target",
		AggregatorTargetLimit: &limit,
		SpentOutputRefs:       []OutputRef{{TxHash: "abc", OutputIndex: 0, KeyImage: "ki1", SpendTxHash: "sp1"}},
		XmrTxKey:              "abc123",
	}
	basePayload, err := base.GetSignablePayloadWithInbound(false)
	c.Assert(err, IsNil)

	// Fields that MUST be signature-covered. Mutating any of these must change
	// the signed payload, otherwise a proposer could mutate the wrapper after
	// observers signed.
	covered := map[string]func(*ObservedTx){
		"Tx":                    func(o *ObservedTx) { o.Tx.ID = "different" },
		"Status":                func(o *ObservedTx) { o.Status = Status_reverted },
		"OutHashes":             func(o *ObservedTx) { o.OutHashes = []string{"changed"} },
		"BlockHeight":           func(o *ObservedTx) { o.BlockHeight = 999 },
		"Signers":               func(o *ObservedTx) { o.Signers = []string{"changed"} },
		"ObservedPubKey":        func(o *ObservedTx) { o.ObservedPubKey = "changed" },
		"FinaliseHeight":        func(o *ObservedTx) { o.FinaliseHeight = base.BlockHeight },
		"Aggregator":            func(o *ObservedTx) { o.Aggregator = "changed" },
		"AggregatorTarget":      func(o *ObservedTx) { o.AggregatorTarget = "changed" },
		"AggregatorTargetLimit": func(o *ObservedTx) { l := cosmos.NewUint(2000); o.AggregatorTargetLimit = &l },
		"SpentOutputRefs":       func(o *ObservedTx) { o.SpentOutputRefs = []OutputRef{{TxHash: "different"}} },
		"XmrTxKey":              func(o *ObservedTx) { o.XmrTxKey = "def456" },
		"XmrMinedHeight":        func(o *ObservedTx) { o.XmrMinedHeight = 99 },
		// The attested durable-nonce update is committed on-chain, so it must be
		// signed: a proposer must not be able to swap the value observers attested.
		"SolNonce": func(o *ObservedTx) { o.SolNonce = &SolNonceUpdate{NonceAccount: "acct", Nonce: "nonce", Slot: 1} },
	}
	for name, mutate := range covered {
		mutated := base
		mutate(&mutated)
		got, errC := mutated.GetSignablePayloadWithInbound(false)
		c.Assert(errC, IsNil, Commentf("field %s", name))
		c.Assert(got, Not(DeepEquals), basePayload, Commentf("field %s must be signature-covered but mutating it did not change the payload", name))
	}

	// Fields explicitly excluded from the signed payload because they are
	// per-observer / non-deterministic.
	excluded := map[string]func(*ObservedTx){
		"KeysignMs": func(o *ObservedTx) { o.KeysignMs = 9999 },
	}
	for name, mutate := range excluded {
		mutated := base
		mutate(&mutated)
		got, errE := mutated.GetSignablePayloadWithInbound(false)
		c.Assert(errE, IsNil, Commentf("field %s", name))
		c.Assert(got, DeepEquals, basePayload, Commentf("field %s must be excluded from the signed payload but mutating it changed the payload", name))
	}

	// Inbound bit must be signature-covered: a proposer flipping QuorumTx.Inbound
	// must invalidate every attestation. Otherwise an inbound swap can be routed
	// through the outbound handler, which slashes the vault for the deposit.
	inboundTrue, err := base.GetSignablePayloadWithInbound(true)
	c.Assert(err, IsNil)
	c.Assert(inboundTrue, Not(DeepEquals), basePayload, Commentf("inbound bit must change the signed payload"))

	// Sanity: AttestTx delegates to ObservedTx.GetSignablePayloadWithInbound using
	// AttestTx.Inbound, and the canonical bytes must be independent of KeysignMs.
	withKeysignMs := AttestTx{ObsTx: base, Inbound: true}
	withoutKeysignMs := withKeysignMs
	withoutKeysignMs.ObsTx.KeysignMs = 0
	a, err := withKeysignMs.GetSignablePayload()
	c.Assert(err, IsNil)
	b, err := withoutKeysignMs.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(a, DeepEquals, b)
	// And matching the standalone call with the same inbound bit.
	c.Assert(a, DeepEquals, inboundTrue)
	// Flipping AttestTx.Inbound changes AttestTx.GetSignablePayload bytes.
	flipped := withKeysignMs
	flipped.Inbound = !withKeysignMs.Inbound
	flippedBz, err := flipped.GetSignablePayload()
	c.Assert(err, IsNil)
	c.Assert(flippedBz, Not(DeepEquals), a, Commentf("AttestTx.Inbound must be signature-covered"))

	// Force every ObservedTx field to be classified as covered or excluded.
	// Adding a new field without updating one of the maps above fails here.
	accountedFor := make(map[string]bool, len(covered)+len(excluded))
	for k := range covered {
		accountedFor[k] = true
	}
	for k := range excluded {
		accountedFor[k] = true
	}
	t := reflect.TypeOf(base)
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i).Name
		c.Assert(accountedFor[f], Equals, true,
			Commentf("ObservedTx field %q is not classified — decide whether it must be signature-covered or excluded and add it to the appropriate map in this test", f))
	}
}

func (s CommonSuite) TestXMRObservedTxPayloadIsNetworkBound(c *C) {
	observed := ObservedTx{Tx: Tx{ID: "tx1", Chain: XMRChain}}
	for _, inbound := range []bool{false, true} {
		chainnet, err := observed.getSignablePayloadWithInboundForNetwork(inbound, ChainNet)
		c.Assert(err, IsNil)
		mainnet, err := observed.getSignablePayloadWithInboundForNetwork(inbound, MainNet)
		c.Assert(err, IsNil)
		c.Assert(chainnet, Not(DeepEquals), mainnet)
	}

	// Other chains retain the established payload format.
	observed.Tx.Chain = BTCChain
	chainnet, err := observed.getSignablePayloadWithInboundForNetwork(false, ChainNet)
	c.Assert(err, IsNil)
	mainnet, err := observed.getSignablePayloadWithInboundForNetwork(false, MainNet)
	c.Assert(err, IsNil)
	c.Assert(chainnet, DeepEquals, mainnet)
}
